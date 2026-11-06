package scheduler

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/store"
	"rss-workshop/internal/testutil"
)

// blockItemWrites makes storing an item fail while reads keep working, which is
// what a full disk or a revoked permission looks like to the scheduler. The
// PostgreSQL matrix runs these same tests with that backend's trigger.
func blockItemWrites(t *testing.T, s *store.Store) func() {
	t.Helper()
	return testutil.BlockItemWrites(t, s.DB)
}

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
	blockItemWrites(t, s)
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
	// The reason itself is backend-specific: SQLite passes the message through,
	// while the PostgreSQL store deliberately reduces driver errors to an
	// identity and SQLSTATE so connection settings and row values cannot reach a
	// log. Either way the line has to carry a non-empty reason.
	out := log.String()
	if !strings.Contains(out, `msg="refresh not saved"`) {
		t.Errorf("the failure was not reported:\n%s", out)
	}
	if !strings.Contains(out, "error=") || strings.Contains(out, `error=""`) {
		t.Errorf("the failure did not report a reason:\n%s", out)
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

	restore := blockItemWrites(t, s)
	jobs.refresh(ctx, f)
	if !jobs.Held(f.ID) {
		t.Fatal("an unsaved refresh did not hold the feed")
	}
	restore()
	// A held feed is skipped by tick, so drive the recovery directly.
	jobs.refresh(ctx, f)
	if jobs.Held(f.ID) {
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
	if jobs.Held("a-feed-that-no-longer-exists") {
		t.Error("a hold survived for a feed that is not in the library")
	}
	if !jobs.Held(f.ID) {
		t.Error("a hold for a live feed was dropped")
	}
}

// A manual refresh, an editor save or a resume shows that the database
// accepts a write for the feed again. Each must let a held feed run now rather
// than when the hold expires: before Retry, Refresh said "queued" and then
// nothing happened for up to 17 hours. The failure count is kept, so a write
// that fails again holds the feed twice as long as before instead of starting
// over at two minutes.
func TestRetryReleasesAHoldButKeepsItsBackoff(t *testing.T) {
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
	tick := func() { t.Helper(); jobs.tick(ctx); jobs.Wait() }
	retryIn := func() time.Duration {
		jobs.mu.Lock()
		defer jobs.mu.Unlock()
		return time.Until(jobs.held[f.ID].until)
	}
	near := func(got, want time.Duration) bool { return got <= want && got > want-10*time.Second }

	restore := blockItemWrites(t, s)
	tick()
	if got := p.calls.Load(); got != 1 || !jobs.Held(f.ID) || !near(retryIn(), 2*time.Minute) {
		t.Fatalf("first unsaved refresh: %d fetches, held=%v for %v", got, jobs.Held(f.ID), retryIn())
	}
	// Queueing alone is what the Refresh button used to do: the hold still wins.
	if err := s.Queue(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	tick()
	if got := p.calls.Load(); got != 1 {
		t.Fatalf("a held feed was fetched %d times without a retry", got)
	}
	jobs.Retry(f.ID)
	if jobs.Held(f.ID) {
		t.Fatal("Retry left the feed held")
	}
	tick()
	tick()
	if got := p.calls.Load(); got != 2 {
		t.Fatalf("source fetched %d times; want one retry, then a new hold", got)
	}
	if !jobs.Held(f.ID) || !near(retryIn(), 4*time.Minute) {
		t.Fatalf("a second unsaved refresh held the feed for %v; want twice the first hold", retryIn())
	}

	// A stored result forgets the failures entirely.
	restore()
	jobs.Retry(f.ID)
	tick()
	if items, err := s.Items(ctx, f.ID); err != nil || len(items) != 1 || jobs.Held(f.ID) {
		t.Fatalf("recovery stored %d items, held=%v: %v", len(items), jobs.Held(f.ID), err)
	}
	// Retrying a feed that was never held creates nothing to forget.
	jobs.Retry("a-feed-that-was-never-held")
	jobs.mu.Lock()
	_, remembered := jobs.held[f.ID]
	entries := len(jobs.held)
	jobs.mu.Unlock()
	if remembered {
		t.Error("a stored result kept the feed's hold failures")
	}
	if entries != 0 {
		t.Error("Retry created a hold")
	}
}
