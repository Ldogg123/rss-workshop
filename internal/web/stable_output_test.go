package web

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
	"rss-workshop/internal/store"
)

type readerResponse struct {
	code          int
	body          string
	etag, lastMod string
}

func readerApp(t *testing.T, s *store.Store, version string) http.Handler {
	t.Helper()
	a, err := auth.New("long-test-password", "", false)
	if err != nil {
		t.Fatal(err)
	}
	f, err := fetch.New(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	return (&App{Store: s, Auth: a, BaseURL: "https://reader.example", Version: version, Scheduler: scheduler.New(s, f, 1, time.Second)}).Handler()
}

func readFeed(t *testing.T, h http.Handler, path string, headers map[string]string) readerResponse {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return readerResponse{w.Code, w.Body.String(), w.Header().Get("ETag"), w.Header().Get("Last-Modified")}
}

// AGENTS.md: unchanged stories produce identical bytes and a polling reader
// keeps receiving 304. Published dates used to come from the last refresh, so
// every refresh -- even a 304 from the source -- changed every feed.
func TestReaderOutputStableAcrossUnchangedRefreshes(t *testing.T) {
	ctx := t.Context()
	s, err := openTestStore(t, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	h := readerApp(t, s, "v1.1.0")
	id, err := s.Save(ctx, model.Feed{Title: "Gazette", URL: "https://example.com/news", Interval: 900, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: ".card", Title: model.Field{Selector: "h2"}}})
	if err != nil {
		t.Fatal(err)
	}
	stories := []model.Item{
		{Key: "https://example.com/1", Title: "Story one", URL: "https://example.com/1", HTML: "<p>One</p>", Published: time.Unix(1757505600, 0)},
		{Key: "https://example.com/2", Title: "Story two", URL: "https://example.com/2", HTML: "<p>Two</p>", Published: time.Unix(1757419200, 0)},
	}
	refresh := func(items []model.Item, status int) model.Feed {
		t.Helper()
		f, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Complete(ctx, f, items, "source-etag", "", status, nil, 0); err != nil {
			t.Fatal(err)
		}
		if f, err = s.Get(ctx, id); err != nil {
			t.Fatal(err)
		}
		return f
	}
	f := refresh(stories, 200)
	paths := []string{"/feeds/" + f.RSSToken + ".xml", "/feeds/" + f.RSSToken + ".atom"}
	before := map[string]readerResponse{}
	for _, path := range paths {
		before[path] = readFeed(t, h, path, nil)
	}

	// A later refresh with the same stories, and one where the source says
	// nothing changed. One second granularity would hide a regression.
	time.Sleep(1100 * time.Millisecond)
	refresh(stories, 200)
	time.Sleep(1100 * time.Millisecond)
	refresh(nil, 304)
	for _, path := range paths {
		after := readFeed(t, h, path, nil)
		if after != before[path] {
			t.Fatalf("%s changed after refreshes that changed nothing:\nbefore %s %s\n%s\nafter %s %s\n%s", path, before[path].etag, before[path].lastMod, before[path].body, after.etag, after.lastMod, after.body)
		}
		if w := readFeed(t, h, path, map[string]string{"If-None-Match": before[path].etag}); w.code != 304 {
			t.Fatalf("%s answered %d to its earlier ETag", path, w.code)
		}
		if w := readFeed(t, h, path, map[string]string{"If-Modified-Since": before[path].lastMod}); w.code != 304 {
			t.Fatalf("%s answered %d to its earlier Last-Modified", path, w.code)
		}
	}

	// A changed story changes the output and moves Last-Modified, even within
	// the same second as the previous change.
	changed := append([]model.Item(nil), stories...)
	changed[0].Title = "Story one, updated"
	refresh(changed, 200)
	for _, path := range paths {
		after := readFeed(t, h, path, nil)
		if after.etag == before[path].etag || after.lastMod == before[path].lastMod {
			t.Fatalf("%s did not change with a story: %+v", path, after)
		}
		if w := readFeed(t, h, path, map[string]string{"If-Modified-Since": before[path].lastMod}); w.code != 200 {
			t.Fatalf("%s answered %d to a Last-Modified from before the change", path, w.code)
		}
		before[path] = after
	}

	// Renaming a paused feed changes its output with no refresh at all.
	f, err = s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	f.Enabled, f.Title = false, "Gazette weekly"
	if _, err := s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if w := readFeed(t, h, path, map[string]string{"If-Modified-Since": before[path].lastMod}); w.code != 200 {
			t.Fatalf("%s answered %d after renaming a paused feed", path, w.code)
		}
	}
}

