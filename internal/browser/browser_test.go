package browser

import (
	"bytes"
	"context"
	"encoding/json"
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

// webRTCUsername is the ICE username fragment the page chooses for itself.
const webRTCUsername = "PageChosenText"

// webRTCPage offers its sink as a STUN server and as a remote ICE candidate,
// which is all a page needs to send UDP without passing through the proxy.
const webRTCPage = `<html><body><script>
const sleep = ms => new Promise(r => setTimeout(r, ms));
(async () => {
  const pc = new RTCPeerConnection({iceServers: [{urls: 'stun:%[1]s:%[2]d'}]});
  pc.createDataChannel('probe');
  const complete = new Promise(r => { pc.onicegatheringstatechange = () => { if (pc.iceGatheringState === 'complete') r() } });
  const offer = await pc.createOffer();
  await pc.setLocalDescription(offer);
  await pc.setRemoteDescription({type: 'answer', sdp: offer.sdp
    .replace(/a=ice-ufrag:.*/g, 'a=ice-ufrag:%[3]s')
    .replace(/a=ice-pwd:.*/g, 'a=ice-pwd:aaaaaaaaaaaaaaaaaaaaaaaa')
    .replace(/a=setup:actpass/g, 'a=setup:active')});
  await pc.addIceCandidate({candidate: 'candidate:1 1 udp 2130706431 %[1]s %[2]d typ host', sdpMid: '0', sdpMLineIndex: 0});
  await Promise.race([complete, sleep(3000)]);
  await sleep(2000);
  document.body.innerHTML = '<article>' + pc.iceGatheringState + '</article>';
})().catch(e => { document.body.innerHTML = '<article>' + e + '</article>' });
</script></body></html>`

func TestChromiumSendsNoWebRTCUDP(t *testing.T) {
	if os.Getenv("RSS_BROWSER_TEST") != "1" {
		t.Skip("run the browser-tests Docker target for real Chromium integration tests")
	}
	path := os.Getenv("CHROMIUM_PATH")
	if path == "" {
		t.Fatal("CHROMIUM_PATH required")
	}
	var address net.IP
	addrs, e := net.InterfaceAddrs()
	if e != nil {
		t.Fatal(e)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			address = n.IP
			break
		}
	}
	if address == nil {
		t.Fatal("no non-loopback IPv4 interface: a loopback-only network produces no ICE activity, so this test could prove nothing")
	}
	// Each run gets its own sink, so a late packet from one cannot count against the next.
	probe := func(flags ...chromedp.ExecAllocatorOption) (packets, chosen int32, page string) {
		t.Helper()
		sink, e := net.ListenUDP("udp4", &net.UDPAddr{IP: address})
		if e != nil {
			t.Fatal(e)
		}
		defer sink.Close()
		var received, withUsername atomic.Int32
		go func() {
			buf := make([]byte, 2048)
			for {
				n, _, e := sink.ReadFromUDP(buf)
				if e != nil {
					return
				}
				received.Add(1)
				if bytes.Contains(buf[:n], []byte(webRTCUsername)) {
					withUsername.Add(1)
				}
			}
		}()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, webRTCPage, address, sink.LocalAddr().(*net.UDPAddr).Port, webRTCUsername)
		}))
		defer srv.Close()
		policy, e := fetch.New(10*time.Second, "127.0.0.1/32")
		if e != nil {
			t.Fatal(e)
		}
		pool, e := New(path, 1, 20*time.Second, policy)
		if e != nil {
			t.Fatal(e)
		}
		defer pool.Close()
		pool.testFlags = flags
		r, e := pool.Render(context.Background(), srv.URL, "article", 0)
		if e != nil {
			t.Fatal(e)
		}
		return received.Load(), withUsername.Load(), string(r.Body)
	}
	// Without a policy the same page must reach the sink, or finding nothing below means nothing.
	if packets, chosen, _ := probe(chromedp.Flag("webrtc-ip-handling-policy", false), chromedp.Flag("force-webrtc-ip-handling-policy", false)); packets == 0 || chosen == 0 {
		t.Fatalf("control sent %d packets, %d with the page's username; this environment cannot show the leak", packets, chosen)
	}
	packets, _, page := probe()
	if packets != 0 {
		t.Fatalf("page sent %d UDP packets past the proxy", packets)
	}
	if !strings.Contains(page, "<article>complete</article>") {
		t.Fatal("ICE gathering did not complete, so the page may not have tried: ", page)
	}
}

// connSink records each connection to one address and the first byte sent on
// it, which is 'G' for a ws:// handshake and 0x16 for a wss:// TLS ClientHello.
type connSink struct {
	mu    sync.Mutex
	conns int
	first []byte
}

func (s *connSink) listen(t *testing.T, address string) int {
	t.Helper()
	l, e := net.Listen("tcp", address)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			s.mu.Lock()
			s.conns++
			s.mu.Unlock()
			go func() {
				defer c.Close()
				buf := make([]byte, 1)
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				if _, e := c.Read(buf); e == nil {
					s.mu.Lock()
					s.first = append(s.first, buf[0])
					s.mu.Unlock()
				}
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func (s *connSink) seen() (int, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns, append([]byte(nil), s.first...)
}

// DevTools URL blocking does not stop WebSockets, so the proxy's CONNECT rule
// is what confines them. Binding port 443 relies on Docker's default
// ip_unprivileged_port_start.
func TestChromiumWebSocketsStayWithinProxyPolicy(t *testing.T) {
	if os.Getenv("RSS_BROWSER_TEST") != "1" {
		t.Skip("run the browser-tests Docker target for real Chromium integration tests")
	}
	path := os.Getenv("CHROMIUM_PATH")
	if path == "" {
		t.Fatal("CHROMIUM_PATH required")
	}
	var allowed, otherPort, rejected connSink
	allowed.listen(t, "127.0.0.1:443")
	rejected.listen(t, "127.0.0.2:443")
	port := otherPort.listen(t, "127.0.0.1:0")
	urls, e := json.Marshal([]string{
		"ws://127.0.0.1:443/", "wss://127.0.0.1:443/",
		fmt.Sprintf("ws://127.0.0.1:%d/", port), fmt.Sprintf("wss://127.0.0.1:%d/", port),
		"ws://127.0.0.2:443/", "wss://127.0.0.2:443/",
	})
	if e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><script>
const open = u => new Promise(r => { const s = new WebSocket(u); s.onopen = s.onerror = s.onclose = r; setTimeout(r, 5000) });
(async () => { for (const u of %s) await open(u); document.body.innerHTML = '<article>done</article>' })();
</script></body></html>`, urls)
	}))
	defer srv.Close()
	policy, e := fetch.New(10*time.Second, "127.0.0.1/32")
	if e != nil {
		t.Fatal(e)
	}
	pool, e := New(path, 1, 30*time.Second, policy)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if _, e := pool.Render(context.Background(), srv.URL, "article", 0); e != nil {
		t.Fatal(e)
	}
	// Both schemes reach an allowed destination on 443, which also shows the sockets below were tried.
	if _, first := allowed.seen(); !bytes.Contains(first, []byte("G")) || !bytes.Contains(first, []byte{0x16}) {
		t.Fatalf("allowed destination on 443 saw first bytes %q, want a ws and a wss connection", first)
	}
	if n, _ := otherPort.seen(); n != 0 {
		t.Fatalf("%d WebSocket connections reached a port other than 443", n)
	}
	if n, _ := rejected.seen(); n != 0 {
		t.Fatalf("%d WebSocket connections reached a destination the policy rejects", n)
	}
}
