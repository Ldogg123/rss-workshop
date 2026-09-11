package feed

import (
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"rss-workshop/internal/model"
	"time"
)

type rss struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel channel  `xml:"channel"`
}
type channel struct {
	Title       string  `xml:"title"`
	Link        string  `xml:"link"`
	Description string  `xml:"description"`
	Items       []entry `xml:"item"`
}
type entry struct {
	Title       string `xml:"title"`
	Link        string `xml:"link,omitempty"`
	GUID        guid   `xml:"guid"`
	Description string `xml:"description"`
	Published   string `xml:"pubDate"`
}
type guid struct {
	Permalink string `xml:"isPermaLink,attr"`
	Value     string `xml:",chardata"`
}

func Render(f model.Feed, items []model.Item) ([]byte, string, error) {
	r := rss{Version: "2.0", Channel: channel{Title: f.Title, Link: f.URL, Description: "Updates from " + f.Title}}
	for _, it := range items {
		r.Channel.Items = append(r.Channel.Items, entry{Title: it.Title, Link: it.URL, GUID: guid{Permalink: "false", Value: it.GUID}, Description: itemHTML(it), Published: it.Published.UTC().Format(time.RFC1123Z)})
	}
	b, e := xml.MarshalIndent(r, "", "  ")
	if e != nil {
		return nil, "", e
	}
	b = append([]byte(xml.Header), b...)
	return b, fmt.Sprintf("\"%x\"", sha256.Sum256(b)), nil
}
