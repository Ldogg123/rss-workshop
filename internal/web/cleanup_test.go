package web

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"rss-workshop/internal/model"
)

func cleanupFeed(rules ...string) model.Feed {
	f := userAgentFeed("")
	f.Recipe.Remove = rules
	return f
}

// The editor sends its rules one per line, so every handler that accepts a
// recipe drops the space around each and the blank lines before checking and
// storing them. A recipe left without rules stores and exports no remove key,
// as one from before rules existed.
func TestCleanupRulesAreTrimmedAndRoundTrip(t *testing.T) {
	h := newPortabilityHarness(t)
	ctx := context.Background()
	w := h.request("POST", "/api/feeds", feedJSON(t, cleanupFeed(" .share ", "", "\t", "aside.related\r")))
	var saved struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &saved); w.Code != http.StatusOK || err != nil {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	f, err := h.app.Store.Get(ctx, saved.ID)
	if err != nil || !slices.Equal(f.Recipe.Remove, []string{".share", "aside.related"}) {
		t.Fatalf("stored rules %q, %v", f.Recipe.Remove, err)
	}
	export := h.request("GET", "/api/recipes/export?id="+saved.ID, nil)
	var doc recipeDocument
	if err := json.Unmarshal(export.Body.Bytes(), &doc); export.Code != http.StatusOK || err != nil || len(doc.Feeds) != 1 || !slices.Equal(doc.Feeds[0].Recipe.Remove, f.Recipe.Remove) {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	doc.Feeds[0].Recipe.Remove = []string{"", "  .newsletter  ", ".share"}
	imported := h.request("POST", "/api/recipes/import", marshalRecipes(t, doc.Feeds))
	var ids struct{ IDs []string }
	if err := json.Unmarshal(imported.Body.Bytes(), &ids); imported.Code != http.StatusCreated || err != nil || len(ids.IDs) != 1 {
		t.Fatalf("import: %d %s", imported.Code, imported.Body.String())
	}
	if f, err := h.app.Store.Get(ctx, ids.IDs[0]); err != nil || !slices.Equal(f.Recipe.Remove, []string{".newsletter", ".share"}) {
		t.Fatalf("imported rules %q, %v", f.Recipe.Remove, err)
	}

	// Blank lines alone are no rules at all.
	f.Recipe.Remove = []string{" ", ""}
	if w := h.request("PUT", "/api/feeds/"+saved.ID, feedJSON(t, f)); w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	if f, err := h.app.Store.Get(ctx, saved.ID); err != nil || f.Recipe.Remove != nil {
		t.Fatalf("stored rules %q, %v", f.Recipe.Remove, err)
	}
	export = h.request("GET", "/api/recipes/export?id="+saved.ID, nil)
	if export.Code != http.StatusOK || strings.Contains(export.Body.String(), "remove") {
		t.Fatalf("export without rules: %d %s", export.Code, export.Body.String())
	}
}

// Invalid rules are refused wherever a recipe is accepted, before anything is
// fetched or saved, with an error that names the rule by its number.
func TestInvalidCleanupRulesAreRefused(t *testing.T) {
	h := newPortabilityHarness(t)
	ctx := context.Background()
	w := h.request("POST", "/api/feeds", feedJSON(t, cleanupFeed(".share")))
	var saved struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &saved); w.Code != http.StatusOK || err != nil {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	before, err := h.app.Store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	xpath := func(rules ...string) model.Feed {
		f := cleanupFeed(rules...)
		f.Recipe.Type, f.Recipe.Items, f.Recipe.Title.Selector = "xpath", "//article", ".//h2"
		return f
	}
	for _, tc := range []struct {
		name string
		feed model.Feed
		want string
	}{
		{"invalid CSS", cleanupFeed(".share", "div[["), "invalid CSS selector in cleanup rule 2"},
		{"CSS rule under XPath", xpath(".//aside", ".share"), "XPath cleanup rule 2 must start with ./"},
		{"too long", cleanupFeed("." + strings.Repeat("a", 1000)), "cleanup rule 1 exceeds 1000 characters"},
		{"too many", cleanupFeed(slices.Repeat([]string{".share"}, 21)...), "use at most 20 cleanup rules"},
		// Trimming leaves the inside of a rule alone, and the editor would
		// split this one into two rules that remove something else.
		{"line break", cleanupFeed(".share", "article\n.share"), "cleanup rule 2 must be on one line"},
		{"line break under XPath", xpath(".//div[\r\n@class='share']"), "cleanup rule 1 must be on one line"},
	} {
		recipes := marshalRecipes(t, []portableFeed{portableExamples()[0], {Title: tc.feed.Title, URL: tc.feed.URL, Interval: tc.feed.Interval, Recipe: tc.feed.Recipe}})
		for _, request := range []struct {
			method, path string
			body         []byte
		}{
			{"POST", "/api/feeds", feedJSON(t, tc.feed)},
			{"PUT", "/api/feeds/" + saved.ID, feedJSON(t, tc.feed)},
			{"POST", "/api/preview", feedJSON(t, tc.feed)},
			{"POST", "/api/recipes/preview", recipes},
			{"POST", "/api/recipes/import", recipes},
		} {
			w := h.request(request.method, request.path, request.body)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("%s: %s %s = %d %s", tc.name, request.method, request.path, w.Code, w.Body.String())
			}
		}
	}
	if after, err := h.app.Store.List(ctx); err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("refused rules changed the library: %+v, %v", after, err)
	}
	if h.fetcher.calls.Load() != 0 {
		t.Fatal("refused rules reached the fetcher")
	}
}
