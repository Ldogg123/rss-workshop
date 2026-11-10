package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
)

// A feed stored with CSS left in its XPath selectors keeps refreshing: the
// run succeeds, without a failure or a backoff, and stores what it always
// stored, the whole card and the whole article page. Diagnostics now says
// why, in a warning that names each selector and quotes neither.
func TestRefreshWithAnUnfinishedXPathSelectorSucceedsAndWarns(t *testing.T) {
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	ctx := context.Background()
	f := model.Feed{Title: "Leftover", URL: "https://example.com/news", Interval: 60, Enabled: true,
		Recipe: model.Recipe{Mode: "static", Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"}, Link: model.Field{Selector: ".//a/@href"},
			Content: model.Field{Selector: ".teaser-text"}, Full: &model.FullContent{Selector: ".entry-body"}}}
	if f.ID, err = s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	if f, err = s.Get(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	fetcher := &staticFetcher{body: []byte(`<html><body><nav>Site navigation</nav><article><h2>Headline</h2><a href="/one">more</a>` +
		`<p class="teaser-text">Summary</p><div>Share this</div></article><div class="entry-body"><p>Article text</p></div></body></html>`)}
	jobs := New(s, fetcher, 1, time.Second)
	jobs.refresh(ctx, f)

	if f, err = s.Get(ctx, f.ID); err != nil || f.Error != "" || f.Failures != 0 || f.LastSuccess.IsZero() {
		t.Fatalf("the refresh did not succeed: error %q, failures %d, %v", f.Error, f.Failures, err)
	}
	items, err := s.Items(ctx, f.ID)
	if err != nil || len(items) != 1 || !strings.Contains(items[0].HTML, "Share this") || !strings.Contains(items[0].FullHTML, "Site navigation") {
		t.Fatalf("stored %+v, %v", items, err)
	}
	runs, err := s.Runs(ctx, f.ID)
	if err != nil || len(runs) != 1 || runs[0].Error != "" || runs[0].Diagnostics == nil || len(runs[0].Diagnostics.Attempts) != 1 {
		t.Fatalf("runs %+v, %v", runs, err)
	}
	warnings := runs[0].Diagnostics.Attempts[0].Warnings
	if len(warnings) != 2 || !strings.HasPrefix(warnings[0], "Description selector: XPath uses only its first complete expression") ||
		!strings.HasPrefix(warnings[1], "Article body selector: XPath uses only its first complete expression") ||
		!strings.HasSuffix(warnings[1], "use Convert to XPath in the visual selector.") {
		t.Fatalf("stored warnings %q", warnings)
	}
	for _, warning := range warnings {
		if strings.Contains(warning, "teaser") || strings.Contains(warning, "entry-body") {
			t.Fatalf("the stored warning quotes the selector: %s", warning)
		}
	}
}

// unchangedSource answers 304 to a request that sends its ETag back.
type unchangedSource struct{ body []byte }

func (u *unchangedSource) Fetch(ctx context.Context, address, etag, modified string) (fetch.Result, error) {
	if etag == "leftover-v1" {
		return fetch.Result{URL: address, Status: 304, ETag: etag}, nil
	}
	return fetch.Result{Body: u.body, URL: address, Status: 200, ETag: "leftover-v1"}, nil
}

// The warning comes from extraction, and a refresh the source answers with
// 304 has no page to extract. Its run records none, which is why the guide
// sends an operator to Preview items, not only to Diagnostics.
func TestRefreshAnswered304RecordsNoSelectorWarning(t *testing.T) {
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	ctx := context.Background()
	f := model.Feed{Title: "Leftover", URL: "https://example.com/news", Interval: 60, Enabled: true,
		Recipe: model.Recipe{Mode: "static", Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"}, Link: model.Field{Selector: ".//a/@href"},
			Content: model.Field{Selector: ".teaser-text"}}}
	if f.ID, err = s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	jobs := New(s, &unchangedSource{body: []byte(`<article><h2>Headline</h2><a href="/one">more</a><p class="teaser-text">Summary</p></article>`)}, 1, time.Second)
	for range 2 {
		if f, err = s.Get(ctx, f.ID); err != nil {
			t.Fatal(err)
		}
		jobs.refresh(ctx, f)
	}
	if f, err = s.Get(ctx, f.ID); err != nil || f.Error != "" || f.Failures != 0 {
		t.Fatalf("error %q, failures %d, %v", f.Error, f.Failures, err)
	}
	runs, err := s.Runs(ctx, f.ID)
	if err != nil || len(runs) != 2 || runs[0].Diagnostics == nil || runs[1].Diagnostics == nil ||
		len(runs[0].Diagnostics.Attempts) != 1 || len(runs[1].Diagnostics.Attempts) != 1 {
		t.Fatalf("runs %+v, %v", runs, err)
	}
	// Newest first.
	unchanged, read := runs[0].Diagnostics.Attempts[0], runs[1].Diagnostics.Attempts[0]
	if read.Status != 200 || len(read.Warnings) != 1 || !strings.HasPrefix(read.Warnings[0], "Description selector: XPath uses only its first complete expression") {
		t.Fatalf("the refresh that read the page stored %+v", read)
	}
	if unchanged.Status != 304 || unchanged.Outcome != "not_modified" || len(unchanged.Warnings) != 0 {
		t.Fatalf("the refresh answered 304 stored %+v", unchanged)
	}
}
