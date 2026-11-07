package scheduler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/extract"
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
