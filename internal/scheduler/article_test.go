package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rss-workshop/internal/diagnostics"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
)

// pageFetcher serves a list page plus one page per article, and records which
// article URLs were actually requested.
type pageFetcher struct {
	list     string
	articles map[string]string
	fail     map[string]bool
	mu       sync.Mutex
	seen     map[string]int
	calls    atomic.Int32
}

func newPageFetcher(list string, articles map[string]string) *pageFetcher {
	return &pageFetcher{list: list, articles: articles, fail: map[string]bool{}, seen: map[string]int{}}
}

func (p *pageFetcher) Fetch(ctx context.Context, u, e, m string) (fetch.Result, error) {
	p.calls.Add(1)
	p.mu.Lock()
	p.seen[u]++
	failed := p.fail[u]
	p.mu.Unlock()
	if u == "https://example.com/list" {
		return fetch.Result{Body: []byte(p.list), URL: u, Status: 200}, nil
	}
	if failed {
		return fetch.Result{}, fmt.Errorf("cannot reach %s", u)
	}
	body, ok := p.articles[u]
	if !ok {
		return fetch.Result{}, fmt.Errorf("no such article at %s", u)
	}
	return fetch.Result{Body: []byte(body), URL: u, Status: 200}, nil
}

func (p *pageFetcher) setFail(urls ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, u := range urls {
		p.fail[u] = true
	}
}

func (p *pageFetcher) count(u string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[u]
}

func listPage(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, `<article><h2>Story %d</h2><a href="https://example.com/a%d">l</a><p>teaser %d</p></article>`, i, i, i)
	}
	return b.String()
}

func fullRecipe() model.Recipe {
	return model.Recipe{Type: "css", Items: "article",
		Title:   model.Field{Selector: "h2"},
		Link:    model.Field{Selector: "a"},
		Content: model.Field{Selector: "p"},
		Full:    &model.FullContent{Selector: ".body"}}
}

func articlePages(n int) map[string]string {
	out := map[string]string{}
	for i := 1; i <= n; i++ {
		out[fmt.Sprintf("https://example.com/a%d", i)] =
			fmt.Sprintf(`<html><body><nav>skip</nav><div class="body"><p>Full article %d.</p></div></body></html>`, i)
	}
	return out
}

