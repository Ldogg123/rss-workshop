package store

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"rss-workshop/internal/model"
)

// Retention applies to stories already saved, so lowering MAX_ITEMS deletes
// them at the next refresh. That is the only irreversible effect an operator
// can cause by editing configuration, and it used to happen silently.
func TestRetentionDeletionsAreLogged(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/rss.db"
	s, err := Open(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	f := model.Feed{Title: "Archive", URL: "https://example.com", Interval: 60, Enabled: true}
	f.ID, err = s.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	items := make([]model.Item, 10)
	for i := range items {
		items[i] = model.Item{Key: string(rune('a' + i)), Title: "Story"}
	}
	if err := s.Complete(ctx, saved, items, "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	s.DB.Close()

	// Reopen with a lower limit, as an operator editing MAX_ITEMS would.
	var log bytes.Buffer
	restore := slog.Default()
	defer slog.SetDefault(restore)
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo})))

	lowered, err := Open(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer lowered.DB.Close()
	saved, err = lowered.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := lowered.Complete(ctx, saved, items[:1], "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	remaining, err := lowered.Items(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 3 {
		t.Fatalf("retention kept %d stories, want 3", len(remaining))
	}
	out := log.String()
	if !strings.Contains(out, `msg="stories removed by retention"`) {
		t.Fatalf("stored stories were deleted with no log line:\n%s", out)
	}
	for _, want := range []string{"removed=7", "max_items=3", `feed=Archive`} {
		if !strings.Contains(out, want) {
			t.Errorf("the retention line does not report %s:\n%s", want, out)
		}
	}
}

// A refresh that removes nothing must stay quiet, or every refresh logs.
func TestRetentionIsSilentWhenNothingIsRemoved(t *testing.T) {
	ctx := context.Background()
	s, err := testStoreOpener(t, 50)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f := model.Feed{Title: "Quiet", URL: "https://example.com", Interval: 60, Enabled: true}
	f.ID, err = s.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	restore := slog.Default()
	defer slog.SetDefault(restore)
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := s.Complete(ctx, saved, []model.Item{{Key: "a", Title: "One"}}, "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "stories removed by retention") {
		t.Errorf("a refresh that removed nothing logged a retention line:\n%s", log.String())
	}
}
