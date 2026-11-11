package scheduler

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rss-workshop/internal/diagnostics"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

const siteList = "https://example.com/list"

// articleSite is a static source whose article pages can answer with an HTTP
// error status or fail in transit. It counts the requests each page received.
// With an ETag it answers 304 to a list request that sends it back.
type articleSite struct {
	mu     sync.Mutex
	list   string
	etag   string
	pages  map[string]string
	status map[string]int
	down   map[string]bool
	seen   map[string]int
	sent   []string
	during func()
}

func newArticleSite(stories int) *articleSite {
	return &articleSite{list: listPage(stories), pages: articlePages(stories), status: map[string]int{}, down: map[string]bool{}, seen: map[string]int{}}
}

func articleURL(n int) string { return fmt.Sprintf("https://example.com/a%d", n) }

func (a *articleSite) Fetch(ctx context.Context, u, etag, modified string) (fetch.Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if u == siteList {
		a.sent = append(a.sent, etag)
		if during := a.during; during != nil {
			a.during = nil
			a.mu.Unlock()
			during()
			a.mu.Lock()
		}
		if a.etag != "" && etag == a.etag {
			return fetch.Result{URL: u, Status: 304, ETag: a.etag}, nil
		}
		return fetch.Result{Body: []byte(a.list), URL: u, Status: 200, ETag: a.etag}, nil
	}
	a.seen[u]++
	if a.down[u] {
		return fetch.Result{}, errors.New("connection reset")
	}
	switch code := a.status[u]; code {
	case 0:
	case 304:
		// The guarded fetcher reports a 304 without an error.
		return fetch.Result{URL: u, Status: code}, nil
	default:
		return fetch.Result{URL: u, Status: code}, &fetch.HTTPError{Status: code}
	}
	return fetch.Result{Body: []byte(a.pages[u]), URL: u, Status: 200}, nil
}

// answer makes articles first to last answer with an HTTP status; 0 serves
// their pages again.
func (a *articleSite) answer(code, first, last int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for n := first; n <= last; n++ {
		a.status[articleURL(n)] = code
	}
}

func (a *articleSite) set(change func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	change()
}

// requests reports how often article n was requested.
func (a *articleSite) requests(n int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.seen[articleURL(n)]
}

// requested fails the test unless articles first to last were each requested
// want times.
func (a *articleSite) requested(t *testing.T, want, first, last int) {
	t.Helper()
	for n := first; n <= last; n++ {
		if got := a.requests(n); got != want {
			t.Fatalf("article %d was requested %d times, want %d", n, got, want)
		}
	}
}

