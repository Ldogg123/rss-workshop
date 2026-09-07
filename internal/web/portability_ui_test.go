package web

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"rss-workshop/internal/store"
)

// Runs inside the fixture's already-authenticated, forwarded-origin browser.
// This exercises the real download and file chooser, not a hand-injected import.
func testPortabilityUI(t *testing.T, tab context.Context, s *store.Store) {
	t.Helper()
	must := func(actions ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(tab, actions...); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	must(chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpbrowser.SetDownloadBehavior(cdpbrowser.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(dir).Do(cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser))
	}), chromedp.Click("#export-recipes"))
	path := filepath.Join(dir, "rss-workshop-recipes-v1.json")
	var data []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		data, err = os.ReadFile(path)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var doc recipeDocument
	if err := json.Unmarshal(data, &doc); err != nil || len(doc.Feeds) != 1 {
		t.Fatal("browser export failed", err, string(data))
	}
	original, err := s.List(context.Background())
	if err != nil || len(original) != 1 {
		t.Fatal("missing original fixture", err)
	}
	// Rejected file must never enable the confirmation button.
	invalid := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte(`{"format":"unknown","version":1,"feeds":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	must(chromedp.Click("#import-recipes"), chromedp.WaitVisible("#recipe-import"), chromedp.SetUploadFiles("#import-file", []string{invalid}), chromedp.Poll(`document.querySelector('#import-status').classList.contains('diagnostic')`, nil))
	var disabled bool
	must(chromedp.Evaluate(`document.querySelector('#import-confirm').disabled`, &disabled))
	if !disabled {
		t.Fatal("invalid import enabled confirmation")
	}
	must(chromedp.SetUploadFiles("#import-file", []string{path}), chromedp.Poll(`document.querySelectorAll('#import-preview .import-recipe').length===1&&!document.querySelector('#import-confirm').disabled`, nil))
	var previewTitle string
	must(chromedp.Text("#import-preview h3", &previewTitle))
	if previewTitle != original[0].Title {
		t.Fatal("import review lost title", previewTitle)
	}
	// Review alone must not create a feed; include narrow-screen layout checking.
	current, _ := s.List(context.Background())
	if len(current) != 1 {
		t.Fatal("file selection imported before confirmation")
	}
	var overflow bool
	must(chromedp.EmulateViewport(390, 844), chromedp.Evaluate(`document.querySelector('#recipe-import').scrollWidth>document.querySelector('#recipe-import').clientWidth+1`, &overflow))
	if overflow {
		t.Fatal("import dialog overflows narrow screen")
	}
	captureTrial(t, tab, "import-dark")
	must(chromedp.Click("#import-confirm"), chromedp.Poll(`!document.querySelector('#recipe-import').open&&document.querySelectorAll('.feed-card').length===2`, nil))
	current, err = s.List(context.Background())
	if err != nil || len(current) != 2 {
		t.Fatal("import did not create copy", err)
	}
	for _, f := range current {
		if f.ID == original[0].ID {
			continue
		}
		if f.Enabled || f.RSSToken == original[0].RSSToken || !reflect.DeepEqual(f.Recipe, original[0].Recipe) || f.Count != 0 {
			t.Fatal("import did not preserve recipe as fresh paused feed")
		}
	}
	var links int
	must(chromedp.Evaluate(`document.querySelectorAll('.feed-card a[href$=".atom"]').length`, &links))
	if links != 2 {
		t.Fatal("Atom links missing", links)
	}
	must(chromedp.EmulateViewport(1280, 900))
}
