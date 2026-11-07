package web

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

// testCleanupUI enters content cleanup rules one per line, previews the
// cleaned description and article bodies, and checks that saving and editing
// keep the rules. The fixture serves its list page for every address, so each
// story's article page is that page too.
func testCleanupUI(t *testing.T, tab context.Context, s *store.Store) {
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
	set := func(name, value string) chromedp.Action {
		return chromedp.Evaluate(`(()=>{const el=form.elements[`+jsString(name)+`];el.value=`+jsString(value)+`;el.dispatchEvent(new Event(el.tagName==='SELECT'?'change':'input'));})()`, nil)
	}

	// The import review lists a recipe's rules, which change what it publishes,
	// in the same entry as its Quiet threshold.
	file := filepath.Join(t.TempDir(), "cleanup.json")
	data, err := json.Marshal(recipeDocument{Format: recipeFormat, Version: recipeVersion, Feeds: []portableFeed{{Title: "Cleanup import", URL: "https://example.com/news", Interval: 900,
		Recipe: model.Recipe{Type: "css", Items: ".card", Title: model.Field{Selector: "h2"}, Remove: []string{".share", "aside.related"}, QuietDays: 90}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	must(clickQuery("#import-recipes"), chromedp.WaitVisible("#recipe-import"), chromedp.SetUploadFiles("#import-file", []string{file}),
		chromedp.Poll(`document.querySelectorAll('#import-preview .import-recipe').length===1`, nil))
	check(`(()=>{const entry=document.querySelector('#import-preview .import-recipe'),term=[...entry.querySelectorAll('.import-mapping dt')].find(dt=>dt.textContent==='Cleanup');
	 return !!term&&term.nextElementSibling.textContent==='.share\naside.related'&&[...entry.querySelectorAll('p.meta')].some(p=>p.textContent==='Mark as Quiet after: 90 days')})()`,
		"the import review does not list the cleanup rules beside the Quiet threshold")
	must(clickQuery("#import-close"), chromedp.Poll(`!document.querySelector('#recipe-import').open`, nil))

	must(clickQuery("#new-feed"), chromedp.WaitVisible("#editor"),
		set("title", "Cleanup fixture"), set("url", "https://example.com/news"), set("items", ".card"), set("title_selector", "h2"),
		set("link_selector", "a"), set("content_selector", "."), set("full_selector", "article.card"))
	check(`document.querySelector('#cleanup-summary').textContent==='Off'&&!document.querySelector('#content-cleanup').open`,
		"a new feed's cleanup section is not collapsed and off")
	must(clickQuery("#content-cleanup summary"), set("cleanup", " time \n\n\th2\n "))
	check(`document.querySelector('#cleanup-summary').textContent==='2 rules'&&document.querySelector('#cleanup-syntax-help').textContent.includes('p:containsOwn(')`,
		"the cleanup summary does not count the rules, or the CSS hint is missing")
	must(set("type", "xpath"))
	check(`document.querySelector('#cleanup-syntax-help').textContent.startsWith('XPath rules start with ./')`, "the hint does not follow the selector type")
	must(set("type", "css"))
	// The field keeps the editor's dark theme, like the other selectors.
	check(`(()=>{const a=getComputedStyle(form.elements.cleanup),b=getComputedStyle(form.elements.full_selector);
	 return document.documentElement.dataset.theme==='dark'&&a.color===b.color&&a.backgroundColor===b.backgroundColor&&a.borderColor===b.borderColor})()`,
		"the cleanup field does not follow the dark theme")

	// The description is the whole card and the article body its first card:
	// both lose the headline and the date.
	must(clickQuery("#preview-button"), chromedp.Poll(`document.querySelectorAll('#preview .preview-item').length===2&&!document.querySelector('#preview-button').disabled`, nil))
	check(`[...document.querySelectorAll('#preview .preview-item')].every(item=>{const body=item.querySelector('div');
	 return item.textContent.includes('Article body from this story')&&body.textContent.includes('First description')&&!body.querySelector('h2')&&!body.textContent.includes('September')})`,
		"the preview does not show cleaned article bodies")

	must(clickQuery(`#recipe-form button[type="submit"]`), chromedp.Poll(`document.querySelector('#editor').hidden&&feeds.some(f=>f.title==='Cleanup fixture')`, nil))
	feeds, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, f := range feeds {
		if f.Title == "Cleanup fixture" {
			id = f.ID
			if !slices.Equal(f.Recipe.Remove, []string{"time", "h2"}) {
				t.Fatalf("saved rules %q", f.Recipe.Remove)
			}
		}
	}
	if id == "" {
		t.Fatal("the feed was not saved")
	}
	defer func() {
		s.Delete(ctx, id)
		must(chromedp.Evaluate(`if(!document.querySelector('#editor').hidden)document.querySelector('#close-editor').click()`, nil), awaitLoad)
	}()
	must(awaitLoad, clickCurrent("Edit on the cleanup card", `[...document.querySelector('.feed-card[data-feed-id="`+id+`"]').querySelectorAll('button')].find(b=>b.textContent==='Edit')`),
		chromedp.Poll(`!document.querySelector('#editor').hidden&&form.elements.id.value==='`+id+`'`, nil))
	check(`form.elements.cleanup.value==='time\nh2'&&document.querySelector('#cleanup-summary').textContent==='2 rules'`,
		"editing lost the cleanup rules")
}

// jsString quotes a value as a JavaScript string literal.
func jsString(value string) string {
	quoted, _ := json.Marshal(value)
	return string(quoted)
}