// storedBodies returns the titles of the stories stored with an article body.
func storedBodies(t *testing.T, s *store.Store, id string) map[string]bool {
	t.Helper()
	items, err := s.Items(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, it := range items {
		if it.FullHTML != "" {
			out[it.Title] = true
		}
	}
	return out
}

// ageStories moves every stored story out of its first hour, in which a
// failed article is still tried at each refresh.
func ageStories(t *testing.T, s *store.Store) {
	t.Helper()
	aged := fmt.Sprintf("UPDATE items SET first_seen=%d", time.Now().Add(-48*time.Hour).Unix())
	if _, err := s.DB.ExecContext(context.Background(), aged); err != nil {
		t.Fatal(err)
	}
}

// refreshOnce runs one scheduled refresh, which must succeed.
func refreshOnce(t *testing.T, jobs *Scheduler, id string) {
	t.Helper()
	jobs.refresh(context.Background(), reload(t, jobs.Store, id))
	if f := reload(t, jobs.Store, id); f.Error != "" {
		t.Fatalf("the refresh failed: %s", f.Error)
	}
}

// latestWarnings returns the warnings of a feed's latest stored run.
func latestWarnings(t *testing.T, s *store.Store, id string) []string {
	t.Helper()
	runs, err := s.Runs(context.Background(), id)
	if err != nil || len(runs) == 0 || runs[0].Diagnostics == nil {
		t.Fatalf("no stored diagnostics: %v", err)
	}
	var out []string
	for _, a := range runs[0].Diagnostics.Attempts {
		out = append(out, a.Warnings...)
	}
	return out
}

// failuresOf returns a copy of what the scheduler remembers of a feed's failed
// articles, or nil when it remembers none.
func failuresOf(jobs *Scheduler, id string) map[articleKey]articleFailure {
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	m, ok := jobs.articles[id]
	if !ok {
		return nil
	}
	out := map[articleKey]articleFailure{}
	for k, v := range m.failed {
		out[k] = v
	}
	return out
}

// remembered fails the test unless the memory holds exactly articles first to
// last, each with count failures of its own.
func remembered(t *testing.T, jobs *Scheduler, id string, count, first, last int) {
	t.Helper()
	got := failuresOf(jobs, id)
	if len(got) != last-first+1 {
		t.Fatalf("%d articles are remembered, want %d", len(got), last-first+1)
	}
	for n := first; n <= last; n++ {
		failure, ok := got[keyOf(articleURL(n))]
		if !ok || failure.count != count {
			t.Fatalf("article %d is remembered (%v) with %d failures, want %d", n, ok, failure.count, count)
		}
		if (count == 0) != failure.retry.IsZero() {
			t.Fatalf("article %d has %d failures and a backoff until %v", n, count, failure.retry)
		}
	}
}

// waitingLine is the warning's text, repeated here so that a change to what
// operators read has to be made twice.
const waitingLine = "Articles waiting to be tried again after failing or coming back empty: %d"

// The ten newest stories link to articles that always answer 404. They used
// to head the queue at every refresh and take all ten article requests, so
// the five stories behind them never got a body. Now each failed article
// waits out a backoff, which Refresh lifts and an edit forgets.
func TestArticlesThatAlwaysFailLeaveTheBudgetToTheOthers(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(15)
	site.answer(404, 1, 10)
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	refreshOnce(t, jobs, f.ID)
	site.requested(t, 1, 1, 10)
	ageStories(t, s)
	for range 4 {
		refreshOnce(t, jobs, f.ID)
	}
	if got := storedBodies(t, s, f.ID); len(got) != 5 || !got["Story 11"] || !got["Story 15"] {
		t.Fatalf("stored bodies for %v, want stories 11 to 15", got)
	}
	// Five refreshes requested each article once.
	site.requested(t, 1, 1, 15)
	remembered(t, jobs, f.ID, 1, 1, 10)
	for _, failure := range failuresOf(jobs, f.ID) {
		if wait := time.Until(failure.retry); wait < 59*time.Minute || wait > time.Hour {
			t.Fatalf("a first failure waits %v, want an hour", wait)
		}
	}
	// The run says how many stories wait, and nothing else about them.
	if got, want := latestWarnings(t, s, f.ID), []string{fmt.Sprintf(waitingLine, 10)}; !slices.Equal(got, want) {
		t.Fatalf("warnings %q, want %q", got, want)
	}

	// Refresh lifts the wait, as it lifts a hold, and keeps the counts: a
	// second failure waits twice as long.
	jobs.Retry(f.ID)
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 2, 1, 10)
	remembered(t, jobs, f.ID, 2, 1, 10)
	for _, failure := range failuresOf(jobs, f.ID) {
		if wait := time.Until(failure.retry); wait < 119*time.Minute || wait > 2*time.Hour {
			t.Fatalf("a second failure waits %v, want two hours", wait)
		}
	}
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 2, 1, 10)

	// A preview tries the first stories listed, whatever is remembered, and
	// leaves the memory as it was.
	before := failuresOf(jobs, f.ID)
	if _, err := jobs.Preview(ctx, reload(t, s, f.ID)); err != nil {
		t.Fatal(err)
	}
	site.requested(t, 3, 1, previewArticles)
	site.requested(t, 2, previewArticles+1, 10)
	if after := failuresOf(jobs, f.ID); !reflect.DeepEqual(after, before) {
		t.Fatal("a preview changed the memory")
	}

	// An edit may have fixed what failed, so every story is tried again.
	edited := reload(t, s, f.ID)
	edited.Title = "Renamed"
	if _, err := s.Save(ctx, edited); err != nil {
		t.Fatal(err)
	}
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 4, 1, previewArticles)
	site.requested(t, 3, previewArticles+1, 10)
	remembered(t, jobs, f.ID, 1, 1, 10)
}

// The backoff doubles from an hour up to a day.
func TestArticleBackoffDoublesUpToADay(t *testing.T) {
	jobs := New(nil, nil, 1, time.Second)
	f := model.Feed{ID: "feed", Version: 1, Recipe: fullRecipe()}
	key := keyOf(articleURL(1))
	run := articleRun{listed: []string{articleURL(1)}, tried: []articleTry{{key: articleURL(1), outcome: articleFault}}}
	for i, want := range []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 16 * time.Hour, 24 * time.Hour, 24 * time.Hour} {
		jobs.rememberArticles(context.Background(), f, run)
		failure := failuresOf(jobs, f.ID)[key]
		if wait := time.Until(failure.retry); failure.count != i+1 || wait > want || wait < want-time.Minute {
			t.Fatalf("failure %d waits %v, want %v", failure.count, wait, want)
		}
	}
}

