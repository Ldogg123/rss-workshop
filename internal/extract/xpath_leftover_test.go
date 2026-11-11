package extract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rss-workshop/internal/diagnostics"
	"rss-workshop/internal/model"
)

// leftoverPage has one card inside a page with navigation, and serves as its
// own article page.
const leftoverPage = `<html><body><nav>Site navigation</nav><div class="post"><article><h2>Headline</h2><a href="/one">more</a>` +
	`<p class="teaser-text">Summary</p><div>Share this</div></article></div><div class="entry-body"><p>Article text</p></div></body></html>`

func leftoverRecipe() model.Recipe {
	return model.Recipe{Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"}, Link: model.Field{Selector: ".//a/@href"}}
}

// The XPath library stops reading at the first complete expression, so each
// of these compiles and runs as its beginning: "." for a CSS class selector.
// The valid forms beside them are the ones the tests, the guides and Convert
// to XPath write, and none of them may be reported.
func TestUnfinishedXPathIsToldFromCompleteXPath(t *testing.T) {
	for _, s := range []string{
		".summary", ".entry-content", ".post", ".teaser-text", ". summary", ".card > h2", ".card h2", ".a:not(.b)", ".card, .teaser",
		"//a garbage", ".//h2 h3", "article .share", ".//a, .//b", ".//h2 .//h3", "//article[@class='post'] //h2 x",
	} {
		r := leftoverRecipe()
		r.Items = s
		if err := Validate(r); err != nil {
			t.Errorf("%q no longer passes validation, so stored recipes holding it would stop refreshing: %v", s, err)
		}
		if !unfinishedXPath(s) {
			t.Errorf("%q is not reported, and runs as its first expression only", s)
		}
	}
	for _, s := range []string{
		"", ".", "..", "/", "//article", ".//h2", ".//a/@href", ".//a[1]/@href", ".//h2/text()", ".//time/@datetime", "./self::article/p",
		"(//a)[1]", "(.//p)[1]", "(.//div)[1]", ".//img | .//h3", ".//h2 | //aside", "//article[contains(@class,'story')]",
		".//h2[@class='story__title']/a/@href", ".//p[@class='story__standfirst']", ".//a[contains(@href, 'two')]/@href",
		".//h2[contains(text(), 'First')]/text()", ".//p[contains(., 'appeared first on')]", ".//p/text()[contains(., 'Sponsored')]",
		".//div[@class='related'][not(*)]", ".//a/ancestor::body//h2", "//div[@class=\"article-body\"]", "//meta/@content",
		".//*[number(@data-score) > 1]", ".//li[position() < 3 and not(@hidden)]", ".//a[starts-with(@href, '/news/') or @rel='bookmark']",
		".//span[string-length(normalize-space(.)) >= 2]", ".//td[count(preceding-sibling::td) = 1]", "  .//h2  ", "( .//h2 )",
		".//a[@title=concat('It', \"'\", 's')]", ".//p[. = 'a) b']", ".//p[contains(., '.summary h2')]",
		// Convert to XPath: a class test, the article selector, a cleanup
		// rule that can name its own block, a list, and the guide's path
		// for a list of article selectors.
		"//article[" + class("card") + "]", ".//aside[" + class("related") + "]",
		"./self::article//*[" + class("share") + "] | .//article//*[" + class("share") + "]",
		"./self::*[" + class("nested") + "]//p | .//*[" + class("nested") + "]//p | ./self::article//blockquote | .//article//blockquote",
		"//*[self::article or " + class("entry-content") + "]",
		// CSS that is also one XPath name: it reads as an element name, so
		// nothing is cut short, and it matches nothing. The guide says so.
		"article.card", "article.content", "div.a.b",
	} {
		if unfinishedXPath(s) {
			t.Errorf("%q is complete XPath and is reported as unfinished", s)
		}
	}

	// Every XPath recipe the repository ships passes untouched.
	files, err := filepath.Glob("../../docs/examples/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no example recipes: %v", err)
	}
	checked := 0
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Feeds []struct {
				Title  string       `json:"title"`
				Recipe model.Recipe `json:"recipe"`
			} `json:"feeds"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, f := range doc.Feeds {
			if f.Recipe.Type != "xpath" {
				continue
			}
			checked++
			if w := SelectorWarnings(f.Recipe); len(w) != 0 {
				t.Errorf("%s, %s: %q", filepath.Base(path), f.Title, w)
			}
			if err := ValidateNew(f.Recipe, nil); err != nil {
				t.Errorf("%s, %s could not be saved as a new feed: %v", filepath.Base(path), f.Title, err)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no shipped example uses XPath")
	}
}

// A stored recipe with such a selector keeps extracting exactly what it
// extracted before, the whole card here, because failing it would back off
// and alert on a feed that publishes today. What changes is the warning,
// which names the field and never quotes the selector.
func TestRunStillExtractsWithAnUnfinishedSelectorAndWarns(t *testing.T) {
	whole := leftoverRecipe()
	whole.Content.Selector = "."
	want, plain := runOne(t, leftoverPage, whole)
	if len(plain) != 0 || !strings.Contains(want.HTML, "Share this") {
		t.Fatalf("the card selector published %q with warnings %q", want.HTML, plain)
	}
	r := leftoverRecipe()
	r.Content.Selector = ".teaser-text"
	got, warnings := runOne(t, leftoverPage, r)
	if got.HTML != want.HTML || got.Key != want.Key || got.Title != want.Title {
		t.Fatalf("a stored recipe's output changed: %+v, want %+v", got, want)
	}
	if len(warnings) != 1 || !strings.HasPrefix(warnings[0], "Description selector: XPath uses only its first complete expression") ||
		!strings.Contains(warnings[0], "selects the whole card") || strings.Contains(warnings[0], "teaser-text") {
		t.Fatalf("warnings %q", warnings)
	}

	// Not only "." is cut short: this title is the h2, and a stray closing
	// parenthesis, which the check cannot see, leaves the paragraph.
	r = leftoverRecipe()
	r.Title.Selector, r.Content.Selector = ".//h2 h3", ".//p) x"
	got, warnings = runOne(t, leftoverPage, r)
	if got.Title != "Headline" || got.HTML != "<p>Summary</p>" || len(warnings) != 1 || !strings.HasPrefix(warnings[0], "Title selector: ") {
		t.Fatalf("item %+v, warnings %q", got, warnings)
	}

	// Every selector is checked and reported in the editor's order.
	r = leftoverRecipe()
	r.Items, r.Title.Selector, r.Link.Selector, r.Image.Selector, r.Date.Selector = ".post", ".headline", "", ".thumb", ".date"
	r.Full = &model.FullContent{Selector: ".entry-body"}
	p, err := Run([]byte(leftoverPage), "https://example.com/news/", r)
	if err != nil || p.Matches != 1 || len(p.Items) != 1 || !strings.Contains(p.Items[0].Title, "Site navigation") {
		t.Fatalf("the whole page was not read as one item, as before: %+v, %v", p, err)
	}
	var names []string
	for _, w := range p.Warnings {
		if name, _, found := strings.Cut(w, " selector: XPath uses only its first complete expression"); found {
			names = append(names, name)
		}
	}
	if got := strings.Join(names, "|"); got != "Repeated item|Title|Image|Date|Article body" {
		t.Fatalf("warned about %q in %q", got, p.Warnings)
	}
	if !strings.Contains(p.Warnings[0], "makes the whole page one item") || !strings.Contains(p.Warnings[4], "selects the whole page") {
		t.Fatalf("warnings %q", p.Warnings)
	}
	body, err := Article([]byte(leftoverPage), "https://example.com/one", r)
	if err != nil || !strings.Contains(body, "Site navigation") || !strings.Contains(body, "Article text") {
		t.Fatalf("the article body is no longer the whole page, as before: %q, %v", body, err)
	}

	// Diagnostics stores a warning as written: within its length limit, and
	// with nothing its redaction would rewrite. A selector holding a URL
	// stays out of it.
	r = leftoverRecipe()
	r.Content.Selector = ".//a[@href='https://user:secret@example.com/private?token=hidden'] .teaser"
	p, err = Run([]byte(leftoverPage), "https://example.com/news/", r)
	if err != nil || len(p.Warnings) == 0 {
		t.Fatalf("%+v, %v", p, err)
	}
	for _, w := range append(p.Warnings[:1:1], SelectorWarnings(model.Recipe{Type: "xpath", Items: ".post", Full: &model.FullContent{Selector: ".entry-body"}})...) {
		if diagnostics.SafeText(w, 512) != w || strings.Contains(w, "example.com") || strings.Contains(w, "secret") || strings.Contains(w, "hidden") {
			t.Errorf("warning is not stored as written, or quotes the selector: %q", w)
		}
	}
}

// A save refuses such a selector only when the save introduces it. The type
// menu keeps the existing text, so a recipe that just became XPath is all
// new; a stored selector saved as it is still passes. Selectors of the other
// types are not XPath, whatever they look like.
func TestValidateNewRefusesOnlyWhatTheSaveIntroduces(t *testing.T) {
	stored := leftoverRecipe()
	stored.Content.Selector = ".teaser-text"
	stored.Full = &model.FullContent{Selector: ".entry-body"}
	edit := func(change func(*model.Recipe)) model.Recipe {
		r := stored
		full := *stored.Full
		r.Full = &full
		change(&r)
		return r
	}
	storedAs := func(change func(*model.Recipe)) *model.Recipe {
		r := edit(change)
		return &r
	}
	css := model.Recipe{Type: "css", Items: ".post", Title: model.Field{Selector: ".headline"}, Content: model.Field{Selector: ".teaser-text"}}
	asXPath := css
	asXPath.Type = "xpath"
	for _, tc := range []struct {
		name   string
		recipe model.Recipe
		stored *model.Recipe
		want   string
	}{
		{"new feed", stored, nil, "invalid XPath in the Description selector: XPath uses only its first complete expression"},
		{"new feed, article only", edit(func(r *model.Recipe) { r.Content.Selector = ".//p" }), nil, "invalid XPath in the Article body selector"},
		{"unchanged", stored, &stored, ""},
		{"unrelated edit", edit(func(r *model.Recipe) {
			r.Title.Selector, r.UserAgent, r.Remove = ".//h3", "Reader/1", []string{".//aside"}
		}), &stored, ""},
		{"repaired", edit(func(r *model.Recipe) { r.Content.Selector, r.Full.Selector = ".//p", "//div[@class='entry-body']" }), &stored, ""},
		{"article turned off", edit(func(r *model.Recipe) { r.Full = nil }), &stored, ""},
		{"changed text", edit(func(r *model.Recipe) { r.Content.Selector = ".teaser" }), &stored, "invalid XPath in the Description selector"},
		{"changed article text", edit(func(r *model.Recipe) { r.Full.Selector = ".entry" }), &stored, "invalid XPath in the Article body selector"},
		// The editor trims the article body field, and a recipe file can have
		// stored it with space around it: the same selector, not a new one.
		{"article text trimmed by the editor", stored, storedAs(func(r *model.Recipe) { r.Full.Selector = " .entry-body\n" }), ""},
		{"space added around stored text", edit(func(r *model.Recipe) { r.Content.Selector, r.Full.Selector = ".teaser-text\n", "\t.entry-body " }), &stored, ""},
		{"changed article text stored with space", edit(func(r *model.Recipe) { r.Full.Selector = ".entry" }),
			storedAs(func(r *model.Recipe) { r.Full.Selector = ".entry-body\n" }), "invalid XPath in the Article body selector"},
		{"moved to another field", edit(func(r *model.Recipe) { r.Content.Selector, r.Date.Selector = "", ".teaser-text" }), &stored, "invalid XPath in the Date selector"},
		{"another field added", edit(func(r *model.Recipe) { r.Image.Selector = ".thumb" }), &stored, "invalid XPath in the Image selector"},
		{"article added", stored, storedAs(func(r *model.Recipe) { r.Full = nil }), "invalid XPath in the Article body selector"},
		{"type changed to XPath", asXPath, &css, "invalid XPath in the Repeated item selector"},
		{"stored as XPath", asXPath, &asXPath, ""},
		{"complete XPath over stored CSS", leftoverRecipe(), &css, ""},
		{"complete XPath with a cleanup rule", cleanupRecipe("xpath", ".//div[@class='share']"), nil, ""},
		{"CSS", edit(func(r *model.Recipe) { *r = css; r.Full = &model.FullContent{Selector: ".entry-body"} }), nil, ""},
		{"CSS over stored XPath", css, &stored, ""},
		{"feed source", model.Recipe{Type: FeedType, Full: &model.FullContent{Selector: ".entry-body"}, Remove: []string{".share"}}, nil, ""},
	} {
		if err := Validate(tc.recipe); err != nil {
			t.Fatalf("%s: validation refuses what v1.3.0 stored and exported: %v", tc.name, err)
		}
		// A new feed is refused exactly when it would be warned about.
		if warned, refused := len(SelectorWarnings(tc.recipe)) > 0, ValidateNew(tc.recipe, nil) != nil; warned != refused {
			t.Errorf("%s: warned about %v, refused as a new feed %v", tc.name, warned, refused)
		}
		err := ValidateNew(tc.recipe, tc.stored)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.want)):
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.want)
		case err != nil && (strings.Contains(err.Error(), "teaser") || strings.Contains(err.Error(), ".entry-body") || strings.Contains(err.Error(), ".thumb")):
			t.Errorf("%s: the error quotes the selector: %v", tc.name, err)
		}
	}
}
