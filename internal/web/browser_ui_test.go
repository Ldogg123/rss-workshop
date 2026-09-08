package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"rss-workshop/internal/auth"
	"rss-workshop/internal/scheduler"
)

func TestBrowserUI(t *testing.T) {
	if os.Getenv("RSS_BROWSER_TEST") != "1" {
		t.Skip("run in the browser-tests Docker target")
	}
	for _, fixture := range []struct{ name, card, body string }{
		{"basic", "article.card", `<script>parent.document.body.dataset.attacked="yes"</script><iframe src="/snapshot-attack"></iframe><article class="card"><h2>First story</h2><a href="/one">Read</a><p>First description</p><img src="/snapshot-attack" alt="First thumbnail" onerror="parent.document.body.dataset.attacked=1"><time datetime="2026-09-01" data-age="2 minutes ago">September 1</time></article><article class="card"><h2>Second story</h2><a href="/two">Read</a><p>Second description</p><img src="/snapshot-attack" alt="Second thumbnail"><time datetime="2026-09-02" data-age="1 month ago">September 2</time></article>`},
		{"wrapped_variants", "div.card.lead", `<div class="card lead" data-testid="story-card"><a href="/one"><div class="heading lead"><h2 class="headline large">First story</h2></div><p>First description</p><img src="/snapshot-attack" alt="First thumbnail"><time datetime="2026-09-01" data-age="2 minutes ago">September 1</time></a></div><div class="card compact" data-testid="story-card"><a href="/two"><div class="heading compact"><h2 class="headline small">Second story</h2></div><p>Second description</p><img src="/snapshot-attack" alt="Second thumbnail"><time datetime="2026-09-02" data-age="1 month ago">September 2</time></a></div>`},
	} {
		t.Run(fixture.name, func(t *testing.T) { testBrowserFixture(t, fixture.body, fixture.card) })
	}
}

func testBrowserFixture(t *testing.T, body, card string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, e := openTestStore(t, 50)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	a, e := auth.New("ui-test-password-123", "", false)
	if e != nil {
		t.Fatal(e)
	}
	f := &fixtureFetcher{body: []byte(body)}
	jobs := scheduler.New(s, f, 2, time.Second)
	solver := &fixtureSolver{&fixtureFetcher{body: []byte(body)}}
	jobs.FlareSolverr = solver
	jobs.FlareSolverrTimeout = time.Second
	jobs.Start(ctx)
	defer func() { cancel(); jobs.Wait() }()
	server := httptest.NewUnstartedServer(nil)
	app := &App{Store: s, Scheduler: jobs, Auth: a}
	server.Config.Handler = app.Handler()
	server.Start()
	defer server.Close()
	app.BaseURL = server.URL
	// Reproduce a local tunnel: browser origin has a different port, and the
	// forwarder rewrites Host to the backend while preserving Fetch Metadata.
	backend, _ := url.Parse(server.URL)
	proxy := httputil.NewSingleHostReverseProxy(backend)
	director := proxy.Director
	var forwarded atomic.Bool
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = backend.Host
		if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != app.BaseURL && r.Header.Get("Sec-Fetch-Site") == "same-origin" {
			forwarded.Store(true)
		}
	}
	front := httptest.NewServer(proxy)
	defer front.Close()
	alloc, closeAlloc := chromedp.NewExecAllocator(context.Background(), chromedp.ExecPath(os.Getenv("CHROMIUM_PATH")), chromedp.Headless, chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck, chromedp.Flag("no-sandbox", false), chromedp.Flag("disable-background-networking", true))
	defer closeAlloc()
	tab, closeTab := chromedp.NewContext(alloc)
	defer closeTab()
	tab, timeout := context.WithTimeout(tab, 60*time.Second)
	defer timeout()
	var mu sync.Mutex
	exceptions := []string{}
	chromedp.ListenTarget(tab, func(ev any) {
		if e, ok := ev.(*runtime.EventExceptionThrown); ok {
			mu.Lock()
			exceptions = append(exceptions, e.ExceptionDetails.Text)
			mu.Unlock()
		}
	})
	err := chromedp.Run(tab,
		chromedp.EmulateViewport(1280, 900), chromedp.Navigate(front.URL), chromedp.WaitVisible("#login-form"),
		chromedp.Evaluate(`if(document.documentElement.dataset.theme!=='dark')document.querySelector('#theme-toggle').click()`, nil),
		chromedp.Reload(), chromedp.WaitVisible("#login-form"),
		chromedp.Poll(`document.documentElement.dataset.theme==='dark'&&localStorage.getItem('rss-theme')==='dark'`, nil),
		chromedp.SendKeys("#password", "ui-test-password-123"), chromedp.Click("#login-form button"), chromedp.WaitVisible("#workspace"),
		chromedp.Click("#new-feed"), chromedp.WaitVisible("#editor"),
		chromedp.SendKeys(`#recipe-form input[name="title"]`, "UI fixture"), chromedp.SendKeys(`#recipe-form input[name="url"]`, "https://example.com/news"),
		chromedp.SendKeys(`#recipe-form [name="items"]`, ".card"), chromedp.SendKeys(`#recipe-form [name="title_selector"]`, "h2"), chromedp.SendKeys(`#recipe-form [name="link_selector"]`, "a"),
		chromedp.Click("#preview-button"), chromedp.WaitVisible("#preview .preview-item"),
	)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if e = chromedp.Run(tab, chromedp.Evaluate(`document.querySelectorAll('#preview .preview-item').length`, &count)); e != nil || count != 2 {
		t.Fatal("UI preview mismatch", count, e)
	}
	if card == "article.card" {
		testPreviewDiagnostics(t, tab)
	}
	testVisualSelector(t, tab, card, f)
	if card == "article.card" {
		testEstimatedDatePreview(t, tab)
		testFlareSolverrSelector(t, tab, f, solver)
	}
	if e = chromedp.Run(tab, chromedp.Click(`#recipe-form button[type="submit"]`), chromedp.WaitVisible(".feed-card"), chromedp.Click(`//button[text()='Edit']`, chromedp.BySearch), chromedp.WaitVisible("#editor")); e != nil {
		t.Fatal(e)
	}
	var title string
	if e = chromedp.Run(tab, chromedp.Value(`#recipe-form input[name="title"]`, &title)); e != nil || title != "UI fixture" {
		t.Fatal("editing lost recipe", title, e)
	}
	if card == "article.card" {
		assertFlareSolverrEditMode(t, tab)
		testPortabilityUI(t, tab, s)
		testRunDiagnosticsUI(t, tab, s, jobs, f, solver)
		testFiltersUI(t, tab, s)
	}
	var overflow bool
	if e = chromedp.Run(tab, chromedp.EmulateViewport(390, 844), chromedp.Evaluate(`document.documentElement.scrollWidth>innerWidth+1`, &overflow)); e != nil || overflow {
		t.Fatal("mobile layout overflows", e)
	}
	captureTrial(t, tab, "UI-dark")
	if !forwarded.Load() {
		t.Fatal("test did not exercise a forwarded browser origin")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(exceptions) > 0 {
		t.Fatal("browser JavaScript errors", exceptions)
	}
}
