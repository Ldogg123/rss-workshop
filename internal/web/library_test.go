package web

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"rss-workshop/internal/model"
)

func decodeReply[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body.String(), err)
	}
	return out
}

func TestLibraryFilterAPIPreviewSaveExportAndDelete(t *testing.T) {
	h := newPortabilityHarness(t)
	h.fetcher.body = []byte(`<article><h2>Linux news</h2><a href="/linux">Read</a></article><article><h2>Sponsored Linux</h2><a href="/ad">Read</a></article><article><h2>Sports</h2><a href="/sports">Read</a></article>`)
	sponsored := &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"sponsored"}}
	for _, bad := range []string{
		`{"name":"  ","filters":{"exclude":{"op":"contains_any","field":"title","keywords":["x"]}}}`,
		`{"name":"` + strings.Repeat("n", 101) + `","filters":{"exclude":{"op":"contains_any","field":"title","keywords":["x"]}}}`,
		`{"name":"Empty","filters":{}}`,
		`{"name":"Broken","filters":{"include":{"op":"all"}}}`,
		`{"name":"Unknown","filters":{},"extra":true}`,
	} {
		if w := h.request("POST", "/api/filters", []byte(bad)); w.Code != 400 {
			t.Fatalf("invalid library filter %s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	body, _ := json.Marshal(map[string]any{"name": "No sponsored posts", "filters": model.FilterSet{Exclude: sponsored}})
	w := h.request("POST", "/api/filters", body)
	if w.Code != 200 {
		t.Fatalf("create library filter: %d %s", w.Code, w.Body.String())
	}
	filterID := decodeReply[map[string]string](t, w)["id"]

	own := titleFilter("linux")
	f := model.Feed{Title: "Linked feed", URL: "https://example.com", Interval: 600, FilterIDs: []string{filterID},
		Recipe: model.Recipe{Mode: "static", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Filters: own}}
	feedBody, _ := json.Marshal(f)
	w = h.request("POST", "/api/preview", feedBody)
	if w.Code != 200 {
		t.Fatalf("preview with library filter: %d %s", w.Code, w.Body.String())
	}
	if p := decodeReply[model.Preview](t, w); len(p.Items) != 1 || p.Items[0].Title != "Linux news" || p.Filtered != 2 {
		t.Fatalf("preview did not merge library rules: %+v", p)
	}
	unknown := f
	unknown.FilterIDs = []string{"missing"}
	unknownBody, _ := json.Marshal(unknown)
	calls := h.fetcher.calls.Load()
	for _, path := range []string{"/api/preview", "/api/feeds"} {
		if w := h.request("POST", path, unknownBody); w.Code != 400 || !strings.Contains(w.Body.String(), "no longer exists") {
			t.Fatalf("%s with unknown library filter: %d %s", path, w.Code, w.Body.String())
		}
	}
	if h.fetcher.calls.Load() != calls {
		t.Fatal("a rejected library filter still fetched the source")
	}
	w = h.request("POST", "/api/feeds", feedBody)
	if w.Code != 200 {
		t.Fatalf("save linked feed: %d %s", w.Code, w.Body.String())
	}
	feedID := decodeReply[map[string]string](t, w)["id"]

	listed := decodeReply[struct {
		Feeds []model.Feed `json:"feeds"`
	}](t, h.request("GET", "/api/feeds", nil))
	if len(listed.Feeds) != 1 || !reflect.DeepEqual(listed.Feeds[0].FilterIDs, []string{filterID}) {
		t.Fatalf("feed list lost filter_ids: %+v", listed.Feeds)
	}
	library := decodeReply[struct {
		Filters []model.LibraryFilter `json:"filters"`
	}](t, h.request("GET", "/api/filters", nil))
	if len(library.Filters) != 1 || library.Filters[0].Name != "No sponsored posts" || !reflect.DeepEqual(library.Filters[0].FeedIDs, []string{feedID}) {
		t.Fatalf("library listing: %+v", library.Filters)
	}

	// Pausing sends the feed without filter_ids, as older clients do.
	pause, _ := json.Marshal(map[string]any{"title": f.Title, "url": f.URL, "interval": f.Interval, "recipe": f.Recipe, "enabled": false})
	if w := h.request("PUT", "/api/feeds/"+feedID, pause); w.Code != 200 {
		t.Fatalf("pause: %d %s", w.Code, w.Body.String())
	}
	if paused, err := h.app.Store.Get(t.Context(), feedID); err != nil || len(paused.FilterIDs) != 1 {
		t.Fatal("a save without filter_ids unlinked the library filter", err)
	}

	// Exports carry the merged rules, so the file imports anywhere as a
	// self-contained feed with no reference to this library.
	exported := h.request("GET", "/api/recipes/export?id="+feedID, nil)
	if exported.Code != 200 || strings.Contains(exported.Body.String(), filterID) || strings.Contains(exported.Body.String(), "filter_ids") {
		t.Fatalf("export: %d %s", exported.Code, exported.Body.String())
	}
	doc := decodeReply[recipeDocument](t, exported)
	want := &model.FilterSet{Include: own.Include, Exclude: sponsored}
	if len(doc.Feeds) != 1 || !reflect.DeepEqual(doc.Feeds[0].Recipe.Filters, want) {
		t.Fatalf("exported filters: %+v", doc.Feeds[0].Recipe.Filters)
	}
	if w := h.request("POST", "/api/recipes/import", exported.Body.Bytes()); w.Code != 201 {
		t.Fatalf("import merged export: %d %s", w.Code, w.Body.String())
	}
	feeds, err := h.app.Store.List(t.Context())
	if err != nil || len(feeds) != 2 {
		t.Fatal(err)
	}
	for _, imported := range feeds {
		if imported.ID != feedID && (len(imported.FilterIDs) != 0 || !reflect.DeepEqual(imported.Recipe.Filters, want)) {
			t.Fatalf("imported feed: %+v", imported)
		}
	}

	// Growing the filter past a linked feed's merged limits is refused by name.
	keywords := make([]string, 500)
	for i := range keywords {
		keywords[i] = "k" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
	}
	large, _ := json.Marshal(map[string]any{"name": "No sponsored posts", "filters": model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: keywords}}})
	if w := h.request("PUT", "/api/filters/"+filterID, large); w.Code != 400 || !strings.Contains(w.Body.String(), "Linked feed") {
		t.Fatalf("oversized merged edit: %d %s", w.Code, w.Body.String())
	}
	if w := h.request("PUT", "/api/filters/missing", body); w.Code != 404 {
		t.Fatalf("edit missing filter: %d", w.Code)
	}
	if w := h.request("DELETE", "/api/filters/"+filterID, nil); w.Code != 409 {
		t.Fatalf("delete filter in use: %d %s", w.Code, w.Body.String())
	}
	if w := h.request("DELETE", "/api/feeds/"+feedID, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := h.request("DELETE", "/api/filters/"+filterID, nil); w.Code != 200 {
		t.Fatalf("delete unused filter: %d %s", w.Code, w.Body.String())
	}
	if w := h.request("DELETE", "/api/filters/"+filterID, nil); w.Code != 404 {
		t.Fatalf("delete missing filter: %d", w.Code)
	}
}

func TestLibraryFilterRoutesRequireSessionAndCSRF(t *testing.T) {
	h := newPortabilityHarness(t)
	body := []byte(`{"name":"No adverts","filters":{"exclude":{"op":"contains_any","field":"title","keywords":["advert"]}}}`)
	for _, route := range []struct{ method, path string }{{"GET", "/api/filters"}, {"POST", "/api/filters"}, {"PUT", "/api/filters/x"}, {"DELETE", "/api/filters/x"}} {
		r := httptest.NewRequest(route.method, route.path, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", h.app.BaseURL)
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("%s %s without session: %d", route.method, route.path, w.Code)
		}
		if route.method == "GET" {
			continue
		}
		r = httptest.NewRequest(route.method, route.path, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", h.app.BaseURL)
		r.AddCookie(h.cookie)
		w = httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s %s without CSRF: %d", route.method, route.path, w.Code)
		}
		r = httptest.NewRequest(route.method, route.path, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("X-CSRF-Token", h.csrf)
		r.AddCookie(h.cookie)
		w = httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s %s cross-origin: %d", route.method, route.path, w.Code)
		}
	}
	if filters, err := h.app.Store.Filters(t.Context()); err != nil || len(filters) != 0 {
		t.Fatal("a rejected request changed the library", err)
	}
}
