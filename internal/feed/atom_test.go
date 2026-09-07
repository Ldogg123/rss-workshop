package feed_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/feed"
	"rss-workshop/internal/model"
)

type parsedAtom struct {
	XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
	ID      string   `xml:"id"`
	Title   struct {
		Type string `xml:"type,attr"`
		Text string `xml:",chardata"`
	} `xml:"title"`
	Updated string `xml:"updated"`
	Author  struct {
		Name string `xml:"name"`
		URI  string `xml:"uri"`
	} `xml:"author"`
	Links   []parsedAtomLink  `xml:"link"`
	Entries []parsedAtomEntry `xml:"entry"`
}

type parsedAtomLink struct {
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
}

type parsedAtomEntry struct {
	ID        string           `xml:"id"`
	Title     string           `xml:"title"`
	Published string           `xml:"published"`
	Updated   string           `xml:"updated"`
	Links     []parsedAtomLink `xml:"link"`
	Content   struct {
		Type     string `xml:"type,attr"`
		Text     string `xml:",chardata"`
		Children []struct {
			XMLName xml.Name
		} `xml:",any"`
	} `xml:"content"`
}

func readAtom(t *testing.T, body []byte) parsedAtom {
	t.Helper()
	var doc parsedAtom
	if err := xml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("invalid Atom XML: %v\n%s", err, body)
	}
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Space != "http://www.w3.org/2005/Atom" {
			t.Fatalf("element %q is outside Atom namespace: %q", start.Name.Local, start.Name.Space)
		}
	}
	return doc
}

func TestAtomReaderContentAndMetadata(t *testing.T) {
	published := time.Date(2026, 9, 6, 12, 30, 0, 0, time.FixedZone("source", 2*60*60))
	observed := published.Add(time.Hour)
	f := model.Feed{ID: "persistent-feed", RSSToken: "private-token", Title: `News & <updates> — café`, URL: "https://example.com/news/", LastSuccess: observed.Add(time.Minute)}
	it := model.Item{
		Key: "one", GUID: "urn:sha256:abc123", Title: `One & </title><entry>injection`, URL: "../story?one=1&two=2",
		HTML:      `<p>Escaped &amp; <b>formatted</b> <![CDATA[text]]></p><img src="https://example.com/image.jpg">`,
		Published: published, FirstSeen: observed, LastSeen: observed,
	}
	body, etag, err := feed.RenderAtom(f, []model.Item{it}, "https://rss.example.com/workshop/")
	if err != nil {
		t.Fatal(err)
	}
	doc := readAtom(t, body)
	if doc.Title.Text != f.Title || doc.Title.Type != "text" || doc.Author.Name != f.Title || doc.Author.URI != f.URL {
		t.Fatalf("lost feed metadata: %+v", doc)
	}
	if doc.ID == "" || doc.Updated != "2026-09-06T11:31:00Z" || len(doc.Entries) != 1 {
		t.Fatalf("missing identity/date or injected entry: %+v", doc)
	}
	if got := doc.Links; len(got) != 2 || got[0].Rel != "alternate" || got[0].Href != f.URL || got[1].Rel != "self" || got[1].Type != "application/atom+xml" || got[1].Href != "https://rss.example.com/workshop/feeds/private-token.atom" {
		t.Fatalf("unexpected feed links: %+v", got)
	}
	entry := doc.Entries[0]
	if entry.ID != it.GUID || entry.Title != it.Title || entry.Published != "2026-09-06T10:30:00Z" || entry.Updated != "2026-09-06T11:30:00Z" {
		t.Fatalf("lost item identity or dates: %+v", entry)
	}
	if len(entry.Links) != 1 || entry.Links[0].Rel != "alternate" || entry.Links[0].Href != "https://example.com/story?one=1&two=2" {
		t.Fatalf("relative story URL was not resolved: %+v", entry.Links)
	}
	if entry.Content.Type != "html" || entry.Content.Text != it.HTML || len(entry.Content.Children) != 0 {
		t.Fatalf("HTML was not round-tripped as escaped text: %+v", entry.Content)
	}
	if strings.Contains(string(body), "<p>") || !strings.Contains(string(body), "&lt;p&gt;") {
		t.Fatal("content markup must be escaped once at the XML layer")
	}
	if want := fmt.Sprintf("\"%x\"", sha256.Sum256(body)); etag != want {
		t.Fatalf("ETag = %q, want exact-byte digest %q", etag, want)
	}
	for _, link := range append(doc.Links, entry.Links...) {
		u, err := url.Parse(link.Href)
		if err != nil || !u.IsAbs() || u.Host == "" {
			t.Fatalf("nonabsolute reader link: %q", link.Href)
		}
	}
}

func TestAtomIdentitySurvivesContentEditsTokenRotationAndRelocation(t *testing.T) {
	f := model.Feed{ID: "persistent-feed", RSSToken: "old-token", Title: "News", URL: "https://example.com/news"}
	items := []model.Item{{Key: "story", GUID: "urn:sha256:persistent-story", Title: "Original", HTML: "Original"}}
	original, originalETag, err := feed.RenderAtom(f, items, "https://rss.example.com")
	if err != nil {
		t.Fatal(err)
	}
	repeat, repeatETag, err := feed.RenderAtom(f, items, "https://rss.example.com")
	if err != nil || !bytes.Equal(original, repeat) || originalETag != repeatETag {
		t.Fatalf("rendering without timestamps must be deterministic: %v", err)
	}
	f.Title, f.URL, f.RSSToken = "Renamed", "https://example.org/updates", "new-token"
	items[0].Title, items[0].HTML = "Corrected", "Corrected"
	changed, changedETag, err := feed.RenderAtom(f, items, "https://relocated.example.com")
	if err != nil {
		t.Fatal(err)
	}
	before, after := readAtom(t, original), readAtom(t, changed)
	if before.ID != after.ID || before.Entries[0].ID != after.Entries[0].ID {
		t.Fatal("feed and entry identity must survive edits, address changes and token rotation")
	}
	if after.Links[1].Href != "https://relocated.example.com/feeds/new-token.atom" || strings.Contains(string(changed), "old-token") || changedETag == originalETag {
		t.Fatal("new address/token/content were not reflected in Atom representation")
	}
	f.ID = "independent-feed"
	independent, _, err := feed.RenderAtom(f, items, "https://relocated.example.com")
	if err != nil || readAtom(t, independent).ID == before.ID {
		t.Fatalf("different saved feeds need different identities: %v", err)
	}
}

