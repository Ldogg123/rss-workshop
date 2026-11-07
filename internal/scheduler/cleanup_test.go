package scheduler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/model"
	"rss-workshop/internal/notify"
)

// An article that cleanup leaves with nothing to show is a failed article: the
// refresh succeeds, the story keeps its list-page description and no body, and
// the operator sees why, in diagnostics and in the preview, which shows the
// other articles cleaned.
func TestArticleEmptiedByCleanupKeepsTheTeaser(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(listPage(2), map[string]string{
		"https://example.com/a1": `<div class="body"><div class="promo">Sponsored by ` + sourceSecret + `</div></div>`,
		"https://example.com/a2": `<div class="body"><p>Full article 2.</p><div class="promo">Sponsored</div></div>`,
	})
	jobs := New(s, p, 2, 10*time.Second)
	r := fullRecipe()
	r.Remove = []string{".promo"}
	f := saveFeed(t, jobs, r)

	preview, err := jobs.Preview(ctx, f)
	if err != nil || len(preview.Items) != 2 {
		t.Fatalf("preview: %+v, %v", preview.Items, err)
	}
	if preview.Items[0].FullHTML != "" || preview.Items[1].FullHTML != "<div><p>Full article 2.</p></div>" {
		t.Errorf("preview bodies %q and %q", preview.Items[0].FullHTML, preview.Items[1].FullHTML)
	}
	want := `Article "Story 1": article body is empty after cleanup`
	if !slices.Equal(preview.Warnings, []string{want}) {
		t.Errorf("preview warnings: %q", preview.Warnings)
	}

	jobs.refresh(ctx, f)
	f, err = s.Get(ctx, f.ID)
	if err != nil || f.Failures != 0 || f.Error != "" || f.LastSuccess.IsZero() {
		t.Fatalf("the refresh failed: %d failures, %q, %v", f.Failures, f.Error, err)
	}
	stories, err := s.Items(ctx, f.ID)
	if err != nil || len(stories) != 2 {
		t.Fatalf("stories: %+v, %v", stories, err)
	}
	for _, story := range stories {
		switch story.Title {
		case "Story 1":
			if story.FullHTML != "" || story.HTML != "<p>teaser 1</p>" {
				t.Errorf("emptied article: body %q, description %q", story.FullHTML, story.HTML)
			}
		case "Story 2":
			if story.FullHTML != "<div><p>Full article 2.</p></div>" {
				t.Errorf("cleaned article: %q", story.FullHTML)
			}
		}
	}
	runs, err := s.Runs(ctx, f.ID)
	if err != nil || len(runs) != 1 || runs[0].Diagnostics == nil || len(runs[0].Diagnostics.Attempts) != 1 {
		t.Fatalf("runs: %+v, %v", runs, err)
	}
	if warnings := runs[0].Diagnostics.Attempts[0].Warnings; !slices.Contains(warnings, want) {
		t.Errorf("diagnostics warnings: %q", warnings)
	}
}

// A cleanup rule that panics fails the refresh like any other selector, and
// the error that reaches the card, the notification and the log names the
// rule by its number, never by its text or by the page's.
func TestCleanupRulePanicNamesOnlyTheRuleNumber(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	page := `<article><h2>Story</h2><a href="https://example.com/s">x</a><div class="summary"><p data-score="` + sourceSecret + `">Text</p></div></article>`
	jobs := New(s, newPageFetcher(page, nil), 1, 10*time.Second)
	h, _ := alerting(jobs, 1)
	r := panicRecipeFor("//article")
	r.Content.Selector = ".//div[@class='summary']"
	r.Remove = []string{".//aside", `.//p[sum('x') > 0][contains(@id,'https://selector.example/')]`}
	f := saveFeed(t, jobs, r)

	log := captureLog(t)
	jobs.refresh(ctx, f)
	f = reload(t, s, f.ID)
	if f.Failures != 1 || !strings.Contains(f.Error, extract.ErrSelectorFailed.Error()+" on the cleanup rule 2 selector") {
		t.Fatalf("failures %d, error %q", f.Failures, f.Error)
	}
	events := h.take()
	if len(events) != 1 || events[0].Kind != notify.Failing || events[0].Error != f.Error {
		t.Fatalf("events: %+v", events)
	}
	for _, text := range []string{f.Error, events[0].Error, log.String()} {
		for _, leaked := range []string{"source.example", "token=secret", "selector.example", "data-score"} {
			if strings.Contains(text, leaked) {
				t.Errorf("%q leaked into %s", leaked, text)
			}
		}
	}
	if _, err := jobs.Preview(ctx, f); !errors.Is(err, extract.ErrSelectorFailed) || !strings.Contains(err.Error(), "cleanup rule 2") {
		t.Errorf("preview: %v", err)
	}
}

// Articles are fetched only for the stories a refresh stores. After a refresh
// whose stories the filters all left out, no stored story is listed, so a
// cleanup edit keeps every article: discarded, they would never be fetched
// again, and the stories would lose them for good.
func TestCleanupEditAfterAnAllFilteredRefreshKeepsTheArticles(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(listPage(2), articlePages(2))
	jobs := New(s, p, 2, 10*time.Second)
	r := fullRecipe()
	r.Filters = &model.FilterSet{Include: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"story"}}}
	f := saveFeed(t, jobs, r)
	jobs.refresh(ctx, f)
	want := map[string]string{"Story 1": "<div><p>Full article 1.</p></div>", "Story 2": "<div><p>Full article 2.</p></div>"}
	check := func(when string) {
		t.Helper()
		stories, err := s.Items(ctx, f.ID)
		if err != nil || len(stories) != len(want) {
			t.Fatalf("%s: stories %+v, %v", when, stories, err)
		}
		for _, story := range stories {
			if story.FullHTML != want[story.Title] {
				t.Errorf("%s: %s body %q, want %q", when, story.Title, story.FullHTML, want[story.Title])
			}
		}
	}
	check("first refresh")

	p.list = `<article><h2>Other 3</h2><a href="https://example.com/a3">l</a><p>teaser 3</p></article>`
	jobs.refresh(ctx, reload(t, s, f.ID))
	f = reload(t, s, f.ID)
	if f.Failures != 0 || f.Error != "" {
		t.Fatalf("the all-filtered refresh failed: %d failures, %q", f.Failures, f.Error)
	}
	before := f.LastChanged
	f.Recipe.Remove = []string{".share"}
	if _, err := s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	if f = reload(t, s, f.ID); !f.LastChanged.Equal(before) {
		t.Errorf("the edit moved the feed's change time %v -> %v", before, f.LastChanged)
	}
	for range 3 {
		jobs.refresh(ctx, reload(t, s, f.ID))
	}
	check("after the edit")
	// Only the first refresh fetched articles; the story left out has none.
	for u, want := range map[string]int{"https://example.com/a1": 1, "https://example.com/a2": 1, "https://example.com/a3": 0} {
		if n := p.count(u); n != want {
			t.Errorf("%s requested %d times, want %d", u, n, want)
		}
	}
}
