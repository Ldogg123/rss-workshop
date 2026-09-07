package scheduler

import (
	"context"
	"errors"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"sync/atomic"
	"testing"
	"time"
)

type gateFetcher struct {
	active, peak, calls atomic.Int32
	release             chan struct{}
}

func (f *gateFetcher) Fetch(ctx context.Context, u, e, m string) (fetch.Result, error) {
	f.calls.Add(1)
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		old := f.peak.Load()
		if n <= old || f.peak.CompareAndSwap(old, n) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return fetch.Result{}, ctx.Err()
	case <-f.release:
		return fetch.Result{Body: []byte(`<article><h2>Hi</h2></article>`), URL: u, Status: 200}, nil
	}
}
func TestBoundsDedupAndPreview(t *testing.T) {
	s, e := openTestStore(t, 50)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &gateFetcher{release: make(chan struct{})}
	jobs := New(s, f, 2, time.Second)
	r := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}
	for i := 0; i < 5; i++ {
		if _, e = s.Save(ctx, model.Feed{Title: "Test", URL: "https://example.com", Recipe: r, Interval: 60, Enabled: true}); e != nil {
			t.Fatal(e)
		}
	}
	jobs.tick(ctx)
	jobs.tick(ctx)
	deadline := time.Now().Add(time.Second)
	for f.active.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.active.Load() != 2 {
		t.Fatal("jobs did not start")
	}
	if _, e = jobs.Preview(ctx, model.Feed{}); !errors.Is(e, ErrBusy) {
		t.Fatal("preview bypassed limit", e)
	}
	cancel()
	jobs.Wait()
	if f.peak.Load() > 2 || f.calls.Load() != 2 {
		t.Fatal("bound/dedup failed", f.peak.Load(), f.calls.Load())
	}
	active, _ := jobs.Stats()
	if active != 0 {
		t.Fatal("slots leaked")
	}
}

type staticFetcher struct {
	body []byte
	err  error
	etag string
}

func (f *staticFetcher) Fetch(ctx context.Context, u, e, m string) (fetch.Result, error) {
	f.etag = e
	return fetch.Result{Body: f.body, URL: u, Status: 200, ETag: "fixture"}, f.err
}
func TestEmptyRetainsItemsAndEditRefetches(t *testing.T) {
	ctx := context.Background()
	s, e := openTestStore(t, 50)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	f := model.Feed{Title: "Test", URL: "https://example.com", Interval: 60, Enabled: true, Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}}
	f.ID, _ = s.Save(ctx, f)
	f, _ = s.Get(ctx, f.ID)
	fake := &staticFetcher{body: []byte(`<article><h2>Good</h2></article>`)}
	jobs := New(s, fake, 1, time.Second)
	jobs.refresh(ctx, f)
	f, _ = s.Get(ctx, f.ID)
	fake.body = []byte(`<html></html>`)
	jobs.refresh(ctx, f)
	items, _ := s.Items(ctx, f.ID)
	f, _ = s.Get(ctx, f.ID)
	if len(items) != 1 || f.Error == "" {
		t.Fatal("empty refresh lost history or diagnostics")
	}
	if _, e = s.Save(ctx, f); e != nil {
		t.Fatal(e)
	}
	f, _ = s.Get(ctx, f.ID)
	jobs.refresh(ctx, f)
	if fake.etag != "" {
		t.Fatal("selector edit reused validator")
	}
}

type fakeRenderer struct {
	calls  int
	result fetch.Result
}

func (f *fakeRenderer) Render(ctx context.Context, u, s string, ms int) (fetch.Result, error) {
	f.calls++
	return f.result, nil
}
func TestAutoFallbackIsBounded(t *testing.T) {
	f := model.Feed{URL: "https://example.com", Recipe: model.Recipe{Mode: "auto", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}}
	static := &staticFetcher{body: []byte("<html></html>")}
	render := &fakeRenderer{result: fetch.Result{Body: []byte("<article><h2>Rendered</h2></article>"), URL: f.URL, Status: 200}}
	jobs := New(nil, static, 1, time.Second)
	jobs.Renderer = render
	_, p, e := jobs.extract(context.Background(), f, false)
	if e != nil || len(p.Items) != 1 || render.calls != 1 {
		t.Fatal("fallback failed", p, e)
	}
	static.err = &fetch.HTTPError{Status: 403}
	_, _, e = jobs.extract(context.Background(), f, false)
	if e == nil || render.calls != 1 {
		t.Fatal("HTTP error incorrectly triggered browser")
	}
	static.err = nil
	static.body = []byte("<article><h2>Static</h2></article>")
	_, p, e = jobs.extract(context.Background(), f, false)
	if e != nil || p.Items[0].Title != "Static" || render.calls != 1 {
		t.Fatal("valid static result rendered unnecessarily")
	}
}

func TestSnapshotSharesBoundsAndCancellation(t *testing.T) {
	f := &gateFetcher{release: make(chan struct{})}
	jobs := New(nil, f, 1, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := jobs.Snapshot(ctx, "https://example.com", model.Recipe{Mode: "static"}); done <- err }()
	deadline := time.Now().Add(time.Second)
	for f.active.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := jobs.Preview(context.Background(), model.Feed{}); !errors.Is(err, ErrBusy) {
		t.Fatal("snapshot bypassed shared slots", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("snapshot ignored cancellation", err)
	}
	if active, _ := jobs.Stats(); active != 0 {
		t.Fatal("snapshot leaked a slot")
	}
	r := &fakeRenderer{result: fetch.Result{Status: 200, Body: []byte("rendered")}}
	jobs.Renderer = r
	if out, err := jobs.Snapshot(context.Background(), "https://example.com", model.Recipe{Mode: "browser"}); err != nil || string(out.Body) != "rendered" || r.calls != 1 {
		t.Fatal("rendered snapshot failed", err)
	}
	if f.calls.Load() != 1 {
		t.Fatal("rendered snapshot also did a static fetch")
	}
}
