package extract

import (
	"bytes"
	"fmt"
	"net/url"

	"golang.org/x/net/html"
	"rss-workshop/internal/model"
)

// MaxArticleBytes bounds one stored article body. It matches the per-item HTML
// limit Run applies to list-page content, so enabling full content cannot make
// an item larger than extraction already allows.
const MaxArticleBytes = 256 << 10

// Article extracts the body of a single item's own page, using the recipe's
// CSS/XPath Type and the same sanitization, URL absolutization and <base>
// handling as list-page content. It deliberately returns only the content: an
// article page must never be able to change an item's identity, date or link.
func Article(body []byte, effective string, r model.Recipe) (string, error) {
	if r.Full == nil || r.Full.Selector == "" {
		return "", fmt.Errorf("no article selector configured")
	}
	base, e := url.Parse(effective)
	if e != nil || base.Host == "" {
		return "", fmt.Errorf("invalid article URL")
	}
	doc, e := html.Parse(bytes.NewReader(body))
	if e != nil {
		return "", e
	}
	if ns := selectNodes(doc, "css", "base[href]"); len(ns) > 0 {
		if u := URL(base, attr(ns[0], "href")); u != "" {
			if parsed, err := url.Parse(u); err == nil && parsed.Host != "" {
				base = parsed
			}
		}
	}
	nodes := selectNodes(doc, r.Type, r.Full.Selector)
	if len(nodes) == 0 {
		return "", fmt.Errorf("article selector matched nothing on the item page")
	}
	// Only the first match is used. A selector matching several blocks is a
	// recipe problem the operator should see in diagnostics, not something to
	// paper over by concatenating unrelated sections of the page.
	out := ""
	if r.Full.Attr != "" {
		out = policy.Sanitize(attr(nodes[0], r.Full.Attr))
	} else {
		out = content(nodes[0], base)
	}
	if len(out) > MaxArticleBytes {
		return "", fmt.Errorf("article body exceeds %d bytes", MaxArticleBytes)
	}
	return out, nil
}
