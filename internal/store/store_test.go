package store

import (
	"context"
	"errors"
	"rss-workshop/internal/model"
	"testing"
	"time"
)

func TestHistoryRestartAndStaleRecipe(t *testing.T) {
	ctx := context.Background()
	open := testStoreOpener(t, 2)
	s, e := open()
	if e != nil {
		t.Fatal(e)
	}
	f := model.Feed{Title: "Test", URL: "https://example.com", Interval: 60, Enabled: true}
	f.ID, e = s.Save(ctx, f)
	if e != nil {
		t.Fatal(e)
	}
	f, _ = s.Get(ctx, f.ID)
	items := []model.Item{{Key: "https://example.com/1", URL: "https://example.com/1", Title: "First"}}
	if e = s.Complete(ctx, f, items, "etag", "", 200, nil, 0); e != nil {
		t.Fatal(e)
	}
	before, _ := s.Items(ctx, f.ID)
	items[0].Title = "Edited"
	if e = s.Complete(ctx, f, items, "etag", "", 200, nil, 0); e != nil {
		t.Fatal(e)
	}
	after, _ := s.Items(ctx, f.ID)
	if len(after) != 1 || before[0].GUID != after[0].GUID || !before[0].Published.Equal(after[0].Published) || after[0].Title != "Edited" {
		t.Fatalf("bad merge: %+v", after)
	}
	if e = s.Complete(ctx, f, nil, "", "", 500, errors.New("source failed"), 0); e != nil {
		t.Fatal(e)
	}
	s.DB.Close()
	s, e = open()
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	got, e := s.Get(ctx, f.ID)
	if e != nil || got.Error != "source failed" || got.LastSuccess.IsZero() || got.NextRun.Before(time.Now()) {
		t.Fatal(got, e)
	}
	after, _ = s.Items(ctx, f.ID)
	if len(after) != 1 {
		t.Fatal(after)
	}
	if _, e = s.Save(ctx, f); e != nil {
		t.Fatal(e)
	}
	if e = s.Complete(ctx, f, items, "", "", 200, nil, 0); !errors.Is(e, ErrStale) {
		t.Fatal("stale result accepted", e)
	}
	edited, _ := s.Get(ctx, f.ID)
	if edited.ETag != "" {
		t.Fatal("edit kept validators")
	}
	if e = s.Delete(ctx, f.ID); e != nil {
		t.Fatal(e)
	}
	after, _ = s.Items(ctx, f.ID)
	if len(after) != 0 {
		t.Fatal("cascade failed")
	}
}
