package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/model"
	"rss-workshop/internal/testutil"
)

// ageOutput moves a feed's last output change, and its stories', into the
// past, as if every refresh since had published the same bytes. Both backends
// accept the literals, and the ID is hexadecimal.
func ageOutput(t *testing.T, h *portabilityHarness, id string, at time.Time) {
	t.Helper()
	for _, query := range []string{"UPDATE feeds SET last_changed=%d WHERE id='%s'", "UPDATE items SET last_changed=%d WHERE feed_id='%s'"} {
		if _, err := h.app.Store.DB.Exec(fmt.Sprintf(query, at.Unix(), id)); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *portabilityHarness) metrics(t *testing.T) map[string]string {
	t.Helper()
	return parseExposition(t, serve(h.handler, newRequest("/metrics", "Bearer "+metricsToken)).Body.String())
}

func feedSeries(name, title, id string) string {
	return name + `{feed="` + escapeLabel(title) + `",id="` + id + `"}`
}

// A feed that keeps refreshing successfully while publishing the same output
// fails nothing, so the library and metrics have to point it out. The state
// comes from the feed row alone: it follows last_changed, which only an output
// change moves, and it ends with a pause, a failure or a change.
func TestQuietFeedIsListedAndCounted(t *testing.T) {
	ctx := context.Background()
	h := newPortabilityHarness(t)
	h.app.MetricsToken = metricsToken
	s := h.app.Store
	id, err := s.Save(ctx, model.Feed{Title: "Slow site", URL: "https://example.com/news", Interval: 3600, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}})
	if err != nil {
		t.Fatal(err)
	}
	stories := []model.Item{{Key: "one", Title: "One"}}
	refresh := func(items []model.Item, runErr error) {
		t.Helper()
		f, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Complete(ctx, f, items, "", "", 200, runErr, 0); err != nil {
			t.Fatal(err)
		}
	}
	state := func(want bool, what string) model.Feed {
		t.Helper()
		f := h.listed(t, id)
		m := h.metrics(t)
		count := "0"
		if want {
			count = "1"
		}
		if f.Quiet != want || m["rss_workshop_feeds_quiet"] != count {
			t.Fatalf("%s: listed quiet=%v, rss_workshop_feeds_quiet=%s, want %v", what, f.Quiet, m["rss_workshop_feeds_quiet"], want)
		}
		if got := m[feedSeries("rss_workshop_feed_last_changed_timestamp_seconds", "Slow site", id)]; got != strconv.FormatInt(f.LastChanged.Unix(), 10) {
			t.Fatalf("%s: last changed series = %q, listed %v", what, got, f.LastChanged)
		}
		return f
	}

	refresh(stories, nil)
	if f := state(false, "a feed that just published"); f.LastChanged.IsZero() {
		t.Fatal("the listing does not report when the output last changed")
	}
	old := time.Now().Add(-model.QuietFloor - time.Hour).Truncate(time.Second)
	ageOutput(t, h, id, old)
	state(true, "a feed unchanged for longer than the threshold")

	// A refresh that publishes the same story leaves it quiet.
	refresh(stories, nil)
	if f := state(true, "after an unchanged refresh"); !f.LastChanged.Equal(old) {
		t.Fatalf("an unchanged refresh moved last_changed to %v", f.LastChanged)
	}

	// The listing's last_changed and quiet are reported state, not settings:
	// a feed sent back with them changed saves without moving either.
	listed := h.listed(t, id)
	listed.LastChanged, listed.Quiet = time.Now(), false
	if w := h.request("PUT", "/api/feeds/"+id, jsonBody(t, listed)); w.Code != http.StatusOK {
		t.Fatalf("saving the listed feed = %d %s", w.Code, w.Body.String())
	}
	if f := state(true, "after a save that sent a new last_changed"); !f.LastChanged.Equal(old) {
		t.Fatalf("a save stored the last_changed it was sent: %v", f.LastChanged)
	}

	if w := h.request("POST", "/api/feeds/"+id+"/enabled", []byte(`{"enabled":false}`)); w.Code != http.StatusOK {
		t.Fatalf("pause = %d %s", w.Code, w.Body.String())
	}
	state(false, "a paused feed")
	if w := h.request("POST", "/api/feeds/"+id+"/enabled", []byte(`{"enabled":true}`)); w.Code != http.StatusOK {
		t.Fatalf("resume = %d %s", w.Code, w.Body.String())
	}
	state(true, "a resumed feed")

	refresh(nil, errors.New("source unavailable"))
	state(false, "a failing feed")
	refresh(stories, nil)
	state(true, "a feed that recovered without changing")
	refresh(append(stories, model.Item{Key: "two", Title: "Two"}), nil)
	state(false, "a feed with a new story")
}

