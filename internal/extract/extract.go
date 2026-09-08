package extract

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	"github.com/antchfx/htmlquery"
	"github.com/antchfx/xpath"
	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"rss-workshop/internal/diagnostics"
	"rss-workshop/internal/filter"
	"rss-workshop/internal/model"
)

var ErrNoItems = errors.New("no valid items")

var policy = bluemonday.UGCPolicy()

func ValidateRender(r model.Recipe) error {
	if r.Mode != "" && r.Mode != "static" && r.Mode != "browser" && r.Mode != "auto" && r.Mode != "flaresolverr" {
		return fmt.Errorf("fetch mode must be static, browser, auto, or flaresolverr")
	}
	if r.SettleMS < 0 || r.SettleMS > 5000 {
		return fmt.Errorf("render settle time must be 0–5000 milliseconds")
	}
	if len(r.WaitSelector) > 1000 {
		return fmt.Errorf("readiness selector exceeds 1000 characters")
	}
	if r.WaitSelector != "" && r.Mode != "flaresolverr" {
		if _, e := cascadia.Compile(r.WaitSelector); e != nil {
			return fmt.Errorf("invalid CSS readiness selector: %w", e)
		}
	}

	return nil
}

func Validate(r model.Recipe) error {
	if err := validateSelectors(r); err != nil {
		return err
	}
	_, err := filter.Compile(r.Filters)
	return err
}

func validateSelectors(r model.Recipe) error {
	if err := ValidateRender(r); err != nil {
		return err
	}
	if r.Type != "css" && r.Type != "xpath" {
		return fmt.Errorf("selector type must be css or xpath")
	}
	if strings.TrimSpace(r.Items) == "" || strings.TrimSpace(r.Title.Selector) == "" {
		return fmt.Errorf("item and title selectors are required")
	}
	for i, s := range []string{r.Items, r.Title.Selector, r.Link.Selector, r.Date.Selector, r.Content.Selector, r.Image.Selector} {
		if len(s) > 1000 {
			return fmt.Errorf("selector exceeds 1000 characters")
		}
		if s == "" {
			continue
		}
		if r.Type == "css" {
			if s != "." {
				if _, e := cascadia.Compile(s); e != nil {
					return fmt.Errorf("invalid CSS selector: %w", e)
				}
			}
		} else {
			if i > 0 && !strings.HasPrefix(s, ".") {
				return fmt.Errorf("XPath fields must be relative, starting with . (for example .//a/@href)")
			}
			if _, e := xpath.Compile(s); e != nil {
				return fmt.Errorf("invalid XPath selector: %w", e)
			}
		}
	}
	if r.Timezone != "" {
		if _, e := time.LoadLocation(r.Timezone); e != nil {
			return fmt.Errorf("unknown time zone")
		}
	}
	return nil
}
func URL(base *url.URL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	u = base.ResolveReference(u)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	u.Fragment = ""
	return u.String()
}
func selectNodes(n *html.Node, typ, s string) []*html.Node {
	if s == "" {
		return nil
	}
	if s == "." {
		return []*html.Node{n}
	}
	if typ == "xpath" {
		out, _ := htmlquery.QueryAll(n, s)
		return out
	}
	m, _ := cascadia.Compile(s)
	return goquery.NewDocumentFromNode(n).FindMatcher(m).Nodes
}

