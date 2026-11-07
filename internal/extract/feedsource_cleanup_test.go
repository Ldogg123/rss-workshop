package extract

import (
	"strings"
	"testing"

	"rss-workshop/internal/model"
)

func feedCleanupRecipe(rules ...string) model.Recipe {
	r := feedRecipe()
	r.Remove = rules
	return r
}

// A feed source has no selector type of its own: its cleanup rules are CSS,
// like its article selector, and a rule written as XPath is told so.
func TestFeedSourceCleanupRulesAreCSS(t *testing.T) {
	if err := Validate(feedCleanupRecipe(".share", "aside.related", `p:containsOwn("appeared first on")`)); err != nil {
		t.Fatalf("CSS rules were refused: %v", err)
	}
	for _, rule := range []string{".//div[@class='share']", "//aside", "(.//p)[1]", "../div", "div[@class='share']"} {
		err := Validate(feedCleanupRecipe(".share", rule))
		if err == nil || err.Error() != "invalid CSS selector in cleanup rule 2: an RSS or Atom source's cleanup rules are CSS, such as .share, not XPath" {
			t.Errorf("%s: %v", rule, err)
		}
	}
	// Other invalid CSS keeps the parser's reason.
	if err := Validate(feedCleanupRecipe("div[[")); err == nil || !strings.HasPrefix(err.Error(), "invalid CSS selector in cleanup rule 1: ") || strings.Contains(err.Error(), "XPath") {
		t.Errorf("invalid CSS: %v", err)
	}
	// A web page's XPath rules keep their own message.
	page := cleanupRecipe("css", ".//div[@class='share']")
	if err := Validate(page); err == nil || strings.Contains(err.Error(), "RSS or Atom") {
		t.Errorf("a web page's rule: %v", err)
	}

	// Rules run as CSS on descriptions and on fetched articles alike.
	body := `<rss><channel><item><title>Harbour</title><link>https://feed.example/1</link>` +
		`<description><![CDATA[<p>The harbour reopened.</p><div class="share"><a href="https://social.example/s">Share</a></div>]]></description></item></channel></rss>`
	r := feedCleanupRecipe(".share")
	if p := runFeedSource(t, []byte(body), "https://feed.example/rss", r); len(p.Items) != 1 || p.Items[0].HTML != "<p>The harbour reopened.</p>" || len(p.Warnings) != 0 {
		t.Fatalf("description: %+v, warnings %q", p.Items, p.Warnings)
	}
	r.Full = &model.FullContent{Selector: "main"}
	article, err := Article([]byte(`<main><p>Full text.</p><div class="share">Share</div></main>`), "https://feed.example/1", r)
	if err != nil || article != "<p>Full text.</p>" {
		t.Fatalf("article %q, %v", article, err)
	}
}

// cleanupFeed's entries carry what cleanup rules are for, around a full post,
// a summary, an untitled post and images.
const cleanupFeed = `<rss xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:media="http://search.yahoo.com/mrss/"><channel>
<item><title>Promo only</title><link>https://feed.example/1</link><description>The summary of one</description>
<content:encoded><![CDATA[<div class="promo">Promo text <img src="https://feed.example/promo.jpg" alt=""></div>]]></content:encoded></item>
<item><title>Nothing else</title><link>https://feed.example/2</link><content:encoded><![CDATA[<div class="promo">Promo text two</div>]]></content:encoded></item>
<item><content:encoded><![CDATA[<p class="promo">Promo opening</p><p>Untitled post text</p>]]></content:encoded></item>
<item><content:encoded><![CDATA[<p class="promo">An untitled promo</p>]]></content:encoded></item>
<item><title>Avatar</title><link>https://feed.example/5</link><content:encoded><![CDATA[<p><img class="promo" src="https://feed.example/avatar.png" alt=""></p><p>Text five</p><p><img src="https://feed.example/photo.jpg" alt=""></p>]]></content:encoded></item>
<item><title>Declared</title><link>https://feed.example/6</link><media:thumbnail url="https://feed.example/thumb.jpg"/><content:encoded><![CDATA[<p>Text six <img class="promo" src="https://feed.example/inline.jpg" alt=""></p>]]></content:encoded></item>
<item><title>Embed</title><link>https://feed.example/7</link><description>Summary seven</description><content:encoded><![CDATA[<iframe src="https://video.example/x"></iframe><div class="promo"> </div>]]></content:encoded></item>
</channel></rss>`

