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

// ErrSolverBusy refuses a FlareSolverr preview or page load while every solver
// slot is taken, rather than letting it wait out its deadline for one. It
// matches ErrBusy, so callers that handle a busy server need nothing new.
var ErrSolverBusy error = solverBusy{}

type solverBusy struct{}

func (solverBusy) Error() string {
	return "FlareSolverr is busy: every FlareSolverr slot is in use; try again shortly"
}
func (solverBusy) Is(target error) bool { return target == ErrBusy }

type Renderer interface {
	Render(context.Context, string, string, int) (fetch.Result, error)
}
type Solver interface {
	fetch.Fetcher
	// Stats reports the requests holding a slot, including slots still held
	// after an interrupted request, and how many slots there are.
	Stats() (int, int)
	// Cooling reports how many of those slots are held after an interrupted
	// request, with no request of this process still using them.
	Cooling() int
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
	// solving holds the admitted jobs that still have FlareSolverr requests
	// to make: refreshes, previews and page loads in that mode, until their
	// last page through the solver. The solver serves only a few requests at
	// a time, and a job that waits for one does so inside its own deadline, so
	// a burst of due feeds would time out and back off for nothing more than
	// queueing. Admission instead keeps their number within the free slots.
	solving map[string]bool
	// rendering holds the admitted jobs that may still render a page in the
	// local Chromium pool. The pool also waits for a free slot inside each
	// job's deadline, so it is admitted the same way: a refresh starts only
	// while its Chromium admission is free (see chromium).
	rendering map[string]bool
	// held records feeds whose result could not be persisted. The usual backoff
	// lives in the feeds row, so a failed write leaves nothing behind to slow
	// the feed down: its schedule rolls back with the rest of the transaction
	// and the next tick refetches the source a second later, indefinitely. This
	// keeps that backoff in memory, where a failing database cannot erase it.
	// Retry lifts a hold early without forgetting its failures.
	held map[string]hold
	// backlog records, per full-content feed, whether its last stored refresh
	// left stories without an article body that the next one should fetch.
	// See draining.
	backlog map[string]bool
	// finished records when each refresh ended, until the next tick. A list
	// read before then can still show the feed due, because its new schedule
	// was saved after the read, and would start it again straight away.
	finished map[string]time.Time
	wg       sync.WaitGroup

	// Notifier, when set, hears when a feed reaches NotifyAfter failed
	// refreshes in a row or recovers from that, and when the database stops
	// storing results or serving the library.
	Notifier    Notifier
	NotifyAfter int
	alerts      alerts
}

type hold struct {
	until    time.Time
	failures int
}

func New(s *store.Store, f fetch.Fetcher, workers int, timeout time.Duration) *Scheduler {
	return &Scheduler{Store: s, Fetcher: f, Timeout: timeout, slots: make(chan struct{}, workers), active: map[string]bool{}, solving: map[string]bool{}, rendering: map[string]bool{}, held: map[string]hold{}, finished: map[string]time.Time{}}
}
func (s *Scheduler) Stats() (int, int) { return len(s.slots), cap(s.slots) }
func (s *Scheduler) timeout(mode string) time.Duration {
	if mode == "flaresolverr" && s.FlareSolverrTimeout > 0 {
		return s.FlareSolverrTimeout
	}
	return s.Timeout
}

// usesSolver reports whether a job in this fetch mode takes a FlareSolverr
// slot. Without a configured solver such a job fails at once, holding none.
func (s *Scheduler) usesSolver(mode string) bool {
	return mode == "flaresolverr" && s.FlareSolverr != nil
}

// chromium says how a job is admitted to the local Chromium pool.
type chromium int

const (
	// noChromium: the job renders nothing there.
	noChromium chromium = iota
	// shareChromium takes a free Chromium admission when there is one and
	// starts either way. Previews and page loads, which an operator waits on,
	// are admitted so. Unlike a FlareSolverr request, a render can be
	// cancelled and frees its slot when it ends, so one without an admission
	// waits for a pool slot inside its own deadline rather than being refused.
	shareChromium
	// needChromium starts only with a free Chromium admission. A refresh that
	// renders its list page or its article pages is admitted so; refused, it
	// stays due rather than waiting out its deadline for a pool slot.
	needChromium
)

// chromiumSlots is the capacity of the Chromium pool, or 0 when the Renderer
// reports none, which leaves every job unadmitted, as test fakes are. It is
// called with mu held, so it must not take the pool's lock: browser.Pool.Stats
// waits while Chromium starts, for up to 15 seconds.
func (s *Scheduler) chromiumSlots() int {
	if c, ok := s.Renderer.(interface{ Capacity() int }); ok {
		return c.Capacity()
	}
	return 0
}

