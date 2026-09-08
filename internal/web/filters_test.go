package web

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"rss-workshop/internal/model"
)

func titleFilter(keywords ...string) *model.FilterSet {
	return &model.FilterSet{Include: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: keywords}}
}

func TestFilterPreviewAndPortableRoundTrip(t *testing.T) {
	h := newPortabilityHarness(t)
	h.fetcher.body = []byte(`<article><h2>KEEP this story</h2><a href="/story">Read</a><p>Linux <b>Docker</b> news</p></article><article><h2>Keep this advertisement</h2><a href="/sponsored">Read</a><p>Linux Docker</p></article><article><h2>Sports today</h2><a href="/sports">Read</a><p>Other news</p></article>`)
	keywords := make([]string, 100)
	for i := range keywords {
		keywords[i] = fmt.Sprintf("keyword-%03d", i)
	}
	keywords[99] = "keep"
	filters := &model.FilterSet{Include: &model.FilterRule{Op: "all", Rules: []model.FilterRule{
		{Op: "contains_any", Field: "title", Keywords: keywords},
		{Op: "contains_all", Field: "description", Keywords: []string{"linux", "docker"}},
	}}, Exclude: &model.FilterRule{Op: "contains_any", Field: "link", Keywords: []string{"/sponsored"}}}
	f := model.Feed{Title: "Filtered news", URL: "https://example.com", Interval: 60, Recipe: model.Recipe{Mode: "auto", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Content: model.Field{Selector: "p"}, Filters: filters}}
	body, _ := json.Marshal(f)
	w := h.request("POST", "/api/preview", body)
	if w.Code != 200 {
		t.Fatalf("filtered preview: %d %s", w.Code, w.Body.String())
	}
	var p model.Preview
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Matches != 3 || p.Valid != 3 || p.Filtered != 2 || len(p.Items) != 1 || p.Items[0].Title != "KEEP this story" || len(p.FilterExamples) != 2 {
		t.Fatalf("preview counts/items: %+v", p)
	}
	if len(p.Diagnostics.Attempts) != 1 || p.Diagnostics.Attempts[0].Filtered != 2 || *p.Diagnostics.Attempts[0].Valid != 3 {
		t.Fatalf("filter trace: %+v", p.Diagnostics)
	}
	w = h.request("POST", "/api/feeds", body)
	if w.Code != 200 {
		t.Fatalf("filtered save: %d %s", w.Code, w.Body.String())
	}
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	beforeCalls := h.fetcher.calls.Load()
	exported := h.request("GET", "/api/recipes/export?id="+saved.ID, nil)
	if exported.Code != 200 {
		t.Fatal("filtered export failed", exported.Code)
	}
	var doc recipeDocument
	if err := json.Unmarshal(exported.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Feeds) != 1 || !reflect.DeepEqual(doc.Feeds[0].Recipe.Filters, filters) {
		t.Fatal("nested 100-keyword filter changed in export")
	}
	if strings.Contains(exported.Body.String(), "apply_filters_to_history") {
		t.Fatal("export included transient history-removal instruction")
	}
	if preview := h.request("POST", "/api/recipes/preview", exported.Body.Bytes()); preview.Code != 200 {
		t.Fatal("filtered import review failed", preview.Code)
	}
	if imported := h.request("POST", "/api/recipes/import", exported.Body.Bytes()); imported.Code != 201 {
		t.Fatal("filtered import failed", imported.Code, imported.Body.String())
	}
	feeds, err := h.app.Store.List(context.Background())
	if err != nil || len(feeds) != 2 || h.fetcher.calls.Load() != beforeCalls {
		t.Fatal("recipe roundtrip fetched a source or changed library size", err)
	}
	for _, feed := range feeds {
		if feed.Enabled || !reflect.DeepEqual(feed.Recipe.Filters, filters) {
			t.Fatal("import lost filter or created an enabled feed")
		}
	}
}

