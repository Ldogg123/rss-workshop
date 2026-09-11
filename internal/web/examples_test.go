package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/extract"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
)

const examplesDir = "../../docs/examples"

// The shipped example recipes are the first thing a new user imports, and
// nothing used to read them, so a rename or a format change could leave them
// broken in the repository indefinitely. Every file goes through the real
// import endpoint, exactly as the browser sends it.
func TestShippedExampleRecipesImport(t *testing.T) {
	s, err := openTestStore(t, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	a, err := auth.New("long-test-password", "", false)
	if err != nil {
		t.Fatal(err)
	}
	f, err := fetch.New(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Store: s, Auth: a, BaseURL: "https://reader.example", Scheduler: scheduler.New(s, f, 2, time.Second)}
	h := app.Handler()
	cookie := login(t, h, app.BaseURL)
	csrf := csrfFor(t, h, cookie)

	files, err := filepath.Glob(filepath.Join(examplesDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no example recipes are shipped")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/api/recipes/preview", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", app.BaseURL)
			r.Header.Set("X-CSRF-Token", csrf)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("the import endpoint rejected this example: %d %s", w.Code, w.Body.String())
			}
			var out struct {
				Count int `json:"count"`
			}
			if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
			if out.Count == 0 {
				t.Error("the example contains no feeds")
			}
		})
	}
}

// An example that imports but extracts nothing is worse than no example. Each
// demo recipe is run against the demo site it targets, which ships beside it
// and therefore cannot drift out from under it.
func TestDemoRecipesExtractFromTheDemoSite(t *testing.T) {
	index, err := os.ReadFile(filepath.Join(examplesDir, "demo-site/index.html"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(examplesDir, "all-demo-recipes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Feeds []struct {
			Title  string       `json:"title"`
			URL    string       `json:"url"`
			Recipe model.Recipe `json:"recipe"`
		} `json:"feeds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Feeds) < 3 {
		t.Fatalf("the demo bundle has %d feeds; it should show CSS, XPath and filtering", len(doc.Feeds))
	}

	for _, feed := range doc.Feeds {
		t.Run(feed.Title, func(t *testing.T) {
			out, err := extract.Run(index, feed.URL, feed.Recipe)
			if err != nil {
				t.Fatalf("extraction failed: %v", err)
			}
			if out.Matches != 5 {
				t.Errorf("matched %d cards on the demo front page, want 5", out.Matches)
			}
			// Filtering is what the third recipe demonstrates; the others keep
			// everything. Either way every story must carry every field.
			if out.Filtered > 0 && len(out.Items) != 4 {
				t.Errorf("filtering kept %d stories, want 4", len(out.Items))
			}
			if out.Filtered == 0 && len(out.Items) != 5 {
				t.Errorf("kept %d stories, want 5", len(out.Items))
			}
			for _, it := range out.Items {
				if it.Title == "" || it.URL == "" {
					t.Errorf("story is missing a title or link: %+v", it)
				}
				if it.Published.IsZero() {
					t.Errorf("%q has no publication date; the example should demonstrate one", it.Title)
				}
				if it.Image == "" {
					t.Errorf("%q has no image; the example should demonstrate one", it.Title)
				}
				if !strings.Contains(it.HTML, "<p>") {
					t.Errorf("%q has no description: %q", it.Title, it.HTML)
				}
				if strings.Contains(it.Title, "Sponsored") && out.Filtered > 0 {
					t.Errorf("the filter example kept the sponsored card: %q", it.Title)
				}
			}
			// Absolute URLs, so a reader can follow them from anywhere.
			for _, it := range out.Items {
				if !strings.HasPrefix(it.URL, "http") || !strings.HasPrefix(it.Image, "http") {
					t.Errorf("%q has a relative link or image: %s / %s", it.Title, it.URL, it.Image)
				}
			}
		})
	}
}

// The full-content example points at a selector that must exist on the demo
// site's article pages, not only on its front page.
func TestDemoFullContentSelectorMatchesTheArticlePages(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(examplesDir, "demo-full-content-recipe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Feeds []struct {
			Recipe model.Recipe `json:"recipe"`
		} `json:"feeds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	recipe := doc.Feeds[0].Recipe
	if recipe.Full == nil || recipe.Full.Selector == "" {
		t.Fatal("the full-content example does not configure an article selector")
	}
	pages, err := filepath.Glob(filepath.Join(examplesDir, "demo-site/articles/*.html"))
	if err != nil || len(pages) == 0 {
		t.Fatalf("the demo site ships no article pages: %v", err)
	}
	for _, page := range pages {
		body, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		article, err := extract.Article(body, "http://127.0.0.1:8000/articles/"+filepath.Base(page), recipe)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(page), err)
			continue
		}
		if !strings.Contains(article, "<p>") || len(article) < 200 {
			t.Errorf("%s produced a thin article body: %q", filepath.Base(page), article)
		}
	}
}

// csrfFor reads the token the session already holds, the way the page does.
func csrfFor(t *testing.T, h http.Handler, cookie *http.Cookie) string {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/session", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out struct {
		CSRF string `json:"csrf"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil || out.CSRF == "" {
		t.Fatalf("could not read the session CSRF token: %d %v", w.Code, err)
	}
	return out.CSRF
}
