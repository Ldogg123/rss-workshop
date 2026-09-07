package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"rss-workshop/internal/fetch"
)

type Stats struct {
	Active   int  `json:"active"`
	Capacity int  `json:"capacity"`
	Restarts int  `json:"restarts"`
	Ready    bool `json:"ready"`
}
type Pool struct {
	path                        string
	timeout                     time.Duration
	slots                       chan struct{}
	proxy                       *proxy
	mu                          sync.Mutex
	root                        context.Context
	stop                        context.CancelFunc
	active, completed, restarts int
	closed                      bool
}

func New(path string, capacity int, timeout time.Duration, policy *fetch.Client) (*Pool, error) {
	if path == "" || capacity < 1 || capacity > 8 {
		return nil, errors.New("browser needs a Chromium path and 1–8 slots")
	}
	// chromedp otherwise automatically adds --no-sandbox for root. Refuse that path.
	if os.Geteuid() == 0 {
		return nil, errors.New("Chromium must run as a non-root user with its sandbox enabled")
	}
	proxy, e := newProxy(policy, timeout)
	if e != nil {
		return nil, e
	}
	return &Pool{path: path, timeout: timeout, slots: make(chan struct{}, capacity), proxy: proxy}, nil
}
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{Active: p.active, Capacity: cap(p.slots), Restarts: p.restarts, Ready: p.root != nil && p.root.Err() == nil}
}
func (p *Pool) Close() {
	p.mu.Lock()
	p.closed = true
	if p.stop != nil {
		p.stop()
		p.root = nil
	}
	p.mu.Unlock()
	p.proxy.Close()
}

