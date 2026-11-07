package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"rss-workshop/internal/extract"
	"rss-workshop/internal/store"
)

// feedSourceURL is the address the UI fixture's fetcher answers with
// feedSourceFixture rather than the page fixture.
const feedSourceURL = "https://example.com/feed.xml"

const feedSourceFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Fixture feed</title>
<item><title>First feed story</title><link>https://example.com/one</link><guid isPermaLink="false">one</guid><description>&lt;p&gt;First feed description&lt;/p&gt;</description><pubDate>Thu, 05 Nov 2026 10:00:00 GMT</pubDate></item>
<item><title>Second feed story</title><link>https://example.com/two</link><guid isPermaLink="false">two</guid><description>&lt;p&gt;Second feed description&lt;/p&gt;</description></item>
<item><title>Third feed story</title><link>https://example.com/three</link><guid isPermaLink="false">three</guid></item>
</channel></rss>`

// testFeedSourceUI covers the editor's Source choice: what a feed source
// hides and stops requiring, its preview and diagnostics, the lock on a saved
// feed and its absence on a copy, and the import review of a feed recipe.
func testFeedSourceUI(t *testing.T, tab context.Context, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	must := func(actions ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(tab, actions...); err != nil {
			t.Fatal(err)
		}
	}
	check := func(expression, failure string) {
		t.Helper()
		var ok bool
		must(chromedp.Evaluate(expression, &ok))
		if !ok {
			t.Fatal(failure)
		}
	}
	choose := func(control, value string) chromedp.Action {
		return chromedp.Evaluate(`(()=>{const c=form.elements['`+control+`'];c.value='`+value+`';c.dispatchEvent(new Event('change'))})()`, nil)
	}
	awaitLoad := chromedp.Evaluate(`load()`, nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) })
	before, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, f := range before {
		known[f.ID] = true
	}
	defer func() {
		after, _ := s.List(ctx)
		for _, f := range after {
			if !known[f.ID] {
				s.Delete(ctx, f.ID)
			}
		}
	}()

	must(chromedp.Evaluate(`if(!document.querySelector('#editor').hidden)document.querySelector('#close-editor').click()`, nil),
		chromedp.Click("#new-feed"), chromedp.WaitVisible("#editor"))
	check(`form.elements.source.value==='page'&&!form.elements.source.disabled&&!document.querySelector('#page-selectors').hidden&&form.elements.items.required&&form.elements.title_selector.required&&document.querySelector('#url-label').textContent==='Page URL'&&document.querySelector('#full-feed-hint').hidden`,
		"a new feed does not start as a web page")
	// Values typed for a web page stay in the hidden fields but are not sent.
	must(chromedp.SendKeys(`#recipe-form [name="items"]`, ".card"), choose("mode", "browser"))
	check(`!document.querySelector('#render-settings').hidden`, "Chromium's render settings are not shown for a web page")
	must(choose("source", "feed"))
	check(`document.querySelector('#page-selectors').hidden&&!document.querySelector('#visual-button').checkVisibility()&&form.elements.type.closest('label').hidden&&form.elements.mode.closest('label').hidden&&document.querySelector('#fetch-mode-help').hidden&&document.querySelector('#render-settings').hidden&&document.querySelector('#full-render').hidden&&!document.querySelector('#full-feed-hint').hidden`,
		"a feed source still shows selectors, the fetch mode or render settings")
	check(`!form.elements.items.required&&!form.elements.title_selector.required&&document.querySelector('#url-label').textContent==='Feed URL'&&document.querySelector('#source-help').textContent.startsWith('Reads the stories of an existing RSS or Atom feed')&&!document.querySelector('#source-help').textContent.includes('keeps its source')`,
		"a feed source still requires selectors, or its labels and help were not changed")
	check(`document.documentElement.dataset.theme==='dark'&&getComputedStyle(form.elements.source).backgroundColor==='rgb(17, 28, 45)'`, "the Source choice ignores the dark theme")
	must(chromedp.SendKeys(`#recipe-form input[name="title"]`, "Feed source fixture"), chromedp.SendKeys(`#recipe-form input[name="url"]`, feedSourceURL))
	check(`(()=>{const r=readForm().recipe;return r.type==='feed'&&r.mode==='static'&&r.items===''&&r.settle_ms===0&&r.wait_selector===''&&['title','link','content','image','date'].every(k=>r[k].selector===''&&r[k].attr==='')})()`,
		"the editor sends selectors or a fetch mode for a feed source")

	must(chromedp.Click("#preview-button"), chromedp.Poll(`document.querySelectorAll('#preview .preview-item').length===3`, nil))
	check(`document.querySelector('#preview h2').textContent==='3 items · 3 matches'&&document.querySelector('#preview .preview-item').textContent.includes('First feed description')`,
		"the preview does not show the feed's stories")
	check(`(()=>{const trace=document.querySelector('#preview .preview-diagnostics .run-trace')?.textContent||'';return trace.includes('SourceRSS or Atom feed')&&trace.includes('Feed entries3')})()`,
		"preview diagnostics do not describe a feed source")

	must(chromedp.Click(`#recipe-form button[type="submit"]`), chromedp.Poll(`document.querySelector('#editor').hidden&&feeds.some(f=>f.title==='Feed source fixture')`, nil))
	id := ""
	feeds, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range feeds {
		if f.Title == "Feed source fixture" {
			id = f.ID
			if f.Recipe.Type != extract.FeedType || f.Recipe.Mode != "static" || f.Recipe.Items != "" || f.URL != feedSourceURL {
				t.Fatalf("the saved feed source: %+v", f)
			}
		}
	}
	// It saved enabled, so the running scheduler refreshes it.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if f, err := s.Get(ctx, id); err == nil && !f.LastSuccess.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the feed source was not refreshed")
		}
	}
	card := `document.querySelector('.feed-card[data-feed-id="` + id + `"]')`
	button := func(label string) string {
		return `[...` + card + `.querySelectorAll('button')].find(b=>b.textContent==='` + label + `')`
	}
	must(awaitLoad, clickCurrent("Diagnostics on the feed source", button("Diagnostics")),
		chromedp.Poll(`document.querySelectorAll('#diagnostics-runs .run-entry').length===1`, nil))
	check(`(()=>{const run=document.querySelector('#diagnostics-runs .run-entry .run-trace')?.textContent||'';return run.includes('SourceRSS or Atom feed')&&run.includes('Feed entries3')})()`,
		"the run history shows no trace for a feed source")
	must(chromedp.Click("#diagnostics-close"))

	// A saved feed keeps its source; a copy may choose either.
	must(clickCurrent("Edit on the feed source", button("Edit")), chromedp.WaitVisible("#editor"))
	check(`form.elements.source.value==='feed'&&form.elements.source.disabled&&document.querySelector('#source-help').textContent.includes('keeps its source')&&document.querySelector('#page-selectors').hidden&&form.elements.type.value==='css'&&form.elements.url.value==='`+feedSourceURL+`'`,
		"editing a feed source does not lock its source")
	must(chromedp.Click("#close-editor"), clickCurrent("Duplicate on the feed source", button("Duplicate")), chromedp.WaitVisible("#editor"))
	check(`document.querySelector('#editor-title').textContent==='Duplicate feed'&&form.elements.source.value==='feed'&&!form.elements.source.disabled&&document.querySelector('#page-selectors').hidden&&!document.querySelector('#source-help').textContent.includes('keeps its source')`,
		"Duplicate does not keep the feed source, or locks the copy's source")
	must(choose("source", "page"))
	check(`!document.querySelector('#page-selectors').hidden&&form.elements.items.required&&document.querySelector('#url-label').textContent==='Page URL'`,
		"a copy switched back to a web page does not require its selectors")
	must(chromedp.Click("#close-editor"))

	// The import review names a feed source rather than empty selectors.
	path := filepath.Join(t.TempDir(), "feed-source.json")
	recipe := `{"format":"rss-workshop.recipes","version":1,"feeds":[{"title":"Imported feed source","url":"` + feedSourceURL + `","interval":900,"recipe":{"mode":"static","type":"feed"}}]}`
	if err := os.WriteFile(path, []byte(recipe), 0600); err != nil {
		t.Fatal(err)
	}
	must(chromedp.Click("#import-recipes"), chromedp.WaitVisible("#recipe-import"), chromedp.SetUploadFiles("#import-file", []string{path}),
		chromedp.Poll(`document.querySelectorAll('#import-preview .import-recipe').length===1&&!document.querySelector('#import-confirm').disabled`, nil))
	check(`[...document.querySelectorAll('#import-preview .import-recipe p.meta')].some(p=>p.textContent==='Source: RSS or Atom feed')&&!document.querySelector('#import-preview details')`,
		"the import review does not name a feed source")
	must(chromedp.Click("#import-close"), chromedp.Poll(`!document.querySelector('#recipe-import').open`, nil))
}
