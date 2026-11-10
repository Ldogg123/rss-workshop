package web

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"rss-workshop/internal/model"
)

// leftoverPage is a list page with one card, and its own article page.
const leftoverPage = `<html><body><nav>Site navigation</nav><div class="post"><article><h2>Headline</h2><a href="/one">more</a>` +
	`<p class="teaser-text">Summary</p><div>Share this</div></article></div><div class="entry-body"><p>Article text</p></div></body></html>`

// leftoverFeed is an XPath feed whose Description selector is a CSS class
// selector, as the selector type menu leaves it. v1.3.0 saved it, and the
// XPath library reads it as ".", the whole card.
func leftoverFeed() model.Feed {
	return model.Feed{Title: "Leftover", URL: "https://example.com/news", Interval: 900,
		Recipe: model.Recipe{Mode: "static", Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"},
			Link: model.Field{Selector: ".//a/@href"}, Content: model.Field{Selector: ".teaser-text"}}}
}

func refusal(t *testing.T, h *portabilityHarness, method, path string, f model.Feed, field string) {
	t.Helper()
	w := h.request(method, path, feedJSON(t, f))
	var out struct{ Error string }
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != http.StatusBadRequest || err != nil ||
		!strings.HasPrefix(out.Error, "invalid XPath in the "+field+" selector: XPath uses only its first complete expression") {
		t.Fatalf("%s %s = %d %s, want a refusal naming the %s selector", method, path, w.Code, w.Body.String(), field)
	}
	// The selector can hold a URL, so the message never repeats it.
	for _, text := range []string{"teaser", "entry-body", ".thumb"} {
		if strings.Contains(out.Error, text) {
			t.Fatalf("the refusal quotes the selector: %s", out.Error)
		}
	}
}

// A save refuses CSS left in an XPath selector when the save introduces it:
// in a new feed, with changed text, or by changing the selector type, which
// keeps the text. A feed stored with one by an earlier release can still be
// edited without rewriting it, and nothing is fetched or stored by a refusal.
func TestSaveRefusesOnlyANewUnfinishedXPathSelector(t *testing.T) {
	h := newPortabilityHarness(t)
	ctx := context.Background()
	refusal(t, h, "POST", "/api/feeds", leftoverFeed(), "Description")
	article := leftoverFeed()
	article.Recipe.Content.Selector = ".//p"
	article.Recipe.Full = &model.FullContent{Selector: ".entry-body"}
	refusal(t, h, "POST", "/api/feeds", article, "Article body")
	if feeds, err := h.app.Store.List(ctx); err != nil || len(feeds) != 0 {
		t.Fatalf("a refused feed was stored: %+v, %v", feeds, err)
	}

	// As an earlier release stored it, article selector included.
	stored := leftoverFeed()
	stored.Recipe.Full = &model.FullContent{Selector: ".entry-body"}
	id, err := h.app.Store.Save(ctx, stored)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/feeds/" + id
	edit := stored
	edit.Title, edit.Recipe.UserAgent, edit.Recipe.Title.Selector = "Renamed", "Reader/1", ".//h3"
	if w := h.request("PUT", path, feedJSON(t, edit)); w.Code != http.StatusOK {
		t.Fatalf("an edit that keeps the stored selectors = %d %s", w.Code, w.Body.String())
	}
	before, err := h.app.Store.Get(ctx, id)
	if err != nil || before.Title != "Renamed" || !reflect.DeepEqual(before.Recipe, edit.Recipe) {
		t.Fatalf("stored %+v, %v", before, err)
	}

	changed := edit
	changed.Recipe.Content.Selector = ".teaser"
	refusal(t, h, "PUT", path, changed, "Description")
	changed = edit
	changed.Recipe.Full = &model.FullContent{Selector: ".entry"}
	refusal(t, h, "PUT", path, changed, "Article body")
	changed = edit
	changed.Recipe.Image.Selector = ".thumb"
	refusal(t, h, "PUT", path, changed, "Image")
	if after, err := h.app.Store.Get(ctx, id); err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("a refused edit changed the feed: %+v, %v", after, err)
	}

	// Repairing one selector does not require repairing the other.
	repaired := edit
	repaired.Recipe.Content.Selector = ".//p[@class='teaser-text']"
	if w := h.request("PUT", path, feedJSON(t, repaired)); w.Code != http.StatusOK {
		t.Fatalf("repairing the description = %d %s", w.Code, w.Body.String())
	}

	// The selector type menu keeps the text, and every field of this CSS
	// recipe starts with ".", so the server accepted the switch.
	css := model.Feed{Title: "Classes", URL: "https://example.com/news", Interval: 900,
		Recipe: model.Recipe{Mode: "static", Type: "css", Items: ".post", Title: model.Field{Selector: ".headline"}, Content: model.Field{Selector: ".teaser-text"}}}
	cssID, err := h.app.Store.Save(ctx, css)
	if err != nil {
		t.Fatal(err)
	}
	if w := h.request("PUT", "/api/feeds/"+cssID, feedJSON(t, css)); w.Code != http.StatusOK {
		t.Fatalf("saving the CSS recipe = %d %s", w.Code, w.Body.String())
	}
	switched := css
	switched.Recipe.Type = "xpath"
	refusal(t, h, "PUT", "/api/feeds/"+cssID, switched, "Repeated item")
	if f, err := h.app.Store.Get(ctx, cssID); err != nil || f.Recipe.Type != "css" {
		t.Fatalf("the refused switch was stored: %+v, %v", f.Recipe, err)
	}

	// A feed deleted meanwhile is reported as missing, as for any save.
	if w := h.request("PUT", "/api/feeds/0000", feedJSON(t, leftoverFeed())); w.Code != http.StatusNotFound {
		t.Fatalf("saving a missing feed = %d %s", w.Code, w.Body.String())
	}
	if h.fetcher.calls.Load() != 0 {
		t.Fatal("a save reached the fetcher")
	}
}

