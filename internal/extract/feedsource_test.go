package extract

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"rss-workshop/internal/feed"
	"rss-workshop/internal/model"
)

var observed = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func feedRecipe() model.Recipe { return model.Recipe{Mode: "static", Type: FeedType} }

func feedFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("../../testdata/feeds", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func runFeedSource(t *testing.T, body []byte, effective string, r model.Recipe) model.Preview {
	t.Helper()
	p, err := RunAt(body, effective, r, observed)
	if err != nil {
		t.Fatalf("the feed was refused: %v", err)
	}
	return p
}

func guidKey(id string) string { return fmt.Sprintf("guid:%x", sha256.Sum256([]byte(id))) }

// byTitle finds a story by its title.
func byTitle(t *testing.T, p model.Preview, title string) model.Item {
	t.Helper()
	for _, it := range p.Items {
		if it.Title == title {
			return it
		}
	}
	titles := []string{}
	for _, it := range p.Items {
		titles = append(titles, it.Title)
	}
	t.Fatalf("no story titled %q among %q", title, titles)
	return model.Item{}
}

func at(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t
}

func TestFeedSourceReadsRSS2(t *testing.T) {
	p := runFeedSource(t, feedFixture(t, "rss2.xml"), "https://news.example/feed.xml", feedRecipe())
	if p.Matches != 11 || p.Valid != 9 || len(p.Items) != 9 {
		t.Fatalf("matches %d, valid %d, items %d; want 11, 9, 9", p.Matches, p.Valid, len(p.Items))
	}
	wantWarnings := []string{
		"Entry 8 skipped: its link is not an HTTP(S) URL",
		"Entry 9 skipped: it repeats an earlier entry's identity",
		`Entry 11: date not recognized, received "sometime last week"; using first-seen time`,
	}
	if !slices.Equal(p.Warnings, wantWarnings) {
		t.Errorf("warnings\n%q\nwant\n%q", p.Warnings, wantWarnings)
	}

	// content:encoded wins over the teaser, is sanitized and made absolute,
	// and already shows the story's image, so it is not added again.
	first := byTitle(t, p, "Bridges & tunnels — the spring survey")
	if first.Key != guidKey("riverside-1001") || first.URL != "https://news.example/2026/11/bridges" || first.Image != "https://news.example/img/bridge.jpg" {
		t.Errorf("first story: %+v", first)
	}
	for _, bad := range []string{"script", "steal", "onerror", "javascript:", "Short teaser"} {
		if strings.Contains(first.HTML, bad) {
			t.Errorf("the description keeps %q: %s", bad, first.HTML)
		}
	}
	if !strings.Contains(first.HTML, `href="https://news.example/more"`) || strings.Count(first.HTML, "<img") != 1 {
		t.Errorf("description not made absolute, or its image repeated: %s", first.HTML)
	}
	if !first.Published.Equal(at("2026-11-05T15:00:00Z")) {
		t.Errorf("EST read as %s", first.Published)
	}

	thumb := byTitle(t, p, "Thumbnail only")
	if thumb.Image != "https://cdn.news.example/thumb.jpg?w=400&h=300" || !strings.HasPrefix(thumb.HTML, `<p><img src="https://cdn.news.example/thumb.jpg?w=400&amp;h=300" alt=""></p>`) ||
		!strings.Contains(thumb.HTML, "<i>teaser</i>") || thumb.Key != guidKey("https://news.example/2026/11/thumb") || !thumb.Published.Equal(at("2026-11-06T00:00:00Z")) {
		t.Errorf("media:thumbnail story: %+v", thumb)
	}
	// An entry without a guid is known by its link, and an audio enclosure is
	// not its image.
	enclosure := byTitle(t, p, "Enclosure image")
	if enclosure.Key != "https://news.example/2026/11/enclosure" || enclosure.Image != "https://news.example/img/cover.png" || !enclosure.Published.Equal(at("2026-11-04T08:30:00Z")) {
		t.Errorf("enclosure story: %+v", enclosure)
	}
	// A declared image beats a tracking pixel, and a pixel is never the image.
	if got := byTitle(t, p, "Tracking pixel first").Image; got != "https://cdn.news.example/hero.jpg" {
		t.Errorf("the declared image lost to a tracking pixel: %q", got)
	}
	if got := byTitle(t, p, "Pixel, then a picture"); got.Image != "https://news.example/img/inline.jpg" || strings.Count(got.HTML, "inline.jpg") != 1 {
		t.Errorf("a pixel became the image, or the picture was shown twice: %+v", got)
	}
	// Markup written without escaping is read as markup, not dropped.
	if got := byTitle(t, p, "Raw markup description").HTML; got != "<p>Para <b>bold</b> tail &amp; more</p>" {
		t.Errorf("raw description markup: %q", got)
	}
	if got := byTitle(t, p, "Permalink guid only"); got.URL != "https://news.example/2026/11/permalink" {
		t.Errorf("a permalink guid is not the link: %+v", got)
	}
	// Entries sharing a link stay apart, by their guids.
	shared := []model.Item{byTitle(t, p, "Shared link, first"), byTitle(t, p, "Shared link, second")}
	if shared[0].Key != guidKey("live-1") || shared[1].Key != guidKey("live-2") || !shared[1].Published.IsZero() {
		t.Errorf("shared-link entries: %+v", shared)
	}
}

func TestFeedSourceReadsRDF(t *testing.T) {
	p := runFeedSource(t, feedFixture(t, "rdf.xml"), "https://journal.example/index.rdf", feedRecipe())
	if len(p.Items) != 2 || len(p.Warnings) != 0 {
		t.Fatalf("items %+v, warnings %q", p.Items, p.Warnings)
	}
	one, two := p.Items[0], p.Items[1]
	if one.Title != "First entry" || one.Key != guidKey("https://journal.example/1") || one.URL != "https://journal.example/1" || one.HTML != "<p>Body one.</p>" || !one.Published.Equal(at("2026-11-05T09:00:00Z")) {
		t.Errorf("first: %+v", one)
	}
	if two.HTML != "Summary two." || !two.Published.Equal(at("2026-11-04T00:00:00Z")) {
		t.Errorf("second: %+v", two)
	}
}

func TestFeedSourceReadsAtom(t *testing.T) {
	p := runFeedSource(t, feedFixture(t, "atom.xml"), "https://blog.example/feed", feedRecipe())
	if len(p.Items) != 4 || len(p.Warnings) != 0 {
		t.Fatalf("items %+v, warnings %q", p.Items, p.Warnings)
	}
	one := p.Items[0]
	// The alternate link, not the replies, through the feed's and the
	// entry's xml:base; the published date, not the updated one.
	if one.Title != "Escaped HTML title" || one.URL != "https://blog.example/notes/2026/1.html" || one.Key != guidKey("tag:blog.example,2026:1") || !one.Published.Equal(at("2026-11-05T10:00:00Z")) {
		t.Errorf("first: %+v", one)
	}
	if one.Image != "https://blog.example/notes/2026/1/cover.jpg" || !strings.Contains(one.HTML, `<a href="https://blog.example/notes/2026/rel" rel="nofollow">body</a>`) || !strings.Contains(one.HTML, `src="https://blog.example/notes/2026/pic.png"`) || strings.Contains(one.HTML, "summary") {
		t.Errorf("xhtml content or enclosure image: %+v", one)
	}
	// An http(s) id is the link of an entry that has none.
	two := p.Items[1]
	if two.Title != "XHTML title" || two.URL != "https://blog.example/notes/id-only" || two.HTML != "<p>HTML content</p>" || !two.Published.Equal(at("2026-11-04T09:00:00Z")) {
		t.Errorf("second: %+v", two)
	}
	// Content kept elsewhere leaves the summary.
	if three := p.Items[2]; three.Title != "Text & title" || three.HTML != "<p>Only a summary</p>" {
		t.Errorf("third: %+v", three)
	}
	if four := p.Items[3]; four.URL != "" || four.Key != guidKey("urn:uuid:4") || four.HTML != "Line &lt;one&gt; &amp; two" {
		t.Errorf("fourth: %+v", four)
	}
}

func TestFeedSourceReadsYouTubeMediaGroup(t *testing.T) {
	p := runFeedSource(t, feedFixture(t, "youtube.xml"), "https://www.youtube.com/feeds/videos.xml?channel_id=UCexample", feedRecipe())
	if len(p.Items) != 1 {
		t.Fatalf("items %+v", p.Items)
	}
	it := p.Items[0]
	want := `<p><img src="https://i1.ytimg.com/vi/abc123/hqdefault.jpg" alt=""></p>Step one: cut the wood.
Step two: &lt;nail&gt; it together.`
	if it.Title != "Building a bird box" || it.URL != "https://www.youtube.com/watch?v=abc123" || it.Image != "https://i1.ytimg.com/vi/abc123/hqdefault.jpg" || it.HTML != want || !it.Published.Equal(at("2026-11-01T12:00:00Z")) {
		t.Errorf("video: %+v", it)
	}
}

func TestFeedSourceTitlesUntitledPostsFromTheirText(t *testing.T) {
	p := runFeedSource(t, feedFixture(t, "mastodon.xml"), "https://social.example/@person.rss", feedRecipe())
	if len(p.Items) != 2 {
		t.Fatalf("items %+v, warnings %q", p.Items, p.Warnings)
	}
	long := p.Items[0]
	if utf8.RuneCountInString(long.Title) != 100 || !strings.HasPrefix(long.Title, "Spent the afternoon at the #library reading about tidal bridges") || !strings.HasSuffix(long.Title, "…") {
		t.Errorf("derived title: %q", long.Title)
	}
	if long.Image != "https://files.social.example/media/1001.png" || long.Key != guidKey("https://social.example/@person/1001") {
		t.Errorf("post: %+v", long)
	}
	if p.Items[1].Title != "Short one." {
		t.Errorf("short title: %q", p.Items[1].Title)
	}
}

func TestFeedSourceReadsUndeclaredPrefixesAndNamespacedRoots(t *testing.T) {
	p := runFeedSource(t, feedFixture(t, "undeclared.xml"), "https://plain.example/rss", feedRecipe())
	if len(p.Items) != 1 || p.Items[0].Image != "https://plain.example/t.jpg" || !strings.HasSuffix(p.Items[0].HTML, "<p>Full text</p>") || !p.Items[0].Published.Equal(at("2026-11-05T10:00:00Z")) {
		t.Fatalf("undeclared prefixes: %+v", p.Items)
	}
	// RSS 0.92 as UserLand published it, in a default namespace.
	userland := `<rss version="0.92" xmlns="http://backend.userland.com/rss2"><channel><title>c</title><item><title>Namespaced</title><link>https://userland.example/1</link></item></channel></rss>`
	p = runFeedSource(t, []byte(userland), "https://userland.example/rss", feedRecipe())
	if len(p.Items) != 1 || p.Items[0].Title != "Namespaced" || p.Items[0].URL != "https://userland.example/1" {
		t.Fatalf("namespaced RSS root: %+v", p.Items)
	}
}

// One character XML forbids would otherwise fail the whole feed. Invalid UTF-8
// is replaced, and forbidden characters, raw or referenced, are removed.
func TestFeedSourceRemovesCharactersXMLForbids(t *testing.T) {
	body := "<rss><channel>" +
		"<item><title>Vertical\x0btab, back\x08space, escape\x1b</title><link>https://controls.example/1</link></item>" +
		"<item><title>References&#11; &#x1B;&#xFFFF;&#xD800;gone, &#233;t&#xE9; kept</title><link>https://controls.example/2</link></item>" +
		"<item><title>Non\uFFFEcharacter and caf\xe9</title><link>https://controls.example/3</link></item>" +
		"</channel></rss>"
	p := runFeedSource(t, []byte(body), "https://controls.example/rss", feedRecipe())
	want := []string{"Verticaltab, backspace, escape", "References gone, été kept", "Noncharacter and caf\uFFFD"}
	got := []string{}
	for _, it := range p.Items {
		got = append(got, it.Title)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("titles %q, want %q", got, want)
	}
}

// Errors reach the log, the card, Diagnostics and notifications, so a
// document that cannot be read is described by line, never by its text.
func TestFeedSourceErrorsQuoteNoSourceText(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"namespace mismatch", "<rss><channel>\n<item><title>x</title><a:b xmlns:a=\"https://secret.example/a\" xmlns:c=\"https://secret.example/c\">y</c:b></item></channel></rss>", "line 2"},
		{"invalid name", "<rss><channel>\n\n<item><title>x</title><https://secret.example/b>y</https://secret.example/b></item></channel></rss>", "line 3"},
		{"stray end tag", "<rss><channel><item><description>secret</b> text</description></item></channel></rss>", "line 1"},
		{"truncated", "<?xml version=\"1.0\"?>\n<rss><channel><item><title>secret", "not well-formed XML"},
		{"broken declaration", "<?xml version=\"1.0\"?><!DOCTYPE secret [<!ENTITY", "not well-formed XML"},
		{"HTML page", "<!doctype html><html><head><title>secret</title></head><body><p>secret</p></body></html>", "not an RSS or Atom feed"},
		{"JSON Feed", `{"version":"https://jsonfeed.org/version/1.1","title":"secret","items":[]}`, "not an RSS or Atom feed"},
		{"plain text", "secret", "not an RSS or Atom feed"},
		{"empty body", "", "not an RSS or Atom feed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := RunAt([]byte(tc.body), "https://feed.example/rss", feedRecipe(), observed)
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(p.Items) != 0 {
				t.Fatalf("error %v, items %d; want an error naming %q", err, len(p.Items), tc.want)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "http") || errors.Is(err, ErrNoItems) {
				t.Fatalf("the error quotes the document, or reads as an empty page: %v", err)
			}
		})
	}
}

