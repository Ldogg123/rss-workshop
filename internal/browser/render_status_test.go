package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"rss-workshop/internal/fetch"
)

// statusPages serves the documents of the status tests and counts requests
// for the embedded ones. Error pages carry a body, as a site's own "not
// found" page does; "/empty" is the one that does not.
func statusPages() (http.Handler, *atomic.Int32) {
	const frame = `<html><body><p>Embedded</p></body></html>`
	pages := map[string]struct {
		status int
		body   string
	}{
		"/missing":               {404, `<html><body><article>No such page</article></body></html>`},
		"/broken":                {500, `<html><body><article>Server error</article></body></html>`},
		"/listing":               {200, `<html><body><article>Listed story</article><iframe src="/missing-frame"></iframe></body></html>`},
		"/missing-frame":         {404, frame},
		"/missing-with-ok-frame": {404, `<html><body><article>No such page</article><iframe src="/ok-frame"></iframe></body></html>`},
		"/ok-frame":              {200, frame},
		"/empty":                 {404, ""},
	}
	frames := new(atomic.Int32)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := pages[r.URL.Path]
		if !ok {
			// A favicon request, for instance. It is not a document.
			http.NotFound(w, r)
			return
		}
		// Only the embedded documents count: "/missing-with-ok-frame" is a
		// page, and counting it would satisfy the iframe check by itself.
		if r.URL.Path == "/missing-frame" || r.URL.Path == "/ok-frame" {
			frames.Add(1)
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(p.status)
		fmt.Fprint(w, p.body)
	}), frames
}

// requireStatus checks what Render returned for a page that shows the given
// text and answered with the given status.
func requireStatus(t *testing.T, r fetch.Result, e error, shows string, want int) {
	t.Helper()
	// The source's own page must be what rendered, or the status below could
	// belong to something else.
	if !strings.Contains(string(r.Body), shows) {
		t.Fatalf("rendered %q (error %v), want the page showing %q", r.Body, e, shows)
	}
	if r.Status != want {
		t.Errorf("result status %d, want %d", r.Status, want)
	}
	if want < 400 {
		if e != nil {
			t.Fatalf("a %d page failed: %v", want, e)
		}
		return
	}
	var he *fetch.HTTPError
	if !errors.As(e, &he) {
		t.Fatalf("a %d page returned error %v, want *fetch.HTTPError", want, e)
	}
	if he.Status != want {
		t.Fatalf("HTTPError status %d, want %d", he.Status, want)
	}
}

// Render learns a page's HTTP status only from DevTools events, and assumes
// 200 when none arrives. chromedp drops an event it cannot decode, as it
// would one carrying an enum value a newer Chromium added, and a dropped
// Network.responseReceived would then turn every 404 and 5xx source page into
// a successful render that extraction runs on. A dropped Page.frameNavigated
// would let an iframe's status stand in for the page's. This test fails in
// either case for the events a plain HTTP response on loopback produces, so
// it has to pass before CHROMIUM_VERSION, chromedp or cdproto moves. What an
// HTTPS response adds to the event is outside it: see
// TestChromiumReportsDocumentStatusOverHTTPS.
func TestChromiumReportsDocumentStatus(t *testing.T) {
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
	pool, e := New(path, 1, 20*time.Second, policy)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	pages, frames := statusPages()
	srv := httptest.NewServer(pages)
	defer srv.Close()
	for _, c := range []struct {
		name, path, shows string
		want              int  // the status Render must report
		frame             bool // the page embeds a frame whose status differs
	}{
		{"404 document", "/missing", "No such page", 404, false},
		{"500 document", "/broken", "Server error", 500, false},
		{"200 document with a 404 iframe", "/listing", "Listed story", 200, true},
		{"404 document with a 200 iframe", "/missing-with-ok-frame", "No such page", 404, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := frames.Load()
			r, e := pool.Render(context.Background(), srv.URL+c.path, "", 0)
			if c.frame && frames.Load() == before {
				t.Fatalf("the iframe was never requested (rendered %q, error %v), so its status could not have been mistaken for the page's", r.Body, e)
			}
			requireStatus(t, r, e, c.shows, c.want)
		})
	}
	// An error response without a body never renders: Chromium shows an error
	// page of its own and the navigation fails, so the failure carries no
	// status. It must still never be a success.
	t.Run("404 document without a body", func(t *testing.T) {
		if r, e := pool.Render(context.Background(), srv.URL+"/empty", "", 0); e == nil {
			t.Fatalf("an empty 404 rendered as a success with status %d", r.Status)
		}
	})
}

// Sources are HTTPS, and an HTTPS response adds to Network.responseReceived
// what a plain one lacks: securityDetails, with enums of its own, any of
// which a newer Chromium could fill with a value cdproto does not know. This
// renders a 404 over TLS and HTTP/2 so that part of the event is decoded too.
// It still sees only the values a self-signed loopback certificate produces,
// not every one a public site can (a certificate transparency state, for
// one), so a pass does not replace rendering a real HTTPS error page after an
// update. The proxy allows CONNECT only to port 443, and binding it relies on
// Docker's default ip_unprivileged_port_start.
func TestChromiumReportsDocumentStatusOverHTTPS(t *testing.T) {
	if os.Getenv("RSS_BROWSER_TEST") != "1" {
		t.Skip("run the browser-tests Docker target for real Chromium integration tests")
	}
	path := os.Getenv("CHROMIUM_PATH")
	if path == "" {
		t.Fatal("CHROMIUM_PATH required")
	}
	l, e := net.Listen("tcp", "127.0.0.1:443")
	if e != nil {
		t.Fatalf("the HTTPS fixture needs port 443, the only one the proxy lets Chromium reach over TLS: %v", e)
	}
	pages, _ := statusPages()
	var proto atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			proto.Store(int32(r.ProtoMajor))
		}
		pages.ServeHTTP(w, r)
	}))
	srv.Listener.Close()
	srv.Listener = l
	srv.EnableHTTP2 = true
	srv.StartTLS()
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
	// The fixture's certificate is self-signed. Without this flag the
	// navigation fails before any response and nothing renders.
	pool.testFlags = []chromedp.ExecAllocatorOption{chromedp.Flag("ignore-certificate-errors", true)}
	r, e := pool.Render(context.Background(), "https://127.0.0.1/missing", "", 0)
	requireStatus(t, r, e, "No such page", 404)
	if !strings.HasPrefix(r.URL, "https://") {
		t.Fatalf("rendered %q, want the HTTPS page", r.URL)
	}
	if got := proto.Load(); got != 2 {
		t.Errorf("the page was requested over HTTP/%d, want HTTP/2 as HTTPS sources serve", got)
	}
}
