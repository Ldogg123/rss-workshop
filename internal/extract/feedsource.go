package extract

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"rss-workshop/internal/model"
)

// FeedType is the recipe type of a feed source: an existing RSS, RDF or Atom
// feed read in place of a web page's cards. It takes no item or field
// selectors, is fetched as Static HTTP only, and its article selector is CSS.
const FeedType = "feed"

const (
	nsAtom    = "http://www.w3.org/2005/Atom"
	nsContent = "http://purl.org/rss/1.0/modules/content/"
	nsDC      = "http://purl.org/dc/elements/1.1/"
	nsITunes  = "http://www.itunes.com/dtds/podcast-1.0.dtd"
	nsMedia   = "http://search.yahoo.com/mrss/"
	nsRDF     = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	nsXML     = "http://www.w3.org/XML/1998/namespace"

	// MaxFeedEntries is how many entries a feed source reads, from the start
	// of the document, where feeds list their newest. Unlike a card selector
	// there is nothing to narrow, so a longer feed is cut rather than refused.
	MaxFeedEntries = 1000
	// derivedTitleRunes bounds a title made from an untitled entry's text, as
	// Mastodon and microblog feeds publish them.
	derivedTitleRunes = 100
)

var errNotFeed = errors.New("source is not an RSS or Atom feed; choose Web page as the source to select stories from an HTML page")

// malformed reports a document encoding/xml could not read by its line alone.
// Its errors quote element names, namespace URIs and raw bytes from the
// document, and a refresh failure reaches the log, the card, Diagnostics and
// notifications, none of which may carry source text.
func malformed(err error) error {
	var syntax *xml.SyntaxError
	if errors.As(err, &syntax) {
		return fmt.Errorf("feed is not well-formed XML (line %d); saved history is retained", syntax.Line)
	}
	return errors.New("feed is not well-formed XML; saved history is retained")
}

// feedLeaf is an element inside an entry's child, such as media:group's
// media:thumbnail. Anything deeper is skipped, so decoding an entry keeps no
// more than a few copies of its own text.
type feedLeaf struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Text    string     `xml:",chardata"`
}

type feedChild struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Text    string     `xml:",chardata"`
	Inner   string     `xml:",innerxml"`
	Kids    []feedLeaf `xml:",any"`
}

type feedEntry struct {
	XMLName  xml.Name
	Attrs    []xml.Attr  `xml:",any,attr"`
	Children []feedChild `xml:",any"`
}

// feedDocument is what readFeed keeps of a document.
type feedDocument struct {
	atom      bool
	base      *url.URL
	entries   []feedEntry
	truncated bool
}

// xmlChar reports whether XML 1.0 allows r in a document.
func xmlChar(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r <= 0xd7ff || r >= 0xe000 && r <= 0xfffd || r >= 0x10000 && r <= utf8.MaxRune
}

// xmlText prepares a feed for encoding/xml, which refuses a whole document
// for one character XML does not allow. Feeds carry them often: a control
// character pasted into a title, or bytes that are not UTF-8. Invalid UTF-8
// becomes U+FFFD, and forbidden characters, written raw or as numeric
// references, are removed. Fetch has already decoded the body to UTF-8.
func xmlText(body []byte) []byte {
	body = bytes.ToValidUTF8(body, []byte("\uFFFD"))
	out := make([]byte, 0, len(body))
	for i := 0; i < len(body); {
		if body[i] == '&' && i+2 < len(body) && body[i+1] == '#' {
			if end := bytes.IndexByte(body[i:min(len(body), i+12)], ';'); end > 2 {
				digits, base := string(body[i+2:i+end]), 10
				if digits[0] == 'x' || digits[0] == 'X' {
					digits, base = digits[1:], 16
				}
				if n, err := strconv.ParseUint(digits, base, 32); err == nil && !xmlChar(rune(n)) {
					i += end + 1
					continue
				}
			}
		}
		r, size := utf8.DecodeRune(body[i:])
		if xmlChar(r) {
			out = append(out, body[i:i+size]...)
		}
		i += size
	}
	return out
}

