package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/model"
)

func TestRunHistoryAccessAndReadOnly(t *testing.T) {
	h := newPortabilityHarness(t)
	ctx := context.Background()
	f := model.Feed{Title: "History", URL: "https://example.com/private-source", Interval: 60, Recipe: model.Recipe{Mode: "static", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}}
	id, err := h.app.Store.Save(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	f, err = h.app.Store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/feeds/" + id + "/runs"
	empty := h.request("GET", path, nil)
	if empty.Code != 200 || !strings.Contains(empty.Body.String(), `"runs":[]`) {
		t.Fatalf("empty: %d %s", empty.Code, empty.Body.String())
	}
	items := []model.Item{{Key: "saved", Title: "Saved item", URL: "https://example.com/private-item"}}
	if err := h.app.Store.Complete(ctx, f, items, "private-etag", "private-validator", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	matches, valid := 20, 0
	trace := &model.RunDiagnostics{Version: 1, RecipeVersion: f.Version, RequestedMode: "static", SelectorType: "css", Started: time.Now(), Attempts: []model.FetchAttempt{{Mode: "static", Outcome: "failed", Stage: "extract", Status: 200, Matches: &matches, Items: &valid, Warnings: []string{"Match 1 skipped: missing or unsafe item URL"}, Error: "could not load https://example.com/private?token=hidden"}}}
	if err := h.app.Store.CompleteWithDiagnostics(ctx, f, nil, "", "", 200, errors.New("matching failed"), 0, trace); err != nil {
		t.Fatal(err)
	}
	before, _ := h.app.Store.Get(ctx, id)
	w := h.request("GET", path, nil)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("history: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Runs  []model.Run `json:"runs"`
		Limit int         `json:"limit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Limit != 50 || len(out.Runs) != 2 || out.Runs[0].Diagnostics == nil || out.Runs[1].Diagnostics != nil || out.Runs[0].Error != "matching failed" || *out.Runs[0].Diagnostics.Attempts[0].Matches != 20 {
		t.Fatalf("history shape/order: %+v", out)
	}
	for _, private := range []string{f.RSSToken, h.cookie.Value, h.csrf, "private-source", "private-item", "private-etag", "private-validator", "hidden", "Saved item"} {
		if private != "" && strings.Contains(w.Body.String(), private) {
			t.Fatalf("history leaked private state %q", private)
		}
	}
	after, _ := h.app.Store.Get(ctx, id)
	saved, _ := h.app.Store.Items(ctx, id)
	if !reflect.DeepEqual(before, after) || len(saved) != 1 || h.fetcher.calls.Load() != 0 {
		t.Fatal("reading logs mutated feed/history or fetched source")
	}
	unauth := httptest.NewRecorder()
	h.handler.ServeHTTP(unauth, httptest.NewRequest("GET", path, nil))
	if unauth.Code != 401 {
		t.Fatalf("unauthenticated history: %d", unauth.Code)
	}
	if missing := h.request("GET", "/api/feeds/missing/runs", nil); missing.Code != 404 {
		t.Fatalf("missing history: %d", missing.Code)
	}
	if err := h.app.Store.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if deleted := h.request("GET", path, nil); deleted.Code != 404 {
		t.Fatalf("deleted history: %d", deleted.Code)
	}
	h.app.Store.DB.Close()
	if closed := h.request("GET", path, nil); closed.Code != 500 || strings.Contains(closed.Body.String(), "sql:") {
		t.Fatalf("storage failure: %d %s", closed.Code, closed.Body.String())
	}
}

func TestPreviewReturnsTraceWithoutPersisting(t *testing.T) {
	h := newPortabilityHarness(t)
	f := model.Feed{Title: "Draft", URL: "https://example.com", Interval: 60, Recipe: model.Recipe{Mode: "static", Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"}}}
	for _, tc := range []struct {
		name    string
		link    model.Field
		status  int
		outcome string
	}{
		{"success", model.Field{}, 200, "success"},
		{"missing link", model.Field{Selector: ".//a", Attr: "href"}, 422, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.Recipe.Link = tc.link
			data, _ := json.Marshal(f)
			w := h.request("POST", "/api/preview", data)
			if w.Code != tc.status {
				t.Fatalf("preview: %d %s", w.Code, w.Body.String())
			}
			var p model.Preview
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.Diagnostics == nil || len(p.Diagnostics.Attempts) != 1 || p.Matches != 1 {
				t.Fatalf("preview omitted trace: %+v", p)
			}
			a := p.Diagnostics.Attempts[0]
			if a.Outcome != tc.outcome || a.Status != 200 || a.Stage != "extract" || *a.Matches != 1 || a.Bytes == 0 {
				t.Fatalf("wrong preview attempt: %+v", a)
			}
			if tc.status == 422 && (len(a.Warnings) == 0 || *a.Items != 0) {
				t.Fatal("failed preview lost rejection details")
			}
		})
	}
	for _, table := range []string{"feeds", "items", "runs"} {
		var count int
		if err := h.app.Store.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("preview wrote %s: %d, %v", table, count, err)
		}
	}
}