// encoding/xml expands no DTD entity, internal or external.
func TestFeedSourceExpandsNoEntities(t *testing.T) {
	body := `<?xml version="1.0"?><!DOCTYPE rss [<!ENTITY inner "EXPANDED"><!ENTITY outer SYSTEM "file:///etc/hostname">]><rss><channel><item><title>&inner;&outer;</title><link>https://entities.example/1</link></item></channel></rss>`
	p := runFeedSource(t, []byte(body), "https://entities.example/rss", feedRecipe())
	if len(p.Items) != 1 || p.Items[0].Title != "&inner;&outer;" {
		t.Fatalf("entities: %+v", p.Items)
	}
}

func TestFeedSourceBounds(t *testing.T) {
	var b strings.Builder
	b.WriteString("<rss><channel>")
	for i := range MaxFeedEntries + 5 {
		fmt.Fprintf(&b, "<item><title>Story %d</title><guid>%d</guid></item>", i, i)
	}
	b.WriteString("</channel></rss>")
	p := runFeedSource(t, []byte(b.String()), "https://long.example/rss", feedRecipe())
	if p.Matches != MaxFeedEntries || len(p.Items) != MaxFeedEntries || p.Items[0].Title != "Story 0" || !slices.Contains(p.Warnings, "Only the first 1000 entries were read") {
		t.Fatalf("matches %d, items %d, warnings %q", p.Matches, len(p.Items), p.Warnings)
	}

	// A post too long to publish falls back to its summary; without one,
	// the entry is left out and the rest of the feed is kept.
	huge := "<p>" + strings.Repeat("long text ", 30<<10) + "</p>"
	body := "<rss xmlns:content=\"http://purl.org/rss/1.0/modules/content/\"><channel>" +
		"<item><title>Huge post</title><link>https://huge.example/1</link><description>Its summary</description><content:encoded><![CDATA[" + huge + "]]></content:encoded></item>" +
		"<item><title>Huge without a summary</title><link>https://huge.example/2</link><content:encoded><![CDATA[" + huge + "]]></content:encoded></item>" +
		"<item><title>" + strings.Repeat("t", 3000) + "</title><link>https://huge.example/3</link></item>" +
		"<item><title>Small</title><link>https://huge.example/4</link></item>" +
		"</channel></rss>"
	p = runFeedSource(t, []byte(body), "https://huge.example/rss", feedRecipe())
	if len(p.Items) != 2 || p.Items[0].HTML != "Its summary" || p.Items[1].Title != "Small" {
		t.Fatalf("items %+v", p.Items)
	}
	want := []string{"Entry 2 skipped: it exceeds the size limits for a story", "Entry 3 skipped: it exceeds the size limits for a story"}
	if !slices.Equal(p.Warnings, want) {
		t.Fatalf("warnings %q, want %q", p.Warnings, want)
	}
}

