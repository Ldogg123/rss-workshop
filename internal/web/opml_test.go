package web

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/model"
)

// Mirrors what a reader parses: only the attributes it needs to subscribe.
type importedOPML struct {
	Version  string `xml:"version,attr"`
	Title    string `xml:"head>title"`
	Outlines []struct {
		Type    string `xml:"type,attr"`
		Text    string `xml:"text,attr"`
		Title   string `xml:"title,attr"`
		XMLURL  string `xml:"xmlUrl,attr"`
		HTMLURL string `xml:"htmlUrl,attr"`
	} `xml:"body>outline"`
}

func TestOPMLExport(t *testing.T) {
	ctx := context.Background()
	s, e := openTestStore(t, 500)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	a, e := auth.New("long-test-password", "", false)
	if e != nil {
		t.Fatal(e)
	}
	app := &App{Store: s, Auth: a, BaseURL: "https://reader.example"}
	h := app.Handler()

	// A quoted, angle-bracketed title proves attribute escaping survives a
	// round trip; a paused feed proves its saved stories stay reachable.
	recipe := model.Recipe{Type: "css", Items: ".card", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}}
	live := model.Feed{Title: `Ars & "Tech" <news>`, URL: "https://example.com/news", Recipe: recipe, Interval: 900, Enabled: true}
	paused := model.Feed{Title: "Paused Blog", URL: "https://example.org/blog", Recipe: recipe, Interval: 900, Enabled: false}
	for _, f := range []model.Feed{live, paused} {
		id, e := s.Save(ctx, f)
		if e != nil {
			t.Fatal(e)
		}
		saved, e := s.Get(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		// Give each feed one successful refresh so its reader link serves a
		// feed rather than the 503 a never-refreshed feed correctly returns.
		if e := s.Complete(ctx, saved, []model.Item{{Key: "one", Title: "First story", URL: f.URL + "/one"}}, "", "", 200, nil, 0); e != nil {
			t.Fatal(e)
		}
	}

	if w := get(t, h, "/api/opml", nil); w.Code != 401 {
		t.Fatalf("unauthenticated OPML export returned %d; reader links are bearer credentials", w.Code)
	}
	cookie := login(t, h, app.BaseURL)

	w := get(t, h, "/api/opml", cookie)
	if w.Code != 200 {
		t.Fatalf("OPML export returned %d", w.Code)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="rss-workshop.opml"`) {
		t.Errorf("export is not offered as a download: %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control is %q; a list of reader links must not be cached", got)
	}

	var doc importedOPML
	if e := xml.Unmarshal(w.Body.Bytes(), &doc); e != nil {
		t.Fatalf("a reader could not parse the export: %v", e)
	}
	if doc.Version != "2.0" || doc.Title != "RSS Workshop" {
		t.Errorf("unexpected OPML head: version=%q title=%q", doc.Version, doc.Title)
	}
	if len(doc.Outlines) != 2 {
		t.Fatalf("exported %d outlines, want both the live and the paused feed", len(doc.Outlines))
	}

	byTitle := map[string]string{}
	for _, o := range doc.Outlines {
		if o.Type != "rss" {
			t.Errorf("outline %q has type %q; readers expect rss for any feed", o.Title, o.Type)
		}
		if o.Text != o.Title {
			t.Errorf("text %q and title %q differ; readers display either one", o.Text, o.Title)
		}
		byTitle[o.Title] = o.XMLURL
	}
	// Escaping is only real if the special characters come back intact.
	link, ok := byTitle[`Ars & "Tech" <news>`]
	if !ok {
		t.Fatalf("title lost its special characters: %v", byTitle)
	}
	if !strings.HasPrefix(link, "https://reader.example/feeds/") || !strings.HasSuffix(link, ".xml") {
		t.Errorf("xmlUrl %q is not a public RSS reader link", link)
	}
	if _, ok := byTitle["Paused Blog"]; !ok {
		t.Error("paused feed was omitted; its link still serves saved stories")
	}

	// The advertised link must be the one the server actually serves, without
	// a session: a reader holds only the token in the URL.
	served := get(t, h, strings.TrimPrefix(link, "https://reader.example"), nil)
	if served.Code != 200 {
		t.Fatalf("exported xmlUrl returned %d; the OPML would not work in a reader", served.Code)
	}
	if got := served.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/rss+xml") {
		t.Errorf("exported link served %q, not an RSS feed", got)
	}
	if !strings.Contains(served.Body.String(), "First story") {
		t.Error("exported link served no stories")
	}

	atom := get(t, h, "/api/opml?format=atom", cookie)
	if atom.Code != 200 {
		t.Fatalf("atom OPML returned %d", atom.Code)
	}
	// A distinct filename keeps the two lists apart in a downloads folder.
	if got := atom.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="rss-workshop-atom.opml"`) {
		t.Errorf("atom export reuses the RSS filename: %q", got)
	}
	var atomDoc importedOPML
	if e := xml.Unmarshal(atom.Body.Bytes(), &atomDoc); e != nil {
		t.Fatal(e)
	}
	for _, o := range atomDoc.Outlines {
		if !strings.HasSuffix(o.XMLURL, ".atom") {
			t.Errorf("format=atom advertised %q", o.XMLURL)
		}
	}
	// Formats are matched exactly; a near miss is a typo worth reporting rather
	// than silently serving the default list.
	for _, bad := range []string{"json", "RSS", "Atom", " rss"} {
		if w := get(t, h, "/api/opml?format="+url.QueryEscape(bad), cookie); w.Code != 400 {
			t.Errorf("format=%q returned %d, want 400", bad, w.Code)
		}
	}
}

func TestOPMLExportWithNoFeeds(t *testing.T) {
	s, e := openTestStore(t, 500)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	a, e := auth.New("long-test-password", "", false)
	if e != nil {
		t.Fatal(e)
	}
	h := (&App{Store: s, Auth: a, BaseURL: "https://reader.example"}).Handler()
	w := get(t, h, "/api/opml", login(t, h, "https://reader.example"))
	if w.Code != 200 {
		t.Fatalf("empty library returned %d; an empty list is not an error", w.Code)
	}
	var doc importedOPML
	if e := xml.Unmarshal(w.Body.Bytes(), &doc); e != nil {
		t.Fatalf("empty export is not valid OPML: %v", e)
	}
	if len(doc.Outlines) != 0 {
		t.Errorf("empty library exported %d outlines", len(doc.Outlines))
	}
}

func get(t *testing.T, h http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func login(t *testing.T, h http.Handler, base string) *http.Cookie {
	t.Helper()
	body := strings.NewReader(`{"password":"long-test-password"}`)
	r := httptest.NewRequest("POST", "/api/login", body)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", base)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login failed: %d", w.Code)
	}
	_ = json.NewDecoder(w.Body).Decode(new(map[string]string))
	if cookies := w.Result().Cookies(); len(cookies) > 0 {
		return cookies[0]
	}
	t.Fatal("login set no session cookie")
	return nil
}
