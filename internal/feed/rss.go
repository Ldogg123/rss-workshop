package feed

import (
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"time"

	"rss-workshop/internal/model"
)

type rss struct {
	XMLName   xml.Name `xml:"rss"`
	Version   string   `xml:"version,attr"`
	ContentNS string   `xml:"xmlns:content,attr"`
	AtomNS    string   `xml:"xmlns:atom,attr"`
	MediaNS   string   `xml:"xmlns:media,attr"`
	Channel   channel  `xml:"channel"`
}
type channel struct {
	Title string `xml:"title"`
	Link  string `xml:"link"`
	// A reader that rediscovers this feed follows the self link, and one
	// deciding how often to poll reads ttl. Both come from stored values, never
	// from the clock, so unchanged data keeps producing identical bytes.
	SelfLink    *atomSelf `xml:"atom:link,omitempty"`
	Description string    `xml:"description"`
	Generator   string    `xml:"generator,omitempty"`
	TTL         int       `xml:"ttl,omitempty"`
	Updated     string    `xml:"lastBuildDate,omitempty"`
	Items       []entry   `xml:"item"`
}
type atomSelf struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}
type entry struct {
	Title string `xml:"title"`
	Link  string `xml:"link,omitempty"`
	GUID  guid   `xml:"guid"`
	// description is the short form a reader shows in a list; content:encoded
	// carries the full body. Without full article content the two would be the
	// same text, so only description is emitted.
	Description string    `xml:"description"`
	Content     string    `xml:"content:encoded,omitempty"`
	Media       *rssMedia `xml:"media:content,omitempty"`
	Published   string    `xml:"pubDate"`
}
type rssMedia struct {
	URL    string `xml:"url,attr"`
	Type   string `xml:"type,attr"`
	Medium string `xml:"medium,attr"`
}
type guid struct {
	Permalink string `xml:"isPermaLink,attr"`
	Value     string `xml:",chardata"`
}

// Render renders persisted items as RSS 2.0 and returns an ETag for the exact
// bytes. Rendering never reads the clock, so unchanged data produces identical
// bytes and a polling reader keeps getting 304.
//
// selfURL is the feed's own public address, advertised so a reader can
// rediscover it; an empty value omits the link rather than guessing one.
func Render(f model.Feed, items []model.Item, selfURL, generator string) ([]byte, string, error) {
	c := channel{Title: f.Title, Link: f.URL, Description: "Updates from " + f.Title, Generator: generator}
	if selfURL != "" {
		c.SelfLink = &atomSelf{Href: selfURL, Rel: "self", Type: "application/rss+xml"}
	}
	// Interval is seconds; ttl is minutes, and a reader treats 0 as unset.
	if minutes := f.Interval / 60; minutes > 0 {
		c.TTL = minutes
	}
	if !f.LastSuccess.IsZero() {
		c.Updated = f.LastSuccess.UTC().Format(time.RFC1123Z)
	}
	for _, it := range items {
		e := entry{
			Title:       it.Title,
			Link:        it.URL,
			GUID:        guid{Permalink: "false", Value: it.GUID},
			Description: itemContent(it),
			Published:   it.Published.UTC().Format(time.RFC1123Z),
		}
		// The full article stays in description so no reader loses content it
		// already receives; content:encoded repeats it for readers that prefer
		// the richer field. Without a fetched body the two would be identical,
		// so it is omitted rather than doubling every feed's size.
		if itemSummary(it) != "" {
			e.Content = e.Description
		}
		if it.Image != "" {
			e.Media = &rssMedia{URL: it.Image, Type: imageType(it.Image), Medium: "image"}
		}
		c.Items = append(c.Items, e)
	}
	r := rss{
		Version:   "2.0",
		ContentNS: "http://purl.org/rss/1.0/modules/content/",
		AtomNS:    "http://www.w3.org/2005/Atom",
		MediaNS:   "http://search.yahoo.com/mrss/",
		Channel:   c,
	}
	b, e := xml.MarshalIndent(r, "", "  ")
	if e != nil {
		return nil, "", e
	}
	b = append([]byte(xml.Header), b...)
	return b, fmt.Sprintf("\"%x\"", sha256.Sum256(b)), nil
}