// A source may finish an article minutes after listing the story, which is
// why a story is fetched again during its first hour. A failed article
// therefore does not wait in that hour: it is tried at each refresh, behind
// the stories never tried, and those tries add no failure, so the hour does
// not end with a backoff of a day.
func TestARecentStoryIsTriedAgainInsideItsBackoff(t *testing.T) {
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(15)
	site.answer(404, 1, 10)
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	refreshOnce(t, jobs, f.ID)
	refreshOnce(t, jobs, f.ID)
	// The five stories never tried came first, then five of the failed ones.
	if got := storedBodies(t, s, f.ID); len(got) != 5 || !got["Story 11"] || !got["Story 15"] {
		t.Fatalf("stored bodies for %v, want stories 11 to 15", got)
	}
	site.requested(t, 2, 1, 5)
	site.requested(t, 1, 6, 10)
	remembered(t, jobs, f.ID, 1, 1, 10)

	// The least recently tried go first. Article 3 is there now.
	site.answer(0, 3, 3)
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 3, 1, 5)
	site.requested(t, 2, 6, 10)
	if got := storedBodies(t, s, f.ID); len(got) != 6 || !got["Story 3"] {
		t.Fatalf("stored bodies for %v, want story 3 among six", got)
	}
	got := failuresOf(jobs, f.ID)
	if _, kept := got[keyOf(articleURL(3))]; kept || len(got) != 9 {
		t.Fatalf("a story with a body is still remembered among %d", len(got))
	}
	if warnings := latestWarnings(t, s, f.ID); slices.Contains(warnings, fmt.Sprintf(waitingLine, 9)) {
		t.Fatalf("recent stories were reported as waiting: %q", warnings)
	}

	// Past the hour they wait.
	ageStories(t, s)
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 3, 1, 2)
	site.requested(t, 2, 6, 10)
	if warnings := latestWarnings(t, s, f.ID); !slices.Contains(warnings, fmt.Sprintf(waitingLine, 9)) {
		t.Fatalf("warnings %q do not count the nine waiting stories", warnings)
	}
}

// Fetching a recent story's article again is a different matter from a story
// without one: its failure keeps the stored body and is not remembered.
func TestAFailedRefetchOfAStoredArticleIsNotRemembered(t *testing.T) {
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(2)
	site.answer(404, 1, 1)
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	refreshOnce(t, jobs, f.ID)
	site.answer(404, 2, 2)
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 2, 1, 2)
	remembered(t, jobs, f.ID, 1, 1, 1)
	if got := storedBodies(t, s, f.ID); len(got) != 1 || !got["Story 2"] {
		t.Fatalf("stored bodies for %v, want story 2 alone", got)
	}
}

// What backs a story off is what its own page did: it answered with an HTTP
// error status or a 304, its selector matched nothing, or it gave no body.
// The next refresh would most likely meet that again. A connection that
// failed says nothing about the story, which is only tried again later.
func TestOnlyAStorysOwnFaultsBackItOff(t *testing.T) {
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(6)
	site.answer(410, 1, 1)
	site.pages[articleURL(2)] = `<html><body><p>No article block here.</p></body></html>`
	site.answer(304, 3, 3)
	site.pages[articleURL(4)] = `<html><body><iframe class="body" src="https://example.com/player"></iframe></body></html>`
	site.down[articleURL(5)] = true
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	refreshOnce(t, jobs, f.ID)
	if got := storedBodies(t, s, f.ID); len(got) != 1 || !got["Story 6"] {
		t.Fatalf("stored bodies for %v, want story 6 alone", got)
	}
	got := failuresOf(jobs, f.ID)
	if len(got) != 5 {
		t.Fatalf("%d articles are remembered, want 5", len(got))
	}
	for n := 1; n <= 4; n++ {
		failure := got[keyOf(articleURL(n))]
		if wait := time.Until(failure.retry); failure.count != 1 || wait < 59*time.Minute || wait > time.Hour {
			t.Fatalf("article %d has %d failures and waits %v, want one and an hour", n, failure.count, wait)
		}
	}
	if failure := got[keyOf(articleURL(5))]; failure.count != 0 || !failure.retry.IsZero() || failure.tried.IsZero() {
		t.Fatalf("a failed connection was counted against the story: %+v", failure)
	}

	// Past their first hour the four wait, and the fifth is tried again.
	ageStories(t, s)
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 1, 1, 4)
	site.requested(t, 2, 5, 5)
	site.requested(t, 1, 6, 6)
	if warnings := latestWarnings(t, s, f.ID); len(warnings) != 2 || warnings[0] != fmt.Sprintf(waitingLine, 4) {
		t.Fatalf("warnings %q, want the count of four waiting stories and the failed connection", warnings)
	}
}