// The version in the RSS generator made every upgrade rewrite every RSS feed.
func TestUpgradingTheAppKeepsReaderValidators(t *testing.T) {
	ctx := t.Context()
	s, err := openTestStore(t, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	id, err := s.Save(ctx, model.Feed{Title: "Gazette", URL: "https://example.com/news", Interval: 900, Enabled: true,
		Recipe: model.Recipe{Type: "css", Items: ".card", Title: model.Field{Selector: "h2"}}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, f, []model.Item{{Key: "k", Title: "Story", URL: "https://example.com/1"}}, "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	if f, err = s.Get(ctx, id); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/feeds/" + f.RSSToken + ".xml", "/feeds/" + f.RSSToken + ".atom"} {
		old := readFeed(t, readerApp(t, s, "v1.1.0"), path, nil)
		upgraded := readerApp(t, s, "v1.2.0")
		if w := readFeed(t, upgraded, path, map[string]string{"If-None-Match": old.etag}); w.code != 304 {
			t.Fatalf("%s answered %d to the previous version's ETag", path, w.code)
		}
	}
}

// Upgrading a v1.0.0 database must not change a published byte: v1.0.0 dated
// the RSS channel and Last-Modified from last_success and each Atom entry from
// last_seen, and the first refresh afterwards changes nothing either.
func TestSchemaFiveUpgradePublishesTheSameOutput(t *testing.T) {
	if os.Getenv("RSS_TEST_POSTGRES_URL") != "" {
		t.Skip("uses an SQLite schema-4 fixture; PostgreSQL migration values are covered in internal/store")
	}
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "rss.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../store/testdata/schema_v4.sql")
	if err != nil {
		t.Fatal(err)
	}
	const lastSuccess, lastSeen = 1757592000, 1757588400
	for _, q := range []string{
		string(schema),
		`INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run,last_success,version) VALUES('feed','token','Gazette','https://example.com/news','{"mode":"static","type":"css","items":".card","title":{"selector":"h2"}}',900,1,0,1757592000,1)`,
		`INSERT INTO items(feed_id,key,guid,title,url,html,image,content_full,published,first_seen,last_seen) VALUES('feed','https://example.com/1','urn:sha256:1','Story one','https://example.com/1','<p>One</p>','','',1757505600,1757505600,1757588400)`,
	} {
		if _, err := legacy.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	legacy.Close()
	s, err := store.Open(path, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	h := readerApp(t, s, "v1.1.0")
	rss, atom := readFeed(t, h, "/feeds/token.xml", nil), readFeed(t, h, "/feeds/token.atom", nil)
	want := time.Unix(lastSuccess, 0).UTC()
	if rss.lastMod != want.Format(http.TimeFormat) || atom.lastMod != rss.lastMod {
		t.Fatalf("Last-Modified after upgrade: %q %q", rss.lastMod, atom.lastMod)
	}
	if !regexp.MustCompile(`<lastBuildDate>` + regexp.QuoteMeta(want.Format(time.RFC1123Z)) + `</lastBuildDate>`).MatchString(rss.body) {
		t.Fatalf("lastBuildDate after upgrade:\n%s", rss.body)
	}
	if !regexp.MustCompile(`<updated>` + regexp.QuoteMeta(time.Unix(lastSeen, 0).UTC().Format(time.RFC3339Nano)) + `</updated>\s*<content`).MatchString(atom.body) {
		t.Fatalf("Atom entry updated after upgrade:\n%s", atom.body)
	}

	time.Sleep(1100 * time.Millisecond)
	f, err := s.Get(ctx, "feed")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, f, []model.Item{{Key: "https://example.com/1", Title: "Story one", URL: "https://example.com/1", HTML: "<p>One</p>", Published: time.Unix(1757505600, 0)}}, "", "", 200, nil, 0); err != nil {
		t.Fatal(err)
	}
	if after := readFeed(t, h, "/feeds/token.xml", nil); after != rss {
		t.Fatalf("RSS changed at the first refresh after upgrading:\n%s\n%s", rss.body, after.body)
	}
	if after := readFeed(t, h, "/feeds/token.atom", nil); after != atom {
		t.Fatalf("Atom changed at the first refresh after upgrading:\n%s\n%s", atom.body, after.body)
	}
}