// readFeed streams a feed and decodes each item or entry on its own, up to
// MaxFeedEntries. Fetch owns decoding, so a declared encoding names what the
// document was, not what it is now, and is ignored. Strict is off for the
// unescaped ampersands and HTML entities feeds publish, but a document that
// still cannot be read fails whole: encoding/xml cannot resume past an error.
// encoding/xml expands no DTD or external entity. AutoClose stays off, because
// HTMLAutoClose treats an RSS <link> as an empty HTML element.
func readFeed(body []byte, base *url.URL) (feedDocument, error) {
	doc := feedDocument{base: base}
	d := xml.NewDecoder(bytes.NewReader(xmlText(body)))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	root, parent, depth := "", "", 0
read:
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if root == "" && !bytes.HasPrefix(bytes.TrimLeft(body, "\ufeff \t\r\n"), []byte("<?xml")) {
				return doc, errNotFeed
			}
			return doc, malformed(err)
		}
		switch v := tok.(type) {
		case xml.StartElement:
			depth++
			switch depth {
			case 1:
				if root != "" {
					// An element after the root, such as a notice or a script
					// a server printed after the feed, or a second document,
					// is not part of it and must not change how it is read.
					break read
				}
				switch {
				case v.Name.Local == "rss":
				case v.Name.Local == "RDF" && (v.Name.Space == nsRDF || v.Name.Space == "rdf"):
				case v.Name.Local == "feed":
					doc.atom = true
				default:
					return doc, errNotFeed
				}
				root = v.Name.Local
				doc.base = withBase(doc.base, v.Attr)
				continue
			case 2:
				parent = v.Name.Local
				if root == "rss" && parent == "channel" {
					doc.base = withBase(doc.base, v.Attr)
				}
			}
			entry := root == "rss" && v.Name.Local == "item" && (depth == 2 || depth == 3 && parent == "channel") ||
				root == "RDF" && v.Name.Local == "item" && depth == 2 ||
				root == "feed" && v.Name.Local == "entry" && depth == 2
			if !entry {
				continue
			}
			if len(doc.entries) == MaxFeedEntries {
				doc.truncated = true
				return doc, nil
			}
			var e feedEntry
			if err := d.DecodeElement(&e, &v); err != nil {
				return doc, malformed(err)
			}
			depth--
			doc.entries = append(doc.entries, e)
		case xml.EndElement:
			// The root closing does not end reading: without Strict, a stray
			// end tag inside the feed closes every open element, the root
			// included, and only the next token reports it.
			depth--
		}
	}
	if root == "" {
		return doc, errNotFeed
	}
	return doc, nil
}

func xmlAttr(attrs []xml.Attr, local string, spaces ...string) string {
	for _, a := range attrs {
		if a.Name.Local != local {
			continue
		}
		if len(spaces) == 0 && a.Name.Space == "" {
			return a.Value
		}
		for _, space := range spaces {
			if a.Name.Space == space {
				return a.Value
			}
		}
	}
	return ""
}

// in reports whether n is the extension element local of namespace ns, also
// accepting the namespace without its trailing slash, and the usual prefix
// when a feed uses it without declaring it, as many do.
func in(n xml.Name, ns, prefix, local string) bool {
	return n.Local == local && (n.Space == ns || n.Space == strings.TrimSuffix(ns, "/") || n.Space == prefix)
}

// withBase applies an element's xml:base to base.
func withBase(base *url.URL, attrs []xml.Attr) *url.URL {
	if u := URL(base, xmlAttr(attrs, "base", nsXML)); u != "" {
		if parsed, err := url.Parse(u); err == nil {
			return parsed
		}
	}
	return base
}