// Cleanup applies to each description candidate before the fallback judges
// it, so a full post the rules leave with nothing to show gives way to the
// summary, and the description is dropped, with the cleanup warning, only
// when every candidate that showed something was emptied by them. The image
// in the description is the first one the rules leave; a declared image is
// not theirs to change. A title derived from an untitled entry, and so its
// identity, is read before cleanup, which never skips an entry.
func TestFeedSourceCleanupRunsBeforeTheDescriptionFallback(t *testing.T) {
	plain := runFeedSource(t, []byte(cleanupFeed), "https://feed.example/rss", feedRecipe())
	cleaned := runFeedSource(t, []byte(cleanupFeed), "https://feed.example/rss", feedCleanupRecipe(".promo"))
	if len(plain.Items) != 7 || len(cleaned.Items) != 7 || len(plain.Warnings) != 0 {
		t.Fatalf("stories %d and %d, warnings %q", len(plain.Items), len(cleaned.Items), plain.Warnings)
	}
	want := []struct{ html, image string }{
		{"The summary of one", ""},
		{"", ""},
		{"<p>Untitled post text</p>", ""},
		{"", ""},
		{`<p></p><p>Text five</p><p><img src="https://feed.example/photo.jpg" alt=""/></p>`, "https://feed.example/photo.jpg"},
		{`<p><img src="https://feed.example/thumb.jpg" alt=""></p><p>Text six </p>`, "https://feed.example/thumb.jpg"},
		{"Summary seven", ""},
	}
	for i, it := range cleaned.Items {
		before := plain.Items[i]
		if it.HTML != want[i].html || it.Image != want[i].image {
			t.Errorf("story %d: description %q, image %q; want %q, %q", i+1, it.HTML, it.Image, want[i].html, want[i].image)
		}
		if it.Title != before.Title || it.Key != before.Key || it.URL != before.URL || !it.Published.Equal(before.Published) {
			t.Errorf("story %d changed beyond its description:\n%+v\n%+v", i+1, it, before)
		}
	}
	// Without rules the full posts are published, and the avatar is the image.
	if !strings.Contains(plain.Items[0].HTML, "Promo text") || plain.Items[0].Image != "https://feed.example/promo.jpg" || plain.Items[4].Image != "https://feed.example/avatar.png" {
		t.Errorf("without rules: %+v", plain.Items)
	}
	if plain.Items[2].Title != "Promo opening Untitled post text" || plain.Items[3].Title != "An untitled promo" {
		t.Errorf("derived titles: %q and %q", plain.Items[2].Title, plain.Items[3].Title)
	}
	wantWarnings := []string{"Entry 2: cleanup removed the whole description; content omitted", "Entry 4: cleanup removed the whole description; content omitted"}
	if strings.Join(cleaned.Warnings, "\n") != strings.Join(wantWarnings, "\n") {
		t.Errorf("warnings %q", cleaned.Warnings)
	}

	// Filters read the cleaned description, here the summary that replaced
	// the full post.
	r := feedCleanupRecipe(".promo")
	r.Filters = &model.FilterSet{Exclude: &model.FilterRule{Op: "contains_any", Field: "description", Keywords: []string{"promo text"}}}
	if p := runFeedSource(t, []byte(cleanupFeed), "https://feed.example/rss", r); len(p.Items) != 7 || p.Filtered != 0 {
		t.Errorf("a filter read text the rules removed: %d stories, %d filtered", len(p.Items), p.Filtered)
	}
	r.Remove = nil
	if p := runFeedSource(t, []byte(cleanupFeed), "https://feed.example/rss", r); p.Filtered != 2 {
		t.Errorf("without rules, the filter excluded %d stories", p.Filtered)
	}
}