// Preview runs the recipe as a refresh would, whole card and whole page
// included, and says which selectors are responsible.
func TestPreviewWarnsAboutAnUnfinishedXPathSelector(t *testing.T) {
	h := newPortabilityHarness(t)
	h.fetcher.body = []byte(leftoverPage)
	f := leftoverFeed()
	f.Recipe.Full = &model.FullContent{Selector: ".entry-body"}
	w := h.request("POST", "/api/preview", feedJSON(t, f))
	var out model.Preview
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != http.StatusOK || err != nil || len(out.Items) != 1 {
		t.Fatalf("preview = %d %s", w.Code, w.Body.String())
	}
	if item := out.Items[0]; !strings.Contains(item.HTML, "Share this") || !strings.Contains(item.FullHTML, "Site navigation") {
		t.Fatalf("the preview does not show what a refresh would publish: %+v", item)
	}
	if len(out.Warnings) != 2 || !strings.HasPrefix(out.Warnings[0], "Description selector: XPath uses only its first complete expression") ||
		!strings.HasPrefix(out.Warnings[1], "Article body selector: XPath uses only its first complete expression") {
		t.Fatalf("warnings %q", out.Warnings)
	}
	for _, warning := range out.Warnings {
		if strings.Contains(warning, "teaser") || strings.Contains(warning, "entry-body") {
			t.Fatalf("the warning quotes the selector: %s", warning)
		}
	}
	if trace := out.Diagnostics; trace == nil || len(trace.Attempts) != 1 || !reflect.DeepEqual(trace.Attempts[0].Warnings, out.Warnings) {
		t.Fatalf("the preview's diagnostics lack the warnings: %+v", trace)
	}

	// A complete recipe previews without one.
	f.Recipe.Content.Selector, f.Recipe.Full.Selector = ".//p", "//div[@class='entry-body']"
	w = h.request("POST", "/api/preview", feedJSON(t, f))
	out = model.Preview{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != http.StatusOK || err != nil || len(out.Items) != 1 || len(out.Warnings) != 0 ||
		out.Items[0].HTML != "<p>Summary</p>" || strings.Contains(out.Items[0].FullHTML, "Site navigation") {
		t.Fatalf("preview = %d %s", w.Code, w.Body.String())
	}
}

// A recipe file exported by v1.3.0 can hold such a selector, so the import
// accepts it and stores it as written. The review names it beside the recipe
// it belongs to.
func TestImportKeepsAnUnfinishedXPathSelectorAndReviewWarns(t *testing.T) {
	h := newPortabilityHarness(t)
	ctx := context.Background()
	f := leftoverFeed()
	recipes := marshalRecipes(t, []portableFeed{portableExamples()[1], {Title: f.Title, URL: f.URL, Interval: f.Interval, Recipe: f.Recipe}})
	w := h.request("POST", "/api/recipes/preview", recipes)
	var review struct {
		Count int
		Feeds []map[string]json.RawMessage
	}
	if err := json.Unmarshal(w.Body.Bytes(), &review); w.Code != http.StatusOK || err != nil || review.Count != 2 || len(review.Feeds) != 2 {
		t.Fatalf("review = %d %s", w.Code, w.Body.String())
	}
	if _, present := review.Feeds[0]["warnings"]; present {
		t.Fatalf("a complete XPath recipe is reviewed with a warning: %s", review.Feeds[0]["warnings"])
	}
	var warnings []string
	if err := json.Unmarshal(review.Feeds[1]["warnings"], &warnings); err != nil || len(warnings) != 1 ||
		!strings.HasPrefix(warnings[0], "Description selector: XPath uses only its first complete expression") || strings.Contains(warnings[0], "teaser") {
		t.Fatalf("review warnings %q, %v", warnings, err)
	}
	// The review still shows the recipe itself, as the import will store it.
	var reviewed model.Recipe
	if err := json.Unmarshal(review.Feeds[1]["recipe"], &reviewed); err != nil || !reflect.DeepEqual(reviewed, f.Recipe) {
		t.Fatalf("reviewed recipe %+v, %v", reviewed, err)
	}

	w = h.request("POST", "/api/recipes/import", recipes)
	var imported struct{ IDs []string }
	if err := json.Unmarshal(w.Body.Bytes(), &imported); w.Code != http.StatusCreated || err != nil || len(imported.IDs) != 2 {
		t.Fatalf("import = %d %s", w.Code, w.Body.String())
	}
	got, err := h.app.Store.Get(ctx, imported.IDs[1])
	if err != nil || !reflect.DeepEqual(got.Recipe, f.Recipe) {
		t.Fatalf("imported recipe %+v, %v", got.Recipe, err)
	}
	// Imported as stored, it is edited like any feed an earlier release saved.
	got.Enabled = true
	if w := h.request("PUT", "/api/feeds/"+got.ID, feedJSON(t, got)); w.Code != http.StatusOK {
		t.Fatalf("resuming the imported feed from the editor = %d %s", w.Code, w.Body.String())
	}
	if h.fetcher.calls.Load() != 0 {
		t.Fatal("reviewing or importing reached the fetcher")
	}
}