// elementHTML is the markup an RSS element carries: its text, which a feed
// escapes or wraps in CDATA, or its raw inner XML when it holds elements, as a
// description written without escaping does.
func elementHTML(c feedChild) string {
	if len(c.Kids) > 0 {
		return c.Inner
	}
	return c.Text
}

// atomHTML is an Atom text construct or content element as markup, following
// its type: text is escaped, html is markup already, and xhtml is the inner
// XML. Content of any other type, often base64, is not a description.
func atomHTML(c feedChild) string {
	kind := strings.ToLower(strings.TrimSpace(xmlAttr(c.Attrs, "type")))
	switch {
	case kind == "xhtml" || strings.HasSuffix(kind, "xhtml+xml") || len(c.Kids) > 0:
		return c.Inner
	case strings.EqualFold(xmlAttr(c.Attrs, "mode"), "base64"):
		return ""
	case kind == "html" || kind == "text/html":
		return c.Text
	case kind == "" || kind == "text" || kind == "text/plain":
		return html.EscapeString(c.Text)
	}
	return ""
}

// blockElements end a run of text, so their text does not run into the next.
var blockElements = map[string]bool{"address": true, "article": true, "aside": true, "blockquote": true, "br": true, "dd": true, "div": true, "dl": true, "dt": true, "figcaption": true, "figure": true, "footer": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "header": true, "hr": true, "li": true, "ol": true, "p": true, "pre": true, "section": true, "table": true, "td": true, "th": true, "tr": true, "ul": true}

// visibleText is markup's readable text on one line, as a title shows it.
func visibleText(markup string) string {
	doc, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "head" || n.Data == "script" || n.Data == "style" || n.Data == "template") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		block := n.Type == html.ElementNode && blockElements[n.Data]
		if block {
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if block {
			b.WriteByte(' ')
		}
	}
	walk(doc)
	return strings.Join(strings.Fields(b.String()), " ")
}

// titleTags are the inline elements an RSS title is read as markup for.
var titleTags = map[string]bool{"a": true, "abbr": true, "b": true, "br": true, "cite": true, "code": true, "em": true, "i": true, "kbd": true, "mark": true, "q": true, "s": true, "small": true, "span": true, "strong": true, "sub": true, "sup": true, "u": true}

// rssTitle is an RSS or RDF title as text. RSS defines no type for it: some
// feeds escape markup into it, and others mean a less-than sign literally, as
// in std::vector<bool> or "the <details> element", which reading it as HTML
// would drop. It is markup only when written unescaped, or when it closes a
// tag and every tag is a common inline element; otherwise a '<' is text.
// Character references are decoded either way.
func rssTitle(c feedChild) string {
	if len(c.Kids) > 0 {
		return visibleText(c.Inner)
	}
	z := html.NewTokenizer(strings.NewReader(c.Text))
	closed, inline := false, true
	for tt := z.Next(); tt != html.ErrorToken && inline; tt = z.Next() {
		switch tt {
		case html.EndTagToken, html.SelfClosingTagToken:
			closed = true
			fallthrough
		case html.StartTagToken:
			name, _ := z.TagName()
			inline = titleTags[string(name)]
		}
	}
	if closed && inline {
		return visibleText(c.Text)
	}
	return visibleText(strings.ReplaceAll(c.Text, "<", "&lt;"))
}

// feedFields are an entry's values before they become an item.
type feedFields struct {
	title, link, id string
	linkFromID      bool
	// dates holds the publication, dc:date and update candidates, in the
	// order they are tried.
	dates [3]string
	// content and summary are markup; media is a description in plain text,
	// already escaped, used only when neither is present.
	content, summary, media string
	// images are explicit image addresses, in document order.
	images []string
}

