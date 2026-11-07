package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"rss-workshop/internal/model"
	"rss-workshop/internal/notify"
	"rss-workshop/internal/store"
)

// heard records the events a scheduler raises.
type heard struct {
	mu     sync.Mutex
	events []notify.Event
}

func (h *heard) Notify(e notify.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
}

// take returns the events since the last call.
func (h *heard) take() []notify.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.events
	h.events = nil
	return out
}

func kinds(events []notify.Event) []notify.Kind {
	out := []notify.Kind{}
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

// fakeClock lets a test move the hourly limits on without waiting.
type fakeClock struct{ now time.Time }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func alerting(jobs *Scheduler, after int) (*heard, *fakeClock) {
	h, clock := &heard{}, &fakeClock{now: time.Now()}
	jobs.Notifier, jobs.NotifyAfter = h, after
	jobs.alerts.now = func() time.Time { return clock.now }
	return h, clock
}

func namedFeed(t *testing.T, s *store.Store, title string) model.Feed {
	t.Helper()
	r := fullRecipe()
	r.Full = nil
	id, err := s.Save(context.Background(), model.Feed{Title: title, URL: "https://example.com/list", Interval: 60, Enabled: true, Recipe: r})
	if err != nil {
		t.Fatal(err)
	}
	return reload(t, s, id)
}

func reload(t *testing.T, s *store.Store, id string) model.Feed {
	t.Helper()
	f, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// Events are edges of the failure count the store already keeps: one when a
// streak reaches the threshold, one when it ends. A restart reads the same
// count, so it neither repeats an alert nor misses a recovery.
func TestFailingAndRecoveredFollowTheStoredFailureCount(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	source := &interruptingFetcher{err: errors.New("source returned HTTP 503")}
	jobs := New(s, source, 1, 10*time.Second)
	h, _ := alerting(jobs, 3)
	f := namedFeed(t, s, "Evening news")
	run := func(times int) []notify.Event {
		for range times {
			jobs.refresh(ctx, reload(t, s, f.ID))
		}
		return h.take()
	}

	if events := run(2); len(events) != 0 {
		t.Fatalf("below the threshold: %v", kinds(events))
	}
	events := run(1)
	stored := reload(t, s, f.ID)
	want := notify.Event{Kind: notify.Failing, Feed: "Evening news", FeedID: f.ID, Failures: 3, Error: stored.Error}
	if len(events) != 1 || events[0] != want || stored.Failures != 3 {
		t.Fatalf("at the threshold: %+v; want %+v", events, want)
	}
	// The usual recovery, from exactly the threshold.
	source.err = nil
	if events := run(1); len(events) != 1 || events[0] != (notify.Event{Kind: notify.Recovered, Feed: "Evening news", FeedID: f.ID, Failures: 3}) {
		t.Fatalf("recovery at the threshold: %+v", events)
	}

	source.err = errors.New("source returned HTTP 503")
	if events := run(3); len(events) != 1 || events[0].Kind != notify.Failing {
		t.Fatalf("a streak after a recovery: %v", kinds(events))
	}
	if events := run(3); len(events) != 0 {
		t.Fatalf("past the threshold: %v", kinds(events))
	}

	source.err = nil
	events = run(1)
	if len(events) != 1 || events[0] != (notify.Event{Kind: notify.Recovered, Feed: "Evening news", FeedID: f.ID, Failures: 6}) {
		t.Fatalf("recovery: %+v", events)
	}
	if events := run(2); len(events) != 0 {
		t.Fatalf("after recovery: %v", kinds(events))
	}

	// A new streak alerts again, and a restart in the middle of it neither
	// repeats that alert nor forgets to report the recovery.
	source.err = errors.New("source returned HTTP 503")
	if events := run(3); len(events) != 1 || events[0].Kind != notify.Failing {
		t.Fatalf("second streak: %v", kinds(events))
	}
	jobs = New(s, source, 1, 10*time.Second)
	h, _ = alerting(jobs, 3)
	if events := run(2); len(events) != 0 {
		t.Fatalf("after a restart: %v", kinds(events))
	}
	source.err = nil
	if events := run(1); len(events) != 1 || events[0].Kind != notify.Recovered || events[0].Failures != 5 {
		t.Fatalf("recovery after a restart: %+v", events)
	}

	// A feed below the threshold recovers silently.
	source.err = errors.New("source returned HTTP 503")
	run(1)
	source.err = nil
	if events := run(1); len(events) != 0 {
		t.Fatalf("a single failure reported a recovery: %v", kinds(events))
	}
}

// Saving a feed in the editor resets its failure count, and the editor is how
// an operator repairs a broken selector. A feed reported as failing still
// reports its recovery after that, or its failing alert would stand
// unanswered; one that never reached the threshold stays silent.
func TestRecoveryAfterAnEditIsStillReported(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	source := &interruptingFetcher{err: errors.New("source returned HTTP 503")}
	jobs := New(s, source, 1, 10*time.Second)
	h, _ := alerting(jobs, 3)
	captureLog(t)
	f := namedFeed(t, s, "Evening news")
	run := func(times int) []notify.Event {
		for range times {
			jobs.refresh(ctx, reload(t, s, f.ID))
		}
		return h.take()
	}
	edit := func() {
		t.Helper()
		if _, err := s.Save(ctx, reload(t, s, f.ID)); err != nil {
			t.Fatal(err)
		}
		if stored := reload(t, s, f.ID); stored.Failures != 0 {
			t.Fatalf("the save kept %d failures", stored.Failures)
		}
	}

	if events := run(3); len(events) != 1 || events[0].Kind != notify.Failing {
		t.Fatalf("at the threshold: %v", kinds(events))
	}
	edit()
	source.err = nil
	// A tick performs the recovering refresh, as in the running app: the save
	// made the feed due, and the tick first forgets reported feeds that no
	// longer exist, which must keep this one.
	jobs.tick(ctx)
	jobs.Wait()
	if events := h.take(); len(events) != 1 || events[0] != (notify.Event{Kind: notify.Recovered, Feed: "Evening news", FeedID: f.ID}) {
		t.Fatalf("recovery after an edit and a tick: %+v", events)
	}
	if events := run(1); len(events) != 0 {
		t.Fatalf("a second success: %v", kinds(events))
	}

	// An edit that does not fix it: the feed fails again below the threshold,
	// then recovers, which still answers the alert.
	source.err = errors.New("source returned HTTP 503")
	if events := run(3); len(events) != 1 || events[0].Kind != notify.Failing {
		t.Fatalf("a new streak: %v", kinds(events))
	}
	edit()
	run(1)
	source.err = nil
	if events := run(1); len(events) != 1 || events[0].Kind != notify.Recovered || events[0].Failures != 1 {
		t.Fatalf("recovery after an edit and a failure: %+v", events)
	}

	// Below the threshold nothing was reported, so nothing is answered.
	source.err = errors.New("source returned HTTP 503")
	run(2)
	edit()
	source.err = nil
	if events := run(1); len(events) != 0 {
		t.Fatalf("a feed that never reached the threshold: %v", kinds(events))
	}

	// What is remembered for a deleted feed goes with it.
	source.err = errors.New("source returned HTTP 503")
	run(3)
	if err := s.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	jobs.tick(ctx)
	jobs.Wait()
	jobs.alerts.mu.Lock()
	left := len(jobs.alerts.reported)
	jobs.alerts.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d reported feeds remembered after the delete", left)
	}
}

// A result thrown away because the feed was edited or deleted changed nothing
// stored, so it is neither a failure nor a recovery.
func TestDiscardedRefreshesRaiseNoEvent(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	source := &interruptingFetcher{err: errors.New("source unavailable")}
	jobs := New(s, source, 1, 10*time.Second)
	h, _ := alerting(jobs, 1)

	edited := namedFeed(t, s, "Edited")
	source.during = func() {
		changed := edited
		changed.Title = "Renamed"
		if _, err := s.Save(ctx, changed); err != nil {
			t.Error(err)
		}
	}
	jobs.refresh(ctx, edited)
	deleted := namedFeed(t, s, "Deleted")
	source.during = func() {
		if err := s.Delete(ctx, deleted.ID); err != nil {
			t.Error(err)
		}
	}
	jobs.refresh(ctx, deleted)
	if events := h.take(); len(events) != 0 {
		t.Fatalf("discarded failures raised %v", kinds(events))
	}

	// The same failure, stored, is an event.
	source.during = nil
	kept := namedFeed(t, s, "Kept")
	jobs.refresh(ctx, kept)
	if events := h.take(); len(events) != 1 || events[0].Kind != notify.Failing || events[0].Feed != "Kept" {
		t.Fatalf("a stored failure at the threshold raised %v", kinds(events))
	}

	// A success thrown away is not a recovery either.
	source.err = nil
	failed := reload(t, s, kept.ID)
	source.during = func() {
		changed := failed
		changed.Title = "Kept, renamed"
		if _, err := s.Save(ctx, changed); err != nil {
			t.Error(err)
		}
	}
	jobs.refresh(ctx, failed)
	if events := h.take(); len(events) != 0 {
		t.Fatalf("a discarded success raised %v", kinds(events))
	}
}

// A database that refuses one result usually refuses them all, so not_saved
// goes out at most once an hour across every feed, and again while results
// are still refused, whichever feed's hold that is.
func TestNotSavedIsReportedAtMostHourlyWhileItLasts(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	jobs := New(s, newPageFetcher(listPage(1), nil), 1, 10*time.Second)
	h, clock := alerting(jobs, 3)
	captureLog(t)
	a, b, c := namedFeed(t, s, "Alpha"), namedFeed(t, s, "Beta"), namedFeed(t, s, "Gamma")
	restore := blockItemWrites(t, s)
	defer restore()

	jobs.refresh(ctx, a)
	events := h.take()
	if len(events) != 1 || events[0].Kind != notify.NotSaved || events[0].Feed != "Alpha" || events[0].FeedID != a.ID || events[0].Error == "" {
		t.Fatalf("first hold: %+v", events)
	}
	if !strings.Contains(events[0].Error, "disk full") && !strings.Contains(events[0].Error, "PostgreSQL") {
		t.Errorf("the event does not say why: %q", events[0].Error)
	}
	clock.advance(10 * time.Minute)
	jobs.refresh(ctx, b)
	if events := h.take(); len(events) != 0 {
		t.Fatalf("a second feed within the hour: %v", kinds(events))
	}
	// Retry keeps Alpha's hold count, so this is its second hold in a row.
	jobs.Retry(a.ID)
	jobs.refresh(ctx, a)
	if events := h.take(); len(events) != 0 {
		t.Fatalf("a held feed failing again within the hour: %v", kinds(events))
	}

	clock.advance(50 * time.Minute)
	jobs.Retry(a.ID)
	jobs.refresh(ctx, a)
	if events := h.take(); len(events) != 1 || events[0].Kind != notify.NotSaved || events[0].Feed != "Alpha" {
		t.Fatalf("a held feed failing again an hour on: %+v", events)
	}
	jobs.refresh(ctx, c)
	if events := h.take(); len(events) != 0 {
		t.Fatalf("a third feed within the hour of that: %v", kinds(events))
	}
	clock.advance(time.Hour)
	jobs.Retry(b.ID)
	jobs.refresh(ctx, b)
	if events := h.take(); len(events) != 1 || events[0].Feed != "Beta" {
		t.Fatalf("another feed's hold an hour on: %+v", events)
	}
}

// A disk that fills again soon after space was freed starts a second outage
// inside the hour of the first one's alert. The hourly limit may delay that
// report, but must not lose it: reads still work, so db_unavailable stays
// silent, and nothing is stored, so no failure count moves towards failing.
func TestNotSavedSoonAfterAnOutageIsReportedOnceTheHourPasses(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	jobs := New(s, newPageFetcher(listPage(1), nil), 1, 10*time.Second)
	h, clock := alerting(jobs, 3)
	captureLog(t)
	a, b := namedFeed(t, s, "Alpha"), namedFeed(t, s, "Beta")
	restore := blockItemWrites(t, s)
	jobs.refresh(ctx, a)
	if events := h.take(); len(events) != 1 || events[0].Kind != notify.NotSaved {
		t.Fatalf("first outage: %+v", events)
	}

	// Space is freed, and Alpha's next try is stored.
	clock.advance(5 * time.Minute)
	restore()
	jobs.Retry(a.ID)
	jobs.refresh(ctx, reload(t, s, a.ID))
	if jobs.Held(a.ID) {
		t.Fatal("a stored result left Alpha held")
	}

	// The disk fills again and stays full. Each round retries both feeds ten
	// minutes apart, which is how a library on short intervals keeps meeting
	// the refusal.
	restore = blockItemWrites(t, s)
	defer restore()
	clock.advance(15 * time.Minute)
	start := clock.now
	var reported []time.Duration
	for range 13 {
		for _, f := range []model.Feed{a, b} {
			jobs.Retry(f.ID)
			jobs.refresh(ctx, reload(t, s, f.ID))
			if !jobs.Held(f.ID) {
				t.Fatalf("%s was not held, so the round tested nothing", f.Title)
			}
		}
		for _, e := range h.take() {
			if e.Kind != notify.NotSaved {
				t.Fatalf("unexpected %s event", e.Kind)
			}
			reported = append(reported, clock.now.Sub(start))
		}
		clock.advance(10 * time.Minute)
	}
	// The first alert went out 20 minutes before the second outage began, so
	// its report waits 40 minutes, and repeats hourly while the outage lasts.
	if want := []time.Duration{40 * time.Minute, 100 * time.Minute}; !slices.Equal(reported, want) {
		t.Fatalf("a second outage over two hours was reported at %v; want %v", reported, want)
	}
}

// When the library cannot be read, the tick ends before any refresh, so no
// feed event could report it. The tick reports it, hourly while it lasts, and
// its end once.
func TestDatabaseOutageIsReportedHourlyAndItsEndOnce(t *testing.T) {
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	broken, err := store.Open(filepath.Join(t.TempDir(), "broken.db"), 50)
	if err != nil {
		t.Fatal(err)
	}
	broken.DB.Close()
	jobs := New(broken, newPageFetcher(listPage(1), nil), 1, 10*time.Second)
	h, clock := alerting(jobs, 3)
	captureLog(t)
	ctx := context.Background()
	tick := func(times int) []notify.Event {
		for range times {
			jobs.tick(ctx)
		}
		return h.take()
	}

	events := tick(5)
	if len(events) != 1 || events[0].Kind != notify.DBUnavailable || events[0].Error == "" || events[0].Feed != "" {
		t.Fatalf("outage: %+v", events)
	}
	clock.advance(59 * time.Minute)
	if events := tick(3); len(events) != 0 {
		t.Fatalf("within the hour: %v", kinds(events))
	}
	clock.advance(time.Minute)
	if events := tick(3); len(events) != 1 || events[0].Kind != notify.DBUnavailable {
		t.Fatalf("an hour on: %v", kinds(events))
	}

	jobs.Store = s
	if events := tick(3); len(events) != 1 || events[0] != (notify.Event{Kind: notify.DBAvailable}) {
		t.Fatalf("recovery: %+v", events)
	}

	// A second outage within the hour is not announced, so neither is its end.
	jobs.Store = broken
	clock.advance(10 * time.Minute)
	if events := tick(3); len(events) != 0 {
		t.Fatalf("a second outage within the hour: %v", kinds(events))
	}
	jobs.Store = s
	if events := tick(3); len(events) != 0 {
		t.Fatalf("the end of an unannounced outage: %v", kinds(events))
	}

	// A tick cut short by shutdown is not an outage.
	jobs.Store = broken
	clock.advance(2 * time.Hour)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	jobs.tick(canceled)
	if events := h.take(); len(events) != 0 {
		t.Fatalf("shutdown raised %v", kinds(events))
	}
}

// Notify must never hold up a refresh: a slow or unreachable notification
// service fills the queue, and the next event is dropped instead.
func TestFullNotificationQueueDoesNotDelayARefresh(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	n, err := notify.New("http://127.0.0.1:9/never-started", notify.JSON, "", "http://localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	// Never started, so nothing drains the queue.
	for range notify.QueueSize {
		n.Notify(notify.Event{Kind: notify.Recovered, Feed: "Filler"})
	}
	jobs := New(s, &interruptingFetcher{err: errors.New("source unavailable")}, 1, 10*time.Second)
	jobs.Notifier, jobs.NotifyAfter = n, 1
	f := namedFeed(t, s, "Evening news")
	done := make(chan struct{})
	go func() {
		defer close(done)
		jobs.refresh(ctx, f)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh waited for room in the notification queue")
	}
	if stored := reload(t, s, f.ID); stored.Failures != 1 || stored.Error == "" {
		t.Errorf("the refresh stored failures=%d error=%q", stored.Failures, stored.Error)
	}
	if _, _, dropped := n.Stats(); dropped != 1 {
		t.Errorf("dropped %d events; want the failing event counted", dropped)
	}
}
