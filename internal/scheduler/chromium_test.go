package scheduler

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

// chromiumPool stands in for browser.Pool. It reports its capacity and, as
// Pool.Render does, waits for a free slot inside the caller's deadline before
// rendering. It records the order in which renders took a slot, how many ran
// at once, overall and for article pages, and how many had to wait for one.
type chromiumPool struct {
	slots chan struct{}
	pages map[string]string
	delay func(string) time.Duration
	// held renders wait, once they hold a slot, until their channel closes.
	held map[string]chan struct{}

	mu              sync.Mutex
	order           []string
	running, peak   map[string]int
	waiting, waited int
}

func newChromiumPool(capacity int) *chromiumPool {
	return &chromiumPool{slots: make(chan struct{}, capacity), pages: map[string]string{}, held: map[string]chan struct{}{},
		running: map[string]int{}, peak: map[string]int{}}
}

func (p *chromiumPool) Capacity() int { return cap(p.slots) }

func (p *chromiumPool) Render(ctx context.Context, u, _ string, _ int) (fetch.Result, error) {
	select {
	case p.slots <- struct{}{}:
	default:
		p.mu.Lock()
		p.waiting++
		p.waited++
		p.mu.Unlock()
		got := false
		select {
		case p.slots <- struct{}{}:
			got = true
		case <-ctx.Done():
		}
		p.mu.Lock()
		p.waiting--
		p.mu.Unlock()
		if !got {
			return fetch.Result{}, fmt.Errorf("no free Chromium slot before the deadline: %w", ctx.Err())
		}
	}
	defer func() { <-p.slots }()
	kind := "list"
	if strings.Contains(u, "/story-") {
		kind = "article"
	}
	p.mu.Lock()
	p.order = append(p.order, u)
	for _, k := range []string{"*", kind} {
		p.running[k]++
		p.peak[k] = max(p.peak[k], p.running[k])
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.running["*"]--
		p.running[kind]--
		p.mu.Unlock()
	}()
	var delay time.Duration
	if p.delay != nil {
		delay = p.delay(u)
	}
	held := p.held[u]
	if held == nil {
		held = make(chan struct{})
		close(held)
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return fetch.Result{}, fmt.Errorf("browser render deadline exceeded or canceled: %w", ctx.Err())
	}
	select {
	case <-held:
	case <-ctx.Done():
		return fetch.Result{}, fmt.Errorf("browser render deadline exceeded or canceled: %w", ctx.Err())
	}
	body, ok := p.pages[u]
	if !ok {
		body = `<article><h2>Rendered</h2></article>`
	}
	return fetch.Result{Body: []byte(body), URL: u, Status: 200}, nil
}

func (p *chromiumPool) renders() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.order...)
}

func (p *chromiumPool) started(u string) bool { return slices.Contains(p.renders(), u) }

func (p *chromiumPool) peakOf(kind string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak[kind]
}

// waits reports how many renders are waiting for a slot now, and how many
// ever did.
func (p *chromiumPool) waits() (now, ever int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waiting, p.waited
}

// uncountedRenderer renders through a pool without reporting its capacity, as
// the scheduler's other test fakes do.
type uncountedRenderer struct{ pool *chromiumPool }

func (r uncountedRenderer) Render(ctx context.Context, u, wait string, settle int) (fetch.Result, error) {
	return r.pool.Render(ctx, u, wait, settle)
}

// sitePages serves static pages by URL, and an empty page for the rest.
type sitePages map[string]string

func (p sitePages) Fetch(_ context.Context, u, _, _ string) (fetch.Result, error) {
	body, ok := p[u]
	if !ok {
		body = "<html></html>"
	}
	return fetch.Result{Body: []byte(body), URL: u, Status: 200}, nil
}

func chromiumFeed(title, u string) model.Feed {
	return model.Feed{Title: title, URL: u, Interval: 3600, Enabled: true,
		Recipe: model.Recipe{Mode: "browser", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}}
}

func (s *Scheduler) renderingJobs() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := map[string]bool{}
	for id := range s.rendering {
		ids[id] = true
	}
	return ids
}

func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !done(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
	}
}

// refreshed reports whether every feed has stored a result.
func refreshed(s *store.Store, ids ...string) func() bool {
	return func() bool {
		for _, id := range ids {
			if f, err := s.Get(context.Background(), id); err != nil || f.LastSuccess.IsZero() && f.Failures == 0 {
				return false
			}
		}
		return true
	}
}

