package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
)

// A windows-1252 site through the real static fetcher. The visual selector's
// snapshot, the list page and the article page must all read its text the way
// a browser does, or the editor shows one thing and the feed publishes another.
// The list page declares its encoding in a <meta>, the article page in its
// Content-Type header.
func TestStaticPagesDecodeAlikeInTheSelectorListAndArticle(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><head><meta charset="windows-1252"></head><body>` +
				"<article><h2>Caf\xe9 cr\xe8me</h2><a href=\"/story\">Read</a><p>Cr\xe8me br\xfbl\xe9e</p></article>" +
				"<article><h2>Th\xe9 vert</h2><a href=\"/sponsored\">Read</a><p>Sponsoris\xe9</p></article></body></html>"))
		case "/story":
			w.Header().Set("Content-Type", "text/html; charset=windows-1252")
			w.Write([]byte("<html><body><div class=\"body\"><p>D\xe9j\xe0 vu</p></div></body></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()

	s, err := openTestStore(t, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	authn, err := auth.New("long-test-password", "", false)
	if err != nil {
		t.Fatal(err)
	}
	client, err := fetch.New(time.Second, "127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Store: s, Auth: authn, BaseURL: "https://reader.example", Scheduler: scheduler.New(s, client, 2, 5*time.Second)}
	h := app.Handler()
	cookie := login(t, h, app.BaseURL)
	csrf := csrfFor(t, h, cookie)
	post := func(path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", app.BaseURL)
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w
	}

	var snapshot struct {
		Nodes []selectorNode `json:"nodes"`
	}
	w := post("/api/selector/snapshot", map[string]any{"url": source.URL + "/list", "recipe": model.Recipe{Mode: "static"}})
	if err := json.NewDecoder(w.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	texts := []string{}
	for _, n := range snapshot.Nodes {
		if n.Text != "" {
			texts = append(texts, n.Text)
		}
	}
	if got := strings.Join(texts, "|"); got != "Café crème|Read|Crème brûlée|Thé vert|Read|Sponsorisé" {
		t.Errorf("the selector snapshot shows %q", got)
	}

	// Filters match the decoded text too, so a non-ASCII keyword works.
	recipe := model.Recipe{Mode: "static", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"},
		Content: model.Field{Selector: "p"}, Full: &model.FullContent{Selector: ".body"},
		Filters: &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "description", Keywords: []string{"sponsorisé"}}}}
	w = post("/api/preview", model.Feed{Title: "Café", URL: source.URL + "/list", Interval: 900, Recipe: recipe})
	var preview model.Preview
	if err := json.NewDecoder(w.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Items) != 1 || preview.Filtered != 1 || len(preview.Warnings) != 0 {
		t.Fatalf("preview kept %d stories, filtered %d, warned %q", len(preview.Items), preview.Filtered, preview.Warnings)
	}
	it := preview.Items[0]
	if it.Title != "Café crème" || it.HTML != "<p>Crème brûlée</p>" {
		t.Errorf("the list page gave %q, %q", it.Title, it.HTML)
	}
	if it.FullHTML != "<div><p>Déjà vu</p></div>" {
		t.Errorf("the article page gave %q", it.FullHTML)
	}
}
