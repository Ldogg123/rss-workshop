package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

var ErrBusy = errors.New("all refresh slots are busy; try again shortly")

type Renderer interface {
	Render(context.Context, string, string, int) (fetch.Result, error)
}
type Solver interface {
	fetch.Fetcher
	Stats() (int, int)
}
type Scheduler struct {
	Renderer            Renderer
	FlareSolverr        Solver
	FlareSolverrTimeout time.Duration

	Store   *store.Store
	Fetcher fetch.Fetcher
	Timeout time.Duration
	slots   chan struct{}
	mu      sync.Mutex
	active  map[string]bool
	wg      sync.WaitGroup
}

func New(s *store.Store, f fetch.Fetcher, workers int, timeout time.Duration) *Scheduler {
	return &Scheduler{Store: s, Fetcher: f, Timeout: timeout, slots: make(chan struct{}, workers), active: map[string]bool{}}
}
func (s *Scheduler) Stats() (int, int) { return len(s.slots), cap(s.slots) }
func (s *Scheduler) timeout(mode string) time.Duration {
	if mode == "flaresolverr" && s.FlareSolverrTimeout > 0 {
		return s.FlareSolverrTimeout
	}
	return s.Timeout
}
func (s *Scheduler) reserve(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[id] {
		return false
	}
	select {
	case s.slots <- struct{}{}:
		s.active[id] = true
		return true
	default:
		return false
	}
}
func (s *Scheduler) release(id string) { s.mu.Lock(); delete(s.active, id); <-s.slots; s.mu.Unlock() }
func (s *Scheduler) Preview(ctx context.Context, f model.Feed) (model.Preview, error) {
	id := store.ID()
	if !s.reserve(id) {
		return model.Preview{}, ErrBusy
	}
	defer s.release(id)
	ctx, cancel := context.WithTimeout(ctx, s.timeout(f.Recipe.Mode))
	defer cancel()
	_, p, e := s.extract(ctx, f, true)
	return p, e
}

// Snapshot shares job slots, deadlines and outbound policy with feed refreshes.
// With no recipe yet, auto cannot decide whether extraction needs rendering;
// the selector explicitly chooses static, local browser, or FlareSolverr.
func (s *Scheduler) Snapshot(ctx context.Context, rawURL string, r model.Recipe) (fetch.Result, error) {
	if err := fetch.ValidateURL(rawURL); err != nil {
		return fetch.Result{}, err
	}
	if err := extract.ValidateRender(r); err != nil {
		return fetch.Result{}, err
	}
	if r.Mode != "static" && r.Mode != "browser" && r.Mode != "flaresolverr" {
		return fetch.Result{}, errors.New("choose static, browser, or flaresolverr for the page preview")
	}
	id := store.ID()
	if !s.reserve(id) {
		return fetch.Result{}, ErrBusy
	}
	defer s.release(id)
	ctx, cancel := context.WithTimeout(ctx, s.timeout(r.Mode))
	defer cancel()
	if r.Mode == "flaresolverr" {
		return s.solve(ctx, rawURL)
	}
	if r.Mode == "browser" {
		if s.Renderer == nil {
			return fetch.Result{}, errors.New("browser rendering is unavailable on this server")
		}
		return s.Renderer.Render(ctx, rawURL, r.WaitSelector, r.SettleMS)
	}
	return s.Fetcher.Fetch(ctx, rawURL, "", "")
}
func (s *Scheduler) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.tick(ctx)
			}
		}
	}()
}
func (s *Scheduler) Wait() { s.wg.Wait() }
func (s *Scheduler) tick(ctx context.Context) {
	fs, e := s.Store.List(ctx)
	if e != nil {
		if ctx.Err() == nil {
			slog.Error("scheduler database read failed")
		}
		return
	}
	// Oldest due first prevents a permanently busy queue starving later feeds.
	sortFeeds(fs)
	for _, f := range fs {
		if !f.Enabled || f.NextRun.After(time.Now()) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if !s.reserve(f.ID) {
			continue
		}
		s.wg.Add(1)
		go func(f model.Feed) { defer s.wg.Done(); defer s.release(f.ID); s.refresh(ctx, f) }(f)
	}
}
func (s *Scheduler) refresh(parent context.Context, f model.Feed) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, s.timeout(f.Recipe.Mode))
	defer cancel()
	r, p, e := s.extract(ctx, f, false)
	items := p.Items
	if e == nil && r.Status == 304 {
		if f.LastSuccess.IsZero() {
			e = fmt.Errorf("source returned 304 before any successful refresh")
		} else {
			if r.ETag == "" {
				r.ETag = f.ETag
			}
			if r.LastModified == "" {
				r.LastModified = f.LastModified
			}
		}
	}
	var he *fetch.HTTPError
	if errors.As(e, &he) {
		r.RetryAfter = he.RetryAfter
	}
	// Use a short independent context to record a timed-out or canceled job.
	saveCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := s.Store.Complete(saveCtx, f, items, r.ETag, r.LastModified, r.Status, e, r.RetryAfter); err != nil && !errors.Is(err, store.ErrStale) {
		slog.Error("refresh persistence failed", "feed_id", f.ID)
	}
	slog.Info("refresh finished", "feed_id", f.ID, "duration", time.Since(start), "items", len(items), "status", r.Status, "success", e == nil)
}

// extract is shared by previews and scheduled refreshes. Auto falls back exactly
// once, only after a successful HTTP response with zero valid extracted items.
func (s *Scheduler) extract(ctx context.Context, f model.Feed, preview bool) (fetch.Result, model.Preview, error) {
	var r fetch.Result
	var p model.Preview
	var e error
	if f.Recipe.Mode == "flaresolverr" {
		r, e = s.solve(ctx, f.URL)
		if e != nil {
			return r, p, e
		}
		p, e = extract.Run(r.Body, r.URL, f.Recipe)
		return r, p, e
	}
	if f.Recipe.Mode != "browser" {
		etag, modified := f.ETag, f.LastModified
		if preview {
			etag = ""
			modified = ""
		}
		r, e = s.Fetcher.Fetch(ctx, f.URL, etag, modified)
		if e != nil || r.Status == 304 {
			return r, p, e
		}
		p, e = extract.Run(r.Body, r.URL, f.Recipe)
		if f.Recipe.Mode != "auto" || !errors.Is(e, extract.ErrNoItems) {
			return r, p, e
		}
	}
	if s.Renderer == nil {
		return r, p, errors.New("browser rendering is unavailable; configure CHROMIUM_PATH or use the browser Compose file")
	}
	r, e = s.Renderer.Render(ctx, f.URL, f.Recipe.WaitSelector, f.Recipe.SettleMS)
	if e != nil {
		return r, p, e
	}
	p, e = extract.Run(r.Body, r.URL, f.Recipe)
	return r, p, e
}

func (s *Scheduler) solve(ctx context.Context, rawURL string) (fetch.Result, error) {
	if s.FlareSolverr == nil {
		return fetch.Result{}, errors.New("FlareSolverr is unavailable; configure FLARESOLVERR_URL on the server")
	}
	// The remote browser always returns a fresh document; HTTP validators from a
	// previous static fetch cannot be reused for this request.
	return s.FlareSolverr.Fetch(ctx, rawURL, "", "")
}
