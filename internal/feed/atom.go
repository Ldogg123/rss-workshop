package feed

import (
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"rss-workshop/internal/model"
)

type atomDocument struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	ID      string      `xml:"id"`
	Title   atomText    `xml:"title"`
	Updated string      `xml:"updated"`
	Author  atomAuthor  `xml:"author"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomText struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type atomAuthor struct {
	Name string `xml:"name"`
	URI  string `xml:"uri"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     atomText   `xml:"title"`
	Links     []atomLink `xml:"link"`
	Published string     `xml:"published"`
	Updated   string     `xml:"updated"`
	// Atom has both fields, so a reader's list view can show the list-page
	// teaser while the entry itself carries the fetched article.
	Summary *atomText `xml:"summary,omitempty"`
	Content atomText  `xml:"content"`
}

// RenderAtom renders persisted items as Atom 1.0 and returns an ETag for the
// exact bytes. IDs do not depend on the public address or rotatable read token.
// Missing dates use persisted observation times, then the Unix epoch; rendering
// never reads the clock, so unchanged data produces identical bytes.
func RenderAtom(f model.Feed, items []model.Item, baseURL string) ([]byte, string, error) {
	if f.ID == "" || f.RSSToken == "" {
		return nil, "", fmt.Errorf("Atom requires a saved feed ID and read token")
	}
	base, err := atomHTTPURL(baseURL, nil)
	if err != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, "", fmt.Errorf("Atom requires an absolute HTTP(S) public base URL without query or fragment")
	}
	source, err := atomHTTPURL(f.URL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("Atom source URL: %w", err)
	}
	self := strings.TrimRight(base.String(), "/") + "/feeds/" + url.PathEscape(f.RSSToken) + ".atom"
	author := strings.TrimSpace(f.Title)
	if author == "" {
		author = source.Hostname()
	}
	doc := atomDocument{
		ID:     fmt.Sprintf("urn:sha256:%x", sha256.Sum256([]byte("rss-workshop:feed\x00"+f.ID))),
		Title:  atomText{Type: "text", Value: f.Title},
		Author: atomAuthor{Name: author, URI: source.String()},
		Links: []atomLink{
			{Rel: "alternate", Type: "text/html", Href: source.String()},
			{Rel: "self", Type: "application/atom+xml", Href: self},
		},
	}
	updated := atomDate(f.LastSuccess)
	for _, it := range items {
		id := it.GUID
		if id == "" && it.Key != "" {
			// Match the store's RSS GUID calculation when rendering a new item.
			id = fmt.Sprintf("urn:sha256:%x", sha256.Sum256([]byte(f.ID+"\x00"+it.Key)))
		}
		parsedID, err := url.Parse(id)
		if err != nil || !parsedID.IsAbs() || strings.ContainsFunc(id, unicode.IsSpace) {
			return nil, "", fmt.Errorf("Atom entry requires an absolute, stable GUID")
		}
		published := atomDate(it.Published, it.FirstSeen, it.LastSeen, f.LastSuccess)
		// The current item model records when an item was last observed, rather
		// than a separate content modification time. Keep that distinction here.
		entryUpdated := atomDate(it.LastSeen, it.FirstSeen, it.Published, f.LastSuccess)
		if published.After(entryUpdated) {
			entryUpdated = published
		}
		if entryUpdated.After(updated) {
			updated = entryUpdated
		}
		e := atomEntry{
			ID:        id,
			Title:     atomText{Type: "text", Value: it.Title},
			Published: published.Format(time.RFC3339Nano),
			Updated:   entryUpdated.Format(time.RFC3339Nano),
			Content:   atomText{Type: "html", Value: itemContent(it)},
		}
		if summary := itemSummary(it); summary != "" {
			e.Summary = &atomText{Type: "html", Value: summary}
		}
		if it.URL != "" {
			link, err := atomHTTPURL(it.URL, source)
			if err != nil {
				return nil, "", fmt.Errorf("Atom entry URL: %w", err)
			}
			e.Links = []atomLink{{Rel: "alternate", Type: "text/html", Href: link.String()}}
		}
		// The extracted image, offered where a reader looks for a thumbnail
		// rather than only inline in the body. The image is never fetched, so
		// its length is unknown and deliberately omitted.
		if it.Image != "" {
			image, err := atomHTTPURL(it.Image, source)
			if err == nil {
				e.Links = append(e.Links, atomLink{Rel: "enclosure", Type: imageType(it.Image), Href: image.String()})
			}
		}
		doc.Entries = append(doc.Entries, e)
	}
	doc.Updated = updated.Format(time.RFC3339Nano)
	b, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, "", err
	}
	b = append([]byte(xml.Header), b...)
	return b, fmt.Sprintf("\"%x\"", sha256.Sum256(b)), nil
}

func atomDate(candidates ...time.Time) time.Time {
	for _, candidate := range candidates {
		utc := candidate.UTC()
		if !candidate.IsZero() && utc.Year() >= 1 && utc.Year() <= 9999 {
			return utc
		}
	}
	return time.Unix(0, 0).UTC()
}

func atomHTTPURL(raw string, base *url.URL) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL")
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.ContainsFunc(raw, unicode.IsSpace) {
		return nil, fmt.Errorf("expected an absolute HTTP(S) URL without credentials")
	}
	return u, nil
}
