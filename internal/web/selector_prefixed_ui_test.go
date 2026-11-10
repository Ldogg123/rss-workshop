package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"rss-workshop/internal/auth"
	"rss-workshop/internal/scheduler"
)

// A page with Word's <o:p> and a <fb:like> between its stories opens in the
// visual selector, shows nothing of those elements, and still counts them as
// siblings: the positional CSS and XPath the picker suggests for a story after
// them, and hand-written :nth-child, find that story in the frame and again
// when Preview items runs the Go extractor on the real page.
func TestBrowserUIPrefixedElements(t *testing.T) {
	if os.Getenv("RSS_BROWSER_TEST") != "1" {
		t.Skip("run in the browser-tests Docker target")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	a, err := auth.New("ui-test-password-123", "", false)
	if err != nil {
		t.Fatal(err)
	}
	jobs := scheduler.New(s, &fixtureFetcher{body: []byte(prefixedPage)}, 2, time.Second)
	jobs.Start(ctx)
	defer func() { cancel(); jobs.Wait() }()
	app := &App{Store: s, Scheduler: jobs, Auth: a}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	app.BaseURL = server.URL
	alloc, closeAlloc := chromedp.NewExecAllocator(context.Background(), chromedp.ExecPath(os.Getenv("CHROMIUM_PATH")), chromedp.Headless, chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck, chromedp.Flag("no-sandbox", false), chromedp.Flag("disable-background-networking", true))
	defer closeAlloc()
	tab, closeTab := chromedp.NewContext(alloc)
	defer closeTab()
	tab, timeout := context.WithTimeout(tab, 60*time.Second)
	defer timeout()
	var mu sync.Mutex
	exceptions := []string{}
	chromedp.ListenTarget(tab, func(ev any) {
		if e, ok := ev.(*runtime.EventExceptionThrown); ok {
			mu.Lock()
			exceptions = append(exceptions, e.ExceptionDetails.Text)
			mu.Unlock()
		}
	})
	must := func(actions ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(tab, actions...); err != nil {
			t.Fatal(err)
		}
	}
	quote := func(v string) string { b, _ := json.Marshal(v); return string(b) }
	must(chromedp.EmulateViewport(1280, 900), chromedp.Navigate(server.URL), chromedp.WaitVisible("#login-form"),
		chromedp.SendKeys("#password", "ui-test-password-123"), chromedp.Click("#login-form button"), chromedp.WaitVisible("#workspace"),
		chromedp.Click("#new-feed"), chromedp.WaitVisible("#editor"),
		chromedp.SendKeys(`#recipe-form input[name="title"]`, "Parish news"), chromedp.SendKeys(`#recipe-form input[name="url"]`, "https://example.com/news"),
		chromedp.Click("#visual-button"),
		// A refused page leaves its reason here and never creates the frame.
		chromedp.Poll(`!['','Loading page…','Opening selection preview…'].includes(document.querySelector('#selector-status').textContent)`, nil))
	var status string
	must(chromedp.Text("#selector-status", &status))
	if !strings.HasPrefix(status, "1.") {
		t.Fatalf("the page did not open in the visual selector: %s", status)
	}
	var iframeID target.ID
	must(chromedp.ActionFunc(func(ctx context.Context) error {
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
	}))
	if iframeID == "" {
		t.Fatal("isolated preview frame target missing")
	}
	preview, closePreview := chromedp.NewContext(tab, chromedp.WithTargetID(iframeID))
	defer closePreview()
	frame := func(expression, failure string) {
		t.Helper()
		var ok bool
		if err := chromedp.Run(preview, chromedp.Evaluate(expression, &ok)); err != nil || !ok {
			t.Fatal(failure, err)
		}
	}
	edit := func(id, value string) {
		t.Helper()
		must(chromedp.Evaluate(`(()=>{const el=document.querySelector(`+quote(id)+`);el.focus();el.value=`+quote(value)+`;el.dispatchEvent(new Event('input',{bubbles:true}));})()`, nil))
	}
	counts := func(items, count int) {
		t.Helper()
		want, _ := json.Marshal([]int{items, count})
		must(chromedp.Poll(`(()=>{const s=document.querySelector('#selector-matches');const want=`+string(want)+`;return s.dataset.error==='false'&&Number(s.dataset.items)===want[0]&&Number(s.dataset.count)===want[1]})()`, nil))
	}
	// Choose the story after both placeholders with the Repeated items field
	// active, and apply the one suggestion that is a positional path.
	pick := func(suffix string) string {
		t.Helper()
		// The frame answers once it has the field and the selector type.
		must(chromedp.SetValue("#selector-field", "items"), chromedp.Evaluate(`document.querySelector('#selector-field').dispatchEvent(new Event('change'))`, nil),
			chromedp.Poll(`'count' in document.querySelector('#selector-matches').dataset`, nil))
		frame(`(()=>{const a=[...document.querySelectorAll('#page .source-link')].find(a=>a.textContent==='Third story');if(a)a.click();return !!a})()`, "the story after the placeholders is not in the preview")
		var selector string
		must(chromedp.Poll(`document.querySelector('#selector-selected').textContent==='Selected: p.MsoNormal'&&!document.querySelector('#selector-apply').disabled`, nil),
			chromedp.Evaluate(`(()=>{const c=document.querySelector('#selector-choices'),o=[...c.options].filter(o=>o.value.endsWith(`+quote(suffix)+`));if(o.length!==1)return [...c.options].map(o=>o.value).join(' | ');c.value=o[0].value;c.dispatchEvent(new Event('change'));return c.value})()`, &selector))
		if !strings.HasSuffix(selector, suffix) {
			t.Fatalf("no single positional suggestion ending in %s: %s", suffix, selector)
		}
		must(chromedp.Click("#selector-apply"), chromedp.Poll(`document.querySelector('#recipe-form [name="items"]').value===`+quote(selector)+`&&document.querySelector('#selector-field').value==='title'`, nil))
		return selector
	}
	third := func(class, failure string) {
		t.Helper()
		frame(`(()=>{const m=[...document.querySelectorAll('#page `+class+`')];return m.length===1&&m[0].textContent==='Third story'})()`, failure)
	}

	frame(`(()=>{const text=document.querySelector('#page').textContent;return text.includes('First story')&&text.includes('Notes from  this week')&&!['Smart-tag-text','Like-text','Inner-text','likeAttack'].some(s=>document.documentElement.outerHTML.includes(s))&&![...document.querySelectorAll('*')].some(e=>e.localName.includes(':')||e.localName===`+quote(selectorPlaceholder)+`||e.id==='inside-like'||e.classList.contains('like-class'))})()`,
		"a prefixed element's name, attributes or content reached the visible preview")

	// Hand-written positions count the placeholders as the extractor will.
	edit("#visual-items", ".WordSection1 > p:nth-child(5)")
	counts(1, 1)
	third(".matched", ":nth-child did not highlight the story after the placeholders")
	edit("#visual-items", ".WordSection1 > p:nth-child(3), .WordSection1 > p:nth-child(4)")
	counts(1, 1)
	frame(`(()=>{const m=[...document.querySelectorAll('#page .matched')];return m.length===1&&m[0].textContent==='Second story'})()`, "a placeholder did not keep its own :nth-child position")
	// The placeholder itself is counted, since the real element will match,
	// and reported as not shown.
	edit("#visual-items", ".WordSection1 > :nth-child(4)")
	counts(1, 1)
	var badge string
	must(chromedp.Text("#selector-matches", &badge))
	if !strings.Contains(badge, "0 visible in this simplified preview") {
		t.Fatalf("a match on a placeholder was not reported as hidden: %s", badge)
	}

	css := pick(" > p:nth-of-type(3)")
	if want := "html:nth-of-type(1) > body:nth-of-type(1) > div:nth-of-type(1) > p:nth-of-type(3)"; css != want {
		t.Fatalf("positional CSS = %s, want %s", css, want)
	}
	edit("#visual-title-selector", "a")
	counts(1, 1)
	third(".field-matched", "the positional CSS path did not lead to the story's title")

	must(chromedp.SetValue("#visual-type", "xpath"), chromedp.Evaluate(`document.querySelector('#visual-type').dispatchEvent(new Event('change'))`, nil),
		chromedp.Poll(`document.querySelector('#recipe-form [name="type"]').value==='xpath'&&document.querySelector('#selector-choices-label').textContent==='Suggested XPath'`, nil))
	xpath := pick("/p[3]")
	if want := "//html[1]/body[1]/div[1]/p[3]"; xpath != want {
		t.Fatalf("positional XPath = %s, want %s", xpath, want)
	}
	edit("#visual-title-selector", ".//a")
	edit("#visual-link-selector", ".//a/@href")
	counts(1, 1)
	third(".field-matched", "the positional XPath did not lead to the story's link")
	edit("#visual-items", "//div[@class='WordSection1']/*[5]")
	counts(1, 1)
	third(".matched", "*[5] did not highlight the story after the placeholders")
	edit("#visual-items", xpath)
	counts(1, 1)

	// Preview items fetches the page again and extracts with the Go parser,
	// where <o:p> and <fb:like> are elements under their own names.
	extracted := func(recipe string) {
		t.Helper()
		var got string
		must(chromedp.Click("#preview-button"), chromedp.Poll(`(()=>{const text=document.querySelector('#preview').textContent.trim();return text!==''&&text!=='Fetching and extracting…'})()`, nil, chromedp.WithPollingTimeout(10*time.Second)),
			chromedp.Evaluate(`(()=>{const items=[...document.querySelectorAll('#preview .preview-item')];return items.length===1&&items[0].querySelector('a')?items[0].querySelector('a').textContent+' '+items[0].querySelector('a').href:items.length+' items: '+document.querySelector('#preview').textContent.slice(0,300)})()`, &got))
		if got != "Third story https://example.com/three" {
			t.Fatalf("%s extracted from the real page: %s", recipe, got)
		}
	}
	must(chromedp.Click("#selector-close"), chromedp.WaitNotVisible("#visual-selector"))
	extracted(xpath)
	must(chromedp.Evaluate(`(()=>{const set=(name,value)=>{const el=form.elements[name];el.value=value;el.dispatchEvent(new Event(el.localName==='select'?'change':'input',{bubbles:true}))};set('type','css');set('items',`+quote(css)+`);set('title_selector','a');set('link_selector','a');set('link_attr','');document.querySelector('#preview').replaceChildren()})()`, nil))
	extracted(css)

	mu.Lock()
	defer mu.Unlock()
	if len(exceptions) > 0 {
		t.Fatal("browser JavaScript errors", exceptions)
	}
}
