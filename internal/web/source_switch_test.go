package web

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

// Only the editor's locked Source control kept a saved feed from switching
// between a web page and a feed source. A save sent by a script is valid on
// its own and was stored, which gave every story the source still lists a
// second identity. The server answers it with a 400 whose fixed message names
// Duplicate, and stores nothing.
func TestSaveAnswersASourceSwitchWith400AndPointsToDuplicate(t *testing.T) {
	page := model.Recipe{Mode: "static", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}
	source := model.Recipe{Mode: "static", Type: extract.FeedType}
	for name, c := range map[string]struct{ from, to model.Recipe }{
		"page to feed source": {page, source},
		"feed source to page": {source, page},
	} {
		t.Run(name, func(t *testing.T) {
			h := newPortabilityHarness(t)
			ctx := context.Background()
			body := func(f model.Feed) []byte {
				data, err := json.Marshal(f)
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			feed := model.Feed{Title: "Gazette", URL: "https://gazette.example/news", Interval: 900, Recipe: c.from}
			w := h.request("POST", "/api/feeds", body(feed))
			var saved struct {
				ID string `json:"id"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.ID == "" {
				t.Fatalf("save: %d %s", w.Code, w.Body.String())
			}
			before, err := h.app.Store.Get(ctx, saved.ID)
			if err != nil {
				t.Fatal(err)
			}

			// A script sends back the object the listing gave it, with the
			// other source's recipe and a new title.
			switched := before
			switched.Recipe, switched.Title = c.to, "Gazette, switched"
			w = h.request("PUT", "/api/feeds/"+saved.ID, body(switched))
			var refusal struct {
				Error string `json:"error"`
			}
			if w.Code != http.StatusBadRequest || json.Unmarshal(w.Body.Bytes(), &refusal) != nil {
				t.Fatalf("a source switch answered %d %s, want 400", w.Code, w.Body.String())
			}
			if refusal.Error != store.ErrSourceSwitch.Error() || !strings.Contains(refusal.Error, "Duplicate") {
				t.Errorf("the refusal is %q, want the fixed message that points to Duplicate", refusal.Error)
			}
			if after, err := h.app.Store.Get(ctx, saved.ID); err != nil || !reflect.DeepEqual(after, before) {
				t.Errorf("the refused save changed the feed:\n got %+v\nwant %+v (%v)", after, before, err)
			}

			// The feed stays editable with the source it has, and what the
			// message recommends works: the same recipe saved as a new feed.
			renamed := before
			renamed.Title = "Gazette, renamed"
			if w := h.request("PUT", "/api/feeds/"+saved.ID, body(renamed)); w.Code != http.StatusOK {
				t.Errorf("an edit that keeps the source: %d %s", w.Code, w.Body.String())
			}
			switched.ID = ""
			if w := h.request("POST", "/api/feeds", body(switched)); w.Code != http.StatusOK {
				t.Errorf("a copy with the other source: %d %s", w.Code, w.Body.String())
			}
			feeds, err := h.app.Store.List(ctx)
			if err != nil || len(feeds) != 2 {
				t.Fatalf("%d feeds, %v", len(feeds), err)
			}
			for _, f := range feeds {
				want := c.to.Type
				if f.ID == saved.ID {
					want = c.from.Type
				}
				if f.Recipe.Type != want {
					t.Errorf("feed %q has source type %q, want %q", f.Title, f.Recipe.Type, want)
				}
			}
		})
	}
}
