package feed

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/model"
)

func outputFeed() model.Feed {
	return model.Feed{ID: "fid", RSSToken: "tok", Title: "Gazette", URL: "https://e.com",
		Interval: 900, LastSuccess: time.Unix(1757592000, 0)}
}

func withArticle() model.Item {
	return model.Item{Key: "a", GUID: "urn:sha256:aa", Title: "Full", URL: "https://e.com/1",
		HTML:      "<p>One-line teaser.</p>",
		FullHTML:  `<div><p><img src="https://e.com/lead.jpg" alt=""></p><p>Full body.</p></div>`,
		Image:     "https://e.com/lead.jpg",
		Published: time.Unix(1757505600, 0), LastSeen: time.Unix(1757592000, 0)}
}

func teaserOnly() model.Item {
	return model.Item{Key: "b", GUID: "urn:sha256:bb", Title: "Teaser", URL: "https://e.com/2",
		HTML:      `<p><img src="https://e.com/two.png" alt=""></p><p>Teaser body.</p>`,
		Image:     "https://e.com/two.png",
		Published: time.Unix(1757419200, 0), LastSeen: time.Unix(1757592000, 0)}
}

func renderRSS(t *testing.T, items []model.Item) string {
	t.Helper()
	b, _, err := Render(outputFeed(), items, "https://e.com/feeds/tok.xml", "RSS Workshop v1")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A reader that only renders description must keep receiving the article it
// receives today. content:encoded repeats it for readers that prefer that
// field, and is omitted when there is no separate article to carry.
func TestDescriptionKeepsTheArticleAndContentEncodedRepeatsIt(t *testing.T) {
	out := renderRSS(t, []model.Item{withArticle(), teaserOnly()})
	var doc struct {
		Items []struct {
			Title       string `xml:"title"`
			Description string `xml:"description"`
			Encoded     string `xml:"encoded"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Items) != 2 {
		t.Fatalf("rendered %d items", len(doc.Items))
	}
	full, teaser := doc.Items[0], doc.Items[1]
	if !strings.Contains(full.Description, "Full body.") {
		t.Errorf("description lost the article: %q", full.Description)
	}
	if full.Encoded != full.Description {
		t.Errorf("content:encoded does not match description:\n %q\n %q", full.Encoded, full.Description)
	}
	if teaser.Encoded != "" {
		t.Errorf("an item with no fetched article emitted content:encoded: %q", teaser.Encoded)
	}
	if !strings.Contains(teaser.Description, "Teaser body.") {
		t.Errorf("teaser-only item lost its description: %q", teaser.Description)
	}
}

// Article pages normally include the lead image in the body they mark up, so
// prepending it unconditionally showed it twice in every reader.
func TestTheImageIsNotPublishedTwice(t *testing.T) {
	// What a reader displays is the body, so that is where a duplicate shows.
	body := itemContent(withArticle())
	if n := strings.Count(body, "lead.jpg"); n != 1 {
		t.Errorf("the published body shows the image %d times: %q", n, body)
	}
	// In the document the body appears twice by design -- description and
	// content:encoded -- plus once as the media element a reader uses for a
	// thumbnail. Three references, one visible image.
	out := renderRSS(t, []model.Item{withArticle()})
	if n := strings.Count(out, "lead.jpg"); n != 3 {
		t.Errorf("lead.jpg appears %d times; want description, content:encoded and media:content", n)
	}
}

// A source that publishes its preview image after the story goes live is the
// reason the list page is re-extracted every refresh; that image must still
// reach the reader when the fetched article does not contain it.
func TestALateImageIsStillPrependedWhenTheArticleLacksIt(t *testing.T) {
	it := withArticle()
	it.FullHTML = "<div><p>Body with no image.</p></div>"
	body := itemContent(it)
	if !strings.Contains(body, "lead.jpg") {
		t.Errorf("a late image did not reach the reader: %q", body)
	}
	if !strings.HasPrefix(body, "<p><img") {
		t.Errorf("the image is not at the top of the body: %q", body)
	}
}

// Atom has both fields, so the list-page teaser is no longer thrown away.
func TestAtomCarriesTheTeaserAsSummary(t *testing.T) {
	b, _, err := RenderAtom(outputFeed(), []model.Item{withArticle(), teaserOnly()}, "https://e.com")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Entries []struct {
			Title   string `xml:"title"`
			Summary string `xml:"summary"`
			Content string `xml:"content"`
			Links   []struct {
				Rel  string `xml:"rel,attr"`
				Type string `xml:"type,attr"`
				Href string `xml:"href,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	full, teaser := doc.Entries[0], doc.Entries[1]
	if !strings.Contains(full.Summary, "One-line teaser.") {
		t.Errorf("the teaser did not reach summary: %q", full.Summary)
	}
	if !strings.Contains(full.Content, "Full body.") {
		t.Errorf("content lost the article: %q", full.Content)
	}
	if teaser.Summary != "" {
		t.Errorf("an item whose teaser IS its content emitted a duplicate summary: %q", teaser.Summary)
	}
	var enclosure string
	for _, l := range full.Links {
		if l.Rel == "enclosure" {
			enclosure = l.Href + " " + l.Type
		}
	}
	if !strings.Contains(enclosure, "lead.jpg") || !strings.Contains(enclosure, "image/jpeg") {
		t.Errorf("the extracted image is not offered as an enclosure: %q", enclosure)
	}
}

// A reader building a card or grid layout looks for a media element, not for
// the first img inside description.
func TestTheExtractedImageIsOfferedAsMedia(t *testing.T) {
	out := renderRSS(t, []model.Item{withArticle(), teaserOnly()})
	for _, want := range []string{
		`<media:content url="https://e.com/lead.jpg" type="image/jpeg" medium="image">`,
		`<media:content url="https://e.com/two.png" type="image/png" medium="image">`,
		`xmlns:media="http://search.yahoo.com/mrss/"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	for src, want := range map[string]string{
		"https://e.com/a.png": "image/png", "https://e.com/a.gif": "image/gif",
		"https://e.com/a.webp": "image/webp", "https://e.com/a.svg": "image/svg+xml",
		"https://e.com/a.avif": "image/avif", "https://e.com/a.jpg": "image/jpeg",
		"https://e.com/a.jpeg?v=2": "image/jpeg", "https://e.com/a": "image/jpeg",
		"https://e.com/a.png#frag": "image/png",
	} {
		if got := imageType(src); got != want {
			t.Errorf("imageType(%q) = %q, want %q", src, got, want)
		}
	}
}

// Rediscovery, polling frequency and "last updated" all come from stored values.
func TestChannelCarriesTheMetadataReadersUse(t *testing.T) {
	out := renderRSS(t, []model.Item{withArticle()})
	for _, want := range []string{
		`<atom:link href="https://e.com/feeds/tok.xml" rel="self" type="application/rss+xml">`,
		"<generator>RSS Workshop v1</generator>",
		"<ttl>15</ttl>", // 900s interval
		"<lastBuildDate>Thu, 11 Sep 2025 12:00:00 +0000</lastBuildDate>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("channel is missing %s", want)
		}
	}
	// A feed with no public address must omit the link rather than invent one.
	b, _, err := Render(outputFeed(), []model.Item{withArticle()}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "atom:link") || strings.Contains(string(b), "<generator>") {
		t.Error("an unset self link or generator was still emitted")
	}
}

// Serialization must never read the clock, or unchanged data stops producing
// identical bytes and every polling reader refetches forever.
func TestRenderingIsDeterministic(t *testing.T) {
	items := []model.Item{withArticle(), teaserOnly()}
	first, tag1, err := Render(outputFeed(), items, "https://e.com/feeds/tok.xml", "RSS Workshop v1")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	second, tag2, err := Render(outputFeed(), items, "https://e.com/feeds/tok.xml", "RSS Workshop v1")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || tag1 != tag2 {
		t.Error("rendering the same feed twice produced different bytes")
	}
	a1, at1, _ := RenderAtom(outputFeed(), items, "https://e.com")
	time.Sleep(2 * time.Millisecond)
	a2, at2, _ := RenderAtom(outputFeed(), items, "https://e.com")
	if string(a1) != string(a2) || at1 != at2 {
		t.Error("rendering the same Atom feed twice produced different bytes")
	}
}
