package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
	"rss-workshop/internal/notify"
)

// interruptingFetcher runs a callback while the source is being fetched, which
// is when an operator's edit or delete lands during a real refresh.
type interruptingFetcher struct {
	during func()
	err    error
}

func (i *interruptingFetcher) Fetch(ctx context.Context, u, e, m string) (fetch.Result, error) {
	if i.during != nil {
		i.during()
	}
	if i.err != nil {
		return fetch.Result{}, i.err
	}
	return fetch.Result{Body: []byte(listPage(2)), URL: u, Status: 200}, nil
}

// An edit made while a refresh runs makes the store throw the result away,
// with no stories and no run record. The log used to report it as finished
// with an item count, or as failed with a failure count, for a run that
// Diagnostics never shows.
func TestStaleRefreshIsLoggedAsDiscarded(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"successful", nil}, {"failed", errors.New("source unavailable")}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, err := openTestStore(t, 50)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			source := &interruptingFetcher{err: tc.err}
			jobs := New(s, source, 1, 10*time.Second)
			r := fullRecipe()
			r.Full = nil
			f := saveFeed(t, jobs, r)
			source.during = func() {
				edited := f
				edited.Title = "Renamed"
				if _, err := s.Save(ctx, edited); err != nil {
					t.Error(err)
				}
			}
			// The edit releases a hold as Retry does, keeping its failures.
			jobs.hold(f.ID)
			jobs.hold(f.ID)
			log := captureLog(t)
			jobs.refresh(ctx, f)

			out := log.String()
			if !strings.Contains(out, `level=INFO msg="refresh discarded"`) || !strings.Contains(out, `reason="feed changed during refresh"`) {
				t.Errorf("the discarded refresh was not reported:\n%s", out)
			}
			for _, wrong := range []string{"refresh finished", "refresh failed", "refresh not saved"} {
				if strings.Contains(out, wrong) {
					t.Errorf("a discarded refresh logged %q:\n%s", wrong, out)
				}
			}
			stored, err := s.Get(ctx, f.ID)
			if err != nil || stored.Failures != 0 || stored.Error != "" || stored.Count != 0 {
				t.Fatalf("the discarded result was stored: %+v, %v", stored, err)
			}
			if runs, err := s.Runs(ctx, f.ID); err != nil || len(runs) != 0 {
				t.Fatalf("the discarded result left %d runs: %v", len(runs), err)
			}
			jobs.mu.Lock()
			h := jobs.held[f.ID]
			jobs.mu.Unlock()
			if jobs.Held(f.ID) || h.failures != 2 {
				t.Errorf("after a discarded refresh: held=%v with %d failures; want released with 2", jobs.Held(f.ID), h.failures)
			}
		})
	}
}

// Deleting a feed while it refreshes is an ordinary action. It used to be
// logged at ERROR as a database that refused the write, and the deleted feed
// was held as if the disk were full.
func TestFeedDeletedDuringItsRefreshIsDiscarded(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	source := &interruptingFetcher{}
	jobs := New(s, source, 1, 10*time.Second)
	r := fullRecipe()
	r.Full = nil
	f := saveFeed(t, jobs, r)
	source.during = func() {
		if err := s.Delete(ctx, f.ID); err != nil {
			t.Error(err)
		}
	}
	log := captureLog(t)
	jobs.refresh(ctx, f)

	out := log.String()
	if !strings.Contains(out, `level=INFO msg="refresh discarded"`) || !strings.Contains(out, `reason="feed deleted during refresh"`) {
		t.Errorf("the deleted feed's refresh was not reported as discarded:\n%s", out)
	}
	for _, wrong := range []string{"level=ERROR", "refresh not saved", "refresh finished", "refresh failed"} {
		if strings.Contains(out, wrong) {
			t.Errorf("a deleted feed's refresh logged %q:\n%s", wrong, out)
		}
	}
	jobs.mu.Lock()
	_, held := jobs.held[f.ID]
	jobs.mu.Unlock()
	if held {
		t.Error("a deleted feed was held as a failed write")
	}
}

// stallingFetcher answers only when its context ends, as a slow source does
// when the server stops or the refresh's own deadline passes.
type stallingFetcher struct{ started chan struct{} }

func (f *stallingFetcher) Fetch(ctx context.Context, u, e, m string) (fetch.Result, error) {
	f.started <- struct{}{}
	<-ctx.Done()
	return fetch.Result{}, ctx.Err()
}

// Stopping the server cancels the refreshes still running. Their failure says
// nothing about the source, yet it used to be stored: every restart could add
// a "context canceled" failure and a backoff to a healthy feed, and with
// notifications on, send a failing alert for it. Such a failure is discarded
// like an edited feed's result, so the feed is still due at the next start.
// The refresh's own deadline is still the source's failure, and a result that
// succeeded before the shutdown is still stored.
func TestRefreshStoppedByShutdownIsDiscarded(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	source := &stallingFetcher{started: make(chan struct{}, 1)}
	jobs := New(s, source, 1, 10*time.Second)
	h, _ := alerting(jobs, 1)
	f := namedFeed(t, s, "Healthy feed")
	log := captureLog(t)

	parent, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		jobs.refresh(parent, f)
	}()
	<-source.started
	stop()
	<-done
	out := log.String()
	if !strings.Contains(out, `level=INFO msg="refresh discarded"`) || !strings.Contains(out, `reason="server stopping"`) || strings.Contains(out, "refresh failed") {
		t.Errorf("the interrupted refresh was not reported as discarded:\n%s", out)
	}
	stored := reload(t, s, f.ID)
	if stored.Failures != 0 || stored.Error != "" || !stored.NextRun.Equal(f.NextRun) || !stored.LastAttempt.Equal(f.LastAttempt) {
		t.Fatalf("the interrupted refresh was stored: failures=%d error=%q next_run %v, was %v", stored.Failures, stored.Error, stored.NextRun, f.NextRun)
	}
	if runs, err := s.Runs(ctx, f.ID); err != nil || len(runs) != 0 {
		t.Fatalf("the interrupted refresh left %d runs: %v", len(runs), err)
	}
	if events := h.take(); len(events) != 0 {
		t.Fatalf("stopping the server raised %v", kinds(events))
	}

	// The refresh's own deadline is a source that did not answer in time.
	jobs.Timeout = 20 * time.Millisecond
	jobs.refresh(ctx, f)
	<-source.started
	if stored := reload(t, s, f.ID); stored.Failures != 1 || stored.Error == "" {
		t.Fatalf("a timed-out refresh stored failures=%d error=%q", stored.Failures, stored.Error)
	}
	if events := h.take(); len(events) != 1 || events[0].Kind != notify.Failing {
		t.Fatalf("a timed-out refresh at the threshold raised %v", kinds(events))
	}

	// A list page that arrived before the shutdown is a success to keep.
	parent, stop = context.WithCancel(ctx)
	defer stop()
	finished := New(s, &interruptingFetcher{during: stop}, 1, 10*time.Second)
	finished.refresh(parent, reload(t, s, f.ID))
	if stored := reload(t, s, f.ID); stored.Failures != 0 || stored.Count != 2 || stored.LastSuccess.IsZero() {
		t.Fatalf("a success finished during shutdown was not stored: failures=%d count=%d", stored.Failures, stored.Count)
	}
}
