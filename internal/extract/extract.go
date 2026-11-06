package extract

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"runtime/debug"
	"strings"
	"time"
	"unicode"

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

// ErrSelectorFailed reports that extraction stopped because a selector library
// panicked, which antchfx/xpath does on ordinary page content. It is not
// ErrNoItems: rendering the page would not make the selector work.
var ErrSelectorFailed = errors.New("selector evaluation failed")

// maxPanicStack bounds what one failure writes to the log. A page can cause one
// per article on every refresh, and the frames naming the failure come first.
const maxPanicStack = 4 << 10

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
	// The article selector runs against the item's own page, not against a card
	// on the list page, so XPath here is absolute like Items rather than
	// relative like the per-field selectors.
	if r.Full != nil {
		s := strings.TrimSpace(r.Full.Selector)
		if s == "" {
			return fmt.Errorf("full article content needs a selector for the article body")
		}
		if len(s) > 1000 {
			return fmt.Errorf("selector exceeds 1000 characters")
		}
		if len(r.Full.Attr) > 200 {
			return fmt.Errorf("attribute name exceeds 200 characters")
		}
		if r.Type == "css" {
			if _, e := cascadia.Compile(s); e != nil {
				return fmt.Errorf("invalid CSS selector for the article body: %w", e)
			}
		} else if _, e := xpath.Compile(s); e != nil {
			return fmt.Errorf("invalid XPath selector for the article body: %w", e)
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

// imageAttributes are the attributes automatic detection reads first, in order,
// and that a warning reports when none of them gave a usable address.
var imageAttributes = []string{"data-src", "data-lazy-src", "src", "data-srcset", "srcset"}

// lazyImageAttributes hold the real address for lazy-loading scripts and CDNs
// that leave a placeholder, such as an inline SVG data: URI, in src. Detection
// reads them only when imageAttributes gave no address, so an image that
// already resolves keeps its address. They are listed one by one because a name
// does not reveal an attribute's purpose: data-image-link, data-video-src and
// data-original-title all look like image attributes. Originals come first, and
// format variants and thumbnails last.
var lazyImageAttributes = []string{
	"data-original", "data-orig-src", "data-orig-file", "data-original-src", "data-lazy-original", "data-wpfc-original-src",
	"data-lazy", "data-lazyload", "data-flickity-lazyload", "data-flickity-lazyload-src", "data-echo", "data-breeze", "nitro-lazy-src", "data-pagespeed-lazy-src", "data-cfsrc", "data-ezsrc", "data-actualsrc",
	"data-image", "data-image-src", "data-img", "data-img-src", "data-img-url",
	"data-webp", "data-avif", "data-thumb",
}

// lazySrcsetAttributes are the same for attributes holding a srcset list.
var lazySrcsetAttributes = []string{"data-lazy-srcset", "data-original-srcset", "data-original-set", "data-flickity-lazyload-srcset", "data-brsrcset", "nitro-lazy-srcset"}

const (
	// maxImageAddress is the longest image address an item can publish.
	maxImageAddress = 8192
	// maxSrcset bounds the work a source page can demand per card. Far longer than
	// a real list, and the end of a value is where the usable candidates are.
	maxSrcset = 256 << 10
	// maxLazySrcset is lower because every candidate is parsed when none is usable.
	maxLazySrcset = 16 << 10
)

// imageURL reads the attributes images normally keep their address in. Images
// inside a description or an article use only these: the story's own image is
// published once in its body, and addresses read from other attributes must not
// be able to grow a description past its limit.
func imageURL(n *html.Node, base *url.URL) string {
	for _, k := range []string{"data-src", "data-lazy-src", "src"} {
		if u := URL(base, attr(n, k)); u != "" {
			return u
		}
	}
	for _, k := range []string{"data-srcset", "srcset"} {
		if u := srcsetURL(base, attr(n, k), false); u != "" {
			return u
		}
	}
	return ""
}

// itemImageURL returns a story's image: imageURL, then an img element's
// lazy-loading attributes. Other elements stop at imageURL because an image
// selector can reach them, and on them the same names hold links and streams.
func itemImageURL(n *html.Node, base *url.URL) string {
	if u := imageURL(n, base); u != "" || n.Data != "img" {
		return u
	}
	for _, k := range lazyImageAttributes {
		if u := lazyImageURL(base, attr(n, k)); u != "" {
			return u
		}
	}
	for _, k := range lazySrcsetAttributes {
		if u := srcsetURL(base, attr(n, k), true); u != "" {
			return u
		}
	}
	return ""
}

// srcsetURL returns the last usable candidate of a srcset value, which is
// normally the largest, or of a single URL. A candidate too long to publish is
// not usable, so it cannot fail the whole page. lazy accepts only image
// addresses, for attributes other than the standard ones.
func srcsetURL(base *url.URL, value string, lazy bool) string {
	if lazy && len(value) > maxLazySrcset {
		return ""
	}
	candidates := srcsetCandidates(value)
	for i := len(candidates) - 1; i >= 0; i-- {
		u := ""
		if lazy {
			u = lazyImageURL(base, candidates[i])
		} else if u = URL(base, candidates[i]); len(u) > maxImageAddress {
			u = ""
		}
		if u != "" {
			return u
		}
	}
	return ""
}

// srcsetCandidates splits a srcset value the way browsers split it: a URL runs
// to the next whitespace, so commas inside it, as in a data: URI or a CDN
// transformation such as w_400,h_300, stay part of it. Descriptors run to the
// next comma. Unlike a browser it also separates addresses joined by a bare
// comma where joinedAddresses can tell, which sites do publish.
func srcsetCandidates(value string) []string {
	if len(value) > maxSrcset {
		// The cut may fall inside a candidate, whose commas are not separators,
		// so restart at the first comma after the run it fell in.
		value = strings.TrimLeftFunc(value[len(value)-maxSrcset:], unicode.IsSpace)
		if space := strings.IndexFunc(value, unicode.IsSpace); space > 0 {
			value = value[space-1:] // The run's last byte may be its trailing comma.
		}
		_, value, _ = strings.Cut(value, ",")
	}
	var out []string
	separator := func(r rune) bool { return r == ',' || unicode.IsSpace(r) }
	for rest := value; ; {
		rest = strings.TrimLeftFunc(rest, separator)
		if rest == "" {
			return out
		}
		end := strings.IndexFunc(rest, unicode.IsSpace)
		if end < 0 {
			end = len(rest)
		}
		candidate := rest[:end]
		rest = rest[end:]
		if trimmed := strings.TrimRight(candidate, ","); trimmed != candidate {
			// A trailing comma ends the candidate without descriptors.
			candidate = trimmed
		} else if comma := strings.IndexByte(rest, ','); comma >= 0 {
			rest = rest[comma+1:]
		} else {
			rest = ""
		}
		out = append(out, joinedAddresses(candidate)...)
	}
}

// joinedAddresses splits /a.jpg,/b.jpg into two addresses while keeping the
// comma of a CDN address such as /w_400,h_300/a.jpg: a comma separates only
// when an address visibly starts after it or an image file ends before it. The
// payload of a data: URI is never split.
func joinedAddresses(value string) []string {
	if len(value) >= 5 && strings.EqualFold(value[:5], "data:") {
		return []string{value}
	}
	var out []string
	start := 0
	for i := 0; i < len(value); i++ {
		if value[i] != ',' {
			continue
		}
		before, after := value[start:i], value[i+1:]
		if tail := len(before) - len(".jpeg"); tail > 0 {
			before = before[tail:] // Every image extension fits, which keeps this linear.
		}
		if imageExtension(path.Ext(before)) || addressStart(after) {
			out = append(out, value[start:i])
			start = i + 1
		}
	}
	return append(out, value[start:])
}

func addressStart(value string) bool {
	if strings.HasPrefix(value, "/") {
		return true
	}
	for _, scheme := range []string{"http://", "https://"} {
		if len(value) >= len(scheme) && strings.EqualFold(value[:len(scheme)], scheme) {
			return true
		}
	}
	return false
}

func imageExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".jfif", ".png", ".gif", ".webp", ".avif", ".heic", ".svg", ".bmp", ".tif", ".tiff":
		return true
	}
	return false
}

