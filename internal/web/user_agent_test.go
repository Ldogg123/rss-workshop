package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
)

func userAgentFeed(agent string) model.Feed {
	return model.Feed{Title: "Agent", URL: "https://example.com/news", Interval: 900, Enabled: false,
		Recipe: model.Recipe{Mode: "static", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, UserAgent: agent}}
}

func feedJSON(t *testing.T, f model.Feed) []byte {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A recipe's User-Agent follows FETCH_USER_AGENT's rule wherever a recipe is
// accepted: the editor's save and preview, the visual selector's page load,
// and recipe imports. Surrounding space is trimmed before it is stored, and a
// value a request header cannot carry as written is refused with a field
// error that does not repeat it, before anything is fetched or saved.
func TestRecipeUserAgentIsTrimmedAndValidated(t *testing.T) {
	h := newPortabilityHarness(t)
	ctx := context.Background()
	w := h.request("POST", "/api/feeds", feedJSON(t, userAgentFeed(" \tFeed/1 (+mailto:ops@example.net)\r\n")))
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var saved struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	f, err := h.app.Store.Get(ctx, saved.ID)
	if err != nil || f.Recipe.UserAgent != "Feed/1 (+mailto:ops@example.net)" {
		t.Fatalf("stored User-Agent %q, %v", f.Recipe.UserAgent, err)
	}
	export := h.request("GET", "/api/recipes/export", nil)
	if export.Code != http.StatusOK || !strings.Contains(export.Body.String(), `"user_agent": "Feed/1 (+mailto:ops@example.net)"`) {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	imported := h.request("POST", "/api/recipes/import", marshalRecipes(t, []portableFeed{{Title: "Imported", URL: "https://example.com/news", Interval: 900, Recipe: userAgentFeed("  Imported/2  ").Recipe}}))
	var ids struct{ IDs []string }
	if err := json.Unmarshal(imported.Body.Bytes(), &ids); imported.Code != http.StatusCreated || err != nil || len(ids.IDs) != 1 {
		t.Fatalf("import: %d %s", imported.Code, imported.Body.String())
	}
	if f, err := h.app.Store.Get(ctx, ids.IDs[0]); err != nil || f.Recipe.UserAgent != "Imported/2" {
		t.Fatalf("imported User-Agent %q, %v", f.Recipe.UserAgent, err)
	}
	before, err := h.app.Store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, agent := range []string{"secret-agent\r\nX-Injected: 1", "secret-agent\nX-Injected: 1", "secret\tagent", "secret-agenté", "secret-agent\x7f", strings.Repeat("secret", 43)} {
		feed := feedJSON(t, userAgentFeed(agent))
		snapshot, err := json.Marshal(map[string]any{"url": "https://example.com/news", "recipe": model.Recipe{Mode: "static", UserAgent: agent}})
		if err != nil {
			t.Fatal(err)
		}
		recipes := marshalRecipes(t, []portableFeed{portableExamples()[0], {Title: "Agent", URL: "https://example.com/news", Interval: 900, Recipe: userAgentFeed(agent).Recipe}})
		for _, tc := range []struct {
			method, path string
			body         []byte
			status       int
		}{
			{"POST", "/api/feeds", feed, http.StatusBadRequest},
			{"PUT", "/api/feeds/" + saved.ID, feed, http.StatusBadRequest},
			{"POST", "/api/preview", feed, http.StatusBadRequest},
			{"POST", "/api/selector/snapshot", snapshot, http.StatusUnprocessableEntity},
			{"POST", "/api/recipes/preview", recipes, http.StatusBadRequest},
			{"POST", "/api/recipes/import", recipes, http.StatusBadRequest},
		} {
			w := h.request(tc.method, tc.path, tc.body)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), "User-Agent must be at most 256 printable ASCII characters") || strings.Contains(w.Body.String(), "secret") {
				t.Errorf("%s %s with %q: %d %s", tc.method, tc.path, agent, w.Code, w.Body.String())
			}
		}
	}
	if after, err := h.app.Store.List(ctx); err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("a refused User-Agent changed the library: %+v, %v", after, err)
	}
	if h.fetcher.calls.Load() != 0 {
		t.Fatal("a refused User-Agent reached the fetcher")
	}
}