// A held feed's latest refresh was never stored, so its unchanged row says
// nothing about whether the output would have changed.
func TestHeldFeedIsNotQuiet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := newPortabilityHarness(t)
	h.app.MetricsToken = metricsToken
	s := h.app.Store
	h.fetcher.body = []byte(`<article><h2>New story</h2></article>`)
	id, err := s.Save(ctx, model.Feed{Title: "Held and unchanged", URL: "https://example.com/news", Interval: 3600, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, f, []model.Item{{Key: "old", Title: "Old story"}}, "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	ageOutput(t, h, id, time.Now().Add(-30*24*time.Hour))
	if !h.listed(t, id).Quiet {
		t.Fatal("the fixture feed is not quiet before it is held")
	}

	restore := testutil.BlockItemWrites(t, s.DB)
	defer restore()
	if w := h.request("POST", "/api/feeds/"+id+"/refresh", []byte(`{}`)); w.Code != http.StatusAccepted {
		t.Fatalf("refresh = %d %s", w.Code, w.Body.String())
	}
	h.app.Scheduler.Start(ctx)
	defer func() { cancel(); h.app.Scheduler.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	listed := h.listed(t, id)
	for !listed.Held && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		listed = h.listed(t, id)
	}
	if !listed.Held {
		t.Fatal("the refresh the database refused did not hold the feed")
	}
	if m := h.metrics(t); listed.Quiet || m["rss_workshop_feeds_quiet"] != "0" || m["rss_workshop_feeds_held"] != "1" {
		t.Fatalf("held feed: listed quiet=%v, quiet=%s, held=%s", listed.Quiet, m["rss_workshop_feeds_quiet"], m["rss_workshop_feeds_held"])
	}
}

// The quiet count covers the whole library, like the other aggregates, while
// the new per-feed series stays within the same bound as the rest.
func TestQuietMetricsKeepTheFeedBound(t *testing.T) {
	h := newPortabilityHarness(t)
	h.app.MetricsToken = metricsToken
	now := time.Now()
	tx, err := h.app.Store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i <= maxMetricFeeds; i++ {
		// Feed 1000 sorts last by title, so it is the one left without series.
		if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run,last_attempt,last_success,last_changed)
VALUES('%048x','token%04d','Feed %04d','https://example.com/section/%04d','{"type":"css","items":"article","title":{"selector":"h2"}}',900,TRUE,%d,%d,%d,%d)`,
			i, i, i, i, now.Add(time.Hour).Unix(), now.Add(-time.Hour).Unix(), now.Add(-time.Hour).Unix(), now.Add(-model.QuietFloor-time.Duration(i)*time.Second).Unix())); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	body := serve(h.handler, newRequest("/metrics", "Bearer "+metricsToken)).Body.String()
	series := parseExposition(t, body)
	if got := series["rss_workshop_feeds_quiet"]; got != strconv.Itoa(maxMetricFeeds+1) {
		t.Errorf("rss_workshop_feeds_quiet = %q, want every feed in the library", got)
	}
	for _, name := range []string{"rss_workshop_feed_last_changed_timestamp_seconds", "rss_workshop_feed_last_success_timestamp_seconds"} {
		n := 0
		for key := range series {
			if strings.HasPrefix(key, name+"{") {
				n++
			}
		}
		if n != maxMetricFeeds {
			t.Errorf("%s has %d series, want %d", name, n, maxMetricFeeds)
		}
	}
	last := fmt.Sprintf("%048x", maxMetricFeeds)
	if _, ok := series[feedSeries("rss_workshop_feed_last_changed_timestamp_seconds", "Feed 1000", last)]; ok {
		t.Error("a feed past the bound has a per-feed series")
	}
	first := feedSeries("rss_workshop_feed_last_changed_timestamp_seconds", "Feed 0000", fmt.Sprintf("%048x", 0))
	if got := series[first]; got != strconv.FormatInt(now.Add(-model.QuietFloor).Unix(), 10) {
		t.Errorf("%s = %q", first, got)
	}
	if strings.Contains(body, "example.com") || strings.Contains(body, "token0") {
		t.Error("metrics carry a source URL or a reader token")
	}
}

// The listing reports last_changed under its own name, so a script reading
// the library sees the same time the reader routes advertise.
func TestListingReportsLastChanged(t *testing.T) {
	ctx := context.Background()
	h := newPortabilityHarness(t)
	id, err := h.app.Store.Save(ctx, model.Feed{Title: "Listed", URL: "https://example.com/news", Interval: 3600, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	ageOutput(t, h, id, at)
	var out struct {
		Feeds []map[string]json.RawMessage `json:"feeds"`
	}
	if err := json.Unmarshal(h.request("GET", "/api/feeds", nil).Body.Bytes(), &out); err != nil || len(out.Feeds) != 1 {
		t.Fatalf("listing: %v", err)
	}
	if got := string(out.Feeds[0]["last_changed"]); got != `"2026-10-01T08:30:00Z"` {
		t.Fatalf("last_changed = %s", got)
	}
	if got := string(out.Feeds[0]["quiet"]); got != "false" {
		t.Fatalf("quiet for a feed that never refreshed = %s", got)
	}
}
