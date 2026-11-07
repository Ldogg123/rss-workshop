package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/model"
	"rss-workshop/internal/testutil"
)

// ageStories moves a feed's last new story and last output change, and its
// stories' first and last changes, into the past, as if every refresh since
// had found the same stories unchanged. first_seen matters as well: the merge
// counts a story first seen in the current second as new, which a test that
// refreshes at once would otherwise hit. Both backends accept the literals,
// and the ID is hexadecimal.
func ageStories(t *testing.T, h *portabilityHarness, id string, at time.Time) {
	t.Helper()
	for _, query := range []string{"UPDATE feeds SET last_changed=%[1]d,last_new=%[1]d WHERE id='%[2]s'", "UPDATE items SET first_seen=%[1]d,last_changed=%[1]d WHERE feed_id='%[2]s'"} {
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

// A feed that keeps refreshing successfully without finding a new story fails
// nothing, so the library and metrics have to point it out. The state comes
// from the feed row alone: it follows last_new, which only a new story moves,
// so a story whose text keeps changing or an edit to the title does not end
// it, and a pause, a failure or a new story does.
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
		for name, at := range map[string]time.Time{"rss_workshop_feed_last_changed_timestamp_seconds": f.LastChanged, "rss_workshop_feed_last_new_story_timestamp_seconds": f.LastNew} {
			if got := m[feedSeries(name, f.Title, id)]; got != strconv.FormatInt(at.Unix(), 10) {
				t.Fatalf("%s: %s = %q, listed %v", what, name, got, at)
			}
		}
		return f
	}

	refresh(stories, nil)
	if f := state(false, "a feed that just published"); f.LastChanged.IsZero() || f.LastNew.IsZero() {
		t.Fatal("the listing does not report when the output last changed and when a story was last new")
	}
	old := time.Now().Add(-model.QuietFloor - time.Hour).Truncate(time.Second)
	ageStories(t, h, id, old)
	state(true, "a feed without a new story for longer than the threshold")

	// A refresh that publishes the same story leaves it quiet.
	refresh(stories, nil)
	if f := state(true, "after an unchanged refresh"); !f.LastChanged.Equal(old) || !f.LastNew.Equal(old) {
		t.Fatalf("an unchanged refresh moved last_changed to %v or last_new to %v", f.LastChanged, f.LastNew)
	}
	// So does one whose story changed, such as a comment count: the output
	// changes, but nothing in it is new.
	refresh([]model.Item{{Key: "one", Title: "One", HTML: "<p>3 comments</p>"}}, nil)
	if f := state(true, "after a refresh that only changed a story"); !f.LastChanged.After(old) || !f.LastNew.Equal(old) {
		t.Fatalf("a changed story left last_changed at %v or moved last_new to %v", f.LastChanged, f.LastNew)
	}

	// The listing's last_changed, last_new and quiet are reported state, not
	// settings: a feed sent back with them changed saves without moving any.
	listed := h.listed(t, id)
	changed := listed.LastChanged
	listed.LastChanged, listed.LastNew, listed.Quiet = time.Now(), time.Now(), false
	if w := h.request("PUT", "/api/feeds/"+id, jsonBody(t, listed)); w.Code != http.StatusOK {
		t.Fatalf("saving the listed feed = %d %s", w.Code, w.Body.String())
	}
	if f := state(true, "after a save that sent a new last_changed and last_new"); !f.LastChanged.Equal(changed) || !f.LastNew.Equal(old) {
		t.Fatalf("a save stored the last_changed %v or the last_new %v it was sent", f.LastChanged, f.LastNew)
	}
	// A new title changes the output, and is not a new story either.
	listed = h.listed(t, id)
	listed.Title = "Slow site, renamed"
	if w := h.request("PUT", "/api/feeds/"+id, jsonBody(t, listed)); w.Code != http.StatusOK {
		t.Fatalf("renaming the feed = %d %s", w.Code, w.Body.String())
	}
	if f := state(true, "after a rename"); !f.LastChanged.After(changed) || !f.LastNew.Equal(old) {
		t.Fatalf("a rename left last_changed at %v or moved last_new to %v", f.LastChanged, f.LastNew)
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
	state(true, "a feed that recovered without a new story")
	refresh(append(stories, model.Item{Key: "two", Title: "Two"}), nil)
	if f := state(false, "a feed with a new story"); !f.LastNew.After(old) {
		t.Fatalf("a new story left last_new at %v", f.LastNew)
	}
}

// A feed that has never stored a story, such as one whose filters leave out
// every story, has no last_new, so Quiet counts from its last output change:
// its first successful refresh, or an edit to its title, URL or interval,
// which therefore restarts the count. Pausing and resuming do not, and once
// the feed has stored a story the same edit no longer does either.
func TestQuietWithoutAStoryCountsFromTheLastOutputChange(t *testing.T) {
	ctx := context.Background()
	h := newPortabilityHarness(t)
	s := h.app.Store
	id, err := s.Save(ctx, model.Feed{Title: "Filtered site", URL: "https://example.com/news", Interval: 3600, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}})
	if err != nil {
		t.Fatal(err)
	}
	refresh := func(items []model.Item) {
		t.Helper()
		f, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Complete(ctx, f, items, "", "", 200, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	request := func(method, path string, body []byte) {
		t.Helper()
		if w := h.request(method, path, body); w.Code != http.StatusOK {
			t.Fatalf("%s %s = %d %s", method, path, w.Code, w.Body.String())
		}
	}
	rename := func(title string) model.Feed {
		t.Helper()
		listed := h.listed(t, id)
		listed.Title = title
		request("PUT", "/api/feeds/"+id, jsonBody(t, listed))
		return h.listed(t, id)
	}
	old := time.Now().Add(-model.QuietFloor - time.Hour).Truncate(time.Second)

	// A refresh whose filters left out every story.
	refresh(nil)
	if f := h.listed(t, id); f.Quiet || f.LastChanged.IsZero() || !f.LastNew.IsZero() {
		t.Fatalf("after the first success: quiet=%v, last_changed=%v, last_new=%v", f.Quiet, f.LastChanged, f.LastNew)
	}
	if _, err := s.DB.Exec(fmt.Sprintf("UPDATE feeds SET last_changed=%d WHERE id='%s'", old.Unix(), id)); err != nil {
		t.Fatal(err)
	}
	if f := h.listed(t, id); !f.Quiet {
		t.Fatal("a feed without a story whose first success is past the threshold is not quiet")
	}
	request("POST", "/api/feeds/"+id+"/enabled", []byte(`{"enabled":false}`))
	request("POST", "/api/feeds/"+id+"/enabled", []byte(`{"enabled":true}`))
	if f := h.listed(t, id); !f.Quiet || !f.LastChanged.Equal(old) {
		t.Fatalf("pausing and resuming: quiet=%v, last_changed=%v", f.Quiet, f.LastChanged)
	}
	if f := rename("Filtered site, renamed"); f.Quiet || !f.LastChanged.After(old) || !f.LastNew.IsZero() {
		t.Fatalf("a rename of a feed without a story: quiet=%v, last_changed=%v, last_new=%v", f.Quiet, f.LastChanged, f.LastNew)
	}

	refresh([]model.Item{{Key: "one", Title: "One"}})
	ageStories(t, h, id, old)
	if f := rename("Filtered site, renamed again"); !f.Quiet || !f.LastChanged.After(old) || !f.LastNew.Equal(old) {
		t.Fatalf("a rename of a feed with a story: quiet=%v, last_changed=%v, last_new=%v", f.Quiet, f.LastChanged, f.LastNew)
	}
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
	ageStories(t, h, id, time.Now().Add(-30*24*time.Hour))
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
// the per-feed time series stay within the same bound as the rest.
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
		// Every feed's output changed an hour ago, but no story is new.
		if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run,last_attempt,last_success,last_changed,last_new)
VALUES('%048x','token%04d','Feed %04d','https://example.com/section/%04d','{"type":"css","items":"article","title":{"selector":"h2"}}',900,TRUE,%d,%d,%d,%d,%d)`,
			i, i, i, i, now.Add(time.Hour).Unix(), now.Add(-time.Hour).Unix(), now.Add(-time.Hour).Unix(), now.Add(-time.Hour).Unix(), now.Add(-model.QuietFloor-time.Duration(i)*time.Second).Unix())); err != nil {
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
	for _, name := range []string{"rss_workshop_feed_last_new_story_timestamp_seconds", "rss_workshop_feed_last_changed_timestamp_seconds", "rss_workshop_feed_last_success_timestamp_seconds"} {
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
	if _, ok := series[feedSeries("rss_workshop_feed_last_new_story_timestamp_seconds", "Feed 1000", last)]; ok {
		t.Error("a feed past the bound has a per-feed series")
	}
	for name, want := range map[string]time.Time{"rss_workshop_feed_last_new_story_timestamp_seconds": now.Add(-model.QuietFloor), "rss_workshop_feed_last_changed_timestamp_seconds": now.Add(-time.Hour)} {
		first := feedSeries(name, "Feed 0000", fmt.Sprintf("%048x", 0))
		if got := series[first]; got != strconv.FormatInt(want.Unix(), 10) {
			t.Errorf("%s = %q", first, got)
		}
	}
	if strings.Contains(body, "example.com") || strings.Contains(body, "token0") {
		t.Error("metrics carry a source URL or a reader token")
	}
}

// The listing reports last_changed and last_new under their own names, so a
// script reading the library sees the time the reader routes advertise and
// the one Quiet measures from.
func TestListingReportsLastChangedAndLastNew(t *testing.T) {
	ctx := context.Background()
	h := newPortabilityHarness(t)
	id, err := h.app.Store.Save(ctx, model.Feed{Title: "Listed", URL: "https://example.com/news", Interval: 3600, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	ageStories(t, h, id, at)
	if _, err := h.app.Store.DB.Exec(fmt.Sprintf("UPDATE feeds SET last_new=%d WHERE id='%s'", at.Add(-24*time.Hour).Unix(), id)); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Feeds []map[string]json.RawMessage `json:"feeds"`
	}
	if err := json.Unmarshal(h.request("GET", "/api/feeds", nil).Body.Bytes(), &out); err != nil || len(out.Feeds) != 1 {
		t.Fatalf("listing: %v", err)
	}
	if got := string(out.Feeds[0]["last_changed"]); got != `"2026-10-01T08:30:00Z"` {
		t.Fatalf("last_changed = %s", got)
	}
	if got := string(out.Feeds[0]["last_new"]); got != `"2026-09-30T08:30:00Z"` {
		t.Fatalf("last_new = %s", got)
	}
	if got := string(out.Feeds[0]["quiet"]); got != "false" {
		t.Fatalf("quiet for a feed that never refreshed = %s", got)
	}
}

// A feed's own Quiet threshold is a recipe setting. Only automatic, never and
// the editor's choices are accepted, wherever a recipe is, so a stored value
// is always one the editor's select can show and send back. Saving one
// changes which feeds the library and metrics call quiet, and nothing that
// readers receive.
func TestRecipeQuietDays(t *testing.T) {
	ctx := context.Background()
	h := newPortabilityHarness(t)
	h.app.MetricsToken = metricsToken
	s := h.app.Store
	f := userAgentFeed("")
	f.Enabled = true
	f.Recipe.QuietDays = 7
	w := h.request("POST", "/api/feeds", feedJSON(t, f))
	var saved struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &saved); w.Code != http.StatusOK || err != nil {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	id := saved.ID
	stored, err := s.Get(ctx, id)
	if err != nil || stored.Recipe.QuietDays != 7 {
		t.Fatalf("stored quiet_days %d, %v", stored.Recipe.QuietDays, err)
	}
	if err := s.Complete(ctx, stored, []model.Item{{Key: "a", Title: "Story", URL: "https://example.com/a", Published: time.Unix(1757505600, 0)}}, "source-etag", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	// Eight days without a new story is quiet after 7, and not yet after the
	// automatic 14.
	ageStories(t, h, id, time.Now().Add(-8*24*time.Hour))
	quiet := func(want bool, what string) {
		t.Helper()
		if f := h.listed(t, id); f.Quiet != want || h.metrics(t)["rss_workshop_feeds_quiet"] != map[bool]string{false: "0", true: "1"}[want] {
			t.Fatalf("%s: listed quiet=%v, rss_workshop_feeds_quiet=%s, want %v", what, f.Quiet, h.metrics(t)["rss_workshop_feeds_quiet"], want)
		}
	}
	quiet(true, "eight days without a new story, quiet after 7")
	export := h.request("GET", "/api/recipes/export", nil)
	if export.Code != http.StatusOK || !strings.Contains(export.Body.String(), `"quiet_days": 7`) {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	recipe := userAgentFeed("").Recipe
	recipe.QuietDays = model.QuietNever
	imported := h.request("POST", "/api/recipes/import", marshalRecipes(t, []portableFeed{{Title: "Imported", URL: "https://example.com/news", Interval: 900, Recipe: recipe}}))
	var ids struct{ IDs []string }
	if err := json.Unmarshal(imported.Body.Bytes(), &ids); imported.Code != http.StatusCreated || err != nil || len(ids.IDs) != 1 {
		t.Fatalf("import: %d %s", imported.Code, imported.Body.String())
	}
	if f, err := s.Get(ctx, ids.IDs[0]); err != nil || f.Recipe.QuietDays != model.QuietNever {
		t.Fatalf("imported quiet_days %d, %v", f.Recipe.QuietDays, err)
	}

	// Saving another value is a recipe edit that publishes nothing new.
	if stored, err = s.Get(ctx, id); err != nil {
		t.Fatal(err)
	}
	reader := "/feeds/" + stored.RSSToken + ".xml"
	before := h.request("GET", reader, nil)
	for _, c := range []struct {
		days  int
		quiet bool
	}{{model.QuietNever, false}, {0, false}, {30, false}, {90, false}, {365, false}, {7, true}} {
		stored.Recipe.QuietDays = c.days
		if w := h.request("PUT", "/api/feeds/"+id, feedJSON(t, stored)); w.Code != http.StatusOK {
			t.Fatalf("edit to %d: %d %s", c.days, w.Code, w.Body.String())
		}
		edited, err := s.Get(ctx, id)
		if err != nil || edited.Recipe.QuietDays != c.days || edited.ETag != "" || !edited.LastChanged.Equal(stored.LastChanged) || !edited.LastNew.Equal(stored.LastNew) {
			t.Fatalf("edit to %d: quiet_days %d, source validator %q, change time %v -> %v, last new %v -> %v, %v",
				c.days, edited.Recipe.QuietDays, edited.ETag, stored.LastChanged, edited.LastChanged, stored.LastNew, edited.LastNew, err)
		}
		quiet(c.quiet, fmt.Sprintf("eight days without a new story, quiet_days %d", c.days))
		after := h.request("GET", reader, nil)
		if before.Code != http.StatusOK || after.Body.String() != before.Body.String() || after.Header().Get("ETag") != before.Header().Get("ETag") || after.Header().Get("Last-Modified") != before.Header().Get("Last-Modified") {
			t.Fatalf("quiet_days %d changed the reader output:\n%v\n%s\n%v\n%s", c.days, before.Header(), before.Body, after.Header(), after.Body)
		}
		stored = edited
	}
	// Never holds however long a feed goes without a new story.
	stored.Recipe.QuietDays = model.QuietNever
	if w := h.request("PUT", "/api/feeds/"+id, feedJSON(t, stored)); w.Code != http.StatusOK {
		t.Fatalf("edit to never: %d %s", w.Code, w.Body.String())
	}
	ageStories(t, h, id, time.Now().Add(-10*365*24*time.Hour))
	quiet(false, "ten years without a new story, never quiet")
	// Automatic is stored and exported as no setting at all.
	if stored, err = s.Get(ctx, id); err != nil {
		t.Fatal(err)
	}
	stored.Recipe.QuietDays = 0
	if w := h.request("PUT", "/api/feeds/"+id, feedJSON(t, stored)); w.Code != http.StatusOK {
		t.Fatalf("edit to automatic: %d %s", w.Code, w.Body.String())
	}
	quiet(true, "ten years without a new story, automatic")
	var raw string
	if err := s.DB.QueryRow(fmt.Sprintf("SELECT recipe FROM feeds WHERE id='%s'", id)).Scan(&raw); err != nil || strings.Contains(raw, "quiet_days") {
		t.Fatalf("an automatic threshold is stored: %s, %v", raw, err)
	}
	if export := h.request("GET", "/api/recipes/export?id="+id, nil); export.Code != http.StatusOK || strings.Contains(export.Body.String(), "quiet_days") {
		t.Fatalf("an automatic threshold is exported: %d %s", export.Code, export.Body.String())
	}

	library, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, days := range []int{-2, 1, 14, 366} {
		f := userAgentFeed("")
		f.Recipe.QuietDays = days
		feed := feedJSON(t, f)
		recipes := marshalRecipes(t, []portableFeed{portableExamples()[0], {Title: "Quiet", URL: "https://example.com/news", Interval: 900, Recipe: f.Recipe}})
		for _, tc := range []struct{ method, path string }{
			{"POST", "/api/feeds"}, {"PUT", "/api/feeds/" + id}, {"POST", "/api/preview"}, {"POST", "/api/recipes/preview"}, {"POST", "/api/recipes/import"},
		} {
			body := feed
			if strings.HasPrefix(tc.path, "/api/recipes/") {
				body = recipes
			}
			w := h.request(tc.method, tc.path, body)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Mark as Quiet after (quiet_days) must be 0 for automatic, -1 for never, or 7, 30, 90 or 365 days") {
				t.Errorf("%s %s with quiet_days %d: %d %s", tc.method, tc.path, days, w.Code, w.Body.String())
			}
		}
	}
	if after, err := s.List(ctx); err != nil || !reflect.DeepEqual(after, library) {
		t.Fatalf("a refused quiet_days changed the library: %+v, %v", after, err)
	}
	if h.fetcher.calls.Load() != 0 {
		t.Fatal("a refused quiet_days reached the fetcher")
	}
}

// A refresh that stores a new story schedules the next one an interval later,
// so a weekly feed set to 7 days whose every refresh finds a story must not be
// listed or counted as quiet once that refresh falls due, only once it has
// had QuietMargin to run.
func TestChosenQuietTimeOutlastsTheNextRefresh(t *testing.T) {
	ctx := context.Background()
	h := newPortabilityHarness(t)
	h.app.MetricsToken = metricsToken
	s := h.app.Store
	id, err := s.Save(ctx, model.Feed{Title: "Weekly", URL: "https://example.com/news", Interval: 604800, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, QuietDays: 7}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, f, []model.Item{{Key: "a", Title: "A"}}, "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	if f, err = s.Get(ctx, id); err != nil {
		t.Fatal(err)
	}
	if f.IsQuiet(f.NextRun) {
		t.Fatalf("quiet when the next refresh falls due, %v after the new story", f.NextRun.Sub(f.LastNew))
	}
	// Move the feed's times back, as if the next refresh fell due a minute ago
	// and is still waiting or running, then as if it had an hour more.
	earlier := func(by time.Duration) {
		t.Helper()
		if _, err := s.DB.Exec(fmt.Sprintf("UPDATE feeds SET last_attempt=last_attempt-%[1]d,last_success=last_success-%[1]d,last_changed=last_changed-%[1]d,last_new=last_new-%[1]d,next_run=next_run-%[1]d WHERE id='%[2]s'", int64(by/time.Second), id)); err != nil {
			t.Fatal(err)
		}
	}
	quiet := func(want bool, what string) {
		t.Helper()
		if f := h.listed(t, id); f.Quiet != want || h.metrics(t)["rss_workshop_feeds_quiet"] != map[bool]string{false: "0", true: "1"}[want] {
			t.Fatalf("%s: listed quiet=%v, rss_workshop_feeds_quiet=%s, want %v", what, f.Quiet, h.metrics(t)["rss_workshop_feeds_quiet"], want)
		}
	}
	earlier(time.Until(f.NextRun).Truncate(time.Second) + time.Minute)
	quiet(false, "the next refresh due a minute ago")
	earlier(model.QuietMargin)
	quiet(true, "the next refresh due an hour and a minute ago")
}

// The editor offers exactly the thresholds validate accepts, so whatever it
// sends is saved and whatever is stored can be shown and sent back.
func TestEditorOffersTheAcceptedQuietDays(t *testing.T) {
	h := newPortabilityHarness(t)
	page := h.request("GET", "/", nil).Body.String()
	start := strings.Index(page, `<select name="quiet_days"`)
	end := strings.Index(page[max(start, 0):], "</select>")
	if start < 0 || end < 0 {
		t.Fatal("the editor has no quiet_days select")
	}
	var offered []int
	for _, option := range strings.Split(page[start:start+end], `<option value="`)[1:] {
		days, err := strconv.Atoi(option[:strings.IndexByte(option, '"')])
		if err != nil {
			t.Fatal(err)
		}
		if err := model.ValidateQuietDays(days); err != nil {
			t.Errorf("the editor offers %d: %v", days, err)
		}
		offered = append(offered, days)
	}
	if want := append(append([]int{0}, model.QuietDayChoices...), model.QuietNever); !reflect.DeepEqual(offered, want) {
		t.Fatalf("the editor offers %v, want %v", offered, want)
	}
}
