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
// User-Agent and the Accept-Language as a save does, so the values the editor
// would store are the ones they send. The scheduler and the fetcher accept
// only values that can be sent as given, so either handler without its trim
// would refuse values that save. The real guarded fetcher is used, so the
// source sees the headers themselves.
func TestPreviewAndSnapshotSendTheTrimmedRequestHeaders(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.UserAgent()+" lang="+strings.Join(r.Header.Values("Accept-Language"), "|"))
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

	const agent, lang = " \tAgent/1 (+mailto:ops@example.net)\r\n", "\u00a0de-CH, de;q=0.9\t\n"
	f := userAgentFeed(agent)
	f.URL, f.Recipe.AcceptLanguage = source.URL+"/preview", lang
	if w := h.request("POST", "/api/preview", feedJSON(t, f)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Story") {
		t.Errorf("preview: %d %s", w.Code, w.Body.String())
	}
	snapshot, err := json.Marshal(map[string]any{"url": source.URL + "/snapshot", "recipe": model.Recipe{Mode: "static", UserAgent: agent, AcceptLanguage: lang}})
	if err != nil {
		t.Fatal(err)
	}
	if w := h.request("POST", "/api/selector/snapshot", snapshot); w.Code != http.StatusOK {
		t.Errorf("snapshot: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"/preview Agent/1 (+mailto:ops@example.net) lang=de-CH, de;q=0.9", "/snapshot Agent/1 (+mailto:ops@example.net) lang=de-CH, de;q=0.9"}; !reflect.DeepEqual(seen, want) {
		t.Errorf("the source received %q, want %q", seen, want)
	}
}

