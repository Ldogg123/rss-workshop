package extract

import (
	"testing"
	"time"

	"rss-workshop/internal/model"
)

func TestDateExtractionPrecedenceAndEstimates(t *testing.T) {
	reference := time.Date(2026, 9, 7, 14, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, typ, selector, attr, markup, layout, want, source string
		warning                                                 bool
	}{
		{name: "minutes", typ: "css", selector: "time", markup: `<time>2 minutes ago</time>`, want: "2026-09-07T14:28:00Z", source: "2 minutes ago"},
		{name: "calendar month", typ: "css", selector: "time", markup: `<time>1 month ago</time>`, want: "2026-08-07T14:30:00Z", source: "1 month ago"},
		{name: "automatic exact datetime", typ: "css", selector: "time", markup: `<time datetime="2026-09-06T09:10:00Z">2 minutes ago</time>`, want: "2026-09-06T09:10:00Z"},
		{name: "empty text exact datetime", typ: "css", selector: "time", markup: `<time datetime="2026-09-06"></time>`, want: "2026-09-06T00:00:00Z"},
		{name: "invalid datetime fallback", typ: "css", selector: "time", markup: `<time datetime="unknown">2 minutes ago</time>`, want: "2026-09-07T14:28:00Z", source: "2 minutes ago"},
		{name: "explicit attribute wins", typ: "css", selector: "time", attr: "data-age", markup: `<time datetime="2026-09-06" data-age="2 minutes ago">1 month ago</time>`, want: "2026-09-07T14:28:00Z", source: "2 minutes ago"},
		{name: "missing explicit attribute", typ: "css", selector: "time", attr: "data-age", markup: `<time datetime="2026-09-06">2 minutes ago</time>`},
		{name: "custom date layout", typ: "css", selector: "time", markup: `<time>06/09/2026</time>`, layout: "02/01/2006", want: "2026-09-06T00:00:00Z"},
		{name: "custom format prefers valid machine datetime", typ: "css", selector: "time", markup: `<time datetime="2026-09-05">06/09/2026</time>`, layout: "02/01/2006", want: "2026-09-05T00:00:00Z"},
		{name: "relative after custom layout", typ: "css", selector: "time", markup: `<time>2 minutes ago</time>`, layout: "02/01/2006", want: "2026-09-07T14:28:00Z", source: "2 minutes ago"},
		{name: "XPath element machine datetime", typ: "xpath", selector: ".//time", markup: `<time datetime="2026-09-06">2 minutes ago</time>`, want: "2026-09-06T00:00:00Z"},
		{name: "XPath text respects selection", typ: "xpath", selector: ".//time/text()", markup: `<time datetime="2026-09-06">2 minutes ago</time>`, want: "2026-09-07T14:28:00Z", source: "2 minutes ago"},
		{name: "XPath attribute respects selection", typ: "xpath", selector: ".//time/@data-age", markup: `<time datetime="2026-09-06" data-age="2 minutes ago">1 month ago</time>`, want: "2026-09-07T14:28:00Z", source: "2 minutes ago"},
		{name: "XPath exact attribute", typ: "xpath", selector: ".//time/@datetime", markup: `<time datetime="2026-09-06">2 minutes ago</time>`, want: "2026-09-06T00:00:00Z"},
		{name: "unknown date", typ: "css", selector: "time", markup: `<time>recently-ish</time>`, warning: true},
		{name: "unsupported future date", typ: "css", selector: "time", markup: `<time>in 2 minutes</time>`, warning: true},
		{name: "no date selector stays unset", typ: "css", markup: `<time datetime="2026-09-06">2 minutes ago</time>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := recipe(tc.typ)
			r.Items = "article"
			if tc.typ == "xpath" {
				r.Items = "//article"
			}
			r.Date = model.Field{Selector: tc.selector, Attr: tc.attr}
			r.DateLayout = tc.layout
			body := []byte(`<article><h2>Story</h2><a href="/story">Read</a>` + tc.markup + `</article>`)
			p, err := RunAt(body, "https://example.com", r, reference)
			if err != nil {
				t.Fatal(err)
			}
			it := p.Items[0]
			if tc.want == "" {
				if !it.Published.IsZero() {
					t.Fatalf("unexpected date: %v", it.Published)
				}
			} else if it.Published.Format(time.RFC3339) != tc.want {
				t.Fatalf("date=%s want=%s", it.Published, tc.want)
			}
			if it.PublishedEstimated != (tc.source != "") || it.PublishedSource != tc.source {
				t.Fatalf("incorrect estimate provenance: %+v", it)
			}
			if (len(p.Warnings) > 0) != tc.warning {
				t.Fatalf("warnings=%v", p.Warnings)
			}
		})
	}
}

func TestRelativeDatesShareOnePageReference(t *testing.T) {
	r := recipe("css")
	r.Items = "article"
	r.Link = model.Field{}
	r.Date = model.Field{Selector: "time"}
	body := `<article><h2>First</h2><time>just now</time></article><article><h2>Second</h2><time>0 minutes ago</time></article>`
	reference := time.Date(2026, 9, 7, 14, 30, 5, 123000000, time.UTC)
	p, err := RunAt([]byte(body), "https://example.com", r, reference)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 2 {
		t.Fatal(p)
	}
	for _, it := range p.Items {
		if !it.Published.Equal(reference) || !it.PublishedEstimated {
			t.Fatalf("not one observation time: %+v", it)
		}
	}
	if _, err := RunAt([]byte(body), "https://example.com", r, time.Time{}); err == nil {
		t.Fatal("zero reference accepted")
	}
}
