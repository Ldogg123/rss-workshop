package web

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

// testDashboardUI covers the library toolbar (search, status filter, sort and
// the remembered view), the quiet badge, and Duplicate. The fixture's
// scheduler is running, so feeds are stored paused and enabled afterwards
// with a distant next refresh, which keeps it from changing their state.
func testDashboardUI(t *testing.T, tab context.Context, s *store.Store) {
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
	awaitLoad := chromedp.Evaluate(`load()`, nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) })
	recipe := model.Recipe{Type: "css", Items: ".card", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}}
	now := time.Now()
	// Feeds go first: a library filter in use cannot be deleted.
	var created, filters []string
	defer func() {
		for _, id := range created {
			s.Delete(ctx, id)
		}
		for _, id := range filters {
			s.DeleteFilter(ctx, id)
		}
	}()
	// add stores a feed with the given refreshes, then sets the times the
	// sorts compare, so feeds stored in the same second still differ.
	add := func(f model.Feed, runs []error, succeeded, changed time.Time, next time.Duration) string {
		t.Helper()
		enabled := f.Enabled
		f.Enabled = false
		id, err := s.Save(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, id)
		for _, runErr := range runs {
			saved, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Complete(ctx, saved, []model.Item{{Key: "story", Title: "Story"}}, "", "", 200, runErr, 0); err != nil {
				t.Fatal(err)
			}
		}
		queries := []string{}
		if !succeeded.IsZero() {
			queries = append(queries, fmt.Sprintf("UPDATE feeds SET last_success=%d WHERE id='%s'", succeeded.Unix(), id))
			if runs[len(runs)-1] == nil {
				queries = append(queries, fmt.Sprintf("UPDATE feeds SET last_attempt=%d WHERE id='%s'", succeeded.Unix(), id))
			}
		}
		if !changed.IsZero() {
			queries = append(queries, fmt.Sprintf("UPDATE feeds SET last_changed=%d WHERE id='%s'", changed.Unix(), id), fmt.Sprintf("UPDATE items SET last_changed=%d WHERE feed_id='%s'", changed.Unix(), id))
		}
		if enabled {
			queries = append(queries, fmt.Sprintf("UPDATE feeds SET enabled=TRUE,next_run=%d WHERE id='%s'", now.Add(next).Unix(), id))
		}
		for _, query := range queries {
			if _, err := s.DB.Exec(query); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	quiet := add(model.Feed{Title: "Morning briefing", URL: "https://alpha.example/zebra-desk", Interval: 3600, Enabled: true, Recipe: recipe},
		[]error{nil}, now.Add(-30*time.Minute), now.Add(-20*24*time.Hour-time.Hour), 2*time.Hour)
	failing := add(model.Feed{Title: "Beta bulletin", URL: "https://beta.example/news", Interval: 3600, Enabled: true, Recipe: recipe},
		[]error{nil, errors.New("source unavailable"), errors.New("source unavailable")}, now.Add(-3*time.Hour), time.Time{}, time.Hour)
	paused := add(model.Feed{Title: "Gamma gazette", URL: "https://gamma.example/news", Interval: 3600, Recipe: recipe},
		[]error{nil}, now.Add(-time.Hour), now.Add(-40*24*time.Hour), 0)

	card := func(id string) string { return `document.querySelector('.feed-card[data-feed-id="` + id + `"]')` }
	shown := `[...document.querySelectorAll('#feeds .feed-card')].map(c=>c.dataset.feedId)`
	order := func(ids ...string) string {
		return `(()=>{const ids=` + shown + `;const at=[` + `'` + strings.Join(ids, `','`) + `'` + `].map(id=>ids.indexOf(id));return at.every((n,i)=>n>=0&&(i===0||n>at[i-1]))})()`
	}
	choose := func(control, value string) chromedp.Action {
		return chromedp.Evaluate(`(()=>{const c=document.querySelector('`+control+`');c.value='`+value+`';c.dispatchEvent(new Event('change'))})()`, nil)
	}
	must(chromedp.Evaluate(`if(!document.querySelector('#editor').hidden)document.querySelector('#close-editor').click()`, nil), awaitLoad)
	check(`!document.querySelector('#library-tools').hidden&&document.querySelector('#feed-status').value==='all'&&document.querySelector('#feed-sort').value==='title'&&document.querySelector('#feed-count').textContent==='Showing all '+feeds.length+' feeds'`,
		"the library toolbar did not start with every feed shown by name")
	check(`['#feed-search','#feed-status','#feed-sort'].every(s=>document.querySelector(s).labels.length===1&&document.querySelector(s).labels[0].textContent.trim().length>0)`,
		"a library control has no label")

	// The quiet badge and its explanation, in the dark theme's colors.
	check(card(quiet)+`.querySelector('.badge').textContent==='Quiet'&&`+card(quiet)+`.querySelector('.feed-quiet').textContent.startsWith('Output unchanged for 20 days.')`,
		"the quiet feed has no Quiet badge and explanation")
	check(card(failing)+`.querySelector('.badge').textContent==='Needs attention'&&`+card(failing)+`.querySelector('.times').textContent.includes('Failures in a row2')&&!`+card(failing)+`.querySelector('.feed-quiet')`,
		"the failing feed is not reported as failing")
	check(card(paused)+`.querySelector('.badge').textContent==='Paused'&&!`+card(paused)+`.querySelector('.feed-quiet')`, "a paused feed is reported as quiet")
	check(`document.documentElement.dataset.theme==='dark'&&getComputedStyle(document.querySelector('#feed-search')).backgroundColor==='rgb(17, 28, 45)'&&getComputedStyle(`+card(quiet)+`.querySelector('.badge')).backgroundColor==='rgb(34, 59, 97)'`,
		"the library toolbar or the quiet badge ignores the dark theme")
	check(`document.querySelector('#status-help').textContent.includes('14 days, or for 10 refresh intervals')`, "the status help does not give the quiet threshold")

	// Search matches the name and the site, not the rest of the address, and
	// the poll keeps both the search and the focus.
	must(chromedp.Focus("#feed-search"), chromedp.SendKeys("#feed-search", "ALPHA.example"))
	check(`JSON.stringify(`+shown+`)==='["`+quiet+`"]'&&document.querySelector('#feed-count').textContent==='Showing 1 of '+feeds.length+' feeds'`, "searching by site did not narrow the library")
	must(awaitLoad)
	check(`JSON.stringify(`+shown+`)==='["`+quiet+`"]'&&document.activeElement===document.querySelector('#feed-search')&&document.querySelector('#feed-search').value==='ALPHA.example'`,
		"the poll reset the search or took focus from it")
	// SetValue cannot set an empty value, so clear the field in the page.
	clear := chromedp.Evaluate(`document.querySelector('#feed-search').value=''`, nil)
	must(clear, chromedp.SendKeys("#feed-search", "  morning   BRIEF"))
	check(`JSON.stringify(`+shown+`)==='["`+quiet+`"]'`, "searching by name did not ignore case and spacing")
	must(clear, chromedp.SendKeys("#feed-search", "zebra-desk"))
	check(shown+`.length===0&&!document.querySelector('#no-matches').hidden&&document.querySelector('#empty').hidden`, "a search matched the path or showed the first-run panel")
	must(chromedp.Click("#show-all-feeds"))
	check(shown+`.length===feeds.length&&document.querySelector('#no-matches').hidden&&document.activeElement===document.querySelector('#feed-search')`, "Show all feeds did not clear the search")

	// A control inside a card keeps focus across the poll's redraw.
	must(chromedp.Evaluate(card(quiet)+`.querySelector('.feed-diagnostics').focus()`, nil), awaitLoad)
	check(`document.activeElement.classList.contains('feed-diagnostics')&&document.activeElement.dataset.feedId==='`+quiet+`'`, "the poll took focus from a card's button")

	// Status filters follow the badge.
	for status, id := range map[string]string{"quiet": quiet, "failing": failing, "paused": paused} {
		must(choose("#feed-status", status))
		check(shown+`.includes('`+id+`')&&[...document.querySelectorAll('#feeds .badge')].every(b=>b.textContent===statusLabels['`+status+`'])`, "the "+status+" filter shows the wrong feeds")
	}
	must(choose("#feed-status", "all"))

	// Sorts, each with its own direction.
	for sort, ids := range map[string][]string{
		"title":    {failing, paused, quiet},
		"next":     {failing, quiet, paused},
		"success":  {failing, paused, quiet},
		"changed":  {paused, quiet, failing},
		"failures": {failing, quiet},
	} {
		must(choose("#feed-sort", sort))
		check(order(ids...), "the "+sort+" sort put the feeds in the wrong order")
	}

	// The browser remembers status and sort, but not the search.
	must(choose("#feed-status", "quiet"), choose("#feed-sort", "changed"), chromedp.SendKeys("#feed-search", "morning"))
	check(`localStorage.getItem('rss-library-view')==='{"status":"quiet","sort":"changed"}'`, "the library view was not remembered")
	captureTrial(t, tab, "dashboard-quiet")
	must(chromedp.Reload(), chromedp.WaitVisible(".feed-card"))
	check(`document.querySelector('#feed-status').value==='quiet'&&document.querySelector('#feed-sort').value==='changed'&&document.querySelector('#feed-search').value===''&&`+shown+`.includes('`+quiet+`')&&!`+shown+`.includes('`+failing+`')`,
		"a reload lost the remembered view")

	// Without storage the page still works, from the defaults.
	var blocked page.ScriptIdentifier
	must(chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		blocked, err = page.AddScriptToEvaluateOnNewDocument(`Object.defineProperty(window,'localStorage',{configurable:true,get(){throw new DOMException('Storage is disabled','SecurityError')}})`).Do(ctx)
		return err
	}), chromedp.Reload(), chromedp.WaitVisible(".feed-card"))
	check(`(()=>{try{localStorage;return false}catch{return true}})()`, "the fixture could not make storage unavailable")
	must(choose("#feed-sort", "failures"))
	check(`document.querySelector('#feed-status').value==='all'&&`+shown+`.length===feeds.length&&`+order(failing, quiet), "the library did not work without storage")
	must(chromedp.ActionFunc(func(ctx context.Context) error { return page.RemoveScriptToEvaluateOnNewDocument(blocked).Do(ctx) }),
		chromedp.Reload(), chromedp.WaitVisible(".feed-card"), choose("#feed-status", "all"), choose("#feed-sort", "title"))

	// Duplicate opens a new, unsaved feed with every setting, its library
	// filters linked rather than copied, and a name that still fits 200 bytes.
	library, err := s.SaveFilter(ctx, model.LibraryFilter{Name: "Copied by reference", Filters: model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"sponsored"}}}}, store.SaveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	filters = append(filters, library)
	full := model.Recipe{Mode: "auto", WaitSelector: "article.card", SettleMS: 750, Type: "css", Items: ".card", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a", Attr: "href"},
		Content: model.Field{Selector: "p"}, Image: model.Field{Selector: "img", Attr: "data-src"}, Date: model.Field{Selector: "time", Attr: "datetime"}, DateLayout: "2006-01-02", Timezone: "UTC",
		Filters: &model.FilterSet{Include: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"story"}}}, Full: &model.FullContent{Selector: ".body", Browser: true}}
	// 66 three-byte characters: 198 bytes, so "Copy of " would pass the
	// field's 200-character limit and fail the server's 200-byte one.
	wide := strings.Repeat("界", 66)
	source := add(model.Feed{Title: wide, URL: "https://example.com/news", Interval: 7200, Enabled: true, Recipe: full, FilterIDs: []string{library}}, nil, time.Time{}, time.Time{}, 24*time.Hour)
	original, err := s.Get(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	must(awaitLoad, chromedp.Evaluate(`[...`+card(source)+`.querySelectorAll('button')].find(b=>b.textContent==='Duplicate').click()`, nil),
		chromedp.Poll(`!document.querySelector('#editor').hidden&&!!document.querySelector('#library-filter-choices input[value="`+library+`"]')?.checked`, nil))
	copied := "Copy of " + strings.Repeat("界", 63) + "…"
	check(`document.querySelector('#editor-title').textContent==='Duplicate feed'&&form.elements.id.value===''&&form.elements.title.value==='`+copied+`'&&new TextEncoder().encode(form.elements.title.value).length===200`,
		"Duplicate did not open a new feed named within 200 bytes")
	check(`!form.elements.enabled.checked&&!document.querySelector('#duplicate-hint').hidden&&document.querySelector('#apply-filter-history').hidden&&form.elements.items.value==='.card'&&form.elements.wait_selector.value==='article.card'&&form.elements.settle_ms.value==='750'&&form.elements.full_selector.value==='.body'&&form.elements.full_browser.checked&&form.elements.timezone.value==='UTC'&&form.elements.date_layout.value==='2006-01-02'&&form.elements.interval.value==='120'&&document.querySelector('#filter-include textarea').value==='story'`,
		"Duplicate did not copy the feed's settings, or offered to prune history")
	must(chromedp.Click(`#recipe-form button[type="submit"]`), chromedp.Poll(`document.querySelector('#editor').hidden&&feeds.some(f=>f.title==='`+copied+`')`, nil))
	feeds, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var copy model.Feed
	for _, f := range feeds {
		if f.Title == copied {
			copy, err = s.Get(ctx, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			created = append(created, f.ID)
		}
	}
	if copy.ID == "" || copy.ID == source || copy.RSSToken == original.RSSToken || copy.Enabled || copy.Count != 0 || copy.Interval != 7200 || copy.URL != original.URL ||
		!reflect.DeepEqual(copy.Recipe, full) || !reflect.DeepEqual(copy.FilterIDs, []string{library}) {
		t.Fatalf("the saved copy differs: %+v", copy)
	}
	if after, err := s.Get(ctx, source); err != nil || after.Version != original.Version || after.Title != wide || !after.Enabled {
		t.Fatalf("duplicating changed the original: %+v, %v", after, err)
	}
	check(card(copy.ID)+`.querySelector('.badge').textContent==='Paused'&&!`+card(copy.ID)+`.querySelector('.rss-link').href.includes('`+original.RSSToken+`')`,
		"the copy's card does not show a paused feed with its own links")
}
