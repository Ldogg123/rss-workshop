package model

import "time"

type Field struct {
	Selector string `json:"selector"`
	Attr     string `json:"attr"`
}
type Recipe struct {
	Mode         string     `json:"mode"`
	WaitSelector string     `json:"wait_selector"`
	SettleMS     int        `json:"settle_ms"`
	Type         string     `json:"type"`
	Items        string     `json:"items"`
	Title        Field      `json:"title"`
	Link         Field      `json:"link"`
	Date         Field      `json:"date"`
	Content      Field      `json:"content"`
	Image        Field      `json:"image"`
	DateLayout   string     `json:"date_layout"`
	Timezone     string     `json:"timezone"`
	Filters      *FilterSet `json:"filters,omitempty"`
	// Optional per-item article fetching. Nil leaves a feed built from its
	// list page alone, which stays the default for every existing recipe.
	Full *FullContent `json:"full_content,omitempty"`
}

// FullContent follows each item's own link and extracts the article body from
// that page. Selector uses the recipe's existing CSS/XPath Type. Browser opts a
// single feed into its configured fetch mode for articles; the default is the
// plain guarded fetcher, which is cheap enough to run on every refresh.
type FullContent struct {
	Selector string `json:"selector"`
	Attr     string `json:"attr,omitempty"`
	Browser  bool   `json:"browser,omitempty"`
}
type Feed struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	URL          string    `json:"url"`
	Recipe       Recipe    `json:"recipe"`
	Interval     int       `json:"interval"` // seconds
	Enabled      bool      `json:"enabled"`
	NextRun      time.Time `json:"next_run"`
	LastAttempt  time.Time `json:"last_attempt"`
	LastSuccess  time.Time `json:"last_success"`
	Error        string    `json:"error"`
	Failures     int       `json:"failures"`
	ETag         string    `json:"-"`
	LastModified string    `json:"-"`
	Version      int       `json:"version"`
	Count        int       `json:"count"`
	RSSToken     string    `json:"-"`
	RSSURL       string    `json:"rss_url,omitempty"`
	AtomURL      string    `json:"atom_url,omitempty"`
}
type Item struct {
	Key   string `json:"key"`
	GUID  string `json:"guid"`
	Title string `json:"title"`
	URL   string `json:"url"`
	HTML  string `json:"html"`
	Image string `json:"image"`
	// FullHTML is the article body fetched from the item's own page. It is
	// stored separately from HTML so that re-extracting the list page each
	// refresh keeps updating the teaser and any late-published preview image
	// without discarding article content already retrieved.
	FullHTML  string    `json:"full_html,omitempty"`
	Published time.Time `json:"published"`
	// Preview-only provenance. The stored publication timestamp remains fixed
	// after first discovery; these fields are not part of the database record.
	PublishedEstimated bool      `json:"published_estimated,omitempty"`
	PublishedSource    string    `json:"published_source,omitempty"`
	FirstSeen          time.Time `json:"first_seen"`
	LastSeen           time.Time `json:"last_seen"`
}
type Preview struct {
	Matches        int             `json:"matches"`
	Valid          int             `json:"valid"`
	Filtered       int             `json:"filtered"`
	Items          []Item          `json:"items"`
	Warnings       []string        `json:"warnings"`
	FilterExamples []FilterExample `json:"filter_examples,omitempty"`
	Diagnostics    *RunDiagnostics `json:"diagnostics,omitempty"`
}
