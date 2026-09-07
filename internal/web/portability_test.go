package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"rss-workshop/internal/auth"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
)

type portabilityHarness struct {
	app     *App
	handler http.Handler
	cookie  *http.Cookie
	csrf    string
	fetcher *fixtureFetcher
}

func newPortabilityHarness(t *testing.T) *portabilityHarness {
	t.Helper()
	s, err := openTestStore(t, 500)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	// Authentication behavior is exercised without paying production bcrypt
	// cost for each independent database fixture.
	hash, err := bcrypt.GenerateFromPassword([]byte("portability-test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New("", string(hash), false)
	if err != nil {
		t.Fatal(err)
	}
	session, err := a.Login("portability-test-password")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.Cookie(w, session)
	f := &fixtureFetcher{body: []byte(`<article><h2>Unexpected fetch</h2></article>`)}
	app := &App{Store: s, Auth: a, BaseURL: "http://localhost:8080", Scheduler: scheduler.New(s, f, 2, time.Second)}
	return &portabilityHarness{app: app, handler: app.Handler(), cookie: w.Result().Cookies()[0], csrf: session.CSRF, fetcher: f}
}

func (h *portabilityHarness) request(method, path string, data []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", h.app.BaseURL)
	r.Header.Set("X-CSRF-Token", h.csrf)
	r.AddCookie(h.cookie)
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

func portableExamples() []portableFeed {
	return []portableFeed{
		{Title: "CSS & browser news", URL: "https://example.com/news?section=world&lang=en", Interval: 1800,
			Recipe: model.Recipe{Mode: "browser", WaitSelector: "article.ready", SettleMS: 1234, Type: "css", Items: "main article.card",
				Title: model.Field{Selector: "h2", Attr: "data-title"}, Link: model.Field{Selector: "a.story", Attr: "href"},
				Date: model.Field{Selector: "time", Attr: "datetime"}, Content: model.Field{Selector: ".summary", Attr: "data-summary"},
				Image: model.Field{Selector: "img", Attr: "data-src"}, DateLayout: "2006-01-02 15:04", Timezone: "Europe/London"}},
		{Title: "XPath auto news", URL: "https://example.org/latest", Interval: 604800,
			Recipe: model.Recipe{Mode: "auto", WaitSelector: "main", SettleMS: 5000, Type: "xpath", Items: "//article",
				Title: model.Field{Selector: ".//h2"}, Link: model.Field{Selector: ".//a/@href"},
				Date: model.Field{Selector: ".//time/@datetime"}, Content: model.Field{Selector: ".//p"},
				Image: model.Field{Selector: ".//img", Attr: "src"}, DateLayout: time.RFC3339, Timezone: "UTC"}},
	}
}

func marshalRecipes(t *testing.T, feeds []portableFeed) []byte {
	t.Helper()
	data, err := json.Marshal(recipeDocument{Format: recipeFormat, Version: recipeVersion, Feeds: feeds})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRecipeExportsAndRoundTrip(t *testing.T) {
	h := newPortabilityHarness(t)
	ctx := context.Background()
	examples := portableExamples()
	var originals []model.Feed
	for _, example := range examples {
		f := example.model()
		f.Enabled = true
		id, err := h.app.Store.Save(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		f, err = h.app.Store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.app.Store.Complete(ctx, f, []model.Item{{Key: "stored-item-key", Title: "private stored headline", URL: "https://example.com/item"}}, "private-source-etag", "private-last-modified", 200, nil, 0); err != nil {
			t.Fatal(err)
		}
		if err := h.app.Store.Complete(ctx, f, nil, "", "", 500, errors.New("private refresh failure"), 0); err != nil {
			t.Fatal(err)
		}
		f, err = h.app.Store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		originals = append(originals, f)
	}
	for _, tc := range []struct {
		name, path string
		want       []portableFeed
	}{
		{"all", "/api/recipes/export", examples},
		{"single", "/api/recipes/export?id=" + originals[1].ID, examples[1:]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Import into another library so exports can be compared without
			// confusing imported duplicates with the original fixtures.
			destination := newPortabilityHarness(t)
			w := h.request("GET", tc.path, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("export: %d %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("export headers: %v", w.Header())
			}
			var doc recipeDocument
			if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Format != recipeFormat || doc.Version != 1 || !reflect.DeepEqual(doc.Feeds, tc.want) {
				t.Fatalf("export changed configuration: %+v", doc)
			}
			for _, original := range originals {
				for _, secret := range []string{original.ID, original.RSSToken, h.cookie.Value, h.csrf, "private stored headline", "private refresh failure", "private-source-etag", "private-last-modified"} {
					if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
						t.Fatalf("export contains private state %q", secret)
					}
				}
			}
			var shape struct {
				Feeds []map[string]json.RawMessage `json:"feeds"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &shape); err != nil {
				t.Fatal(err)
			}
			for _, fields := range shape.Feeds {
				if len(fields) != 4 || fields["title"] == nil || fields["url"] == nil || fields["interval"] == nil || fields["recipe"] == nil {
					t.Fatalf("export includes fields beyond recipe configuration: %v", fields)
				}
			}
			data := append([]byte(nil), w.Body.Bytes()...)
			w = destination.request("POST", "/api/recipes/preview", data)
			if w.Code != http.StatusOK {
				t.Fatalf("preview: %d %s", w.Code, w.Body.String())
			}
			if feeds, err := destination.app.Store.List(ctx); err != nil || len(feeds) != 0 {
				t.Fatalf("preview wrote feeds: %v, %v", feeds, err)
			}
			seen := map[string]bool{}
			for _, original := range originals {
				seen[original.ID], seen[original.RSSToken] = true, true
			}
			// Re-importing creates separate paused copies without replacing an
			// earlier import, even when names and source URLs are identical.
			for attempt := 0; attempt < 2; attempt++ {
				w = destination.request("POST", "/api/recipes/import", data)
				if w.Code != http.StatusCreated {
					t.Fatalf("import: %d %s", w.Code, w.Body.String())
				}
				var imported struct {
					Count int      `json:"count"`
					IDs   []string `json:"ids"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &imported); err != nil || imported.Count != len(tc.want) || len(imported.IDs) != len(tc.want) {
					t.Fatalf("import response: %s, %v", w.Body.String(), err)
				}
				for i, id := range imported.IDs {
					f, err := destination.app.Store.Get(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					got := portableFeed{Title: f.Title, URL: f.URL, Interval: f.Interval, Recipe: f.Recipe}
					if !reflect.DeepEqual(got, tc.want[i]) {
						t.Fatalf("round trip changed configuration: got=%+v want=%+v", got, tc.want[i])
					}
					if f.Enabled || !f.LastSuccess.IsZero() || !f.LastAttempt.IsZero() || f.Count != 0 || f.Error != "" || f.Failures != 0 {
						t.Fatalf("import was active or retained run history: %+v", f)
					}
					for _, identity := range []string{f.ID, f.RSSToken} {
						if identity == "" || seen[identity] {
							t.Fatalf("reused identity %q", identity)
						}
						seen[identity] = true
					}
					if w := destination.request("POST", "/api/feeds/"+id+"/refresh", []byte(`{}`)); w.Code != http.StatusConflict {
						t.Fatalf("paused import allowed refresh: %d %s", w.Code, w.Body.String())
					}
				}
			}
			if destination.fetcher.calls.Load() != 0 {
				t.Fatal("recipe preview or import fetched a source")
			}
		})
	}
	for _, original := range originals {
		got, err := h.app.Store.Get(ctx, original.ID)
		if err != nil || !reflect.DeepEqual(got, original) {
			t.Fatalf("export changed original feed: %+v, %v", got, err)
		}
	}
	if w := h.request("GET", "/api/recipes/export?id=missing", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing export = %d", w.Code)
	}
	if h.fetcher.calls.Load() != 0 {
		t.Fatal("export fetched a source")
	}
}

func TestEmptyRecipeExport(t *testing.T) {
	h := newPortabilityHarness(t)
	w := h.request("GET", "/api/recipes/export", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("empty export = %d %s", w.Code, w.Body.String())
	}
	var doc recipeDocument
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil || doc.Format != recipeFormat || doc.Version != 1 || doc.Feeds == nil || len(doc.Feeds) != 0 {
		t.Fatalf("invalid empty export: %s, %v", w.Body.String(), err)
	}
	if w := h.request("POST", "/api/recipes/import", w.Body.Bytes()); w.Code != http.StatusBadRequest {
		t.Fatalf("empty import = %d %s", w.Code, w.Body.String())
	}
}

func TestRecipeImportValidationIsAtomic(t *testing.T) {
	h := newPortabilityHarness(t)
	ctx := context.Background()
	if _, err := h.app.Store.Save(ctx, portableExamples()[0].model()); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.Store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	valid := marshalRecipes(t, portableExamples())
	tests := []struct{ name, body string }{
		{"unknown document field", strings.Replace(string(valid), `"format":`, `"extra":true,"format":`, 1)},
		{"unknown feed field", strings.Replace(string(valid), `"interval":`, `"enabled":true,"interval":`, 1)},
		{"unknown recipe field", strings.Replace(string(valid), `"mode":`, `"extra":true,"mode":`, 1)},
		{"unknown field option", strings.Replace(string(valid), `"selector":`, `"extra":true,"selector":`, 1)},
		{"unsupported format", strings.Replace(string(valid), recipeFormat, "other.recipes", 1)},
		{"unsupported version", strings.Replace(string(valid), `"version":1`, `"version":2`, 1)},
		{"missing version", strings.Replace(string(valid), `"version":1,`, "", 1)},
		{"empty list", string(marshalRecipes(t, []portableFeed{}))},
		{"null list", string(marshalRecipes(t, nil))},
		{"second document", string(valid) + ` {"another":true}`},
		{"trailing garbage", string(valid) + " garbage"},
		{"oversized", string(valid) + strings.Repeat(" ", maxRecipeBytes-len(valid)+1)},
	}
	tooMany := make([]portableFeed, maxRecipes+1)
	for i := range tooMany {
		tooMany[i] = portableExamples()[0]
	}
	tests = append(tests, struct{ name, body string }{"too many feeds", string(marshalRecipes(t, tooMany))})
	for _, invalid := range []struct {
		name   string
		mutate func(*portableFeed)
	}{
		{"missing title", func(f *portableFeed) { f.Title = " " }},
		{"unsafe URL", func(f *portableFeed) { f.URL = "file:///etc/passwd" }},
		{"short interval", func(f *portableFeed) { f.Interval = 59 }},
		{"invalid selector", func(f *portableFeed) { f.Recipe.Type = "css"; f.Recipe.Items = "[" }},
		{"unknown mode", func(f *portableFeed) { f.Recipe.Mode = "unsupported" }},
		{"excess settle", func(f *portableFeed) { f.Recipe.SettleMS = 5001 }},
	} {
		feeds := portableExamples()
		invalid.mutate(&feeds[1])
		tests = append(tests, struct{ name, body string }{"invalid later entry: " + invalid.name, string(marshalRecipes(t, feeds))})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/api/recipes/preview", "/api/recipes/import"} {
				w := h.request("POST", path, []byte(tc.body))
				if w.Code != http.StatusBadRequest {
					t.Fatalf("%s = %d %s", path, w.Code, w.Body.String())
				}
				after, err := h.app.Store.List(ctx)
				if err != nil || !reflect.DeepEqual(after, before) {
					t.Fatalf("invalid document changed the library: %+v, %v", after, err)
				}
			}
		})
	}
	if h.fetcher.calls.Load() != 0 {
		t.Fatal("validation fetched a source")
	}
}

func TestRecipeImportLimitsIncludeBoundary(t *testing.T) {
	h := newPortabilityHarness(t)
	data := marshalRecipes(t, portableExamples()[:1])
	data = append(data, bytes.Repeat([]byte(" "), maxRecipeBytes-len(data))...)
	if w := h.request("POST", "/api/recipes/preview", data); w.Code != http.StatusOK {
		t.Fatalf("exactly 2 MiB rejected: %d %s", w.Code, w.Body.String())
	}
	feeds := make([]portableFeed, maxRecipes)
	for i := range feeds {
		feeds[i] = portableExamples()[0]
		feeds[i].Title = fmt.Sprintf("Feed %04d", i)
	}
	w := h.request("POST", "/api/recipes/import", marshalRecipes(t, feeds))
	if w.Code != http.StatusCreated {
		t.Fatalf("exactly 1,000 feeds rejected: %d %s", w.Code, w.Body.String())
	}
	got, err := h.app.Store.List(context.Background())
	if err != nil || len(got) != maxRecipes {
		t.Fatalf("maximum import count = %d, %v", len(got), err)
	}
	if w := h.request("GET", "/api/recipes/export", nil); w.Code != http.StatusOK {
		t.Fatalf("maximum count export = %d %s", w.Code, w.Body.String())
	}
	if _, err := h.app.Store.Save(context.Background(), portableExamples()[0].model()); err != nil {
		t.Fatal(err)
	}
	if w := h.request("GET", "/api/recipes/export", nil); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized library export = %d %s", w.Code, w.Body.String())
	}
}

