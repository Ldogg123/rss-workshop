package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
)

const sourceETag, sourceModified = `"v1"`, "Mon, 02 Nov 2026 10:00:00 GMT"

// validators are what a list request sent.
type validators struct{ etag, modified string }

var (
	unconditional = validators{}
	conditional   = validators{sourceETag, sourceModified}
)

// conditionalSource is a static site whose list pages carry validators and
// answer 304 when a request sends them back, as most servers do. It records
// the validators of every list request and counts article requests.
type conditionalSource struct {
	mu       sync.Mutex
	lists    map[string]string
	articles map[string]string
	fail     map[string]bool
	sent     map[string][]validators
	fetches  int
}

func newConditionalSource(stories int) *conditionalSource {
	return &conditionalSource{lists: map[string]string{}, articles: articlePages(stories), fail: map[string]bool{}, sent: map[string][]validators{}}
}

func (c *conditionalSource) Fetch(ctx context.Context, u, etag, modified string) (fetch.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if list, ok := c.lists[u]; ok {
		c.sent[u] = append(c.sent[u], validators{etag, modified})
		if etag == sourceETag {
			return fetch.Result{URL: u, Status: 304, ETag: sourceETag}, nil
		}
		return fetch.Result{Body: []byte(list), URL: u, Status: 200, ETag: sourceETag, LastModified: sourceModified}, nil
	}
	c.fetches++
	if body, ok := c.articles[u]; ok && !c.fail[u] {
		return fetch.Result{Body: []byte(body), URL: u, Status: 200}, nil
	}
	return fetch.Result{}, fmt.Errorf("cannot reach %s", u)
}

func (c *conditionalSource) articleFetches() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetches
}

// feed saves a feed listing page and returns its ID.
func (c *conditionalSource) feed(t *testing.T, jobs *Scheduler, url, page string, r model.Recipe) string {
	t.Helper()
	c.mu.Lock()
	c.lists[url] = page
	c.mu.Unlock()
	id, err := jobs.Store.Save(context.Background(), model.Feed{Title: "Backlog", URL: url, Interval: 60, Enabled: true, Recipe: r})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// round is what one refresh stored and sent: the article bodies saved after
// it, and the validators of its list request.
type round struct {
	bodies int
	sent   validators
}

// expect runs scheduled refreshes of a saved feed, one per round.
func (c *conditionalSource) expect(t *testing.T, jobs *Scheduler, id string, rounds ...round) {
	t.Helper()
	ctx := context.Background()
	for i, want := range rounds {
		f, err := jobs.Store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		jobs.refresh(ctx, f)
		if after, err := jobs.Store.Get(ctx, id); err != nil || after.Error != "" {
			t.Fatalf("refresh %d failed: %v %q", i+1, err, after.Error)
		}
		items, err := jobs.Store.Items(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		got := round{}
		for _, it := range items {
			if it.FullHTML != "" {
				got.bodies++
			}
		}
		c.mu.Lock()
		got.sent = c.sent[f.URL][len(c.sent[f.URL])-1]
		c.mu.Unlock()
		if got != want {
			t.Fatalf("refresh %d stored %d bodies after sending %+v; want %d after %+v", i+1, got.bodies, got.sent, want.bodies, want.sent)
		}
	}
}

// A 304 skips extraction, so a refresh fetched articles only when the list
// page changed: a long list behind a source with an ETag stopped at its first
// ten bodies. The list is requested in full until every story has one, then
// conditional requests resume and fetch no articles.
func TestArticleBacklogDrainsBehindConditionalRequests(t *testing.T) {
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	const total = 2*MaxArticlesPerRun + 5
	source := newConditionalSource(total)
	jobs := New(s, source, 2, 30*time.Second)
	id := source.feed(t, jobs, "https://example.com/list", listPage(total), fullRecipe())

	source.expect(t, jobs, id,
		round{MaxArticlesPerRun, unconditional},
		round{2 * MaxArticlesPerRun, unconditional},
		round{total, unconditional},
		round{total, conditional})
	before := source.articleFetches()
	source.expect(t, jobs, id, round{total, conditional})
	if n := source.articleFetches() - before; n != 0 {
		t.Errorf("a 304 refresh fetched %d articles", n)
	}
}

// An article that keeps failing stays without a body. That must not keep the
// list request unconditional: only a refresh that left stories unqueued and
// stored a body continues the backlog.
func TestFailingArticlesDoNotKeepListRequestsUnconditional(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stories int
		failing []int
		rounds  []round
	}{
		// Every other article was fetched, so nothing was left unqueued.
		{"one failing article", 3, []int{2}, []round{{2, unconditional}, {2, conditional}, {2, conditional}}},
		// Ten bodies, then a refresh that queued only failures.
		{"fifteen failing articles", 25, []int{11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25},
			[]round{{10, unconditional}, {10, unconditional}, {10, conditional}, {10, conditional}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := openTestStore(t, 100)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			source := newConditionalSource(tc.stories)
			for _, n := range tc.failing {
				source.fail[fmt.Sprintf("https://example.com/a%d", n)] = true
			}
			jobs := New(s, source, 2, 30*time.Second)
			id := source.feed(t, jobs, "https://example.com/list", listPage(tc.stories), fullRecipe())
			source.expect(t, jobs, id, tc.rounds...)
		})
	}
}

// Retention removes a story in the refresh that stored it, every time, when
// the list page shows more stories than MAX_ITEMS keeps. Neither a body stored
// for such a story nor a story left behind that is never stored is progress,
// so they must not keep the list request unconditional.
func TestStoriesRetentionRemovesAreNoBacklog(t *testing.T) {
	const total = 25
	for _, tc := range []struct {
		name        string
		oldestFirst bool
		bodies      int // retention keeps the 5 newest; articles 1 to 10 are fetched
	}{{"newest first", false, 5}, {"oldest first", true, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := openTestStore(t, 5)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			var page strings.Builder
			for i := 1; i <= total; i++ {
				age := time.Duration(i) * time.Hour
				if tc.oldestFirst {
					age = time.Duration(total+1-i) * time.Hour
				}
				date := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC).Add(-age).Format(time.RFC3339)
				fmt.Fprintf(&page, `<article><h2>Story %d</h2><a href="https://example.com/a%d">l</a><time>%s</time><p>teaser %d</p></article>`, i, i, date, i)
			}
			r := fullRecipe()
			r.Date = model.Field{Selector: "time"}
			source := newConditionalSource(total)
			jobs := New(s, source, 2, 30*time.Second)
			id := source.feed(t, jobs, "https://example.com/list", page.String(), r)
			source.expect(t, jobs, id, round{tc.bodies, unconditional}, round{tc.bodies, conditional}, round{tc.bodies, conditional})
		})
	}
}