// A recipe's Accept-Language is trimmed and checked wherever a recipe is
// accepted, as its User-Agent is: the editor's save and preview, the visual
// selector's page load, and recipe imports. A value that is not a short list
// of language ranges is refused with a field error that does not repeat it,
// before anything is fetched or saved, and saving a valid one publishes
// nothing: readers keep their validators.
func TestRecipeAcceptLanguageIsTrimmedAndValidated(t *testing.T) {
	h := newPortabilityHarness(t)
	ctx := context.Background()
	f := userAgentFeed("")
	f.Recipe.AcceptLanguage = " de-CH, de;q=0.9, en;q=0.5\r\n"
	w := h.request("POST", "/api/feeds", feedJSON(t, f))
	var saved struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &saved); w.Code != http.StatusOK || err != nil {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	stored, err := h.app.Store.Get(ctx, saved.ID)
	if err != nil || stored.Recipe.AcceptLanguage != "de-CH, de;q=0.9, en;q=0.5" {
		t.Fatalf("stored Accept-Language %q, %v", stored.Recipe.AcceptLanguage, err)
	}
	export := h.request("GET", "/api/recipes/export", nil)
	if export.Code != http.StatusOK || !strings.Contains(export.Body.String(), `"accept_language": "de-CH, de;q=0.9, en;q=0.5"`) {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	recipe := userAgentFeed("").Recipe
	recipe.AcceptLanguage = "\tfr-CA , fr;Q=0.8 "
	imported := h.request("POST", "/api/recipes/import", marshalRecipes(t, []portableFeed{{Title: "Imported", URL: "https://example.com/news", Interval: 900, Recipe: recipe}}))
	var ids struct{ IDs []string }
	if err := json.Unmarshal(imported.Body.Bytes(), &ids); imported.Code != http.StatusCreated || err != nil || len(ids.IDs) != 1 {
		t.Fatalf("import: %d %s", imported.Code, imported.Body.String())
	}
	if f, err := h.app.Store.Get(ctx, ids.IDs[0]); err != nil || f.Recipe.AcceptLanguage != "fr-CA , fr;Q=0.8" {
		t.Fatalf("imported Accept-Language %q, %v", f.Recipe.AcceptLanguage, err)
	}

	// Saving a different value is a recipe edit that publishes nothing new.
	if err := h.app.Store.Complete(ctx, stored, []model.Item{{Key: "a", Title: "Story", URL: "https://example.com/a", Published: time.Unix(1757505600, 0)}}, "source-etag", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	if stored, err = h.app.Store.Get(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	reader := "/feeds/" + stored.RSSToken + ".xml"
	before := h.request("GET", reader, nil)
	stored.Recipe.AcceptLanguage = "en"
	if w := h.request("PUT", "/api/feeds/"+saved.ID, feedJSON(t, stored)); w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	edited, err := h.app.Store.Get(ctx, saved.ID)
	if err != nil || edited.Recipe.AcceptLanguage != "en" || edited.ETag != "" || !edited.LastChanged.Equal(stored.LastChanged) {
		t.Fatalf("edit: Accept-Language %q, source validator %q, change time %v -> %v, %v", edited.Recipe.AcceptLanguage, edited.ETag, stored.LastChanged, edited.LastChanged, err)
	}
	after := h.request("GET", reader, nil)
	if before.Code != http.StatusOK || after.Body.String() != before.Body.String() || after.Header().Get("ETag") != before.Header().Get("ETag") || after.Header().Get("Last-Modified") != before.Header().Get("Last-Modified") {
		t.Fatalf("the reader output changed:\n%v\n%s\n%v\n%s", before.Header(), before.Body, after.Header(), after.Body)
	}
	library, err := h.app.Store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for lang, want := range map[string]string{
		"secret\r\nX-Injected: 1":                "Accept-Language entry 1 must be a language tag",
		"en, secret;q=2":                         "Accept-Language entry 2 must be a language tag",
		"secret\x00":                             "Accept-Language entry 1 must be a language tag",
		"secrét":                                 "Accept-Language entry 1 must be a language tag",
		strings.Repeat("secret,", 19):            "Accept-Language must be at most 128 characters",
		strings.Repeat("secret,", 10) + "secret": "Accept-Language must list at most 10 languages",
	} {
		f := userAgentFeed("")
		f.Recipe.AcceptLanguage = lang
		feed := feedJSON(t, f)
		snapshot, err := json.Marshal(map[string]any{"url": "https://example.com/news", "recipe": model.Recipe{Mode: "static", AcceptLanguage: lang}})
		if err != nil {
			t.Fatal(err)
		}
		recipes := marshalRecipes(t, []portableFeed{portableExamples()[0], {Title: "Language", URL: "https://example.com/news", Interval: 900, Recipe: f.Recipe}})
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
			if w.Code != tc.status || !strings.Contains(w.Body.String(), want) || strings.Contains(w.Body.String(), "secret") {
				t.Errorf("%s %s with %q: %d %s", tc.method, tc.path, lang, w.Code, w.Body.String())
			}
		}
	}
	if after, err := h.app.Store.List(ctx); err != nil || !reflect.DeepEqual(after, library) {
		t.Fatalf("a refused Accept-Language changed the library: %+v, %v", after, err)
	}
	if h.fetcher.calls.Load() != 0 {
		t.Fatal("a refused Accept-Language reached the fetcher")
	}
}

// Recipes without a User-Agent or an Accept-Language are stored and exported
// exactly as before the fields existed, so existing feeds, recipe files and
// older servers reading them see no difference. A blank or all-space value
// counts as none.
func TestBlankRequestHeadersLeaveRecipeBytesUnchanged(t *testing.T) {
	h := newPortabilityHarness(t)
	const stored = `{"mode":"static","wait_selector":"","settle_ms":0,"type":"css","items":"article","title":{"selector":"h2","attr":""},"link":{"selector":"","attr":""},"date":{"selector":"","attr":""},"content":{"selector":"","attr":""},"image":{"selector":"","attr":""},"date_layout":"","timezone":""}`
	// Without the field, as the editor sends a blank one, then with it empty
	// and with only spaces.
	omitted := feedJSON(t, userAgentFeed(""))
	if strings.Contains(string(omitted), "user_agent") || strings.Contains(string(omitted), "accept_language") {
		t.Fatalf("a blank request header is encoded: %s", omitted)
	}
	empty := strings.Replace(string(omitted), `"mode":"static",`, `"mode":"static","user_agent":"","accept_language":"",`, 1)
	spaces := userAgentFeed("   ")
	spaces.Recipe.AcceptLanguage = " \t "
	for _, body := range [][]byte{omitted, []byte(empty), feedJSON(t, spaces)} {
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
	if export.Code != http.StatusOK || strings.Contains(export.Body.String(), "user_agent") || strings.Contains(export.Body.String(), "accept_language") {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
}
