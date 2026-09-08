package filter

import (
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"rss-workshop/internal/model"
)

type subject struct {
	item   model.Item
	values [3]string
	ready  [3]bool
}

func (s *subject) value(field int) string {
	if !s.ready[field] {
		var value string
		switch field {
		case 0:
			value = s.item.Title
		case 1:
			value = description(s.item.HTML)
		case 2:
			value = s.item.URL
		}
		s.values[field], s.ready[field] = normalize(value), true
	}
	return s.values[field]
}

func normalize(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	return strings.Map(func(r rune) rune {
		// Choose one representative from the Unicode simple-fold orbit. Lower
		// casing alone would treat Greek sigma and final sigma differently.
		canonical := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			canonical = min(canonical, next)
		}
		return canonical
	}, value)
}

func description(value string) string {
	doc, err := html.Parse(strings.NewReader(value))
	if err != nil {
		return ""
	}
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		block := false
		if n.Type == html.ElementNode {
			switch n.Data {
			case "head", "script", "style", "template":
				return
			case "address", "article", "aside", "blockquote", "br", "caption", "dd", "details", "dialog", "div", "dl", "dt", "fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr", "li", "main", "nav", "ol", "p", "pre", "section", "summary", "table", "tbody", "td", "tfoot", "th", "thead", "tr", "ul":
				block = true
				out.WriteByte(' ')
			}
		}
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if block {
			out.WriteByte(' ')
		}
	}
	walk(doc)
	return out.String()
}