// lazyImageURL resolves a value from a lazy-loading attribute, or returns
// nothing when it is not a single image address short enough to publish.
func lazyImageURL(base *url.URL, value string) string {
	value = strings.TrimSpace(value)
	if !imageAddress(value) {
		return ""
	}
	u := URL(base, value)
	if len(u) > maxImageAddress {
		return ""
	}
	// A network path such as //::/a.jpg parses but its absolute form does not.
	if _, err := url.Parse(u); err != nil {
		return ""
	}
	return u
}

// imageAddress reports whether a lazy-loading attribute's value can be an image
// address. It refuses flags, numbers, lists, directories and known media and
// document types, and accepts an http(s) or path-relative URL or a bare relative
// path to an image file. Any other extensionless address passes, as it cannot be
// told apart from a CDN or thumbnail address, which is why the attributes
// themselves are a fixed list.
func imageAddress(value string) bool {
	// Raw base64 JPEG data always starts /9j/, which looks like a path.
	if value == "" || len(value) > maxImageAddress || strings.HasPrefix(value, "/9j/") || strings.ContainsAny(value, "{}|<>\"'") || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return false
	}
	if len(joinedAddresses(value)) > 1 {
		return false
	}
	u, err := url.Parse(value)
	if err != nil {
		return false
	}
	// Some sites end a media URL with a slash, as in /get_file/clip.mp4/.
	file := path.Base(strings.TrimRight(u.Path, "/"))
	ext := path.Ext(file)
	switch strings.ToLower(ext) {
	case ".mp4", ".m4v", ".webm", ".mov", ".ogv", ".mkv", ".avi", ".wmv", ".flv", ".mpg", ".mpeg", ".ts", ".3gp", ".m3u8", ".mpd", ".m4s",
		".mp3", ".m4a", ".ogg", ".oga", ".opus", ".wav", ".flac", ".aac",
		".pdf", ".html", ".htm", ".js", ".mjs", ".css", ".json", ".xml", ".txt", ".zip":
		return false
	}
	web := u.Scheme == "http" || u.Scheme == "https"
	if !web && u.Scheme != "" {
		return false
	}
	if imageExtension(ext) {
		return file != ext // A bare extension names no file.
	}
	if file == "." || file == ".." || strings.HasSuffix(u.Path, "/") {
		return false
	}
	return web || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../")
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

