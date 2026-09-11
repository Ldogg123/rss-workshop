package feed

import (
	"encoding/xml"
	"time"

	"rss-workshop/internal/model"
)

// OPML 2.0 subscription lists let a reader subscribe to every generated feed
// at once. Readers identify feeds by xmlUrl, so these documents carry the
// reader links themselves and are only ever served to an authenticated admin.
type opml struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    opmlHead `xml:"head"`
	Body    opmlBody `xml:"body"`
}
type opmlHead struct {
	Title       string `xml:"title"`
	DateCreated string `xml:"dateCreated"`
}
type opmlBody struct {
	Outlines []outline `xml:"outline"`
}

// text is the only attribute OPML requires; readers disagree about which of
// text and title they display, so both carry the feed name. htmlUrl points at
// the source page, which lets a reader link back to the site it was built from.
type outline struct {
	Type    string `xml:"type,attr"`
	Text    string `xml:"text,attr"`
	Title   string `xml:"title,attr"`
	XMLURL  string `xml:"xmlUrl,attr"`
	HTMLURL string `xml:"htmlUrl,attr,omitempty"`
}

// RenderOPML lists every feed for a reader to import. Callers fill RSSURL and
// AtomURL; atom selects which of the two each outline advertises. Paused feeds
// are included because their links keep serving the stories already saved.
func RenderOPML(title string, feeds []model.Feed, atom bool, created time.Time) ([]byte, error) {
	doc := opml{Version: "2.0", Head: opmlHead{Title: title, DateCreated: created.UTC().Format(time.RFC1123Z)}}
	doc.Body.Outlines = make([]outline, 0, len(feeds))
	for _, f := range feeds {
		link := f.RSSURL
		if atom {
			link = f.AtomURL
		}
		if link == "" {
			continue
		}
		name := f.Title
		if name == "" {
			name = f.URL
		}
		// "rss" is what OPML uses for any syndication feed, Atom included.
		doc.Body.Outlines = append(doc.Body.Outlines, outline{Type: "rss", Text: name, Title: name, XMLURL: link, HTMLURL: f.URL})
	}
	b, e := xml.MarshalIndent(doc, "", "  ")
	if e != nil {
		return nil, e
	}
	return append([]byte(xml.Header), append(b, '\n')...), nil
}
