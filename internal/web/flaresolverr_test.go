package web

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"rss-workshop/internal/model"
)

// A separate fixture counter makes accidental static fetches observable while
// exposing the capacity metadata used by the authenticated editor.
type fixtureSolver struct{ *fixtureFetcher }

func (*fixtureSolver) Stats() (int, int) { return 0, 1 }

func TestFlareSolverrPreviewSnapshotAndPortableRecipe(t *testing.T) {
	h := newPortabilityHarness(t)
	solver := &fixtureSolver{&fixtureFetcher{body: []byte(`<script>sourceAttack()</script><article class="card"><h2>Solved story</h2><a href="/story">Read</a></article>`)}}
	h.app.Scheduler.FlareSolverr = solver
	f := model.Feed{Title: "Protected news", URL: "https://example.com/news", Interval: 600, Enabled: true,
		Recipe: model.Recipe{Mode: "flaresolverr", Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"}, Link: model.Field{Selector: ".//a/@href"}}}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	w := h.request("POST", "/api/preview", data)
	var preview model.Preview
	if err := json.Unmarshal(w.Body.Bytes(), &preview); w.Code != http.StatusOK || err != nil || len(preview.Items) != 1 || preview.Items[0].Title != "Solved story" || preview.Items[0].URL != "https://example.com/story" {
		t.Fatalf("solver preview: %d %s, %v", w.Code, w.Body.String(), err)
	}
	w = h.request("POST", "/api/selector/snapshot", []byte(`{"url":"https://example.com/news","recipe":{"mode":"flaresolverr"}}`))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Solved story") || strings.Contains(w.Body.String(), "sourceAttack()") {
		t.Fatalf("solver snapshot was missing or unsafe: %d %s", w.Code, w.Body.String())
	}
	if solver.calls.Load() != 2 || h.fetcher.calls.Load() != 0 || h.app.Scheduler.Renderer != nil {
		t.Fatalf("expected two solver calls without static fetch or local Chromium: solver=%d static=%d", solver.calls.Load(), h.fetcher.calls.Load())
	}
	w = h.request("GET", "/api/feeds", nil)
	var status struct {
		FlareSolverr struct{ Active, Capacity int } `json:"flaresolverr"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); w.Code != http.StatusOK || err != nil || status.FlareSolverr.Active != 0 || status.FlareSolverr.Capacity != 1 {
		t.Fatalf("missing solver capability metadata: %d %s, %v", w.Code, w.Body.String(), err)
	}
	w = h.request("POST", "/api/feeds", data)
	if w.Code != http.StatusOK {
		t.Fatalf("save solver recipe: %d %s", w.Code, w.Body.String())
	}
	w = h.request("GET", "/api/recipes/export", nil)
	var archive recipeDocument
	if err := json.Unmarshal(w.Body.Bytes(), &archive); w.Code != http.StatusOK || err != nil || len(archive.Feeds) != 1 || !reflect.DeepEqual(archive.Feeds[0].Recipe, f.Recipe) {
		t.Fatalf("export changed solver recipe: %d %s, %v", w.Code, w.Body.String(), err)
	}
	for _, privateField := range []string{"endpoint", "cookies", "session", "10.20.30.40", h.cookie.Value, h.csrf} {
		if strings.Contains(w.Body.String(), privateField) {
			t.Fatalf("archive contains server/session state: %q", privateField)
		}
	}
	archiveData := append([]byte(nil), w.Body.Bytes()...)
	// A server without FlareSolverr can still review and import a paused recipe;
	// only fetching it needs the optional service configuration.
	destination := newPortabilityHarness(t)
	if w := destination.request("POST", "/api/recipes/preview", archiveData); w.Code != http.StatusOK {
		t.Fatalf("recipe review without solver: %d %s", w.Code, w.Body.String())
	}
	w = destination.request("POST", "/api/recipes/import", archiveData)
	if w.Code != http.StatusCreated {
		t.Fatalf("import solver recipe: %d %s", w.Code, w.Body.String())
	}
	imported, err := destination.app.Store.List(context.Background())
	if err != nil || len(imported) != 1 || imported[0].Enabled || !reflect.DeepEqual(imported[0].Recipe, f.Recipe) || !imported[0].LastAttempt.IsZero() {
		t.Fatalf("import did not preserve a fresh paused solver recipe: %+v, %v", imported, err)
	}
	if solver.calls.Load() != 2 || h.fetcher.calls.Load() != 0 || destination.fetcher.calls.Load() != 0 {
		t.Fatal("save, export, review, or import fetched a page")
	}
}

func TestFlareSolverrUnavailableAPI(t *testing.T) {
	h := newPortabilityHarness(t)
	for _, request := range []struct{ path, data string }{
		{"/api/preview", `{"title":"Protected news","url":"https://example.com/news","interval":600,"recipe":{"mode":"flaresolverr","type":"css","items":"article","title":{"selector":"h2"}}}`},
		{"/api/selector/snapshot", `{"url":"https://example.com/news","recipe":{"mode":"flaresolverr"}}`},
	} {
		w := h.request("POST", request.path, []byte(request.data))
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(strings.ToLower(w.Body.String()), "flaresolverr") {
			t.Fatalf("missing solver error for %s: %d %s", request.path, w.Code, w.Body.String())
		}
	}
	w := h.request("GET", "/api/feeds", nil)
	var status struct {
		FlareSolverr struct{ Active, Capacity int } `json:"flaresolverr"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); w.Code != http.StatusOK || err != nil || status.FlareSolverr.Capacity != 0 {
		t.Fatalf("unconfigured solver metadata: %d %s, %v", w.Code, w.Body.String(), err)
	}
	if h.fetcher.calls.Load() != 0 {
		t.Fatal("missing solver silently fell back to static fetching")
	}
}