// rendersPages reports whether a job for f is set to render pages in the
// Chromium pool: its list page in Chromium mode, or its article pages. Auto
// renders only when static HTML yields no stories, which is not known in
// advance. Admitting it as well would hold a Chromium admission through every
// Auto feed's static fetch and limit those to BROWSER_SLOTS at a time, so it
// is not admitted, and a fallback waits for a pool slot inside the feed's
// deadline.
func rendersPages(f model.Feed) bool {
	return f.Recipe.Mode == "browser" || articlesUseChromium(f)
}

// reserve admits a job to one of the shared slots and, when it uses
// FlareSolverr, to a solver slot that is free now. A job that renders in
// Chromium is admitted there as browser says. A refresh passes the time its
// feed list was read, and is refused if the feed finished since then.
func (s *Scheduler) reserve(id string, solver bool, browser chromium, listed time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[id] || len(s.slots) == cap(s.slots) {
		return ErrBusy
	}
	if done, ok := s.finished[id]; ok && !listed.IsZero() && !done.Before(listed) {
		return ErrBusy
	}
	if solver && !s.solverFree() {
		return ErrSolverBusy
	}
	slots := s.chromiumSlots()
	rendering := browser != noChromium && len(s.rendering) < slots
	if browser == needChromium && slots > 0 && !rendering {
		return ErrBusy
	}
	s.slots <- struct{}{} // cannot block: only reserve sends, and it holds mu
	s.active[id] = true
	if solver {
		s.solving[id] = true
	}
	if rendering {
		s.rendering[id] = true
	}
	return nil
}

// release ends a job, with its FlareSolverr and Chromium admissions if it
// still holds them.
func (s *Scheduler) release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, id)
	delete(s.solving, id)
	delete(s.rendering, id)
	<-s.slots
}

// finish releases a refresh, after its result was saved or given up.
func (s *Scheduler) finish(id string) {
	s.mu.Lock()
	s.finished[id] = time.Now()
	s.mu.Unlock()
	s.release(id)
}

// doneWithSolver gives back a job's FlareSolverr admission once the job has
// no more requests to make through the solver, as when a refresh fetches its
// article pages as plain HTML. The slot it used then goes to the next
// FlareSolverr job instead of idling until this one ends.
func (s *Scheduler) doneWithSolver(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.solving, id)
}

// doneWithChromium gives back a job's Chromium admission once the job will
// render nothing more: a Chromium-mode refresh after its list page, unless it
// renders its article pages too, and any job after its last article page.
func (s *Scheduler) doneWithChromium(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rendering, id)
}

// solverFree reports, with mu held, whether one more FlareSolverr job can
// start without waiting for a slot. A job holds at most one at a time: a
// refresh fetches its list page and then any article pages it renders there
// one by one, and gives its admission back after its last such page. Slots
// still held after an interrupted request are not free until they are let
// go, so admitted jobs plus those can never exceed the capacity. Stats is
// checked as well, so nothing using a slot is overlooked.
func (s *Scheduler) solverFree() bool {
	active, capacity := s.FlareSolverr.Stats()
	return max(active, len(s.solving)+s.FlareSolverr.Cooling()) < capacity
}

// hold backs a feed off after its result could not be stored, and reports how
// long until it is tried again. The delay doubles per consecutive failure from
// two minutes up to 1,024 minutes, about 17 hours: the same steps the database
// takes after a failed fetch. Only a source's Retry-After can push that
// backoff to its 24-hour cap, and a failed write has none, so a hold never
// reaches it.
func (s *Scheduler) hold(id string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.held[id]
	h.failures++
	wait := min(time.Duration(1<<min(h.failures, 10))*time.Minute, 24*time.Hour)
	h.until = time.Now().Add(wait)
	s.held[id] = h
	return wait
}

// Held reports whether a feed is waiting out a hold, so a tick will not
// dispatch it however overdue its stored schedule says it is.
func (s *Scheduler) Held(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.held[id]
	return ok && h.until.After(time.Now())
}

// Retry lets a held feed run at its stored due time instead of when the hold
// expires. Callers use it once the database has just accepted a write for the
// feed: a manual refresh, an editor save or a resume, each an authenticated
// request. That write proves less than a stored result would, because a disk
// can refuse new stories while still accepting a small update, so the failure
// count stays: a write that fails again holds the feed twice as long as the
// last one rather than starting over at two minutes.
func (s *Scheduler) Retry(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.held[id]; ok {
		h.until = time.Time{}
		s.held[id] = h
	}
}