func requireSuccess(t *testing.T, s *store.Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		f, err := s.Get(context.Background(), id)
		if err != nil || f.Failures != 0 || f.Error != "" || f.LastSuccess.IsZero() {
			t.Fatalf("%s: failures=%d error=%q success=%v, %v", f.Title, f.Failures, f.Error, f.LastSuccess, err)
		}
	}
}

// Eight Chromium feeds fall due at once with two Chromium slots, as after a
// restart, an outage or an import. Started together, each waits for a slot
// inside its own deadline, and the later ones fail and back off. The
// scheduler instead starts only what the pool can serve: the rest stay due,
// are not failed, and start oldest first as slots free, while a static feed
// due in the same tick starts at once.
func TestChromiumBurstStartsOnlyWhatThePoolCanServe(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	pool := newChromiumPool(2)
	pool.delay = func(string) time.Duration { return 150 * time.Millisecond }
	static := &gateFetcher{release: make(chan struct{})}
	// Room for one render, not for four rounds of two.
	jobs := New(s, static, 16, 400*time.Millisecond)
	jobs.Renderer = pool

	now := time.Now()
	var ids, want []string
	for i := range 8 {
		f := chromiumFeed(fmt.Sprintf("Rendered %d", i), fmt.Sprintf("https://example.com/rendered-%d", i))
		pool.pages[f.URL] = fmt.Sprintf(`<article><h2>Story %d</h2></article>`, i)
		id, err := s.Save(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// Due in an order unrelated to creation, to show which ones go next.
	for rank, i := range []int{3, 0, 5, 1, 7, 4, 2, 6} {
		setNextRun(t, s, ids[i], now.Add(time.Duration(rank-10)*time.Second))
		want = append(want, fmt.Sprintf("https://example.com/rendered-%d", i))
	}
	staticID, err := s.Save(ctx, model.Feed{Title: "Plain", URL: "https://example.com/plain", Interval: 60, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}})
	if err != nil {
		t.Fatal(err)
	}
	setNextRun(t, s, staticID, now.Add(-time.Second))

	jobs.tick(ctx)
	admitted, _ := jobs.admitted()
	if rendering := jobs.renderingJobs(); len(admitted) != 3 || !admitted[ids[3]] || !admitted[ids[0]] || !admitted[staticID] || len(rendering) != 2 {
		t.Fatalf("first tick admitted %v, %v rendering; want the two oldest Chromium feeds and the static one", admitted, rendering)
	}
	close(static.release)
	tickUntil(t, jobs, 10*time.Second, refreshed(s, append(ids, staticID)...))
	requireSuccess(t, s, append(ids, staticID)...)
	// Two feeds admitted in the same tick may reach the pool in either order,
	// so each render is at most one place from its turn.
	got := pool.renders()
	if len(got) != len(want) {
		t.Fatalf("renders %v; want %v", got, want)
	}
	for i, u := range got {
		if turn := slices.Index(want, u); turn < i-1 || turn > i+1 {
			t.Fatalf("renders %v; want about %v, oldest first", got, want)
		}
	}
	if _, waited := pool.waits(); waited != 0 || static.calls.Load() != 1 {
		t.Fatalf("%d renders waited for a Chromium slot; static feed fetched %d times", waited, static.calls.Load())
	}
	if rendering := jobs.renderingJobs(); len(rendering) != 0 {
		t.Fatalf("Chromium admissions left after the burst: %v", rendering)
	}
}

// A Chromium feed that renders its article pages keeps its admission through
// them and renders them one at a time, so it holds a single pool slot. A
// Chromium feed that falls due meanwhile starts at once on the other slot,
// and no render waits for one.
func TestChromiumArticlesHoldOneSlot(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	pool := newChromiumPool(2)
	pool.delay = func(u string) time.Duration {
		if strings.Contains(u, "/story-") {
			return 100 * time.Millisecond
		}
		return 10 * time.Millisecond
	}
	long := chromiumFeed("Long articles", "https://example.com/long")
	long.Recipe.Link = model.Field{Selector: "a"}
	long.Recipe.Full = &model.FullContent{Selector: ".body", Browser: true}
	for a := range 6 {
		link := fmt.Sprintf("https://example.com/long/story-%d", a)
		pool.pages[long.URL] += fmt.Sprintf(`<article><h2>Long %d</h2><a href="%s">l</a></article>`, a, link)
		pool.pages[link] = fmt.Sprintf(`<div class="body">Long article %d</div>`, a)
	}
	pool.pages["https://example.com/quick"] = `<article><h2>Quick story</h2></article>`
	jobs := New(s, &gateFetcher{release: make(chan struct{})}, 16, 4*time.Second)
	jobs.Renderer = pool

	longID, err := s.Save(ctx, long)
	if err != nil {
		t.Fatal(err)
	}
	jobs.tick(ctx)
	waitUntil(t, "an article page renders", func() bool { return pool.peakOf("article") > 0 })
	if rendering := jobs.renderingJobs(); len(rendering) != 1 || !rendering[longID] {
		t.Fatalf("a feed rendering its articles holds %v Chromium admissions", rendering)
	}
	quickID, err := s.Save(ctx, chromiumFeed("Quick list", "https://example.com/quick"))
	if err != nil {
		t.Fatal(err)
	}
	jobs.tick(ctx)
	if rendering := jobs.renderingJobs(); len(rendering) != 2 || !rendering[quickID] {
		t.Fatalf("a feed due beside one rendering its articles was not admitted: %v", rendering)
	}
	tickUntil(t, jobs, 10*time.Second, refreshed(s, longID, quickID))
	requireSuccess(t, s, longID, quickID)
	if _, waited := pool.waits(); pool.peakOf("article") != 1 || waited != 0 {
		t.Fatalf("articles rendered %d at once and %d renders waited for a slot", pool.peakOf("article"), waited)
	}
	if items, err := s.Items(ctx, longID); err != nil || len(items) != 6 || !allFetched(items, "Long article") {
		t.Fatalf("articles: %+v, %v", items, err)
	}
}

// A Chromium feed whose article pages are plain HTML, the default, is done
// with Chromium once its list page is rendered. While those articles load,
// its slot goes to the next Chromium feed instead of idling until it ends.
func TestChromiumStaticArticlesFreeTheSlot(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	pool := newChromiumPool(1)
	long := chromiumFeed("Long articles", "https://example.com/long")
	long.Recipe.Link = model.Field{Selector: "a"}
	long.Recipe.Full = &model.FullContent{Selector: ".body"}
	pool.pages[long.URL] = `<article><h2>Long</h2><a href="https://example.com/long/story">l</a></article>`
	articles := &gateFetcher{release: make(chan struct{})}
	jobs := New(s, articles, 16, 4*time.Second)
	jobs.Renderer = pool
	longID, err := s.Save(ctx, long)
	if err != nil {
		t.Fatal(err)
	}
	jobs.tick(ctx)
	waitUntil(t, "the article fetch starts", func() bool { return articles.active.Load() == 1 })
	if rendering := jobs.renderingJobs(); len(rendering) != 0 {
		t.Fatalf("a feed fetching plain articles holds %v Chromium admissions", rendering)
	}
	quickID, err := s.Save(ctx, chromiumFeed("Quick list", "https://example.com/quick"))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); !refreshed(s, quickID)(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			close(articles.release)
			jobs.Wait()
			t.Fatal("a Chromium feed waited for another feed's plain articles")
		}
		jobs.tick(ctx)
	}
	close(articles.release)
	jobs.Wait()
	requireSuccess(t, s, longID, quickID)
}

