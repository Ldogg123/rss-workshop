package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
	"rss-workshop/internal/flaresolverr"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

type allowAll struct{}

func (allowAll) CheckURL(context.Context, string) error { return nil }

// solverService is a FlareSolverr API stand-in for the real client. Each page
// takes delay to solve, and the service records the order of requests and
// how many overlapped, overall and per group of URLs.
type solverService struct {
	pages map[string]string
	delay func(string) time.Duration
	group func(string) string
	seen  func(string)

	mu      sync.Mutex
	order   []string
	running map[string]int
	peak    map[string]int
}

func startSolver(t *testing.T, slots int, svc *solverService) *flaresolverr.Client {
	t.Helper()
	svc.running, svc.peak = map[string]int{}, map[string]int{}
	if svc.group == nil {
		svc.group = func(string) string { return "" }
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			URL string `json:"url"`
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &in)
		group := svc.group(in.URL)
		svc.mu.Lock()
		svc.order = append(svc.order, in.URL)
		for _, key := range []string{"*", group} {
			svc.running[key]++
			svc.peak[key] = max(svc.peak[key], svc.running[key])
		}
		svc.mu.Unlock()
		defer func() {
			svc.mu.Lock()
			svc.running["*"]--
			svc.running[group]--
			svc.mu.Unlock()
		}()
		if svc.seen != nil {
			svc.seen(in.URL)
		}
		select {
		case <-time.After(svc.delay(in.URL)):
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "solution": map[string]any{"url": in.URL, "status": 200, "response": svc.pages[in.URL]}})
	}))
	t.Cleanup(server.Close)
	client, err := flaresolverr.New(server.URL, slots, 5*time.Second, allowAll{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func (svc *solverService) requests() []string {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return append([]string(nil), svc.order...)
}

func (svc *solverService) peakOf(group string) int {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return svc.peak[group]
}

// setNextRun gives feeds distinct due times, which Save cannot: it makes each
// due now, to the second.
func setNextRun(t *testing.T, s *store.Store, id string, at time.Time) {
	t.Helper()
	query := "UPDATE feeds SET next_run=? WHERE id=?"
	if os.Getenv("RSS_TEST_POSTGRES_URL") != "" {
		query = "UPDATE feeds SET next_run=$1 WHERE id=$2"
	}
	if _, err := s.DB.ExecContext(context.Background(), query, at.Unix(), id); err != nil {
		t.Fatal(err)
	}
}

// tickUntil runs the scheduler the way Start does, only faster, until done
// reports true, and then waits for every refresh it started.
func tickUntil(t *testing.T, jobs *Scheduler, limit time.Duration, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !done() {
		if time.Now().After(deadline) {
			jobs.Wait()
			t.Fatal("refreshes did not finish in time")
		}
		jobs.tick(context.Background())
		time.Sleep(5 * time.Millisecond)
	}
	jobs.Wait()
}

func allFetched(items []model.Item, body string) bool {
	for _, it := range items {
		if !strings.Contains(it.FullHTML, body) {
			return false
		}
	}
	return true
}

func (s *Scheduler) admitted() (ids map[string]bool, solving int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids = map[string]bool{}
	for id := range s.active {
		ids[id] = true
	}
	return ids, len(s.solving)
}

// Six FlareSolverr feeds fall due at once with one solver slot, as after a
// restart, an outage or an import. Started together, each waits for the slot
// inside its own deadline and the later ones fail and back off. The scheduler
// instead starts only what the solver can serve: the rest stay due, are not
// failed, and start oldest first as the slot frees, while other fetch modes
// are not held back.
func TestFlareSolverrBurstStartsOnlyWhatTheSolverCanServe(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	svc := &solverService{pages: map[string]string{}, delay: func(string) time.Duration { return 100 * time.Millisecond }}
	solver := startSolver(t, 1, svc)
	static := &gateFetcher{release: make(chan struct{})}
	jobs := New(s, static, 16, time.Second)
	jobs.FlareSolverr = solver
	// Room for one feed's list page and two articles, not for six queued feeds.
	jobs.FlareSolverrTimeout = 450 * time.Millisecond

	now := time.Now()
	var ids, want []string
	for i := range 6 {
		f := solverFeed()
		f.Title, f.URL = fmt.Sprintf("Protected %d", i), fmt.Sprintf("https://example.com/protected-%d", i)
		svc.pages[f.URL] = fmt.Sprintf(`<article><h2>Story %d</h2></article>`, i)
		if i == 2 {
			// Its articles go through the solver too, one by one, inside the
			// same admission, so they cannot hold back the feeds behind it.
			f.Recipe.Link = model.Field{Selector: "a"}
			f.Recipe.Full = &model.FullContent{Selector: ".body", Browser: true}
			svc.pages[f.URL] = ""
			for a := range 2 {
				link := fmt.Sprintf("%s/story-%d", f.URL, a)
				svc.pages[f.URL] += fmt.Sprintf(`<article><h2>Story %d.%d</h2><a href="%s">l</a></article>`, i, a, link)
				svc.pages[link] = fmt.Sprintf(`<div class="body">Article %d.%d</div>`, i, a)
			}
		}
		id, err := s.Save(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// Due in an order unrelated to creation, to show which one goes next.
	for rank, i := range []int{3, 0, 5, 1, 4, 2} {
		setNextRun(t, s, ids[i], now.Add(time.Duration(rank-10)*time.Second))
		want = append(want, fmt.Sprintf("https://example.com/protected-%d", i))
	}
	staticID, err := s.Save(ctx, model.Feed{Title: "Plain", URL: "https://example.com/plain", Interval: 60, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}})
	if err != nil {
		t.Fatal(err)
	}
	setNextRun(t, s, staticID, now.Add(-time.Second))

	jobs.tick(ctx)
	admitted, solving := jobs.admitted()
	if solving != 1 || len(admitted) != 2 || !admitted[ids[3]] || !admitted[staticID] {
		t.Fatalf("first tick admitted %v with %d FlareSolverr jobs; want the oldest FlareSolverr feed and the static one", admitted, solving)
	}
	close(static.release)
	tickUntil(t, jobs, 10*time.Second, func() bool {
		for _, id := range ids {
			if f, err := s.Get(ctx, id); err != nil || f.LastSuccess.IsZero() && f.Failures == 0 {
				return false
			}
		}
		return true
	})
	for _, id := range ids {
		f, err := s.Get(ctx, id)
		if err != nil || f.Failures != 0 || f.Error != "" || f.LastSuccess.IsZero() {
			t.Fatalf("%s: failures=%d error=%q success=%v, %v", f.Title, f.Failures, f.Error, f.LastSuccess, err)
		}
	}
	lists := []string{}
	for _, u := range svc.requests() {
		if !strings.Contains(u, "/story-") {
			lists = append(lists, u)
		}
	}
	if fmt.Sprint(lists) != fmt.Sprint(want) || svc.peakOf("*") != 1 {
		t.Fatalf("solver saw %v with %d at once; want %v one at a time", lists, svc.peakOf("*"), want)
	}
	if items, err := s.Items(ctx, ids[2]); err != nil || len(items) != 2 || !allFetched(items, "Article") {
		t.Fatalf("articles through the solver: %+v, %v", items, err)
	}
	if static.calls.Load() != 1 {
		t.Fatalf("static feed fetched %d times", static.calls.Load())
	}
}

// With two solver slots, a feed fetching its articles through FlareSolverr
// keeps to one of them. A list refresh that falls due meanwhile starts on the
// other at once instead of queueing behind those articles.
func TestFlareSolverrArticlesLeaveRoomForListRefreshes(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	articleStarted := make(chan struct{})
	var once sync.Once
	svc := &solverService{
		pages: map[string]string{"https://example.com/quick": `<article><h2>Quick story</h2></article>`},
		delay: func(u string) time.Duration {
			if strings.Contains(u, "/story-") {
				return 250 * time.Millisecond
			}
			return 20 * time.Millisecond
		},
		group: func(u string) string {
			if strings.Contains(u, "/story-") {
				return "articles"
			}
			return ""
		},
		seen: func(u string) {
			if strings.Contains(u, "/story-") {
				once.Do(func() { close(articleStarted) })
			}
		},
	}
	solver := startSolver(t, 2, svc)
	jobs := New(s, &gateFetcher{release: make(chan struct{})}, 16, time.Second)
	jobs.FlareSolverr = solver
	jobs.FlareSolverrTimeout = 4 * time.Second

	long := solverFeed()
	long.Title, long.URL = "Long articles", "https://example.com/long"
	long.Recipe.Link = model.Field{Selector: "a"}
	long.Recipe.Full = &model.FullContent{Selector: ".body", Browser: true}
	for a := range 6 {
		link := fmt.Sprintf("https://example.com/long/story-%d", a)
		svc.pages[long.URL] += fmt.Sprintf(`<article><h2>Long %d</h2><a href="%s">l</a></article>`, a, link)
		svc.pages[link] = fmt.Sprintf(`<div class="body">Long article %d</div>`, a)
	}
	longID, err := s.Save(ctx, long)
	if err != nil {
		t.Fatal(err)
	}
	jobs.tick(ctx)
	<-articleStarted
	// Its articles still need the solver, so it keeps its admission between
	// them; Stats alone would show the slot free in those gaps.
	if _, solving := jobs.admitted(); solving != 1 {
		t.Fatalf("a feed rendering its articles through FlareSolverr holds %d admissions", solving)
	}

	quick := solverFeed()
	quick.Title, quick.URL = "Quick list", "https://example.com/quick"
	quickID, err := s.Save(ctx, quick)
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now()
	var quickDone time.Duration
	tickUntil(t, jobs, 10*time.Second, func() bool {
		if quickDone == 0 {
			if f, err := s.Get(ctx, quickID); err == nil && (!f.LastSuccess.IsZero() || f.Failures > 0) {
				quickDone = time.Since(due)
			}
		}
		f, err := s.Get(ctx, longID)
		return quickDone != 0 && err == nil && (!f.LastSuccess.IsZero() || f.Failures > 0)
	})
	for _, id := range []string{longID, quickID} {
		f, err := s.Get(ctx, id)
		if err != nil || f.Failures != 0 || f.LastSuccess.IsZero() {
			t.Fatalf("%s: failures=%d error=%q, %v", f.Title, f.Failures, f.Error, err)
		}
	}
	// Six articles in parallel would keep both slots busy for 750 ms.
	if peak := svc.peakOf("articles"); peak != 1 || quickDone > 400*time.Millisecond {
		t.Fatalf("articles ran %d at once and the list refresh finished %v after falling due", peak, quickDone)
	}
	if items, err := s.Items(ctx, longID); err != nil || len(items) != 6 || !allFetched(items, "Long article") {
		t.Fatalf("articles: %+v, %v", items, err)
	}
}

// coolingSolver reports slots held after interrupted requests, which belong
// to no job the scheduler admitted.
type coolingSolver struct {
	cooling, capacity atomic.Int32
	calls             atomic.Int32
}

func (c *coolingSolver) Fetch(_ context.Context, u, _, _ string) (fetch.Result, error) {
	c.calls.Add(1)
	return fetch.Result{Body: []byte(`<article><h2>Solved</h2></article>`), URL: u, Status: 200}, nil
}
func (c *coolingSolver) Stats() (int, int) { return int(c.cooling.Load()), int(c.capacity.Load()) }
func (c *coolingSolver) Cooling() int      { return int(c.cooling.Load()) }

// A slot still held after an interrupted request is not free: a feed started
// on it would only wait out its deadline. The feed stays due, unfailed, and
// starts once the slot is let go. Previews and page loads say the solver is
// busy at once instead of waiting.
func TestFlareSolverrCoolingSlotsDelayRefreshesAndRefusePreviews(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	solver := &coolingSolver{}
	solver.capacity.Store(1)
	solver.cooling.Store(1)
	jobs := New(s, &staticFetcher{body: []byte(`<article><h2>Plain</h2></article>`)}, 4, time.Second)
	jobs.FlareSolverr = solver
	jobs.FlareSolverrTimeout = time.Second
	f := solverFeed()
	f.ID, err = s.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		jobs.tick(ctx)
	}
	jobs.Wait()
	stored, err := s.Get(ctx, f.ID)
	if err != nil || solver.calls.Load() != 0 || stored.Failures != 0 || stored.Error != "" || stored.NextRun.After(time.Now()) {
		t.Fatalf("feed started on a cooling slot or lost its turn: calls=%d %+v, %v", solver.calls.Load(), stored, err)
	}
	if _, err := jobs.Preview(ctx, f); !errors.Is(err, ErrSolverBusy) || !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "FlareSolverr is busy") {
		t.Fatalf("preview on a busy solver: %v", err)
	}
	if _, err := jobs.Snapshot(ctx, f.URL, f.Recipe); !errors.Is(err, ErrSolverBusy) {
		t.Fatalf("page load on a busy solver: %v", err)
	}
	static := f
	static.Recipe.Mode = "static"
	if _, err := jobs.Preview(ctx, static); err != nil {
		t.Fatalf("a busy solver held back a static preview: %v", err)
	}
	if active, _ := jobs.Stats(); active != 0 || solver.calls.Load() != 0 {
		t.Fatalf("refused jobs kept a slot or reached the solver: active=%d calls=%d", active, solver.calls.Load())
	}
	solver.cooling.Store(0)
	jobs.tick(ctx)
	jobs.Wait()
	if stored, err = s.Get(ctx, f.ID); err != nil || solver.calls.Load() != 1 || stored.LastSuccess.IsZero() || stored.Failures != 0 {
		t.Fatalf("feed did not start once the slot was free: calls=%d %+v, %v", solver.calls.Load(), stored, err)
	}
}

// A FlareSolverr refresh in progress fills the only slot: a preview or page
// load then fails fast, and the slot count returns to zero afterwards.
func TestFlareSolverrRunningRefreshMakesPreviewsBusy(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	gate := &gateFetcher{release: make(chan struct{})}
	jobs := New(s, &gateFetcher{release: make(chan struct{})}, 4, time.Second)
	jobs.FlareSolverr = &solverFetcher{fetch: gate.Fetch}
	jobs.FlareSolverrTimeout = 5 * time.Second
	f := solverFeed()
	if _, err := s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	jobs.tick(ctx)
	for deadline := time.Now().Add(time.Second); gate.active.Load() != 1; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("refresh did not reach the solver")
		}
	}
	started := time.Now()
	if _, err := jobs.Preview(ctx, f); !errors.Is(err, ErrSolverBusy) {
		t.Fatalf("preview beside a running refresh: %v", err)
	}
	if _, err := jobs.Snapshot(ctx, f.URL, f.Recipe); !errors.Is(err, ErrSolverBusy) {
		t.Fatalf("page load beside a running refresh: %v", err)
	}
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("busy answers took %v", waited)
	}
	close(gate.release)
	jobs.Wait()
	if _, solving := jobs.admitted(); solving != 0 || gate.calls.Load() != 1 {
		t.Fatalf("solver jobs after the refresh: %d, solver calls %d", solving, gate.calls.Load())
	}
	if _, err := jobs.Preview(ctx, f); err != nil {
		t.Fatalf("preview once the solver is free: %v", err)
	}
}

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

