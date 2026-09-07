package browser

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"rss-workshop/internal/extract"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
)

func TestChromiumLifecycleAndIsolation(t *testing.T) {
	if os.Getenv("RSS_BROWSER_TEST") != "1" {
		t.Skip("run the browser-tests Docker target for real Chromium integration tests")
	}
	path := os.Getenv("CHROMIUM_PATH")
	if path == "" {
		t.Fatal("CHROMIUM_PATH required")
	}
	policy, e := fetch.New(10*time.Second, "127.0.0.1/32")
	if e != nil {
		t.Fatal(e)
	}
	pool, e := New(path, 2, 10*time.Second, policy)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	var blockedHits atomic.Int32
	blocked := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { blockedHits.Add(1); w.Write([]byte("private")) }))
	l, e := net.Listen("tcp", "127.0.0.2:0")
	if e != nil {
		t.Fatal(e)
	}
	blocked.Listener = l
	blocked.Start()
	defer blocked.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/js":
			fmt.Fprint(w, `<html><body><script>setTimeout(()=>{document.body.innerHTML='<article><h2>Rendered story</h2><a href="/story">Read</a><img data-lazy-src="/thumb.jpg"></article>'},120)</script></body></html>`)
		case "/set":
			fmt.Fprint(w, `<html><body><script>document.cookie='session=secret';localStorage.setItem('secret','value');document.body.innerHTML='<article>set</article>'</script></body></html>`)
		case "/check":
			fmt.Fprint(w, `<html><body><script>document.body.innerHTML='<article>'+(document.cookie||localStorage.getItem('secret')?'LEAK':'clean')+'</article>'</script></body></html>`)
		case "/blocked":
			fmt.Fprintf(w, `<html><body><script>fetch(%q).catch(()=>{}).finally(()=>{document.body.innerHTML='<article>done</article>'})</script><img src=%q></body></html>`, blocked.URL, blocked.URL+"/image")
		case "/redirect":
			http.Redirect(w, r, blocked.URL, 302)
		default:
			fmt.Fprint(w, "<html><body>waiting</body></html>")
		}
	}))
	defer srv.Close()
	render := func(path, selector string) fetch.Result {
		t.Helper()
		r, e := pool.Render(context.Background(), srv.URL+path, selector, 0)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	result := render("/js", "article")
	p, e := extract.Run(result.Body, result.URL, model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}})
	if e != nil || len(p.Items) != 1 || p.Items[0].Title != "Rendered story" || p.Items[0].Image != srv.URL+"/thumb.jpg" {
		t.Fatal(p, e)
	}
	render("/set", "article")
	if r := render("/check", "article"); strings.Contains(string(r.Body), ">LEAK<") {
		t.Fatal("browser context leaked cookies/localStorage")
	}
	render("/blocked", "article")
	if blockedHits.Load() != 0 {
		t.Fatal("browser subresource bypassed outbound policy")
	}
	if _, e := pool.Render(context.Background(), srv.URL+"/redirect", "", 0); e == nil {
		t.Fatal("private redirect accepted")
	}
	if blockedHits.Load() != 0 {
		t.Fatal("redirect bypassed outbound policy")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, e = pool.Render(ctx, srv.URL, ".never", 0)
	cancel()
	if e == nil {
		t.Fatal("readiness timeout ignored")
	}
	render("/js", "article")
	// Observe sandbox state from Chromium itself, not just the absence of a flag.
	pool.mu.Lock()
	root := pool.root
	pool.mu.Unlock()
	tab, closeTab := chromedp.NewContext(root)
	var sandbox string
	if e = chromedp.Run(tab, chromedp.Navigate("chrome://sandbox"), chromedp.Text("body", &sandbox, chromedp.ByQuery)); e != nil {
		t.Fatal(e)
	}
	closeTab()
	t.Log("Chromium sandbox:", sandbox)
	if !strings.Contains(sandbox, "Seccomp-BPF sandbox\tYes") || !strings.Contains(sandbox, "PID namespaces\tYes") || !strings.Contains(sandbox, "Network namespaces\tYes") || !strings.Contains(sandbox, "You are adequately sandboxed") {
		t.Fatal("Chromium sandbox is not active")
	}
	var wg sync.WaitGroup
	var peak atomic.Int32
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := pool.Render(context.Background(), srv.URL+"/js", "article", 100); err != nil {
				t.Error(err)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for {
		select {
		case <-done:
			goto bounded
		default:
			n := int32(pool.Stats().Active)
			if n > peak.Load() {
				peak.Store(n)
			}
			time.Sleep(time.Millisecond)
		}
	}
bounded:
	if peak.Load() > 2 {
		t.Fatal("browser concurrency exceeded configuration", peak.Load())
	}
	// Crash a real process while a render is waiting, then verify a later render recovers.
	crashDone := make(chan error, 1)
	go func() { _, err := pool.Render(context.Background(), srv.URL, ".never", 0); crashDone <- err }()
	deadline := time.Now().Add(2 * time.Second)
	for pool.Stats().Active == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	pool.mu.Lock()
	proc := chromedp.FromContext(pool.root).Browser.Process()
	pool.mu.Unlock()
	if e = proc.Kill(); e != nil {
		t.Fatal(e)
	}
	select {
	case err := <-crashDone:
		if err == nil {
			t.Fatal("crashed render reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("crash did not release job")
	}
	render("/js", "article")
	if pool.Stats().Active != 0 || pool.Stats().Restarts < 2 {
		t.Fatal("browser recovery failed", pool.Stats())
	}
}
