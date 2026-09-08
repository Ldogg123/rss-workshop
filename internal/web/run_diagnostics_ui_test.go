package web

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
	"rss-workshop/internal/store"
)

// Seed real history in an isolated paused feed, then exercise its authenticated
// API through the same forwarded-origin browser as the other UI workflows.
func testRunDiagnosticsUI(t *testing.T, tab context.Context, s *store.Store, jobs *scheduler.Scheduler, fetcher *fixtureFetcher, solver *fixtureSolver) {
	t.Helper()
	ctx := context.Background()
	// The earlier save schedules the original fixture's first refresh. Finish
	// that independent work, then pause it before measuring history-only reads.
	deadline := time.Now().Add(5 * time.Second)
	for {
		existing, err := s.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		pending := false
		for _, f := range existing {
			if !f.Enabled {
				continue
			}
			if f.LastSuccess.IsZero() {
				pending = true
				continue
			}
			f.Enabled = false
			if _, err := s.Save(ctx, f); err != nil {
				t.Fatal(err)
			}
		}
		active, _ := jobs.Stats()
		if !pending && active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("earlier fixture refresh did not finish before history-only checks")
		}
		time.Sleep(10 * time.Millisecond)
	}
	feed := model.Feed{Title: "Diagnostics fixture", URL: "https://example.com/diagnostics", Interval: 3600,
		Recipe: model.Recipe{Mode: "static", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}}
	id, err := s.Save(ctx, feed)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Delete(ctx, id)
	feed, err = s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	empty := feed
	empty.ID = ""
	empty.Title = "No history fixture"
	emptyID, err := s.Save(ctx, empty)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Delete(ctx, emptyID)
	if err := s.Complete(ctx, feed, nil, "", "", 304, nil, 0); err != nil {
		t.Fatal(err)
	}
	matches, count, zero := 2, 1, 0
	details := &model.RunDiagnostics{Version: 1, RecipeVersion: feed.Version, RequestedMode: "static", SelectorType: "css", Started: time.Now().UTC(), DurationMS: 25,
		Attempts: []model.FetchAttempt{{Mode: "static", Outcome: "success", Stage: "extract", Status: 200, DurationMS: 25, Bytes: 1234, Matches: &matches, Items: &count}}}
	if err := s.CompleteWithDiagnostics(ctx, feed, []model.Item{{Key: "saved", Title: "Saved fixture item"}}, "", "", 200, nil, 0, details); err != nil {
		t.Fatal(err)
	}
	attack := `<img id="run-diagnostic-injection" src=x onerror="document.body.dataset.runAttack=1">`
	details.RequestedMode = "auto"
	details.DurationMS = 60
	details.Attempts = []model.FetchAttempt{
		{Mode: "static", Outcome: "failed", Stage: "extract", Status: 200, DurationMS: 25, Bytes: 1234, Matches: &matches, Items: &zero, Error: "No valid items", Warnings: []string{attack}, WarningsOmitted: 3},
		{Mode: "browser", Outcome: "failed", Stage: "fetch", Status: 403, DurationMS: 35, Error: "Source denied the request"},
	}
	if err := s.CompleteWithDiagnostics(ctx, feed, nil, "", "", 403, errors.New(attack), 0, details); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs(ctx, id)
	if err != nil || len(runs) != 3 {
		t.Fatal("could not seed diagnostic history", err)
	}
	must := func(actions ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(tab, actions...); err != nil {
			t.Fatal(err)
		}
	}
	button := `.feed-diagnostics[data-feed-id="` + id + `"]`
	emptyButton := `.feed-diagnostics[data-feed-id="` + emptyID + `"]`
	var correct bool
	must(chromedp.Evaluate(`load()`, nil), chromedp.WaitVisible(button))
	staticBefore, solverBefore := fetcher.calls.Load(), solver.calls.Load()
	must(chromedp.Click(button), chromedp.WaitVisible("#feed-diagnostics"),
		chromedp.Poll(`document.querySelectorAll('#diagnostics-runs .run-entry').length===3&&!document.querySelector('#diagnostics-reload').disabled`, nil),
		chromedp.Evaluate(`(()=>{
	 const entries=[...document.querySelectorAll('#diagnostics-runs .run-entry')];
	 return entries[0].dataset.runId==='`+strconv.FormatInt(runs[0].ID, 10)+`'&&
	  entries[0].querySelector('summary').textContent.includes('Failed')&&entries[1].querySelector('summary').textContent.includes('Completed')&&
	  entries[2].querySelector('summary').textContent.includes('Unchanged')&&entries[2].textContent.includes('older run')&&
	  entries[0].querySelectorAll('.run-attempt').length===2&&entries[0].textContent.includes('HTTP 403')&&
	  entries[0].textContent.includes('Matched elements2')&&entries[0].textContent.includes('3 more warnings')&&
	  entries[0].querySelector('.run-warning').textContent.startsWith('<img id="run-diagnostic-injection"')&&
	  [...document.querySelectorAll('#diagnostics-runs .run-warning,#diagnostics-runs .run-error')].every(e=>e.childElementCount===0)&&
	  !document.querySelector('#run-diagnostic-injection')&&!document.body.dataset.runAttack;
	})()`, &correct))
	if !correct {
		t.Fatal("run history lost order, attempt details, legacy summaries, or safe text rendering")
	}
	must(chromedp.Click("#diagnostics-runs .run-entry summary"), chromedp.EmulateViewport(390, 844),
		chromedp.Evaluate(`(()=>{const d=document.querySelector('#feed-diagnostics');return d.scrollWidth<=d.clientWidth+1&&getComputedStyle(d).backgroundColor==='rgb(25, 36, 56)'})()`, &correct))
	if !correct {
		t.Fatal("diagnostics modal lost dark mode or overflows a narrow screen")
	}
	captureTrial(t, tab, "run-diagnostics-dark-mobile")
	must(chromedp.EmulateViewport(1280, 900), chromedp.Click("#diagnostics-reload"),
		chromedp.Poll(`document.querySelectorAll('#diagnostics-runs .run-entry').length===3&&!document.querySelector('#diagnostics-reload').disabled`, nil),
		chromedp.Click("#diagnostics-close"), chromedp.Poll(`document.activeElement.dataset.feedId==='`+id+`'`, nil), chromedp.Click(emptyButton),
		chromedp.Poll(`document.querySelector('#diagnostics-status').textContent.startsWith('No refresh runs yet')`, nil))
	// Hold a real response, close the dialog, and open another feed before it
	// arrives. Even a transport that ignores cancellation must not restore it.
	must(chromedp.Click("#diagnostics-close"), chromedp.Evaluate(`(()=>{
	 const original=window.fetch;window.restoreRunFetch=()=>{window.fetch=original};
	 window.fetch=async(...args)=>{const response=await original(...args);if(args[0]==='/api/feeds/`+id+`/runs')await new Promise(resolve=>window.releaseRunResponse=resolve);return response};
	})()`, nil), chromedp.Click(button), chromedp.Poll(`typeof window.releaseRunResponse==='function'`, nil),
		chromedp.Click("#diagnostics-close"), chromedp.Click(emptyButton),
		chromedp.Poll(`document.querySelector('#diagnostics-status').textContent.startsWith('No refresh runs yet')`, nil),
		chromedp.Evaluate(`window.restoreRunFetch();window.releaseRunResponse();delete window.restoreRunFetch;delete window.releaseRunResponse`, nil),
		chromedp.Poll(`document.querySelector('#diagnostics-feed').textContent==='No history fixture'&&document.querySelectorAll('#diagnostics-runs .run-entry').length===0`, nil))
	if err := s.Delete(ctx, emptyID); err != nil {
		t.Fatal(err)
	}
	must(chromedp.Evaluate(`load()`, nil), chromedp.Poll(`!document.querySelector('#feed-diagnostics').open&&document.querySelector('#diagnostics-feed').textContent===''`, nil))
	// Expire the actual server session while the modal is open. Its next read
	// must return to sign-in and discard history, including preview traces.
	must(chromedp.Click(button), chromedp.Poll(`document.querySelectorAll('#diagnostics-runs .run-entry').length===3`, nil),
		chromedp.Evaluate(`api('/logout','POST',{}).then(()=>document.querySelector('#diagnostics-reload').click())`, nil),
		chromedp.Poll(`!document.querySelector('#login-panel').hidden&&!document.querySelector('#feed-diagnostics').open&&document.querySelector('#diagnostics-runs').childElementCount===0&&!document.querySelector('.preview-diagnostics')`, nil))
	if fetcher.calls.Load() != staticBefore || solver.calls.Load() != solverBefore {
		t.Fatal("reading or reloading diagnostics fetched a source page")
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	must(chromedp.SendKeys("#password", "ui-test-password-123"), chromedp.Click("#login-form button"), chromedp.WaitVisible("#workspace"))
}