func TestAtomMissingDatesAndAuthor(t *testing.T) {
	stamp := time.Date(2026, 9, 6, 10, 0, 0, 123, time.UTC)
	for _, tc := range []struct {
		name                       string
		item                       model.Item
		success                    time.Time
		wantPublished, wantUpdated string
	}{
		{name: "first observation", item: model.Item{FirstSeen: stamp, LastSeen: stamp.Add(time.Hour)}, wantPublished: "2026-09-06T10:00:00.000000123Z", wantUpdated: "2026-09-06T11:00:00.000000123Z"},
		{name: "last observation", item: model.Item{LastSeen: stamp}, wantPublished: "2026-09-06T10:00:00.000000123Z", wantUpdated: "2026-09-06T10:00:00.000000123Z"},
		{name: "feed observation", success: stamp, wantPublished: "2026-09-06T10:00:00.000000123Z", wantUpdated: "2026-09-06T10:00:00.000000123Z"},
		{name: "no observations", wantPublished: "1970-01-01T00:00:00Z", wantUpdated: "1970-01-01T00:00:00Z"},
		{name: "future publication", item: model.Item{Published: stamp.Add(time.Hour), LastSeen: stamp}, wantPublished: "2026-09-06T11:00:00.000000123Z", wantUpdated: "2026-09-06T11:00:00.000000123Z"},
		{name: "unrepresentable source date", item: model.Item{Published: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), FirstSeen: stamp}, wantPublished: "2026-09-06T10:00:00.000000123Z", wantUpdated: "2026-09-06T10:00:00.000000123Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := model.Feed{ID: "feed-id", RSSToken: "token", URL: "https://example.com/news", LastSuccess: tc.success}
			it := tc.item
			it.Key = "story"
			body, _, err := feed.RenderAtom(f, []model.Item{it}, "http://localhost:8080")
			if err != nil {
				t.Fatal(err)
			}
			doc := readAtom(t, body)
			entry := doc.Entries[0]
			if doc.Author.Name != "example.com" || entry.Published != tc.wantPublished || entry.Updated != tc.wantUpdated || doc.Updated != tc.wantUpdated {
				t.Fatalf("wrong fallback metadata: %+v", doc)
			}
			wantGUID := fmt.Sprintf("urn:sha256:%x", sha256.Sum256([]byte("feed-id\x00story")))
			if entry.ID != wantGUID || len(entry.Links) != 0 {
				t.Fatal("linkless entry must retain store-compatible identity without an invented link")
			}
			for _, value := range []string{doc.Updated, entry.Published, entry.Updated} {
				if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
					t.Fatalf("invalid Atom date %q: %v", value, err)
				}
			}
		})
	}
}

func TestAtomEmptyFeed(t *testing.T) {
	body, _, err := feed.RenderAtom(model.Feed{ID: "empty", RSSToken: "token", Title: "Empty", URL: "https://example.com"}, nil, "http://localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	doc := readAtom(t, body)
	if len(doc.Entries) != 0 || doc.ID == "" || doc.Updated != "1970-01-01T00:00:00Z" || doc.Author.Name != "Empty" {
		t.Fatalf("empty Atom feed lacks required metadata: %+v", doc)
	}
}

func TestAtomRejectsInvalidReaderURLsAndIDs(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		base, source, guid, itemURL string
	}{
		{name: "relative base", base: "/rss"},
		{name: "base query", base: "https://rss.example.com?token=x"},
		{name: "empty base query", base: "https://rss.example.com?"},
		{name: "base fragment", base: "https://rss.example.com#fragment"},
		{name: "invalid base port", base: "https://rss.example.com:nope"},
		{name: "relative source", source: "/news"},
		{name: "source credentials", source: "https://user:pass@example.com/news"},
		{name: "source whitespace", source: "https://example.com/news page"},
		{name: "nonabsolute GUID", guid: "story"},
		{name: "GUID whitespace", guid: "urn:story:one two"},
		{name: "malformed GUID", guid: "https://example.com/%zz"},
		{name: "active entry link", itemURL: "javascript:alert(1)"},
		{name: "credentialed entry link", itemURL: "https://user:pass@example.com/story"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := model.Feed{ID: "feed", RSSToken: "token", URL: "https://example.com/news"}
			it := model.Item{GUID: "urn:story:one"}
			base := "https://rss.example.com"
			if tc.base != "" {
				base = tc.base
			}
			if tc.source != "" {
				f.URL = tc.source
			}
			if tc.guid != "" {
				it.GUID = tc.guid
			}
			it.URL = tc.itemURL
			if body, tag, err := feed.RenderAtom(f, []model.Item{it}, base); err == nil || body != nil || tag != "" {
				t.Fatalf("invalid reader metadata produced a successful feed: %s, %s, %v", body, tag, err)
			}
		})
	}
}
