package feed

import (
	"encoding/xml"
	"html"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/extract"
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

// The article body is sanitized markup, so an address the body contains appears
// there escaped. Both bodies come from the real extractor, which is what escapes
// them, and the item's own image is the raw address the list page gave.
func TestAnEscapedImageAddressIsNotPublishedTwice(t *testing.T) {
	recipe := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"},
		Link: model.Field{Selector: "a"}, Full: &model.FullContent{Selector: "main"}}
	for _, c := range []struct{ src, attr string }{
		{"https://cdn.example.com/lead.jpg?w=640&q=75", "src"},
		{"https://cdn.example.com/o'neil.jpg", "src"},
		// A lazy thumbnail found through the fallback, beside the same address in data-src.
		{"https://cdn.example.com/x.jpg?w=800&h=450", "data-original"},
	} {
		src, escaped := c.src, html.EscapeString(c.src)
		list := `<article><h2>Story</h2><a href="/1">Read</a><img ` + c.attr + `="` + escaped + `"></article>`
		if c.attr != "src" {
			list = strings.Replace(list, "<img ", `<img src="data:image/gif;base64,R0lGODlhAQABAAAAACw=" `, 1)
		}
		page, err := extract.Run([]byte(list), "https://e.com/", recipe)
		if err != nil || len(page.Items) != 1 || page.Items[0].Image != src {
			t.Fatalf("%s: list page gave %+v, %v", src, page.Items, err)
		}
		body, err := extract.Article([]byte(`<main><img data-src="`+escaped+`"><p>Body.</p></main>`), "https://e.com/1", recipe)
		if err != nil || !strings.Contains(body, escaped) {
			t.Fatalf("%s: article gave %q, %v", src, body, err)
		}
		it := page.Items[0]
		it.FullHTML = body
		if n := strings.Count(itemContent(it), "<img"); n != 1 {
			t.Errorf("%s: the published body shows %d images: %q", src, n, itemContent(it))
		}
	}
}

// A content selector covering the card's image puts that image in the
// list-page description, which is the whole body when no article is fetched.
// It must still show once, and still be offered to readers as media.
func TestAListPageDescriptionShowingTheImageIsNotPublishedTwice(t *testing.T) {
	recipe := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"},
		Link: model.Field{Selector: "a"}, Content: model.Field{Selector: ".card"}}
	list := `<article><h2>Story</h2><a href="/1">Read</a><div class="card"><img src="/lead.jpg?w=640&amp;q=75"><p>Teaser.</p></div></article>`
	page, err := extract.Run([]byte(list), "https://e.com/", recipe)
	if err != nil || len(page.Items) != 1 || page.Items[0].Image != "https://e.com/lead.jpg?w=640&q=75" {
		t.Fatalf("list page gave %+v, %v", page.Items, err)
	}
	it := page.Items[0]
	it.GUID = "urn:sha256:cc"
	var rss struct {
		Description string `xml:"channel>item>description"`
		Media       struct {
			URL string `xml:"url,attr"`
		} `xml:"channel>item>content"`
	}
	if err := xml.Unmarshal([]byte(renderRSS(t, []model.Item{it})), &rss); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(rss.Description, "<img"); n != 1 || !strings.Contains(rss.Description, "Teaser.") {
		t.Errorf("RSS description shows %d images: %q", n, rss.Description)
	}
	if rss.Media.URL != it.Image {
		t.Errorf("media:content is %q, want %q", rss.Media.URL, it.Image)
	}
	b, _, err := RenderAtom(outputFeed(), []model.Item{it}, "https://e.com")
	if err != nil {
		t.Fatal(err)
	}
	var atom struct {
		Content string `xml:"entry>content"`
		Links   []struct {
			Rel  string `xml:"rel,attr"`
			Href string `xml:"href,attr"`
		} `xml:"entry>link"`
	}
	if err := xml.Unmarshal(b, &atom); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(atom.Content, "<img"); n != 1 {
		t.Errorf("Atom content shows %d images: %q", n, atom.Content)
	}
	enclosed := false
	for _, l := range atom.Links {
		enclosed = enclosed || l.Rel == "enclosure" && l.Href == it.Image
	}
	if !enclosed {
		t.Errorf("Atom offers no enclosure for the image: %+v", atom.Links)
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
