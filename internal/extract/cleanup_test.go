package extract

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"rss-workshop/internal/model"
)

// cleanupPage has one story whose description carries the kind of blocks
// cleanup rules are for. The sanitizer would keep each of them, minus their
// class: it keeps id, text and links.
const cleanupPage = `<article><h2>Harbour reopens</h2><a href="/harbour">Read</a><time datetime="2026-09-01T12:00:00Z">Sept 1</time>
<div class="summary"><p>The harbour reopened today.</p><img src="/images/harbour.jpg" alt="">
<div class="share" id="share"><a href="https://social.example/share?u=harbour">Share</a></div>
<aside class="newsletter"><p>Sign up for our newsletter</p></aside>
<p>The post appeared first on Example News.</p>
<div class="related"><div class="related-item">Related: ferries</div></div>
<a class="tracking" href="/track">Track</a></div></article>`

func cleanupRecipe(typ string, rules ...string) model.Recipe {
	if typ == "xpath" {
		return model.Recipe{Type: typ, Items: "//article", Title: model.Field{Selector: ".//h2"}, Link: model.Field{Selector: ".//a[1]/@href"},
			Date: model.Field{Selector: ".//time/@datetime"}, Content: model.Field{Selector: ".//div[@class='summary']"}, Remove: rules}
	}
	return model.Recipe{Type: typ, Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"},
		Date: model.Field{Selector: "time", Attr: "datetime"}, Content: model.Field{Selector: ".summary"}, Remove: rules}
}

func runOne(t *testing.T, page string, r model.Recipe) (model.Item, []string) {
	t.Helper()
	p, err := Run([]byte(page), "https://example.com/news/", r)
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("items %+v, error %v", p.Items, err)
	}
	return p.Items[0], p.Warnings
}

func TestCleanupRulesRemoveBlocksFromDescriptions(t *testing.T) {
	for _, tc := range []struct {
		typ   string
		rules []string
	}{
		{"css", []string{".share", "aside.newsletter", `p:containsOwn("APPEARED FIRST ON")`, ".related"}},
		{"xpath", []string{".//div[@class='share']", ".//aside", ".//p[contains(., 'appeared first on')]", ".//div[@class='related']"}},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			plain, _ := runOne(t, cleanupPage, cleanupRecipe(tc.typ))
			cleaned, warnings := runOne(t, cleanupPage, cleanupRecipe(tc.typ, tc.rules...))
			for _, gone := range []string{"Share", "social.example", "newsletter", "appeared first on", "Related"} {
				if !strings.Contains(plain.HTML, gone) {
					t.Fatalf("the page lacks %q without rules: %s", gone, plain.HTML)
				}
				if strings.Contains(cleaned.HTML, gone) {
					t.Errorf("cleanup kept %q: %s", gone, cleaned.HTML)
				}
			}
			for _, kept := range []string{"<p>The harbour reopened today.</p>", `href="https://example.com/track"`} {
				if !strings.Contains(cleaned.HTML, kept) {
					t.Errorf("cleanup removed %q: %s", kept, cleaned.HTML)
				}
			}
			if len(warnings) != 0 {
				t.Errorf("warnings: %q", warnings)
			}
			// Nothing but the description changes: not the story's identity,
			// date or image.
			cleaned.HTML, plain.HTML = "", ""
			if cleaned != plain {
				t.Errorf("cleanup changed the story:\n%+v\n%+v", cleaned, plain)
			}
		})
	}
}

// A rule matches inside the selected block and can never remove the block
// itself, or anything outside it, nor an attribute that an XPath rule selects.
func TestCleanupRulesStayInsideTheBlock(t *testing.T) {
	page := `<article><h2>Story</h2><a href="/s">Read</a><div class="summary"><p>Kept <a href="/more">more</a></p></div><p class="outside">Outside</p></article>`
	for _, tc := range []struct {
		typ   string
		rules []string
	}{
		{"css", []string{":root", "article", "article > .summary", ".summary", "div"}},
		{"xpath", []string{"./self::*", "./..", "./ancestor-or-self::*", "./following::*", ".//a/@href", "./self::div[@class='summary']"}},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			plain, want := runOne(t, page, cleanupRecipe(tc.typ))
			for _, rule := range tc.rules {
				cleaned, warnings := runOne(t, page, cleanupRecipe(tc.typ, rule))
				if cleaned.HTML != plain.HTML || !slices.Equal(warnings, want) {
					t.Errorf("rule %s changed the description: %s (warnings %q)", rule, cleaned.HTML, warnings)
				}
			}
		})
	}
}