// Article pages render in Chromium in every fetch mode but FlareSolverr,
// Static HTTP included, so such a feed is admitted as a Chromium one is. With
// one slot, a Chromium feed due while its article renders stays due, unfailed,
// and starts once the article is done.
func TestChromiumStaticFeedRenderingArticlesIsAdmitted(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	pool := newChromiumPool(1)
	article := "https://example.com/a1"
	pool.pages[article] = `<div class="body">Rendered article</div>`
	pool.held[article] = make(chan struct{})
	list := newPageFetcher(listPage(1), nil)
	jobs := New(s, list, 16, 4*time.Second)
	jobs.Renderer = pool
	r := fullRecipe()
	r.Mode = "static"
	r.Full.Browser = true
	staticID, err := s.Save(ctx, model.Feed{Title: "Static list", URL: "https://example.com/list", Interval: 3600, Enabled: true, Recipe: r})
	if err != nil {
		t.Fatal(err)
	}
	jobs.tick(ctx)
	waitUntil(t, "the article page renders", func() bool { return pool.started(article) })
	if rendering := jobs.renderingJobs(); len(rendering) != 1 || !rendering[staticID] {
		t.Fatalf("a static feed rendering its article holds %v Chromium admissions", rendering)
	}
	otherID, err := s.Save(ctx, chromiumFeed("Rendered", "https://example.com/rendered"))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		jobs.tick(ctx)
		if admitted, _ := jobs.admitted(); admitted[otherID] {
			close(pool.held[article])
			jobs.Wait()
			t.Fatal("a Chromium feed started while the only slot rendered another feed's article")
		}
	}
	if f, err := s.Get(ctx, otherID); err != nil || f.Failures != 0 || f.NextRun.After(time.Now()) {
		t.Fatalf("a feed waiting for its turn was failed or lost it: %+v, %v", f, err)
	}
	close(pool.held[article])
	tickUntil(t, jobs, 10*time.Second, refreshed(s, staticID, otherID))
	requireSuccess(t, s, staticID, otherID)
	if items, err := s.Items(ctx, staticID); err != nil || len(items) != 1 || !allFetched(items, "Rendered article") {
		t.Fatalf("article rendered for a static feed: %+v, %v", items, err)
	}
	if got := pool.renders(); fmt.Sprint(got) != fmt.Sprint([]string{article, "https://example.com/rendered"}) || list.calls.Load() != 1 {
		t.Fatalf("renders %v and %d static fetches", got, list.calls.Load())
	}
}

