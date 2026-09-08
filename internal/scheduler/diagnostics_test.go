package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/diagnostics"
	"rss-workshop/internal/extract"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
)

func TestAutoDiagnosticsRetainEachAttempt(t *testing.T) {
	f := model.Feed{Version: 7, URL: "https://example.com", Recipe: model.Recipe{Mode: "auto", Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"}, Link: model.Field{Selector: ".//a", Attr: "href"}}}
	static := &staticFetcher{body: []byte(strings.Repeat(`<article><h2>Waiting for a link</h2></article>`, 25))}
	renderer := &fakeRenderer{result: fetch.Result{Status: 200, URL: f.URL, Body: []byte(`<article><h2>Ready</h2><a href="/story">Read</a></article>`)}}
	jobs := New(nil, static, 1, time.Second)
	jobs.Renderer = renderer
	p, err := jobs.Preview(context.Background(), f)
	if err != nil || len(p.Items) != 1 || p.Diagnostics == nil || len(p.Diagnostics.Attempts) != 2 || renderer.calls != 1 {
		t.Fatalf("fallback: %+v, %v", p, err)
	}
	d := p.Diagnostics
	if d.RequestedMode != "auto" || d.SelectorType != "xpath" || d.RecipeVersion != 7 || d.Started.IsZero() {
		t.Fatalf("metadata: %+v", d)
	}
	a, b := d.Attempts[0], d.Attempts[1]
	if a.Mode != "static" || a.Stage != "extract" || a.Outcome != "failed" || a.Status != 200 || *a.Matches != 25 || *a.Items != 0 || a.Bytes != len(static.body) || len(a.Warnings) != diagnostics.MaxWarnings || a.WarningsOmitted != 5 || !strings.Contains(a.Warnings[0], "missing or unsafe item URL") {
		t.Fatalf("static rejection lost: %+v", a)
	}
	if b.Mode != "browser" || b.Outcome != "success" || *b.Matches != 1 || *b.Items != 1 || len(b.Warnings) != 0 || len(p.Warnings) != 0 {
		t.Fatalf("browser success: %+v", b)
	}
	jobs.Renderer = nil
	p, err = jobs.Preview(context.Background(), f)
	if err == nil || len(p.Diagnostics.Attempts) != 2 || p.Diagnostics.Attempts[0].Matches == nil || p.Diagnostics.Attempts[1].Matches != nil || p.Diagnostics.Attempts[1].Stage != "fetch" {
		t.Fatalf("unavailable fallback lost first result: %+v, %v", p, err)
	}
}

func TestFetchFailuresAndNotModifiedDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  fetch.Result
		err     error
		outcome string
		status  int
	}{
		{"HTTP denied", fetch.Result{}, &fetch.HTTPError{Status: 403}, "failed", 403},
		{"transport", fetch.Result{}, errors.New("TLS request https://example.com/private?token=hidden failed"), "failed", 0},
		{"timeout", fetch.Result{}, context.DeadlineExceeded, "failed", 0},
		{"cancelled", fetch.Result{}, context.Canceled, "failed", 0},
		{"unchanged", fetch.Result{Status: 304}, nil, "not_modified", 304},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := model.Feed{URL: "https://example.com", Recipe: model.Recipe{Mode: "auto", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}}
			fetcher := &solverFetcher{fetch: func(context.Context, string, string, string) (fetch.Result, error) { return tc.result, tc.err }}
			jobs := New(nil, fetcher, 1, time.Second)
			renderer := &fakeRenderer{}
			jobs.Renderer = renderer
			r, p, err := jobs.extract(context.Background(), f, false)
			if (err == nil) != (tc.err == nil) || len(p.Diagnostics.Attempts) != 1 || renderer.calls != 0 || r.Status != tc.status {
				t.Fatalf("unexpected fallback/result: %+v, %v", p, err)
			}
			a := p.Diagnostics.Attempts[0]
			if a.Outcome != tc.outcome || a.Stage != "fetch" || a.Status != tc.status || a.Matches != nil || a.Items != nil || strings.Contains(a.Error, "hidden") {
				t.Fatalf("fetch-stage diagnostics: %+v", a)
			}
		})
	}
}