// XPath rules can select text, so a rule can drop a sentence without its
// element, and each rule runs on what the earlier ones left.
func TestCleanupRulesRemoveTextAndApplyInOrder(t *testing.T) {
	r := cleanupRecipe("xpath", ".//div[@class='related']/div", ".//div[@class='related'][not(*)]", ".//p/text()[contains(., 'appeared first')]")
	cleaned, _ := runOne(t, cleanupPage, r)
	if strings.Contains(cleaned.HTML, "related") || strings.Contains(cleaned.HTML, "Related") || strings.Contains(cleaned.HTML, "appeared first") {
		t.Fatalf("rules were not applied in order: %s", cleaned.HTML)
	}
	if !strings.Contains(cleaned.HTML, "<p></p>") {
		t.Fatalf("the paragraph whose text was removed is gone: %s", cleaned.HTML)
	}
}

// Rules see the page as it was, class attributes included, and sanitizing
// still comes last: it strips what a kept block carries.
func TestSanitizationRunsAfterCleanup(t *testing.T) {
	page := `<article><h2>Story</h2><a href="/s">Read</a><div class="summary"><p class="lead" onclick="steal()">Kept<script>alert(1)</script></p><iframe src="https://frame.example/"></iframe><div class="share">Share</div><a href="javascript:alert(1)">x</a></div></article>`
	cleaned, _ := runOne(t, page, cleanupRecipe("css", ".share"))
	if strings.Contains(cleaned.HTML, "Share") {
		t.Fatalf("the rule did not match a class the sanitizer removes: %s", cleaned.HTML)
	}
	for _, unsafe := range []string{"<script", "alert", "onclick", "class=", "<iframe", "javascript:"} {
		if strings.Contains(cleaned.HTML, unsafe) {
			t.Errorf("cleaned description kept %q: %s", unsafe, cleaned.HTML)
		}
	}
	body, err := Article([]byte(`<main class="body"><p class="lead" onclick="steal()">Kept<script>alert(1)</script></p><div class="share">Share</div></main>`), "https://example.com/s",
		model.Recipe{Type: "css", Full: &model.FullContent{Selector: ".body"}, Remove: []string{".share"}})
	if err != nil || body != "<p>Kept</p>" {
		t.Fatalf("article body %q, %v", body, err)
	}
}

// The story's image comes from the card as the page has it. A rule that
// removes it from the description moves it to the top, where it is shown once.
func TestCleanupKeepsTheStoryImageAndShowsItOnce(t *testing.T) {
	for _, typ := range []string{"css", "xpath"} {
		t.Run(typ, func(t *testing.T) {
			plain, _ := runOne(t, cleanupPage, cleanupRecipe(typ))
			rule := "img"
			if typ == "xpath" {
				rule = ".//img"
			}
			cleaned, _ := runOne(t, cleanupPage, cleanupRecipe(typ, rule))
			if plain.Image != "https://example.com/images/harbour.jpg" || cleaned.Image != plain.Image {
				t.Fatalf("image %q, without rules %q", cleaned.Image, plain.Image)
			}
			if strings.Count(cleaned.HTML, "<img") != 1 || !strings.HasPrefix(cleaned.HTML, `<p><img src="https://example.com/images/harbour.jpg" alt=""></p>`) {
				t.Fatalf("description: %s", cleaned.HTML)
			}
			if strings.Count(plain.HTML, "<img") != 1 || strings.HasPrefix(plain.HTML, "<p><img") {
				t.Fatalf("description without rules: %s", plain.HTML)
			}
		})
	}
}

// A description the rules leave with nothing to show is published empty, and
// the warning says so without quoting the text they removed. One that showed
// nothing before is kept as it always was.
func TestDescriptionEmptiedByCleanupIsReported(t *testing.T) {
	page := `<article><h2>Story</h2><a href="/s">Read</a><div class="summary"><div class="promo">Sponsored by ` + secretValue + `</div></div></article>
<article><h2>Blank</h2><a href="/b">Read</a><div class="summary"><span> </span></div></article>`
	r := cleanupRecipe("css", ".promo")
	r.Date = model.Field{}
	p, err := Run([]byte(page), "https://example.com/", r)
	if err != nil || len(p.Items) != 2 {
		t.Fatalf("items %+v, %v", p.Items, err)
	}
	if p.Items[0].HTML != "" {
		t.Errorf("emptied description: %q", p.Items[0].HTML)
	}
	if p.Items[1].HTML != "<div><span> </span></div>" {
		t.Errorf("a description that was blank before cleanup changed: %q", p.Items[1].HTML)
	}
	if len(p.Warnings) != 1 || p.Warnings[0] != "Match 1: cleanup removed the whole description; content omitted" {
		t.Fatalf("warnings: %q", p.Warnings)
	}
}

