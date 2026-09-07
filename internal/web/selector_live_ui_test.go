package web

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chromedp/chromedp"
)

// Exercise the real parent/frame bridge while editing selectors; field text and
// attribute XPath results must map back to visible source elements without a fetch.
func testLiveSelector(t *testing.T, tab, preview context.Context, fetcher *fixtureFetcher) {
	t.Helper()
	must := func(actions ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(tab, actions...); err != nil {
			t.Fatal(err)
		}
	}
	frame := func(actions ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(preview, actions...); err != nil {
			t.Fatal(err)
		}
	}
	edit := func(id, value string) {
		t.Helper()
		a, _ := json.Marshal(id)
		b, _ := json.Marshal(value)
		must(chromedp.Evaluate(`(()=>{const el=document.querySelector(`+string(a)+`);el.focus();el.value=`+string(b)+`;el.dispatchEvent(new Event('input',{bubbles:true}));})()`, nil))
	}
	counts := func(items, count int) {
		t.Helper()
		want, _ := json.Marshal([]int{items, count})
		must(chromedp.Poll(`(()=>{const s=document.querySelector('#selector-matches');const want=`+string(want)+`;return s.dataset.error==='false'&&Number(s.dataset.items)===want[0]&&Number(s.dataset.count)===want[1]})()`, nil))
	}
	var ok bool
	must(chromedp.Evaluate(`(()=>{const l=document.querySelector('.selector-controls').getBoundingClientRect(),r=document.querySelector('#selector-frame-host').getBoundingClientRect();return r.left>=l.right&&r.width>l.width})()`, &ok))
	if !ok {
		t.Fatal("source preview is not to the right of selector controls")
	}
	before := fetcher.calls.Load()
	edit("#visual-items", ".card\nh2")
	counts(2, 2)
	edit("#visual-items", ".card:has(h2)")
	counts(2, 2)
	edit("#visual-title-selector", "h2")
	counts(2, 2)
	frame(chromedp.Evaluate(`document.querySelectorAll('#page .field-matched').length===2`, &ok))
	if !ok {
		t.Fatal("manual CSS did not highlight titles")
	}
	must(chromedp.Click("#selector-convert"), chromedp.Poll(`document.querySelector('#visual-type').value==='xpath'&&document.querySelector('#recipe-form [name="type"]').value==='xpath'`, nil))
	counts(2, 2)
	// Text predicates require real source text in the inert selector tree.
	edit("#visual-title-selector", ".//h2[contains(text(), 'First')]/text()")
	counts(2, 1)
	frame(chromedp.Evaluate(`(()=>{const a=[...document.querySelectorAll('#page .field-matched')];return a.length===1&&a[0].localName==='h2'&&a[0].textContent==='First story'})()`, &ok))
	if !ok {
		t.Fatal("XPath text result did not highlight its heading")
	}
	edit("#visual-link-selector", ".//a[contains(@href, 'two')]/@href")
	counts(2, 1)
	frame(chromedp.Evaluate(`document.querySelectorAll('#page .source-link.field-matched').length===1`, &ok))
	if !ok {
		t.Fatal("XPath attribute result did not highlight its link")
	}
	edit("#visual-link-selector", ".//a/@href")
	counts(2, 2)
	edit("#visual-title-selector", ".//[")
	must(chromedp.Poll(`document.querySelector('#selector-matches').dataset.error==='true'`, nil))
	frame(chromedp.Evaluate(`document.querySelectorAll('#page .matched,#page .field-matched').length===0`, &ok))
	if !ok {
		t.Fatal("invalid XPath retained stale highlights")
	}
	edit("#visual-title-selector", ".//h9")
	counts(2, 0)
	frame(chromedp.Evaluate(`document.querySelectorAll('#page .field-matched').length===0`, &ok))
	if !ok {
		t.Fatal("zero-match XPath retained field highlights")
	}
	edit("#visual-title-selector", ".//a/ancestor::body//h2")
	counts(2, 0)
	edit("#visual-title-selector", "//h2")
	must(chromedp.Poll(`document.querySelector('#selector-matches').dataset.error==='true'`, nil))
	edit("#visual-items", "count(//article)")
	must(chromedp.Poll(`document.querySelector('#selector-matches').dataset.error==='true'`, nil))
	edit("#visual-items", "//h9")
	counts(0, 0)
	edit("#visual-items", "//*[contains(concat(' ', normalize-space(@class), ' '), ' card ')]")
	counts(2, 2)
	edit("#visual-title-selector", ".//h2")
	counts(2, 2)
	// Choosing a field in the second matched card generates XPath and must not
	// switch the recipe back to CSS or erase the manual repeated-item selector.
	frame(chromedp.Evaluate(`document.querySelectorAll('#page h2')[1].click()`, nil))
	must(chromedp.Poll(`!document.querySelector('#selector-apply').disabled`, nil), chromedp.Click("#selector-apply"), chromedp.Poll(`document.querySelector('#visual-title-selector').value.startsWith('.')&&document.querySelector('#visual-type').value==='xpath'`, nil))
	counts(2, 2)
	must(chromedp.Evaluate(`document.querySelector('#visual-items').value.includes('normalize-space')&&document.querySelector('#visual-link-selector').value==='.//a/@href'`, &ok))
	if !ok {
		t.Fatal("visual XPath selection overwrote other manually tuned fields")
	}
	if got := fetcher.calls.Load(); got != before {
		t.Fatalf("typing/selecting fetched source: before=%d after=%d", before, got)
	}
	// Hold a reload response while editing to reproduce a slow source. Loading
	// must preserve the latest text, focused field, and an existing Auto recipe.
	must(chromedp.Evaluate(`(()=>{form.elements.mode.value='auto';const original=window.fetch;window.restoreSelectorFetch=()=>{window.fetch=original};window.fetch=async(...args)=>{const response=await original(...args);if(args[0]==='/api/selector/snapshot')await new Promise(resolve=>window.releaseSelectorSnapshot=resolve);return response}})()`, nil),
		chromedp.Click("#selector-load"), chromedp.Poll(`typeof window.releaseSelectorSnapshot==='function'`, nil))
	edit("#visual-title-selector", ".//h2/text()")
	must(chromedp.Evaluate(`window.restoreSelectorFetch();window.releaseSelectorSnapshot()`, nil), chromedp.Poll(`document.querySelector('#selector-status').textContent.startsWith('1.')`, nil))
	counts(2, 2)
	must(chromedp.Evaluate(`document.querySelector('#selector-field').value==='title'&&form.elements.mode.value==='auto'&&document.querySelector('#visual-title-selector').value==='.//h2/text()'`, &ok))
	if !ok {
		t.Fatal("loading replaced the active field, edited text, or Auto fetch mode")
	}
	captureTrial(t, tab, "selector-xpath-dark")
	must(chromedp.EmulateViewport(390, 844), chromedp.Evaluate(`document.querySelector('#visual-selector').scrollWidth<=document.querySelector('#visual-selector').clientWidth+1`, &ok))
	if !ok {
		t.Fatal("live selector mobile layout overflows")
	}
	captureTrial(t, tab, "selector-xpath-mobile")
	must(chromedp.EmulateViewport(1280, 900))
}
