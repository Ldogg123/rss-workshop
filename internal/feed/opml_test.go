package feed_test

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/feed"
	"rss-workshop/internal/model"
)

type parsedOPML struct {
	Outlines []struct {
		Attrs []xml.Attr `xml:",any,attr"`
	} `xml:"body>outline"`
}

func opmlFeed(title, source, recipeType string) model.Feed {
	token := strings.ToLower(strings.ReplaceAll(title, " ", "-"))
	return model.Feed{Title: title, URL: source, Recipe: model.Recipe{Type: recipeType},
		RSSURL: "https://reader.example/feeds/" + token + ".xml", AtomURL: "https://reader.example/feeds/" + token + ".atom"}
}

// The export for a library of page feeds is pinned byte for byte, so a change
// made for feed sources cannot alter what a reader already imports. htmlUrl
// is the page the feed was built from, whichever selector language reads it.
func TestOPMLForPageFeedsIsUnchanged(t *testing.T) {
	feeds := []model.Feed{
		opmlFeed(`Ars & "Tech" <news>`, "https://example.com/news?page=1&sort=new", "css"),
		opmlFeed("Gazette", "https://example.org/blog", "xpath"),
	}
	created := time.Date(2026, 11, 7, 12, 30, 0, 0, time.FixedZone("CET", 3600))
	const want = `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <head>
    <title>RSS Workshop</title>
    <dateCreated>Sat, 07 Nov 2026 11:30:00 +0000</dateCreated>
  </head>
  <body>
    <outline type="rss" text="Ars &amp; &#34;Tech&#34; &lt;news&gt;" title="Ars &amp; &#34;Tech&#34; &lt;news&gt;" xmlUrl="https://reader.example/feeds/ars-&amp;-&#34;tech&#34;-&lt;news&gt;.xml" htmlUrl="https://example.com/news?page=1&amp;sort=new"></outline>
    <outline type="rss" text="Gazette" title="Gazette" xmlUrl="https://reader.example/feeds/gazette.xml" htmlUrl="https://example.org/blog"></outline>
  </body>
</opml>
`
	got, err := feed.RenderOPML("RSS Workshop", feeds, false, created)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("page feeds' OPML changed:\n got: %s\nwant: %s", got, want)
	}
}

// A feed source's address is the upstream RSS or Atom document, not a web
// page. Published as htmlUrl, it makes a reader's "open website" link show
// raw XML, so the attribute is left out; everything a reader needs to
// subscribe stays, in both formats. Only the recipe type decides: the page
// feeds beside it keep theirs, even at addresses that look like a feed.
func TestOPMLOmitsHTMLURLForAFeedSource(t *testing.T) {
	const upstream = "https://gazette.example/feed.xml"
	feeds := []model.Feed{
		opmlFeed("Page before", "https://example.com/rss", "css"),
		opmlFeed("Gazette feed", upstream, extract.FeedType),
		opmlFeed("Page after", "https://example.org/feed.xml", "xpath"),
	}
	for _, format := range []struct {
		name string
		atom bool
		ext  string
	}{{"rss", false, ".xml"}, {"atom", true, ".atom"}} {
		t.Run(format.name, func(t *testing.T) {
			out, err := feed.RenderOPML("RSS Workshop", feeds, format.atom, time.Unix(1757592000, 0))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), upstream) {
				t.Errorf("the export names the upstream feed %s:\n%s", upstream, out)
			}
			var doc parsedOPML
			if err := xml.Unmarshal(out, &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Outlines) != len(feeds) {
				t.Fatalf("exported %d outlines, want %d", len(doc.Outlines), len(feeds))
			}
			want := []map[string]string{
				{"type": "rss", "text": "Page before", "title": "Page before", "xmlUrl": "https://reader.example/feeds/page-before" + format.ext, "htmlUrl": "https://example.com/rss"},
				{"type": "rss", "text": "Gazette feed", "title": "Gazette feed", "xmlUrl": "https://reader.example/feeds/gazette-feed" + format.ext},
				{"type": "rss", "text": "Page after", "title": "Page after", "xmlUrl": "https://reader.example/feeds/page-after" + format.ext, "htmlUrl": "https://example.org/feed.xml"},
			}
			for i, o := range doc.Outlines {
				got := map[string]string{}
				for _, a := range o.Attrs {
					got[a.Name.Local] = a.Value
				}
				if len(got) != len(want[i]) {
					t.Errorf("outline %d has attributes %v, want %v", i, got, want[i])
					continue
				}
				for name, value := range want[i] {
					if got[name] != value {
						t.Errorf("outline %d: %s is %q, want %q", i, name, got[name], value)
					}
				}
			}
		})
	}
}
