package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/model"
)

// A feed source goes through the same save, preview, export and import as a
// web page's recipe, and is refused with a reason when it carries settings it
// cannot use.
func TestFeedSourceSavePreviewExportAndImport(t *testing.T) {
	h := newPortabilityHarness(t)
	rss, err := os.ReadFile(examplesDir + "/demo-site/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	h.fetcher.body = rss
	source := portableFeed{Title: "Gazette feed", URL: "https://gazette.example/feed.xml", Interval: 900, Recipe: model.Recipe{
		Mode: "static", Type: extract.FeedType, Full: &model.FullContent{Selector: ".article-body"}, AcceptLanguage: "en",
		Filters: &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"Sponsored"}}},
	}}
	body := func(f portableFeed) []byte {
		data, err := json.Marshal(f.model())
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	w := h.request("POST", "/api/preview", body(source))
	var preview model.Preview
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &preview) != nil {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	if preview.Matches != 5 || preview.Filtered != 1 || len(preview.Items) != 4 || preview.Diagnostics == nil || preview.Diagnostics.SelectorType != extract.FeedType {
		t.Fatalf("preview: %+v", preview)
	}

	for name, change := range map[string]func(*model.Recipe){
		"no item or field selectors":            func(r *model.Recipe) { r.Items = "item" },
		"fetched as Static HTTP":                func(r *model.Recipe) { r.Mode = "browser" },
		"fetches article pages as plain HTML":   func(r *model.Recipe) { r.Full = &model.FullContent{Selector: ".article-body", Browser: true} },
		"invalid CSS selector for the article ": func(r *model.Recipe) { r.Full = &model.FullContent{Selector: "//div"} },
	} {
		refused := source
		change(&refused.Recipe)
		for _, path := range []string{"/api/feeds", "/api/preview"} {
			if w := h.request("POST", path, body(refused)); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), name) {
				t.Errorf("%s at %s: %d %s", name, path, w.Code, w.Body.String())
			}
		}
	}

	w = h.request("POST", "/api/feeds", body(source))
	var saved struct {
		ID string `json:"id"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &saved) != nil {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	w = h.request("GET", "/api/recipes/export?id="+saved.ID, nil)
	var doc recipeDocument
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &doc) != nil || len(doc.Feeds) != 1 || !reflect.DeepEqual(doc.Feeds[0], source) {
		t.Fatalf("export: %d %s", w.Code, w.Body.String())
	}
	destination := newPortabilityHarness(t)
	w = destination.request("POST", "/api/recipes/import", w.Body.Bytes())
	var imported struct {
		IDs []string `json:"ids"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &imported) != nil || len(imported.IDs) != 1 {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}
	f, err := destination.app.Store.Get(context.Background(), imported.IDs[0])
	if err != nil || !reflect.DeepEqual(f.Recipe, source.Recipe) {
		t.Fatalf("the imported recipe differs: %+v, %v", f.Recipe, err)
	}
}