// A FlareSolverr feed whose article pages are plain HTML, the default, is done
// with the solver once its list page is solved. While those articles load,
// the slot it used must go to another FlareSolverr feed, preview or page
// load: the solver is idle, so refusing them as busy would contradict the
// dashboard and only delay them.
func TestFlareSolverrStaticArticlesFreeTheSolver(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	svc := &solverService{pages: map[string]string{
		"https://example.com/long":  `<article><h2>Long</h2><a href="https://example.com/long/story">l</a></article>`,
		"https://example.com/quick": `<article><h2>Quick story</h2></article>`,
	}, delay: func(string) time.Duration { return 10 * time.Millisecond }}
	solver := startSolver(t, 1, svc)
	articles := &gateFetcher{release: make(chan struct{})}
	jobs := New(s, articles, 16, time.Second)
	jobs.FlareSolverr = solver
	jobs.FlareSolverrTimeout = 4 * time.Second

	long := solverFeed()
	long.Title, long.URL = "Long articles", "https://example.com/long"
	long.Recipe.Link = model.Field{Selector: "a"}
	long.Recipe.Full = &model.FullContent{Selector: ".body"}
	quick := solverFeed()
	quick.Title, quick.URL = "Quick list", "https://example.com/quick"
	waitForArticle := func(n int32) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); articles.active.Load() != n; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("article fetch did not start")
			}
		}
	}

	longID, err := s.Save(ctx, long)
	if err != nil {
		t.Fatal(err)
	}
	jobs.tick(ctx)
	waitForArticle(1)
	if _, err := jobs.Preview(ctx, quick); err != nil {
		t.Fatalf("preview while another feed fetches static articles: %v", err)
	}
	// Previewing the long feed holds its own article fetch open as well.
	previewed := make(chan error, 1)
	go func() { _, err := jobs.Preview(ctx, long); previewed <- err }()
	waitForArticle(2)
	if _, err := jobs.Snapshot(ctx, quick.URL, quick.Recipe); err != nil {
		t.Fatalf("page load while two jobs fetch static articles: %v", err)
	}
	quickID, err := s.Save(ctx, quick)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if f, err := s.Get(ctx, quickID); err == nil && (!f.LastSuccess.IsZero() || f.Failures > 0) {
			break
		}
		if time.Now().After(deadline) {
			close(articles.release)
			jobs.Wait()
			t.Fatal("a FlareSolverr feed waited for another feed's static articles")
		}
		jobs.tick(ctx)
	}
	close(articles.release)
	jobs.Wait()
	if err := <-previewed; err != nil {
		t.Fatalf("preview with static articles: %v", err)
	}
	for _, id := range []string{longID, quickID} {
		f, err := s.Get(ctx, id)
		if err != nil || f.Failures != 0 || f.LastSuccess.IsZero() {
			t.Fatalf("%s: failures=%d error=%q, %v", f.Title, f.Failures, f.Error, err)
		}
	}
	if _, solving := jobs.admitted(); solving != 0 || svc.peakOf("*") != 1 {
		t.Fatalf("FlareSolverr jobs left admitted: %d; solver peak %d", solving, svc.peakOf("*"))
	}
}