func TestFilterHistoryChoiceAndValidation(t *testing.T) {
	h := newPortabilityHarness(t)
	ctx := context.Background()
	f := model.Feed{Title: "History choice", URL: "https://example.com", Interval: 60, Recipe: model.Recipe{Mode: "static", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}}
	id, err := h.app.Store.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	f, err = h.app.Store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.Store.Complete(ctx, f, []model.Item{{Key: "keep", Title: "Keep this"}, {Key: "remove", Title: "Unrelated story"}}, "old-etag", "old-modified", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	f.Recipe.Filters = titleFilter("keep")
	requestBody := func(apply bool) []byte {
		body, err := json.Marshal(struct {
			model.Feed
			Apply bool `json:"apply_filters_to_history"`
		}{Feed: f, Apply: apply})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	path := "/api/feeds/" + id
	if w := h.request("PUT", path, requestBody(false)); w.Code != 200 {
		t.Fatalf("keep history: %d %s", w.Code, w.Body.String())
	}
	items, _ := h.app.Store.Items(ctx, id)
	if len(items) != 2 {
		t.Fatal("default filter save pruned historical stories")
	}
	// Invalid filters and missing CSRF must not make a destructive partial save.
	before, _ := h.app.Store.Get(ctx, id)
	f.Recipe.Filters = titleFilter("")
	if w := h.request("PUT", path, requestBody(true)); w.Code != 400 {
		t.Fatalf("invalid filter accepted: %d", w.Code)
	}
	after, _ := h.app.Store.Get(ctx, id)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("invalid filter changed recipe or schedule")
	}
	f.Recipe.Filters = titleFilter("keep")
	csrf := h.csrf
	h.csrf = "invalid"
	if w := h.request("PUT", path, requestBody(true)); w.Code != 403 {
		t.Fatalf("history mutation bypassed CSRF: %d", w.Code)
	}
	h.csrf = csrf
	items, _ = h.app.Store.Items(ctx, id)
	if len(items) != 2 {
		t.Fatal("failed save changed history")
	}
	if w := h.request("PUT", path, requestBody(true)); w.Code != 200 {
		t.Fatalf("apply to history: %d %s", w.Code, w.Body.String())
	}
	items, _ = h.app.Store.Items(ctx, id)
	if len(items) != 1 || items[0].Key != "keep" || h.fetcher.calls.Load() != 0 {
		t.Fatal("selected pruning lost matching story or fetched source")
	}
	// The option is a one-time action, not persistent recipe configuration.
	exported := h.request("GET", "/api/recipes/export?id="+id, nil)
	if exported.Code != 200 || strings.Contains(exported.Body.String(), "apply_filters_to_history") {
		t.Fatal("history action persisted in portable recipe")
	}
}

func TestAllFilteredPreviewAndEmptyReaderFeed(t *testing.T) {
	h := newPortabilityHarness(t)
	f := model.Feed{Title: "Only interesting stories", URL: "https://example.com", Interval: 60, Recipe: model.Recipe{Mode: "auto", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Filters: titleFilter("not present")}}
	body, _ := json.Marshal(f)
	w := h.request("POST", "/api/preview", body)
	var p model.Preview
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || p.Valid != 1 || p.Filtered != 1 || len(p.Items) != 0 || len(p.Diagnostics.Attempts) != 1 {
		t.Fatalf("intentional empty preview treated as failure: %d %+v", w.Code, p)
	}
	ctx := context.Background()
	id, err := h.app.Store.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	f, _ = h.app.Store.Get(ctx, id)
	if err := h.app.Store.CompleteWithDiagnostics(ctx, f, p.Items, "", "", 200, nil, 0, p.Diagnostics); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".xml", ".atom"} {
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, httptest.NewRequest("GET", "/feeds/"+f.RSSToken+suffix, nil))
		if w.Code != 200 {
			t.Fatalf("first successful all-filtered feed was unavailable: %d", w.Code)
		}
		var doc struct {
			Items   []struct{} `xml:"channel>item"`
			Entries []struct{} `xml:"entry"`
		}
		if err := xml.Unmarshal(w.Body.Bytes(), &doc); err != nil || len(doc.Items) != 0 || len(doc.Entries) != 0 {
			t.Fatal("all-filtered reader output malformed or includes stories", err)
		}
	}
}