// An address too long to publish is not an image, and never fails the feed: a
// feed source has no selector to narrow, so the story is kept without one and
// the next candidate is tried.
func TestFeedSourceOverlongImagesAreUnusable(t *testing.T) {
	long := "https://cdn.example/" + strings.Repeat("a", 8200) + ".jpg"
	body := `<rss xmlns:media="http://search.yahoo.com/mrss/" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel>` +
		`<item><title>Long thumbnail</title><link>https://long.example/1</link><description>Short.</description><media:thumbnail url="` + long + `"/></item>` +
		`<item><title>Long inline image</title><link>https://long.example/2</link><content:encoded><![CDATA[<p>text</p><img src="` + long + `">]]></content:encoded></item>` +
		`<item><title>Long, then usable</title><link>https://long.example/3</link><description>Short.</description><media:thumbnail url="` + long + `"/><media:thumbnail url="https://cdn.example/ok.jpg"/></item>` +
		`</channel></rss>`
	p := runFeedSource(t, []byte(body), "https://long.example/rss", feedRecipe())
	if len(p.Items) != 3 || len(p.Warnings) != 0 {
		t.Fatalf("items %d, warnings %q", len(p.Items), p.Warnings)
	}
	for _, it := range p.Items[:2] {
		if it.Image != "" || strings.HasPrefix(it.HTML, "<p><img") {
			t.Errorf("%s: image %.40q, description %.60q", it.Title, it.Image, it.HTML)
		}
	}
	if got := p.Items[2]; got.Image != "https://cdn.example/ok.jpg" {
		t.Errorf("the usable thumbnail after a long one: %.60q", got.Image)
	}
}

