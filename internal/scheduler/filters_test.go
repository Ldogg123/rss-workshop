package scheduler

import (
	"context"
	"testing"
	"time"

	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

func TestAllFilteredRefreshSucceedsWithoutAutoFallbackOrLosingHistory(t *testing.T) {
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	ctx := context.Background()
	f := model.Feed{Title: "Filtered", URL: "https://example.com", Interval: 3600, Enabled: true, Recipe: model.Recipe{Mode: "auto", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}}}
	f.ID, err = s.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	f, _ = s.Get(ctx, f.ID)
	fetcher := &staticFetcher{body: []byte(`<article><h2>Older accepted story</h2><a href="/same">Read</a></article>`)}
	jobs := New(s, fetcher, 1, time.Second)
	renderer := &fakeRenderer{}
	jobs.Renderer = renderer
	jobs.refresh(ctx, f)
	f, _ = s.Get(ctx, f.ID)
	f.Recipe.Filters = &model.FilterSet{Include: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"interesting"}}}
	if _, err := s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	f, _ = s.Get(ctx, f.ID)
	fetcher.body = []byte(`<article><h2>Unrelated update</h2><a href="/same">Read</a></article><article><h2>Unrelated new story</h2><a href="/new">Read</a></article>`)
	jobs.refresh(ctx, f)
	after, err := s.Get(ctx, f.ID)
	if err != nil || after.Error != "" || after.Failures != 0 || after.LastSuccess.IsZero() || time.Until(after.NextRun) < 59*time.Minute || renderer.calls != 0 {
		t.Fatalf("intentional filtering triggered failure/backoff/browser: %+v, %v, browser=%d", after, err, renderer.calls)
	}
	items, err := s.Items(ctx, f.ID)
	if err != nil || len(items) != 1 || items[0].Title != "Older accepted story" {
		t.Fatalf("filtered refresh changed historical copy or saved new rejection: %+v, %v", items, err)
	}
	runs, err := s.Runs(ctx, f.ID)
	if err != nil || len(runs) != 2 || runs[0].Error != "" || runs[0].Count != 0 {
		t.Fatalf("filtered run summary: %+v, %v", runs, err)
	}
	a := runs[0].Diagnostics.Attempts[0]
	if len(runs[0].Diagnostics.Attempts) != 1 || a.Outcome != "success" || *a.Valid != 2 || a.Filtered != 2 || *a.Items != 0 {
		t.Fatalf("filtered run trace: %+v", a)
	}
}

func TestRefreshAppliesLibraryFiltersAndDiscardsResultsFromBeforeAnEdit(t *testing.T) {
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	ctx := context.Background()
	exclude := func(keyword string) model.FilterSet {
		return model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{keyword}}}
	}
	library, err := s.SaveFilter(ctx, model.LibraryFilter{Name: "No adverts", Filters: exclude("advert")}, store.SaveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f := model.Feed{Title: "Linked", URL: "https://example.com", Interval: 3600, Enabled: true, FilterIDs: []string{library},
		Recipe: model.Recipe{Mode: "static", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Filters: &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "link", Keywords: []string{"/sport"}}}}}
	if f.ID, err = s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	f, _ = s.Get(ctx, f.ID)
	fetcher := &staticFetcher{body: []byte(`<article><h2>News</h2><a href="/news">Read</a></article><article><h2>Advert</h2><a href="/advert">Read</a></article><article><h2>Match report</h2><a href="/sport">Read</a></article><article><h2>Sponsored</h2><a href="/sponsored">Read</a></article>`)}
	jobs := New(s, fetcher, 1, time.Second)
	jobs.refresh(ctx, f)
	items, err := s.Items(ctx, f.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("refresh did not merge own and library rules: %+v, %v", items, err)
	}
	// A refresh that loaded the feed before the library edit must not save
	// results built from the old rules.
	stale, _ := s.Get(ctx, f.ID)
	if _, err := s.SaveFilter(ctx, model.LibraryFilter{ID: library, Name: "No sponsored", Filters: exclude("sponsored")}, store.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	fetcher.body = []byte(`<article><h2>Advert again</h2><a href="/advert-2">Read</a></article>`)
	jobs.refresh(ctx, stale)
	if items, _ := s.Items(ctx, f.ID); len(items) != 2 {
		t.Fatalf("stale refresh saved stories: %+v", items)
	}
	fresh, _ := s.Get(ctx, f.ID)
	jobs.refresh(ctx, fresh)
	items, err = s.Items(ctx, f.ID)
	if err != nil || len(items) != 3 {
		t.Fatalf("refresh after the edit did not apply the new library rules: %+v, %v", items, err)
	}
}