// Pausing and resuming are not edits: the recipe version stays, and so does
// what is remembered. Resuming lifts the waits through Retry, as it lifts a
// hold.
func TestPausingAFeedKeepsItsArticleMemory(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(2)
	site.answer(404, 1, 1)
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	refreshOnce(t, jobs, f.ID)
	ageStories(t, s)
	for _, enabled := range []bool{false, true} {
		if err := s.SetEnabled(ctx, f.ID, enabled); err != nil {
			t.Fatal(err)
		}
	}
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 1, 1, 2)
	remembered(t, jobs, f.ID, 1, 1, 1)

	jobs.Retry(f.ID)
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 2, 1, 1)
	remembered(t, jobs, f.ID, 2, 1, 1)
}

// outageRenderer renders article pages one at a time, as a Chromium pool
// does. While down is set every render fails, and the page at hang never
// loads.
type outageRenderer struct {
	pages map[string]string
	down  atomic.Bool
	hang  string
}

func (*outageRenderer) Capacity() int { return 2 }

func (r *outageRenderer) Render(ctx context.Context, u, wait string, settle int) (fetch.Result, error) {
	if r.down.Load() {
		return fetch.Result{}, errors.New("Chromium could not start")
	}
	if u == r.hang {
		<-ctx.Done()
	}
	if ctx.Err() != nil {
		return fetch.Result{}, ctx.Err()
	}
	return fetch.Result{Body: []byte(r.pages[u]), URL: u, Status: 200}, nil
}

// solverSite serves a site through FlareSolverr.
type solverSite struct{ *articleSite }

func (solverSite) Stats() (int, int) { return 0, 1 }
func (solverSite) Cooling() int      { return 0 }

// A failure that describes this server or the network says nothing about the
// story. Backing stories off for it would leave a feed without articles for
// an hour after an outage of one refresh. Such a story only goes behind those
// never tried, here stories 11 and 12.
func TestAnOutageDoesNotBackStoriesOff(t *testing.T) {
	const total = 12
	rendered := func() model.Recipe {
		r := fullRecipe()
		r.Mode = "static"
		r.Full.Browser = true
		return r
	}
	for _, tc := range []struct {
		name   string
		recipe model.Recipe
		// outage starts the trouble and returns what ends it.
		outage func(jobs *Scheduler, site *articleSite) (recover func())
	}{
		{"connection", fullRecipe(), func(jobs *Scheduler, site *articleSite) func() {
			site.set(func() {
				for n := 1; n <= total; n++ {
					site.down[articleURL(n)] = true
				}
			})
			return func() { site.set(func() { site.down = map[string]bool{} }) }
		}},
		{"Chromium", rendered(), func(jobs *Scheduler, site *articleSite) func() {
			renderer := &outageRenderer{pages: site.pages}
			renderer.down.Store(true)
			jobs.Renderer = renderer
			return func() { renderer.down.Store(false) }
		}},
		{"no renderer", rendered(), func(jobs *Scheduler, site *articleSite) func() {
			return func() { jobs.Renderer = &outageRenderer{pages: site.pages} }
		}},
		{"FlareSolverr", func() model.Recipe {
			r := rendered()
			r.Mode = "flaresolverr"
			return r
		}(), func(jobs *Scheduler, site *articleSite) func() {
			jobs.FlareSolverr = solverSite{site}
			site.set(func() {
				for n := 1; n <= total; n++ {
					site.down[articleURL(n)] = true
				}
			})
			return func() { site.set(func() { site.down = map[string]bool{} }) }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := openTestStore(t, 100)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			site := newArticleSite(total)
			jobs := New(s, site, 2, 30*time.Second)
			recover := tc.outage(jobs, site)
			f := saveFeed(t, jobs, tc.recipe)

			refreshOnce(t, jobs, f.ID)
			if got := storedBodies(t, s, f.ID); len(got) != 0 {
				t.Fatalf("stored bodies for %v during the outage", got)
			}
			remembered(t, jobs, f.ID, 0, 1, MaxArticlesPerRun)
			// Not even stories past their first hour wait.
			ageStories(t, s)
			recover()
			refreshOnce(t, jobs, f.ID)
			if got := storedBodies(t, s, f.ID); len(got) != MaxArticlesPerRun || !got["Story 11"] || !got["Story 12"] || !got["Story 8"] {
				t.Fatalf("stored bodies for %v, want stories 11, 12 and 1 to 8", got)
			}
			refreshOnce(t, jobs, f.ID)
			if got := storedBodies(t, s, f.ID); len(got) != total {
				t.Fatalf("stored %d bodies, want %d", len(got), total)
			}
			if got := failuresOf(jobs, f.ID); got != nil {
				t.Fatalf("%d stories are still remembered", len(got))
			}
		})
	}
}

