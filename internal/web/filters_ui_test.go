package web

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

func testFiltersUI(t *testing.T, tab context.Context, s *store.Store) {
	t.Helper()
	must := func(actions ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(tab, actions...); err != nil {
			t.Fatal(err)
		}
	}
	var correct bool
	must(chromedp.Click("#new-feed"), chromedp.Evaluate(`(()=>{
	 Object.entries({title:'Filtered reading',url:'https://example.com/news',items:'.card',title_selector:'h2',link_selector:'a',content_selector:'p'}).forEach(([name,value])=>form.elements[name].value=value);
	 form.elements.enabled.checked=false;document.querySelector('#story-filters').open=true;
	 const original=window.fetch;window.filterRequests=[];window.restoreFilterFetch=()=>window.fetch=original;
	 window.fetch=async(...args)=>{const response=await original(...args);if(args[1]?.body)window.filterRequests.push({path:args[0],body:JSON.parse(args[1].body)});
	  if(args[0]==='/api/preview'&&window.injectFilterExample){window.injectFilterExample=false;const body=await response.json();body.filter_examples[0]={title:'<img id="filter-injection" src=x onerror="document.body.dataset.filterAttack=1">',reason:'<svg onload="document.body.dataset.filterAttack=1">'};return new Response(JSON.stringify(body),{status:response.status,headers:{'Content-Type':'application/json'}})}return response};
	})()`, nil), chromedp.Click("#filter-include .filter-add-condition"),
		chromedp.Evaluate(`(()=>{const input=document.querySelector('#filter-include textarea');input.value=['First story',...Array.from({length:98},(_,i)=>'topic '+(i+1)),'<img onerror="example">'].join('\n');input.dispatchEvent(new Event('input',{bubbles:true}));})()`, nil),
		chromedp.Click("#preview-button"), chromedp.Poll(`!document.querySelector('#preview-button').disabled&&document.querySelectorAll('#preview .preview-item').length===1`, nil),
		chromedp.Evaluate(`document.querySelector('#filter-count').textContent.startsWith('100 / 500')&&document.querySelector('#preview h2').textContent==='1 items · 2 matches'&&document.querySelector('.filter-preview-counts').textContent==='2 valid before filters · 1 included · 1 filtered out'&&document.querySelector('.filter-examples').textContent.includes('Second story')&&!document.querySelector('.filter-examples').open&&document.querySelector('#apply-filter-history').hidden`, &correct))
	if !correct {
		t.Fatal("100-phrase include preview lost matching, counts, examples, or new-feed defaults")
	}
	// Add a nested OR group inside the root AND group, and an exclude rule.
	must(chromedp.Click("#filter-include > .filter-actions .filter-add-group"),
		chromedp.Evaluate(`(()=>{
	 const nested=document.querySelector('#filter-include .filter-group .filter-group');
	 const op=nested.querySelector('.filter-group-op');op.value='any';op.dispatchEvent(new Event('change'));
	 const field=nested.querySelector('.filter-field');field.value='description';field.dispatchEvent(new Event('change'));
	 const matching=nested.querySelector('.filter-condition-op');matching.value='contains_all';matching.dispatchEvent(new Event('change'));
	 const words=nested.querySelector('textarea');words.value='first\ndescription';words.dispatchEvent(new Event('input'));
	 nested.querySelector(':scope > .filter-actions .filter-add-condition').click();
	})()`, nil), chromedp.Evaluate(`(()=>{
	 const condition=document.querySelector('#filter-include .filter-group .filter-group .filter-condition:last-child');
	 const field=condition.querySelector('.filter-field');field.value='link';field.dispatchEvent(new Event('change'));
	 const words=condition.querySelector('textarea');words.value='/one';words.dispatchEvent(new Event('input'));
	})()`, nil), chromedp.Click("#filter-exclude .filter-add-condition"),
		chromedp.Evaluate(`(()=>{const words=document.querySelector('#filter-exclude textarea');words.value='story';words.dispatchEvent(new Event('input'));window.injectFilterExample=true;})()`, nil),
		chromedp.Click("#preview-button"), chromedp.Poll(`!document.querySelector('#preview-button').disabled&&!!document.querySelector('.filter-empty-result')`, nil),
		chromedp.Evaluate(`document.querySelector('#preview h2').textContent==='0 items · 2 matches'&&document.querySelector('.filter-preview-counts').textContent==='2 valid before filters · 0 included · 2 filtered out'&&document.querySelector('.filter-empty-result').textContent.includes('Extraction succeeded')&&document.querySelector('#notice').hidden&&!document.querySelector('#filter-injection')&&!document.body.dataset.filterAttack&&document.querySelector('.filter-examples strong').textContent.startsWith('<img')&&[...document.querySelectorAll('.filter-examples strong,.filter-examples li p')].every(e=>e.childElementCount===0)&&document.querySelector('.run-trace').textContent.includes('Filtered out2')`, &correct))
	if !correct {
		t.Fatal("all-filtered preview was treated as extraction failure or rendered unsafe example HTML")
	}
	must(chromedp.Evaluate(`(()=>{const words=document.querySelector('#filter-exclude textarea');words.value='sponsored';words.dispatchEvent(new Event('input'));window.expectedFilters=readForm().recipe.filters;})()`, nil),
		chromedp.Click(`#recipe-form button[type="submit"]`), chromedp.Poll(`document.querySelector('#editor').hidden&&feeds.some(f=>f.title==='Filtered reading')`, nil),
		chromedp.Evaluate(`window.filterFeedID=feeds.find(f=>f.title==='Filtered reading').id;!('apply_filters_to_history' in window.filterRequests.find(r=>r.path==='/api/feeds').body)`, &correct))
	if !correct {
		t.Fatal("new feed unexpectedly sent a history-removal instruction")
	}
	var id string
	must(chromedp.Evaluate(`window.filterFeedID`, &id))
	defer s.Delete(context.Background(), id)
	f, err := s.Get(context.Background(), id)
	if err != nil || f.Recipe.Filters == nil || len(f.Recipe.Filters.Include.Rules[0].Keywords) != 100 {
		t.Fatal("saved recipe lost the bulk keywords", err)
	}
	// Seed existing stories so the unchecked versus checked edit has an actual
	// storage consequence; preview must never apply that save-only option.
	items := []model.Item{{Key: "first", Title: "First story", URL: "https://example.com/one", HTML: "First description"}, {Key: "second", Title: "Second story", URL: "https://example.com/two", HTML: "Second description"}}
	if err := s.Complete(context.Background(), f, items, "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	openEdit := `document.querySelector('.feed-diagnostics[data-feed-id="'+window.filterFeedID+'"]').closest('.feed-card').querySelector('button').click()`
	must(chromedp.Evaluate(openEdit, nil), chromedp.Evaluate(`JSON.stringify(readForm().recipe.filters)===JSON.stringify(window.expectedFilters)&&!document.querySelector('#apply-filter-history').hidden&&!document.querySelector('#apply-filters-to-history').checked`, &correct))
	if !correct {
		t.Fatal("editing lost nested filters or defaulted to removing history")
	}
	must(chromedp.Click(`#recipe-form button[type="submit"]`), chromedp.Poll(`document.querySelector('#editor').hidden`, nil))
	f, err = s.Get(context.Background(), id)
	if err != nil || f.Count != 2 {
		t.Fatal("ordinary filter edit removed existing stories", err)
	}
	must(chromedp.Evaluate(openEdit, nil), chromedp.Click("#apply-filters-to-history"), chromedp.Click("#preview-button"),
		chromedp.Poll(`!document.querySelector('#preview-button').disabled&&document.querySelectorAll('#preview .preview-item').length===1`, nil),
		chromedp.Evaluate(`window.filterRequests.filter(r=>r.path==='/api/preview').every(r=>!('apply_filters_to_history' in r.body))`, &correct))
	f, err = s.Get(context.Background(), id)
	if err != nil || f.Count != 2 || !correct {
		t.Fatal("preview sent or applied history removal", err)
	}
	must(chromedp.Click(`#recipe-form button[type="submit"]`), chromedp.Poll(`document.querySelector('#editor').hidden`, nil))
	f, err = s.Get(context.Background(), id)
	if err != nil || f.Count != 1 {
		t.Fatal("explicit history removal did not remove the excluded story", err)
	}
	must(chromedp.Evaluate(openEdit, nil), chromedp.Evaluate(`!document.querySelector('#apply-filters-to-history').checked&&window.filterRequests.some(r=>r.path==='/api/feeds/'+window.filterFeedID&&r.body.apply_filters_to_history===true)`, &correct))
	if !correct {
		t.Fatal("history option was lost or stayed checked on the next edit")
	}
	// Export the actual filtered recipe, then use the file chooser and import
	// confirmation to check that nested and bulk conditions survive portability.
	var archive string
	must(chromedp.Evaluate(`api('/recipes/export?id='+window.filterFeedID).then(out=>JSON.stringify(out))`, &archive, chromedp.EvalAsValue, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
	var exported recipeDocument
	if err := json.Unmarshal([]byte(archive), &exported); err != nil || len(exported.Feeds) != 1 {
		t.Fatal("filtered recipe export failed", err)
	}
	// Imported phrases may contain whitespace within one keyword. Editing must
	// keep this as one literal phrase, not turn its newline into an OR boundary.
	exported.Feeds[0].Recipe.Filters.Include.Rules[0].Keywords[1] = "climate\n\tchange"
	archiveBytes, err := json.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "filtered-recipes.json")
	if err := os.WriteFile(path, archiveBytes, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	must(chromedp.Click("#import-recipes"), chromedp.SetUploadFiles("#import-file", []string{path}),
		chromedp.Poll(`!document.querySelector('#import-confirm').disabled&&!document.querySelector('#import-confirm').hidden`, nil),
		chromedp.Click("#import-confirm"), chromedp.Poll(`!document.querySelector('#recipe-import').open`, nil))
	after, err := s.List(context.Background())
	if err != nil || len(after) != len(before)+1 {
		t.Fatal("filtered recipe import failed", err)
	}
	known := map[string]bool{}
	for _, existing := range before {
		known[existing.ID] = true
	}
	for _, imported := range after {
		if known[imported.ID] {
			continue
		}
		defer s.Delete(context.Background(), imported.ID)
		if imported.Enabled || !reflect.DeepEqual(imported.Recipe.Filters, exported.Feeds[0].Recipe.Filters) {
			t.Fatal("import changed nested filters or enabled the new feed")
		}
		must(chromedp.WaitVisible(`.feed-diagnostics[data-feed-id="`+imported.ID+`"]`), chromedp.Evaluate(`document.querySelector('.feed-diagnostics[data-feed-id="`+imported.ID+`"]').closest('.feed-card').querySelector('button').click()`, nil),
			chromedp.Evaluate(`(()=>{const words=readForm().recipe.filters.include.rules[0].keywords;return words.length===100&&words[1]==='climate change'})()`, &correct))
		if !correct {
			t.Fatal("editing an imported multiline phrase changed its meaning")
		}
	}
	must(chromedp.EmulateViewport(390, 844), chromedp.Evaluate(`document.documentElement.scrollWidth<=innerWidth+1&&getComputedStyle(document.querySelector('.filter-panel')).backgroundColor==='rgb(23, 44, 70)'`, &correct))
	if !correct {
		t.Fatal("nested filters overflow a narrow screen or lost dark mode")
	}
	captureTrial(t, tab, "filters-dark-mobile")
	must(chromedp.EmulateViewport(1280, 900), chromedp.Evaluate(`window.restoreFilterFetch();delete window.restoreFilterFetch;delete window.filterRequests;delete window.expectedFilters;delete window.filterFeedID`, nil))
}
