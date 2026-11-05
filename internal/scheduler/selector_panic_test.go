package scheduler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
)

// These selectors panic inside antchfx/xpath v1.3.6 on the pages below. Before
// the guard in extract, each test here killed the whole test binary, as the same
// panic killed the server: a refresh and each article worker run in bare
// goroutines, where nothing recovers. xpath v1.3.9 no longer panics on them;
// moving to it means switching these to a form that still does, such as
// //article[sum('x') > 0].
const (
	comparisonItems = `//article[@data-score > 1]`
	substringItems  = `//article[substring(.//a/@href,2,5)='a']`

	sourceSecret = "https://source.example/private?token=secret"
)

func panicRecipeFor(items string) model.Recipe {
	return model.Recipe{Mode: "static", Type: "xpath", Items: items, Title: model.Field{Selector: ".//h2"}, Link: model.Field{Selector: ".//a/@href"}}
}

func panicPage(score string) string {
	return `<article data-score="` + score + `"><h2>Story</h2><a href="https://example.com/s">x</a></article>`
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var log bytes.Buffer
	restore := slog.Default()
	t.Cleanup(func() { slog.SetDefault(restore) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return &log
}

// versionedPage gives each page its own validator, so a test can see which one
// the store kept.
type versionedPage struct {
	body, etag, sent string
}

func (p *versionedPage) Fetch(ctx context.Context, u, e, m string) (fetch.Result, error) {
	p.sent = e
	return fetch.Result{Body: []byte(p.body), URL: u, Status: 200, ETag: p.etag}, nil
}

func TestSelectorPanicFollowsFailureRulesAndKeepsStories(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	page := &versionedPage{body: panicPage("2"), etag: "good"}
	jobs := New(s, page, 2, 10*time.Second)
	f := saveFeed(t, jobs, panicRecipeFor(comparisonItems))
	jobs.refresh(ctx, f)
	before, err := s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	stories, err := s.Items(ctx, f.ID)
	if err != nil || len(stories) != 1 || before.Failures != 0 {
		t.Fatalf("first refresh: %d stories, %d failures, %v", len(stories), before.Failures, err)
	}

	log := captureLog(t)
	page.body, page.etag = panicPage(sourceSecret), "bad"
	jobs.refresh(ctx, before)

	after, err := s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Failures != 1 || !strings.Contains(after.Error, extract.ErrSelectorFailed.Error()) {
		t.Fatalf("failures %d, error %q", after.Failures, after.Error)
	}
	// A success would run again within the interval and its 30 s jitter; the
	// first failure waits at least two minutes.
	if wait := time.Until(after.NextRun); wait < 110*time.Second {
		t.Errorf("next run in %s, want the failure backoff", wait)
	}
	if !after.LastSuccess.Equal(before.LastSuccess) || !after.LastChanged.Equal(before.LastChanged) {
		t.Error("a failed refresh moved last_success or last_changed")
	}
	kept, err := s.Items(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(kept, stories) {
		t.Errorf("saved stories changed: %+v, want %+v (%v)", kept, stories, err)
	}
	runs, err := s.Runs(ctx, f.ID)
	if err != nil || len(runs) != 2 || runs[0].Error == "" || runs[0].Count != 0 {
		t.Errorf("run history: %+v, %v", runs, err)
	}
	out := log.String()
	if !strings.Contains(out, `msg="refresh failed"`) || strings.Contains(out, `msg="refresh finished"`) {
		t.Errorf("a failed refresh was not reported as one:\n%s", out)
	}
	if strings.Contains(out, "source.example") {
		t.Errorf("the log contains a source URL:\n%s", out)
	}

	// The failed page's validator was not kept. If it were, the source would
	// answer 304 next time and hide the failure as an unchanged success.
	jobs.refresh(ctx, after)
	if page.sent != "good" {
		t.Errorf("next request sent validator %q, want the last successful one", page.sent)
	}
	again, err := s.Get(ctx, f.ID)
	if err != nil || again.Failures != 2 {
		t.Fatalf("second failure: %d failures, %v", again.Failures, err)
	}

	page.body, page.etag = panicPage("3"), "fixed"
	jobs.refresh(ctx, again)
	recovered, err := s.Get(ctx, f.ID)
	if err != nil || recovered.Failures != 0 || recovered.Error != "" {
		t.Fatalf("recovery: %d failures, %q, %v", recovered.Failures, recovered.Error, err)
	}
}

func TestSelectorPanicInOneFeedLeavesOthersRunning(t *testing.T) {
	for _, tc := range []struct{ name, items, page string }{
		{"numeric comparison", comparisonItems, panicPage("n/a")},
		{"substring", substringItems, `<article><h2>Story</h2><a href="/a">x</a></article>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, err := openTestStore(t, 50)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			p := newPageFetcher(listPage(2), map[string]string{"https://example.com/bad": tc.page})
			jobs := New(s, p, 4, 10*time.Second)
			r := fullRecipe()
			r.Full = nil
			healthy := saveFeed(t, jobs, r)
			id, err := s.Save(ctx, model.Feed{Title: "Bad", URL: "https://example.com/bad", Interval: 60, Enabled: true, Recipe: panicRecipeFor(tc.items)})
			if err != nil {
				t.Fatal(err)
			}

			// tick starts each refresh the way the running server does.
			jobs.tick(ctx)
			jobs.Wait()

			if active, _ := jobs.Stats(); active != 0 {
				t.Errorf("%d refresh slots still held", active)
			}
			bad, err := s.Get(ctx, id)
			if err != nil || bad.Failures != 1 {
				t.Errorf("bad feed: %d failures, %v", bad.Failures, err)
			}
			stories, err := s.Items(ctx, healthy.ID)
			if err != nil || len(stories) != 2 {
				t.Errorf("healthy feed: %d stories, %v", len(stories), err)
			}
		})
	}
}

// Imports are paused, and evaluating a selector needs a page, so validation
// cannot catch this: the recipe fails when the operator resumes it.
func TestImportedSelectorPanicFailsWhenResumed(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	r := panicRecipeFor(comparisonItems)
	if err := extract.Validate(r); err != nil {
		t.Fatalf("import validation refused the recipe: %v", err)
	}
	ids, err := s.Import(ctx, []model.Feed{{Title: "Imported", URL: "https://example.com/list", Interval: 60, Recipe: r}})
	if err != nil {
		t.Fatal(err)
	}
	p := newPageFetcher(panicPage(sourceSecret), nil)
	jobs := New(s, p, 2, 10*time.Second)
	jobs.tick(ctx)
	jobs.Wait()
	if got := p.calls.Load(); got != 0 {
		t.Fatalf("a paused import fetched its source %d times", got)
	}

	f, err := s.Get(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	f.Enabled = true
	if _, err = s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	log := captureLog(t)
	jobs.tick(ctx)
	jobs.Wait()
	f, err = s.Get(ctx, ids[0])
	if err != nil || f.Failures != 1 || !strings.Contains(f.Error, extract.ErrSelectorFailed.Error()) {
		t.Fatalf("resumed import: %d failures, %q, %v", f.Failures, f.Error, err)
	}
	if strings.Contains(log.String(), "source.example") {
		t.Errorf("the log contains a source URL:\n%s", log.String())
	}
}

// Auto renders only when a good response has no usable stories. Rendering would
// not make this selector work, and takes a slot in the shared browser pool.
func TestSelectorPanicDoesNotStartAutoFallback(t *testing.T) {
	f := model.Feed{URL: "https://example.com", Recipe: panicRecipeFor(comparisonItems)}
	f.Recipe.Mode = "auto"
	render := &fakeRenderer{result: fetch.Result{Body: []byte(panicPage("2")), URL: f.URL, Status: 200}}
	jobs := New(nil, &staticFetcher{body: []byte(panicPage("n/a"))}, 1, time.Second)
	jobs.Renderer = render
	_, p, err := jobs.extract(context.Background(), f, false)
	if !errors.Is(err, extract.ErrSelectorFailed) || render.calls != 0 {
		t.Fatalf("error %v, %d renders", err, render.calls)
	}
	attempt := p.Diagnostics.Attempts[0]
	if len(p.Diagnostics.Attempts) != 1 || attempt.Stage != "extract" || attempt.Outcome != "failed" || *attempt.Items != 0 {
		t.Fatalf("trace: %+v", p.Diagnostics.Attempts)
	}

	// A rendered page goes through the same extraction.
	f.Recipe.Mode = "browser"
	render.result.Body = []byte(panicPage("n/a"))
	if _, _, err = jobs.extract(context.Background(), f, false); !errors.Is(err, extract.ErrSelectorFailed) {
		t.Fatalf("rendered page: %v", err)
	}
}

// Article pages follow the rule for any failed article: the story keeps its
// list-page description and the refresh still succeeds. This also runs on the
// article workers' own goroutines, away from the refresh that started them.
func TestSelectorPanicInArticleKeepsTheStory(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(listPage(2), map[string]string{
		"https://example.com/a1": `<main data-score="` + sourceSecret + `"><p>Never used</p></main>`,
		"https://example.com/a2": `<main data-score="2"><p>Full article</p></main>`,
	})
	jobs := New(s, p, 4, 10*time.Second)
	r := fullRecipe()
	r.Type, r.Items = "xpath", "//article"
	r.Title.Selector, r.Link.Selector, r.Content.Selector = ".//h2", ".//a/@href", ".//p"
	r.Full.Selector = `//main[@data-score > 1]`
	f := saveFeed(t, jobs, r)

	log := captureLog(t)
	jobs.refresh(ctx, f)

	f, err = s.Get(ctx, f.ID)
	if err != nil || f.Failures != 0 || f.Error != "" {
		t.Fatalf("a failed article failed the refresh: %d failures, %q, %v", f.Failures, f.Error, err)
	}
	stories, err := s.Items(ctx, f.ID)
	if err != nil || len(stories) != 2 {
		t.Fatalf("stories: %+v, %v", stories, err)
	}
	for _, story := range stories {
		switch story.Title {
		case "Story 1":
			if story.FullHTML != "" || !strings.Contains(story.HTML, "teaser 1") {
				t.Errorf("failed article: full %q, description %q", story.FullHTML, story.HTML)
			}
		case "Story 2":
			if !strings.Contains(story.FullHTML, "Full article") {
				t.Errorf("working article lost its body: %q", story.FullHTML)
			}
		}
	}
	runs, err := s.Runs(ctx, f.ID)
	if err != nil || len(runs) != 1 || runs[0].Diagnostics == nil || len(runs[0].Diagnostics.Attempts) != 1 {
		t.Fatalf("runs: %+v, %v", runs, err)
	}
	warned := false
	for _, w := range runs[0].Diagnostics.Attempts[0].Warnings {
		warned = warned || strings.Contains(w, extract.ErrSelectorFailed.Error())
	}
	if !warned {
		t.Errorf("the operator was not told: %+v", runs[0].Diagnostics.Attempts[0].Warnings)
	}
	if strings.Contains(log.String(), "source.example") {
		t.Errorf("the log contains a source URL:\n%s", log.String())
	}
}

func TestPreviewReportsSelectorPanics(t *testing.T) {
	ctx := context.Background()
	f := model.Feed{URL: "https://example.com/list", Recipe: panicRecipeFor(comparisonItems)}
	jobs := New(nil, newPageFetcher(panicPage(sourceSecret), nil), 1, time.Second)
	p, err := jobs.Preview(ctx, f)
	if !errors.Is(err, extract.ErrSelectorFailed) {
		t.Fatalf("items selector: %v", err)
	}
	if len(p.Items) != 0 || strings.Contains(err.Error(), "source.example") {
		t.Fatalf("items %+v, error %v", p.Items, err)
	}
	if active, _ := jobs.Stats(); active != 0 {
		t.Errorf("%d preview slots still held", active)
	}

	// The same article form used to kill the server from the editor, because a
	// preview's article workers are goroutines net/http cannot recover.
	f.Recipe = panicRecipeFor("//article")
	f.Recipe.Full = &model.FullContent{Selector: `//main[@data-score > 1]`}
	jobs = New(nil, newPageFetcher(`<article><h2>Story</h2><a href="https://example.com/a1">x</a></article>`,
		map[string]string{"https://example.com/a1": `<main data-score="` + sourceSecret + `">Body</main>`}), 1, time.Second)
	p, err = jobs.Preview(ctx, f)
	if err != nil || len(p.Items) != 1 || p.Items[0].FullHTML != "" {
		t.Fatalf("article selector: %+v, %v", p.Items, err)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], extract.ErrSelectorFailed.Error()) || strings.Contains(p.Warnings[0], "source.example") {
		t.Fatalf("warnings: %q", p.Warnings)
	}
}
