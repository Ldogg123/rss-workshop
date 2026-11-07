package extract

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"rss-workshop/internal/model"
)

// goldenPage covers what extraction does to a description apart from
// selecting it: sanitizing, absolutizing, the story image prepended unless the
// description already shows it, attribute descriptions, a description the
// sanitizer empties, and invisible markup that is still stored.
const goldenPage = `<!doctype html><html><head><base href="/news/"></head><body>
<article class="card"><h2>Share bar</h2><a href="one">Read</a><time datetime="2026-09-01T12:00:00Z">Sept 1</time><div class="summary"><p>Kept <b>text</b> <a href="../about" class="more">About</a></p><div class="share" id="share-1"><a href="https://social.example/share?u=1">Share</a></div><img src="/images/one.jpg" alt=""></div></article>
<article class="card"><h2>Image outside</h2><a href="two">Read</a><img data-src="/images/two.jpg" src="data:image/gif;base64,AAAA"><div class="summary"><p>Text only</p><script>alert(1)</script></div></article>
<article class="card"><h2>Invisible</h2><a href="three">Read</a><div class="summary"><div><span> </span></div></div></article>
<article class="card"><h2>Sanitized away</h2><a href="four">Read</a><iframe class="summary" src="https://frame.example/"></iframe></article>
<article class="card" data-teaser="&lt;p&gt;From an attribute&lt;/p&gt;"><h2>Attribute</h2><a href="five">Read</a></article>
</body></html>`

const goldenArticle = `<!doctype html><html><head><base href="https://example.com/news/"></head><body>
<nav><a href="/">Home</a></nav>
<main class="body" id="main" data-body="&lt;p&gt;Body from an attribute&lt;/p&gt;"><h1>Headline</h1><p onclick="x()">Body <a href="rel.html">link</a></p><div class="share">Share this</div><img src="pic.jpg"><script>alert(1)</script></main>
<div class="invisible"><div><span> </span></div></div>
<iframe class="framed" src="https://frame.example/"></iframe>
</body></html>`

// goldenObserved fixes the observation time, so undated stories and relative
// dates are reproducible.
var goldenObserved = time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)

func goldenRecipes() map[string]model.Recipe {
	css := model.Recipe{Type: "css", Items: "article.card", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"},
		Date: model.Field{Selector: "time"}, Content: model.Field{Selector: ".summary"}}
	xpath := model.Recipe{Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"}, Link: model.Field{Selector: ".//a[1]/@href"},
		Date: model.Field{Selector: ".//time/@datetime"}, Content: model.Field{Selector: ".//*[@class='summary']"}, Image: model.Field{Selector: ".//img"}}
	attribute := css
	attribute.Content = model.Field{Selector: ".", Attr: "data-teaser"}
	return map[string]model.Recipe{"css": css, "xpath": xpath, "attribute": attribute}
}

func goldenArticles() map[string]model.Recipe {
	return map[string]model.Recipe{
		"css":       {Type: "css", Full: &model.FullContent{Selector: ".body"}},
		"xpath":     {Type: "xpath", Full: &model.FullContent{Selector: "//main[@id='main']"}},
		"attribute": {Type: "css", Full: &model.FullContent{Selector: ".body", Attr: "data-body"}},
		"invisible": {Type: "css", Full: &model.FullContent{Selector: ".invisible"}},
		"sanitized": {Type: "css", Full: &model.FullContent{Selector: ".framed"}},
	}
}

type goldenResult struct {
	Preview *model.Preview `json:"preview,omitempty"`
	Article *string        `json:"article,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// goldenOutput runs every golden case. A recipe without cleanup rules must
// extract exactly what it did before rules existed, or every feed would
// publish changed bytes after the upgrade.
func goldenOutput(t *testing.T) []byte {
	t.Helper()
	cards, err := os.ReadFile("../../testdata/cards.html")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]goldenResult{}
	for name, r := range goldenRecipes() {
		for page, body := range map[string][]byte{"cards": cards, "edge": []byte(goldenPage)} {
			p, err := RunAt(body, "https://example.com/news/page", r, goldenObserved)
			result := goldenResult{Preview: &p}
			if err != nil {
				result.Error = err.Error()
			}
			out["list "+page+" "+name] = result
		}
	}
	for name, r := range goldenArticles() {
		body, err := Article([]byte(goldenArticle), "https://example.com/news/story", r)
		result := goldenResult{Article: &body}
		if err != nil {
			result.Error = err.Error()
		}
		out["article "+name] = result
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

// testdata/no_cleanup_rules.golden.json is the output from before cleanup
// rules existed. Change it only with a deliberate change to extracted output,
// which rewrites published feeds and belongs in the changelog; set
// RSS_UPDATE_GOLDEN=1 to write the new output.
func TestRecipesWithoutCleanupRulesExtractUnchangedOutput(t *testing.T) {
	const path = "testdata/no_cleanup_rules.golden.json"
	got := goldenOutput(t)
	if os.Getenv("RSS_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output without cleanup rules differs from %s:\n%s", path, got)
	}
}
