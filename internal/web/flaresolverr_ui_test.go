package web

import (
	"context"
	"testing"

	"github.com/chromedp/chromedp"
)

func testFlareSolverrSelector(t *testing.T, tab context.Context, static *fixtureFetcher, solver *fixtureSolver) {
	t.Helper()
	staticBefore, solverBefore := static.calls.Load(), solver.calls.Load()
	var correct bool
	err := chromedp.Run(tab,
		chromedp.Evaluate(`(()=>{const mode=document.querySelector('#recipe-form [name="mode"]');mode.value='flaresolverr';mode.dispatchEvent(new Event('change',{bubbles:true}))})()`, nil),
		chromedp.Click("#visual-button"), chromedp.WaitVisible("#selector-frame"),
		chromedp.Poll(`document.querySelector('#selector-status').textContent.startsWith('1.')&&document.querySelector('#selector-matches').dataset.items==='2'`, nil),
		chromedp.Evaluate(`document.querySelector('#selector-mode').value==='flaresolverr'&&!document.querySelector('#selector-mode option[value="flaresolverr"]').disabled&&document.querySelector('#selector-mode option[value="browser"]').disabled`, &correct),
	)
	if err != nil || !correct {
		t.Fatal("FlareSolverr visual page mode unavailable without local Chromium", correct, err)
	}
	// Editing the existing XPath must keep the snapshot's fetch mode in the
	// saved recipe, just as committing a visual selection does.
	err = chromedp.Run(tab,
		chromedp.Evaluate(`(()=>{const title=document.querySelector('#visual-title-selector');title.focus();title.dispatchEvent(new Event('input',{bubbles:true}))})()`, nil),
		chromedp.Poll(`document.querySelector('#selector-matches').dataset.count==='2'`, nil),
		chromedp.Evaluate(`document.querySelector('#recipe-form [name="mode"]').value==='flaresolverr'`, &correct),
	)
	if err != nil || !correct {
		t.Fatal("fine tuning changed the FlareSolverr recipe mode", correct, err)
	}
	captureTrial(t, tab, "flaresolverr-selector")
	err = chromedp.Run(tab, chromedp.Click("#selector-close"), chromedp.Click("#preview-button"),
		chromedp.Poll(`!document.querySelector('#preview-button').disabled&&document.querySelectorAll('#preview .preview-item').length===2`, nil))
	if err != nil {
		t.Fatal("FlareSolverr item preview failed", err)
	}
	if solver.calls.Load()-solverBefore != 2 || static.calls.Load() != staticBefore {
		t.Fatalf("visual snapshot/preview used incorrect fetch path: solver=%d static=%d", solver.calls.Load()-solverBefore, static.calls.Load()-staticBefore)
	}
}

func assertFlareSolverrEditMode(t *testing.T, tab context.Context) {
	t.Helper()
	var mode string
	if err := chromedp.Run(tab, chromedp.Value(`#recipe-form [name="mode"]`, &mode)); err != nil || mode != "flaresolverr" {
		t.Fatal("saving and reopening lost FlareSolverr mode", mode, err)
	}
}