// A description that shows nothing once sanitized, such as a post that is only
// a video embed or a tracking image, gives way to the entry's summary, which
// then titles an untitled entry and is what filters read.
func TestFeedSourceFallsBackFromAnEmptyDescription(t *testing.T) {
	const embed = `<figure class="wp-block-embed"><div><iframe src="https://www.youtube.com/embed/x"></iframe></div></figure>`
	body := `<rss xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel>` +
		`<item><title>Bridges</title><link>https://embed.example/1</link><description>Watch our video about bridges</description><content:encoded><![CDATA[<iframe src="https://www.youtube.com/embed/x"></iframe>]]></content:encoded></item>` +
		`<item><link>https://embed.example/2</link><description>An untitled video about tunnels</description><content:encoded><![CDATA[` + embed + `]]></content:encoded></item>` +
		`<item><title>Pixel</title><link>https://embed.example/3</link><description>Bridges seen from a boat</description><content:encoded><![CDATA[<img src="https://stats.example/p.gif" width="1px" height="1px" alt="">]]></content:encoded></item>` +
		`<item><title>Picture only</title><link>https://embed.example/4</link><description>Its summary</description><content:encoded><![CDATA[<img src="https://embed.example/photo.jpg" alt="">]]></content:encoded></item>` +
		`<item><title>Embed without a summary</title><link>https://embed.example/5</link><content:encoded><![CDATA[` + embed + `]]></content:encoded></item>` +
		`</channel></rss>`
	p := runFeedSource(t, []byte(body), "https://embed.example/rss", feedRecipe())
	got := []string{}
	for _, it := range p.Items {
		got = append(got, it.Title+": "+it.HTML)
	}
	want := []string{
		"Bridges: Watch our video about bridges",
		"An untitled video about tunnels: An untitled video about tunnels",
		"Pixel: Bridges seen from a boat",
		// A picture is something to show, so the post itself is kept.
		`Picture only: <img src="https://embed.example/photo.jpg" alt=""/>`,
		// Without a summary, the post is kept as it sanitizes.
		"Embed without a summary: <figure><div></div></figure>",
	}
	if !slices.Equal(got, want) || len(p.Warnings) != 0 {
		t.Fatalf("stories\n%q\nwant\n%q\nwarnings %q", got, want, p.Warnings)
	}

	r := feedRecipe()
	r.Filters = &model.FilterSet{Include: &model.FilterRule{Op: "contains_any", Field: "description", Keywords: []string{"bridges"}}}
	if p = runFeedSource(t, []byte(body), "https://embed.example/rss", r); len(p.Items) != 2 || p.Items[0].Title != "Bridges" || p.Items[1].Title != "Pixel" {
		t.Fatalf("a description filter did not read the summary: %+v", p.Items)
	}

	atom := `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>tag:embed.example,1</id><link href="https://embed.example/6"/><summary>An Atom summary</summary>` +
		`<content type="html">&lt;iframe src="https://www.youtube.com/embed/x"&gt;&lt;/iframe&gt;</content></entry></feed>`
	if p = runFeedSource(t, []byte(atom), "https://embed.example/atom", feedRecipe()); len(p.Items) != 1 || p.Items[0].Title != "An Atom summary" || p.Items[0].HTML != "An Atom summary" {
		t.Fatalf("untitled Atom embed: %+v", p.Items)
	}
}