// Auto feeds are not admitted: most find their stories as static HTML, and
// holding an admission through every static fetch would let only
// BROWSER_SLOTS of them run at once. With the only Chromium admission taken,
// Auto feeds still start. One that finds its stories never renders, and a
// fallback waits for a pool slot inside its own deadline, as before.
func TestChromiumAutoFeedsAreNotHeldBack(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	pool := newChromiumPool(1)
	busy := "https://example.com/rendered"
	pool.held[busy] = make(chan struct{})
	pool.pages["https://example.com/auto-fallback"] = `<article><h2>Fallback story</h2></article>`
	jobs := New(s, sitePages{"https://example.com/auto-static": `<article><h2>Static story</h2></article>`}, 16, 4*time.Second)
	jobs.Renderer = pool
	busyID, err := s.Save(ctx, chromiumFeed("Rendered", busy))
	if err != nil {
		t.Fatal(err)
	}
	jobs.tick(ctx)
	waitUntil(t, "the Chromium feed renders", func() bool { return pool.started(busy) })
	var autoIDs []string
	for _, name := range []string{"static", "fallback"} {
		f := chromiumFeed("Auto "+name, "https://example.com/auto-"+name)
		f.Recipe.Mode = "auto"
		id, err := s.Save(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		autoIDs = append(autoIDs, id)
	}
	jobs.tick(ctx)
	admitted, _ := jobs.admitted()
	if rendering := jobs.renderingJobs(); !admitted[autoIDs[0]] || !admitted[autoIDs[1]] || len(rendering) != 1 || !rendering[busyID] {
		close(pool.held[busy])
		jobs.Wait()
		t.Fatalf("with Chromium busy, admitted %v with %v rendering; want both Auto feeds started without an admission", admitted, rendering)
	}
	waitUntil(t, "the Auto feed with static stories finishes", refreshed(s, autoIDs[0]))
	waitUntil(t, "the Auto fallback waits for the slot", func() bool { now, _ := pool.waits(); return now == 1 })
	close(pool.held[busy])
	tickUntil(t, jobs, 10*time.Second, refreshed(s, append(autoIDs, busyID)...))
	requireSuccess(t, s, append(autoIDs, busyID)...)
	if items, err := s.Items(ctx, autoIDs[1]); err != nil || len(items) != 1 || items[0].Title != "Fallback story" {
		t.Fatalf("Auto fallback: %+v, %v", items, err)
	}
	if slices.Contains(pool.renders(), "https://example.com/auto-static") {
		t.Fatal("an Auto feed with static stories rendered")
	}
}

// Previews and page loads are never refused for want of a Chromium slot,
// unlike FlareSolverr's: a render can be cancelled, and frees its slot when it
// ends. Without a free admission they wait for a pool slot inside their own
// deadline. With one free, a preview takes it, so a refresh due meanwhile
// stays due rather than starting only to wait behind it.
func TestChromiumPreviewsAndPageLoadsAreNeverRefused(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	pool := newChromiumPool(1)
	busy, slow := "https://example.com/busy", "https://example.com/slow-preview"
	pool.held[busy], pool.held[slow] = make(chan struct{}), make(chan struct{})
	pool.pages["https://example.com/preview"] = `<article><h2>Previewed story</h2></article>`
	jobs := New(s, &gateFetcher{release: make(chan struct{})}, 16, 4*time.Second)
	jobs.Renderer = pool
	busyID, err := s.Save(ctx, chromiumFeed("Busy", busy))
	if err != nil {
		t.Fatal(err)
	}
	jobs.tick(ctx)
	waitUntil(t, "the refresh renders", func() bool { return pool.started(busy) })

	previewed := chromiumFeed("Previewed", "https://example.com/preview")
	type result struct {
		items int
		body  string
		err   error
	}
	results := make(chan result, 2)
	go func() { p, err := jobs.Preview(ctx, previewed); results <- result{items: len(p.Items), err: err} }()
	go func() {
		r, err := jobs.Snapshot(ctx, previewed.URL, previewed.Recipe)
		results <- result{body: string(r.Body), err: err}
	}()
	waitUntil(t, "the preview and the page load wait for the slot", func() bool { now, _ := pool.waits(); return now == 2 })
	if rendering := jobs.renderingJobs(); len(rendering) != 1 || !rendering[busyID] {
		t.Fatalf("Chromium admissions %v; want only the refresh's", rendering)
	}
	close(pool.held[busy])
	// The preview reports one story, the page load returns its page.
	for range 2 {
		if r := <-results; r.err != nil || r.items != 1 && !strings.Contains(r.body, "Previewed story") {
			t.Fatalf("preview or page load beside a busy pool: %+v", r)
		}
	}
	jobs.Wait()
	requireSuccess(t, s, busyID)

	done := make(chan error, 1)
	go func() { _, err := jobs.Preview(ctx, chromiumFeed("Slow preview", slow)); done <- err }()
	waitUntil(t, "the second preview renders", func() bool { return pool.started(slow) })
	if rendering := jobs.renderingJobs(); len(rendering) != 1 {
		t.Fatalf("a preview with the slot free holds %d Chromium admissions", len(rendering))
	}
	dueID, err := s.Save(ctx, chromiumFeed("Due", "https://example.com/due"))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		jobs.tick(ctx)
		if admitted, _ := jobs.admitted(); admitted[dueID] {
			close(pool.held[slow])
			jobs.Wait()
			t.Fatal("a refresh started while a preview held the only Chromium admission")
		}
	}
	close(pool.held[slow])
	if err := <-done; err != nil {
		t.Fatalf("preview with the slot free: %v", err)
	}
	tickUntil(t, jobs, 10*time.Second, refreshed(s, dueID))
	requireSuccess(t, s, dueID)
}

