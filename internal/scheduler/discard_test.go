package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
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