// What a server prints after the root element, such as a PHP notice or an
// injected script, is not part of the feed, and a second document after it
// does not change how the first is read. A stray end tag inside the feed still
// fails it, rather than ending it early.
func TestFeedSourceIgnoresContentAfterTheRoot(t *testing.T) {
	const doc = `<?xml version="1.0"?><rss><channel>` +
		`<item><title>A</title><link>https://after.example/a</link><guid>g-a</guid></item>` +
		`<item><title>B</title><link>https://after.example/b</link></item>` +
		`</channel></rss>`
	for name, tail := range map[string]string{
		"PHP notice":   "\n<br />\n<b>Notice</b>:  Undefined index: x in <b>/var/www/feed.php</b> on line <b>12</b><br />\n",
		"script":       `<script>if (a < b && c) { track(); }</script>`,
		"Atom root":    `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>other</id><title>C</title></entry></feed>`,
		"second RSS":   `<rss><channel><item><title>C</title><link>https://after.example/c</link></item></channel></rss>`,
		"unclosed tag": "<br>",
	} {
		p := runFeedSource(t, []byte(doc+tail), "https://after.example/rss", feedRecipe())
		if len(p.Items) != 2 || p.Items[0].URL != "https://after.example/a" || p.Items[0].Key != guidKey("g-a") || p.Items[1].Key != "https://after.example/b" {
			t.Errorf("%s: %+v", name, p.Items)
		}
	}
	// A self-closing root is the whole feed too.
	p, err := RunAt([]byte(`<rss/><br />`), "https://after.example/rss", feedRecipe(), observed)
	if err != nil || len(p.Items) != 0 || !slices.Equal(p.Warnings, []string{"The feed lists no entries"}) {
		t.Fatalf("self-closing root: %+v, %v", p, err)
	}
	stray := `<rss><channel><item><title>A</title><description>x</b> y</description></item><item><title>B</title></item></channel></rss>`
	if _, err := RunAt([]byte(stray), "https://after.example/rss", feedRecipe(), observed); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("a stray end tag ended the feed early: %v", err)
	}
}