// A Renderer that reports no capacity, as the scheduler's other test fakes
// do, leaves every job unadmitted, as before: due Chromium feeds all start,
// and a feed renders its article pages four at a time.
func TestRendererWithoutCapacityIsNotAdmitted(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	pool := newChromiumPool(8)
	pool.delay = func(string) time.Duration { return 100 * time.Millisecond }
	articles := chromiumFeed("Articles", "https://example.com/articles")
	articles.Recipe.Link = model.Field{Selector: "a"}
	articles.Recipe.Full = &model.FullContent{Selector: ".body", Browser: true}
	for a := range 4 {
		link := fmt.Sprintf("https://example.com/articles/story-%d", a)
		pool.pages[articles.URL] += fmt.Sprintf(`<article><h2>Story %d</h2><a href="%s">l</a></article>`, a, link)
		pool.pages[link] = fmt.Sprintf(`<div class="body">Article %d</div>`, a)
	}
	jobs := New(s, &gateFetcher{release: make(chan struct{})}, 16, 4*time.Second)
	jobs.Renderer = uncountedRenderer{pool}
	ids := []string{}
	for _, f := range []model.Feed{articles, chromiumFeed("One", "https://example.com/one"), chromiumFeed("Two", "https://example.com/two")} {
		id, err := s.Save(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	jobs.tick(ctx)
	if admitted, _ := jobs.admitted(); len(admitted) != 3 || len(jobs.renderingJobs()) != 0 {
		t.Fatalf("admitted %v with %v rendering; want every due feed and no admissions", admitted, jobs.renderingJobs())
	}
	tickUntil(t, jobs, 10*time.Second, refreshed(s, ids...))
	requireSuccess(t, s, ids...)
	if peak := pool.peakOf("article"); peak != articleWorkers {
		t.Fatalf("articles rendered %d at once; want %d", peak, articleWorkers)
	}
}
