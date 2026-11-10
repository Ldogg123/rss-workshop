package web

import (
	"context"
	"encoding/xml"
	"strings"
	"testing"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/extract"
	"rss-workshop/internal/model"
)

// A feed source's address is the upstream feed, so the export must not offer
// it to a reader as the feed's website. The page feed beside it keeps its
// htmlUrl, and both stay subscribable: each xmlUrl is a link the reader routes
// serve without a session, in either format.
func TestOPMLExportOmitsHTMLURLForAFeedSource(t *testing.T) {
	ctx := context.Background()
	s, e := openTestStore(t, 500)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	a, e := auth.New("long-test-password", "", false)
	if e != nil {
		t.Fatal(e)
	}
	app := &App{Store: s, Auth: a, BaseURL: "https://reader.example"}
	h := app.Handler()

	const page, upstream = "https://example.com/news", "https://gazette.example/feed.xml"
	for _, f := range []model.Feed{
		{Title: "Page feed", URL: page, Interval: 900, Enabled: true,
			Recipe: model.Recipe{Type: "css", Items: ".card", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}}},
		{Title: "Feed source", URL: upstream, Interval: 900, Enabled: true,
			Recipe: model.Recipe{Mode: "static", Type: extract.FeedType}},
	} {
		id, e := s.Save(ctx, f)
		if e != nil {
			t.Fatal(e)
		}
		saved, e := s.Get(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		if e := s.Complete(ctx, saved, []model.Item{{Key: "one", Title: "Story from " + f.Title, URL: "https://stories.example/" + id}}, "", "", 200, nil, 0); e != nil {
			t.Fatal(e)
		}
	}

	if w := get(t, h, "/api/opml", nil); w.Code != 401 {
		t.Fatalf("unauthenticated OPML export returned %d; reader links are bearer credentials", w.Code)
	}
	cookie := login(t, h, app.BaseURL)
	for _, format := range []struct{ query, ext, contentType string }{
		{"", ".xml", "application/rss+xml"},
		{"?format=atom", ".atom", "application/atom+xml"},
	} {
		w := get(t, h, "/api/opml"+format.query, cookie)
		if w.Code != 200 {
			t.Fatalf("OPML export %q returned %d", format.query, w.Code)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control is %q; a list of reader links must not be cached", got)
		}
		if strings.Contains(w.Body.String(), upstream) {
			t.Errorf("export %q names the upstream feed, which a reader would open as the website:\n%s", format.query, w.Body.String())
		}
		var doc struct {
			Outlines []struct {
				Title string     `xml:"title,attr"`
				Attrs []xml.Attr `xml:",any,attr"`
			} `xml:"body>outline"`
		}
		if e := xml.Unmarshal(w.Body.Bytes(), &doc); e != nil {
			t.Fatalf("a reader could not parse the export: %v", e)
		}
		if len(doc.Outlines) != 2 {
			t.Fatalf("exported %d outlines, want the page feed and the feed source", len(doc.Outlines))
		}
		for _, o := range doc.Outlines {
			attrs := map[string]string{}
			for _, a := range o.Attrs {
				attrs[a.Name.Local] = a.Value
			}
			htmlURL, has := attrs["htmlUrl"]
			switch o.Title {
			case "Page feed":
				if htmlURL != page {
					t.Errorf("page feed's htmlUrl is %q, want its source page %q", htmlURL, page)
				}
			case "Feed source":
				if has {
					t.Errorf("feed source carries htmlUrl=%q; it has no website to link back to", htmlURL)
				}
			default:
				t.Fatalf("unexpected outline %q", o.Title)
			}
			link := attrs["xmlUrl"]
			if !strings.HasPrefix(link, "https://reader.example/feeds/") || !strings.HasSuffix(link, format.ext) {
				t.Fatalf("%s: xmlUrl %q is not a public reader link", o.Title, link)
			}
			served := get(t, h, strings.TrimPrefix(link, "https://reader.example"), nil)
			if served.Code != 200 || !strings.HasPrefix(served.Header().Get("Content-Type"), format.contentType) {
				t.Errorf("%s: xmlUrl returned %d %q; the OPML would not work in a reader", o.Title, served.Code, served.Header().Get("Content-Type"))
			}
			if !strings.Contains(served.Body.String(), "Story from "+o.Title) {
				t.Errorf("%s: xmlUrl served another feed's stories, or none", o.Title)
			}
		}
	}
}
