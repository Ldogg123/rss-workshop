package scheduler

import (
	"context"
	"testing"
	"time"

	"rss-workshop/internal/model"
)

// A refresh can end while a tick is still reading the feed list, which then
// shows the feed due because its new schedule was saved after the read. That
// tick must not start the feed again: with FlareSolverr it would spend a
// solver slot on a page just fetched, ahead of feeds still waiting their turn.
func TestRefreshIsNotRestartedFromAListReadBeforeItEnded(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	fetcher := &gateFetcher{release: make(chan struct{})}
	close(fetcher.release)
	jobs := New(s, fetcher, 4, time.Second)
	if _, err := s.Save(ctx, model.Feed{Title: "Plain", URL: "https://example.com/plain", Interval: 60, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}}); err != nil {
		t.Fatal(err)
	}
	listed := time.Now()
	stale, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	jobs.tick(ctx)
	jobs.Wait()
	jobs.dispatch(ctx, stale, listed)
	jobs.Wait()
	if fetcher.calls.Load() != 1 {
		t.Fatalf("a list read before the refresh ended started it again: %d fetches", fetcher.calls.Load())
	}
	jobs.tick(ctx)
	jobs.Wait()
	jobs.mu.Lock()
	remembered := len(jobs.finished)
	jobs.mu.Unlock()
	if fetcher.calls.Load() != 1 || remembered != 0 {
		t.Fatalf("fetches=%d, end times kept=%d", fetcher.calls.Load(), remembered)
	}
}
