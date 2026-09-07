package extract

import (
	"os"
	"rss-workshop/internal/model"
	"strings"
	"testing"
)

func recipe(typ string) model.Recipe {
	if typ == "xpath" {
		return model.Recipe{Type: typ, Items: "//article", Title: model.Field{Selector: ".//h2"}, Link: model.Field{Selector: ".//a[1]/@href"}, Date: model.Field{Selector: ".//time/@datetime"}, Content: model.Field{Selector: ".//div"}, Image: model.Field{Selector: ".//img"}}
	}
	return model.Recipe{Type: typ, Items: "article.card", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Date: model.Field{Selector: "time", Attr: "datetime"}, Content: model.Field{Selector: ".summary"}, Image: model.Field{Selector: "img"}}
}
func TestCSSAndXPath(t *testing.T) {
	b, e := os.ReadFile("../../testdata/cards.html")
	if e != nil {
		t.Fatal(e)
	}
	for _, typ := range []string{"css", "xpath"} {
		t.Run(typ, func(t *testing.T) {
			p, e := Run(b, "https://example.com/page", recipe(typ))
			if e != nil {
				t.Fatal(e)
			}
			if p.Matches != 2 || len(p.Items) != 2 {
				t.Fatalf("counts: %+v", p)
			}
			a := p.Items[0]
			if a.Title != "First & best" || a.URL != "https://example.com/news/first?edition=1" || a.Image != "https://example.com/images/one.jpg" || a.Published.IsZero() {
				t.Fatalf("bad item: %+v", a)
			}
			if p.Items[1].Image != "https://example.com/images/large.jpg" {
				t.Fatal(p.Items[1].Image)
			}
			for _, bad := range []string{"<script", "onerror", "javascript:", "<iframe", "data:image"} {
				if strings.Contains(a.HTML, bad) {
					t.Fatalf("unsafe HTML: %s", a.HTML)
				}
			}
			if !strings.Contains(a.HTML, "https://example.com/about") {
				t.Fatal(a.HTML)
			}
		})
	}
}
func TestInvalidAndEmpty(t *testing.T) {
	r := recipe("css")
	r.Items = "["
	if _, e := Run([]byte("<html>"), "https://example.com", r); e == nil {
		t.Fatal("accepted invalid selector")
	}
	r = recipe("css")
	if _, e := Run([]byte("<html>"), "https://example.com", r); e == nil {
		t.Fatal("accepted empty extraction")
	}
}

func TestFlareSolverrIgnoresLocalReadinessSelector(t *testing.T) {
	r := recipe("css")
	r.Mode = "flaresolverr"
	r.WaitSelector = "["
	if err := Validate(r); err != nil {
		t.Fatal("unused local browser selector blocked FlareSolverr", err)
	}
	r.Mode = "browser"
	if err := Validate(r); err == nil {
		t.Fatal("invalid local readiness selector accepted by Chromium mode")
	}
}
func TestNoLinkFallbackAndUnsafeLink(t *testing.T) {
	r := recipe("css")
	r.Items = "article"
	r.Link = model.Field{}
	r.Content = model.Field{}
	r.Date = model.Field{}
	r.Image = model.Field{}
	b := []byte(`<article><h2>Stable</h2><a href="javascript:alert(1)">bad</a></article>`)
	p, e := Run(b, "https://example.com", r)
	if e != nil || !strings.HasPrefix(p.Items[0].Key, "title:") {
		t.Fatal(p, e)
	}
	r.Link.Selector = "a"
	if _, e = Run(b, "https://example.com", r); e == nil {
		t.Fatal("unsafe URL accepted")
	}
}
func TestXPathCannotEscapeCard(t *testing.T) {
	r := recipe("xpath")
	r.Title.Selector = ".//h2 | //aside"
	p, e := Run([]byte(`<aside>Outside</aside><article><h2>Inside</h2><a href="/one">One</a></article>`), "https://example.com", r)
	if e != nil || p.Items[0].Title != "Inside" {
		t.Fatal(p, e)
	}
}
