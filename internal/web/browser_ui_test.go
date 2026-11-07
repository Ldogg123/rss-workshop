package web

import (
	"context"
	"encoding/json"
	"fmt"
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
		// Collapsed under request settings. Previews and page loads send it,
		// and the export and import below carry it with the recipe.
		chromedp.Evaluate(`document.querySelector('#recipe-form [name="user_agent"]').value=' UI-Fixture/1 (+mailto:ops@example.net) '`, nil),
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
	if e = chromedp.Run(tab, chromedp.Click(`#recipe-form button[type="submit"]`), chromedp.WaitVisible(".feed-card"),
		clickCurrent("Edit on the first card", `[...document.querySelectorAll('.feed-card button')].find(b=>b.textContent==='Edit')`), chromedp.WaitVisible("#editor")); e != nil {
		t.Fatal(e)
	}
	var title string
	if e = chromedp.Run(tab, chromedp.Value(`#recipe-form input[name="title"]`, &title)); e != nil || title != "UI fixture" {
		t.Fatal("editing lost recipe", title, e)
	}
	var agent bool
	if e = chromedp.Run(tab, chromedp.Evaluate(`document.querySelector('#recipe-form [name="user_agent"]').value==='UI-Fixture/1 (+mailto:ops@example.net)'&&document.querySelector('#user-agent-summary').textContent==='Custom User-Agent'`, &agent)); e != nil || !agent {
		t.Fatal("saving and reopening lost the User-Agent", e)
	}
	if card == "article.card" {
		assertFlareSolverrEditMode(t, tab)
		testPortabilityUI(t, tab, s)
		testRunDiagnosticsUI(t, tab, s, jobs, f, solver)
		testFiltersUI(t, tab, s)
		testLibraryFiltersUI(t, tab, s)
		testFeedStateUI(t, tab, s, f)
		testDashboardUI(t, tab, s)
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

// clickCurrent clicks the element that a JavaScript expression returns, in
// the same evaluation that finds it. Scripts rebuild some parts of the page
// after requests the test does not wait for: every feed card on each load of
// the dashboard, including its ten-second poll, and the filter library's list
// and the editor's library filter picker each time the library reloads.
// chromedp.Click finds a node and then makes several more calls with its id,
// so a rebuild between them fails the click with "Could not find node with
// given id", or loses it without an error when it lands between the press and
// the release. Use this for anything inside those lists; chromedp.Click stays
// right for the page template's own elements, which no script replaces.
//
// Like a user, it waits until the element is visible, enabled and on top at
// its centre once scrolled into view, then focuses and clicks it. Each
// attempt either clicks or reports why it did not, so waiting never clicks
// twice, and the reason is in the error if the tab's deadline passes first.
func clickCurrent(name, element string) chromedp.Action {
	script := `(()=>{let el;try{el=(` + element + `);}catch(e){return 'not found: '+e.message}
 if(!(el instanceof HTMLElement))return 'not found';
 if(el.matches(':disabled'))return 'disabled';
 if(!el.checkVisibility())return 'not visible';
 el.scrollIntoView({block:'nearest',inline:'nearest'});
 const box=el.getBoundingClientRect();if(!box.width||!box.height)return 'not visible';
 const top=document.elementFromPoint(box.left+box.width/2,box.top+box.height/2);
 if(!top||!el.contains(top))return top?'covered by <'+top.localName+(top.id?' id="'+top.id+'"':'')+(top.getAttribute('class')?' class="'+top.getAttribute('class')+'"':'')+'>':'outside the viewport';
 el.focus({preventScroll:true});el.click();return '';
})()`
	return chromedp.ActionFunc(func(ctx context.Context) error {
		last := ""
		for {
			var reason string
			if err := chromedp.Evaluate(script, &reason).Do(ctx); err != nil {
				// The deadline can also pass during an attempt's round trip.
				if last != "" && ctx.Err() != nil {
					return fmt.Errorf("click %s: %s: %w", name, last, err)
				}
				return fmt.Errorf("click %s: %w", name, err)
			}
			if reason == "" {
				return nil
			}
			last = reason
			select {
			case <-ctx.Done():
				return fmt.Errorf("click %s: %s: %w", name, reason, ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	})
}

// clickQuery is clickCurrent for the first element matching a CSS selector.
func clickQuery(selector string) chromedp.Action {
	quoted, _ := json.Marshal(selector)
	return clickCurrent(selector, `document.querySelector(`+string(quoted)+`)`)
}
