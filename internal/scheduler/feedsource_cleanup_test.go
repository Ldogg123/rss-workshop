package scheduler

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/model"
)

// A feed source's cleanup rules clean its entries' descriptions and its
// fetched articles alike, and a preview shows exactly what the refresh
// stores, warning included.
func TestFeedSourceCleanupMatchesInPreviewAndRefresh(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	list := `<rss><channel>
<item><title>One</title><link>https://example.com/a1</link><guid isPermaLink="false">1</guid><description><![CDATA[<p>Teaser one</p><div class="share">Share one</div>]]></description></item>
<item><title>Two</title><link>https://example.com/a2</link><guid isPermaLink="false">2</guid><description><![CDATA[<div class="share">Share two</div>]]></description></item>
</channel></rss>`
	jobs := New(s, newPageFetcher(list, map[string]string{
		"https://example.com/a1": `<main class="body"><p>Full one</p><div class="share">Share</div></main>`,
		"https://example.com/a2": `<main class="body"><p>Full two</p><aside class="share">Share</aside></main>`,
	}), 2, 10*time.Second)
	f := saveFeed(t, jobs, model.Recipe{Mode: "static", Type: extract.FeedType, Full: &model.FullContent{Selector: ".body"}, Remove: []string{".share"}})

	want := map[string][2]string{
		"One": {"<p>Teaser one</p>", "<p>Full one</p>"},
		"Two": {"", "<p>Full two</p>"},
	}
	warning := "Entry 2: cleanup removed the whole description; content omitted"
	check := func(where string, items []model.Item) {
		t.Helper()
		if len(items) != len(want) {
			t.Fatalf("%s: %+v", where, items)
		}
		for _, it := range items {
			if got := [2]string{it.HTML, it.FullHTML}; got != want[it.Title] {
				t.Errorf("%s: %s has description %q and article %q; want %q", where, it.Title, got[0], got[1], want[it.Title])
			}
		}
	}

	preview, err := jobs.Preview(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	check("preview", preview.Items)
	if !slices.Contains(preview.Warnings, warning) {
		t.Errorf("preview warnings: %q", preview.Warnings)
	}

	jobs.refresh(ctx, f)
	f = reload(t, s, f.ID)
	if f.Failures != 0 || f.Error != "" {
		t.Fatalf("the refresh failed: %q", f.Error)
	}
	stored, err := s.Items(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	check("refresh", stored)
	for _, it := range stored {
		if strings.Contains(it.HTML+it.FullHTML, "Share") {
			t.Errorf("a rule did not apply to %s: %+v", it.Title, it)
		}
	}
	runs, err := s.Runs(ctx, f.ID)
	if err != nil || len(runs) != 1 || runs[0].Diagnostics == nil || len(runs[0].Diagnostics.Attempts) != 1 || !slices.Contains(runs[0].Diagnostics.Attempts[0].Warnings, warning) {
		t.Fatalf("diagnostics: %+v, %v", runs, err)
	}
}

// A well-formed feed with no entries is a successful refresh that stores no
// story, so none of the saved stories is listed and no later refresh would
// fetch their articles again. A change to the cleanup rules then keeps every
// article, which stays published.
func TestFeedSourceEmptyFeedKeepsItsArticlesThroughACleanupEdit(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(`<rss><channel>
<item><title>One</title><link>https://example.com/a1</link><guid isPermaLink="false">1</guid><description>Teaser one</description></item>
<item><title>Two</title><link>https://example.com/a2</link><guid isPermaLink="false">2</guid><description>Teaser two</description></item>
</channel></rss>`, map[string]string{
		"https://example.com/a1": `<main class="body"><p>Full one</p></main>`,
		"https://example.com/a2": `<main class="body"><p>Full two</p></main>`,
	})
	jobs := New(s, p, 2, 10*time.Second)
	f := saveFeed(t, jobs, model.Recipe{Mode: "static", Type: extract.FeedType, Full: &model.FullContent{Selector: ".body"}})
	want := map[string]string{"One": "<p>Full one</p>", "Two": "<p>Full two</p>"}
	check := func(when string) {
		t.Helper()
		stored, err := s.Items(ctx, f.ID)
		if err != nil || len(stored) != len(want) {
			t.Fatalf("%s: stories %+v, %v", when, stored, err)
		}
		for _, it := range stored {
			if it.FullHTML != want[it.Title] {
				t.Errorf("%s: %s has article %q, want %q", when, it.Title, it.FullHTML, want[it.Title])
			}
		}
	}
	jobs.refresh(ctx, f)
	check("first refresh")

	p.list = `<rss><channel><title>Nothing yet</title></channel></rss>`
	jobs.refresh(ctx, reload(t, s, f.ID))
	f = reload(t, s, f.ID)
	if f.Failures != 0 || f.Error != "" {
		t.Fatalf("the empty feed failed: %d failures, %q", f.Failures, f.Error)
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
	for _, u := range []string{"https://example.com/a1", "https://example.com/a2"} {
		if n := p.count(u); n != 1 {
			t.Errorf("%s requested %d times, want once", u, n)
		}
	}
}
