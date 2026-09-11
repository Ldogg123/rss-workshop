package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"rss-workshop/internal/diagnostics"
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
	if e == nil {
		// A preview fetches a sample rather than every article: it runs while an
		// operator waits, and it must not become a way to fan out hundreds of
		// requests from the editor.
		if w := s.fetchArticles(ctx, f, p.Items, nil, true); len(w) > 0 {
			p.Warnings = append(p.Warnings, w...)
		}
	}
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
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		due := 0
		for _, f := range fs {
			if f.Enabled && !f.NextRun.After(time.Now()) {
				due++
			}
		}
		if due > 0 {
			active, capacity := s.Stats()
			slog.Debug("scheduler tick", "feeds", len(fs), "due", due, "active", active, "capacity", capacity)
		}
	}
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
	if e == nil && f.Recipe.Full != nil {
		// Saved rows say which articles were already retrieved. A read failure
		// only costs this round its skip list, so it must not fail the refresh.
		known := map[string]model.Item{}
		if stored, err := s.Store.Items(ctx, f.ID); err == nil {
			for _, it := range stored {
				known[it.Key] = it
			}
		}
		if w := s.fetchArticles(ctx, f, items, known, false); len(w) > 0 && p.Diagnostics != nil && len(p.Diagnostics.Attempts) > 0 {
			last := &p.Diagnostics.Attempts[len(p.Diagnostics.Attempts)-1]
			// Appended after extract() sanitized the trace, which is safe
			// because store.encodeDiagnostics sanitizes again on write: stored
			// article warnings are bounded and have their URLs redacted there.
			last.Warnings = append(last.Warnings, w...)
		}
	}
	if e == nil && r.Status == 304 {
		if f.LastSuccess.IsZero() {
			e = fmt.Errorf("source returned 304 before any successful refresh")
			if p.Diagnostics != nil && len(p.Diagnostics.Attempts) > 0 {
				last := &p.Diagnostics.Attempts[len(p.Diagnostics.Attempts)-1]
				last.Outcome, last.Error = "failed", e.Error()
			}
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
	if err := s.Store.CompleteWithDiagnostics(saveCtx, f, items, r.ETag, r.LastModified, r.Status, e, r.RetryAfter, p.Diagnostics); err != nil && !errors.Is(err, store.ErrStale) {
		slog.Error("refresh persistence failed", "feed_id", f.ID)
	}
	// A failure is the line an operator actually needs, so it carries the reason
	// and rises above the routine ones. Titles are the operator's own labels;
	// source URLs and reader tokens stay out of the log.
	if e != nil {
		slog.Warn("refresh failed", "feed", f.Title, "feed_id", f.ID, "error", e,
			"status", r.Status, "failures", f.Failures+1, "duration", time.Since(start))
		return
	}
	slog.Info("refresh finished", "feed", f.Title, "feed_id", f.ID,
		"items", len(items), "status", r.Status, "duration", time.Since(start))
}

// extract is shared by previews and scheduled refreshes. Auto falls back exactly
// once, only after a successful HTTP response with zero valid extracted items.
func (s *Scheduler) extract(ctx context.Context, f model.Feed, preview bool) (r fetch.Result, p model.Preview, e error) {
	started := time.Now()
	trace := &model.RunDiagnostics{Version: 1, RecipeVersion: f.Version, RequestedMode: f.Recipe.Mode, SelectorType: f.Recipe.Type, Started: started}
	defer func() {
		trace.DurationMS = time.Since(started).Milliseconds()
		p.Diagnostics = diagnostics.Sanitize(trace)
	}()
	// Each actual fetch gets its own result. Auto retains the static failure
	// alongside the browser attempt rather than losing the reason for fallback.
	attempt := func(mode string, fetchPage func() (fetch.Result, error)) {
		start := time.Now()
		entry := model.FetchAttempt{Mode: mode, Stage: "fetch", Outcome: "success", Warnings: []string{}}
		r, e = fetchPage()
		p = model.Preview{Items: []model.Item{}, Warnings: []string{}}
		entry.Status, entry.Bytes = r.Status, len(r.Body)
		var httpErr *fetch.HTTPError
		if errors.As(e, &httpErr) {
			entry.Status = httpErr.Status
			r.Status = httpErr.Status
		}
		if e == nil && r.Status != 304 {
			entry.Stage = "extract"
			p, e = extract.Run(r.Body, r.URL, f.Recipe)
			matches, valid, items := p.Matches, p.Valid, len(p.Items)
			entry.Matches, entry.Valid, entry.Items = &matches, &valid, &items
			entry.Filtered = p.Filtered
			entry.Warnings = p.Warnings
		}
		if e != nil {
			entry.Outcome, entry.Error = "failed", e.Error()
		} else if r.Status == 304 {
			entry.Outcome = "not_modified"
		}
		entry.DurationMS = time.Since(start).Milliseconds()
		trace.Attempts = append(trace.Attempts, entry)
	}
	if f.Recipe.Mode == "flaresolverr" {
		attempt("flaresolverr", func() (fetch.Result, error) { return s.solve(ctx, f.URL) })
		return r, p, e
	}
	if f.Recipe.Mode != "browser" {
		etag, modified := f.ETag, f.LastModified
		if preview {
			etag = ""
			modified = ""
		}
		attempt("static", func() (fetch.Result, error) { return s.Fetcher.Fetch(ctx, f.URL, etag, modified) })
		if trace.Attempts[len(trace.Attempts)-1].Stage != "extract" || f.Recipe.Mode != "auto" || !errors.Is(e, extract.ErrNoItems) {
			return r, p, e
		}
	}
	attempt("browser", func() (fetch.Result, error) {
		if s.Renderer == nil {
			return fetch.Result{}, errors.New("browser rendering is unavailable; configure CHROMIUM_PATH or use the browser Compose file")
		}
		return s.Renderer.Render(ctx, f.URL, f.Recipe.WaitSelector, f.Recipe.SettleMS)
	})
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
