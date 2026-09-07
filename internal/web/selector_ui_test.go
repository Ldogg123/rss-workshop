package web

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func testVisualSelector(t *testing.T, tab context.Context, card string, fetcher *fixtureFetcher) {
	t.Helper()
	if err := chromedp.Run(tab, chromedp.Click("#visual-button"), chromedp.WaitVisible("#selector-frame"), chromedp.Poll(`document.querySelector('#selector-status').textContent.startsWith('1.')`, nil)); err != nil {
		t.Fatal("visual selector did not load", err)
	}
	var background string
	if err := chromedp.Run(tab, chromedp.Evaluate(`getComputedStyle(document.querySelector('#visual-selector')).backgroundColor`, &background)); err != nil || background != "rgb(25, 36, 56)" {
		t.Fatal("picker did not adopt dark surface", background, err)
	}
	var iframeID target.ID
	if err := chromedp.Run(tab, chromedp.ActionFunc(func(ctx context.Context) error {
		infos, err := target.GetTargets().Do(ctx)
		if err != nil {
			return err
		}
		for _, info := range infos {
			if info.Type == "iframe" && strings.HasSuffix(info.URL, "/selector/frame") {
				iframeID = info.TargetID
			}
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	if iframeID == "" {
		t.Fatal("isolated preview frame target missing")
	}
	preview, closePreview := chromedp.NewContext(tab, chromedp.WithTargetID(iframeID))
	defer closePreview()
	var safe bool
	if err := chromedp.Run(preview, chromedp.Evaluate(`(()=>{let isolated=false;try{void parent.document.body}catch{isolated=true}return isolated && document.documentElement.dataset.theme==='dark' && document.querySelectorAll('script').length===1 && !document.querySelector('iframe,img,form,object,svg,[onclick],[onerror]')})()`, &safe)); err != nil || !safe {
		t.Fatal("snapshot isolation failed", safe, err)
	}
	// A message from another window must not write the recipe, even with the
	// expected opaque-origin string and plausible selector contents.
	if err := chromedp.Run(tab, chromedp.Evaluate(`window.dispatchEvent(new MessageEvent('message',{source:window,origin:'null',data:{type:'committed',field:'items',selector:'body',token:'fake'}}))`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(tab, chromedp.Evaluate(`window.addEventListener('message',e=>{if(e.data?.label==='forged')window.selectorProbeReceived=true})`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(preview, chromedp.Evaluate(`parent.postMessage({type:'selection',token:'wrong',field:'items',label:'forged',choices:[{selector:'body',count:1}]},new URL(document.URL).origin)`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(tab, chromedp.Poll(`window.selectorProbeReceived===true`, nil), chromedp.Evaluate(`document.querySelector('#selector-selected').textContent==='Click an element in the page preview.'`, &safe)); err != nil || !safe {
		t.Fatal("forged bridge message accepted", safe, err)
	}
	if err := chromedp.Run(preview, chromedp.Click("#page h2", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(tab,
		chromedp.Poll(`document.querySelector('#selector-selected').textContent==='Selected: `+card+`'`, nil),
		chromedp.Evaluate(`const level=document.querySelector('#selector-level');level.selectedIndex=Math.max(0,level.selectedIndex-1);level.dispatchEvent(new Event('change'))`, nil),
		chromedp.Poll(`document.querySelector('#selector-selected').textContent!=='Selected: `+card+`'`, nil),
		chromedp.Click("#selector-parent"),
		chromedp.Poll(`document.querySelector('#selector-selected').textContent==='Selected: `+card+`'`, nil),
		chromedp.Click("#selector-apply"),
		chromedp.Poll(`document.querySelector('#recipe-form [name="items"]').value!=='' && document.querySelector('#selector-field').value==='title'`, nil),
	); err != nil {
		t.Fatal("could not choose repeating card", err)
	}
	for _, v := range []struct{ field, click, selector, attr string }{{"title", "h2", "h2", ""}, {"link", ".source-link", "a", "href"}, {"content", "p", "p", ""}, {"image", ".image-placeholder", "img", ""}, {"date", "time", "time", "datetime"}} {
		if err := chromedp.Run(tab, chromedp.SetValue("#selector-field", v.field), chromedp.Evaluate(`document.querySelector('#selector-field').dispatchEvent(new Event('change'))`, nil)); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Run(preview, chromedp.Click("#page "+v.click, chromedp.ByQuery)); err != nil {
			t.Fatal("click field", v.field, err)
		}
		if err := chromedp.Run(tab,
			chromedp.Poll(`!document.querySelector('#selector-apply').disabled`, nil), chromedp.Click("#selector-apply"),
			chromedp.Poll(`document.querySelector('#recipe-form [name="`+v.field+`_selector"]').value===`+`"`+v.selector+`"`, nil),
		); err != nil {
			t.Fatal("field assignment", v.field, err)
		}
		var attr string
		if err := chromedp.Run(tab, chromedp.Value(`#recipe-form [name="`+v.field+`_attr"]`, &attr)); err != nil || attr != v.attr {
			t.Fatal("attribute inference", v.field, attr, err)
		}
	}
	testLiveSelector(t, tab, preview, fetcher)
	var overflow bool
	if path := os.Getenv("RSS_UI_SCREENSHOT"); path != "" {
		var png []byte
		if err := chromedp.Run(tab, chromedp.CaptureScreenshot(&png)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, png, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := chromedp.Run(tab, chromedp.EmulateViewport(390, 844), chromedp.Evaluate(`document.querySelector('#visual-selector').scrollWidth>document.querySelector('#visual-selector').clientWidth+1`, &overflow)); err != nil || overflow {
		t.Fatal("selector mobile overflow", overflow, err)
	}
	if err := chromedp.Run(tab, chromedp.EmulateViewport(1280, 900), chromedp.Click("#selector-close"), chromedp.Click("#preview-button"), chromedp.Poll(`document.querySelectorAll('#preview .preview-item').length===2`, nil, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Fatal("visual recipe extraction failed", err)
	}
	var result bool
	if err := chromedp.Run(tab, chromedp.Evaluate(`(()=>{const p=document.querySelector('#preview .preview-item');return p.querySelector('a').textContent==='First story'&&p.querySelector('a').href==='https://example.com/one'&&p.textContent.includes('First description')&&p.querySelector('img').src==='https://example.com/snapshot-attack'&&!document.body.dataset.attacked})()`, &result)); err != nil || !result {
		t.Fatal("visual recipe changed extraction results", result, err)
	}
}