// Rendered one at a time, an article page that never loads used to take the
// rest of every refresh's deadline ahead of the stories behind it. The
// deadline is not the story's failure, so it is not backed off, but it goes
// behind the stories not tried yet.
func TestAnArticleThatNeverLoadsDoesNotHoldBackTheRest(t *testing.T) {
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(6)
	jobs := New(s, site, 2, time.Second)
	jobs.Renderer = &outageRenderer{pages: site.pages, hang: articleURL(2)}
	r := fullRecipe()
	r.Mode = "static"
	r.Full.Browser = true
	f := saveFeed(t, jobs, r)

	refreshOnce(t, jobs, f.ID)
	if got := storedBodies(t, s, f.ID); len(got) != 1 || !got["Story 1"] {
		t.Fatalf("stored bodies for %v, want story 1 alone", got)
	}
	ageStories(t, s)
	refreshOnce(t, jobs, f.ID)
	if got := storedBodies(t, s, f.ID); len(got) != 5 || got["Story 2"] {
		t.Fatalf("stored bodies for %v, want every story but 2", got)
	}
	remembered(t, jobs, f.ID, 0, 2, 2)
}

// An article whose selector matches an element with nothing to publish comes
// back empty without an error. Nothing is stored for it, so it used to be
// fetched again at every refresh. It reported no failure, which is why the
// warning does not call it one.
func TestAnArticleThatComesBackEmptyIsBackedOff(t *testing.T) {
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(2)
	site.pages[articleURL(1)] = `<div class="body" data-html=""></div>`
	site.pages[articleURL(2)] = `<div class="body" data-html="&lt;p&gt;Full article 2.&lt;/p&gt;"></div>`
	jobs := New(s, site, 2, 30*time.Second)
	r := fullRecipe()
	r.Full.Attr = "data-html"
	f := saveFeed(t, jobs, r)

	refreshOnce(t, jobs, f.ID)
	if got := storedBodies(t, s, f.ID); len(got) != 1 || !got["Story 2"] {
		t.Fatalf("stored bodies for %v, want story 2 alone", got)
	}
	if got := latestWarnings(t, s, f.ID); len(got) != 0 {
		t.Fatalf("an empty article was reported as a failure: %q", got)
	}
	remembered(t, jobs, f.ID, 1, 1, 1)
	ageStories(t, s)
	for range 3 {
		refreshOnce(t, jobs, f.ID)
	}
	site.requested(t, 1, 1, 2)
	if got, want := latestWarnings(t, s, f.ID), []string{fmt.Sprintf(waitingLine, 1)}; !slices.Equal(got, want) {
		t.Fatalf("warnings %q, want %q", got, want)
	}
}

// Behind a source that answers 304, a round of failed articles is followed by
// conditional requests, as before. When the list page changes, the stories
// never tried are reached at once, and the failed ones, which the next
// refresh would not try either, do not count as a backlog.
func TestAListChangeReachesTheStoriesBehindFailedArticles(t *testing.T) {
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(16)
	site.list, site.etag = listPage(15), `"v1"`
	site.answer(404, 1, 10)
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	refreshOnce(t, jobs, f.ID)
	ageStories(t, s)
	refreshOnce(t, jobs, f.ID)
	if got := storedBodies(t, s, f.ID); len(got) != 0 {
		t.Fatalf("stored bodies for %v behind a 304", got)
	}
	site.set(func() {
		site.list = `<article><h2>Story 16</h2><a href="https://example.com/a16">l</a><p>teaser 16</p></article>` + listPage(15)
		site.etag = `"v2"`
	})
	refreshOnce(t, jobs, f.ID)
	if got := storedBodies(t, s, f.ID); len(got) != 6 || !got["Story 16"] || !got["Story 11"] {
		t.Fatalf("stored bodies for %v, want stories 11 to 16", got)
	}
	site.requested(t, 1, 1, 16)
	refreshOnce(t, jobs, f.ID)
	site.set(func() {
		if want := []string{"", `"v1"`, `"v1"`, `"v2"`}; !slices.Equal(site.sent, want) {
			t.Fatalf("list requests sent the validators %q, want %q", site.sent, want)
		}
	})
}

