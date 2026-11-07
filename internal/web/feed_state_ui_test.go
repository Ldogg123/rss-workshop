package web

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
	"rss-workshop/internal/testutil"
)

// testFeedStateUI covers the card's Pause and Resume, an editor left open
// across a pause, and a feed held after the database refused its refresh.
func testFeedStateUI(t *testing.T, tab context.Context, s *store.Store, fetcher *fixtureFetcher) {
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
	// The page polls every ten seconds; reload the library directly instead.
	waitAfterLoad := func(expression, failure string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var ok bool
			must(chromedp.Evaluate(`load().then(()=>`+expression+`)`, &ok, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
			if ok {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal(failure)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	id, err := s.Save(ctx, model.Feed{Title: "Card state", URL: "https://example.com/news", Interval: 3600, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: ".card", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Delete(ctx, id)
	// Let its first refresh finish, so later steps do not race it.
	deadline := time.Now().Add(5 * time.Second)
	saved, err := s.Get(ctx, id)
	for err == nil && saved.LastSuccess.IsZero() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		saved, err = s.Get(ctx, id)
	}
	if err != nil || saved.LastSuccess.IsZero() {
		t.Fatal("the new feed's first refresh did not finish", err)
	}
	card := `document.querySelector('.feed-diagnostics[data-feed-id="` + id + `"]').closest('.feed-card')`
	button := func(label string) chromedp.Action {
		return clickCurrent(label+" on the Card state card", `[...`+card+`.querySelectorAll('button')].find(b=>b.textContent==='`+label+`')`)
	}
	listed := `feeds.find(f=>f.id==='` + id + `')`
	writes := `window.stateRequests.filter(r=>r.method!=='GET'&&r.path.startsWith('/api/feeds/` + id + `'))`
	must(chromedp.Evaluate(`(()=>{if(!document.querySelector('#editor').hidden)document.querySelector('#close-editor').click();
	 const original=window.fetch;window.stateRequests=[];window.restoreStateFetch=()=>window.fetch=original;
	 window.fetch=async(...args)=>{window.stateRequests.push({path:args[0],method:args[1]?.method||'GET',body:args[1]?.body?JSON.parse(args[1].body):undefined});return original(...args)};
	})()`, nil))
	defer chromedp.Run(tab, chromedp.Evaluate(`window.restoreStateFetch()`, nil))
	waitAfterLoad(`!!`+listed, "the new feed was not listed")

	// Pause from the card while the editor is open on the same feed.
	must(button("Edit"),
		chromedp.Poll(`!document.querySelector('#editor').hidden&&form.elements.title.value==='Card state'&&form.elements.enabled.checked`, nil),
		button("Pause"),
		chromedp.Poll(listed+`?.enabled===false&&`+card+`.querySelector('.badge').textContent==='Paused'`, nil))
	check(`(()=>{const r=`+writes+`;return r.length===1&&r[0].method==='POST'&&r[0].path==='/api/feeds/`+id+`/enabled'&&JSON.stringify(r[0].body)==='{"enabled":false}'})()`,
		"Pause did not send only the enabled setting")
	if paused, err := s.Get(ctx, id); err != nil || paused.Enabled || paused.Version != saved.Version {
		t.Fatalf("Pause saved more than the setting: enabled=%v, version %d -> %d, %v", paused.Enabled, saved.Version, paused.Version, err)
	}

	// Saving the editor without touching its checkbox keeps the pause.
	must(chromedp.SetValue(`#recipe-form input[name="title"]`, "Card state, edited"), chromedp.Click(`#recipe-form button[type="submit"]`),
		chromedp.Poll(`document.querySelector('#editor').hidden&&`+listed+`?.title==='Card state, edited'`, nil))
	check(`(()=>{const r=`+writes+`.at(-1);return r.method==='PUT'&&r.body.enabled===true&&r.body.loaded_enabled===true})()`,
		"the editor did not send the enabled value it opened with")
	if edited, err := s.Get(ctx, id); err != nil || edited.Enabled || edited.Title != "Card state, edited" {
		t.Fatalf("an editor left open undid a pause: enabled=%v title=%q, %v", edited.Enabled, edited.Title, err)
	}

	// Resume while the database refuses new stories: the refresh it starts
	// cannot be stored, which holds the feed. The card says why it is not
	// refreshing, and Refresh tries again at once. Nothing else refreshes
	// this feed while it is paused, so no earlier refresh can race the block.
	restore := testutil.BlockItemWrites(t, s.DB)
	must(button("Resume"), chromedp.Poll(listed+`?.enabled===true`, nil))
	held := card + `.querySelector('.badge').textContent==='Waiting to retry'&&!!` + card + `.querySelector('.feed-held')&&` + card + `.querySelector('.times').textContent.includes('Delayed after a failed save')`
	waitAfterLoad(held, "the card did not show the feed waiting to retry a failed database write")
	must(chromedp.Evaluate(card+`.scrollIntoView()`, nil))
	captureTrial(t, tab, "feed-held")
	before := fetcher.calls.Load()
	must(button("Refresh"), chromedp.Poll(`document.querySelector('#notice').textContent==='Refresh queued.'`, nil))
	deadline = time.Now().Add(5 * time.Second)
	for fetcher.calls.Load() == before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fetcher.calls.Load() == before {
		t.Fatal("Refresh did not retry a held feed")
	}
	waitAfterLoad(held, "a retry that failed again did not hold the feed")
	restore()
	must(button("Refresh"))
	waitAfterLoad(card+`.querySelector('.badge').textContent==='Active'&&!`+card+`.querySelector('.feed-held')`, "the card stayed held after the database recovered")
}