// The editor's preview and the visual selector's page load trim the
// User-Agent as a save does, so a value the editor would store is the one they
// send. The scheduler and the fetcher accept only a value that can be sent as
// given, so either handler without its trim would refuse a value that saves.
// The real guarded fetcher is used, so the source sees the header itself.
func TestPreviewAndSnapshotSendTheTrimmedUserAgent(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.UserAgent())
		mu.Unlock()
		w.Write([]byte(`<article><h2>Story</h2></article>`))
	}))
	defer source.Close()
	h := newPortabilityHarness(t)
	client, err := fetch.New(time.Second, "127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	h.app.Scheduler = scheduler.New(h.app.Store, client, 2, 5*time.Second)

	const agent = " \tAgent/1 (+mailto:ops@example.net)\r\n"
	f := userAgentFeed(agent)
	f.URL = source.URL + "/preview"
	if w := h.request("POST", "/api/preview", feedJSON(t, f)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Story") {
		t.Errorf("preview: %d %s", w.Code, w.Body.String())
	}
	snapshot, err := json.Marshal(map[string]any{"url": source.URL + "/snapshot", "recipe": model.Recipe{Mode: "static", UserAgent: agent}})
	if err != nil {
		t.Fatal(err)
	}
	if w := h.request("POST", "/api/selector/snapshot", snapshot); w.Code != http.StatusOK {
		t.Errorf("snapshot: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"/preview Agent/1 (+mailto:ops@example.net)", "/snapshot Agent/1 (+mailto:ops@example.net)"}; !reflect.DeepEqual(seen, want) {
		t.Errorf("the source received %q, want %q", seen, want)
	}
}

// Recipes without a User-Agent are stored and exported exactly as before the
// field existed, so existing feeds, recipe files and older servers reading
// them see no difference. A blank or all-space value counts as none.
func TestBlankUserAgentLeavesRecipeBytesUnchanged(t *testing.T) {
	h := newPortabilityHarness(t)
	const stored = `{"mode":"static","wait_selector":"","settle_ms":0,"type":"css","items":"article","title":{"selector":"h2","attr":""},"link":{"selector":"","attr":""},"date":{"selector":"","attr":""},"content":{"selector":"","attr":""},"image":{"selector":"","attr":""},"date_layout":"","timezone":""}`
	// Without the field, as the editor sends a blank one, then with it empty
	// and with only spaces.
	omitted := feedJSON(t, userAgentFeed(""))
	if strings.Contains(string(omitted), "user_agent") {
		t.Fatalf("a blank User-Agent is encoded: %s", omitted)
	}
	empty := strings.Replace(string(omitted), `"mode":"static",`, `"mode":"static","user_agent":"",`, 1)
	for _, body := range [][]byte{omitted, []byte(empty), feedJSON(t, userAgentFeed("   "))} {
		if w := h.request("POST", "/api/feeds", body); w.Code != http.StatusOK {
			t.Fatalf("save %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	rows, err := h.app.Store.DB.Query("SELECT recipe FROM feeds")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var recipe string
		if err := rows.Scan(&recipe); err != nil {
			t.Fatal(err)
		}
		if recipe != stored {
			t.Errorf("stored recipe changed:\n%s\nwant\n%s", recipe, stored)
		}
		n++
	}
	if err := rows.Err(); err != nil || n != 3 {
		t.Fatalf("read %d recipes, %v", n, err)
	}
	export := h.request("GET", "/api/recipes/export", nil)
	if export.Code != http.StatusOK || strings.Contains(export.Body.String(), "user_agent") {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
}