// explicitImage returns the address of a media or enclosure element that
// declares itself an image, or "".
func explicitImage(n xml.Name, attrs []xml.Attr) string {
	image := strings.HasPrefix(strings.ToLower(xmlAttr(attrs, "type")), "image/")
	switch {
	case in(n, nsMedia, "media", "thumbnail"):
		return xmlAttr(attrs, "url")
	case in(n, nsMedia, "media", "content") && (image || strings.EqualFold(xmlAttr(attrs, "medium"), "image")):
		return xmlAttr(attrs, "url")
	case in(n, nsITunes, "itunes", "image"):
		return xmlAttr(attrs, "href")
	}
	return ""
}

func readEntry(e feedEntry, atom bool, base *url.URL) (feedFields, *url.URL) {
	var f feedFields
	base = withBase(base, e.Attrs)
	core := e.XMLName.Space
	permalink := true
	if !atom {
		f.id = strings.TrimSpace(xmlAttr(e.Attrs, "about", nsRDF, "rdf"))
	}
	set := func(slot *string, value string) {
		if *slot == "" {
			*slot = value
		}
	}
	for _, c := range e.Children {
		n := c.XMLName
		own := n.Space == core
		switch {
		case own && n.Local == "title":
			if !atom {
				set(&f.title, rssTitle(c))
			} else if kind := xmlAttr(c.Attrs, "type"); (kind == "" || kind == "text") && len(c.Kids) == 0 {
				set(&f.title, strings.Join(strings.Fields(c.Text), " "))
			} else {
				set(&f.title, visibleText(atomHTML(c)))
			}
		case own && n.Local == "link" && !atom:
			set(&f.link, strings.TrimSpace(c.Text))
		case own && n.Local == "link":
			switch rel := strings.TrimSpace(xmlAttr(c.Attrs, "rel")); rel {
			case "", "alternate", "http://www.iana.org/assignments/relation/alternate":
				set(&f.link, strings.TrimSpace(xmlAttr(c.Attrs, "href")))
			case "enclosure", "http://www.iana.org/assignments/relation/enclosure":
				if strings.HasPrefix(strings.ToLower(xmlAttr(c.Attrs, "type")), "image/") {
					f.images = append(f.images, xmlAttr(c.Attrs, "href"))
				}
			}
		case own && n.Local == "guid" && !atom:
			f.id = strings.TrimSpace(c.Text)
			permalink = !strings.EqualFold(strings.TrimSpace(xmlAttr(c.Attrs, "isPermaLink")), "false")
		case own && n.Local == "id" && atom:
			set(&f.id, strings.TrimSpace(c.Text))
		case own && (n.Local == "pubDate" || n.Local == "published" || n.Local == "issued"):
			set(&f.dates[0], c.Text)
		case in(n, nsDC, "dc", "date"):
			set(&f.dates[1], c.Text)
		case own && atom && (n.Local == "updated" || n.Local == "modified"):
			set(&f.dates[2], c.Text)
		case in(n, nsContent, "content", "encoded"):
			set(&f.content, elementHTML(c))
		case own && atom && n.Local == "content" && xmlAttr(c.Attrs, "src") == "":
			set(&f.content, atomHTML(c))
		case own && !atom && n.Local == "description":
			set(&f.summary, elementHTML(c))
		case own && atom && n.Local == "summary":
			set(&f.summary, atomHTML(c))
		case own && !atom && n.Local == "enclosure":
			if strings.HasPrefix(strings.ToLower(xmlAttr(c.Attrs, "type")), "image/") {
				f.images = append(f.images, xmlAttr(c.Attrs, "url"))
			}
		case in(n, nsMedia, "media", "group"):
			for _, k := range c.Kids {
				if image := explicitImage(k.XMLName, k.Attrs); image != "" {
					f.images = append(f.images, image)
				} else if in(k.XMLName, nsMedia, "media", "description") {
					set(&f.media, mediaDescription(k.Attrs, k.Text))
				}
			}
		case in(n, nsMedia, "media", "description"):
			set(&f.media, mediaDescription(c.Attrs, c.Text))
		default:
			if image := explicitImage(n, c.Attrs); image != "" {
				f.images = append(f.images, image)
			}
		}
	}
	if f.link == "" && (atom || permalink) && (strings.HasPrefix(f.id, "http://") || strings.HasPrefix(f.id, "https://")) {
		f.link, f.linkFromID = f.id, true
	}
	return f, base
}