// RSS gives a title no type. A less-than sign is literal unless the title
// reads as markup, so titles about code keep their words, and the app's own
// output, which escapes them, reads back unchanged.
func TestFeedSourceRSSTitles(t *testing.T) {
	for raw, want := range map[string]string{
		"Why std::vector&lt;bool&gt; is special":           "Why std::vector<bool> is special",
		"Use the &lt;details&gt; element":                  "Use the <details> element",
		"<![CDATA[Use the <details> element]]>":            "Use the <details> element",
		"Use the &lt;code&gt; element":                     "Use the <code> element",
		"if x&lt;y then swap":                              "if x<y then swap",
		"Vec&lt;T&gt; vs Box&lt;T&gt; in Rust":             "Vec<T> vs Box<T> in Rust",
		"a &lt; b":                                         "a < b",
		"&lt;b&gt;Bold&lt;/b&gt; news":                     "Bold news",
		"<![CDATA[<em>Big</em> <a href=\"/x\">news</a>]]>": "Big news",
		"Line<br/>break":                                   "Line break",
		"Raw <b>bold</b> title":                            "Raw bold title",
		"AT&amp;amp;T":                                     "AT&T",
		"&lt;b&gt;Unclosed":                                "<b>Unclosed",
		"&lt;b&gt;Mixed&lt;/b&gt; &lt;T&gt;":               "<b>Mixed</b> <T>",
	} {
		body := "<rss><channel><item><title>" + raw + "</title><link>https://titles.example/1</link></item></channel></rss>"
		p := runFeedSource(t, []byte(body), "https://titles.example/rss", feedRecipe())
		if len(p.Items) != 1 || p.Items[0].Title != want {
			t.Errorf("%s: %+v, want %q", raw, p.Items, want)
		}
	}

	titles := []string{"Vec<T> vs Box<T> in Rust", "if x<y then swap", "Use the <b> element", "AT&T <3"}
	items := []model.Item{}
	for i, title := range titles {
		items = append(items, model.Item{Title: title, URL: fmt.Sprintf("https://titles.example/%d", i), GUID: fmt.Sprintf("urn:test:%d", i), HTML: "x", Published: observed})
	}
	source := model.Feed{ID: "1", RSSToken: "token", Title: "Own output", URL: "https://titles.example/"}
	rss, _, err := feed.Render(source, items, "", "RSS Workshop")
	if err != nil {
		t.Fatal(err)
	}
	atom, _, err := feed.RenderAtom(source, items, "https://reader.example")
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string][]byte{"RSS": rss, "Atom": atom} {
		p := runFeedSource(t, out, "https://reader.example/feeds/x", feedRecipe())
		got := []string{}
		for _, it := range p.Items {
			got = append(got, it.Title)
		}
		if !slices.Equal(got, titles) {
			t.Errorf("%s read back as %q, want %q", name, got, titles)
		}
	}
}