// startLocked is called only after a job has acquired a bounded slot.
func (p *Pool) startLocked() error {
	if p.closed {
		return errors.New("browser pool is closed")
	}
	if p.root != nil {
		b := chromedp.FromContext(p.root).Browser
		if b != nil {
			select {
			case <-b.LostConnection:
			default:
				if p.root.Err() == nil && (p.completed < 100 || p.active > 0) {
					return nil
				}
			}
		}
		if p.stop != nil {
			p.stop()
		}
		p.root = nil
	}
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(p.path), chromedp.Headless, chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("no-sandbox", false), chromedp.Flag("disable-dev-shm-usage", false),
		chromedp.Flag("disable-background-networking", true), chromedp.Flag("disable-extensions", true), chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-quic", true), chromedp.Flag("disable-features", "MediaRouter,OptimizationHints,Translate"),
		chromedp.Flag("force-webrtc-ip-handling-policy", "disable_non_proxied_udp"),
		chromedp.Flag("proxy-server", p.proxy.URL()), chromedp.Flag("proxy-bypass-list", "<-loopback>"),
		chromedp.Flag("host-resolver-rules", "MAP * ~NOTFOUND, EXCLUDE 127.0.0.1"),
		chromedp.Flag("disable-popup-blocking", false), chromedp.Flag("renderer-process-limit", strconv.Itoa(cap(p.slots)*2+1)),
		chromedp.WSURLReadTimeout(10 * time.Second),
	}
	alloc, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	root, rootCancel := chromedp.NewContext(alloc, chromedp.WithErrorf(func(string, ...any) {}))
	// A watchdog bounds initialization without making the reusable root itself expire.
	timer := time.AfterFunc(15*time.Second, rootCancel)
	err := chromedp.Run(root)
	timer.Stop()
	if err != nil {
		rootCancel()
		allocCancel()
		return fmt.Errorf("Chromium could not start; check executable, sandbox support, and container profile: %w", err)
	}
	p.root = root
	p.completed = 0
	p.restarts++
	p.stop = func() { rootCancel(); allocCancel() }
	return nil
}
func (p *Pool) Render(ctx context.Context, raw, waitSelector string, settleMS int) (fetch.Result, error) {
	var out fetch.Result
	if e := fetch.ValidateURL(raw); e != nil {
		return out, e
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return out, ctx.Err()
	}
	p.mu.Lock()
	if e := p.startLocked(); e != nil {
		p.mu.Unlock()
		return out, e
	}
	root := p.root
	p.active++
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.active--; p.completed++; p.mu.Unlock() }()
	jobCtx, jobCancel := context.WithCancel(root)
	defer jobCancel()
	stopCancel := context.AfterFunc(ctx, jobCancel)
	defer stopCancel()
	browser := chromedp.FromContext(root).Browser
	executor := cdp.WithExecutor(jobCtx, browser)
	browserID, e := target.CreateBrowserContext().WithDisposeOnDetach(true).Do(executor)
	if e != nil {
		return out, fmt.Errorf("create isolated browser context: %w", e)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = target.DisposeBrowserContext(browserID).Do(cdp.WithExecutor(cleanup, browser))
	}()
	// A newly isolated context has no window. Chromium 152 requires newWindow
	// explicitly; chromedp's implicit CreateTarget otherwise fails here.
	targetID, e := target.CreateTarget("about:blank").WithBrowserContextID(browserID).WithNewWindow(true).Do(executor)
	if e != nil {
		return out, fmt.Errorf("create isolated browser tab: %w", e)
	}
	tab, tabCancel := chromedp.NewContext(jobCtx, chromedp.WithTargetID(targetID))
	defer tabCancel()
	var status int64
	var mainFrame cdp.FrameID
	var mu sync.Mutex
	chromedp.ListenTarget(tab, func(ev any) {
		switch e := ev.(type) {
		case *page.EventFrameNavigated:
			if e.Frame.ParentID == "" {
				mu.Lock()
				mainFrame = e.Frame.ID
				mu.Unlock()
			}
		case *network.EventResponseReceived:
			if e.Type == network.ResourceTypeDocument {
				mu.Lock()
				if e.FrameID == mainFrame || mainFrame == "" {
					status = e.Response.Status
				}
				mu.Unlock()
			}
		}
	})
	// New tabs/popups are closed so a page cannot create an unbounded tab population.
	chromedp.ListenBrowser(tab, func(ev any) {
		if e, ok := ev.(*target.EventTargetCreated); ok && e.TargetInfo.OpenerID != "" {
			go func() {
				c := chromedp.FromContext(tab)
				if c.Browser != nil {
					_ = target.CloseTarget(e.TargetInfo.TargetID).Do(cdp.WithExecutor(tab, c.Browser))
				}
			}()
		}
	})
	err := chromedp.Run(tab,
		network.Enable(), network.SetBypassServiceWorker(true), network.SetCacheDisabled(true),
		network.SetBlockedURLs().WithURLPatterns([]*network.BlockPattern{{URLPattern: "ws://*", Block: true}, {URLPattern: "wss://*", Block: true}, {URLPattern: "file://*", Block: true}, {URLPattern: "ftp://*", Block: true}}),
		cdpbrowser.SetDownloadBehavior(cdpbrowser.SetDownloadBehaviorBehaviorDeny).WithBrowserContextID(browserID),
		chromedp.Navigate(raw),
	)
	if err == nil && waitSelector != "" {
		err = chromedp.Run(tab, chromedp.WaitReady(waitSelector, chromedp.ByQuery))
	}
	if err == nil && settleMS > 0 {
		err = chromedp.Run(tab, chromedp.Sleep(time.Duration(settleMS)*time.Millisecond))
	}
	var snapshot struct {
		URL      string `json:"url"`
		HTML     string `json:"html"`
		TooLarge bool   `json:"tooLarge"`
	}
	if err == nil {
		err = chromedp.Run(tab, chromedp.Evaluate(`(()=>{const html=document.documentElement.outerHTML;return {url:location.href,html:html.length>4194304?'':html,tooLarge:html.length>4194304}})()`, &snapshot))
	}
	if err != nil {
		if ctx.Err() != nil {
			return out, fmt.Errorf("browser render deadline exceeded or canceled: %w", ctx.Err())
		}
		return out, fmt.Errorf("browser navigation or readiness failed: %w", err)
	}
	if snapshot.TooLarge || len(snapshot.HTML) > fetch.MaxBody {
		return out, errors.New("rendered snapshot exceeds 4 MiB limit")
	}
	if e := fetch.ValidateURL(snapshot.URL); e != nil {
		return out, e
	}
	mu.Lock()
	code := status
	mu.Unlock()
	if code == 0 {
		code = 200
	}
	out = fetch.Result{URL: snapshot.URL, Body: []byte(snapshot.HTML), Status: int(code)}
	if code >= 400 {
		return out, &fetch.HTTPError{Status: int(code)}
	}
	return out, nil
}
