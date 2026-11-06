package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"rss-workshop/internal/model"
	"rss-workshop/internal/testutil"
)

func feedStateRecipe(article string) model.Recipe {
	return model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"},
		Full: &model.FullContent{Selector: article}}
}

func jsonBody(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (h *portabilityHarness) listed(t *testing.T, id string) listedFeed {
	t.Helper()
	w := h.request("GET", "/api/feeds", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("listing = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Feeds []listedFeed `json:"feeds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, f := range out.Feeds {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("feed %s is not listed", id)
	return listedFeed{}
}

// A held feed waits out a failed database write in memory, so its stored
// schedule says it is overdue. The library and metrics must say why it is not
// refreshing, and Refresh and an editor save, which each just wrote to the
// database, must try it again now instead of after the hold.
func TestHeldFeedIsReportedAndRetriedOnRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := newPortabilityHarness(t)
	s := h.app.Store
	h.app.MetricsToken = metricsToken
	h.fetcher.body = []byte(`<article><h2>One</h2><a href="/one">Read</a></article>`)
	restore := testutil.BlockItemWrites(t, s.DB)
	feed := model.Feed{Title: "Held", URL: "https://example.com/news", Interval: 3600, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}}}
	w := h.request("POST", "/api/feeds", jsonBody(t, feed))
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d %s", w.Code, w.Body.String())
	}
	id := decodeReply[map[string]string](t, w)["id"]
	h.app.Scheduler.Start(ctx)
	defer func() { cancel(); h.app.Scheduler.Wait() }()

	// The scheduler ticks once a second, so each retry completes within a few.
	waitFor := func(what string, done func(listedFeed) bool) listedFeed {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			f := h.listed(t, id)
			if done(f) {
				return f
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s: %+v after %d fetches", what, f, h.fetcher.calls.Load())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	fetchedAndHeld := func(what string, fetches int32) {
		t.Helper()
		waitFor(what, func(f listedFeed) bool { return h.fetcher.calls.Load() >= fetches && f.Held })
		if got := h.fetcher.calls.Load(); got != fetches {
			t.Fatalf("%s: %d fetches, want %d", what, got, fetches)
		}
	}
	metrics := func() map[string]string {
		t.Helper()
		return parseExposition(t, serve(h.handler, newRequest("/metrics", "Bearer "+metricsToken)).Body.String())
	}

	fetchedAndHeld("the first unsaved refresh", 1)
	if m := metrics(); m["rss_workshop_feeds_held"] != "1" || m["rss_workshop_feeds_due"] != "1" || m["rss_workshop_feeds_failing"] != "0" {
		t.Fatalf("held feed metrics: held=%s due=%s failing=%s", m["rss_workshop_feeds_held"], m["rss_workshop_feeds_due"], m["rss_workshop_feeds_failing"])
	}
	if w := h.request("POST", "/api/feeds/"+id+"/refresh", []byte(`{}`)); w.Code != http.StatusAccepted {
		t.Fatalf("refresh = %d %s", w.Code, w.Body.String())
	}
	fetchedAndHeld("a manual refresh of a held feed", 2)

	restore()
	if w := h.request("PUT", "/api/feeds/"+id, jsonBody(t, feed)); w.Code != http.StatusOK {
		t.Fatalf("editor save = %d %s", w.Code, w.Body.String())
	}
	waitFor("an editor save to retry the feed", func(f listedFeed) bool { return !f.Held && f.Count == 1 })
	if m := metrics(); m["rss_workshop_feeds_held"] != "0" {
		t.Fatalf("a stored feed is still counted as held: %s", m["rss_workshop_feeds_held"])
	}
}

// During a database outage the feed still exists: Refresh and Reset feed links
// must not answer that it does not.
func TestRefreshAndLinkResetReportDatabaseFailures(t *testing.T) {
	h := newPortabilityHarness(t)
	for _, path := range []string{"/api/feeds/missing/refresh", "/api/feeds/missing/rotate-token"} {
		if w := h.request("POST", path, []byte(`{}`)); w.Code != http.StatusNotFound {
			t.Fatalf("%s for a missing feed = %d, want 404", path, w.Code)
		}
	}
	id, err := h.app.Store.Save(context.Background(), model.Feed{Title: "Unreachable", URL: "https://example.com/news", Interval: 3600, Enabled: true, Recipe: feedStateRecipe(".body")})
	if err != nil {
		t.Fatal(err)
	}
	h.app.Store.DB.Close()
	for _, path := range []string{"/api/feeds/" + id + "/refresh", "/api/feeds/" + id + "/rotate-token"} {
		if w := h.request("POST", path, []byte(`{}`)); w.Code != http.StatusInternalServerError {
			t.Fatalf("%s with the database unavailable = %d %s, want 500", path, w.Code, w.Body.String())
		}
	}
}
