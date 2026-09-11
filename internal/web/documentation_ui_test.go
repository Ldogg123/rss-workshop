package web

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"rss-workshop/internal/auth"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
)

// Opt-in documentation capture uses only a disposable database, fictional
// stories, example.com URLs, and a local page fixture. No operator data loads.
func TestBrowserUIDocumentation(t *testing.T) {
	if os.Getenv("RSS_UI_DOCS") != "1" {
		t.Skip("opt in to isolated documentation screenshots")
	}
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	a, err := auth.New("documentation-fixture-only", "", false)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`<h1>City Journal</h1><p>Independent stories about a changing city.</p><main><article class="story"><h2>City adds electric buses to busiest routes</h2><p class="summary">The new fleet brings quieter journeys and cleaner air to three neighbourhoods.</p><a href="/electric-buses">Read the story</a> <time datetime="2026-09-08">September 8, 2026</time></article><article class="story"><h2>Community solar project opens to new members</h2><p class="summary">Residents can now share the benefits of locally generated renewable energy.</p><a href="/community-solar">Read the story</a> <time datetime="2026-09-07">September 7, 2026</time></article><article class="story"><h2>Weekend guide: exhibitions and live music</h2><p class="summary">Explore new artists, small venues, and free events across the city.</p><a href="/weekend-guide">Read the story</a> <time datetime="2026-09-06">September 6, 2026</time></article></main>`)
	jobs := scheduler.New(s, &fixtureFetcher{body: body}, 4, time.Second)
	recipe := model.Recipe{Mode: "static", Type: "css", Items: "article.story", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Content: model.Field{Selector: ".summary"}, Date: model.Field{Selector: "time"}}
	for i, title := range []string{"Climate & energy", "Open-source releases"} {
		f := model.Feed{Title: title, URL: []string{"https://example.com/city-journal", "https://example.com/project-updates"}[i], Recipe: recipe, Interval: 3600, Enabled: true}
		id, err := s.Save(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		f, err = s.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		// The first feed gets a short history, because it is the one the
		// diagnostics capture opens. Run timestamps come from the clock, so the
		// runs are spaced rather than written in a tight loop -- four rows all
		// stamped the same second would read as an artifact in the screenshot.
		items := []model.Item{{Key: "sample-a", Title: "Sample story", URL: f.URL + "/one"}, {Key: "sample-b", Title: "Another sample story", URL: f.URL + "/two"}}
		runs := 1
		if i == 0 {
			runs = 4
		}
		for run := 0; run < runs; run++ {
			if run > 0 {
				time.Sleep(1100 * time.Millisecond)
			}
			f, err = s.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Complete(context.Background(), f, items, "", "", 200, nil, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	app := &App{Store: s, Scheduler: jobs, Auth: a, Version: "0.2 preview"}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	app.BaseURL = server.URL
	alloc, stopAlloc := chromedp.NewExecAllocator(context.Background(), chromedp.ExecPath(os.Getenv("CHROMIUM_PATH")), chromedp.Headless, chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck, chromedp.Flag("no-sandbox", false), chromedp.Flag("disable-background-networking", true))
	defer stopAlloc()
	tab, stopTab := chromedp.NewContext(alloc)
	defer stopTab()
	// Six captures, including a 100-phrase filter fixture and a live selector
	// frame, so the budget covers the whole sequence rather than the first half.
	tab, cancel := context.WithTimeout(tab, 150*time.Second)
	defer cancel()
	must := func(actions ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(tab, actions...); err != nil {
			t.Fatal(err)
		}
	}
	must(chromedp.EmulateViewport(1440, 1100), chromedp.Navigate(server.URL), chromedp.WaitVisible("#login-form"),
		chromedp.SendKeys("#password", "documentation-fixture-only"), chromedp.Click("#login-form button"), chromedp.WaitVisible(".feed-card"),
		chromedp.Evaluate(`if(document.documentElement.dataset.theme!=='dark')document.querySelector('#theme-toggle').click()`, nil))
	captureTrial(t, tab, "docs-dashboard")
	must(chromedp.Evaluate(`document.querySelectorAll('.feed-card')[0].querySelector('button').click()`, nil),
		chromedp.Click("#visual-button"), chromedp.WaitVisible("#selector-frame"), chromedp.Poll(`document.querySelector('#selector-matches').dataset.items==='3'`, nil),
		chromedp.Click("#selector-convert"), chromedp.Poll(`document.querySelector('#visual-type').value==='xpath'&&document.querySelector('#selector-matches').dataset.items==='3'`, nil),
		chromedp.Focus("#visual-items"))
	captureTrial(t, tab, "docs-visual-selector")
	must(chromedp.Click("#selector-close"), chromedp.Evaluate(`document.querySelector('#story-filters').open=true`, nil),
		chromedp.Click("#filter-include .filter-add-condition"))
	terms := []string{"climate change", "renewable energy", "solar", "electric buses", "public transport", "clean air", "community energy", "heat pumps", "urban trees", "energy storage"}
	for _, subject := range []string{"solar", "wind", "battery", "electric vehicle", "public transit", "building", "water", "community", "clean energy"} {
		for _, topic := range []string{"research", "investment", "policy", "innovation", "projects", "funding", "planning", "efficiency", "infrastructure", "progress"} {
			terms = append(terms, subject+" "+topic)
		}
	}
	if len(terms) != 100 {
		t.Fatal("documentation fixture must contain 100 phrases")
	}
	must(chromedp.EmulateViewport(900, 1150), chromedp.SetValue("#filter-include textarea", strings.Join(terms, "\n")),
		chromedp.Evaluate(`document.querySelector('#filter-include textarea').dispatchEvent(new Event('input'));document.querySelector('#story-filters').scrollIntoView({block:'start'})`, nil),
		chromedp.Poll(`document.querySelector('#filter-count').textContent.startsWith('100 / 500')`, nil))
	captureTrial(t, tab, "docs-filters")
	var counts string
	must(chromedp.Click("#preview-button"), chromedp.Poll(`!document.querySelector('#preview-button').disabled&&!!document.querySelector('.filter-preview-counts')`, nil), chromedp.Text(".filter-preview-counts", &counts))
	if counts != "3 valid before filters · 2 included · 1 filtered out" {
		t.Fatalf("documentation filter fixture result: %s", counts)
	}

	// Full article content, the newest editor section, with the preview showing
	// what a reader would receive.
	must(chromedp.EmulateViewport(820, 760),
		chromedp.Evaluate(`document.querySelector('#story-filters').open=false;document.querySelector('#full-content').open=true`, nil),
		chromedp.SetValue(`[name="full_selector"]`, ".article-body"),
		chromedp.Evaluate(`document.querySelector('[name=full_selector]').dispatchEvent(new Event('input'));`+
			`document.querySelector('#full-content').scrollIntoView({block:'start'});window.scrollBy(0,-24)`, nil),
		chromedp.Poll(`document.querySelector('#full-summary').textContent==='On'`, nil),
		chromedp.Sleep(300*time.Millisecond))
	captureTrial(t, tab, "docs-full-content")

	// Per-feed diagnostics: the answer to "why did this feed stop working".
	must(chromedp.EmulateViewport(1100, 1000), chromedp.Click("#close-editor"),
		chromedp.Evaluate(`document.querySelector('.feed-diagnostics').click()`, nil),
		chromedp.WaitVisible("#diagnostics-runs"),
		chromedp.Poll(`document.querySelector('#diagnostics-status').textContent.length>0`, nil))
	captureTrial(t, tab, "docs-diagnostics")

	// The library in light mode, so the documentation shows both themes.
	must(chromedp.EmulateViewport(1440, 1000), chromedp.Click("#diagnostics-close"),
		chromedp.Evaluate(`if(document.documentElement.dataset.theme==='dark')document.querySelector('#theme-toggle').click()`, nil),
		chromedp.WaitVisible(".feed-card"))
	captureTrial(t, tab, "docs-dashboard-light")
}