// A well-formed feed with no entries has not failed: it keeps saved history
// without backing off. Entries that all fail their checks are an empty page.
func TestFeedSourceEmptyAndUnusableFeeds(t *testing.T) {
	p, err := RunAt([]byte(`<rss version="2.0"><channel><title>Nothing yet</title></channel></rss>`), "https://empty.example/rss", feedRecipe(), observed)
	if err != nil || p.Matches != 0 || len(p.Items) != 0 || !slices.Equal(p.Warnings, []string{"The feed lists no entries"}) {
		t.Fatalf("empty feed: %+v, %v", p, err)
	}
	p, err = RunAt([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>a</id></entry><entry><link href="javascript:x()"/><title>t</title></entry></feed>`), "https://empty.example/atom", feedRecipe(), observed)
	if !errors.Is(err, ErrNoItems) || p.Matches != 2 || p.Valid != 0 {
		t.Fatalf("unusable feed: %+v, %v", p, err)
	}
}

// A story keeps its identity while its source edits it: the guid, then the
// link, then the title decides.
func TestFeedSourceIdentity(t *testing.T) {
	item := func(guid, link, title, date string) string {
		out := "<item><title>" + title + "</title><description>" + title + " text</description>"
		if guid != "" {
			out += "<guid isPermaLink=\"false\">" + guid + "</guid>"
		}
		if link != "" {
			out += "<link>" + link + "</link>"
		}
		return out + "<pubDate>" + date + "</pubDate></item>"
	}
	keys := func(items ...string) []string {
		p := runFeedSource(t, []byte("<rss><channel>"+strings.Join(items, "")+"</channel></rss>"), "https://identity.example/rss", feedRecipe())
		out := []string{}
		for _, it := range p.Items {
			out = append(out, it.Key)
		}
		return out
	}
	before := keys(item("g-1", "https://identity.example/a", "First", "Thu, 05 Nov 2026 10:00:00 GMT"), item("", "https://identity.example/b", "Second", ""), item("", "", "Third", ""))
	after := keys(item("g-1", "https://identity.example/moved", "First, edited", "Fri, 06 Nov 2026 10:00:00 GMT"), item("", "https://identity.example/b", "Second, edited", ""), item("", "", "Third", ""))
	want := []string{guidKey("g-1"), "https://identity.example/b", fmt.Sprintf("title:%x", sha256.Sum256([]byte("Third")))}
	if !slices.Equal(before, want) || !slices.Equal(after, want) {
		t.Fatalf("keys before %q, after %q, want %q", before, after, want)
	}
}

// Smaller rules from docs/feed-sources.md and docs/dates.md, one entry each. A
// change to any of them gives some feed's stories other links, identities,
// dates, images or titles.
func TestFeedSourceDocumentedRules(t *testing.T) {
	const (
		rss  = `<rss xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel>%s</channel></rss>`
		atom = `<feed xmlns="http://www.w3.org/2005/Atom">%s</feed>`
	)
	for _, tc := range []struct {
		name, body string
		check      func(model.Item) bool
	}{
		{"a guid that is not a permalink is not the link", fmt.Sprintf(rss, `<item><title>t</title><guid isPermaLink="false">https://rules.example/not-a-page</guid></item>`),
			func(it model.Item) bool { return it.URL == "" && it.Key == guidKey("https://rules.example/not-a-page") }},
		{"itunes:image", fmt.Sprintf(rss, `<item><title>t</title><link>https://rules.example/1</link><itunes:image href="https://rules.example/cover.jpg"/></item>`),
			func(it model.Item) bool { return it.Image == "https://rules.example/cover.jpg" }},
		{"Atom 0.3 issued", fmt.Sprintf(atom, `<entry><title>t</title><id>a</id><modified>2026-11-06T00:00:00Z</modified><issued>2026-11-05T00:00:00Z</issued></entry>`),
			func(it model.Item) bool { return it.Published.Equal(at("2026-11-05T00:00:00Z")) }},
		{"Atom 0.3 modified alone", fmt.Sprintf(atom, `<entry><title>t</title><id>a</id><modified>2026-11-06T00:00:00Z</modified></entry>`),
			func(it model.Item) bool { return it.Published.Equal(at("2026-11-06T00:00:00Z")) }},
		{"published before an earlier updated", fmt.Sprintf(atom, `<entry><title>t</title><id>a</id><updated>2026-11-06T00:00:00Z</updated><published>2026-11-05T00:00:00Z</published></entry>`),
			func(it model.Item) bool { return it.Published.Equal(at("2026-11-05T00:00:00Z")) }},
		{"RSS channel xml:base", `<rss><channel xml:base="https://base.example/news/"><item><title>t</title><link>story.html</link></item></channel></rss>`,
			func(it model.Item) bool { return it.URL == "https://base.example/news/story.html" }},
		{"Atom content of type text/html", fmt.Sprintf(atom, `<entry><title>t</title><id>a</id><content type="text/html">&lt;p&gt;Markup&lt;/p&gt;</content></entry>`),
			func(it model.Item) bool { return it.HTML == "<p>Markup</p>" }},
		{"Atom content in base64", fmt.Sprintf(atom, `<entry><title>t</title><id>a</id><summary>Summary</summary><content type="text/html" mode="base64">PHA+SGlkZGVuPC9wPg==</content></entry>`),
			func(it model.Item) bool { return it.HTML == "Summary" }},
		{"IANA alternate relation", fmt.Sprintf(atom, `<entry><title>t</title><id>a</id><link rel="http://www.iana.org/assignments/relation/alternate" href="https://rules.example/iana"/></entry>`),
			func(it model.Item) bool { return it.URL == "https://rules.example/iana" }},
		{"RDF with an undeclared rdf prefix", `<rdf:RDF xmlns="http://purl.org/rss/1.0/"><item rdf:about="https://rules.example/about"><title>t</title></item></rdf:RDF>`,
			func(it model.Item) bool { return it.Key == guidKey("https://rules.example/about") }},
		{"content namespace without its trailing slash", `<rss xmlns:c="http://purl.org/rss/1.0/modules/content"><channel><item><title>t</title><link>https://rules.example/1</link><description>Teaser</description><c:encoded>Full</c:encoded></item></channel></rss>`,
			func(it model.Item) bool { return it.HTML == "Full" }},
		{"a pixel sized in px", fmt.Sprintf(rss, `<item><title>t</title><link>https://rules.example/1</link><description><![CDATA[<img src="https://stats.example/p.gif" width="1px" height="1px"><img src="https://rules.example/real.jpg">]]></description></item>`),
			func(it model.Item) bool { return it.Image == "https://rules.example/real.jpg" }},
		{"an RSS item outside the channel", `<rss><item><title>t</title><link>https://rules.example/loose</link></item></rss>`,
			func(it model.Item) bool { return it.URL == "https://rules.example/loose" }},
		{"blocks in a derived title", fmt.Sprintf(rss, `<item><link>https://rules.example/1</link><description><![CDATA[<p>One</p><p>Two</p>]]></description></item>`),
			func(it model.Item) bool { return it.Title == "One Two" }},
		{"styles in an HTML title", fmt.Sprintf(atom, `<entry><title type="html">Styled&lt;style&gt;p{color:red}&lt;/style&gt;&lt;script&gt;x()&lt;/script&gt; title</title><id>a</id></entry>`),
			func(it model.Item) bool { return it.Title == "Styled title" }},
		{"an id that is a web address but not a usable link", fmt.Sprintf(atom, `<entry><title>t</title><id>https://user@rules.example/1</id></entry>`),
			func(it model.Item) bool { return it.URL == "" && it.Key == guidKey("https://user@rules.example/1") }},
	} {
		p := runFeedSource(t, []byte(tc.body), "https://rules.example/feed", feedRecipe())
		if len(p.Items) != 1 || !tc.check(p.Items[0]) {
			t.Errorf("%s: %+v, warnings %q", tc.name, p.Items, p.Warnings)
		}
	}
}