func saveFeed(t *testing.T, jobs *Scheduler, r model.Recipe) model.Feed {
	t.Helper()
	id, err := jobs.Store.Save(context.Background(), model.Feed{Title: "Full", URL: "https://example.com/list", Interval: 60, Enabled: true, Recipe: r})
	if err != nil {
		t.Fatal(err)
	}
	f, err := jobs.Store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestArticleBodiesReplaceTheTeaserAndSurviveLaterRefreshes(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(listPage(2), articlePages(2))
	jobs := New(s, p, 2, 10*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	jobs.refresh(ctx, f)
	items, err := s.Items(ctx, f.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("stored %d items: %v", len(items), err)
	}
	for _, it := range items {
		if !strings.Contains(it.FullHTML, "Full article") {
			t.Fatalf("item %q kept no article body: %q", it.Title, it.FullHTML)
		}
		if !strings.Contains(it.HTML, "teaser") {
			t.Errorf("item %q lost its list-page teaser: %q", it.Title, it.HTML)
		}
	}
	before := map[string]model.Item{}
	for _, it := range items {
		before[it.Key] = it
	}

	// A later refresh that fetches no article (every item is now old news) must
	// keep the stored bodies. This is the case that makes re-extracting the list
	// page on every refresh safe.
	f, err = s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	aged := fmt.Sprintf("UPDATE items SET first_seen=%d", time.Now().Add(-48*time.Hour).Unix())
	if _, err := s.DB.ExecContext(ctx, aged); err != nil {
		t.Fatal(err)
	}
	p.setFail("https://example.com/a1", "https://example.com/a2")
	jobs.refresh(ctx, f)

	after, err := s.Items(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("second refresh stored %d items", len(after))
	}
	for _, it := range after {
		prior := before[it.Key]
		if it.FullHTML != prior.FullHTML || it.FullHTML == "" {
			t.Errorf("refresh without enrichment lost the article body for %q: %q", it.Title, it.FullHTML)
		}
		if !it.Published.Equal(prior.Published) || it.GUID != prior.GUID {
			t.Errorf("enrichment changed item identity for %q", it.Title)
		}
	}
	// An aged item with a stored body is not refetched.
	if n := p.count("https://example.com/a1"); n != 1 {
		t.Errorf("aged item was fetched %d times, want 1", n)
	}
}

func TestArticleFailureKeepsTheItemAndItsTeaser(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(listPage(2), articlePages(2))
	p.setFail("https://example.com/a1")
	jobs := New(s, p, 2, 10*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	jobs.refresh(ctx, f)
	items, err := s.Items(ctx, f.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("a failing article dropped items: %d %v", len(items), err)
	}
	feed, err := s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if feed.Error != "" || feed.Failures != 0 {
		t.Errorf("one unreachable article failed the whole refresh: %q", feed.Error)
	}
	for _, it := range items {
		if it.Title == "Story 1" {
			if it.FullHTML != "" || !strings.Contains(it.HTML, "teaser 1") {
				t.Errorf("failed article did not fall back to the teaser: %q / %q", it.FullHTML, it.HTML)
			}
		} else if !strings.Contains(it.FullHTML, "Full article 2") {
			t.Errorf("a sibling article was not fetched: %q", it.FullHTML)
		}
	}
	runs, err := s.Runs(ctx, f.ID)
	if err != nil || len(runs) == 0 {
		t.Fatal(err)
	}
	if runs[0].Status != 200 {
		t.Errorf("run recorded status %d", runs[0].Status)
	}
}

func TestArticleFetchingIsBoundedPerRefresh(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	const total = 25
	p := newPageFetcher(listPage(total), articlePages(total))
	jobs := New(s, p, 2, 30*time.Second)
	f := saveFeed(t, jobs, fullRecipe())

	jobs.refresh(ctx, f)
	items, err := s.Items(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != total {
		t.Fatalf("stored %d of %d items", len(items), total)
	}
	enriched := 0
	for _, it := range items {
		if it.FullHTML != "" {
			enriched++
		}
	}
	if enriched != MaxArticlesPerRun {
		t.Fatalf("one refresh enriched %d articles, want the %d cap", enriched, MaxArticlesPerRun)
	}
	// The rest are not lost: the backlog drains over later refreshes.
	f, err = s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	jobs.refresh(ctx, f)
	items, err = s.Items(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := 0
	for _, it := range items {
		if it.FullHTML != "" {
			second++
		}
	}
	if second <= enriched {
		t.Errorf("a second refresh made no progress on the backlog: %d then %d", enriched, second)
	}
}

func TestPreviewFetchesOnlyASample(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(listPage(10), articlePages(10))
	jobs := New(s, p, 2, 10*time.Second)

	out, err := jobs.Preview(ctx, model.Feed{URL: "https://example.com/list", Recipe: fullRecipe()})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 10 {
		t.Fatalf("preview returned %d items", len(out.Items))
	}
	enriched := 0
	for _, it := range out.Items {
		if it.FullHTML != "" {
			enriched++
		}
	}
	if enriched != previewArticles {
		t.Errorf("preview fetched %d articles, want the %d sample", enriched, previewArticles)
	}
}

func TestRecipeWithoutFullContentFetchesNoArticles(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(listPage(3), articlePages(3))
	r := fullRecipe()
	r.Full = nil
	jobs := New(s, p, 2, 10*time.Second)
	f := saveFeed(t, jobs, r)

	jobs.refresh(ctx, f)
	if n := p.calls.Load(); n != 1 {
		t.Errorf("a recipe without full content made %d fetches, want only the list page", n)
	}
}

// Article warnings are appended after extract() sanitizes the trace. They stay
// bounded and redacted because store.encodeDiagnostics sanitizes again on
// write; this guards that path, since a fetch error quotes the article URL.
func TestArticleWarningsStayBoundedAndRedacted(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(listPage(25), map[string]string{})
	jobs := New(s, p, 2, 30*time.Second)
	f := saveFeed(t, jobs, fullRecipe())
	jobs.refresh(ctx, f)

	runs, err := s.Runs(ctx, f.ID)
	if err != nil || len(runs) == 0 || runs[0].Diagnostics == nil {
		t.Fatal("no stored diagnostics", err)
	}
	for _, a := range runs[0].Diagnostics.Attempts {
		if len(a.Warnings) > diagnostics.MaxWarnings {
			t.Errorf("stored %d warnings, cap is %d", len(a.Warnings), diagnostics.MaxWarnings)
		}
		for _, w := range a.Warnings {
			if strings.Contains(w, "https://example.com/a") {
				t.Errorf("stored warning leaked an absolute URL: %q", w)
			}
			if len(w) > 512 {
				t.Errorf("stored warning is %d bytes", len(w))
			}
		}
	}
}