func mediaDescription(attrs []xml.Attr, text string) string {
	if strings.EqualFold(xmlAttr(attrs, "type"), "html") {
		return text
	}
	return html.EscapeString(text)
}

// feedBody parses an entry's markup and returns its body element, ready for
// content() and image detection.
func feedBody(markup string) *html.Node {
	doc, _ := html.Parse(strings.NewReader(markup))
	if nodes := selectNodes(doc, "css", "body"); len(nodes) > 0 {
		return nodes[0]
	}
	return doc
}

// pixel reports whether an image declares itself 0 or 1 pixel wide or high,
// as tracking images do. It is not a story's image.
func pixel(n *html.Node) bool {
	for _, k := range []string{"width", "height"} {
		switch strings.TrimSuffix(strings.TrimSpace(attr(n, k)), "px") {
		case "0", "1":
			return true
		}
	}
	return false
}

// shows reports whether a sanitized description displays anything: text, or
// an image that is not 0 or 1 pixel. Sizes are read from the entry's own
// markup in node, because sanitizing drops a width such as 1px.
func shows(node *html.Node, sanitized string) bool {
	if visibleText(sanitized) != "" {
		return true
	}
	if !strings.Contains(sanitized, "<img") {
		return false
	}
	for _, img := range selectNodes(node, "css", "img") {
		if !pixel(img) {
			return true
		}
	}
	return false
}

// describe chooses an entry's description among its candidates, in order. A
// full post can be too long to publish where its summary is not, and a post
// that is only an embed, such as a video, sanitizes to nothing where its
// summary has text. A later candidate replaces an earlier one only when it
// fits where that did not, or shows something where that showed nothing. The
// rank is 2 for one that fits and shows something, 1 for one that only fits,
// 0 for one too long, and -1 when there is none. prepare, when not nil, is
// given each candidate before it is sanitized and judged.
func describe(candidates []string, base *url.URL, prepare func(*html.Node) *html.Node) (description *html.Node, markup string, kept int) {
	kept = -1
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		node := feedBody(candidate)
		if prepare != nil {
			node = prepare(node)
		}
		sanitized := content(node, base)
		rank := 0
		if len(sanitized) <= MaxArticleBytes {
			rank = 1
			if shows(node, sanitized) {
				rank = 2
			}
		}
		if rank > kept {
			description, markup, kept = node, sanitized, rank
		}
		if kept == 2 {
			break
		}
	}
	return description, markup, kept
}