// Filters read the cleaned description, so a label that cleanup removes no
// longer excludes its story. Preview and refresh share this path.
func TestFiltersMatchTheCleanedDescription(t *testing.T) {
	r := cleanupRecipe("css")
	r.Filters = &model.FilterSet{Exclude: keywordRule("description", "newsletter")}
	p, err := Run([]byte(cleanupPage), "https://example.com/news/", r)
	if err != nil || len(p.Items) != 0 || p.Filtered != 1 {
		t.Fatalf("without rules: %+v, %v", p, err)
	}
	r.Remove = []string{".newsletter"}
	if p, err = Run([]byte(cleanupPage), "https://example.com/news/", r); err != nil || len(p.Items) != 1 || p.Filtered != 0 {
		t.Fatalf("with rules: %+v, %v", p, err)
	}
}

const cleanupArticle = `<html><body><nav>Menu</nav><main class="body"><h1>Harbour reopens</h1><p>The harbour reopened today.</p>
<div class="share"><a href="https://social.example/share">Share</a></div><aside class="newsletter">Sign up</aside></main></body></html>`

func TestCleanupRulesRemoveBlocksFromArticles(t *testing.T) {
	for _, tc := range []struct {
		typ, selector string
		rules         []string
	}{
		{"css", ".body", []string{".share", "aside", "h1"}},
		{"xpath", "//main", []string{".//div[@class='share']", ".//aside", "./h1"}},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			body, err := Article([]byte(cleanupArticle), "https://example.com/s", model.Recipe{Type: tc.typ, Full: &model.FullContent{Selector: tc.selector}, Remove: tc.rules})
			if err != nil || strings.TrimSpace(body) != "<p>The harbour reopened today.</p>" {
				t.Fatalf("article %q, %v", body, err)
			}
			// An attribute body is text, not markup, so rules leave it alone.
			attribute := model.Recipe{Type: tc.typ, Full: &model.FullContent{Selector: tc.selector, Attr: "data-body"}, Remove: tc.rules}
			body, err = Article([]byte(`<main class="body" data-body="&lt;aside&gt;Kept&lt;/aside&gt;"></main>`), "https://example.com/s", attribute)
			if err != nil || body != "<aside>Kept</aside>" {
				t.Fatalf("attribute article %q, %v", body, err)
			}
		})
	}
}

// An article the rules empty fails like any other failed article, so the
// story keeps its list-page description; storing it would replace the teaser
// with nothing. A body that showed nothing before cleanup, such as a video
// the sanitizer drops, is stored as it always was: failing it would refetch it
// on every refresh, rules or not.
func TestArticleEmptiedByCleanupFails(t *testing.T) {
	r := model.Recipe{Type: "css", Full: &model.FullContent{Selector: ".body"}, Remove: []string{".promo"}}
	for _, tc := range []struct {
		name, page, want string
		fails            bool
	}{
		{"emptied", `<div class="body"><div class="promo">Sponsored by ` + secretValue + `</div></div>`, "", true},
		{"image left", `<div class="body"><div class="promo">Sponsored</div><img src="/pic.jpg"></div>`, `<div><img src="https://example.com/pic.jpg"/></div>`, false},
		{"video before cleanup", `<div class="body"><video src="/clip.mp4"></video><div class="promo"> </div></div>`, "<div></div>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := Article([]byte(tc.page), "https://example.com/s", r)
			if tc.fails {
				if err == nil || err.Error() != "article body is empty after cleanup" || body != "" {
					t.Fatalf("body %q, error %v", body, err)
				}
				return
			}
			if err != nil || body != tc.want {
				t.Fatalf("body %q, error %v", body, err)
			}
		})
	}
}

