package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"rss-workshop/internal/filter"
	"rss-workshop/internal/store"
)

// The editor shows and enforces the same bounds the server applies. Rendering
// them from internal/filter and internal/store keeps the embedded assets from
// drifting away from the Go values, which each number was previously repeated by hand.
func TestServerBoundsReachTheEditor(t *testing.T) {
	rec := httptest.NewRecorder()
	(&App{}).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 {
		t.Fatalf("dashboard returned %d", rec.Code)
	}
	page := rec.Body.String()
	for attr, want := range map[string]int{
		"data-max-keywords":           filter.MaxKeywords,
		"data-max-nodes":              filter.MaxNodes,
		"data-max-keyword-characters": filter.MaxKeywordCharacters,
		"data-max-depth":              filter.MaxDepth,
		"data-max-runs":               store.MaxRuns,
	} {
		if pair := attr + `="` + strconv.Itoa(want) + `"`; !strings.Contains(page, pair) {
			t.Errorf("dashboard does not carry %s; the editor cannot match the validator", pair)
		}
	}

	// The editor's help sentence quotes all four filter bounds in prose.
	help := fmt.Sprintf("Paste up to %d phrases across both panels, one per line. Each phrase can contain up to %d characters. Use up to %d rules and %d levels of nesting.",
		filter.MaxKeywords, filter.MaxKeywordCharacters, filter.MaxNodes, filter.MaxDepth)
	if !strings.Contains(page, help) {
		t.Errorf("filter help text does not quote the real bounds; wanted %q", help)
	}

	// The retention sentence and the diagnostics list must quote the same number.
	if !strings.Contains(page, "The latest "+strconv.Itoa(store.MaxRuns)+" refresh runs") {
		t.Errorf("diagnostics panel does not describe the real %d-run retention", store.MaxRuns)
	}

	script, e := files.ReadFile("assets/filters.js")
	if e != nil {
		t.Fatal(e)
	}
	// Reading the rendered values is what keeps the two in step; a literal bound
	// here would compile and pass every other test while lying to the operator.
	for _, key := range []string{"maxKeywords", "maxNodes", "maxDepth", "maxKeywordCharacters"} {
		if !strings.Contains(string(script), "dataset."+key) {
			t.Errorf("filters.js no longer reads dataset.%s", key)
		}
	}
	for _, bound := range []int{filter.MaxKeywords, filter.MaxNodes, filter.MaxKeywordCharacters} {
		if strings.Contains(string(script), strconv.Itoa(bound)) {
			t.Errorf("filters.js hardcodes %d again instead of using the rendered bound", bound)
		}
	}

	history, e := files.ReadFile("assets/diagnostics.js")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(history), "dataset.maxRuns") {
		t.Error("diagnostics.js no longer reads dataset.maxRuns")
	}
	if strings.Contains(string(history), "slice(0,"+strconv.Itoa(store.MaxRuns)+")") {
		t.Errorf("diagnostics.js hardcodes the %d-run cap again", store.MaxRuns)
	}
}
