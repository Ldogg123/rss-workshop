package web

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"rss-workshop/internal/auth"
	"rss-workshop/internal/browser"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
	"rss-workshop/internal/store"
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
	// Library filters shared across feeds. The first feed uses one, so the
	// dashboard, the editor's picker and the library dialog all show it. None
	// matches a fixture story, so the 100-phrase preview counts are unchanged.
	sponsored, err := s.SaveFilter(context.Background(), model.LibraryFilter{Name: "No sponsored posts", Filters: model.FilterSet{Exclude: &model.FilterRule{Op: "any", Rules: []model.FilterRule{
		{Op: "contains_any", Field: "title", Keywords: []string{"sponsored", "advertisement", "partner content"}},
		{Op: "contains_any", Field: "link", Keywords: []string{"/sponsored/", "/partners/"}},
	}}}}, store.SaveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveFilter(context.Background(), model.LibraryFilter{Name: "Skip live blogs", Filters: model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"live updates", "as it happened"}}}}, store.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"Climate & energy", "Open-source releases"} {
		f := model.Feed{Title: title, URL: []string{"https://example.com/city-journal", "https://example.com/project-updates"}[i], Recipe: recipe, Interval: 3600, Enabled: true}
		if i == 0 {
			f.FilterIDs = []string{sponsored}
		}
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
		// The second feed shows a quiet one: still refreshing, but with no
		// new story for a few weeks.
		if i == 1 {
			for _, query := range []string{"UPDATE feeds SET last_changed=%[1]d,last_new=%[1]d WHERE id='%[2]s'", "UPDATE items SET first_seen=%[1]d,last_changed=%[1]d WHERE feed_id='%[2]s'"} {
				if _, err := s.DB.Exec(fmt.Sprintf(query, time.Now().Add(-23*24*time.Hour).Unix(), id)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// The default image runs Chromium, so the status line shows its slots as
	// that deployment does. A pool starts Chromium only for a render, and no
	// capture renders one: both feeds and the selector's page are Static HTTP.
	policy, err := fetch.New(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := browser.New(os.Getenv("CHROMIUM_PATH"), 2, 30*time.Second, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	app := &App{Store: s, Scheduler: jobs, Auth: a, Browsers: pool, Version: "0.2 preview"}
	// Notifications on, so the dashboard's status line shows the channel and
	// its test button. They go to a local receiver; no capture presses the
	// button, and the page never shows the address.
	withNotifier(t, app, newReceiver(t, 200).URL+"/rss-alerts")
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
		chromedp.Evaluate(`if(document.documentElement.dataset.theme!=='dark')document.querySelector('#theme-toggle').click()`, nil),
		chromedp.Poll(`document.querySelectorAll('.feed-card .badge')[1]?.textContent==='Quiet'&&document.querySelector('#queue').textContent.includes('Chromium 0/2 slots')&&document.querySelector('#notifications').textContent==='Notifications: JSON webhook'&&!document.querySelector('#test-notification').hidden`, nil))
	captureTrial(t, tab, "docs-dashboard")
	must(clickCurrent("Edit on the first card", `document.querySelectorAll('.feed-card')[0].querySelector('button')`),
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
	// Tall enough for the library filter picker above the feed's own rules,
	// down to the phrase counter and the history cleanup option.
	must(chromedp.EmulateViewport(900, 1560), chromedp.SetValue("#filter-include textarea", strings.Join(terms, "\n")),
		chromedp.Poll(`document.querySelector('#library-filter-choices input[type=checkbox]')?.checked`, nil),
		chromedp.Evaluate(`document.querySelector('#filter-include textarea').dispatchEvent(new Event('input'));document.querySelector('#story-filters').scrollIntoView({block:'start'})`, nil),
		chromedp.Poll(`document.querySelector('#filter-count').textContent.startsWith('100 / 500')&&document.querySelector('#filter-summary-count').textContent==='100 phrases · 1 library filter'`, nil))
	captureTrial(t, tab, "docs-filters")
	var counts string
	must(chromedp.Click("#preview-button"), chromedp.Poll(`!document.querySelector('#preview-button').disabled&&!!document.querySelector('.filter-preview-counts')`, nil), chromedp.Text(".filter-preview-counts", &counts))
	if counts != "3 valid before filters · 2 included · 1 filtered out" {
		t.Fatalf("documentation filter fixture result: %s", counts)
	}

	// Full article content, the newest editor section, with the preview showing
	// what a reader would receive. The recipe has been XPath since the visual
	// selector capture converted it, so the article selector is XPath too: CSS
	// left there is the mistake the hint under the field warns about.
	must(chromedp.EmulateViewport(820, 760),
		chromedp.Evaluate(`document.querySelector('#story-filters').open=false;document.querySelector('#full-content').open=true`, nil),
		chromedp.SetValue(`[name="full_selector"]`, "//div[@class='article-body']"),
		chromedp.Evaluate(`document.querySelector('[name=full_selector]').dispatchEvent(new Event('input'));`+
			`document.querySelector('#full-content').scrollIntoView({block:'start'});window.scrollBy(0,-24)`, nil),
		chromedp.Poll(`document.querySelector('#full-summary').textContent==='On'&&document.querySelector('#full-syntax-help').textContent.startsWith('An XPath article selector')`, nil),
		chromedp.Sleep(300*time.Millisecond))
	captureTrial(t, tab, "docs-full-content")

	// The filter library: saved filters and the feeds using each.
	must(chromedp.EmulateViewport(1100, 620), chromedp.Evaluate(`document.querySelector('#full-content').open=false`, nil),
		chromedp.Click("#manage-filters"),
		chromedp.Poll(`document.querySelectorAll('#filter-library-list .library-filter').length===2&&document.querySelector('#filter-library-list').textContent.includes('Used by Climate & energy')`, nil),
		chromedp.Sleep(300*time.Millisecond))
	captureTrial(t, tab, "docs-filter-library")
	must(chromedp.Click("#filter-library-close"))

	// Per-feed diagnostics: the answer to "why did this feed stop working".
	must(chromedp.EmulateViewport(1100, 1000), chromedp.Click("#close-editor"),
		clickQuery(".feed-diagnostics"),
		chromedp.WaitVisible("#diagnostics-runs"),
		chromedp.Poll(`document.querySelector('#diagnostics-status').textContent.length>0`, nil))
	captureTrial(t, tab, "docs-diagnostics")

	// The library in light mode, so the documentation shows both themes.
	must(chromedp.EmulateViewport(1440, 1000), chromedp.Click("#diagnostics-close"),
		chromedp.Evaluate(`if(document.documentElement.dataset.theme==='dark')document.querySelector('#theme-toggle').click()`, nil),
		chromedp.WaitVisible(".feed-card"))
	captureTrial(t, tab, "docs-dashboard-light")
}
