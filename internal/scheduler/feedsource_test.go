package scheduler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/feed"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/notify"
)

const (
	htmlAccept = "text/html, application/xhtml+xml"
	feedAccept = "application/rss+xml, application/atom+xml, application/rdf+xml;q=0.9, application/xml;q=0.8, text/xml;q=0.8, */*;q=0.1"
)

// A feed source through the real guarded fetcher: only its list request asks
// for a feed and is decoded by its XML declaration, while its article pages
// are HTML. Preview and refresh keep the same stories, a 304 publishes the
// same bytes, and the feed's own request headers go with every request.
func TestFeedSourceRefreshThroughTheRealFetcher(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	list := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n<rss version=\"2.0\"><channel><title>Source</title>" +
		"<item><title>Caf\xe9 cr\xe8me</title><link>/a1</link><guid isPermaLink=\"false\">1</guid><description>Teaser one</description><pubDate>Thu, 05 Nov 2026 10:00:00 GMT</pubDate></item>" +
		"<item><title>Sponsored: windows</title><link>/a2</link><guid isPermaLink=\"false\">2</guid><description>An advert</description></item>" +
		"<item><title>Also kept</title><link>/a3</link><guid isPermaLink=\"false\">3</guid><description>Teaser three</description><pubDate>Wed, 04 Nov 2026 10:00:00 GMT</pubDate></item>" +
		"</channel></rss>"
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" | "+r.Header.Get("Accept")+" | "+r.UserAgent()+" | "+r.Header.Get("Accept-Language")+" | "+r.Header.Get("If-None-Match"))
		mu.Unlock()
		switch r.URL.Path {
		case "/feed.xml":
			if r.Header.Get("If-None-Match") == `"f1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"f1"`)
			w.Header().Set("Content-Type", "application/rss+xml")
			w.Write([]byte(list))
		case "/a1", "/a3":
			w.Write([]byte(`<html><body><article class="body"><p>Full ` + r.URL.Path + `</p></article></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()
	take := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := seen
		seen = nil
		slices.Sort(out)
		return out
	}

	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	client, err := fetch.New(time.Second, "127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	jobs := New(s, client, 2, 5*time.Second)
	f := model.Feed{Title: "Feed source", URL: source.URL + "/feed.xml", Interval: 900, Enabled: true, Recipe: model.Recipe{
		Mode: "static", Type: extract.FeedType, UserAgent: "Feeds/1", AcceptLanguage: "fr",
		Full:    &model.FullContent{Selector: "article.body"},
		Filters: &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"Sponsored"}}},
	}}
	request := func(path, accept, etag string) string {
		return path + " | " + accept + " | Feeds/1 | fr | " + etag
	}
	everything := []string{request("/a1", htmlAccept, ""), request("/a3", htmlAccept, ""), request("/feed.xml", feedAccept, "")}

	preview, err := jobs.Preview(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if got := take(); !slices.Equal(got, everything) {
		t.Fatalf("the preview sent\n%q\nwant\n%q", got, everything)
	}
	if feeds, err := s.List(ctx); err != nil || len(feeds) != 0 {
		t.Fatalf("a preview stored a feed: %v, %v", feeds, err)
	}

	if f.ID, err = s.Save(ctx, f); err != nil {
		t.Fatal(err)
	}
	jobs.refresh(ctx, reload(t, s, f.ID))
	if got := take(); !slices.Equal(got, everything) {
		t.Fatalf("the first refresh sent\n%q\nwant\n%q", got, everything)
	}
	first := reload(t, s, f.ID)
	items, err := s.Items(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Error != "" || len(items) != 2 || len(preview.Items) != 2 || preview.Filtered != 1 {
		t.Fatalf("first refresh: error %q, %d stored, preview %+v", first.Error, len(items), preview)
	}
	for i, it := range items {
		p := preview.Items[i]
		if it.Title != p.Title || it.URL != p.URL || it.HTML != p.HTML || !it.Published.Equal(p.Published) || it.FullHTML != p.FullHTML || it.FullHTML == "" {
			t.Errorf("stored story %d differs from its preview:\n%+v\n%+v", i, it, p)
		}
	}
	if items[0].Title != "Café crème" || items[0].URL != source.URL+"/a1" || items[0].FullHTML != "<article><p>Full /a1</p></article>" || !items[0].Published.Equal(time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("first story: %+v", items[0])
	}
	render := func(f model.Feed, items []model.Item) (string, string) {
		rss, _, err := feed.Render(f, items, "https://reader.example/feeds/x.xml", "RSS Workshop")
		if err != nil {
			t.Fatal(err)
		}
		atom, _, err := feed.RenderAtom(f, items, "https://reader.example")
		if err != nil {
			t.Fatal(err)
		}
		return string(rss), string(atom)
	}
	rss, atom := render(first, items)

	// The next refresh is conditional and the source has not changed.
	jobs.refresh(ctx, first)
	if got := take(); !slices.Equal(got, []string{request("/feed.xml", feedAccept, `"f1"`)}) {
		t.Fatalf("the second refresh sent %q", got)
	}
	second := reload(t, s, f.ID)
	again, err := s.Items(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	rss2, atom2 := render(second, again)
	if second.Error != "" || !second.LastChanged.Equal(first.LastChanged) || rss2 != rss || atom2 != atom || len(again) != 2 || again[0].GUID != items[0].GUID || again[1].GUID != items[1].GUID {
		t.Fatalf("a 304 changed the published feed: error %q, last_changed %s then %s", second.Error, first.LastChanged, second.LastChanged)
	}
}

// A feed that cannot be read is described by line only, in the log line and
// in the notification as well as the card, because encoding/xml quotes the
// document in its errors.
func TestFeedSourceErrorsReachLogAndNotifierWithoutSourceText(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	body := "<rss><channel>\n<item><title>secret story</title><a:secret xmlns:a=\"https://secret.example/a\" xmlns:c=\"https://secret.example/c\">y</c:secret></item></channel></rss>"
	jobs := New(s, &staticFetcher{body: []byte(body)}, 1, 10*time.Second)
	h, _ := alerting(jobs, 1)
	log := captureLog(t)
	id, err := s.Save(ctx, model.Feed{Title: "Broken feed", URL: "https://feed.example/rss", Interval: 60, Enabled: true, Recipe: model.Recipe{Mode: "static", Type: extract.FeedType}})
	if err != nil {
		t.Fatal(err)
	}
	jobs.refresh(ctx, reload(t, s, id))
	stored := reload(t, s, id)
	events := h.take()
	if len(events) != 1 || events[0].Kind != notify.Failing || !strings.Contains(events[0].Error, "line 2") || !strings.Contains(stored.Error, "line 2") {
		t.Fatalf("events %+v, stored error %q", events, stored.Error)
	}
	if !bytes.Contains(log.Bytes(), []byte("refresh failed")) {
		t.Fatalf("no failure was logged: %s", log)
	}
	for _, text := range []string{events[0].Error, stored.Error, log.String()} {
		if strings.Contains(text, "secret") {
			t.Fatalf("source text escaped into %q", text)
		}
	}
}