// selectorFailure turns a recovered panic into the error a refresh or preview
// reports. The value stays out of that error and the log because it quotes the
// page: xpath's is strconv.ParseFloat: parsing "<attribute>", and attributes hold
// URLs. The selector stays out because it can hold one too. A stack prints
// function names and addresses, so with the value's type it still identifies the
// library bug.
func selectorFailure(selector string, v any) error {
	stack := debug.Stack()
	slog.Error("selector library panicked", "selector", selector, "panic_type", fmt.Sprintf("%T", v), "stack", string(stack[:min(len(stack), maxPanicStack)]))
	return fmt.Errorf("%w on the %s selector; check its comparisons and string functions against this page", ErrSelectorFailed, selector)
}

// RunAt uses one observation time for every relative date in a fetched page.
// A supplied reference also makes relative-date extraction deterministic.
func RunAt(body []byte, effective string, r model.Recipe, observedAt time.Time) (out model.Preview, err error) {
	out = model.Preview{Items: []model.Item{}, Warnings: []string{}}
	// Refreshes run on bare goroutines, where a panic stops the whole server, so
	// every selector this page evaluates stays inside this recover.
	selector := "Items"
	defer func() {
		if v := recover(); v != nil {
			out.Items = []model.Item{}
			err = fmt.Errorf("%w; saved history is retained", selectorFailure(selector, v))
		}
	}()
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
		selector = "Title"
		title, titleNode := field(n, r.Type, r.Title, "")
		selector = "Link"
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
		selector = "Content"
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
		selector = "Image"
		raw, im := field(n, r.Type, r.Image, "")
		if im != nil {
			if r.Image.Attr != "" || isAttribute(im) {
				it.Image = URL(base, raw)
			} else {
				it.Image = itemImageURL(im, base)
			}
		} else if r.Image.Selector == "" {
			if ns := selectNodes(n, "css", "img"); len(ns) > 0 {
				it.Image = itemImageURL(ns[0], base)
			}
		}
		if r.Image.Selector != "" && it.Image == "" {
			// Automatic image extraction reads URL-bearing attributes, not the
			// selected element's text. Report the first supplied candidate.
			imageField := r.Image
			if im != nil && imageField.Attr == "" && !isAttribute(im) {
				raw = ""
				for _, k := range imageAttributes {
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
		selector = "Date"
		raw, dateNode := field(n, r.Type, r.Date, "")
		// A selected element can display a relative age while retaining an exact
		// machine-readable datetime. Explicit attributes and XPath text/attribute
		// results keep the user's chosen value instead of inspecting another one.
		if r.Date.Attr == "" && dateNode != nil && dateNode.Type == html.ElementNode && !isAttribute(dateNode) {
			it.Published = parseAbsoluteDate(attr(dateNode, "datetime"), "", loc)
		}
		if it.Published.IsZero() && raw != "" {
			it.Published = parseDateText(raw, r.DateLayout, loc)
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
		if len(it.Title) > 2048 || len(it.HTML) > 256<<10 || len(it.URL) > 8192 || len(it.Image) > maxImageAddress {
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
