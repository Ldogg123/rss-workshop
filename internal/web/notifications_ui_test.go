package web

import (
	"context"
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// testNotificationsUI covers the dashboard's notification status and its test
// button, in the dark theme the fixture uses.
func testNotificationsUI(t *testing.T, tab context.Context, service *receiver) {
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
	awaitLoad := chromedp.Evaluate(`load()`, nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) })
	must(chromedp.Evaluate(`if(!document.querySelector('#editor').hidden)document.querySelector('#close-editor').click()`, nil), awaitLoad)
	check(`document.querySelector('#notifications').textContent==='Notifications: JSON webhook'&&!document.querySelector('#test-notification').hidden`,
		"the dashboard does not show that JSON notifications are on")
	check(`(()=>{const b=document.querySelector('#test-notification'),other=getComputedStyle(document.querySelector('#import-recipes')),style=getComputedStyle(b);
	 return document.documentElement.dataset.theme==='dark'&&b.title.includes('no feed data')&&style.color===other.color&&style.borderColor===other.borderColor&&style.backgroundColor===other.backgroundColor})()`,
		"the test button lacks its help or the dark theme's colors")

	before := len(service.received())
	must(chromedp.Evaluate(`document.querySelector('#test-notification').click()`, nil),
		chromedp.Poll(`document.querySelector('#notice').classList.contains('success')&&document.querySelector('#notice').textContent.startsWith('Test notification sent.')&&!document.querySelector('#test-notification').disabled`, nil))
	if got := len(service.received()) - before; got != 1 {
		t.Fatalf("the test button sent %d notifications", got)
	}
	must(chromedp.Evaluate(`document.querySelector('#test-notification').click()`, nil),
		chromedp.Poll(`!document.querySelector('#notice').hidden&&!document.querySelector('#notice').classList.contains('success')&&document.querySelector('#notice').textContent.includes('try again in')`, nil))
	if got := len(service.received()) - before; got != 1 {
		t.Fatalf("a second click within ten seconds sent %d notifications in all", got)
	}

	check(`(()=>{renderNotifications('off');return document.querySelector('#notifications').textContent==='Notifications: off'&&document.querySelector('#test-notification').hidden})()`,
		"with notifications off the test button is still offered")
	must(awaitLoad, chromedp.Evaluate(`notice('')`, nil))
	check(`!document.querySelector('#test-notification').hidden`, "the next load did not restore the button")
}
