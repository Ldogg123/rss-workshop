package filter

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"rss-workshop/internal/model"
)

func leaf(op, field string, keywords ...string) model.FilterRule {
	return model.FilterRule{Op: op, Field: field, Keywords: keywords}
}

func group(op string, rules ...model.FilterRule) model.FilterRule {
	return model.FilterRule{Op: op, Rules: rules}
}

func mustCompile(t *testing.T, set *model.FilterSet) *Matcher {
	t.Helper()
	m, err := Compile(set)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNestedIncludeAndExclude(t *testing.T) {
	include := group("all", leaf("contains_any", "title", "Golang", "Postgres"),
		group("any", leaf("contains_all", "description", "self hosted", "news"), leaf("contains_any", "link", "/guides/")))
	exclude := group("any", leaf("contains_any", "title", "sponsored"), leaf("contains_all", "description", "affiliate", "buy now"))
	m := mustCompile(t, &model.FilterSet{Include: &include, Exclude: &exclude})
	for _, tc := range []struct {
		name string
		item model.Item
		want bool
	}{
		{"description branch", model.Item{Title: "GOLANG workshop", HTML: "<p>Self hosted <em>news</em></p>"}, true},
		{"link branch", model.Item{Title: "Postgres workshop", URL: "https://example.com/GUIDES/database"}, true},
		{"missing outer condition", model.Item{Title: "Different workshop", URL: "https://example.com/guides/database"}, false},
		{"missing nested alternatives", model.Item{Title: "Golang workshop", HTML: "<p>Self hosted calendar</p>"}, false},
		{"excluded title", model.Item{Title: "Sponsored Golang workshop", URL: "https://example.com/guides/database"}, false},
		{"excluded all keywords", model.Item{Title: "Postgres workshop", URL: "https://example.com/guides/database", HTML: "<p>Affiliate offer, BUY NOW</p>"}, false},
		{"only one exclude keyword", model.Item{Title: "Postgres workshop", URL: "https://example.com/guides/database", HTML: "<p>Affiliate policy</p>"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision := m.Evaluate(tc.item)
			if m.Match(tc.item) != tc.want || decision.Matched != tc.want {
				t.Fatalf("unexpected decision: %+v", decision)
			}
			if tc.want && decision.Reason != "" || !tc.want && decision.Reason == "" {
				t.Fatalf("incorrect explanation: %+v", decision)
			}
		})
	}
}

func TestLiteralTextAndUnicode(t *testing.T) {
	for _, tc := range []struct {
		name, keyword, title string
		want                 bool
	}{
		{"literal punctuation", "c++", "C++ release", true},
		{"no regular expressions", "a.b", "axb", false},
		{"substring", "rust", "Trust matters", true},
		{"whitespace phrase", "  cloud\tsecurity  ", "Cloud\n\u00a0security update", true},
		{"accented case", "CAFÉ", "café news", true},
		{"sigma", "Σ", "final ς", true},
		{"Kelvin", "k", "K", true},
		{"long s", "S", "ſ", true},
		{"sharp s", "ß", "ẞ", true},
		{"no expansion folding", "ss", "ß", false},
		{"no accent removal", "cafe", "café", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := leaf("contains_any", "title", tc.keyword)
			m := mustCompile(t, &model.FilterSet{Include: &r})
			if got := m.Match(model.Item{Title: tc.title}); got != tc.want {
				t.Fatalf("match=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestDescriptionUsesVisibleText(t *testing.T) {
	body := `<html><head><title>headsecret</title></head><body><script>scriptsecret</script><style>.stylesecret{}</style><template>templatesecret</template><!-- commentsecret --><p>Cloud<br>security</p><p>news<em>letter</em> &amp; updates.</p><div>First</div><div>Second</div><a href="https://attributesecret.example">Read</a><img alt="altsecret"></body></html>`
	for _, tc := range []struct {
		keyword string
		want    bool
	}{
		{"cloud security", true}, {"newsletter & updates.", true}, {"first second", true}, {"firstsecond", false},
		{"headsecret", false}, {"scriptsecret", false}, {"stylesecret", false}, {"templatesecret", false}, {"commentsecret", false}, {"attributesecret", false}, {"altsecret", false},
	} {
		t.Run(tc.keyword, func(t *testing.T) {
			r := leaf("contains_any", "description", tc.keyword)
			m := mustCompile(t, &model.FilterSet{Include: &r})
			if got := m.Match(model.Item{HTML: body}); got != tc.want {
				t.Fatalf("match=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestEmptyFiltersAndOptionalSides(t *testing.T) {
	item := model.Item{Title: "Story"}
	for _, set := range []*model.FilterSet{nil, {}} {
		m := mustCompile(t, set)
		if !m.Match(item) || !m.Evaluate(item).Matched {
			t.Fatal("empty filter changed existing behavior")
		}
	}
	r := leaf("contains_any", "title", "story")
	if mustCompile(t, &model.FilterSet{Exclude: &r}).Match(item) {
		t.Fatal("exclude-only rule ignored")
	}
	if !mustCompile(t, &model.FilterSet{Include: &r}).Match(item) {
		t.Fatal("include-only rule rejected matching item")
	}
}

func TestOneHundredAndFiveHundredKeywords(t *testing.T) {
	for _, count := range []int{100, 500} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			terms := make([]string, count)
			for i := range terms {
				terms[i] = fmt.Sprintf("keyword-%03d-end", i)
			}
			r := leaf("contains_any", "title", terms...)
			m := mustCompile(t, &model.FilterSet{Include: &r})
			if !m.Match(model.Item{Title: strings.ToUpper(terms[count-1])}) || m.Match(model.Item{Title: "no keyword"}) {
				t.Fatal("large any list mismatch")
			}
			r.Op = "contains_all"
			m = mustCompile(t, &model.FilterSet{Include: &r})
			if !m.Match(model.Item{Title: strings.Join(terms, " ")}) || m.Match(model.Item{Title: strings.Join(terms[:count-1], " ")}) {
				t.Fatal("large all list mismatch")
			}
		})
	}
}

func TestMalformedRulesAndLimits(t *testing.T) {
	valid := leaf("contains_any", "title", "news")
	deep := valid
	for i := 1; i < MaxDepth; i++ {
		deep = group("all", deep)
	}
	if _, err := Compile(&model.FilterSet{Include: &deep}); err != nil {
		t.Fatal("maximum depth rejected", err)
	}
	tooDeep := group("any", deep)
	full := group("any", make([]model.FilterRule, MaxNodes-1)...)
	for i := range full.Rules {
		full.Rules[i] = valid
	}
	if _, err := Compile(&model.FilterSet{Include: &full}); err != nil {
		t.Fatal("maximum node count rejected", err)
	}
	tooManyNodes := group("any", append(append([]model.FilterRule{}, full.Rules...), valid)...)
	longValid := leaf("contains_any", "title", strings.Repeat("世", MaxKeywordCharacters))
	if _, err := Compile(&model.FilterSet{Include: &longValid}); err != nil {
		t.Fatal("Unicode character limit counted bytes", err)
	}
	tooManyTerms := leaf("contains_any", "title", make([]string, MaxKeywords+1)...)
	for i := range tooManyTerms.Keywords {
		tooManyTerms.Keywords[i] = "term"
	}
	for _, tc := range []struct {
		name string
		rule model.FilterRule
	}{
		{"unknown operator", model.FilterRule{Op: "regex"}},
		{"unknown field", leaf("contains_any", "image", "news")},
		{"empty group", group("all")},
		{"group field", model.FilterRule{Op: "all", Rules: []model.FilterRule{valid}, Field: "title"}},
		{"group keywords", model.FilterRule{Op: "any", Rules: []model.FilterRule{valid}, Keywords: []string{}}},
		{"leaf children", model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"news"}, Rules: []model.FilterRule{}}},
		{"empty keywords", leaf("contains_any", "title")},
		{"blank keyword", leaf("contains_any", "title", " \t\u00a0")},
		{"invalid Unicode", leaf("contains_any", "title", string([]byte{0xff}))},
		{"long keyword", leaf("contains_any", "title", strings.Repeat("世", MaxKeywordCharacters+1))},
		{"too deep", tooDeep}, {"too many nodes", tooManyNodes}, {"too many keywords", tooManyTerms},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Compile(&model.FilterSet{Include: &tc.rule}); err == nil {
				t.Fatal("invalid rule accepted")
			}
		})
	}
	if _, err := Compile(&model.FilterSet{Include: &full, Exclude: &valid}); err == nil {
		t.Fatal("node limit not shared across roots")
	}
	fullTerms := tooManyTerms
	fullTerms.Keywords = fullTerms.Keywords[:MaxKeywords]
	if _, err := Compile(&model.FilterSet{Include: &fullTerms, Exclude: &valid}); err == nil {
		t.Fatal("keyword limit not shared across roots")
	}
}

func TestCompiledMatcherIsDetachedAndConcurrent(t *testing.T) {
	r := group("all", leaf("contains_any", "title", "news"))
	m := mustCompile(t, &model.FilterSet{Include: &r})
	r.Rules[0].Keywords[0] = "changed"
	r.Rules[0].Field = "description"
	r.Op = "any"
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if !m.Match(model.Item{Title: "NEWS"}) || m.Match(model.Item{Title: "changed"}) {
					t.Error("compiled filter changed or leaked item state")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestDecisionsDoNotCopySourceOrKeywords(t *testing.T) {
	r := leaf("contains_any", "link", "private-keyword")
	m := mustCompile(t, &model.FilterSet{Include: &r})
	d := m.Evaluate(model.Item{URL: "https://private-source.example/?token=secret"})
	if d.Matched || len(d.Reason) == 0 || len(d.Reason) > 512 {
		t.Fatal(d)
	}
	for _, secret := range []string{"private-keyword", "private-source", "secret"} {
		if strings.Contains(d.Reason, secret) {
			t.Fatal("source/keyword leaked into reason", d)
		}
	}
}

func BenchmarkMatcherKeywords(b *testing.B) {
	for _, count := range []int{100, 500} {
		for _, field := range []string{"title", "description"} {
			b.Run(fmt.Sprintf("%d/%s", count, field), func(b *testing.B) {
				terms := make([]string, count)
				for i := range terms {
					terms[i] = fmt.Sprintf("missing-keyword-%03d", i)
				}
				r := leaf("contains_any", field, terms...)
				m, err := Compile(&model.FilterSet{Include: &r})
				if err != nil {
					b.Fatal(err)
				}
				item := model.Item{Title: "A normal news headline", HTML: strings.Repeat("<p>A normal paragraph about software releases.</p>", 160)}
				b.ReportAllocs()
				for b.Loop() {
					m.Match(item)
				}
			})
		}
	}
}