// A cleanup rule is a selector like any other: antchfx/xpath panics on the
// same page content, and the panic is contained the same way. The error and
// the log name the rule by its number, never by its text or the page's.
func TestCleanupRulePanicsBecomeSelectorErrors(t *testing.T) {
	for _, rule := range []string{`.//p[@data-score > 1][contains(@id,'https://selector.example/')]`, `.//p[sum('x') > 0][contains(@id,'https://selector.example/')]`} {
		t.Run(rule, func(t *testing.T) {
			var log bytes.Buffer
			restore := slog.Default()
			defer slog.SetDefault(restore)
			slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))

			page := `<article><h2>Story</h2><a href="/s">x</a><div class="summary"><p data-score="` + secretValue + `">Text</p></div></article>`
			r := cleanupRecipe("xpath", ".//aside", rule)
			if err := Validate(r); err != nil {
				t.Fatalf("the rule must pass validation to reach evaluation: %v", err)
			}
			p, err := Run([]byte(page), "https://example.com/", r)
			if !errors.Is(err, ErrSelectorFailed) || errors.Is(err, ErrNoItems) || len(p.Items) != 0 {
				t.Fatalf("list page: %d items, %v", len(p.Items), err)
			}
			if !strings.Contains(err.Error(), "on the cleanup rule 2 selector") || !strings.Contains(err.Error(), "saved history is retained") {
				t.Fatalf("the error does not name the rule: %v", err)
			}
			article := model.Recipe{Type: "xpath", Full: &model.FullContent{Selector: "//main"}, Remove: r.Remove}
			body, articleErr := Article([]byte(`<main><p data-score="`+secretValue+`">Body</p></main>`), "https://example.com/s", article)
			if !errors.Is(articleErr, ErrSelectorFailed) || body != "" || !strings.Contains(articleErr.Error(), "on the cleanup rule 2 selector") {
				t.Fatalf("article %q, %v", body, articleErr)
			}
			for _, text := range []string{err.Error(), articleErr.Error(), log.String()} {
				for _, leaked := range []string{"source.example", "token=secret", "selector.example", "data-score"} {
					if strings.Contains(text, leaked) {
						t.Errorf("%q leaked into %s", leaked, text)
					}
				}
			}
			if got := strings.Count(log.String(), `selector="cleanup rule 2"`); got != 2 {
				t.Errorf("log names the rule %d times:\n%s", got, log.String())
			}
		})
	}
}

func TestCleanupRulesAreValidated(t *testing.T) {
	long := func(typ string, n int) string {
		if typ == "xpath" {
			return "./" + strings.Repeat("a", n-2)
		}
		return "." + strings.Repeat("a", n-1)
	}
	for _, typ := range []string{"css", "xpath"} {
		t.Run(typ, func(t *testing.T) {
			most := make([]string, MaxCleanupRules)
			for i := range most {
				most[i] = long(typ, 1000)
			}
			if err := Validate(cleanupRecipe(typ, most...)); err != nil {
				t.Fatalf("refused %d rules of 1000 characters: %v", MaxCleanupRules, err)
			}
			cases := map[string]struct {
				rules []string
				want  string
			}{
				"too many":   {append(most, long(typ, 10)), fmt.Sprintf("use at most %d cleanup rules", MaxCleanupRules)},
				"too long":   {[]string{long(typ, 10), long(typ, 1001)}, "cleanup rule 2 exceeds 1000 characters"},
				"blank":      {[]string{long(typ, 10), ""}, "cleanup rule 2 must be a selector without space around it"},
				"space":      {[]string{" " + long(typ, 10)}, "cleanup rule 1 must be a selector without space around it"},
				"no compile": {[]string{long(typ, 10), long(typ, 10), long(typ, 10) + "[["}, "cleanup rule 3: "},
			}
			if typ == "css" {
				cases["the block itself"] = struct {
					rules []string
					want  string
				}{[]string{"."}, "invalid CSS selector in cleanup rule 1"}
			} else {
				for name, rule := range map[string]string{"the block itself": ".", "a class left from CSS": ".share", "absolute": "//div[@class='share']", "parent": "..", "grouped": "(.//div)[1]"} {
					cases[name] = struct {
						rules []string
						want  string
					}{[]string{rule}, "XPath cleanup rule 1 must start with ./"}
				}
			}
			for name, tc := range cases {
				r := cleanupRecipe(typ, tc.rules...)
				err := Validate(r)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%s: got %v, want %q", name, err, tc.want)
				}
				// Extraction validates the recipe it is given too.
				if _, err := Run([]byte(cleanupPage), "https://example.com/", r); err == nil || errors.Is(err, ErrSelectorFailed) {
					t.Errorf("%s: extraction ran with an invalid rule: %v", name, err)
				}
			}
		})
	}
}
