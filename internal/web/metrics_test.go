package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
	"rss-workshop/internal/store"
	"time"
)

const metricsToken = "metrics-token-for-tests"

func metricsApp(t *testing.T, token string) (*App, *store.Store) {
	t.Helper()
	s, err := openTestStore(t, 500)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	a, err := auth.New("long-test-password", "", false)
	if err != nil {
		t.Fatal(err)
	}
	f, err := fetch.New(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	return &App{Store: s, Auth: a, BaseURL: "https://reader.example", Version: "v9.9.9",
		Scheduler: scheduler.New(s, f, 4, time.Second), MetricsToken: token}, s
}

// parse turns an exposition body into series and their values, failing on any
// line that is not valid Prometheus text.
func parseExposition(t *testing.T, body string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if strings.HasPrefix(line, "#") {
			if !strings.HasPrefix(line, "# HELP ") && !strings.HasPrefix(line, "# TYPE ") {
				t.Errorf("unrecognized comment line: %q", line)
			}
			continue
		}
		cut := strings.LastIndex(line, " ")
		if cut < 0 {
			t.Errorf("sample line has no value: %q", line)
			continue
		}
		name, value := line[:cut], line[cut+1:]
		if name == "" || strings.ContainsAny(name[:1], "0123456789") {
			t.Errorf("invalid metric name in %q", line)
		}
		if open := strings.Index(name, "{"); open >= 0 && !strings.HasSuffix(name, "}") {
			t.Errorf("unterminated label set: %q", line)
		}
		out[name] = value
	}
	return out
}

func TestMetricsAreDisabledWithoutAToken(t *testing.T) {
	app, _ := metricsApp(t, "")
	h := app.Handler()
	// 404, not 401: an instance with no token must not advertise the endpoint.
	if w := get(t, h, "/metrics", nil); w.Code != 404 {
		t.Fatalf("metrics returned %d with no token configured, want 404", w.Code)
	}
}

func TestMetricsRequireTheBearerToken(t *testing.T) {
	app, _ := metricsApp(t, metricsToken)
	h := app.Handler()
	if w := get(t, h, "/metrics", nil); w.Code != 401 {
		t.Errorf("unauthenticated scrape returned %d", w.Code)
	}
	for _, wrong := range []string{"", "Bearer ", "Bearer wrong", "Basic " + metricsToken, metricsToken + "x"} {
		r := newRequest("/metrics", wrong)
		w := serve(h, r)
		if w.Code != 401 {
			t.Errorf("authorization %q returned %d, want 401", wrong, w.Code)
		}
	}
	// An admin session is deliberately not enough; a scraper cannot hold one.
	if w := get(t, h, "/metrics", login(t, h, "https://reader.example")); w.Code != 401 {
		t.Errorf("session-only scrape returned %d, want 401", w.Code)
	}
	w := serve(h, newRequest("/metrics", "Bearer "+metricsToken))
	if w.Code != 200 {
		t.Fatalf("correct token returned %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain; version=0.0.4") {
		t.Errorf("content type %q is not the Prometheus exposition type", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control is %q", got)
	}
}

func TestMetricsReportLibraryState(t *testing.T) {
	ctx := context.Background()
	app, s := metricsApp(t, metricsToken)
	h := app.Handler()
	recipe := model.Recipe{Type: "css", Items: ".card", Title: model.Field{Selector: "h2"}}
	// A title with a quote and a backslash proves label escaping.
	healthy := model.Feed{Title: `Ars "Tech" \ News`, URL: "https://example.com/a", Recipe: recipe, Interval: 900, Enabled: true}
	paused := model.Feed{Title: "Paused", URL: "https://example.com/b", Recipe: recipe, Interval: 900, Enabled: false}
	for _, f := range []model.Feed{healthy, paused} {
		id, err := s.Save(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if f.Enabled {
			if err := s.Complete(ctx, saved, []model.Item{{Key: "k1", Title: "One"}, {Key: "k2", Title: "Two"}}, "", "", 200, nil, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	body := serve(h, newRequest("/metrics", "Bearer "+metricsToken)).Body.String()
	series := parseExposition(t, body)

	for name, want := range map[string]string{
		"rss_workshop_feeds":            "2",
		"rss_workshop_feeds_enabled":    "1",
		"rss_workshop_feeds_failing":    "0",
		"rss_workshop_items":            "2",
		"rss_workshop_refresh_capacity": "4",
		"rss_workshop_browser_ready":    "0",
	} {
		if got := series[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if series[`rss_workshop_build_info{version="v9.9.9"}`] != "1" {
		t.Errorf("build info missing the version: %v", series)
	}
	// The escaped title must appear exactly as Prometheus expects.
	escaped := `rss_workshop_feed_items{feed="Ars \"Tech\" \\ News",id=`
	found := ""
	for name := range series {
		if strings.HasPrefix(name, escaped) {
			found = name
		}
	}
	if found == "" {
		t.Fatalf("no per-feed series with an escaped title; got %v", keys(series))
	}
	if series[found] != "2" {
		t.Errorf("%s = %q, want 2 stories", found, series[found])
	}
	// A never-refreshed feed reports 0 rather than vanishing from the scrape.
	zero := 0
	for name, v := range series {
		if strings.HasPrefix(name, "rss_workshop_feed_last_success_timestamp_seconds{") && v == "0" {
			zero++
		}
	}
	if zero != 1 {
		t.Errorf("expected exactly one never-refreshed feed reporting 0, got %d", zero)
	}
	// Reader links and source URLs are never part of a scrape.
	for _, leak := range []string{"/feeds/", "rss_token", "https://example.com/a"} {
		if strings.Contains(body, leak) {
			t.Errorf("metrics leaked %q", leak)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func newRequest(path, authorization string) *http.Request {
	r := httptest.NewRequest("GET", path, nil)
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	return r
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
