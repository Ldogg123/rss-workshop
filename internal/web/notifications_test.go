package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"rss-workshop/internal/model"
	"rss-workshop/internal/notify"
)

// receiver is a notification service that records what reaches it.
type receiver struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
	status int
}

func newReceiver(t *testing.T, status int) *receiver {
	t.Helper()
	r := &receiver{status: status}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, string(body))
		r.mu.Unlock()
		w.WriteHeader(r.status)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) received() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

func withNotifier(t *testing.T, app *App, endpoint string) {
	t.Helper()
	n, err := notify.New(endpoint, notify.JSON, "", app.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Close)
	app.Notifier = n
}

func assertSecurityHeaders(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	for header, value := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY", "Cache-Control": "no-store"} {
		if got := w.Header().Get(header); got != value {
			t.Errorf("%s = %q on a %d response", header, got, w.Code)
		}
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("no content policy on a %d response", w.Code)
	}
}

// The dashboard's test button sends one fixed message through the configured
// channel. Like every other change it needs a session, the configured Origin
// and the CSRF token; it is spaced ten seconds apart so it cannot be used to
// make this server send requests continuously; and it carries no feed data.
func TestTestNotificationRoute(t *testing.T) {
	h := newPortabilityHarness(t)
	service := newReceiver(t, 200)
	withNotifier(t, h.app, service.URL+"/rss-alerts")
	h.app.MetricsToken = "metrics-fixture-token-0123456789"

	w := h.request("POST", "/api/feeds", jsonBody(t, model.Feed{Title: "Private library title", URL: "https://private-source.example/news", Interval: 3600, Recipe: feedStateRecipe(".body")}))
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d %s", w.Code, w.Body.String())
	}
	id := decodeReply[map[string]string](t, w)["id"]
	stored, err := h.app.Store.Get(context.Background(), id)
	if err != nil || stored.RSSToken == "" {
		t.Fatal("no reader token", err)
	}
	h.app.readerLinks(&stored)

	send := func(origin, csrf string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/notifications/test", strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		assertSecurityHeaders(t, w)
		return w
	}
	for _, tc := range []struct {
		name, origin, csrf string
		cookie             *http.Cookie
		want               int
	}{
		{"no session", h.app.BaseURL, h.csrf, nil, 401},
		{"no CSRF token", h.app.BaseURL, "", h.cookie, 403},
		{"wrong CSRF token", h.app.BaseURL, "forged", h.cookie, 403},
		{"cross-origin", "https://attacker.example", h.csrf, h.cookie, 403},
		{"no Origin", "", h.csrf, h.cookie, 403},
	} {
		if w := send(tc.origin, tc.csrf, tc.cookie); w.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, w.Code, tc.want)
		}
	}
	if got := service.received(); len(got) != 0 {
		t.Fatalf("a rejected request sent %d notifications", len(got))
	}

	if w := send(h.app.BaseURL, h.csrf, h.cookie); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"sent":true}` {
		t.Fatalf("test send = %d %s", w.Code, w.Body.String())
	}
	got := service.received()
	if len(got) != 1 {
		t.Fatalf("sent %d notifications", len(got))
	}
	var payload struct {
		Message string
		Events  []struct{ Event, Feed, FeedID string }
	}
	if err := json.Unmarshal([]byte(got[0]), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Events) != 1 || payload.Events[0].Event != "test" || payload.Events[0].Feed != "" || !strings.HasPrefix(payload.Message, "Test notification") {
		t.Errorf("test payload %s", got[0])
	}
	for _, private := range []string{"Private library title", "private-source.example", id, stored.RSSToken, stored.RSSURL} {
		if strings.Contains(got[0], private) {
			t.Errorf("the test notification carries %q", private)
		}
	}

	w = send(h.app.BaseURL, h.csrf, h.cookie)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Errorf("an immediate second send = %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	if got := service.received(); len(got) != 1 {
		t.Errorf("the rate limit let %d notifications through", len(got))
	}

	// The dashboard learns the channel, never its address.
	w = h.request("GET", "/api/feeds", nil)
	var listing map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing["notifications"] != "json" || strings.Contains(w.Body.String(), "rss-alerts") {
		t.Errorf("listing reports notifications %v", listing["notifications"])
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+h.app.MetricsToken)
	m := httptest.NewRecorder()
	h.handler.ServeHTTP(m, r)
	for _, line := range []string{`rss_workshop_notifications_total{result="sent"} 1`, `rss_workshop_notifications_total{result="failed"} 0`, "rss_workshop_notification_events_dropped_total 0"} {
		if !strings.Contains(m.Body.String(), line+"\n") {
			t.Errorf("metrics lack %q", line)
		}
	}
	if strings.Contains(m.Body.String(), "rss-alerts") || strings.Contains(m.Body.String(), "127.0.0.1") {
		t.Error("metrics reveal the notification address")
	}
}

// A failed test says why without the address, which can carry a token, and
// a server without notifications says how to turn them on.
func TestTestNotificationFailuresAndOff(t *testing.T) {
	h := newPortabilityHarness(t)
	service := newReceiver(t, 401)
	withNotifier(t, h.app, service.URL+"/secret-topic-path?auth=secret-query-value")
	w := h.request("POST", "/api/notifications/test", []byte("{}"))
	assertSecurityHeaders(t, w)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "HTTP 401") {
		t.Errorf("a refused test = %d %s", w.Code, w.Body.String())
	}
	for _, secret := range []string{"secret-topic-path", "secret-query-value", service.URL} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("the reply contains %q: %s", secret, w.Body.String())
		}
	}

	off := newPortabilityHarness(t)
	w = off.request("POST", "/api/notifications/test", []byte("{}"))
	assertSecurityHeaders(t, w)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "NOTIFY_URL") {
		t.Errorf("with notifications off = %d %s", w.Code, w.Body.String())
	}
	w = off.request("GET", "/api/feeds", nil)
	var listing map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil || listing["notifications"] != "off" {
		t.Errorf("listing reports notifications %v, %v", listing["notifications"], err)
	}
}