// Only a stored successful refresh that read the list page says anything
// about the feed's articles. A 304 lists no story, and a result that was not
// saved or was thrown away as stale has changed nothing.
func TestOnlyAStoredRefreshOfTheListChangesTheArticleMemory(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(4)
	site.list, site.etag = listPage(3), `"v1"`
	site.answer(404, 1, 1)
	site.answer(404, 4, 4)
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	refreshOnce(t, jobs, f.ID)
	remembered(t, jobs, f.ID, 1, 1, 1)
	ageStories(t, s)
	before := failuresOf(jobs, f.ID)

	refreshOnce(t, jobs, f.ID)
	if runs, err := s.Runs(ctx, f.ID); err != nil || len(runs) != 2 || runs[0].Status != 304 {
		t.Fatalf("the second refresh was not a stored 304: %v", err)
	}
	if after := failuresOf(jobs, f.ID); !reflect.DeepEqual(after, before) {
		t.Fatal("a 304 changed the memory")
	}

	// The page now lists story 4, whose article fails, but nothing can be
	// stored.
	site.set(func() { site.list, site.etag = listPage(4), `"v2"` })
	restore := blockItemWrites(t, s)
	jobs.refresh(ctx, reload(t, s, f.ID))
	restore()
	if !jobs.Held(f.ID) {
		t.Fatal("the refresh was saved")
	}
	site.requested(t, 1, 4, 4)
	if after := failuresOf(jobs, f.ID); !reflect.DeepEqual(after, before) {
		t.Fatal("a refresh that was not saved changed the memory")
	}

	// An edit lands while the next refresh runs, so its result is discarded.
	site.set(func() {
		site.during = func() {
			edited := reload(t, s, f.ID)
			edited.Title = "Renamed"
			if _, err := s.Save(ctx, edited); err != nil {
				t.Error(err)
			}
		}
	})
	stale := reload(t, s, f.ID)
	jobs.refresh(ctx, stale)
	if got := reload(t, s, f.ID); got.Version == stale.Version || !got.LastSuccess.Equal(stale.LastSuccess) {
		t.Fatalf("the refresh was not discarded as stale: version %d, was %d", got.Version, stale.Version)
	}
	site.requested(t, 2, 4, 4)
	// The edit lifted the wait, as Retry does, and kept the count.
	if got := failuresOf(jobs, f.ID); len(got) != 1 || got[keyOf(articleURL(1))].count != 1 {
		t.Fatalf("a discarded refresh changed the memory: %+v", got)
	}

	// The edited feed starts over: stories 1 and 4 are tried and remembered.
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 2, 1, 1)
	site.requested(t, 3, 4, 4)
	got := failuresOf(jobs, f.ID)
	if len(got) != 2 || got[keyOf(articleURL(1))].count != 1 || got[keyOf(articleURL(4))].count != 1 {
		t.Fatalf("after the edit: %+v", got)
	}
}

// The memory keeps only stories the list page still shows, which bounds it,
// and lives in the process: a restart tries every story again, and a deleted
// feed is forgotten.
func TestArticleMemoryKeepsOnlyListedStoriesOfLiveFeeds(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(15)
	site.answer(404, 1, 15)
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	refreshOnce(t, jobs, f.ID)
	remembered(t, jobs, f.ID, 1, 1, 10)
	ageStories(t, s)
	refreshOnce(t, jobs, f.ID)
	remembered(t, jobs, f.ID, 1, 1, 15)
	site.set(func() { site.list = listPage(3) })
	refreshOnce(t, jobs, f.ID)
	remembered(t, jobs, f.ID, 1, 1, 3)
	site.requested(t, 1, 1, 15)

	restarted := New(s, site, 2, 30*time.Second)
	refreshOnce(t, restarted, f.ID)
	site.requested(t, 2, 1, 3)

	if err := s.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	jobs.tick(ctx)
	jobs.Wait()
	if got := failuresOf(jobs, f.ID); got != nil {
		t.Fatalf("a deleted feed kept %d remembered articles", len(got))
	}
}

// datedListPage lists stories an hour apart, newest first, so MAX_ITEMS
// retention keeps the first ones listed and never the last.
func datedListPage(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		date := time.Now().UTC().Add(-time.Duration(i) * time.Hour).Format(time.RFC3339)
		fmt.Fprintf(&b, `<article><h2>Story %d</h2><a href="https://example.com/a%d">l</a><time>%s</time><p>teaser %d</p></article>`, i, i, date, i)
	}
	return b.String()
}

