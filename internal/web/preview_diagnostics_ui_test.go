package web

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func testPreviewDiagnostics(t *testing.T, tab context.Context) {
	t.Helper()
	listen, cancel := context.WithCancel(tab)
	defer cancel()
	var status atomic.Int32
	chromedp.ListenTarget(listen, func(event any) {
		if response, ok := event.(*network.EventResponseReceived); ok && strings.HasSuffix(response.Response.URL, "/api/preview") {
			status.Store(int32(response.Response.Status))
		}
	})
	must := func(actions ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(tab, actions...); err != nil {
			t.Fatal(err)
		}
	}
	var correct bool
	must(chromedp.Evaluate(`(()=>{
	 const names=['type','items','title_selector','link_selector','link_attr'];
	 window.previewDiagnosticFields=Object.fromEntries(names.map(name=>[name,form.elements[name].value]));
	 form.elements.type.value='xpath';
	 form.elements.items.value='//article[@class="card"]';
	 form.elements.title_selector.value='.//h9';
	 form.elements.link_selector.value='.//a';
	 form.elements.link_attr.value='href';
	})()`, nil), chromedp.Click("#preview-button"),
		chromedp.Poll(`!document.querySelector('#preview-button').disabled&&document.querySelectorAll('#preview .diagnostic').length===2`, nil),
		chromedp.Evaluate(`(()=>{
	 const p=document.querySelector('#preview'),n=document.querySelector('#notice');
	 return p.querySelector('h2').textContent==='0 items · 2 matches'&&
	  !p.querySelector('.preview-diagnostics').open&&p.querySelector('.preview-diagnostics').textContent.includes('Matching failed')&&
	  p.querySelector('.run-metrics').textContent.includes('XPath')&&
	  [...p.querySelectorAll('.diagnostic')].every((d,i)=>d.textContent.startsWith('Match '+(i+1)+' skipped: empty title'))&&
	  !p.querySelector('.preview-item')&&!n.hidden&&n.textContent.includes('no valid items')&&
	  !document.querySelector('#workspace').hidden&&document.querySelector('#login-panel').hidden;
	})()`, &correct))
	if status.Load() != 422 || !correct {
		t.Fatalf("empty-title preview lost HTTP 422 diagnostics or session state: status=%d correct=%v", status.Load(), correct)
	}

	// Keep the real endpoint's rejection reasons, adding an HTML-shaped warning
	// at the response boundary to prove diagnostics are rendered only as text.
	must(chromedp.Evaluate(`(()=>{
	 form.elements.title_selector.value='.//h2';
	 form.elements.link_selector.value='.//h2';
	 const original=window.fetch;
	 window.restoreDiagnosticFetch=()=>{window.fetch=original};
	 window.fetch=async(...args)=>{
	  const response=await original(...args);
	  if(args[0]!=='/api/preview'||response.status!==422)return response;
	  const body=await response.json();
	  body.warnings.push('<img id="diagnostic-injection" src=x onerror="document.body.dataset.diagnosticAttack=1">',{html:'ignored'});
	  return new Response(JSON.stringify(body),{status:response.status,headers:{'Content-Type':'application/json'}});
	 };
	})()`, nil), chromedp.Click("#preview-button"),
		chromedp.Poll(`!document.querySelector('#preview-button').disabled&&document.querySelectorAll('#preview .diagnostic').length===3`, nil),
		chromedp.Evaluate(`(()=>{
	 const p=document.querySelector('#preview'),warnings=[...p.querySelectorAll('.diagnostic')];
	 return p.querySelector('h2').textContent==='0 items · 2 matches'&&
	  warnings.slice(0,2).every((d,i)=>d.textContent.startsWith('Match '+(i+1)+' skipped: missing or unsafe item URL'))&&
	  warnings[2].textContent.startsWith('<img id="diagnostic-injection"')&&
	  warnings.every(d=>d.childElementCount===0)&&!p.textContent.includes('empty title')&&
	  !document.querySelector('#diagnostic-injection')&&!document.body.dataset.diagnosticAttack;
	})()`, &correct))
	if status.Load() != 422 || !correct {
		t.Fatalf("non-anchor preview diagnostics missing, stale, or interpreted as HTML: status=%d correct=%v", status.Load(), correct)
	}

	must(chromedp.Evaluate(`(()=>{
	 window.restoreDiagnosticFetch();delete window.restoreDiagnosticFetch;
	 for(const [name,value] of Object.entries(window.previewDiagnosticFields))form.elements[name].value=value;
	 delete window.previewDiagnosticFields;
	 form.elements.type.dispatchEvent(new Event('change'));
	})()`, nil), chromedp.Click("#preview-button"),
		chromedp.Poll(`!document.querySelector('#preview-button').disabled&&document.querySelectorAll('#preview .preview-item').length===2`, nil),
		chromedp.Evaluate(`(()=>{
	 const p=document.querySelector('#preview'),n=document.querySelector('#notice');
	 return p.querySelector('h2').textContent==='2 items · 2 matches'&&!p.querySelector('.diagnostic')&&
	  !p.querySelector('.preview-diagnostics').open&&p.querySelector('.preview-diagnostics').textContent.includes('Extraction completed')&&
	  !p.textContent.includes('Preview could not be completed')&&n.hidden&&n.textContent===''&&
	  !document.querySelector('#workspace').hidden&&!document.body.dataset.diagnosticAttack;
	})()`, &correct))
	if status.Load() != 200 || !correct {
		t.Fatalf("successful retry retained failed-preview diagnostics: status=%d correct=%v", status.Load(), correct)
	}

	// Optional Date fields keep otherwise valid stories while explaining the
	// received value (or absence) in both the preview and its detailed trace.
	must(chromedp.Evaluate(`(()=>{
	 window.previewDateFields={selector:form.elements.date_selector.value,attr:form.elements.date_attr.value};
	 form.elements.date_selector.value='h2';form.elements.date_attr.value='';
	})()`, nil), chromedp.Click("#preview-button"),
		chromedp.Poll(`!document.querySelector('#preview-button').disabled&&document.querySelectorAll('#preview > .diagnostic').length===2`, nil),
		chromedp.Evaluate(`(()=>{
	 const p=document.querySelector('#preview'),warnings=[...p.querySelectorAll(':scope > .diagnostic')];
	 return p.querySelectorAll('.preview-item').length===2&&warnings[0].textContent.includes('Date expected a date or relative age but received "First story"')&&
	  warnings[1].textContent.includes('received "Second story"')&&p.querySelector('.run-warning').textContent===warnings[0].textContent;
	})()`, &correct))
	if status.Load() != 200 || !correct {
		t.Fatal("date mismatch did not show its expected and actual values without rejecting stories")
	}
	must(chromedp.SetValue(`#recipe-form [name="date_selector"]`, ".missing-date"), chromedp.Click("#preview-button"),
		chromedp.Poll(`!document.querySelector('#preview-button').disabled&&document.querySelector('#preview > .diagnostic')?.textContent.includes('nothing (selector matched no nodes)')`, nil),
		chromedp.Evaluate(`(()=>{
	 const p=document.querySelector('#preview'),warnings=[...p.querySelectorAll(':scope > .diagnostic')];
	 return p.querySelectorAll('.preview-item').length===2&&warnings.length===2&&warnings.every(w=>w.textContent.includes('Date expected')&&w.textContent.includes('received nothing (selector matched no nodes)'))&&
	  p.querySelector('.run-warning').textContent===warnings[0].textContent;
	})()`, &correct))
	if status.Load() != 200 || !correct {
		t.Fatal("missing Date selector did not show the received-nothing explanation")
	}
	must(chromedp.Evaluate(`(()=>{
	 form.elements.date_selector.value=window.previewDateFields.selector;form.elements.date_attr.value=window.previewDateFields.attr;delete window.previewDateFields;
	})()`, nil), chromedp.Click("#preview-button"),
		chromedp.Poll(`!document.querySelector('#preview-button').disabled&&document.querySelectorAll('#preview .preview-item').length===2&&!document.querySelector('#preview > .diagnostic,#preview .run-warning')`, nil))
}