func TestImportedRecipeFitsEditRequestLimit(t *testing.T) {
	h := newPortabilityHarness(t)
	ctx := context.Background()
	if _, err := h.app.Store.Save(ctx, portableExamples()[0].model()); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.Store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oversized := portableExamples()
	oversized[1].Recipe.DateLayout = strings.Repeat("x", 70_000)
	for _, path := range []string{"/api/recipes/preview", "/api/recipes/import"} {
		w := h.request("POST", path, marshalRecipes(t, oversized))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("oversized individual recipe at %s = %d %s", path, w.Code, w.Body.String())
		}
		after, err := h.app.Store.List(ctx)
		if err != nil || !reflect.DeepEqual(after, before) {
			t.Fatalf("oversized later recipe caused partial import: %+v, %v", after, err)
		}
	}

	// Match the configuration payload used by the UI's Resume action. Keep
	// false while finding the boundary: true needs one fewer JSON byte.
	edit := struct {
		portableFeed
		Enabled bool `json:"enabled"`
	}{portableFeed: portableExamples()[0]}
	edit.Recipe.DateLayout = ""
	data, err := json.Marshal(edit)
	if err != nil {
		t.Fatal(err)
	}
	const editLimit = 64 << 10
	edit.Recipe.DateLayout = strings.Repeat("x", editLimit-len(data))
	data, err = json.Marshal(edit)
	if err != nil || len(data) != editLimit {
		t.Fatalf("boundary fixture = %d bytes, %v", len(data), err)
	}
	w := h.request("POST", "/api/recipes/import", marshalRecipes(t, []portableFeed{edit.portableFeed}))
	if w.Code != http.StatusCreated {
		t.Fatalf("boundary recipe import = %d %s", w.Code, w.Body.String())
	}
	var imported struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &imported); err != nil || len(imported.IDs) != 1 {
		t.Fatalf("boundary import response = %s, %v", w.Body.String(), err)
	}
	edit.Enabled = true
	data, err = json.Marshal(edit)
	if err != nil {
		t.Fatal(err)
	}
	w = h.request("PUT", "/api/feeds/"+imported.IDs[0], data)
	if w.Code != http.StatusOK {
		t.Fatalf("accepted import could not resume = %d %s", w.Code, w.Body.String())
	}
	f, err := h.app.Store.Get(ctx, imported.IDs[0])
	if err != nil || !f.Enabled || !reflect.DeepEqual(f.Recipe, edit.Recipe) {
		t.Fatalf("resume changed recipe or left it paused: enabled=%v, error=%v", f.Enabled, err)
	}
}

