package web

// This opt-in trial uses public websites through the real fetcher. It is kept
// out of deterministic CI: layouts and remote availability change independently.
import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"rss-workshop/internal/auth"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/scheduler"
)

func TestVisualSites(t *testing.T) {
	if os.Getenv("RSS_VISUAL_SITES") != "1" {
		t.Skip("opt-in live-site trial")
	}
	sites := []struct{ name, url, click string }{
		{"BBC", "https://www.bbc.com/", "#page h2"},
		{"Guardian", "https://www.theguardian.com/international", "#page h3"},
		{"Ars", "https://arstechnica.com/", "#page h2"},
		{"HackerNews", "https://news.ycombinator.com/", "#page tr .source-link"},
		{"GoBlog", "https://go.dev/blog/", "#page .source-link"},
	}
	for _, site := range sites {
		t.Run(site.name, func(t *testing.T) {
			if filter := os.Getenv("RSS_SITE_FILTER"); filter != "" && !strings.Contains(filter, site.name) {
				t.Skip("filtered")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, err := openTestStore(t, 500)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			authn, err := auth.New("live-site-trial-password", "", false)
			if err != nil {
				t.Fatal(err)
			}
			client, err := fetch.New(30*time.Second, "")
			if err != nil {
				t.Fatal(err)
			}
			jobs := scheduler.New(s, client, 2, 30*time.Second)
			jobs.Start(ctx)
			defer func() { cancel(); jobs.Wait() }()
			app := &App{Store: s, Scheduler: jobs, Auth: authn}
			server := httptest.NewServer(app.Handler())
			defer server.Close()
			app.BaseURL = server.URL
			alloc, closeAlloc := chromedp.NewExecAllocator(context.Background(), chromedp.ExecPath(os.Getenv("CHROMIUM_PATH")), chromedp.Headless, chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck, chromedp.Flag("no-sandbox", false), chromedp.Flag("disable-background-networking", true))
			defer closeAlloc()
			tab, closeTab := chromedp.NewContext(alloc)
			defer closeTab()
			tab, timeout := context.WithTimeout(tab, 100*time.Second)
			defer timeout()
			must := func(actions ...chromedp.Action) {
				t.Helper()
				if err := chromedp.Run(tab, actions...); err != nil {
					t.Fatal(err)
				}
			}
			must(chromedp.EmulateViewport(1400, 1000), chromedp.Navigate(server.URL), chromedp.WaitVisible("#login-form"), chromedp.SendKeys("#password", "live-site-trial-password"), chromedp.Click("#login-form button"), chromedp.WaitVisible("#workspace"), chromedp.Click("#new-feed"), chromedp.SendKeys(`#recipe-form [name="title"]`, site.name+" visual trial"), chromedp.SendKeys(`#recipe-form [name="url"]`, site.url), chromedp.Click("#visual-button"), chromedp.Poll(`document.querySelector('#selector-status').textContent.startsWith('1.')||(!document.querySelector('#selector-load').disabled&&!document.querySelector('#selector-frame'))`, nil, chromedp.WithPollingTimeout(40*time.Second)))
			var status string
			must(chromedp.Text("#selector-status", &status))
			if !strings.HasPrefix(status, "1.") {
				t.Fatal(status)
			}
			var frameID target.ID
			must(chromedp.ActionFunc(func(ctx context.Context) error {
				infos, err := target.GetTargets().Do(ctx)
				for _, info := range infos {
					if info.Type == "iframe" && strings.HasSuffix(info.URL, "/selector/frame") {
						frameID = info.TargetID
					}
				}
				return err
			}))
			if frameID == "" {
				t.Fatal("missing picker frame")
			}
			frame, closeFrame := chromedp.NewContext(tab, chromedp.WithTargetID(frameID))
			defer closeFrame()
			fm := func(actions ...chromedp.Action) {
				t.Helper()
				if err := chromedp.Run(frame, actions...); err != nil {
					t.Fatal(err)
				}
			}
			// Choose a visible headline, recording its data-node solely to click the
			// same visible element later. No source selectors are written into the recipe.
			headlineJS := `document.querySelector('h2')`
			switch site.name {
			case "Guardian":
				headlineJS = `document.querySelector('h3')`
			case "HackerNews":
				headlineJS = `[...document.querySelectorAll('tr')].find(r=>r.children[0]?.textContent.trim()==='1.')?.children[2]?.querySelector('.source-link')`
			case "GoBlog":
				headlineJS = `[...document.querySelectorAll('.source-link')].find(a=>a.textContent.includes('Go 1.'))`
			}
			var clicked string
			fm(chromedp.Evaluate(`(()=>{const n=`+headlineJS+`;if(!n)throw Error('headline missing');n.scrollIntoView({block:'center'});n.click();return n.dataset.node})()`, &clicked))
			must(chromedp.Poll(`!document.querySelector('#selector-apply').disabled`, nil))
			var selection any
			must(chromedp.Evaluate(`({label:document.querySelector('#selector-selected').textContent,choices:[...document.querySelector('#selector-choices').options].map(o=>o.textContent)})`, &selection))
			t.Logf("INITIAL %s: %+v", site.name, selection)
			if os.Getenv("RSS_SITE_INSPECT") == "1" {
				for i := 0; i < 6; i++ {
					must(chromedp.Click("#selector-parent"), chromedp.Sleep(30*time.Millisecond), chromedp.Evaluate(`({label:document.querySelector('#selector-selected').textContent,choices:[...document.querySelector('#selector-choices').options].slice(0,3).map(o=>o.textContent)})`, &selection))
					t.Logf("PARENT %d: %+v", i+1, selection)
				}
				captureTrial(t, tab, site.name+"-before")
				return
			}
			must(chromedp.Click("#selector-apply"), chromedp.Poll(`document.querySelector('#selector-field').value==='title'`, nil))
			fm(chromedp.Evaluate(`document.querySelector('[data-node="`+clicked+`"]').click()`, nil))
			must(chromedp.Poll(`!document.querySelector('#selector-apply').disabled`, nil), chromedp.Click("#selector-apply"), chromedp.Poll(`document.querySelector('#recipe-form [name="title_selector"]').value!==''`, nil))
			must(chromedp.SetValue("#selector-field", "link"), chromedp.Evaluate(`document.querySelector('#selector-field').dispatchEvent(new Event('change'))`, nil))
			fm(chromedp.Evaluate(`document.querySelector('[data-node="`+clicked+`"]').click()`, nil))
			must(chromedp.Poll(`!document.querySelector('#selector-apply').disabled`, nil), chromedp.Click("#selector-apply"), chromedp.Poll(`document.querySelector('#recipe-form [name="link_selector"]').value!==''`, nil))
			captureTrial(t, tab, site.name)
			must(chromedp.Click("#selector-close"), chromedp.Click("#preview-button"), chromedp.Poll(`!document.querySelector('#preview-button').disabled`, nil, chromedp.WithPollingTimeout(40*time.Second)))
			var count int
			must(chromedp.Evaluate(`document.querySelectorAll('#preview .preview-item').length`, &count))
			if count < 2 {
				must(chromedp.Text("#notice", &status))
				t.Fatalf("only %d items: %s", count, status)
			}
			var recipe any
			must(chromedp.Evaluate(`readForm().recipe`, &recipe))
			must(chromedp.Click(`#recipe-form button[type="submit"]`), chromedp.WaitVisible(".feed-card"))
			deadline := time.Now().Add(35 * time.Second)
			var rss string
			for time.Now().Before(deadline) {
				feeds, err := s.List(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(feeds) == 1 && !feeds[0].LastSuccess.IsZero() {
					rss = "/feeds/" + feeds[0].RSSToken + ".xml"
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if rss == "" {
				t.Fatal("saved feed did not refresh")
			}
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, httptest.NewRequest("GET", rss, nil))
			var parsed struct {
				Channel struct {
					Items []struct {
						Title string `xml:"title"`
						Link  string `xml:"link"`
						GUID  string `xml:"guid"`
					} `xml:"item"`
				} `xml:"channel"`
			}
			if err := xml.Unmarshal(response.Body.Bytes(), &parsed); err != nil {
				t.Fatal(err)
			}
			if len(parsed.Channel.Items) < 2 {
				t.Fatal("saved RSS has fewer than two items")
			}
			for _, item := range parsed.Channel.Items {
				if strings.TrimSpace(item.Title) == "" || !strings.HasPrefix(item.Link, "http") || item.GUID == "" {
					t.Fatal("RSS item missing title/link/GUID")
				}
			}
			result := map[string]any{"site": site.name, "url": site.url, "preview_items": count, "rss_items": len(parsed.Channel.Items), "recipe": recipe}
			data, _ := json.MarshalIndent(result, "", "  ")
			t.Logf("RESULT %s", data)
			if dir := os.Getenv("RSS_SITE_ARTIFACTS"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, site.name+".json"), data, 0644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func captureTrial(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	if dir := os.Getenv("RSS_SITE_ARTIFACTS"); dir != "" {
		var png []byte
		if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&png)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s.png", name)), png, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
