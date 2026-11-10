package extract

import (
	"slices"
	"strings"
	"testing"

	"rss-workshop/internal/model"
)

// class is the test Convert to XPath writes for a CSS class.
func class(name string) string {
	return "contains(concat(' ', normalize-space(@class), ' '), ' " + name + " ')"
}

// convertedPage has an article block that holds another article, so a rule
// naming an article can mean the block itself, the nested one, or both.
const convertedPage = `<nav>Site</nav><article class="post"><h1>Head</h1><p>Lead paragraph</p><blockquote><p>Quoted</p></blockquote>
<div class="share">Top share</div><div class="post-footer"><div class="share">Footer share</div></div>
<article class="nested"><p>Nested paragraph</p><div class="share">Nested share</div></article></article>`

// A CSS cleanup rule runs on a detached copy of its block and still counts the
// block as an ancestor, so "article .share" cleans an article body. Convert to
// XPath (selector-frame.js) writes such a rule as a union of the block itself
// and its descendants. These are the forms it emits: each must remove exactly
// what the CSS rule removes, or converting a recipe silently changes what it
// publishes. The shorter ./descendant-or-self::article/p is not equivalent in
// the Go XPath library, which reads it as .//p.
func TestConvertedCleanupRulesRemoveWhatTheCSSRulesRemove(t *testing.T) {
	const texts = "Head|Lead paragraph|Quoted|Top share|Footer share|Nested paragraph|Nested share"
	for _, tc := range []struct {
		css, xpath string
		gone       string
	}{
		{"article .share", "./self::article//*[" + class("share") + "] | .//article//*[" + class("share") + "]", "Top share|Footer share|Nested share"},
		{"article > p", "./self::article/p | .//article/p", "Lead paragraph|Nested paragraph"},
		{".post > .share", "./self::*[" + class("post") + "]/*[" + class("share") + "] | .//*[" + class("post") + "]/*[" + class("share") + "]", "Top share"},
		{".post-footer .share", "./self::*[" + class("post-footer") + "]//*[" + class("share") + "] | .//*[" + class("post-footer") + "]//*[" + class("share") + "]", "Footer share"},
		{".nested p, article blockquote", "./self::*[" + class("nested") + "]//p | .//*[" + class("nested") + "]//p | ./self::article//blockquote | .//article//blockquote", "Quoted|Nested paragraph"},
		// One part can only name something inside the block, never the block.
		{"article", ".//article", "Nested paragraph|Nested share"},
	} {
		t.Run(tc.css, func(t *testing.T) {
			converted := model.Recipe{Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h1"},
				Full: &model.FullContent{Selector: "//article[" + class("post") + "]"}, Remove: []string{tc.xpath}}
			if err := Validate(converted); err != nil {
				t.Fatalf("the converted recipe is refused: %v", err)
			}
			want, err := Article([]byte(convertedPage), "https://example.com/s", model.Recipe{Type: "css", Full: &model.FullContent{Selector: "article.post"}, Remove: []string{tc.css}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := Article([]byte(convertedPage), "https://example.com/s", converted)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("the converted rule published\n%s\nand the CSS rule\n%s", got, want)
			}
			for _, text := range strings.Split(texts, "|") {
				if gone := slices.Contains(strings.Split(tc.gone, "|"), text); strings.Contains(want, text) == gone {
					t.Errorf("%q removed: %v, want %v, in %s", text, !gone, gone, want)
				}
			}
		})
	}
}

// The same holds for a description, whose block is the element the Description
// selector matched.
func TestConvertedCleanupRuleCleansADescriptionNamedByTheRule(t *testing.T) {
	plain, _ := runOne(t, cleanupPage, cleanupRecipe("css"))
	want, _ := runOne(t, cleanupPage, cleanupRecipe("css", ".summary .share"))
	got, _ := runOne(t, cleanupPage, cleanupRecipe("xpath", "./self::*["+class("summary")+"]//*["+class("share")+"] | .//*["+class("summary")+"]//*["+class("share")+"]"))
	if !strings.Contains(plain.HTML, "Share") || strings.Contains(want.HTML, "Share") {
		t.Fatalf("the CSS rule did not remove the share block: %s", want.HTML)
	}
	if got.HTML != want.HTML {
		t.Fatalf("the converted rule published\n%s\nand the CSS rule\n%s", got.HTML, want.HTML)
	}
}

// Only an article selector's first match is published. A CSS list takes the
// first match in the page, whichever alternative it is; Convert to XPath
// refuses such a list because an XPath union does not, and the guide gives
// this single path to write by hand instead.
func TestArticleSelectorListByHandMatchesCSS(t *testing.T) {
	for name, page := range map[string]string{
		"nested":   `<article><h1>Head</h1><div class="entry-content"><p>Inner</p></div><p>Tail</p></article>`,
		"siblings": `<nav>Nav</nav><article><p>Article first</p></article><div class="entry-content"><p>Div second</p></div>`,
		"reversed": `<div class="entry-content"><p>Div first</p></div><article><p>Article second</p></article>`,
	} {
		t.Run(name, func(t *testing.T) {
			want, err := Article([]byte(page), "https://example.com/s", model.Recipe{Type: "css", Full: &model.FullContent{Selector: ".entry-content, article"}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := Article([]byte(page), "https://example.com/s", model.Recipe{Type: "xpath", Full: &model.FullContent{Selector: "//*[self::article or " + class("entry-content") + "]"}})
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("the XPath selector published\n%s\nand the CSS list\n%s", got, want)
			}
		})
	}
}