func TestRecipePortabilityAccessControl(t *testing.T) {
	h := newPortabilityHarness(t)
	data := marshalRecipes(t, portableExamples()[:1])
	for _, path := range []string{"/api/recipes/preview", "/api/recipes/import"} {
		for _, tc := range []struct {
			name, origin, csrf, contentType string
			cookie                          bool
			want                            int
		}{
			{"unauthenticated", h.app.BaseURL, h.csrf, "application/json", false, 401},
			{"missing CSRF", h.app.BaseURL, "", "application/json", true, 403},
			{"incorrect CSRF", h.app.BaseURL, "wrong", "application/json", true, 403},
			{"cross origin", "https://evil.example", h.csrf, "application/json", true, 403},
			{"sibling port", "http://localhost:9999", h.csrf, "application/json", true, 403},
			{"missing origin", "", h.csrf, "application/json", true, 403},
			{"not JSON", h.app.BaseURL, h.csrf, "text/plain", true, 415},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				r := httptest.NewRequest("POST", path, bytes.NewReader(data))
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
	}
	for _, cookie := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/api/recipes/export", nil)
		r.Header.Set("Origin", "https://evil.example")
		if cookie {
			r.AddCookie(h.cookie)
		}
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		if !cookie && w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated export = %d", w.Code)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("export grants cross-origin browser access")
		}
	}
	if feeds, err := h.app.Store.List(context.Background()); err != nil || len(feeds) != 0 {
		t.Fatalf("rejected requests added feeds: %v, %v", feeds, err)
	}
}
