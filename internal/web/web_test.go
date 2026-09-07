package web

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"rss-workshop/internal/auth"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fixtureFetcher struct {
	body  []byte
	calls atomic.Int32
}

func (f *fixtureFetcher) Fetch(ctx context.Context, u, e, m string) (fetch.Result, error) {
	f.calls.Add(1)
	return fetch.Result{Body: f.body, URL: u, Status: 200}, nil
}
func TestManagementAndRSSWorkflow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, e := openTestStore(t, 500)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	a, e := auth.New("long-test-password", "", false)
	if e != nil {
		t.Fatal(e)
	}
	body, e := os.ReadFile("../../testdata/cards.html")
	if e != nil {
		t.Fatal(e)
	}
	f := &fixtureFetcher{body: body}
	jobs := scheduler.New(s, f, 2, time.Second)
	app := &App{Store: s, Scheduler: jobs, Auth: a, BaseURL: "http://localhost:8080"}
	h := app.Handler()
	var cookie *http.Cookie
	csrf := ""
	req := func(method, path string, data any, origin string, token bool) *httptest.ResponseRecorder {
		var b bytes.Buffer
		if data != nil {
			_ = json.NewEncoder(&b).Encode(data)
		}
		r := httptest.NewRequest(method, path, &b)
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if token {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := req("GET", "/", nil, "", false); w.Code != 200 || !strings.Contains(w.Body.String(), "RSS Workshop") {
		t.Fatal("UI missing", w.Code)
	}
	if w := req("GET", "/api/feeds", nil, "", false); w.Code != 401 {
		t.Fatal("unauthenticated API accessible")
	}
	if w := req("POST", "/api/login", map[string]string{"password": "long-test-password"}, "https://evil.example", false); w.Code != 403 {
		t.Fatal("cross-origin login accepted")
	}
	w := req("POST", "/api/login", map[string]string{"password": "long-test-password"}, app.BaseURL, false)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookie = w.Result().Cookies()[0]
	var session map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &session)
	csrf = session["csrf"]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("weak session cookie")
	}
	input := model.Feed{Title: "Fixture news", URL: "https://example.com/page", Interval: 60, Enabled: true, Recipe: model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}, Content: model.Field{Selector: ".summary"}, Image: model.Field{Selector: "img"}}}
	if w = req("POST", "/api/feeds", input, app.BaseURL, false); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	if w = req("POST", "/api/preview", input, app.BaseURL, true); w.Code != 200 || strings.Contains(w.Body.String(), "onerror") {
		t.Fatal("bad preview", w.Body.String())
	}
	w = req("POST", "/api/feeds", input, app.BaseURL, true)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var saved map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &saved)
	id := saved["id"]
	stored, _ := s.Get(ctx, id)
	path := "/feeds/" + stored.RSSToken + ".xml"
	atomPath := "/feeds/" + stored.RSSToken + ".atom"
	before := f.calls.Load()
	if w = req("GET", path, nil, "", false); w.Code != 503 {
		t.Fatal("new feed should be pending")
	}
	if w = req("GET", atomPath, nil, "", false); w.Code != 503 || w.Header().Get("Retry-After") != "60" {
		t.Fatal("new Atom feed should be pending")
	}
	if f.calls.Load() != before {
		t.Fatal("reader triggered fetch")
	}
	jobs.Start(ctx)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		stored, _ = s.Get(ctx, id)
		if !stored.LastSuccess.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stored.LastSuccess.IsZero() {
		t.Fatal("initial refresh did not complete")
	}
	before = f.calls.Load()
	w = req("GET", path, nil, "", false)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var parsed struct {
		Channel struct {
			Items []struct {
				Title string `xml:"title"`
				GUID  string `xml:"guid"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if e = xml.Unmarshal(w.Body.Bytes(), &parsed); e != nil || len(parsed.Channel.Items) != 2 {
		t.Fatal("invalid RSS", e, w.Body.String())
	}
	if f.calls.Load() != before {
		t.Fatal("RSS fetched source")
	}
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("If-None-Match", w.Header().Get("ETag"))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Code != 304 {
		t.Fatal("conditional RSS failed", rr.Code)
	}
	w = req("GET", atomPath, nil, "", false)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/atom+xml") {
		t.Fatal("Atom reader response failed", w.Code, w.Body.String())
	}
	var atom struct {
		XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
		ID      string   `xml:"id"`
		Entries []struct {
			ID    string `xml:"id"`
			Title string `xml:"title"`
		} `xml:"entry"`
	}
	if e = xml.Unmarshal(w.Body.Bytes(), &atom); e != nil || len(atom.Entries) != len(parsed.Channel.Items) {
		t.Fatal("Atom XML does not match RSS items", e)
	}
	for i, entry := range atom.Entries {
		if entry.ID != parsed.Channel.Items[i].GUID || entry.Title != parsed.Channel.Items[i].Title {
			t.Fatal("RSS and Atom disagree on item identity/content")
		}
	}
	atomTag := w.Header().Get("ETag")
	if atomTag == r.Header.Get("If-None-Match") {
		t.Fatal("RSS and Atom shared an ETag")
	}
	r = httptest.NewRequest("GET", atomPath, nil)
	r.Header.Set("If-None-Match", `W/`+atomTag)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Code != 304 || rr.Body.Len() != 0 {
		t.Fatal("conditional Atom failed", rr.Code)
	}
	if f.calls.Load() != before {
		t.Fatal("Atom reader triggered a source fetch")
	}
	oldAtomID := atom.ID
	if w = req("POST", "/api/feeds/"+id+"/rotate-token", map[string]bool{}, app.BaseURL, true); w.Code != 200 {
		t.Fatal("rotate failed")
	}
	if w = req("GET", path, nil, "", false); w.Code != 404 {
		t.Fatal("old token still valid")
	}
	if w = req("GET", atomPath, nil, "", false); w.Code != 404 {
		t.Fatal("old Atom token still valid")
	}
	stored, _ = s.Get(ctx, id)
	w = req("GET", "/feeds/"+stored.RSSToken+".atom", nil, "", false)
	atom.Entries = nil
	if e = xml.Unmarshal(w.Body.Bytes(), &atom); e != nil || atom.ID != oldAtomID {
		t.Fatal("rotating links changed Atom identity", e)
	}
	if w = req("POST", "/api/logout", map[string]bool{}, app.BaseURL, true); w.Code != 200 {
		t.Fatal("logout failed")
	}
	if w = req("GET", "/api/feeds", nil, "", false); w.Code != 401 {
		t.Fatal("session still valid")
	}
	cancel()
	jobs.Wait()
}
