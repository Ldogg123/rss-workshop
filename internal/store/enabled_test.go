package store

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"rss-workshop/internal/model"
)

// Pausing and resuming used to save the whole recipe, which bumped the version,
// cleared the error, the failure count and the source validators, and discarded
// a refresh already running. Whether a feed refreshes is not published, so
// SetEnabled must leave every one of those, and the stories, as they were.
func TestSetEnabledChangesOnlyWhetherTheFeedRefreshes(t *testing.T) {
	ctx := t.Context()
	s, err := testStoreOpener(t, 10)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f := model.Feed{Title: "Paused and resumed", URL: "https://example.com", Interval: 3600, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Full: &model.FullContent{Selector: ".body"}}}
	if f.ID, err = s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	f, _ = s.Get(ctx, f.ID)
	stories := []model.Item{{Key: "a", Title: "A", URL: "https://example.com/a", HTML: "<p>Teaser</p>", FullHTML: "<p>Article</p>", Published: time.Unix(1700000000, 0)}}
	if err := s.Complete(ctx, f, stories, `"validator"`, "Mon, 02 Jan 2006 15:04:05 GMT", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	f, _ = s.Get(ctx, f.ID)
	if err := s.Complete(ctx, f, nil, "", "", 503, errors.New("source unavailable"), 0); err != nil {
		t.Fatal(err)
	}
	before, err := s.Get(ctx, f.ID)
	if err != nil || before.Failures != 1 || before.Error == "" || before.ETag == "" || !before.NextRun.After(time.Now()) {
		t.Fatalf("fixture is not a failing feed in backoff: %+v, %v", before, err)
	}
	storedStories, err := s.Items(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	unchanged := func(what string) model.Feed {
		t.Helper()
		got, err := s.Get(ctx, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := before
		want.Enabled, want.NextRun = got.Enabled, got.NextRun
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s changed more than the schedule:\n got %+v\nwant %+v", what, got, want)
		}
		if items, err := s.Items(ctx, f.ID); err != nil || !reflect.DeepEqual(items, storedStories) {
			t.Fatalf("%s changed the stories: %v", what, err)
		}
		return got
	}

	if err := s.SetEnabled(ctx, f.ID, false); err != nil {
		t.Fatal(err)
	}
	if got := unchanged("pause"); got.Enabled || !got.NextRun.Equal(before.NextRun) {
		t.Fatalf("pause: enabled=%v, next run %v -> %v", got.Enabled, before.NextRun, got.NextRun)
	}
	if err := s.SetEnabled(ctx, f.ID, false); err != nil {
		t.Fatal("pausing a paused feed:", err)
	}

	// Resuming makes the feed due now, through its backoff, but keeps the
	// failure count, so another failure continues the backoff.
	if err := s.SetEnabled(ctx, f.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := unchanged("resume"); !got.Enabled || got.NextRun.After(time.Now()) {
		t.Fatalf("resume: enabled=%v, next run %v", got.Enabled, got.NextRun)
	}
	// Resuming a feed that is already enabled is not a refresh request.
	later := time.Now().Add(time.Hour).Unix()
	if _, err := s.DB.ExecContext(ctx, s.bind("UPDATE feeds SET next_run=? WHERE id=?"), later, f.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(ctx, f.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := unchanged("resuming an enabled feed"); got.NextRun.Unix() != later {
		t.Fatalf("resuming an enabled feed moved its next run to %v", got.NextRun)
	}

	// A refresh that loaded the feed before the pause and resume still saves.
	if err := s.Complete(ctx, before, stories, `"validator"`, "", 200, nil, 0); err != nil {
		t.Fatal("a refresh running across a pause was discarded:", err)
	}
	if err := s.SetEnabled(ctx, "missing", true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("a missing feed was resumed:", err)
	}
}
