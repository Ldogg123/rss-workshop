package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
)

// A reader polling every fifteen minutes from three devices should cost three
// validators, not three full serializations. ETag already covered readers that
// send If-None-Match; those that send only If-Modified-Since received a
// complete body on every poll.
func TestReaderRoutesHonorIfModifiedSince(t *testing.T) {
	ctx := context.Background()
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
	app := &App{Store: s, Auth: a, BaseURL: "https://reader.example", Version: "v1.0.0",
		Scheduler: scheduler.New(s, f, 2, time.Second)}
	h := app.Handler()

	id, err := s.Save(ctx, model.Feed{Title: "Gazette", URL: "https://example.com/news",
		Recipe:   model.Recipe{Type: "css", Items: ".card", Title: model.Field{Selector: "h2"}},
		Interval: 900, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, saved, []model.Item{{Key: "k", Title: "Story", URL: "https://example.com/1"}}, "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	saved, err = s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	path := "/feeds/" + saved.RSSToken + ".xml"

	get := func(headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	first := get(nil)
	if first.Code != 200 {
		t.Fatalf("first fetch returned %d", first.Code)
	}
	modified := first.Header().Get("Last-Modified")
	if modified == "" {
		t.Fatal("no Last-Modified on a reader response")
	}
	if _, err := http.ParseTime(modified); err != nil {
		t.Fatalf("Last-Modified is not an HTTP date: %q", err)
	}

	if w := get(map[string]string{"If-Modified-Since": modified}); w.Code != 304 {
		t.Errorf("an unchanged feed returned %d to If-Modified-Since, want 304", w.Code)
	}
	// A client whose timestamp predates the last refresh must get the body.
	older := saved.LastSuccess.Add(-time.Hour).UTC().Format(http.TimeFormat)
	if w := get(map[string]string{"If-Modified-Since": older}); w.Code != 200 {
		t.Errorf("a stale client returned %d, want the body", w.Code)
	}
	if w := get(map[string]string{"If-Modified-Since": "not a date"}); w.Code != 200 {
		t.Errorf("an unparseable date returned %d; it must be ignored, not trusted", w.Code)
	}

	// The ETag hashes the bytes and is the stronger validator, so it decides
	// when both are present -- otherwise a refresh inside the same second as
	// the client's timestamp could be reported as unchanged.
	etag := first.Header().Get("ETag")
	if w := get(map[string]string{"If-None-Match": etag, "If-Modified-Since": older}); w.Code != 304 {
		t.Errorf("a matching ETag with an old date returned %d, want 304", w.Code)
	}
	if w := get(map[string]string{"If-None-Match": `"stale"`, "If-Modified-Since": modified}); w.Code != 200 {
		t.Errorf("a non-matching ETag returned %d; the weaker date validator must not override it", w.Code)
	}

	// The Atom route is served by the same handler and must behave identically.
	atom := "/feeds/" + saved.RSSToken + ".atom"
	r := httptest.NewRequest("GET", atom, nil)
	r.Header.Set("If-Modified-Since", modified)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 304 {
		t.Errorf("the Atom route returned %d to If-Modified-Since, want 304", w.Code)
	}
}