// stallingRenderer renders article pages. While stall is set, every page but
// the first waits for the refresh's deadline.
type stallingRenderer struct {
	pages map[string]string
	stall atomic.Bool
}

func (r *stallingRenderer) Render(ctx context.Context, u, wait string, settle int) (fetch.Result, error) {
	if r.stall.Load() && u != "https://example.com/a1" {
		<-ctx.Done()
	}
	if ctx.Err() != nil {
		return fetch.Result{}, ctx.Err()
	}
	return fetch.Result{Body: []byte(r.pages[u]), URL: u, Status: 200}, nil
}

// The refresh deadline stops queueing articles as surely as the cap does,
// here for a static list whose articles Chromium renders.
func TestArticlesLeftByTheDeadlineAreABacklog(t *testing.T) {
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	const total = 8 // fewer than the cap
	source := newConditionalSource(total)
	renderer := &stallingRenderer{pages: articlePages(total)}
	renderer.stall.Store(true)
	jobs := New(s, source, 2, time.Second)
	jobs.Renderer = renderer
	r := fullRecipe()
	r.Mode = "static"
	r.Full.Browser = true
	id := source.feed(t, jobs, "https://example.com/list", listPage(total), r)

	source.expect(t, jobs, id, round{1, unconditional})
	renderer.stall.Store(false)
	source.expect(t, jobs, id, round{total, unconditional}, round{total, conditional})
}

// The flag lives in memory, so the first refresh after a start sends no
// validators for a full-content feed. That is how feeds stuck behind a 304
// before an upgrade drain theirs. A feed with no backlog is conditional again
// at once, and a feed without full content keeps its 304s throughout.
func TestAStartRequestsEachFullContentListOnce(t *testing.T) {
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	source := newConditionalSource(25)
	jobs := New(s, source, 2, 30*time.Second)
	plain := fullRecipe()
	plain.Full = nil
	backlog := source.feed(t, jobs, "https://example.com/backlog", listPage(25), fullRecipe())
	complete := source.feed(t, jobs, "https://example.com/complete", listPage(3), fullRecipe())
	teasers := source.feed(t, jobs, "https://example.com/teasers", listPage(3), plain)
	source.expect(t, jobs, backlog, round{10, unconditional})
	source.expect(t, jobs, complete, round{3, unconditional}, round{3, conditional})
	source.expect(t, jobs, teasers, round{0, unconditional}, round{0, conditional})

	restarted := New(s, source, 2, 30*time.Second)
	source.expect(t, restarted, backlog, round{20, unconditional})
	source.expect(t, restarted, complete, round{3, unconditional}, round{3, conditional})
	source.expect(t, restarted, teasers, round{0, conditional})
}

// A preview fetches a sample with no stored history, so it says nothing about
// a feed's backlog: the next refresh still counts as the first after a start.
func TestPreviewLeavesTheBacklogAlone(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	source := newConditionalSource(3)
	jobs := New(s, source, 2, 30*time.Second)
	id := source.feed(t, jobs, "https://example.com/list", listPage(3), fullRecipe())
	source.expect(t, jobs, id, round{3, unconditional}, round{3, conditional})

	restarted := New(s, source, 2, 30*time.Second)
	f, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Preview(ctx, f); err != nil {
		t.Fatal(err)
	}
	restarted.mu.Lock()
	_, recorded := restarted.backlog[id]
	restarted.mu.Unlock()
	if recorded {
		t.Fatal("a preview recorded a backlog")
	}
	source.expect(t, restarted, id, round{3, unconditional}, round{3, conditional})
}

// Flags must not accumulate for feeds the operator has deleted.
func TestBacklogFlagsAreForgottenForDeletedFeeds(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	source := newConditionalSource(25)
	jobs := New(s, source, 2, 30*time.Second)
	kept := source.feed(t, jobs, "https://example.com/kept", listPage(25), fullRecipe())
	deleted := source.feed(t, jobs, "https://example.com/deleted", listPage(25), fullRecipe())
	source.expect(t, jobs, kept, round{10, unconditional})
	source.expect(t, jobs, deleted, round{10, unconditional})
	if err := s.Delete(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	// Both feeds just refreshed, so the tick starts neither.
	jobs.tick(ctx)
	jobs.Wait()
	jobs.mu.Lock()
	_, remembered := jobs.backlog[deleted]
	backlog := jobs.backlog[kept]
	jobs.mu.Unlock()
	if remembered {
		t.Error("a backlog flag survived for a feed that is not in the library")
	}
	if !backlog {
		t.Error("the backlog flag of a live feed was dropped")
	}
}