// released clears the backoff once a result is stored again.
func (s *Scheduler) released(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.held, id)
}

// forgetHolds drops entries for feeds that no longer exist, so a long-running
// process does not accumulate them for deleted feeds.
func (s *Scheduler) forgetHolds(fs []model.Feed) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.held) == 0 {
		return
	}
	live := make(map[string]bool, len(fs))
	for _, f := range fs {
		live[f.ID] = true
	}
	for id := range s.held {
		if !live[id] {
			delete(s.held, id)
		}
	}
}

// forgetFinished drops end times a list read at listed already reflects: a
// refresh saves its result before it ends.
func (s *Scheduler) forgetFinished(listed time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, done := range s.finished {
		if done.Before(listed) {
			delete(s.finished, id)
		}
	}
}
func (s *Scheduler) Preview(ctx context.Context, f model.Feed) (model.Preview, error) {
	id, solver, browser := store.ID(), s.usesSolver(f.Recipe.Mode), noChromium
	if rendersPages(f) {
		browser = shareChromium
	}
	if err := s.reserve(id, solver, browser, time.Time{}); err != nil {
		return model.Preview{}, err
	}
	defer s.release(id)
	ctx, cancel := context.WithTimeout(ctx, s.timeout(f.Recipe.Mode))
	defer cancel()
	ctx = requestContext(ctx, f.Recipe)
	_, p, e := s.extract(ctx, f, true)
	if e != nil || !articlesUseSolver(f) {
		s.doneWithSolver(id)
	}
	if e != nil || !articlesUseChromium(f) {
		s.doneWithChromium(id)
	}
	if e == nil {
		// A preview fetches a sample rather than every article: it runs while an
		// operator waits, and it must not become a way to fan out hundreds of
		// requests from the editor.
		if w := s.fetchArticles(ctx, f, p.Items, nil, true).warnings; len(w) > 0 {
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
	id, solver, browser := store.ID(), s.usesSolver(r.Mode), noChromium
	if r.Mode == "browser" {
		browser = shareChromium
	}
	if err := s.reserve(id, solver, browser, time.Time{}); err != nil {
		return fetch.Result{}, err
	}
	defer s.release(id)
	ctx, cancel := context.WithTimeout(ctx, s.timeout(r.Mode))
	defer cancel()
	ctx = requestContext(ctx, r)
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
	listed := time.Now()
	fs, e := s.Store.List(ctx)
	if e != nil {
		if ctx.Err() == nil {
			// Once per second until the database recovers, so the reason has to
			// be on the line: rotation would otherwise discard the first
			// occurrence, which is the one that says what actually broke.
			slog.Error("scheduler database read failed", "error", e)
			s.alertListed(e)
		}
		return
	}
	s.alertListed(nil)
	s.dispatch(ctx, fs, listed)
}

// dispatch starts the due feeds in a list read at listed.
func (s *Scheduler) dispatch(ctx context.Context, fs []model.Feed, listed time.Time) {
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
	s.forgetHolds(fs)
	s.forgetBacklog(fs)
	s.alerts.forget(fs)
	s.forgetFinished(listed)
	for _, f := range fs {
		if !f.Enabled || f.NextRun.After(time.Now()) {
			continue
		}
		if s.Held(f.ID) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		// A feed that cannot start, because it is already running or its
		// fetcher is busy, stays due: a later tick takes it, oldest first,
		// without recording a failure or backing it off.
		browser := noChromium
		if rendersPages(f) {
			browser = needChromium
		}
		if s.reserve(f.ID, s.usesSolver(f.Recipe.Mode), browser, listed) != nil {
			continue
		}
		s.wg.Add(1)
		go func(f model.Feed) { defer s.wg.Done(); defer s.finish(f.ID); s.refresh(ctx, f) }(f)
	}
}
func (s *Scheduler) refresh(parent context.Context, f model.Feed) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, s.timeout(f.Recipe.Mode))
	defer cancel()
	// Every static request this refresh makes, article pages included, sends
	// the feed's own request headers when its recipe has them.
	ctx = requestContext(ctx, f.Recipe)
	var r fetch.Result
	var p model.Preview
	// Library filters are read as the refresh starts. Editing one afterwards
	// bumps this feed's version, so a result built with the old rules is
	// discarded as stale rather than saved.
	recipe, e := s.Store.EffectiveRecipe(ctx, f)
	if e == nil {
		f.Recipe = recipe
		r, p, e = s.extract(ctx, f, false)
	}
	// Past its list page, a FlareSolverr refresh uses the solver only for
	// article pages it renders there. Plain articles and the save leave the
	// slot to the next FlareSolverr job.
	if e != nil || !articlesUseSolver(f) {
		s.doneWithSolver(f.ID)
	}
	// The same goes for a Chromium admission, which a refresh keeps past its
	// list page only to render its article pages.
	if e != nil || !articlesUseChromium(f) {
		s.doneWithChromium(f.ID)
	}
	items := p.Items
	var articles articleRun
	if e == nil && f.Recipe.Full != nil {
		// Which articles were already retrieved, without loading the stories
		// themselves: the decision needs three facts per item, and reading whole
		// rows would pull every stored article body into memory. A read failure
		// only costs this round its skip list, so it must not fail the refresh.
		known, err := s.Store.ArticleStates(ctx, f.ID)
		if err != nil {
			known = nil
			slog.Warn("article skip list unavailable; articles may be refetched",
				"feed", f.Title, "feed_id", f.ID, "error", err)
		}
		articles = s.fetchArticles(ctx, f, items, known, false)
		if w := articles.warnings; len(w) > 0 && p.Diagnostics != nil && len(p.Diagnostics.Attempts) > 0 {
			last := &p.Diagnostics.Attempts[len(p.Diagnostics.Attempts)-1]
			// Appended after extract() sanitized the trace, which is safe
			// because store.encodeDiagnostics sanitizes again on write: stored
			// article warnings are bounded and have their URLs redacted there.
			last.Warnings = append(last.Warnings, w...)
		}
		// Its last article page has been rendered, so the save that follows
		// leaves the slot to the next job.
		s.doneWithChromium(f.ID)
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
	if e != nil && parent.Err() != nil {
		// The server is stopping, so the failure describes the shutdown rather
		// than the source. Storing it would count a failure the source never
		// had, back the feed off and, at the threshold, send a failing alert
		// for a healthy feed. Nothing is written, so the feed is still due and
		// refreshes when the server starts again. A result that succeeded
		// before the shutdown is stored as usual.
		slog.Info("refresh discarded", "feed", f.Title, "feed_id", f.ID, "reason", "server stopping", "duration", time.Since(start))
		return
	}
	var he *fetch.HTTPError
	if errors.As(e, &he) {
		r.RetryAfter = he.RetryAfter
	}
	// Use a short independent context to record a timed-out job, or one that
	// finished as the server began to stop.
	saveCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	switch err := s.Store.CompleteWithDiagnostics(saveCtx, f, items, r.ETag, r.LastModified, r.Status, e, r.RetryAfter, p.Diagnostics); {
	case errors.Is(err, store.ErrStale), errors.Is(err, store.ErrDeleted):
		// The feed was edited or deleted while this ran, so the result was
		// thrown away unsaved and left no run record. Saying it finished or
		// failed would describe a run that Diagnostics never shows. Neither is
		// a write failure. An edit just committed, so it releases a hold the
		// way Retry does, keeping its failure count; a deleted feed has
		// nothing left to hold.
		reason := "feed changed during refresh"
		if errors.Is(err, store.ErrDeleted) {
			reason = "feed deleted during refresh"
			s.released(f.ID)
		} else {
			s.Retry(f.ID)
		}
		slog.Info("refresh discarded", "feed", f.Title, "feed_id", f.ID, "reason", reason, "duration", time.Since(start))
		return
	case err != nil:
		// Nothing was written, including the schedule, so this feed is still due.
		// Hold it here or the source gets refetched every second for as long as
		// the database stays unwritable. Reporting success below would also be a
		// lie: no story, run record or timestamp survived.
		wait := s.hold(f.ID)
		slog.Error("refresh not saved", "feed", f.Title, "feed_id", f.ID, "error", err,
			"retry_in", wait, "note", "nothing was stored, including this feed's schedule")
		s.alertNotSaved(f, err)
		return
	}
	s.released(f.ID)
	if e == nil {
		s.recordBacklog(saveCtx, f, articles)
	}
	s.alertRefresh(f, e)
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
		// A 304 would leave a draining article backlog unfetched.
		if preview || s.draining(f) {
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
		return fetch.Result{}, errors.New("FlareSolverr is not configured on this server: set FLARESOLVERR_URL, in .env for Docker Compose or exported for a native run, then recreate the container with docker compose up -d or start the native server again; see docs/flaresolverr.md")
	}
	// The remote browser always returns a fresh document; HTTP validators from a
	// previous static fetch cannot be reused for this request.
	return s.FlareSolverr.Fetch(ctx, rawURL, "", "")
}
