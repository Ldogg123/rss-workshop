package store

import (
	"encoding/xml"
	"strings"
	"testing"

	"rss-workshop/internal/extract"
	feedoutput "rss-workshop/internal/feed"
	"rss-workshop/internal/model"
)

func TestLaterImageUpdatesExistingItemAndReaderOutput(t *testing.T) {
	for _, selectorType := range []string{"css", "xpath"} {
		t.Run(selectorType, func(t *testing.T) {
			s, err := testStoreOpener(t, 10)()
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			r := model.Recipe{Type: selectorType, Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Content: model.Field{Selector: "p"}, Image: model.Field{Selector: "img"}, Date: model.Field{Selector: "time"}}
			if selectorType == "xpath" {
				r.Items, r.Title.Selector, r.Link.Selector = "//article", ".//h2", ".//a/@href"
				r.Content.Selector, r.Image.Selector, r.Date.Selector = ".//p", ".//img", ".//time"
			}
			f := savedRunFeed(t, s)
			f.Recipe = r
			if _, err := s.Save(t.Context(), f); err != nil {
				t.Fatal(err)
			}
			f, err = s.Get(t.Context(), f.ID)
			if err != nil {
				t.Fatal(err)
			}
			html := `<article><h2>New story</h2><a href="/story">Read</a><p>First description</p><time datetime="2026-01-02T03:04:05Z"></time></article>`
			initial, err := extract.Run([]byte(html), f.URL, r)
			if err != nil || len(initial.Items) != 1 || initial.Items[0].Image != "" {
				t.Fatal("initially missing image rejected a valid story", err)
			}
			if err := s.Complete(t.Context(), f, initial.Items, "first", "", 200, nil, 0); err != nil {
				t.Fatal(err)
			}
			before, err := s.Items(t.Context(), f.ID)
			if err != nil || len(before) != 1 {
				t.Fatal(err)
			}
			// A later page can add artwork and correct text/date while preserving
			// the same article link. The original publication date remains fixed.
			laterHTML := strings.ReplaceAll(html, "First description", "Updated description")
			laterHTML = strings.ReplaceAll(laterHTML, "2026-01-02", "2026-01-03")
			laterHTML = strings.ReplaceAll(laterHTML, "</article>", `<img src="/picture.png"></article>`)
			later, err := extract.Run([]byte(laterHTML), f.URL, r)
			if err != nil || len(later.Items) != 1 || later.Items[0].Image != "https://example.com/picture.png" {
				t.Fatal("later image was not extracted", err)
			}
			if err := s.Complete(t.Context(), f, later.Items, "second", "", 200, nil, 0); err != nil {
				t.Fatal(err)
			}
			after, err := s.Items(t.Context(), f.ID)
			if err != nil || len(after) != 1 {
				t.Fatal("later image created a duplicate story", err)
			}
			if after[0].Key != before[0].Key || after[0].GUID != before[0].GUID || !after[0].Published.Equal(before[0].Published) || !after[0].FirstSeen.Equal(before[0].FirstSeen) {
				t.Fatal("later image changed the story's identity or date")
			}
			if after[0].Image != later.Items[0].Image || !strings.Contains(after[0].HTML, "Updated description") {
				t.Fatal("later image/content did not merge into the saved story")
			}
			for phase, items := range map[string][]model.Item{"before": before, "after": after} {
				rss, _, err := feedoutput.Render(f, items, "", "")
				if err != nil {
					t.Fatal(err)
				}
				atom, _, err := feedoutput.RenderAtom(f, items, "https://reader.example.com")
				if err != nil {
					t.Fatal(err)
				}
				var rssDoc struct {
					Description string `xml:"channel>item>description"`
				}
				var atomDoc struct {
					Content string `xml:"entry>content"`
				}
				if err := xml.Unmarshal(rss, &rssDoc); err != nil {
					t.Fatal(err)
				}
				if err := xml.Unmarshal(atom, &atomDoc); err != nil {
					t.Fatal(err)
				}
				for _, body := range []string{rssDoc.Description, atomDoc.Content} {
					hasImage := strings.Contains(body, `<img src="https://example.com/picture.png"`)
					if hasImage != (phase == "after") || body != items[0].HTML {
						t.Fatal("reader output did not reflect the later image", phase)
					}
				}
			}
		})
	}
}
