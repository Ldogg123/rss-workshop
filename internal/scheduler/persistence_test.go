package scheduler

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A failed write rolls back the whole transaction, including the feed's next
// run. Nothing is left in the database to slow the feed down, so without an
// in-memory hold the source is refetched on every tick for as long as the
// database stays unwritable -- which is how a full disk turns into a sustained
// scrape of every source in the library.
func TestUnsavedRefreshBacksOffInsteadOfRefetching(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(listPage(1), articlePages(1))
	r := fullRecipe()
	r.Full = nil
	jobs := New(s, p, 4, 10*time.Second)
	f := saveFeed(t, jobs, r)

	// Reads keep working, so the scheduler ticks exactly as it would in
	// production; only the write fails, as it would on a full disk.
	if _, err := s.DB.ExecContext(ctx,
		`CREATE TRIGGER block_item_writes BEFORE INSERT ON items BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	before, err := s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}

	var log bytes.Buffer
	restore := slog.Default()
	defer slog.SetDefault(restore)
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo})))

	for i := 0; i < 6; i++ {
		jobs.tick(ctx)
		time.Sleep(120 * time.Millisecond)
	}

	if got := p.calls.Load(); got != 1 {
		t.Errorf("source was fetched %d times after an unwritable database; want 1 before the hold takes effect", got)
	}
	after, err := s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.NextRun.Equal(before.NextRun) {
		t.Error("a rolled-back transaction somehow advanced the schedule")
	}
	// The operator must be told what happened and must not be told it worked.
	out := log.String()
	if !strings.Contains(out, `msg="refresh not saved"`) || !strings.Contains(out, "disk full") {
		t.Errorf("the failure did not report its reason:\n%s", out)
	}
	if strings.Contains(out, `msg="refresh finished"`) {
		t.Errorf("a refresh that stored nothing reported success:\n%s", out)
	}
	if !strings.Contains(out, "retry_in=") {
		t.Error("the log does not say when the feed will be tried again")
	}
}

// Once writes succeed again the feed returns to its normal schedule rather than
// staying held for the rest of the process's life.
func TestHoldClearsAfterASuccessfulSave(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(listPage(1), articlePages(1))
	r := fullRecipe()
	r.Full = nil
	jobs := New(s, p, 4, 10*time.Second)
	f := saveFeed(t, jobs, r)

	if _, err := s.DB.ExecContext(ctx,
		`CREATE TRIGGER block_item_writes BEFORE INSERT ON items BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	jobs.refresh(ctx, f)
	if !jobs.holding(f.ID) {
		t.Fatal("an unsaved refresh did not hold the feed")
	}
	if _, err := s.DB.ExecContext(ctx, "DROP TRIGGER block_item_writes"); err != nil {
		t.Fatal(err)
	}
	// A held feed is skipped by tick, so drive the recovery directly.
	jobs.refresh(ctx, f)
	if jobs.holding(f.ID) {
		t.Error("the feed stayed held after its result was stored")
	}
	items, err := s.Items(ctx, f.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("recovery stored %d items: %v", len(items), err)
	}
}

// Holds must not accumulate for feeds the operator has deleted.
func TestHoldsAreForgottenForDeletedFeeds(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(listPage(1), articlePages(1))
	r := fullRecipe()
	r.Full = nil
	jobs := New(s, p, 4, 10*time.Second)
	f := saveFeed(t, jobs, r)

	jobs.hold(f.ID)
	jobs.hold("a-feed-that-no-longer-exists")
	jobs.tick(ctx)
	if jobs.holding("a-feed-that-no-longer-exists") {
		t.Error("a hold survived for a feed that is not in the library")
	}
	if !jobs.holding(f.ID) {
		t.Error("a hold for a live feed was dropped")
	}
}