func datedRecipe() model.Recipe {
	r := fullRecipe()
	r.Date = model.Field{Selector: "time"}
	return r
}

// A list page can show more stories than MAX_ITEMS keeps. Retention removes
// the oldest in the refresh that fetched their articles, so they are listed
// without a stored body at every refresh. Were they forgotten like stories
// that kept their body, they would count as never tried each time, and ten of
// them would take every article request ahead of a stored story whose article
// failed once. They are fetched again after it instead.
func TestStoriesRetentionDoesNotKeepLeaveAFailedArticleItsTurn(t *testing.T) {
	for _, tc := range []struct {
		name string
		// fail makes article 5 fail, and returns what ends that.
		fail func(site *articleSite) (recover func())
		// lift asks for a Refresh, which ends a backoff.
		lift bool
	}{
		{"connection", func(site *articleSite) func() {
			site.set(func() { site.down[articleURL(5)] = true })
			return func() { site.set(func() { site.down = map[string]bool{} }) }
		}, false},
		{"status and Refresh", func(site *articleSite) func() {
			site.answer(503, 5, 5)
			return func() { site.answer(0, 5, 5) }
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const kept, total = 20, 30
			s, err := openTestStore(t, kept)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			site := newArticleSite(total)
			site.list = datedListPage(total)
			recover := tc.fail(site)
			jobs := New(s, site, 2, 30*time.Second)
			f := saveFeed(t, jobs, datedRecipe())

			refreshOnce(t, jobs, f.ID)
			recover()
			ageStories(t, s)
			if tc.lift {
				jobs.Retry(f.ID)
			}
			// Stories never tried go first: 11 to 20, then 21 to 30, which
			// retention does not keep.
			refreshOnce(t, jobs, f.ID)
			refreshOnce(t, jobs, f.ID)
			site.requested(t, 1, 1, total)
			if got := storedBodies(t, s, f.ID); len(got) != kept-1 || got["Story 5"] || got["Story 21"] {
				t.Fatalf("stored bodies for %v, want stories 1 to 20 without 5", got)
			}
			// Now story 5 leads the ten that were fetched in vain.
			refreshOnce(t, jobs, f.ID)
			site.requested(t, 2, 5, 5)
			if got := storedBodies(t, s, f.ID); len(got) != kept || !got["Story 5"] {
				t.Fatalf("stored bodies for %v, want stories 1 to 20", got)
			}
			site.requested(t, 2, 21, 29)
			site.requested(t, 1, 30, 30)
			// Fetching them was no failure of theirs, so none waits.
			remembered(t, jobs, f.ID, 0, 21, 30)
			refreshOnce(t, jobs, f.ID)
			site.requested(t, 2, 30, 30)
			if warnings := latestWarnings(t, s, f.ID); len(warnings) != 0 {
				t.Fatalf("warnings %q for articles that were fetched", warnings)
			}
		})
	}
}

// While its article fails, a stored story is tried at every refresh ahead of
// the stories retention does not keep, however many of those have waited
// longer for their turn. They used to follow it in list order, being the
// oldest, so it is tried no later than it was.
func TestAFailedArticleIsTriedBeforeStoriesRetentionDoesNotKeep(t *testing.T) {
	const kept, total = 20, 45
	s, err := openTestStore(t, kept)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(total)
	site.list = datedListPage(total)
	site.set(func() { site.down[articleURL(5)] = true })
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, datedRecipe())

	// Four refreshes try stories 1 to 40, and the fifth the last five.
	for range 4 {
		refreshOnce(t, jobs, f.ID)
	}
	site.requested(t, 1, 1, 40)
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 2, 5, 5)
	site.requested(t, 2, 21, 24)
	site.requested(t, 1, 25, 45)
	// Twenty-one stories retention does not keep were fetched before story 5
	// was last tried, and it still goes first.
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 3, 5, 5)
	site.requested(t, 2, 25, 33)
	site.requested(t, 1, 34, 45)
	site.set(func() { site.down = map[string]bool{} })
	refreshOnce(t, jobs, f.ID)
	if got := storedBodies(t, s, f.ID); len(got) != kept || !got["Story 5"] {
		t.Fatalf("stored bodies for %v, want stories 1 to 20", got)
	}
	remembered(t, jobs, f.ID, 0, 21, total)
}

