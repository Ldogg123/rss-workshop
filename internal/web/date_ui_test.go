package web

import (
	"context"
	"testing"

	"github.com/chromedp/chromedp"
)

func testEstimatedDatePreview(t *testing.T, tab context.Context) {
	t.Helper()
	var correct bool
	err := chromedp.Run(tab,
		chromedp.SetValue(`#recipe-form [name="date_attr"]`, "data-age"), chromedp.Click("#preview-button"),
		chromedp.Poll(`document.querySelectorAll('#preview .estimated-date').length===2&&!!document.querySelector('#preview-date-note')`, nil),
		chromedp.Evaluate(`(()=>{const estimates=[...document.querySelectorAll('#preview .estimated-date')];return estimates.every(e=>e.querySelector('.estimate-label').textContent==='Estimated date')&&estimates[0].querySelector('.estimate-source').textContent.includes('2 minutes ago')&&estimates[1].querySelector('.estimate-source').textContent.includes('1 month ago')})()`, &correct),
	)
	if err != nil || !correct {
		t.Fatal("relative date preview labels missing", correct, err)
	}
	if err := chromedp.Run(tab, chromedp.ScrollIntoView("#preview .preview-item:last-child")); err != nil {
		t.Fatal(err)
	}
	captureTrial(t, tab, "relative-dates-preview")
	// A blank attribute uses the exact datetime on the selected time element.
	// Returning to exact dates must also remove old estimate labels and notes.
	err = chromedp.Run(tab,
		chromedp.Evaluate(`(()=>{const input=document.querySelector('#recipe-form [name="date_attr"]');input.value='';input.dispatchEvent(new Event('input',{bubbles:true}));})()`, nil), chromedp.Click("#preview-button"),
		chromedp.Poll(`!document.querySelector('#preview-button').disabled&&document.querySelectorAll('#preview .preview-item').length===2`, nil),
		chromedp.Evaluate(`!document.querySelector('#preview .estimated-date,#preview-date-note')`, &correct),
	)
	if err != nil || !correct {
		t.Fatal("exact datetime retained estimate labels", correct, err)
	}
}
