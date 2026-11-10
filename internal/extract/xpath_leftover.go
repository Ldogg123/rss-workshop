package extract

import (
	"fmt"

	"github.com/antchfx/xpath"
	"rss-workshop/internal/model"
)

// unfinishedXPath reports whether the XPath library would run only the start
// of s. Its parser returns the first complete expression without checking that
// the input ended there, so ".summary", a CSS selector left behind by a change
// of selector type, compiles and evaluates as ".": the whole card or page.
// Inside parentheses that expression has to end at the closing one, so the
// library reports what follows it. A stray closing parenthesis still ends the
// group early and passes; the expression before it is what runs either way.
//
// A selector that does not compile at all is not this function's to report:
// validation refuses it everywhere.
func unfinishedXPath(s string) bool {
	if s == "" {
		return false
	}
	if _, err := xpath.Compile(s); err != nil {
		return false
	}
	_, err := xpath.Compile("(" + s + ")")
	return err != nil
}

// xpathSelector is one selector of an XPath recipe, with the editor's name
// for it and what a CSS class selector left in it does.
type xpathSelector struct {
	name, text     string
	css, does, fix string
}

// xpathSelectors lists the selectors an XPath recipe evaluates as written:
// the repeated items, the card fields and the article body. Cleanup rules
// have their own guard in validateCleanup, and a feed source's selectors are
// CSS.
func xpathSelectors(r model.Recipe) []xpathSelector {
	if r.Type != "xpath" {
		return nil
	}
	out := []xpathSelector{{name: "Repeated item", text: r.Items, css: ".post", does: "makes the whole page one item", fix: "//*[@class='post']"}}
	for _, f := range []struct {
		name string
		f    model.Field
	}{{"Title", r.Title}, {"Link", r.Link}, {"Description", r.Content}, {"Image", r.Image}, {"Date", r.Date}} {
		out = append(out, xpathSelector{name: f.name, text: f.f.Selector, css: ".summary", does: "selects the whole card", fix: ".//*[@class='summary']"})
	}
	if r.Full != nil {
		out = append(out, xpathSelector{name: "Article body", text: r.Full.Selector, css: ".entry-content", does: "selects the whole page", fix: "//*[@class='entry-content']"})
	}
	return out
}

// unfinished describes the problem in fixed text that names the selector by
// its field and never quotes it, because a selector can hold a URL and the
// text is stored with a refresh's diagnostics.
func (s xpathSelector) unfinished() string {
	return fmt.Sprintf("%s selector: XPath uses only its first complete expression and ignores the text after it. "+
		"A CSS selector left after a change of selector type, such as %s, %s. Write it as XPath, such as %s; "+
		"for a recipe still written in CSS, set the selector type back to CSS and use Convert to XPath in the visual selector.", s.name, s.css, s.does, s.fix)
}

// SelectorWarnings returns one warning for each selector of an XPath recipe
// that the library would run only the start of. Extraction stays lenient
// about them, because a stored recipe that publishes today must not start
// failing its refreshes; the warning is how a preview, a refresh's
// diagnostics and an import review say that such a selector matches more
// than it reads.
func SelectorWarnings(r model.Recipe) []string {
	var out []string
	for _, s := range xpathSelectors(r) {
		if unfinishedXPath(s.text) {
			out = append(out, s.unfinished())
		}
	}
	return out
}

// ValidateNew refuses a selector SelectorWarnings would report when this save
// introduces it: in a new feed, for which stored is nil, when its text
// changed, or when the recipe's type just became XPath, since the type menu
// keeps the existing text. A selector saved as it is stored passes, as it
// does in Validate, so an existing feed can still be renamed or rescheduled
// without rewriting it first. Imports use Validate alone and stay as lenient
// as the release that wrote the file.
func ValidateNew(r model.Recipe, stored *model.Recipe) error {
	kept := map[string]string{}
	if stored != nil {
		for _, s := range xpathSelectors(*stored) {
			kept[s.name] = s.text
		}
	}
	for _, s := range xpathSelectors(r) {
		if unfinishedXPath(s.text) && kept[s.name] != s.text {
			return fmt.Errorf("invalid XPath in the %s", s.unfinished())
		}
	}
	return nil
}