// runFeed maps a feed's entries to items for add, which applies the limits
// and filters a web page's cards get, so a preview and a refresh agree. While
// a cleanup rule runs, label names it, as RunAt's recover reports it.
func runFeed(body []byte, base *url.URL, r model.Recipe, loc *time.Location, out *model.Preview, add func(model.Item) error, label *string) error {
	doc, err := readFeed(body, base)
	if err != nil {
		return err
	}
	out.Matches = len(doc.entries)
	if doc.truncated {
		out.Warnings = append(out.Warnings, fmt.Sprintf("Only the first %d entries were read", MaxFeedEntries))
	}
	if len(doc.entries) == 0 {
		// A feed with nothing in it is not broken, so history is kept without
		// a failure or backoff, and the library marks the feed Quiet if it
		// stays empty.
		out.Warnings = append(out.Warnings, "The feed lists no entries")
		return nil
	}
	seen := map[string]bool{}
	for i, e := range doc.entries {
		f, entryBase := readEntry(e, doc.atom, doc.base)
		it := model.Item{URL: URL(entryBase, f.link)}
		if f.link != "" && it.URL == "" && !f.linkFromID {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Entry %d skipped: its link is not an HTTP(S) URL", i+1))
			continue
		}
		candidates := []string{f.content, f.summary, f.media}
		description, text, kept := describe(candidates, entryBase, nil)
		it.HTML = text
		if len(r.Remove) > 0 {
			// Cleanup rules apply to each candidate before it is judged, so
			// one they leave with nothing to show gives way to the next, as
			// an embed-only post does. The image below is looked for in what
			// they leave.
			var cleaned int
			description, it.HTML, cleaned = describe(candidates, entryBase, func(n *html.Node) *html.Node { return clean(n, r, label) })
			*label = "Description"
			if kept == 2 && cleaned < 2 {
				// As on a web page, a description the rules left with
				// nothing to show is dropped, and the warning quotes none of
				// the text they removed.
				it.HTML = ""
				out.Warnings = append(out.Warnings, fmt.Sprintf("Entry %d: cleanup removed the whole description; content omitted", i+1))
			}
		}
		// An untitled entry is titled from its text as the feed has it, so
		// cleanup rules never change a story's title or identity, nor leave
		// an entry without a title to skip.
		it.Title = f.title
		if it.Title == "" {
			it.Title = visibleText(text)
			if utf8.RuneCountInString(it.Title) > derivedTitleRunes {
				it.Title = string([]rune(it.Title)[:derivedTitleRunes-1]) + "…"
			}
		}
		if it.Title == "" {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Entry %d skipped: no title or description text", i+1))
			continue
		}
		switch {
		case f.id != "":
			it.Key = fmt.Sprintf("guid:%x", sha256.Sum256([]byte(f.id)))
		case it.URL != "":
			it.Key = it.URL
		default:
			it.Key = fmt.Sprintf("title:%x", sha256.Sum256([]byte(it.Title)))
		}
		if seen[it.Key] {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Entry %d skipped: it repeats an earlier entry's identity", i+1))
			continue
		}
		// Images the entry declares come first: the first image in a post is
		// as often a tracking pixel or an avatar, and a feed source has no
		// image selector to correct the choice.
		for _, raw := range f.images {
			if u := URL(entryBase, raw); u != "" && len(u) <= maxImageAddress {
				// A network path such as //::/a.jpg resolves to an address
				// that does not parse again; see lazyImageURL.
				if _, err := url.Parse(u); err == nil {
					it.Image = u
					break
				}
			}
		}
		if it.Image == "" && description != nil {
			for _, img := range selectNodes(description, "css", "img") {
				if !pixel(img) {
					if u := itemImageURL(img, entryBase); len(u) <= maxImageAddress {
						it.Image = u
					}
					break
				}
			}
		}
		if it.Image != "" && !showsImage(it.HTML, it.Image) {
			it.HTML = "<p><img src=\"" + html.EscapeString(it.Image) + "\" alt=\"\"></p>" + it.HTML
		}
		first := ""
		for _, raw := range f.dates {
			if raw = strings.TrimSpace(raw); raw == "" {
				continue
			}
			if first == "" {
				first = raw
			}
			if it.Published = parseDateText(raw, r.DateLayout, loc); !it.Published.IsZero() {
				break
			}
		}
		if first != "" && it.Published.IsZero() {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Entry %d: date not recognized, received %s; using first-seen time", i+1, receivedSample(first)))
		}
		// A card selector can be narrowed to fit; an entry cannot, so one too
		// large to store is left out rather than failing the feed.
		if len(it.Title) > 2048 || len(it.HTML) > MaxArticleBytes || len(it.URL) > 8192 {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Entry %d skipped: it exceeds the size limits for a story", i+1))
			continue
		}
		seen[it.Key] = true
		if err := add(it); err != nil {
			return err
		}
	}
	if out.Valid == 0 {
		return fmt.Errorf("%w: the feed's %d entries gave no valid stories; saved history is retained", ErrNoItems, out.Matches)
	}
	return nil
}
