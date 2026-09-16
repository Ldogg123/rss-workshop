package web

import (
	"context"
	"reflect"
	"testing"

	"github.com/chromedp/chromedp"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

func testLibraryFiltersUI(t *testing.T, tab context.Context, s *store.Store) {
	t.Helper()
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
	setKeywords := func(panel, text string) chromedp.Action {
		return chromedp.Evaluate(`(()=>{const input=document.querySelector('`+panel+` textarea');input.value=`+"`"+text+"`"+`;input.dispatchEvent(new Event('input',{bubbles:true}));})()`, nil)
	}
	must(chromedp.Evaluate(`(()=>{if(!document.querySelector('#editor').hidden)document.querySelector('#close-editor').click();
	 const original=window.fetch;window.libraryRequests=[];window.restoreLibraryFetch=()=>window.fetch=original;
	 window.fetch=async(...args)=>{if(args[1]?.body)window.libraryRequests.push({path:args[0],method:args[1].method,body:JSON.parse(args[1].body)});return original(...args)};
	})()`, nil),
		chromedp.Click("#manage-filters"), chromedp.Poll(`document.querySelector('#filter-library').open`, nil))
	defer chromedp.Run(tab, chromedp.Evaluate(`window.restoreLibraryFetch()`, nil))
	check(`!document.querySelector('#filter-library-empty').hidden&&document.querySelector('#library-filter-form').hidden`, "empty library did not explain itself")

	// An empty filter is refused in the dialog before any request is made.
	must(chromedp.Click("#library-filter-new"), chromedp.SetValue("#library-filter-name", "No second story"),
		chromedp.Click(`#library-filter-form button[type="submit"]`), chromedp.Poll(`!document.querySelector('#library-filter-error').hidden`, nil))
	check(`!window.libraryRequests.some(r=>r.path==='/api/filters')`, "empty library filter was submitted")
	must(chromedp.Click("#library-filter-exclude .filter-add-condition"), setKeywords("#library-filter-exclude", "Second"),
		chromedp.Click(`#library-filter-form button[type="submit"]`),
		chromedp.Poll(`document.querySelectorAll('#filter-library-list .library-filter').length===1&&document.querySelector('#library-filter-form').hidden`, nil))
	check(`document.querySelector('#filter-library-list h3').textContent==='No second story'&&document.querySelector('#filter-library-list').textContent.includes('Not used by any feed')&&!document.querySelector('#filter-library-list .danger').disabled`, "saved library filter was not listed")
	library, err := s.Filters(context.Background())
	if err != nil || len(library) != 1 || !reflect.DeepEqual(library[0].Filters, model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"Second"}}}) {
		t.Fatalf("library filter was not stored: %+v %v", library, err)
	}
	filterID := library[0].ID
	defer s.DeleteFilter(context.Background(), filterID)

	// Select it in a new feed: the preview and the saved feed both use it.
	must(chromedp.Click("#filter-library-close"), chromedp.Click("#new-feed"), chromedp.Evaluate(`(()=>{
	 Object.entries({title:'Library reading',url:'https://example.com/news',items:'.card',title_selector:'h2',link_selector:'a',content_selector:'p'}).forEach(([name,value])=>form.elements[name].value=value);
	 form.elements.enabled.checked=false;document.querySelector('#story-filters').open=true;
	})()`, nil), chromedp.Poll(`document.querySelectorAll('#library-filter-choices input[type=checkbox]').length===1`, nil),
		chromedp.Click("#library-filter-choices input[type=checkbox]"), chromedp.Click("#preview-button"),
		chromedp.Poll(`!document.querySelector('#preview-button').disabled&&document.querySelectorAll('#preview .preview-item').length===1`, nil))
	must(chromedp.Evaluate(`document.querySelector('#library-filter-picker').scrollIntoView()`, nil))
	captureTrial(t, tab, "library-filter-picker")
	check(`document.querySelector('.filter-preview-counts').textContent==='2 valid before filters · 1 included · 1 filtered out'&&document.querySelector('#filter-summary-count').textContent==='1 library filter'&&window.libraryRequests.find(r=>r.path==='/api/preview').body.filter_ids[0]==='`+filterID+`'`, "preview did not apply the selected library filter")
	must(chromedp.Click(`#recipe-form button[type="submit"]`), chromedp.Poll(`document.querySelector('#editor').hidden&&feeds.some(f=>f.title==='Library reading')`, nil))
	var feedID string
	must(chromedp.Evaluate(`feeds.find(f=>f.title==='Library reading').id`, &feedID))
	defer s.Delete(context.Background(), feedID)
	f, err := s.Get(context.Background(), feedID)
	if err != nil || !reflect.DeepEqual(f.FilterIDs, []string{filterID}) || f.Recipe.Filters != nil {
		t.Fatalf("saved feed did not link the library filter: %+v %v", f, err)
	}
	card := `document.querySelector('.feed-diagnostics[data-feed-id="` + feedID + `"]').closest('.feed-card')`
	check(card+`.querySelector('.feed-filters').textContent==='Library filters: No second story'`, "feed card did not name its library filter")

	// Pausing sends no filter list and must keep the link.
	must(chromedp.Evaluate(`[...`+card+`.querySelectorAll('button')].find(b=>b.textContent==='Resume').click()`, nil),
		chromedp.Poll(`feeds.find(f=>f.id==='`+feedID+`').enabled`, nil))
	if f, err = s.Get(context.Background(), feedID); err != nil || len(f.FilterIDs) != 1 {
		t.Fatal("resuming a feed unlinked its library filter", err)
	}

	// A session that ends with the feed open in the editor keeps its selection:
	// saving after signing in again must not unlink the library filter.
	must(chromedp.Evaluate(`[...`+card+`.querySelectorAll('button')].find(b=>b.textContent==='Edit').click()`, nil),
		chromedp.Poll(`document.querySelector('#library-filter-choices input[type=checkbox]')?.checked`, nil),
		chromedp.Evaluate(`showLogin()`, nil), chromedp.SetValue("#password", "ui-test-password-123"),
		chromedp.Click(`#login-form button[type="submit"]`), chromedp.Poll(`!document.querySelector('#workspace').hidden&&!document.querySelector('#editor').hidden`, nil),
		chromedp.Poll(`document.querySelector('#library-filter-choices input[type=checkbox]')?.checked`, nil),
		chromedp.Click(`#recipe-form button[type="submit"]`), chromedp.Poll(`document.querySelector('#editor').hidden`, nil))
	if f, err = s.Get(context.Background(), feedID); err != nil || len(f.FilterIDs) != 1 {
		t.Fatal("signing in again with the editor open unlinked the library filter", err)
	}

	// Editing the filter reports the feeds it changes, and cannot be deleted while used.
	must(chromedp.Click("#manage-filters"), chromedp.Poll(`document.querySelector('#filter-library-list').textContent.includes('Used by Library reading')`, nil))
	captureTrial(t, tab, "library-filter-list")
	check(`document.querySelector('#filter-library-list .danger').disabled`, "a library filter in use could be deleted")
	must(chromedp.Evaluate(`[...document.querySelectorAll('#filter-library-list button')].find(b=>b.textContent==='Edit').click()`, nil),
		chromedp.Poll(`!document.querySelector('#library-filter-form').hidden`, nil))
	captureTrial(t, tab, "library-filter-edit")
	check(`document.querySelector('#library-filter-used-by').textContent.includes('Library reading')&&!document.querySelector('#library-filter-history').hidden&&!document.querySelector('#library-filter-apply-history').checked&&document.querySelector('#library-filter-exclude textarea').value==='Second'`, "editing did not load the filter or its users")
	must(setKeywords("#library-filter-exclude", "First"), chromedp.Click(`#library-filter-form button[type="submit"]`),
		chromedp.Poll(`document.querySelector('#filter-library-status').textContent.includes('1 feed will use the new rules')`, nil))
	library, err = s.Filters(context.Background())
	if err != nil || library[0].Filters.Exclude.Keywords[0] != "First" {
		t.Fatal("library edit was not saved", err)
	}
	if edited, err := s.Get(context.Background(), feedID); err != nil || edited.Version != f.Version+1 {
		t.Fatal("library edit did not invalidate the linked feed", err)
	}

	// Removing it from the feed makes it deletable.
	must(chromedp.Click("#filter-library-close"), chromedp.Evaluate(`[...`+card+`.querySelectorAll('button')].find(b=>b.textContent==='Edit').click()`, nil),
		chromedp.Poll(`document.querySelector('#library-filter-choices input[type=checkbox]')?.checked`, nil),
		chromedp.Click("#library-filter-choices input[type=checkbox]"), chromedp.Click(`#recipe-form button[type="submit"]`),
		chromedp.Poll(`document.querySelector('#editor').hidden&&!`+card+`.querySelector('.feed-filters')`, nil))
	if f, err = s.Get(context.Background(), feedID); err != nil || len(f.FilterIDs) != 0 {
		t.Fatal("unchecking the library filter did not unlink it", err)
	}
	must(chromedp.Click("#manage-filters"), chromedp.Poll(`document.querySelector('#filter-library-list').textContent.includes('Not used by any feed')`, nil),
		chromedp.Evaluate(`window.confirm=()=>true;document.querySelector('#filter-library-list .danger').click()`, nil),
		chromedp.Poll(`document.querySelectorAll('#filter-library-list .library-filter').length===0&&!document.querySelector('#filter-library-empty').hidden`, nil),
		chromedp.Click("#filter-library-close"))
	if library, err = s.Filters(context.Background()); err != nil || len(library) != 0 {
		t.Fatal("library filter was not deleted", err)
	}
}
