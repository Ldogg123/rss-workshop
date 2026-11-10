package scheduler

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

const (
	// MaxArticlesPerRun bounds how many item pages one refresh may fetch. A
	// larger feed is not truncated: items left unfetched keep an empty stored
	// body and are picked up by the next refresh, so a backlog drains over a
	// few cycles instead of hammering the source in one burst.
	MaxArticlesPerRun = 10
	// articleWorkers bounds concurrent requests to a single source. Through
	// FlareSolverr or a Chromium pool a feed fetches its articles one at a
	// time instead.
	articleWorkers = 4
	// articleRecency keeps re-fetching a recently discovered item, because a
	// source may publish its images or finish its article body minutes after
	// the story first appears on the list page. Older items are fetched once.
	articleRecency = time.Hour
	// previewArticles is the sample a preview fetches. A preview has no stored
	// history and runs while an operator waits, so it demonstrates the selector
	// rather than building the whole feed.
	previewArticles = 3
)

// articleRun is what one round of article fetching did.
type articleRun struct {
	warnings []string
	// fetched holds the keys of the stories this round gave a body.
	fetched []string
	// left holds the keys of listed stories without a stored body that this
	// round did not queue, because of MaxArticlesPerRun or the deadline. A
	// story handed to a worker after the deadline was not queued in time.
	left []string
	// listed holds the keys of the listed stories with a link, and had how
	// many of them known showed with a body before this round.
	listed []string
	had    int
	// tried holds the outcome for each story without a stored body that a
	// worker took before the deadline. A refresh hands it to rememberArticles
	// once its result is stored.
	tried []articleTry
}

// fetchArticles fills in item bodies from each item's own page.
//
// known says, per item key, whether an article body is already stored and when
// the item was first seen; a refresh passes the saved state so an article is
// fetched once and then left alone, and a preview passes nil. Items are never dropped or reordered: an item whose
// article cannot be fetched keeps the list-page teaser it already has.
//
// A refresh also reads what this process remembers of the feed's failed
// articles, and reports what it tried for rememberArticles. A preview does
// neither: it tries the first stories listed.
func (s *Scheduler) fetchArticles(ctx context.Context, f model.Feed, items []model.Item, known map[string]store.ArticleState, preview bool) (run articleRun) {
	if f.Recipe.Full == nil || f.Recipe.Full.Selector == "" {
		return run
	}
	limit := MaxArticlesPerRun
	if preview {
		limit = previewArticles
	}
	// Items that have never been fetched come first. Re-fetching recent items
	// competes for the same budget, so without this priority a feed larger than
	// the per-run cap would keep refreshing its newest stories and never reach
	// the backlog behind them.
	//
	// Among them, stories whose article this process has not tried lead, in
	// list order. One whose article failed follows, least recently tried
	// first, and only once its backoff has passed or while the story is
	// recent: otherwise articles that always fail would head the queue at
	// every refresh and take the whole budget from the stories behind them.
	var failed map[articleKey]articleFailure
	if !preview {
		failed = s.articleFailures(f)
	}
	now := time.Now()
	var fresh, again, recent []int
	// tries holds the stories without a stored body that are queued: when
	// each was last tried, and whether a failure of its own in this round
	// backs it off.
	type try struct {
		last   time.Time
		counts bool
	}
	tries := map[int]try{}
	waiting := 0
	for i := range items {
		if items[i].URL == "" {
			continue
		}
		prior, ok := known[items[i].Key]
		run.listed = append(run.listed, items[i].Key)
		if prior.HasBody {
			run.had++
		}
		// A source may finish an article minutes after listing it.
		isRecent := ok && (prior.FirstSeen.IsZero() || now.Sub(prior.FirstSeen) <= articleRecency)
		switch {
		case preview:
			fresh = append(fresh, i)
		case !ok, !prior.HasBody:
			failure, tried := articleFailure{}, false
			if len(failed) > 0 {
				failure, tried = failed[keyOf(items[i].Key)]
			}
			switch {
			case !tried:
				fresh = append(fresh, i)
				tries[i] = try{counts: true}
			case !now.Before(failure.retry):
				again = append(again, i)
				tries[i] = try{last: failure.tried, counts: true}
			case isRecent:
				// The article may not have been there yet. Trying again is
				// the purpose of the first hour, so it adds no failure.
				again = append(again, i)
				tries[i] = try{last: failure.tried}
			default:
				waiting++
			}
		case isRecent:
			recent = append(recent, i)
		}
	}
	sort.SliceStable(again, func(a, b int) bool { return tries[again[a]].last.Before(tries[again[b]].last) })
	if waiting > 0 {
		// Fixed text and a count, ahead of the warnings for single articles,
		// which a run keeps only so many of. The run that tried a story says
		// why it failed.
		run.warnings = append(run.warnings, fmt.Sprintf("Articles waiting to be tried again after failing or coming back empty: %d", waiting))
	}
	// Stories waiting out a backoff are not queued, so they are not a backlog
	// the next refresh would fetch either.
	missing := append(fresh, again...)
	targets := append(missing[:len(missing):len(missing)], recent...)
	if len(targets) > limit {
		targets = targets[:limit]
	}
	if len(targets) == 0 {
		return run
	}

	workers := articleWorkers
	if articlesUseSolver(f) || articlesUseChromium(f) && s.chromiumSlots() > 0 {
		// A FlareSolverr job is admitted for one solver slot (see solverFree),
		// and a Chromium job for one pool slot (see chromium). Fetching its
		// articles in parallel would take the slots of jobs admitted after it,
		// which then wait inside their own deadlines.
		workers = 1
	}
	var mu sync.Mutex
	queue := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				// A story handed over after the deadline was not tried.
				live := ctx.Err() == nil
				body, err := s.article(ctx, f, items[i].URL)
				mu.Lock()
				if err != nil {
					// The item keeps its list-page content; the operator sees why.
					run.warnings = append(run.warnings, fmt.Sprintf("Article %q: %s", items[i].Title, err))
				} else {
					items[i].FullHTML = body
					if body != "" {
						run.fetched = append(run.fetched, items[i].Key)
					}
				}
				if queued, ok := tries[i]; ok && live {
					outcome := articleDeferred
					switch {
					case err == nil && body != "":
						outcome = articleStored
					case queued.counts && ownFault(body, err):
						outcome = articleFault
					}
					run.tried = append(run.tried, articleTry{key: keyOf(items[i].Key), outcome: outcome})
				}
				mu.Unlock()
			}
		}()
	}
	queued := 0
	for _, i := range targets {
		// A cancelled or timed-out refresh stops queueing rather than racing the
		// deadline. Whatever was not fetched stays empty and is retried later.
		if ctx.Err() != nil {
			break
		}
		queue <- i
		// The send waits for a free worker and can outlast the deadline. A
		// story handed over after it is never fetched, so it counts as left.
		if ctx.Err() != nil {
			break
		}
		queued++
	}
	close(queue)
	wg.Wait()
	// Stories without a body lead the queue, so those it did not reach are
	// the backlog a later refresh has to fetch.
	for _, i := range missing[min(queued, len(missing)):] {
		run.left = append(run.left, items[i].Key)
	}
	return run
}

