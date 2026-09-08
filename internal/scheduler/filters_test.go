package scheduler

import (
	"context"
	"testing"
	"time"

	"rss-workshop/internal/model"
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