func TestRefreshPersistsDiagnosticsAndRetainsItems(t *testing.T) {
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	ctx := context.Background()
	f := model.Feed{Title: "Test", URL: "https://example.com", Interval: 60, Enabled: true, Recipe: model.Recipe{Mode: "static", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a", Attr: "href"}}}
	f.ID, err = s.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	f, err = s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &staticFetcher{body: []byte(`<article><h2>Saved</h2><a href="/story">Read</a></article>`)}
	jobs := New(s, fetcher, 1, time.Second)
	jobs.refresh(ctx, f)
	f, _ = s.Get(ctx, f.ID)
	fetcher.body = []byte(`<article><h2>Still waiting for the link</h2></article>`)
	jobs.refresh(ctx, f)
	runs, err := s.Runs(ctx, f.ID)
	if err != nil || len(runs) != 2 || runs[0].Diagnostics == nil || runs[1].Diagnostics == nil {
		t.Fatalf("refresh logs: %+v, %v", runs, err)
	}
	if !strings.Contains(runs[0].Error, extract.ErrNoItems.Error()) || *runs[0].Diagnostics.Attempts[0].Matches != 1 || *runs[0].Diagnostics.Attempts[0].Items != 0 || runs[1].Diagnostics.Attempts[0].Outcome != "success" {
		t.Fatalf("refresh details: %+v", runs)
	}
	items, err := s.Items(ctx, f.ID)
	if err != nil || len(items) != 1 || items[0].Title != "Saved" {
		t.Fatalf("failed refresh changed history: %+v, %v", items, err)
	}
	// Previewing an edited draft must not add a third run to the saved feed.
	if _, err := jobs.Preview(ctx, f); !errors.Is(err, extract.ErrNoItems) {
		t.Fatalf("draft: %v", err)
	}
	runs, err = s.Runs(ctx, f.ID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("draft persisted: %+v, %v", runs, err)
	}
}

func TestFlareSolverrDiagnosticsUseOnlySolver(t *testing.T) {
	f := solverFeed()
	jobs := New(nil, &staticFetcher{err: errors.New("unexpected static fetch")}, 1, time.Second)
	jobs.FlareSolverr = &solverFetcher{fetch: func(context.Context, string, string, string) (fetch.Result, error) {
		return fetch.Result{Status: 503}, &fetch.HTTPError{Status: 503}
	}}
	renderer := &fakeRenderer{}
	jobs.Renderer = renderer
	p, err := jobs.Preview(context.Background(), f)
	if err == nil || len(p.Diagnostics.Attempts) != 1 || p.Diagnostics.Attempts[0].Mode != "flaresolverr" || p.Diagnostics.Attempts[0].Status != 503 || renderer.calls != 0 {
		t.Fatalf("solver trace: %+v, %v", p, err)
	}
}

func TestInitialNotModifiedRecordsFailure(t *testing.T) {
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	ctx := context.Background()
	f := model.Feed{Title: "Uninitialized", URL: "https://example.com", Interval: 60, Enabled: true, Recipe: model.Recipe{Mode: "static", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}}
	f.ID, err = s.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	f, err = s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &solverFetcher{fetch: func(context.Context, string, string, string) (fetch.Result, error) {
		return fetch.Result{Status: 304}, nil
	}}
	New(s, fetcher, 1, time.Second).refresh(ctx, f)
	runs, err := s.Runs(ctx, f.ID)
	if err != nil || len(runs) != 1 || runs[0].Diagnostics == nil {
		t.Fatalf("initial 304: %+v, %v", runs, err)
	}
	a := runs[0].Diagnostics.Attempts[0]
	if a.Outcome != "failed" || a.Status != 304 || a.Error != runs[0].Error || !strings.Contains(a.Error, "before any successful refresh") {
		t.Fatalf("initial 304 claimed saved items were retained: %+v", a)
	}
}
