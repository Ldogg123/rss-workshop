package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
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

func (h *portabilityHarness) listed(t *testing.T, id string) model.Feed {
	t.Helper()
	w := h.request("GET", "/api/feeds", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("listing = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Feeds []model.Feed `json:"feeds"`
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
	return model.Feed{}
}

// Pause and Resume used to send the card's whole copy of the feed through the
// editor's save. A card loaded before another tab's edit then reverted that
// edit, deleted the article bodies fetched with the corrected selector, and
// cleared the feed's error, failure count and source validators. They now
// change only whether the feed refreshes, and readers see no change.
func TestPauseAndResumeKeepTheFeedAsSaved(t *testing.T) {
	ctx := context.Background()
	h := newPortabilityHarness(t)
	s := h.app.Store
	w := h.request("POST", "/api/feeds", jsonBody(t, model.Feed{Title: "Original", URL: "https://example.com/news", Interval: 3600, Enabled: true, Recipe: feedStateRecipe(".body")}))
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d %s", w.Code, w.Body.String())
	}
	id := decodeReply[map[string]string](t, w)["id"]

	// After this tab's card was drawn, another tab fixes the article selector
	// and renames the feed, which then refreshes with it and later fails once.
	edit := model.Feed{Title: "Renamed", URL: "https://example.com/news", Interval: 3600, Enabled: true, Recipe: feedStateRecipe(".article")}
	if w := h.request("PUT", "/api/feeds/"+id, jsonBody(t, edit)); w.Code != http.StatusOK {
		t.Fatalf("edit = %d %s", w.Code, w.Body.String())
	}
	current, _ := s.Get(ctx, id)
	stories := []model.Item{{Key: "a", Title: "A", URL: "https://example.com/a", HTML: "<p>Teaser</p>", FullHTML: "<p>The right article</p>", Published: time.Unix(1700000000, 0)}}
	if err := s.Complete(ctx, current, stories, `"source-validator"`, "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	current, _ = s.Get(ctx, id)
	if err := s.Complete(ctx, current, nil, "", "", 503, errors.New("source unavailable"), 0); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(ctx, id)
	reader := readerApp(t, s, "test")
	path := "/feeds/" + before.RSSToken + ".xml"
	published := readFeed(t, reader, path, nil)
	if published.code != http.StatusOK {
		t.Fatalf("reader = %d %s", published.code, published.body)
	}
	for _, step := range []struct {
		enabled bool
		what    string
	}{{false, "pause"}, {true, "resume"}} {
		w := h.request("POST", "/api/feeds/"+id+"/enabled", []byte(`{"enabled":`+strconv.FormatBool(step.enabled)+`}`))
		if w.Code != http.StatusOK || decodeReply[map[string]bool](t, w)["enabled"] != step.enabled {
			t.Fatalf("%s = %d %s", step.what, w.Code, w.Body.String())
		}
		got, err := s.Get(ctx, id)
		if err != nil || got.Enabled != step.enabled {
			t.Fatalf("%s left enabled=%v: %v", step.what, got.Enabled, err)
		}
		want := before
		want.Enabled, want.NextRun = got.Enabled, got.NextRun
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s changed the feed:\n got %+v\nwant %+v", step.what, got, want)
		}
		if items, err := s.Items(ctx, id); err != nil || len(items) != 1 || items[0].FullHTML != "<p>The right article</p>" {
			t.Fatalf("%s lost the article body: %+v, %v", step.what, items, err)
		}
		if again := readFeed(t, reader, path, nil); again.body != published.body || again.etag != published.etag || again.lastMod != published.lastMod {
			t.Fatalf("%s changed the published feed", step.what)
		}
		if conditional := readFeed(t, reader, path, map[string]string{"If-None-Match": published.etag}); conditional.code != http.StatusNotModified {
			t.Fatalf("%s: a reader holding the feed got %d, want 304", step.what, conditional.code)
		}
	}
	// Resuming makes the feed due now, even through its error backoff.
	if got, _ := s.Get(ctx, id); got.NextRun.After(time.Now()) || got.Failures != 1 {
		t.Fatalf("resume left the next run at %v with %d failures; want due now with the count kept", got.NextRun, got.Failures)
	}
}

func TestEnabledRouteIsAProtectedMutation(t *testing.T) {
	ctx := context.Background()
	h := newPortabilityHarness(t)
	id, err := h.app.Store.Save(ctx, model.Feed{Title: "Protected", URL: "https://example.com/news", Interval: 3600, Enabled: true, Recipe: feedStateRecipe(".body")})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := h.app.Store.Get(ctx, id)
	path := "/api/feeds/" + id + "/enabled"
	for _, tc := range []struct {
		name, origin, csrf, contentType, body string
		cookie                                bool
		want                                  int
	}{
		{"unauthenticated", h.app.BaseURL, h.csrf, "application/json", `{"enabled":false}`, false, 401},
		{"missing CSRF", h.app.BaseURL, "", "application/json", `{"enabled":false}`, true, 403},
		{"incorrect CSRF", h.app.BaseURL, "wrong", "application/json", `{"enabled":false}`, true, 403},
		{"cross origin", "https://evil.example", h.csrf, "application/json", `{"enabled":false}`, true, 403},
		{"missing origin", "", h.csrf, "application/json", `{"enabled":false}`, true, 403},
		{"not JSON", h.app.BaseURL, h.csrf, "text/plain", `{"enabled":false}`, true, 415},
		{"no value", h.app.BaseURL, h.csrf, "application/json", `{}`, true, 400},
		{"null", h.app.BaseURL, h.csrf, "application/json", `{"enabled":null}`, true, 400},
		{"not a boolean", h.app.BaseURL, h.csrf, "application/json", `{"enabled":"no"}`, true, 400},
		{"unknown field", h.app.BaseURL, h.csrf, "application/json", `{"enabled":false,"title":"Changed"}`, true, 400},
		{"two objects", h.app.BaseURL, h.csrf, "application/json", `{"enabled":false}{"enabled":false}`, true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", path, strings.NewReader(tc.body))
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-CSRF-Token", tc.csrf)
			r.Header.Set("Content-Type", tc.contentType)
			if tc.cookie {
				r.AddCookie(h.cookie)
			}
			w := httptest.NewRecorder()
			h.handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	if after, _ := h.app.Store.Get(ctx, id); !reflect.DeepEqual(after, before) {
		t.Fatal("a rejected request changed the feed")
	}
	if w := h.request("POST", "/api/feeds/missing/enabled", []byte(`{"enabled":true}`)); w.Code != http.StatusNotFound {
		t.Fatalf("missing feed = %d %s", w.Code, w.Body.String())
	}
}

// The editor sends the enabled value it opened with. If the operator left the
// checkbox alone, its save keeps whatever the card set meanwhile, so an editor
// left open no longer re-enables a feed paused from its card.
func TestEditorSaveKeepsAPauseMadeWhileItWasOpen(t *testing.T) {
	ctx := context.Background()
	h := newPortabilityHarness(t)
	id, err := h.app.Store.Save(ctx, model.Feed{Title: "Open in the editor", URL: "https://example.com/news", Interval: 3600, Enabled: true, Recipe: feedStateRecipe(".body")})
	if err != nil {
		t.Fatal(err)
	}
	type editorSave struct {
		model.Feed
		ApplyFiltersToHistory bool  `json:"apply_filters_to_history"`
		LoadedEnabled         *bool `json:"loaded_enabled,omitempty"`
	}
	yes, no := true, false
	save := func(title string, enabled bool, loaded *bool) model.Feed {
		t.Helper()
		f := model.Feed{Title: title, URL: "https://example.com/news", Interval: 3600, Enabled: enabled, Recipe: feedStateRecipe(".body")}
		if w := h.request("PUT", "/api/feeds/"+id, jsonBody(t, editorSave{Feed: f, LoadedEnabled: loaded})); w.Code != http.StatusOK {
			t.Fatalf("save = %d %s", w.Code, w.Body.String())
		}
		got, err := h.app.Store.Get(ctx, id)
		if err != nil || got.Title != title {
			t.Fatalf("save did not store the edit: %+v, %v", got, err)
		}
		return got
	}
	if w := h.request("POST", "/api/feeds/"+id+"/enabled", []byte(`{"enabled":false}`)); w.Code != http.StatusOK {
		t.Fatalf("pause = %d %s", w.Code, w.Body.String())
	}
	if got := save("Edited while paused", true, &yes); got.Enabled {
		t.Fatal("an editor opened before the pause re-enabled the feed")
	}
	if got := save("Resumed in the editor", true, &no); !got.Enabled {
		t.Fatal("the operator's change to the checkbox was not saved")
	}
	if w := h.request("POST", "/api/feeds/"+id+"/enabled", []byte(`{"enabled":false}`)); w.Code != http.StatusOK {
		t.Fatalf("pause = %d %s", w.Code, w.Body.String())
	}
	// Clients that do not send it, such as older ones, set enabled as sent.
	if got := save("Older client", true, nil); !got.Enabled {
		t.Fatal("a save without loaded_enabled did not set enabled")
	}
}

// Before the enabled route, a script paused or resumed a feed by sending its
// object from the listing back as a save. Every key the listing adds must stay
// one that saves and previews accept and ignore, or that round trip breaks.
func TestListedFeedCanBeSentBack(t *testing.T) {
	ctx := context.Background()
	h := newPortabilityHarness(t)
	h.fetcher.body = []byte(`<article><h2>One</h2><a href="/one">Read</a></article>`)
	recipe := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}}
	id, err := h.app.Store.Save(ctx, model.Feed{Title: "Scripted", URL: "https://example.com/news", Interval: 3600, Enabled: true, Recipe: recipe})
	if err != nil {
		t.Fatal(err)
	}
	listed := func() map[string]json.RawMessage {
		t.Helper()
		w := h.request("GET", "/api/feeds", nil)
		var out struct {
			Feeds []map[string]json.RawMessage `json:"feeds"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatalf("listing = %d %s: %v", w.Code, w.Body.String(), err)
		}
		for _, f := range out.Feeds {
			if string(f["id"]) == strconv.Quote(id) {
				if _, ok := f["held"]; !ok {
					t.Fatal("the listing no longer reports held; this test no longer covers it")
				}
				return f
			}
		}
		t.Fatalf("feed %s is not listed", id)
		return nil
	}
	for _, enabled := range []bool{false, true} {
		f := listed()
		f["enabled"] = json.RawMessage(strconv.FormatBool(enabled))
		if w := h.request("PUT", "/api/feeds/"+id, jsonBody(t, f)); w.Code != http.StatusOK {
			t.Fatalf("saving the listed feed with enabled=%v = %d %s", enabled, w.Code, w.Body.String())
		}
		got, err := h.app.Store.Get(ctx, id)
		if err != nil || got.Enabled != enabled || got.Title != "Scripted" || !reflect.DeepEqual(got.Recipe, recipe) {
			t.Fatalf("after saving the listed feed with enabled=%v: %+v, %v", enabled, got, err)
		}
	}
	if w := h.request("POST", "/api/preview", jsonBody(t, listed())); w.Code != http.StatusOK {
		t.Fatalf("previewing the listed feed = %d %s", w.Code, w.Body.String())
	}
}

// A held feed waits out a failed database write in memory, so its stored
// schedule says it is overdue. The library and metrics must say why it is not
// refreshing, and Refresh, Resume and an editor save, which each just wrote to
// the database, must try it again now instead of after the hold.
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
	waitFor := func(what string, done func(model.Feed) bool) model.Feed {
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
		waitFor(what, func(f model.Feed) bool { return h.fetcher.calls.Load() >= fetches && f.Held })
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

	// A paused feed is not waiting for anything.
	if w := h.request("POST", "/api/feeds/"+id+"/enabled", []byte(`{"enabled":false}`)); w.Code != http.StatusOK {
		t.Fatalf("pause = %d %s", w.Code, w.Body.String())
	}
	if f := h.listed(t, id); f.Held || metrics()["rss_workshop_feeds_held"] != "0" {
		t.Fatal("a paused feed was reported as held")
	}
	if w := h.request("POST", "/api/feeds/"+id+"/enabled", []byte(`{"enabled":true}`)); w.Code != http.StatusOK {
		t.Fatalf("resume = %d %s", w.Code, w.Body.String())
	}
	fetchedAndHeld("resuming a held feed", 3)

	restore()
	if w := h.request("PUT", "/api/feeds/"+id, jsonBody(t, feed)); w.Code != http.StatusOK {
		t.Fatalf("editor save = %d %s", w.Code, w.Body.String())
	}
	waitFor("an editor save to retry the feed", func(f model.Feed) bool { return !f.Held && f.Count == 1 })
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
	for _, path := range []string{"/api/feeds/" + id + "/refresh", "/api/feeds/" + id + "/rotate-token", "/api/feeds/" + id + "/enabled"} {
		body := []byte(`{}`)
		if strings.HasSuffix(path, "/enabled") {
			body = []byte(`{"enabled":false}`)
		}
		if w := h.request("POST", path, body); w.Code != http.StatusInternalServerError {
			t.Fatalf("%s with the database unavailable = %d %s, want 500", path, w.Code, w.Body.String())
		}
	}
}