func TestFeedSourceDatesFollowTheRecipe(t *testing.T) {
	body := `<rss><channel>
<item><title>Layout</title><link>https://dates.example/1</link><pubDate>05.11.2026 10:00</pubDate></item>
<item><title>Zone name</title><link>https://dates.example/2</link><pubDate>Thu, 05 Nov 2026 10:00:00 CET</pubDate></item>
<item><title>Bad pubDate, good dc:date</title><link>https://dates.example/3</link><pubDate>soon</pubDate><dc:date>2026-11-05T10:00:00Z</dc:date></item>
</channel></rss>`
	r := feedRecipe()
	r.DateLayout, r.Timezone = "02.01.2006 15:04", "Europe/Paris"
	p := runFeedSource(t, []byte(body), "https://dates.example/rss", r)
	want := []time.Time{at("2026-11-05T09:00:00Z"), at("2026-11-05T09:00:00Z"), at("2026-11-05T10:00:00Z")}
	for i, it := range p.Items {
		if !it.Published.Equal(want[i]) || it.PublishedEstimated {
			t.Errorf("%s: %s, want %s", it.Title, it.Published, want[i])
		}
	}
	if len(p.Items) != 3 || len(p.Warnings) != 0 {
		t.Fatalf("items %d, warnings %q", len(p.Items), p.Warnings)
	}
}

func TestFeedSourceFiltersLikeCards(t *testing.T) {
	r := feedRecipe()
	r.Filters = &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "description", Keywords: []string{"hero image"}}}
	p := runFeedSource(t, feedFixture(t, "rss2.xml"), "https://news.example/feed.xml", r)
	if p.Valid != 9 || p.Filtered != 1 || len(p.Items) != 8 || len(p.FilterExamples) != 1 || p.FilterExamples[0].Title != "Tracking pixel first" {
		t.Fatalf("valid %d, filtered %d, items %d, examples %+v", p.Valid, p.Filtered, len(p.Items), p.FilterExamples)
	}
}

func TestFeedSourceValidation(t *testing.T) {
	ok := feedRecipe()
	ok.Full = &model.FullContent{Selector: "div.article-body > p:first-child"}
	ok.UserAgent, ok.AcceptLanguage, ok.DateLayout, ok.Timezone = "Reader/1", "en", "2006-01-02", "UTC"
	ok.Filters = &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "title", Keywords: []string{"Sponsored"}}}
	if err := Validate(ok); err != nil {
		t.Fatalf("a valid feed source was refused: %v", err)
	}
	blank := ok
	blank.Mode = ""
	if err := Validate(blank); err != nil {
		t.Fatalf("a feed source without a mode was refused: %v", err)
	}
	for name, change := range map[string]func(*model.Recipe){
		"browser mode":         func(r *model.Recipe) { r.Mode = "browser" },
		"auto mode":            func(r *model.Recipe) { r.Mode = "auto" },
		"flaresolverr mode":    func(r *model.Recipe) { r.Mode = "flaresolverr" },
		"item selector":        func(r *model.Recipe) { r.Items = "item" },
		"title selector":       func(r *model.Recipe) { r.Title.Selector = "title" },
		"link attribute":       func(r *model.Recipe) { r.Link.Attr = "href" },
		"date selector":        func(r *model.Recipe) { r.Date.Selector = "pubDate" },
		"content selector":     func(r *model.Recipe) { r.Content.Selector = "description" },
		"image attribute":      func(r *model.Recipe) { r.Image.Attr = "url" },
		"rendered articles":    func(r *model.Recipe) { r.Full = &model.FullContent{Selector: ".body", Browser: true} },
		"XPath article":        func(r *model.Recipe) { r.Full = &model.FullContent{Selector: "//div[@class='body']"} },
		"invalid article":      func(r *model.Recipe) { r.Full = &model.FullContent{Selector: "div["} },
		"unknown selector set": func(r *model.Recipe) { r.Type = "json" },
	} {
		r := ok
		change(&r)
		if err := Validate(r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The article selector is evaluated as it is validated: as CSS.
	article, err := Article([]byte(`<div class="article-body"><p>First</p><p>Second</p></div>`), "https://feed.example/a", ok)
	if err != nil || article != "<p>First</p>" {
		t.Fatalf("article: %q, %v", article, err)
	}
}
