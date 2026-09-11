package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/scheduler"
)

// The Content-Security-Policy is the second layer behind the editor's one HTML
// sink: previews assign extracted markup to innerHTML, and the first layer is
// server-side sanitization. Both layers are load-bearing, but only the
// sanitizer has tests of its own, so a refactor of Handler could drop this
// block and nothing would fail. These headers apply to every route, including
// the unauthenticated ones, and must survive that.
//
// The visual selector frame deliberately replaces this policy with a stricter
// nonce-based one so it can run its picker inside a sandbox; that override is
// pinned separately by TestSelectorEndpointAuthAndFramePolicy.
func TestSecurityHeadersCoverEveryResponse(t *testing.T) {
	s, err := openTestStore(t, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	a, err := auth.New("long-test-password", "", false)
	if err != nil {
		t.Fatal(err)
	}
	f, err := fetch.New(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Store: s, Auth: a, BaseURL: "https://reader.example", Scheduler: scheduler.New(s, f, 2, time.Second)}
	h := app.Handler()

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
	}
	// Each directive is here for a reason, so pin them individually rather than
	// comparing one long string that a reformat would break for no cause.
	directives := []string{
		"default-src 'self'",
		"script-src 'self'", // no unsafe-inline: the editor's inline data is rendered, never executed
		"object-src 'none'", // no plugin content from an extracted page
		"base-uri 'none'",   // an injected <base> cannot retarget relative URLs
		"frame-ancestors 'none'",
		"form-action 'self'",
	}

	// A representative route of every kind, including ones that answer before
	// authentication and ones that fail.
	for _, route := range []struct {
		method, path string
		authorized   bool
	}{
		{"GET", "/", false},
		{"GET", "/healthz", false},
		{"GET", "/readyz", false},
		{"GET", "/assets/app.js", false},
		{"GET", "/api/feeds", false},             // 401
		{"GET", "/feeds/nonexistent.xml", false}, // 404 reader route
		{"GET", "/metrics", false},               // 404, metrics disabled
		{"POST", "/api/feeds", false},            // 403, cross-origin gate
		{"GET", "/api/feeds", true},              // authenticated
	} {
		name := route.method + " " + route.path
		if route.authorized {
			name += " (signed in)"
		}
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(route.method, route.path, strings.NewReader("{}"))
			if route.method != "GET" {
				r.Header.Set("Content-Type", "application/json")
			}
			if route.authorized {
				r.AddCookie(login(t, h, app.BaseURL))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			for header, value := range want {
				if got := w.Header().Get(header); got != value {
					t.Errorf("%s = %q, want %q (status %d)", header, got, value, w.Code)
				}
			}
			csp := w.Header().Get("Content-Security-Policy")
			if csp == "" {
				t.Fatalf("no Content-Security-Policy (status %d)", w.Code)
			}
			for _, directive := range directives {
				if !strings.Contains(csp, directive) {
					t.Errorf("policy is missing %q: %s", directive, csp)
				}
			}
			if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
				t.Errorf("policy allows unsafe script execution: %s", csp)
			}
		})
	}
}