// pendingSolver counts a request in Stats only once it holds a slot. The
// real client first checks the source URL, which can mean a DNS lookup, so an
// admitted job can be invisible to Stats for a while.
type pendingSolver struct {
	cooling, running, calls atomic.Int32
	capacity                int
	gate                    chan struct{}
}

func (c *pendingSolver) Fetch(ctx context.Context, u, _, _ string) (fetch.Result, error) {
	c.calls.Add(1)
	select {
	case <-c.gate:
	case <-ctx.Done():
		return fetch.Result{}, ctx.Err()
	}
	c.running.Add(1)
	defer c.running.Add(-1)
	return fetch.Result{Body: []byte(`<article><h2>Solved</h2></article>`), URL: u, Status: 200}, nil
}
func (c *pendingSolver) Stats() (int, int) {
	return int(c.cooling.Load() + c.running.Load()), c.capacity
}
func (c *pendingSolver) Cooling() int { return int(c.cooling.Load()) }

// Of two slots, one is still held after an interrupted request, and Stats
// cannot tell it from a request in progress. Two feeds fall due while the
// first one admitted has not reached the solver yet: counting only Stats, or
// only admitted jobs, both look like one slot in use and would start the
// second as well, to wait for a slot inside its deadline. Admission counts the
// cooling slot beside the admitted job, so the second stays due until a slot
// is really free.
func TestFlareSolverrCoolingSlotsCountBesideAdmittedJobs(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	solver := &pendingSolver{capacity: 2, gate: make(chan struct{})}
	solver.cooling.Store(1)
	jobs := New(s, &staticFetcher{}, 16, time.Second)
	jobs.FlareSolverr = solver
	jobs.FlareSolverrTimeout = 4 * time.Second
	now := time.Now()
	var ids []string
	for i := range 2 {
		f := solverFeed()
		f.Title, f.URL = fmt.Sprintf("Protected %d", i), fmt.Sprintf("https://example.com/protected-%d", i)
		id, err := s.Save(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		setNextRun(t, s, id, now.Add(time.Duration(i-10)*time.Second))
		ids = append(ids, id)
	}
	for range 3 {
		jobs.tick(ctx)
		if admitted, solving := jobs.admitted(); solving != 1 || len(admitted) != 1 || !admitted[ids[0]] {
			close(solver.gate)
			jobs.Wait()
			t.Fatalf("admitted %v with %d FlareSolverr jobs beside a cooling slot; want only the oldest feed", admitted, solving)
		}
	}
	close(solver.gate)
	tickUntil(t, jobs, 5*time.Second, func() bool {
		for _, id := range ids {
			if f, err := s.Get(ctx, id); err != nil || f.LastSuccess.IsZero() && f.Failures == 0 {
				return false
			}
		}
		return true
	})
	for _, id := range ids {
		if f, err := s.Get(ctx, id); err != nil || f.Failures != 0 || f.LastSuccess.IsZero() {
			t.Fatalf("%s: failures=%d error=%q, %v", f.Title, f.Failures, f.Error, err)
		}
	}
}