// htmlquery uses a detached synthetic element for an attribute result.
func isAttribute(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Parent == nil && n.DataAtom == 0
}
func clone(n *html.Node) *html.Node {
	v := &html.Node{Type: n.Type, DataAtom: n.DataAtom, Data: n.Data, Namespace: n.Namespace, Attr: append([]html.Attribute(nil), n.Attr...)}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		v.AppendChild(clone(c))
	}
	return v
}
func attr(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}
func field(n *html.Node, typ string, f model.Field, defaultAttr string) (string, *html.Node) {
	ns := selectNodes(n, typ, f.Selector)
	if len(ns) == 0 {
		return "", nil
	}
	v := ns[0]
	a := f.Attr
	if a == "" {
		a = defaultAttr
	}
	// htmlquery represents attribute XPath results as synthetic nodes.
	if isAttribute(v) {
		return strings.TrimSpace(htmlquery.InnerText(v)), v
	}
	if a != "" {
		return attr(v, a), v
	}
	return strings.TrimSpace(htmlquery.InnerText(v)), v
}
func imageURL(n *html.Node, base *url.URL) string {
	for _, k := range []string{"data-src", "data-lazy-src", "src"} {
		if u := URL(base, attr(n, k)); u != "" {
			return u
		}
	}
	for _, k := range []string{"data-srcset", "srcset"} {
		parts := strings.Split(attr(n, k), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			f := strings.Fields(parts[i])
			if len(f) > 0 {
				if u := URL(base, f[0]); u != "" {
					return u
				}
			}
		}
	}
	return ""
}
func content(n *html.Node, base *url.URL) string {
	// Clone before normalizing so overlapping field selectors are unaffected.
	var b bytes.Buffer
	_ = html.Render(&b, n)
	doc, _ := html.Parse(strings.NewReader(b.String()))
	var walk func(*html.Node)
	walk = func(v *html.Node) {
		if v.Type == html.ElementNode {
			if v.Data == "img" {
				u := imageURL(v, base)
				v.Attr = append(v.Attr, html.Attribute{Key: "src", Val: u})
				for i := 0; i < len(v.Attr)-1; i++ {
					if v.Attr[i].Key == "src" {
						v.Attr[i].Val = u
					}
				}
			}
			for i, a := range v.Attr {
				if a.Key == "href" || a.Key == "src" {
					v.Attr[i].Val = URL(base, a.Val)
				}
			}
		}
		for c := v.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	b.Reset()
	_ = html.Render(&b, doc)
	return policy.Sanitize(b.String())
}
func Run(body []byte, effective string, r model.Recipe) (model.Preview, error) {
	return RunAt(body, effective, r, time.Now())
}

// RunAt uses one observation time for every relative date in a fetched page.
// A supplied reference also makes relative-date extraction deterministic.
func RunAt(body []byte, effective string, r model.Recipe, observedAt time.Time) (model.Preview, error) {
	out := model.Preview{Items: []model.Item{}, Warnings: []string{}}
	if observedAt.IsZero() || observedAt.UTC().Year() < 1 || observedAt.UTC().Year() > 9999 {
		return out, fmt.Errorf("invalid page observation time")
	}
	if e := validateSelectors(r); e != nil {
		return out, e
	}
	matcher, e := filter.Compile(r.Filters)
	if e != nil {
		return out, e
	}
	base, e := url.Parse(effective)
	if e != nil || base.Host == "" {
		return out, fmt.Errorf("invalid document URL")
	}
	doc, e := html.Parse(bytes.NewReader(body))
	if e != nil {
		return out, e
	}
	if ns := selectNodes(doc, "css", "base[href]"); len(ns) > 0 {
		if u := URL(base, attr(ns[0], "href")); u != "" {
			base, _ = url.Parse(u)
		}
	}
	nodes := selectNodes(doc, r.Type, r.Items)
	out.Matches = len(nodes)
	if len(nodes) > 1000 {
		return out, fmt.Errorf("selector matched more than 1000 items; narrow the selector")
	}
	seen := map[string]bool{}
	loc := time.UTC
	if r.Timezone != "" {
		loc, _ = time.LoadLocation(r.Timezone)
	}
	totalBytes := 0
	for i, n := range nodes {
		if r.Type == "xpath" {
			n = clone(n)
		}
		title, titleNode := field(n, r.Type, r.Title, "")
		rawLink, linkNode := field(n, r.Type, r.Link, "href")
		link := URL(base, rawLink)
		if title == "" {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Match %d skipped: empty title; %s", i+1,
				fieldMismatch("Title", "non-empty text", title, titleNode, r.Title, "")))
			continue
		}
		key := link
		if key == "" {
			if r.Link.Selector != "" {
				out.Warnings = append(out.Warnings, fmt.Sprintf("Match %d skipped: missing or unsafe item URL; %s", i+1,
					fieldMismatch("Link", "an HTTP(S) URL", rawLink, linkNode, r.Link, "href")))
				continue
			}
			key = fmt.Sprintf("title:%x", sha256.Sum256([]byte(title)))
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		it := model.Item{Key: key, Title: title, URL: link}
		rawContent, cn := field(n, r.Type, r.Content, "")
		if cn != nil {
			if r.Content.Attr != "" || isAttribute(cn) {
				it.HTML = html.EscapeString(rawContent)
			} else {
				it.HTML = content(cn, base)
			}
		}
		if r.Content.Selector != "" && strings.TrimSpace(it.HTML) == "" {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Match %d: %s; content omitted", i+1,
				fieldMismatch("Content", "text or safe HTML", rawContent, cn, r.Content, "")))
		}
		raw, im := field(n, r.Type, r.Image, "")
		if im != nil {
			if r.Image.Attr != "" || isAttribute(im) {
				it.Image = URL(base, raw)
			} else {
				it.Image = imageURL(im, base)
			}
		} else if r.Image.Selector == "" {
			if ns := selectNodes(n, "css", "img"); len(ns) > 0 {
				it.Image = imageURL(ns[0], base)
			}
		}
		if r.Image.Selector != "" && it.Image == "" {
			// Automatic image extraction reads URL-bearing attributes, not the
			// selected element's text. Report the first supplied candidate.
			imageField := r.Image
			if im != nil && imageField.Attr == "" && !isAttribute(im) {
				raw = ""
				for _, k := range []string{"data-src", "data-lazy-src", "src", "data-srcset", "srcset"} {
					if v := attr(im, k); strings.TrimSpace(v) != "" {
						raw, imageField.Attr = v, k
						break
					}
				}
				if imageField.Attr == "" {
					imageField.Attr = "src"
				}
			}
			out.Warnings = append(out.Warnings, fmt.Sprintf("Match %d: %s; image omitted", i+1,
				fieldMismatch("Image", "an HTTP(S) image URL", raw, im, imageField, "")))
		}
		if it.Image != "" {
			it.HTML = "<p><img src=\"" + html.EscapeString(it.Image) + "\" alt=\"\"></p>" + it.HTML
		}
		raw, dateNode := field(n, r.Type, r.Date, "")
		// A selected element can display a relative age while retaining an exact
		// machine-readable datetime. Explicit attributes and XPath text/attribute
		// results keep the user's chosen value instead of inspecting another one.
		if r.Date.Attr == "" && dateNode != nil && dateNode.Type == html.ElementNode && !isAttribute(dateNode) {
			it.Published = parseAbsoluteDate(attr(dateNode, "datetime"), "", loc)
		}
		if it.Published.IsZero() && raw != "" {
			it.Published = parseAbsoluteDate(raw, r.DateLayout, loc)
			if it.Published.IsZero() {
				it.Published = parseRelativeDate(raw, observedAt, loc)
				if !it.Published.IsZero() {
					it.PublishedEstimated = true
					it.PublishedSource = strings.TrimSpace(raw)
				}
			}
		}
		if r.Date.Selector != "" && it.Published.IsZero() {
			dateField := r.Date
			if strings.TrimSpace(raw) == "" && dateField.Attr == "" && dateNode != nil && !isAttribute(dateNode) && attr(dateNode, "datetime") != "" {
				raw, dateField.Attr = attr(dateNode, "datetime"), "datetime"
			}
			out.Warnings = append(out.Warnings, fmt.Sprintf("Match %d: date not recognized; %s; using first-seen time", i+1,
				fieldMismatch("Date", "a date or relative age", raw, dateNode, dateField, "")))
		}
		if len(it.Title) > 2048 || len(it.HTML) > 256<<10 || len(it.URL) > 8192 || len(it.Image) > 8192 {
			return out, fmt.Errorf("extracted item exceeds size limits; narrow content selector")
		}
		totalBytes += len(it.HTML) + len(it.Title)
		if totalBytes > 8<<20 {
			return out, fmt.Errorf("extracted output exceeds 8 MiB limit")
		}
		out.Valid++
		decision := matcher.Evaluate(it)
		if !decision.Matched {
			out.Filtered++
			if len(out.FilterExamples) < 20 {
				title := strings.Join(strings.Fields(diagnostics.SafeText(it.Title, 512)), " ")
				runes := []rune(title)
				if len(runes) > 120 {
					title = string(runes[:119]) + "…"
				}
				out.FilterExamples = append(out.FilterExamples, model.FilterExample{Title: title, Reason: diagnostics.SafeText(decision.Reason, 512)})
			}
			continue
		}
		out.Items = append(out.Items, it)
	}
	if out.Valid == 0 {
		return out, fmt.Errorf("%w: selector produced zero valid items (%d matches); saved history is retained", ErrNoItems, out.Matches)
	}
	return out, nil
}
