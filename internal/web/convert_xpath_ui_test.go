package web

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"rss-workshop/internal/store"
)

// testConvertXPathUI converts a full-content recipe with cleanup rules in the
// visual selector. The article body selector and the rules follow the selector
// type, so they are converted with the card selectors, and one that has no
// XPath form keeps the whole recipe in CSS. The fixture serves its list page
// for every address, so each story's article page is that page too, and the
// block the rules clean is the first card.
func testConvertXPathUI(t *testing.T, tab context.Context, s *store.Store) {
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
		return chromedp.Evaluate(`(()=>{const el=form.elements[`+jsString(name)+`];el.value=`+jsString(value)+`;el.dispatchEvent(new Event('input'));})()`, nil)
	}
	// Convert, then wait for the reply: the button's handler shows
	// "Converting…" before it returns, and every outcome replaces that text.
	convert := func() string {
		t.Helper()
		var status string
		must(clickQuery("#selector-convert"), chromedp.Poll(`!document.querySelector('#selector-status').textContent.startsWith('Converting')`, nil),
			chromedp.Evaluate(`document.querySelector('#selector-status').textContent`, &status))
		return status
	}
	const card = `//article[contains(concat(' ', normalize-space(@class), ' '), ' card ')]`
	const cssRules = "TIME\narticle.card h2\na[HREF]\nimg, h3"
	xpathRules := []string{".//time", "./self::" + card[2:] + "//h2 | ." + card + "//h2", ".//a[@href]", ".//img | .//h3"}
	css := `form.elements.type.value==='css'&&document.querySelector('#visual-type').value==='css'&&!document.querySelector('#selector-convert').hidden&&!document.querySelector('#selector-convert').disabled` +
		`&&form.elements.items.value==='article.card'&&form.elements.title_selector.value==='h2'&&form.elements.link_selector.value==='a'&&form.elements.content_selector.value==='.'`

	must(clickQuery("#new-feed"), chromedp.WaitVisible("#editor"),
		set("title", "Convert fixture"), set("url", "https://example.com/news"), set("items", "article.card"), set("title_selector", "h2"),
		set("link_selector", "a"), set("content_selector", "."), set("full_selector", "article.card"),
		set("cleanup", "time\np:containsOwn(\"appeared first on\")"),
		clickQuery("#visual-button"), chromedp.Poll(`document.querySelector('#selector-status').textContent.startsWith('1.')`, nil))
	defer must(chromedp.Evaluate(`(()=>{const d=document.querySelector('#visual-selector');if(d.open)d.close();if(!document.querySelector('#editor').hidden)document.querySelector('#close-editor').click()})()`, nil))

	// A rule with no XPath form: nothing is converted, and the message names
	// the rule, which the visual selector does not show.
	if got, want := convert(), `Cleanup rule 2: This CSS selector needs manual XPath conversion: :containsOwn("appeared first on") Your selectors were kept.`; got != want {
		t.Fatalf("converting with an unsupported cleanup rule reported %q, want %q", got, want)
	}
	check(css+`&&form.elements.full_selector.value==='article.card'&&form.elements.cleanup.value==='time\np:containsOwn("appeared first on")'`,
		"an unsupported cleanup rule did not keep the whole recipe in CSS")
	// The same for the article body selector.
	must(set("cleanup", cssRules), set("full_selector", "article.card:first-child"))
	if got, want := convert(), `Article body selector: This CSS selector needs manual XPath conversion: :first-child Your selectors were kept.`; got != want {
		t.Fatalf("converting with an unsupported article selector reported %q, want %q", got, want)
	}
	check(css+`&&form.elements.full_selector.value==='article.card:first-child'&&form.elements.cleanup.value===`+jsString(cssRules),
		"an unsupported article selector did not keep the whole recipe in CSS")
	// A list of alternatives is refused for the article selector, whose first
	// match alone is published: CSS takes the first in the page and an XPath
	// union the first alternative. The same list is converted as a rule.
	must(set("full_selector", "article.card, main"))
	if got, want := convert(), `Article body selector: A CSS selector list (a, b) needs manual XPath conversion, because XPath can take a different first match. Your selectors were kept.`; got != want {
		t.Fatalf("converting with a list of article selectors reported %q, want %q", got, want)
	}
	check(css+`&&form.elements.full_selector.value==='article.card, main'&&form.elements.cleanup.value===`+jsString(cssRules),
		"a list of article selectors did not keep the whole recipe in CSS")

	// The article selector becomes absolute, like the repeated items, and
	// each rule relative to the block it cleans, as XPath rules must be.
	// Element and attribute names are lowercased, since CSS matches them in
	// any case and XPath does not. A rule whose first part can name the
	// block itself says so, because CSS counts the block as an ancestor.
	must(set("full_selector", "ARTICLE.card"))
	if got, want := convert(), "Converted to XPath, including the article body selector and 4 cleanup rules in the feed editor. Fine-tune any expression and check its highlights on the right."; got != want {
		t.Fatalf("conversion reported %q, want %q", got, want)
	}
	check(`form.elements.type.value==='xpath'&&form.elements.items.value===`+jsString(card)+`&&form.elements.title_selector.value==='.//h2'&&form.elements.content_selector.value==='.'`,
		"the card selectors were not converted")
	var full, rules string
	must(chromedp.Evaluate(`form.elements.full_selector.value`, &full), chromedp.Evaluate(`form.elements.cleanup.value`, &rules))
	if want := strings.Join(xpathRules, "\n"); full != card || rules != want {
		t.Fatalf("converted article selector %q and cleanup rules %q, want %q and %q", full, rules, card, want)
	}
	check(`document.querySelector('#cleanup-syntax-help').textContent.startsWith('XPath rules start with ./')&&document.querySelector('#cleanup-summary').textContent==='4 rules'&&document.querySelector('#full-summary').textContent==='On'`,
		"the editor's full content and cleanup sections do not show the converted recipe")

	// The Go extractor reads the converted recipe as it read the CSS one: each
	// article body is the first card without its headline, date and link,
	// not the whole page, which holds both stories. The headline is inside
	// the card the second rule names, which is the block itself.
	must(clickQuery("#selector-close"), chromedp.Poll(`!document.querySelector('#visual-selector').open`, nil),
		clickQuery("#preview-button"), chromedp.Poll(`document.querySelectorAll('#preview .preview-item').length===2&&!document.querySelector('#preview-button').disabled`, nil))
	check(`[...document.querySelectorAll('#preview .preview-item')].every(item=>{const body=item.querySelector('div');
	 return item.textContent.includes('Article body from this story')&&body.textContent.includes('First description')&&!body.textContent.includes('Second description')&&!body.querySelector('h2,a')&&!body.textContent.includes('September')})`,
		"the preview does not show the converted recipe's cleaned article bodies")

	// The converted recipe can be saved, and is stored as the editor shows it.
	must(clickQuery(`#recipe-form button[type="submit"]`), chromedp.Poll(`document.querySelector('#editor').hidden&&feeds.some(f=>f.title==='Convert fixture')`, nil))
	feeds, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	saved := false
	for _, f := range feeds {
		if f.Title != "Convert fixture" {
			continue
		}
		saved = true
		defer func() {
			s.Delete(ctx, f.ID)
			must(awaitLoad)
		}()
		if r := f.Recipe; r.Type != "xpath" || r.Items != card || r.Full == nil || r.Full.Selector != card || !slices.Equal(r.Remove, xpathRules) {
			t.Fatalf("saved recipe type %q, items %q, article selector %+v, rules %q", r.Type, r.Items, r.Full, r.Remove)
		}
	}
	if !saved {
		t.Fatal("the converted feed was not saved")
	}
}
