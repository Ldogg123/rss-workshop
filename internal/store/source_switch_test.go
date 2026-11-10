package store

import (
	"errors"
	"reflect"
	"testing"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/model"
)

// A web page's stories are identified by their link and a feed source's by
// their guid, so a saved feed that switched would store every story its source
// lists a second time under a new GUID. The editor locks the source, but a
// save need not come from the editor, so the store has to refuse the switch,
// and a refused save must change nothing.
func TestSaveRefusesSwitchingBetweenPageAndFeedSource(t *testing.T) {
	article := &model.FullContent{Selector: ".body"}
	css := model.Recipe{Mode: "static", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Full: article}
	xpath := model.Recipe{Mode: "static", Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"}, Full: article}
	source := model.Recipe{Mode: "static", Type: extract.FeedType, Full: article}
	// Stores written through Save alone can hold a recipe without a type. It
	// has never been a feed source.
	untyped := model.Recipe{Full: article}
	for name, c := range map[string]struct {
		from, to model.Recipe
		refused  bool
	}{
		"CSS page to feed source":     {css, source, true},
		"XPath page to feed source":   {xpath, source, true},
		"untyped page to feed source": {untyped, source, true},
		"feed source to CSS page":     {source, css, true},
		"feed source to XPath page":   {source, xpath, true},
		"feed source to no type":      {source, untyped, true},
		"CSS page to XPath page":      {css, xpath, false},
		"untyped page to CSS page":    {untyped, css, false},
		"feed source edited":          {source, source, false},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			s, err := testStoreOpener(t, 50)()
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			library, err := s.SaveFilter(ctx, model.LibraryFilter{Name: "No lottery", Filters: model.FilterSet{
				Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"Lottery"}},
			}}, SaveOptions{})
			if err != nil {
				t.Fatal(err)
			}
			id, err := s.Save(ctx, model.Feed{Title: "Gazette", URL: "https://gazette.example/news", Interval: 900, Enabled: true,
				Recipe: c.from, FilterIDs: []string{library}})
			if err != nil {
				t.Fatal(err)
			}
			saved, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Complete(ctx, saved, []model.Item{
				{Key: "kept", Title: "Council meets", URL: "https://gazette.example/council", HTML: "<p>teaser</p>", FullHTML: "<p>the article</p>"},
				{Key: "ad", Title: "Sponsored: a new bakery", URL: "https://gazette.example/bakery", HTML: "<p>advert</p>", FullHTML: "<p>the advert</p>"},
			}, `"v1"`, "Mon, 02 Nov 2026 10:00:00 GMT", 200, nil, 0); err != nil {
				t.Fatal(err)
			}
			// A failed refresh leaves an error, a failure count and a backoff
			// that an accepted save would reset.
			if err := s.Complete(ctx, saved, nil, "", "", 503, errors.New("source unavailable"), 0); err != nil {
				t.Fatal(err)
			}
			// Put the stored times in the past, so that a save that moved one
			// could not land on the same second.
			if _, err := s.DB.ExecContext(ctx, s.bind("UPDATE feeds SET last_changed=last_changed-1000,next_run=next_run-1000 WHERE id=?"), id); err != nil {
				t.Fatal(err)
			}
			before, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			stories, err := s.Items(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if before.ETag == "" || before.LastModified == "" || before.Failures != 1 || before.Error == "" || before.LastChanged.IsZero() ||
				len(before.FilterIDs) != 1 || len(stories) != 2 || stories[0].FullHTML == "" || stories[1].FullHTML == "" {
				t.Fatalf("setup: %+v, %d stories", before, len(stories))
			}

			// Everything else an edit can change comes with the switch: the
			// title, address and interval (last_changed), the article selector
			// (stored bodies), a filter applied to saved history (stories), the
			// library filters and whether the feed refreshes.
			edit := before
			edit.Recipe = c.to
			edit.Recipe.Full = &model.FullContent{Selector: ".story"}
			edit.Recipe.Filters = &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"Sponsored"}}}
			edit.Title, edit.URL, edit.Interval, edit.Enabled, edit.FilterIDs = "Gazette, renamed", "https://gazette.example/other", 3600, false, []string{}
			_, err = s.SaveWithOptions(ctx, edit, SaveOptions{ApplyFiltersToHistory: true})
			if !c.refused {
				if err != nil {
					t.Fatalf("an edit that keeps the source was refused: %v", err)
				}
				after, err := s.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if after.Recipe.Type != c.to.Type || after.Title != edit.Title || after.Version != before.Version+1 || after.ETag != "" {
					t.Errorf("the edit was not stored: %+v", after)
				}
				return
			}
			var bad *InvalidError
			if !errors.Is(err, ErrSourceSwitch) || !errors.As(err, &bad) {
				t.Fatalf("the switch was saved or failed for another reason: %v", err)
			}
			after, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Errorf("the refused save changed the feed:\n got %+v\nwant %+v", after, before)
			}
			if kept, err := s.Items(ctx, id); err != nil || !reflect.DeepEqual(kept, stories) {
				t.Errorf("the refused save changed the stored stories: %+v, %v", kept, err)
			}

			// The same edit without the switch is an ordinary save, which shows
			// that each of those changes was the refused save's to make.
			edit.Recipe.Type, edit.Recipe.Items, edit.Recipe.Title = c.from.Type, c.from.Items, c.from.Title
			if _, err := s.SaveWithOptions(ctx, edit, SaveOptions{ApplyFiltersToHistory: true}); err != nil {
				t.Fatalf("the edit without the switch: %v", err)
			}
			after, err = s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			kept, err := s.Items(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if after.Version != before.Version+1 || after.ETag != "" || after.LastModified != "" || !after.LastChanged.After(before.LastChanged) ||
				after.Failures != 0 || after.Error != "" || after.Enabled || len(after.FilterIDs) != 0 ||
				len(kept) != 1 || kept[0].Key != "kept" || kept[0].FullHTML != "" {
				t.Errorf("the edit without the switch: %+v, stories %+v", after, kept)
			}

			// Duplicate, which the refusal points to, saves a new feed, and a
			// new feed chooses either source.
			edit.ID, edit.Recipe.Type, edit.Recipe.Items, edit.Recipe.Title = "", c.to.Type, c.to.Items, c.to.Title
			copyID, err := s.Save(ctx, edit)
			if err != nil {
				t.Fatalf("a copy with the other source: %v", err)
			}
			if copied, err := s.Get(ctx, copyID); err != nil || copied.Recipe.Type != c.to.Type {
				t.Errorf("the copy: %+v, %v", copied, err)
			}
		})
	}
}