// A story retention does not keep has no first hour: once its own article
// failed it waits like any other, and the count includes it, so the count
// can exceed the number of stories the feed stores.
func TestAFailedStoryRetentionDoesNotKeepWaitsAndIsCounted(t *testing.T) {
	const kept, total = 5, 15
	s, err := openTestStore(t, kept)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(total)
	site.list = datedListPage(total)
	site.answer(404, 1, total)
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, datedRecipe())

	refreshOnce(t, jobs, f.ID)
	ageStories(t, s)
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 1, 1, total)
	if got, want := latestWarnings(t, s, f.ID)[0], fmt.Sprintf(waitingLine, 10); got != want {
		t.Fatalf("first warning %q, want %q", got, want)
	}
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 1, 1, total)
	remembered(t, jobs, f.ID, 1, 1, total)
	if items, err := s.Items(context.Background(), f.ID); err != nil || len(items) != kept {
		t.Fatalf("%d stories are stored (%v), want %d", len(items), err, kept)
	}
	if got, want := latestWarnings(t, s, f.ID), []string{fmt.Sprintf(waitingLine, total)}; !slices.Equal(got, want) {
		t.Fatalf("warnings %q, want %q", got, want)
	}
}

// A run keeps only so many warnings, and a list page can fill them all by
// itself, here with a date the recipe does not recognize in every story.
// Stories that wait are not tried, so they add no warning of their own: the
// count is the only line that says why they have no article, and it leads.
func TestTheWaitingCountIsKeptWhenTheListPageFillsTheWarnings(t *testing.T) {
	const total = diagnostics.MaxWarnings + 5
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	var page strings.Builder
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&page, `<article><h2>Story %d</h2><a href="https://example.com/a%d">l</a><time>sometime</time><p>teaser %d</p></article>`, i, i, i)
	}
	site := newArticleSite(total)
	site.list = page.String()
	site.answer(404, 1, 1)
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, datedRecipe())

	for range 3 {
		refreshOnce(t, jobs, f.ID)
	}
	ageStories(t, s)
	refreshOnce(t, jobs, f.ID)
	remembered(t, jobs, f.ID, 1, 1, 1)
	runs, err := s.Runs(context.Background(), f.ID)
	if err != nil || len(runs) == 0 || runs[0].Diagnostics == nil || len(runs[0].Diagnostics.Attempts) != 1 {
		t.Fatalf("no stored diagnostics: %v", err)
	}
	attempt := runs[0].Diagnostics.Attempts[0]
	if len(attempt.Warnings) != diagnostics.MaxWarnings || attempt.Warnings[0] != fmt.Sprintf(waitingLine, 1) {
		t.Fatalf("%d warnings starting with %q, want %d led by the count of waiting stories", len(attempt.Warnings), attempt.Warnings[0], diagnostics.MaxWarnings)
	}
	// The line it displaced is counted with the others that did not fit.
	if want := total + 1 - diagnostics.MaxWarnings; attempt.WarningsOmitted != want {
		t.Fatalf("%d warnings omitted, want %d", attempt.WarningsOmitted, want)
	}
}

// More failed stories than one refresh fetches take turns: the least recently
// tried go first, or those past the tenth would never be tried again.
func TestFailedStoriesTakeTurnsLeastRecentlyTriedFirst(t *testing.T) {
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(15)
	site.answer(404, 1, 15)
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	// Every story stays in its first hour, so none waits. Stories 11 to 15
	// are first tried in the second refresh, after which nothing is new.
	refreshOnce(t, jobs, f.ID)
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 2, 1, 5)
	site.requested(t, 1, 6, 15)
	// Stories 6 to 10 were tried longest ago, then 11 to 15. Stories tried in
	// the same refresh keep their list order.
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 3, 1, 5)
	site.requested(t, 2, 6, 10)
	site.requested(t, 1, 11, 15)
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 4, 1, 5)
	site.requested(t, 2, 6, 10)
	site.requested(t, 2, 11, 15)
	remembered(t, jobs, f.ID, 1, 1, 15)
}

// A feed that no longer fetches articles has none to remember. The edit's
// new version would hide the old failures, but not free them.
func TestAFeedWithoutAnArticleSelectorKeepsNoArticleMemory(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	site := newArticleSite(2)
	site.answer(404, 1, 1)
	jobs := New(s, site, 2, 30*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	refreshOnce(t, jobs, f.ID)
	remembered(t, jobs, f.ID, 1, 1, 1)
	edited := reload(t, s, f.ID)
	edited.Recipe.Full = nil
	if _, err := s.Save(ctx, edited); err != nil {
		t.Fatal(err)
	}
	refreshOnce(t, jobs, f.ID)
	site.requested(t, 1, 1, 2)
	if got := failuresOf(jobs, f.ID); got != nil {
		t.Fatalf("%d articles are still remembered", len(got))
	}
}
