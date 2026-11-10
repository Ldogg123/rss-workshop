package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
)

// Render learns a page's HTTP status only from DevTools events, and assumes
// 200 when none arrives. chromedp drops an event it cannot decode, as it
// would one carrying an enum value a newer Chromium added, and a dropped
// Network.responseReceived would then turn every 404 and 5xx source page into
// a successful render that extraction runs on. A dropped Page.frameNavigated
// would let an iframe's status stand in for the page's. This test fails in
// either case, so it has to pass before CHROMIUM_VERSION, chromedp or cdproto
// moves.
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
	// Error pages carry a body, as a site's own "not found" page does. The
	// last case covers one that does not.
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
	var frames atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := pages[r.URL.Path]
		if !ok {
			// A favicon request, for instance. It is not a document.
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "-frame") {
			frames.Add(1)
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(p.status)
		fmt.Fprint(w, p.body)
	}))
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
			// The source's own page must be what rendered, or the status
			// below could belong to something else.
			if !strings.Contains(string(r.Body), c.shows) {
				t.Fatalf("rendered %q (error %v), want the page showing %q", r.Body, e, c.shows)
			}
			if c.frame && frames.Load() == before {
				t.Fatal("the iframe was never requested, so its status could not have been mistaken for the page's")
			}
			if r.Status != c.want {
				t.Errorf("result status %d, want %d", r.Status, c.want)
			}
			if c.want < 400 {
				if e != nil {
					t.Fatalf("a %d page failed: %v", c.want, e)
				}
				return
			}
			var he *fetch.HTTPError
			if !errors.As(e, &he) {
				t.Fatalf("a %d page returned error %v, want *fetch.HTTPError", c.want, e)
			}
			if he.Status != c.want {
				t.Fatalf("HTTPError status %d, want %d", he.Status, c.want)
			}
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
