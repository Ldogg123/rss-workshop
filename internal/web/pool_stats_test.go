package web

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"rss-workshop/internal/browser"
	"rss-workshop/internal/fetch"
)

// fakeChromiumEnv holds the address a stand-in Chromium connects to. The pool
// starts this test binary as its Chromium, and init turns the binary into a
// process that never prints a DevTools address: it connects, so the test knows
// the start is under way, and exits when the test hangs up. An init is used
// because it runs before the flags Chromium is given are parsed.
const fakeChromiumEnv = "RSS_WORKSHOP_TEST_FAKE_CHROMIUM"

func init() {
	addr := os.Getenv(fakeChromiumEnv)
	if addr == "" {
		return
	}
	if conn, err := net.Dial("tcp", addr); err == nil {
		_, _ = io.Copy(io.Discard, conn)
	}
	os.Exit(1)
}

// A Chromium start takes seconds, and 10 of them when Chromium never reports
// its DevTools address. The library listing, which the dashboard polls every
// 10 seconds, and a /metrics scrape must answer meanwhile instead of waiting
// for it, and report that Chromium is not ready.
func TestListingAndMetricsAnswerWhileChromiumStarts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the pool refuses to start Chromium as root")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv(fakeChromiumEnv, listener.Addr().String())
	policy, err := fetch.New(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := browser.New(self, 1, 30*time.Second, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	app, _ := metricsApp(t, metricsToken)
	app.Browsers = pool
	h := app.Handler()
	listing := httptest.NewRequest("GET", "/api/feeds", nil)
	listing.AddCookie(login(t, h, "https://reader.example"))

	rendered := make(chan error, 1)
	go func() {
		_, err := pool.Render(context.Background(), "https://example.com/story", "", 0)
		rendered <- err
	}()
	// The stand-in's connection is the start, which lasts until it is closed.
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Second))
	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("the stand-in Chromium did not start: %v", err)
	}
	defer conn.Close() // ends a start that a failed check left waiting

	answer := func(r *http.Request) *httptest.ResponseRecorder {
		t.Helper()
		answered := make(chan *httptest.ResponseRecorder, 1)
		go func() { answered <- serve(h, r) }()
		select {
		case w := <-answered:
			if w.Code != 200 {
				t.Fatalf("GET %s during a Chromium start returned %d", r.URL.Path, w.Code)
			}
			return w
		case <-time.After(5 * time.Second):
			t.Fatalf("GET %s waited for Chromium to start", r.URL.Path)
			return nil
		}
	}
	var listed struct {
		Browser *browser.Stats `json:"browser"`
	}
	if err := json.Unmarshal(answer(listing).Body.Bytes(), &listed); err != nil || listed.Browser == nil || *listed.Browser != (browser.Stats{Capacity: 1}) {
		t.Fatalf("listing during a Chromium start reports %+v (%v); want one free slot and not ready", listed.Browser, err)
	}
	series := parseExposition(t, answer(newRequest("/metrics", "Bearer "+metricsToken)).Body.String())
	for name, want := range map[string]string{
		"rss_workshop_browser_active":         "0",
		"rss_workshop_browser_capacity":       "1",
		"rss_workshop_browser_restarts_total": "0",
		"rss_workshop_browser_ready":          "0",
	} {
		if got, ok := series[name]; !ok || got != want {
			t.Errorf("%s during a Chromium start = %q, want %q", name, got, want)
		}
	}
	select {
	case err := <-rendered:
		t.Fatalf("the start ended before both requests were answered: %v", err)
	default:
	}
	conn.Close()
	select {
	case <-rendered:
	case <-time.After(30 * time.Second):
		t.Fatal("the render outlived its Chromium")
	}
}
