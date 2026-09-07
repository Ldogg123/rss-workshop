package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
)

type solverFetcher struct {
	fetch func(context.Context, string, string, string) (fetch.Result, error)
}

func (f *solverFetcher) Fetch(ctx context.Context, u, etag, modified string) (fetch.Result, error) {
	return f.fetch(ctx, u, etag, modified)
}
func (*solverFetcher) Stats() (int, int) { return 0, 1 }

func solverFeed() model.Feed {
	return model.Feed{Title: "Protected news", URL: "https://example.com/news", Interval: 60, Enabled: true,
		ETag: "old-static-etag", LastModified: "old-static-modified",
		Recipe: model.Recipe{Mode: "flaresolverr", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}}
}

func TestFlareSolverrRoutesAllFetchPathsWithSeparateDeadline(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f := solverFeed()
	f.ID, err = s.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	f, err = s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.ETag, f.LastModified = "old-static-etag", "old-static-modified"
	// Existing static validators must never be sent to the solver, including
	// when the user switches a previously refreshed feed to this mode.
	static := &gateFetcher{release: make(chan struct{})}
	renderer := &fakeRenderer{}
	jobs := New(s, static, 1, time.Second)
	jobs.Renderer = renderer
	jobs.FlareSolverrTimeout = 20 * time.Second
	calls := 0
	jobs.FlareSolverr = &solverFetcher{fetch: func(ctx context.Context, u, etag, modified string) (fetch.Result, error) {
		calls++
		if u != f.URL || etag != "" || modified != "" {
			t.Errorf("solver request = %q, %q, %q", u, etag, modified)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 15*time.Second || time.Until(deadline) > jobs.FlareSolverrTimeout {
			t.Errorf("solver did not receive its separate deadline: %v, %v", deadline, ok)
		}
		return fetch.Result{Body: []byte(`<article><h2>Solved story</h2></article>`), URL: u, Status: 200}, nil
	}}
	if p, err := jobs.Preview(ctx, f); err != nil || len(p.Items) != 1 || p.Items[0].Title != "Solved story" {
		t.Fatalf("solver preview: %+v, %v", p, err)
	}
	if r, err := jobs.Snapshot(ctx, f.URL, f.Recipe); err != nil || !strings.Contains(string(r.Body), "Solved story") {
		t.Fatalf("solver snapshot: %+v, %v", r, err)
	}
	jobs.refresh(ctx, f)
	items, err := s.Items(ctx, f.ID)
	if err != nil || len(items) != 1 || items[0].Title != "Solved story" {
		t.Fatalf("solver refresh did not persist items: %+v, %v", items, err)
	}
	if calls != 3 || static.calls.Load() != 0 || renderer.calls != 0 {
		t.Fatalf("unexpected fetch paths: solver=%d static=%d browser=%d", calls, static.calls.Load(), renderer.calls)
	}
}

func TestFlareSolverrFailureKeepsHistoryWithoutFallback(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f := solverFeed()
	f.ID, err = s.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	f, err = s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	static := &gateFetcher{release: make(chan struct{})}
	renderer := &fakeRenderer{}
	jobs := New(s, static, 1, time.Second)
	jobs.Renderer = renderer
	jobs.FlareSolverrTimeout = time.Second
	body := []byte(`<article><h2>Last good story</h2></article>`)
	var solveErr error
	jobs.FlareSolverr = &solverFetcher{fetch: func(ctx context.Context, u, _, _ string) (fetch.Result, error) {
		return fetch.Result{Body: body, URL: u, Status: 200}, solveErr
	}}
	jobs.refresh(ctx, f)
	f, _ = s.Get(ctx, f.ID)
	initial, err := s.Items(ctx, f.ID)
	if err != nil || len(initial) != 1 || f.LastSuccess.IsZero() {
		t.Fatalf("missing initial success: %+v, %v", initial, err)
	}
	for _, failure := range []struct {
		name, body string
		err        error
	}{
		{"unsolved challenge", "", errors.New("challenge could not be solved")},
		{"empty page", "<html><p>No matching stories</p></html>", nil},
	} {
		t.Run(failure.name, func(t *testing.T) {
			body, solveErr = []byte(failure.body), failure.err
			jobs.refresh(ctx, f)
			stored, err := s.Get(ctx, f.ID)
			if err != nil || stored.Error == "" || !stored.LastSuccess.Equal(f.LastSuccess) {
				t.Fatalf("missing failure or last success changed: %+v, %v", stored, err)
			}
			items, err := s.Items(ctx, f.ID)
			if err != nil || len(items) != 1 || items[0].GUID != initial[0].GUID || items[0].Title != initial[0].Title || !items[0].Published.Equal(initial[0].Published) {
				t.Fatalf("failure changed stored history: %+v, %v", items, err)
			}
			f = stored
		})
	}
	if static.calls.Load() != 0 || renderer.calls != 0 {
		t.Fatal("solver failure triggered an unrelated fetch method")
	}
}

func TestFlareSolverrSharesWorkerBoundAndHonorsCancellation(t *testing.T) {
	for _, path := range []string{"preview", "snapshot", "scheduled refresh"} {
		t.Run(path, func(t *testing.T) {
			s, err := openTestStore(t, 50)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			gate := &gateFetcher{release: make(chan struct{})}
			static := &gateFetcher{release: make(chan struct{})}
			jobs := New(s, static, 1, time.Second)
			jobs.FlareSolverrTimeout = 20 * time.Second
			jobs.FlareSolverr = &solverFetcher{fetch: gate.Fetch}
			f := solverFeed()
			done := make(chan error, 1)
			switch path {
			case "preview":
				go func() { _, err := jobs.Preview(ctx, f); done <- err }()
			case "snapshot":
				go func() { _, err := jobs.Snapshot(ctx, f.URL, f.Recipe); done <- err }()
			case "scheduled refresh":
				if _, err := s.Save(ctx, f); err != nil {
					t.Fatal(err)
				}
				jobs.tick(ctx)
				jobs.tick(ctx)
			}
			deadline := time.Now().Add(time.Second)
			for gate.active.Load() != 1 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if gate.active.Load() != 1 {
				t.Fatal("solver did not start")
			}
			if _, err := jobs.Preview(context.Background(), f); !errors.Is(err, ErrBusy) {
				t.Fatal("solver bypassed shared preview bound", err)
			}
			if _, err := jobs.Snapshot(context.Background(), f.URL, model.Recipe{Mode: "static"}); !errors.Is(err, ErrBusy) {
				t.Fatal("static snapshot bypassed occupied solver slot", err)
			}
			cancel()
			if path == "scheduled refresh" {
				jobs.Wait()
			} else if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal("solver ignored caller cancellation", err)
			}
			if active, _ := jobs.Stats(); active != 0 || gate.calls.Load() != 1 || static.calls.Load() != 0 {
				t.Fatalf("solver leaked slot or duplicate fetch: active=%d solver=%d static=%d", active, gate.calls.Load(), static.calls.Load())
			}
		})
	}
}

func TestFlareSolverrMissingConfiguration(t *testing.T) {
	static := &gateFetcher{release: make(chan struct{})}
	jobs := New(nil, static, 1, time.Second)
	f := solverFeed()
	_, previewErr := jobs.Preview(context.Background(), f)
	_, snapshotErr := jobs.Snapshot(context.Background(), f.URL, f.Recipe)
	for _, err := range []error{previewErr, snapshotErr} {
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "flaresolverr") {
			t.Fatal("missing solver should give a clear configuration error", err)
		}
	}
	if active, _ := jobs.Stats(); active != 0 || static.calls.Load() != 0 {
		t.Fatal("missing solver leaked a slot or fetched statically")
	}
}
