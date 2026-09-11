package extract

import (
	"strings"
	"testing"

	"rss-workshop/internal/model"
)

func articleRecipe(selector, attr, typ string) model.Recipe {
	return model.Recipe{Type: typ, Items: "article", Title: model.Field{Selector: "h2"},
		Full: &model.FullContent{Selector: selector, Attr: attr}}
}

func TestArticleExtractsSanitizesAndAbsolutizes(t *testing.T) {
	page := `<html><head><base href="https://example.com/news/"></head><body>
	 <nav><a href="/skip">nav</a></nav>
	 <div class="body"><p>Body <b>text</b>.</p><a href="rel.html">more</a>
	  <img src="pic.jpg"><script>alert(1)</script><p onclick="x()">handler</p></div>
	</body></html>`
	got, err := Article([]byte(page), "https://example.com/news/story", articleRecipe(".body", "", "css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Body <b>text</b>", `href="https://example.com/news/rel.html"`, `src="https://example.com/news/pic.jpg"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	for _, banned := range []string{"<script", "alert(1)", "onclick", "nav</a>"} {
		if strings.Contains(got, banned) {
			t.Errorf("article kept %q: %q", banned, got)
		}
	}
}

func TestArticleReportsAProblemInsteadOfGuessing(t *testing.T) {
	page := `<html><body><div class="body">ok</div></body></html>`
	for name, tc := range map[string]struct {
		body   string
		recipe model.Recipe
		url    string
	}{
		"no match":       {page, articleRecipe(".missing", "", "css"), "https://example.com/s"},
		"no selector":    {page, model.Recipe{Type: "css", Full: &model.FullContent{}}, "https://example.com/s"},
		"not configured": {page, model.Recipe{Type: "css"}, "https://example.com/s"},
		"unusable URL":   {page, articleRecipe(".body", "", "css"), "not-a-url"},
		"oversized body": {`<div class="body">` + strings.Repeat("x", MaxArticleBytes+1) + `</div>`, articleRecipe(".body", "", "css"), "https://example.com/s"},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := Article([]byte(tc.body), tc.url, tc.recipe); err == nil {
				t.Fatalf("accepted %s: %q", name, got)
			}
		})
	}
}

func TestArticleSupportsXPathAndAttributes(t *testing.T) {
	page := `<html><body><article id="main" data-body="&lt;p&gt;From an attribute&lt;/p&gt;"><p>XPath body</p></article></body></html>`
	got, err := Article([]byte(page), "https://example.com/s", articleRecipe("//article[@id='main']", "", "xpath"))
	if err != nil || !strings.Contains(got, "XPath body") {
		t.Fatalf("xpath article: %q %v", got, err)
	}
	got, err = Article([]byte(page), "https://example.com/s", articleRecipe("//article[@id='main']", "data-body", "xpath"))
	if err != nil || !strings.Contains(got, "From an attribute") {
		t.Fatalf("attribute article: %q %v", got, err)
	}
}

// An article selector must be validated when the recipe is saved, not on the
// first refresh, and XPath here is absolute because it runs against a new page.
func TestFullContentSelectorIsValidatedOnSave(t *testing.T) {
	base := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}
	valid := base
	valid.Full = &model.FullContent{Selector: ".body"}
	if err := Validate(valid); err != nil {
		t.Fatalf("rejected a valid article selector: %v", err)
	}
	empty := base
	empty.Full = &model.FullContent{}
	if err := Validate(empty); err == nil {
		t.Error("accepted full content with no selector")
	}
	bad := base
	bad.Full = &model.FullContent{Selector: "a[["}
	if err := Validate(bad); err == nil {
		t.Error("accepted an invalid CSS article selector")
	}
	absolute := model.Recipe{Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"},
		Full: &model.FullContent{Selector: "//div[@class='body']"}}
	if err := Validate(absolute); err != nil {
		t.Errorf("rejected an absolute XPath article selector: %v", err)
	}
}
