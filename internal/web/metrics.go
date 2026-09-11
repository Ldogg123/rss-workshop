package web

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxMetricFeeds bounds the per-feed series in one scrape. A library larger
// than this still reports every aggregate; only the labelled series stop, so a
// scrape can never grow without limit.
const maxMetricFeeds = 1000

// Metrics exposes counters in the Prometheus text exposition format.
//
// It is disabled unless METRICS_TOKEN is set, and then requires that token as a
// bearer credential. The series are labelled with feed titles, which is what
// makes an alert name the broken feed, so this is deliberately not public. It
// reports only counters and timestamps: never a reader link, a source URL, or
// stored story content.
func (a *App) metrics(w http.ResponseWriter, r *http.Request) {
	if a.MetricsToken == "" {
		http.NotFound(w, r)
		return
	}
	supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(a.MetricsToken)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="rss-workshop metrics"`)
		http.Error(w, "metrics require the configured bearer token", 401)
		return
	}
	feeds, err := a.Store.List(r.Context())
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}

	var b strings.Builder
	metric := func(name, help, kind string) { fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind) }
	value := func(name, labels string, v any) {
		fmt.Fprintf(&b, "%s%s %v\n", name, labels, v)
	}

	metric("rss_workshop_build_info", "Build identity of the running server.", "gauge")
	value("rss_workshop_build_info", `{version="`+escapeLabel(a.Version)+`"}`, 1)

	enabled, due, failing, items := 0, 0, 0, 0
	now := time.Now()
	for _, f := range feeds {
		items += f.Count
		if f.Enabled {
			enabled++
			if !f.NextRun.After(now) {
				due++
			}
		}
		if f.Failures > 0 {
			failing++
		}
	}
	for _, m := range []struct {
		name, help string
		v          int
	}{
		{"rss_workshop_feeds", "Feeds in the library.", len(feeds)},
		{"rss_workshop_feeds_enabled", "Feeds set to refresh automatically.", enabled},
		{"rss_workshop_feeds_due", "Enabled feeds whose next refresh is due.", due},
		{"rss_workshop_feeds_failing", "Feeds whose last refresh failed.", failing},
		{"rss_workshop_items", "Stories saved across all feeds.", items},
	} {
		metric(m.name, m.help, "gauge")
		value(m.name, "", m.v)
	}

	active, capacity := a.Scheduler.Stats()
	browser := a.browserStats()
	solverActive, solverCapacity := 0, 0
	if a.Scheduler.FlareSolverr != nil {
		solverActive, solverCapacity = a.Scheduler.FlareSolverr.Stats()
	}
	ready := 0
	if browser.Ready {
		ready = 1
	}
	for _, m := range []struct {
		name, help, kind string
		v                int
	}{
		{"rss_workshop_refresh_active", "Refreshes running now.", "gauge", active},
		{"rss_workshop_refresh_capacity", "Concurrent refreshes allowed.", "gauge", capacity},
		{"rss_workshop_browser_active", "Chromium jobs running now.", "gauge", browser.Active},
		{"rss_workshop_browser_capacity", "Concurrent Chromium jobs allowed.", "gauge", browser.Capacity},
		{"rss_workshop_browser_restarts", "Times the Chromium pool has been restarted.", "counter", browser.Restarts},
		{"rss_workshop_browser_ready", "Whether Chromium is available.", "gauge", ready},
		{"rss_workshop_flaresolverr_active", "FlareSolverr jobs running now.", "gauge", solverActive},
		{"rss_workshop_flaresolverr_capacity", "Concurrent FlareSolverr jobs allowed.", "gauge", solverCapacity},
	} {
		metric(m.name, m.help, m.kind)
		value(m.name, "", m.v)
	}

	if len(feeds) > maxMetricFeeds {
		feeds = feeds[:maxMetricFeeds]
	}
	// Per-feed series are what let an alert name the feed that broke. A feed
	// that has never refreshed reports 0 rather than a missing series, so a
	// dashboard shows it instead of silently omitting it.
	for _, m := range []struct{ name, help, kind string }{
		{"rss_workshop_feed_items", "Stories saved for this feed.", "gauge"},
		{"rss_workshop_feed_failures", "Consecutive failed refreshes for this feed.", "gauge"},
		{"rss_workshop_feed_enabled", "Whether this feed refreshes automatically.", "gauge"},
		{"rss_workshop_feed_last_success_timestamp_seconds", "Unix time of this feed's last successful refresh, or 0.", "gauge"},
	} {
		metric(m.name, m.help, m.kind)
		for _, f := range feeds {
			labels := `{feed="` + escapeLabel(f.Title) + `",id="` + escapeLabel(f.ID) + `"}`
			switch m.name {
			case "rss_workshop_feed_items":
				value(m.name, labels, f.Count)
			case "rss_workshop_feed_failures":
				value(m.name, labels, f.Failures)
			case "rss_workshop_feed_enabled":
				value(m.name, labels, boolValue(f.Enabled))
			default:
				success := int64(0)
				if !f.LastSuccess.IsZero() {
					success = f.LastSuccess.Unix()
				}
				value(m.name, labels, strconv.FormatInt(success, 10))
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b.String()))
}

func boolValue(v bool) int {
	if v {
		return 1
	}
	return 0
}

// escapeLabel applies the Prometheus label-value escaping rules. A feed title
// is operator-supplied text and may contain any of these.
func escapeLabel(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}