// articlesUseSolver reports whether a feed fetches its article pages through
// FlareSolverr. Otherwise a FlareSolverr job is done with the solver once its
// list page is solved, and gives its admission back before any article.
func articlesUseSolver(f model.Feed) bool {
	return f.Recipe.Mode == "flaresolverr" && f.Recipe.Full != nil && f.Recipe.Full.Selector != "" && f.Recipe.Full.Browser
}

// articlesUseChromium reports whether a feed renders its article pages in the
// local Chromium pool: in every fetch mode but FlareSolverr, Static HTTP
// included, once the option is on (see article).
func articlesUseChromium(f model.Feed) bool {
	return f.Recipe.Mode != "flaresolverr" && f.Recipe.Full != nil && f.Recipe.Full.Selector != "" && f.Recipe.Full.Browser
}

// article fetches and extracts one item page. It uses the plain guarded fetcher
// unless the recipe opted this feed into its configured browser mode: article
// pages are numerous, and the Chromium pool is a small shared resource.
func (s *Scheduler) article(ctx context.Context, f model.Feed, rawURL string) (string, error) {
	var r fetch.Result
	var err error
	switch {
	case !f.Recipe.Full.Browser:
		r, err = s.Fetcher.Fetch(ctx, rawURL, "", "")
	case f.Recipe.Mode == "flaresolverr":
		r, err = s.solve(ctx, rawURL)
	case s.Renderer == nil:
		return "", fmt.Errorf("browser rendering is unavailable for article pages")
	default:
		r, err = s.Renderer.Render(ctx, rawURL, "", f.Recipe.SettleMS)
	}
	if err != nil {
		return "", err
	}
	// From here on a failure is the page's own; see ownFault.
	if r.Status == 304 {
		return "", pageFault{fmt.Errorf("source returned 304 for an article page")}
	}
	body, err := extract.Article(r.Body, r.URL, f.Recipe)
	if err != nil {
		return "", pageFault{err}
	}
	return body, nil
}
