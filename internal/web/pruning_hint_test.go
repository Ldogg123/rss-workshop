package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"rss-workshop/internal/model"
)

// Removing saved stories that do not match is the editor's one irreversible
// action, and it judges each story by the description stored for it. A save
// cleans nothing: only a refresh that lists a story again stores its
// description as the current cleanup rules leave it. A cleanup rule added in
// the same edit as a filter therefore does not protect a story whose stored
// description still shows what the rule removes, although the next refresh
// would have kept it. The hint beside the checkbox is the only place the
// editor can say so before the stories are gone.
func TestHistoryPruningJudgesStoredDescriptionsAndSaysSo(t *testing.T) {
	h := newPortabilityHarness(t)
	ctx := context.Background()
	h.fetcher.body = []byte(`<article><h2>Storage notes</h2><a href="/storage">Read</a><div class="summary"><p>Mirrors and parity.</p><p class="promo">Sponsored by a newsletter</p></div></article>`)
	base := model.Feed{Title: "Stored descriptions", URL: "https://example.com/news", Interval: 900,
		Recipe: model.Recipe{Mode: "static", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Content: model.Field{Selector: ".summary"}}}
	cleaned := base
	cleaned.Recipe.Remove = []string{".promo"}
	cleaned.Recipe.Filters = &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "description", Keywords: []string{"sponsored"}}}

	save := func(method, path string, f model.Feed, prune bool) string {
		t.Helper()
		w := h.request(method, path, jsonBody(t, struct {
			model.Feed
			Apply bool `json:"apply_filters_to_history"`
		}{f, prune}))
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return decodeReply[map[string]string](t, w)["id"]
	}
	// refresh stores what a refresh of the page stores under the saved recipe:
	// a preview extracts, cleans and filters exactly as a refresh does.
	refresh := func(id string) {
		t.Helper()
		f, err := h.app.Store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		w := h.request("POST", "/api/preview", jsonBody(t, f))
		if w.Code != http.StatusOK {
			t.Fatalf("preview: %d %s", w.Code, w.Body.String())
		}
		if err := h.app.Store.Complete(ctx, f, decodeReply[model.Preview](t, w).Items, "", "", 200, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	descriptions := func(id string) []string {
		t.Helper()
		items, err := h.app.Store.Items(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(items))
		for i, item := range items {
			out[i] = item.HTML
		}
		return out
	}
	stored := func(id, what string, sponsored bool) {
		t.Helper()
		got := descriptions(id)
		if len(got) != 1 || !strings.Contains(got[0], "Mirrors and parity") || strings.Contains(got[0], "Sponsored") != sponsored {
			t.Fatalf("%s: stored descriptions %q", what, got)
		}
	}

	// The cleanup rule and the filter that needs it arrive in one edit, with
	// the history option: the story is judged on the text it was saved with.
	together := save("POST", "/api/feeds", base, false)
	refresh(together)
	stored(together, "before any rule", true)
	save("PUT", "/api/feeds/"+together, cleaned, true)
	if got := descriptions(together); len(got) != 0 {
		t.Fatalf("pruning in the edit that added the cleanup rule kept %q; if it now cleans stored descriptions first, the editor hint and docs/filtering.md must say so", got)
	}
	// The same recipe keeps the story once a refresh has cleaned it.
	refresh(together)
	stored(together, "the refresh after pruning", false)

	// The order the guide recommends: save the rule, let a refresh run, then
	// remove what does not match.
	inOrder := save("POST", "/api/feeds", base, false)
	refresh(inOrder)
	save("PUT", "/api/feeds/"+inOrder, cleaned, false)
	stored(inOrder, "a save without the history option", true)
	refresh(inOrder)
	stored(inOrder, "the refresh after the rule", false)
	save("PUT", "/api/feeds/"+inOrder, cleaned, true)
	stored(inOrder, "pruning after that refresh", false)

	page := h.request("GET", "/", nil).Body.String()
	start := strings.Index(page, `<div id="apply-filter-history"`)
	end := strings.Index(page[max(start, 0):], "</div>")
	if start < 0 || end < 0 {
		t.Fatal("the editor has no history option")
	}
	option := page[start : start+end]
	for _, want := range []string{"description as saved", "cleanup rules changed in this edit", "refresh lists"} {
		if !strings.Contains(option, want) {
			t.Errorf("the history option's hint does not mention %q: %s", want, option)
		}
	}
}
