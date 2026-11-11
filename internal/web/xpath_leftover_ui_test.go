package web

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

// testXPathLeftoverUI leaves CSS class selectors in an XPath recipe, as the
// selector type menu does. The import review and Preview items name the
// selectors, a save is refused with the same explanation, and the article
// body field shows the syntax of the selected type. The fixture serves its
// list page for every address, so each story's article page is that page too.
func testXPathLeftoverUI(t *testing.T, tab context.Context, s *store.Store) {
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
	set := func(name, value string) chromedp.Action {
		return chromedp.Evaluate(`(()=>{const el=form.elements[`+jsString(name)+`];el.value=`+jsString(value)+`;el.dispatchEvent(new Event(el.tagName==='SELECT'?'change':'input'));})()`, nil)
	}
	const warned = `XPath uses only its first complete expression`

	// A file an earlier release exported still imports, so the review says
	// which of its selectors is not what it looks like.
	file := filepath.Join(t.TempDir(), "leftover.json")
	data, err := json.Marshal(recipeDocument{Format: recipeFormat, Version: recipeVersion, Feeds: []portableFeed{{Title: "Leftover import", URL: "https://example.com/news", Interval: 900,
		Recipe: model.Recipe{Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"}, Content: model.Field{Selector: ".card"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	must(clickQuery("#import-recipes"), chromedp.WaitVisible("#recipe-import"), chromedp.SetUploadFiles("#import-file", []string{file}),
		chromedp.Poll(`document.querySelectorAll('#import-preview .import-recipe').length===1`, nil))
	check(`(()=>{const lines=[...document.querySelectorAll('#import-preview .import-recipe p.diagnostic')].map(p=>p.textContent);
	 return lines.length===1&&lines[0].startsWith('Description selector: `+warned+`')&&!document.querySelector('#import-confirm').disabled})()`,
		"the import review does not warn about CSS left in an XPath selector, or refuses the file")
	must(clickQuery("#import-close"), chromedp.Poll(`!document.querySelector('#recipe-import').open`, nil))

	must(clickQuery("#new-feed"), chromedp.WaitVisible("#editor"))
	defer must(chromedp.Evaluate(`if(!document.querySelector('#editor').hidden)document.querySelector('#close-editor').click()`, nil))
	// The article body field follows the selector type, as the cleanup
	// hint beside it does.
	check(`form.elements.full_selector.placeholder==='article.content'&&form.elements.full_selector.getAttribute('aria-describedby')==='full-syntax-help'&&document.querySelector('#full-syntax-help').textContent.startsWith('A CSS selector for the block')`,
		"the article body field does not describe a CSS selector")
	must(set("type", "xpath"))
	check(`form.elements.full_selector.placeholder==="//div[@class='entry-content']"&&document.querySelector('#full-syntax-help').textContent.startsWith('An XPath article selector reads the article page from its root')`,
		"the article body field does not follow the selector type")

	must(set("title", "Leftover fixture"), set("url", "https://example.com/news"), set("items", "//article"), set("title_selector", ".//h2"),
		set("link_selector", ".//a/@href"), set("content_selector", ".card"), set("full_selector", ".card"),
		clickQuery("#preview-button"), chromedp.Poll(`document.querySelectorAll('#preview .preview-item').length===2&&!document.querySelector('#preview-button').disabled`, nil))
	// The preview shows what a refresh would publish, the whole page as each
	// article body, and names the two selectors behind it.
	check(`(()=>{const lines=[...document.querySelectorAll('#preview p.diagnostic')].map(p=>p.textContent);
	 return lines.length===2&&lines[0].startsWith('Description selector: `+warned+`')&&lines[1].startsWith('Article body selector: `+warned+`')
	  &&[...document.querySelectorAll('#preview .preview-item')].every(item=>item.textContent.includes('First description')&&item.textContent.includes('Second description'))})()`,
		"the preview does not warn about CSS left in the XPath selectors")

	// Saving them is refused, one selector at a time, and the editor stays open.
	submit := clickQuery(`#recipe-form button[type="submit"]`)
	must(submit, chromedp.Poll(`document.querySelector('#notice').textContent.startsWith('invalid XPath in the Description selector: `+warned+`')&&!document.querySelector('#recipe-form button[type="submit"]').disabled`, nil))
	must(set("content_selector", ".//p"), submit,
		chromedp.Poll(`document.querySelector('#notice').textContent.startsWith('invalid XPath in the Article body selector: `+warned+`')&&!document.querySelector('#recipe-form button[type="submit"]').disabled`, nil))
	check(`!document.querySelector('#editor').hidden&&form.elements.full_selector.value==='.card'`, "a refused save closed the editor or changed the recipe")
	feeds, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range feeds {
		if f.Title == "Leftover fixture" {
			t.Fatal("a recipe with CSS left in an XPath selector was saved")
		}
	}
}
