package extract

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"testing"
	"unicode/utf8"

	"rss-workshop/internal/model"
)

func keywordRule(field string, terms ...string) *model.FilterRule {
	return &model.FilterRule{Op: "contains_any", Field: field, Keywords: terms}
}

func TestFiltersSeparateMatchesValidAndIncluded(t *testing.T) {
	body := []byte(`<article><h2>Linux tutorial</h2><a href="/news/1">Read</a></article>
<article><h2>Windows guide</h2><a href="/news/2">Read</a></article>
<article><h2>Linux sponsored post</h2><a href="/news/3">Read</a></article>
<article><h2>Duplicate identity</h2><a href="/news/1">Read</a></article>
<article><a href="/news/4">No title</a></article>
<article><h2>Linux without a link</h2></article>`)
	for _, typ := range []string{"css", "xpath"} {
		t.Run(typ, func(t *testing.T) {
			r := model.Recipe{Type: typ, Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Filters: &model.FilterSet{Include: keywordRule("title", "linux"), Exclude: keywordRule("title", "sponsored")}}
			if typ == "xpath" {
				r.Items, r.Title.Selector, r.Link.Selector = "//article", ".//h2", ".//a/@href"
			}
			p, err := Run(body, "https://example.com", r)
			if err != nil || p.Matches != 6 || p.Valid != 3 || p.Filtered != 2 || len(p.Items) != 1 || p.Valid != len(p.Items)+p.Filtered {
				t.Fatalf("bad filter counts: %v %+v", err, p)
			}
			if p.Items[0].Title != "Linux tutorial" || len(p.FilterExamples) != 2 || len(p.Warnings) != 2 {
				t.Fatal(p)
			}
			if !strings.Contains(p.FilterExamples[0].Reason, "include") || !strings.Contains(p.FilterExamples[1].Reason, "exclude") {
				t.Fatal(p.FilterExamples)
			}
			r.Filters = nil
			p, err = Run(body, "https://example.com", r)
			if err != nil || p.Valid != 3 || len(p.Items) != 3 || p.Filtered != 0 || len(p.FilterExamples) != 0 {
				t.Fatalf("unfiltered behavior changed: %v %+v", err, p)
			}
			encoded, err := json.Marshal(p)
			if err != nil || strings.Contains(string(encoded), "filter_examples") {
				t.Fatal("empty examples not omitted", err, string(encoded))
			}
		})
	}
}

func TestAllFilteredIsSuccessful(t *testing.T) {
	r := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Filters: &model.FilterSet{Include: keywordRule("title", "missing")}}
	p, err := Run([]byte(`<article><h2>First story</h2></article><article><h2>Second story</h2></article>`), "https://example.com", r)
	if err != nil || p.Valid != 2 || p.Filtered != 2 || len(p.Items) != 0 || p.Items == nil || len(p.FilterExamples) != 2 {
		t.Fatalf("intentional empty selection treated as extraction failure: %v %+v", err, p)
	}
	for _, body := range []string{`<div>No article elements</div>`, `<article>No title field</article>`} {
		p, err = Run([]byte(body), "https://example.com", r)
		if !errors.Is(err, ErrNoItems) || p.Valid != 0 || p.Filtered != 0 || len(p.FilterExamples) != 0 {
			t.Fatalf("invalid extraction treated as intentional filtering: %v %+v", err, p)
		}
	}
}

func TestFilterExamplesAreBounded(t *testing.T) {
	r := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Filters: &model.FilterSet{Include: keywordRule("title", "not present")}}
	var body strings.Builder
	for i := 0; i < 25; i++ {
		title := fmt.Sprintf("Headline %d", i)
		if i == 0 {
			title = strings.Repeat("世", 500)
		}
		if i == 1 {
			title = `<img src=x onerror="alert(1)">`
		}
		if i == 2 {
			title = "Read https://user:password@private.example/story?token=hidden"
		}
		fmt.Fprintf(&body, `<article><h2>%s</h2><a href="/%d">Read</a></article>`, html.EscapeString(title), i)
	}
	p, err := Run([]byte(body.String()), "https://example.com", r)
	if err != nil || p.Valid != 25 || p.Filtered != 25 || len(p.FilterExamples) != 20 {
		t.Fatal(p, err)
	}
	for _, example := range p.FilterExamples {
		if !utf8.ValidString(example.Title) || utf8.RuneCountInString(example.Title) > 120 || len(example.Reason) > 512 || example.Reason == "" {
			t.Fatalf("unbounded example: %+v", example)
		}
	}
	if !strings.HasSuffix(p.FilterExamples[0].Title, "…") || p.FilterExamples[1].Title != `<img src=x onerror="alert(1)">` {
		t.Fatal(p.FilterExamples[:2])
	}
	for _, secret := range []string{"password", "private.example", "hidden"} {
		if strings.Contains(p.FilterExamples[2].Title, secret) {
			t.Fatal("URL leaked into filter example", p.FilterExamples[2])
		}
	}
}

func TestFilterRulesAreValidatedBeforeExtraction(t *testing.T) {
	r := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Filters: &model.FilterSet{Include: &model.FilterRule{Op: "invalid"}}}
	if err := Validate(r); err == nil {
		t.Fatal("invalid filter recipe accepted")
	}
	p, err := Run([]byte(`<article><h2>Story</h2></article>`), "https://example.com", r)
	if err == nil || errors.Is(err, ErrNoItems) || p.Matches != 0 {
		t.Fatalf("filter validation ran after extraction: %v %+v", err, p)
	}
}

func TestFiltersCannotBypassExtractionSizeLimits(t *testing.T) {
	r := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Content: model.Field{Selector: "section"}, Filters: &model.FilterSet{Include: keywordRule("title", "never matches")}}
	body := `<article><h2>Story</h2><section>` + strings.Repeat("x", 256<<10) + `</section></article>`
	p, err := Run([]byte(body), "https://example.com", r)
	if err == nil || !strings.Contains(err.Error(), "item exceeds size limits") || p.Valid != 0 {
		t.Fatal("excluded large item bypassed size check", p, err)
	}
	var total strings.Builder
	for i := 0; i < 33; i++ {
		fmt.Fprintf(&total, `<article><h2>Story %d</h2><section>%s</section></article>`, i, strings.Repeat("x", 250<<10))
	}
	p, err = Run([]byte(total.String()), "https://example.com", r)
	if err == nil || !strings.Contains(err.Error(), "8 MiB limit") || p.Filtered != p.Valid {
		t.Fatal("excluded items bypassed total size check", p, err)
	}
}

func TestFiltersUseExtractedDescriptionAndResolvedLink(t *testing.T) {
	r := model.Recipe{Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"}, Link: model.Field{Selector: ".//a/@href"}, Content: model.Field{Selector: ".//section"}, Filters: &model.FilterSet{Include: &model.FilterRule{Op: "all", Rules: []model.FilterRule{*keywordRule("description", "cloud security"), *keywordRule("link", "https://example.com/news/")}}, Exclude: keywordRule("description", "invisible")}}
	body := []byte(`<article><h2>Story</h2><a href="/news/one">Read</a><section><p>Cloud<br>security</p><script>invisible</script></section></article>`)
	p, err := Run(body, "https://example.com", r)
	if err != nil || p.Valid != 1 || p.Filtered != 0 || len(p.Items) != 1 {
		t.Fatal(p, err)
	}
}
