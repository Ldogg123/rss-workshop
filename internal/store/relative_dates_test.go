package store

import (
	"bytes"
	"context"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/extract"
	feedoutput "rss-workshop/internal/feed"
	"rss-workshop/internal/model"
)

func TestRelativePublicationDatesPersistAcrossRefreshAndRestart(t *testing.T) {
	for _, selectorType := range []string{"css", "xpath"} {
		t.Run(selectorType, func(t *testing.T) {
			ctx := context.Background()
			open := testStoreOpener(t, 10)
			s, err := open()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.DB.Close() })
			r := model.Recipe{
				Type:    selectorType,
				Items:   "article",
				Title:   model.Field{Selector: "h2"},
				Link:    model.Field{Selector: "a"},
				Date:    model.Field{Selector: "time"},
				Content: model.Field{Selector: ".summary"},
			}
			if selectorType == "xpath" {
				r.Items = "//article"
				r.Title.Selector = ".//h2"
				r.Link.Selector = ".//a/@href"
				r.Date.Selector = ".//time"
				r.Content.Selector = ".//p"
			}
			f := model.Feed{Title: "Relative dates", URL: "https://example.com/news", Recipe: r, Interval: 300, Enabled: true}
			f.ID, err = s.Save(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			f, err = s.Get(ctx, f.ID)
			if err != nil {
				t.Fatal(err)
			}

			observedAt := time.Date(2026, time.March, 15, 14, 30, 0, 0, time.UTC)
			initialHTML := []byte(`
				<article><h2>Minutes</h2><a href="/minutes">Read</a><time>2 minutes ago</time><p class="summary">Original minute story.</p></article>
				<article><h2>Months</h2><a href="/months">Read</a><time>1 month ago</time><p class="summary">Original month story.</p></article>
				<article><h2>Exact</h2><a href="/exact">Read</a><time datetime="2026-03-15T13:45:00Z">1 hour ago</time><p class="summary">Exact timestamp.</p></article>
				<article><h2>Unknown</h2><a href="/unknown">Read</a><time>some time ago</time><p class="summary">First-seen fallback.</p></article>`)
			initial, err := extract.RunAt(initialHTML, f.URL, r, observedAt)
			if err != nil {
				t.Fatal(err)
			}
			preview := relativeDatesByKey(t, initial.Items)
			wantPublished := map[string]time.Time{
				"https://example.com/minutes": observedAt.Add(-2 * time.Minute),
				"https://example.com/months":  observedAt.AddDate(0, -1, 0),
				"https://example.com/exact":   time.Date(2026, time.March, 15, 13, 45, 0, 0, time.UTC),
			}
			for key, want := range wantPublished {
				got, ok := preview[key]
				if !ok || !got.Published.Equal(want) {
					t.Fatalf("initial date for %s = %v, want %v", key, got.Published, want)
				}
				if got.PublishedEstimated != (key != "https://example.com/exact") {
					t.Fatalf("unexpected estimate flag for %s: %+v", key, got)
				}
			}
			if len(preview) != 4 || !preview["https://example.com/unknown"].Published.IsZero() {
				t.Fatalf("unknown date should use the store's first-seen fallback: %+v", preview)
			}
			if err = s.Complete(ctx, f, initial.Items, "", "", 200, nil, 0); err != nil {
				t.Fatal(err)
			}
			beforeItems, err := s.Items(ctx, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			before := relativeDatesByKey(t, beforeItems)
			for key, want := range wantPublished {
				if !before[key].Published.Equal(want) {
					t.Fatalf("stored first estimate for %s = %v, want %v", key, before[key].Published, want)
				}
			}
			fallback := before["https://example.com/unknown"]
			if fallback.Published.IsZero() || !fallback.Published.Equal(fallback.FirstSeen) {
				t.Fatalf("unknown date did not receive persisted first-seen time: %+v", fallback)
			}

			// Rounded labels produce different estimates on the next fetch. Even
			// a previously unrecognized label must not rewrite a saved pubDate.
			laterHTML := strings.ReplaceAll(string(initialHTML), "2 minutes ago", "3 minutes ago")
			laterHTML = strings.ReplaceAll(laterHTML, "some time ago", "2 hours ago")
			laterHTML = strings.ReplaceAll(laterHTML, ">Minutes<", ">Minutes updated<")
			laterHTML = strings.ReplaceAll(laterHTML, "Original minute story.", "Updated minute story.")
			later, err := extract.RunAt([]byte(laterHTML), f.URL, r, observedAt.Add(2*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			laterPreview := relativeDatesByKey(t, later.Items)
			for _, key := range []string{"https://example.com/minutes", "https://example.com/months", "https://example.com/unknown"} {
				got := laterPreview[key]
				if got.Published.IsZero() || !got.PublishedEstimated || got.Published.Equal(before[key].Published) {
					t.Fatalf("fixture must produce a different recognized estimate for %s: %+v", key, got)
				}
			}
			if err = s.Complete(ctx, f, later.Items, "", "", 200, nil, 0); err != nil {
				t.Fatal(err)
			}
			afterItems, err := s.Items(ctx, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			after := relativeDatesByKey(t, afterItems)
			relativeDatesAssertStable(t, before, after)
			if got := after["https://example.com/minutes"]; got.Title != "Minutes updated" || !strings.Contains(got.HTML, "Updated minute story.") {
				t.Fatalf("preserving publication date prevented content update: %+v", got)
			}
			f, err = s.Get(ctx, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			rssBefore, atomBefore := relativeDatesCheckReaderFormats(t, f, afterItems, before)

			if err = s.DB.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := open()
			if err != nil {
				t.Fatal(err)
			}
			s = reopened
			reopenedFeed, err := s.Get(ctx, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			reopenedItems, err := s.Items(ctx, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			relativeDatesAssertStable(t, before, relativeDatesByKey(t, reopenedItems))
			rssAfter, atomAfter := relativeDatesCheckReaderFormats(t, reopenedFeed, reopenedItems, before)
			if !bytes.Equal(rssBefore, rssAfter) || !bytes.Equal(atomBefore, atomAfter) {
				t.Fatal("RSS or Atom output changed after closing and reopening the database")
			}
		})
	}
}

func relativeDatesByKey(t *testing.T, items []model.Item) map[string]model.Item {
	t.Helper()
	out := make(map[string]model.Item, len(items))
	for _, item := range items {
		if _, exists := out[item.Key]; exists {
			t.Fatalf("duplicate item key %q", item.Key)
		}
		out[item.Key] = item
	}
	return out
}

func relativeDatesAssertStable(t *testing.T, before, after map[string]model.Item) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("item count changed: before %d, after %d", len(before), len(after))
	}
	for key, original := range before {
		got, ok := after[key]
		if !ok || got.GUID == "" || got.GUID != original.GUID || !got.Published.Equal(original.Published) || !got.FirstSeen.Equal(original.FirstSeen) {
			t.Fatalf("saved identity/date changed for %s: before %+v, after %+v", key, original, got)
		}
	}
}

func relativeDatesCheckReaderFormats(t *testing.T, f model.Feed, items []model.Item, expected map[string]model.Item) ([]byte, []byte) {
	t.Helper()
	rssBody, _, err := feedoutput.Render(f, items)
	if err != nil {
		t.Fatal(err)
	}
	atomBody, _, err := feedoutput.RenderAtom(f, items, "https://reader.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var rssDoc struct {
		XMLName xml.Name `xml:"rss"`
		Items   []struct {
			Link      string `xml:"link"`
			GUID      string `xml:"guid"`
			Published string `xml:"pubDate"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(rssBody, &rssDoc); err != nil {
		t.Fatal(err)
	}
	var atomDoc struct {
		XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
		Entries []struct {
			ID        string `xml:"id"`
			Published string `xml:"published"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(atomBody, &atomDoc); err != nil {
		t.Fatal(err)
	}
	if len(rssDoc.Items) != len(expected) || len(atomDoc.Entries) != len(expected) {
		t.Fatalf("reader item counts differ: RSS %d, Atom %d, want %d", len(rssDoc.Items), len(atomDoc.Entries), len(expected))
	}
	wantByGUID := make(map[string]model.Item, len(expected))
	for _, it := range expected {
		wantByGUID[it.GUID] = it
	}
	seen := make(map[string]bool, len(expected))
	for _, got := range rssDoc.Items {
		want, ok := expected[got.Link]
		parsed, err := time.Parse(time.RFC1123Z, got.Published)
		if !ok || seen[got.GUID] || got.GUID != want.GUID || err != nil || !parsed.Equal(want.Published) {
			t.Fatalf("RSS publication date or ID changed: got %+v, want %+v, parse error %v", got, want, err)
		}
		seen[got.GUID] = true
	}
	clear(seen)
	for _, got := range atomDoc.Entries {
		want, ok := wantByGUID[got.ID]
		parsed, err := time.Parse(time.RFC3339Nano, got.Published)
		if !ok || seen[got.ID] || err != nil || !parsed.Equal(want.Published) {
			t.Fatalf("Atom publication date or ID changed: got %+v, want %+v, parse error %v", got, want, err)
		}
		seen[got.ID] = true
	}
	return rssBody, atomBody
}
