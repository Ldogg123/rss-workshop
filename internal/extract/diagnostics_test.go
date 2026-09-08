package extract

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"rss-workshop/internal/model"
)

func TestFieldWarningsExplainExpectedAndReceived(t *testing.T) {
	for _, typ := range []string{"css", "xpath"} {
		t.Run(typ, func(t *testing.T) {
			for _, tc := range []struct {
				name, field, selector, attr, markup string
				want                                []string
				skipped                             bool
			}{
				{name: "missing title match", field: "title", selector: "absent", skipped: true,
					want: []string{"skipped: empty title; Title expected non-empty text", "received nothing (selector matched no nodes)"}},
				{name: "empty title", field: "title", selector: "h3", markup: `<h3> </h3>`, skipped: true,
					want: []string{"skipped: empty title", "received nothing (selected value is empty)"}},
				{name: "title attribute missing", field: "title", selector: "h2", attr: "data-title", skipped: true,
					want: []string{"Title expected non-empty text", `has no "data-title" attribute`}},
				{name: "wrong link element", field: "link", selector: "h2", skipped: true,
					want: []string{"skipped: missing or unsafe item URL; Link expected an HTTP(S) URL", `received nothing (selected element has no "href" attribute)`}},
				{name: "empty link attribute", field: "link", selector: "h3", markup: `<h3 href="">Other</h3>`, skipped: true,
					want: []string{`received nothing (selected element's "href" attribute is empty)`}},
				{name: "unsafe link", field: "link", selector: "h3", markup: `<h3 href="javascript:alert(1)">Other</h3>`, skipped: true,
					want: []string{`Link expected an HTTP(S) URL but received URL "javascript:alert(1)"`}},
				{name: "date text mismatch", field: "date", selector: "h2",
					want: []string{`Date expected a date or relative age but received "A real title"`, "using first-seen time"}},
				{name: "date selected link", field: "date", selector: "a", attr: "href",
					want: []string{`Date expected a date or relative age but received "/news/story"`}},
				{name: "date selector missing", field: "date", selector: "time",
					want: []string{"Date expected a date or relative age but received nothing (selector matched no nodes)"}},
				{name: "date attribute missing", field: "date", selector: "time", attr: "data-age", markup: `<time datetime="2026-09-06">2 minutes ago</time>`,
					want: []string{`received nothing (selected element has no "data-age" attribute)`, "using first-seen time"}},
				{name: "date attribute empty", field: "date", selector: "time", attr: "data-age", markup: `<time data-age=" ">2 minutes ago</time>`,
					want: []string{`received nothing (selected element's "data-age" attribute is empty)`}},
				{name: "empty date value", field: "date", selector: "time", markup: `<time></time>`,
					want: []string{"received nothing (selected value is empty)"}},
				{name: "invalid machine date without text", field: "date", selector: "time", markup: `<time datetime="not a date"></time>`,
					want: []string{`Date expected a date or relative age but received "not a date"`}},
				{name: "missing content", field: "content", selector: "section",
					want: []string{"Content expected text or safe HTML but received nothing (selector matched no nodes); content omitted"}},
				{name: "missing content attribute", field: "content", selector: "h2", attr: "data-summary",
					want: []string{`Content expected text or safe HTML`, `has no "data-summary" attribute`, "content omitted"}},
				{name: "missing image", field: "image", selector: "img",
					want: []string{"Image expected an HTTP(S) image URL but received nothing (selector matched no nodes); image omitted"}},
				{name: "missing image source", field: "image", selector: "img", markup: `<img>`,
					want: []string{`Image expected an HTTP(S) image URL`, `has no "src" attribute`, "image omitted"}},
				{name: "unsafe image source", field: "image", selector: "img", markup: `<img src="javascript:alert(1)">`,
					want: []string{`Image expected an HTTP(S) image URL but received URL "javascript:alert(1)"; image omitted`}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := model.Recipe{Type: typ, Items: "article", Title: model.Field{Selector: "h2"}, Link: model.Field{Selector: "a"}}
					selected := model.Field{Selector: tc.selector, Attr: tc.attr}
					if typ == "xpath" {
						r.Items, r.Title.Selector, r.Link.Selector = "//article", ".//h2", ".//a"
						selected.Selector = ".//" + tc.selector
					}
					switch tc.field {
					case "title":
						r.Title = selected
					case "link":
						r.Link = selected
					case "date":
						r.Date = selected
					case "content":
						r.Content = selected
					case "image":
						r.Image = selected
					}
					body := `<article><h2>A real title</h2><a href="/news/story">Read story</a>` + tc.markup + `</article>`
					p, err := Run([]byte(body), "https://example.com", r)
					if tc.skipped {
						if !errors.Is(err, ErrNoItems) || len(p.Items) != 0 {
							t.Fatalf("required field mismatch did not reject item: %v %+v", err, p)
						}
					} else if err != nil || len(p.Items) != 1 || !p.Items[0].Published.IsZero() {
						t.Fatalf("optional field mismatch discarded or dated item: %v %+v", err, p)
					}
					if len(p.Warnings) != 1 {
						t.Fatalf("expected one field warning, got %v", p.Warnings)
					}
					for _, want := range tc.want {
						if !strings.Contains(p.Warnings[0], want) {
							t.Errorf("warning %q lacks %q", p.Warnings[0], want)
						}
					}
				})
			}
		})
	}
}

func TestXPathDateAttributeWarnings(t *testing.T) {
	r := model.Recipe{Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"}, Date: model.Field{Selector: ".//a/@href"}}
	p, err := Run([]byte(`<article><h2>Story</h2><a href="/wrong-field">Read</a></article>`), "https://example.com", r)
	if err != nil || len(p.Items) != 1 || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], `received "/wrong-field"`) {
		t.Fatal(p, err)
	}
	r.Date.Selector = ".//a/@datetime"
	p, err = Run([]byte(`<article><h2>Story</h2><a>Read</a></article>`), "https://example.com", r)
	if err != nil || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "received nothing (selector matched no nodes)") {
		t.Fatal(p, err)
	}
}

func TestFieldWarningSamplesAreBoundedAndSafe(t *testing.T) {
	r := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}, Date: model.Field{Selector: "time"}}
	for _, tc := range []struct {
		name, markup, want string
		absent             []string
	}{
		{name: "HTML shaped value", markup: `&lt;img src=x onerror=&quot;alert(1)&quot;&gt;`, want: `received "<img src=x onerror=\"alert(1)\">"`},
		{name: "bounded Unicode", markup: strings.Repeat("世", 400), want: "…", absent: []string{strings.Repeat("世", 121)}},
		{name: "flatten whitespace", markup: "invalid\n\tdate\r\nvalue", want: `received "invalid date value"`, absent: []string{"\n", "\r", "\t"}},
		{name: "absolute URL", markup: "https://name:password@example.com/story?token=private-secret", want: `received URL "[URL omitted]"`, absent: []string{"private-secret", "password", "example.com"}},
		{name: "short absolute URL", markup: "http://x", want: `received URL "[URL omitted]"`, absent: []string{"http://x"}},
		{name: "relative URL query", markup: "/story?token=private-secret", want: `received "/story"`, absent: []string{"private-secret", "token="}},
		{name: "protocol relative URL", markup: "//name:password@example.com/story?token=private-secret", want: `received URL "[URL omitted]"`, absent: []string{"private-secret", "password", "example.com"}},
		{name: "credential assignment", markup: "password=private-secret", want: "[redacted]", absent: []string{"private-secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Run([]byte(`<article><h2>Story</h2><time>`+tc.markup+`</time></article>`), "https://example.com", r)
			if err != nil || len(p.Items) != 1 || len(p.Warnings) != 1 {
				t.Fatal(p, err)
			}
			warning := p.Warnings[0]
			if !strings.Contains(warning, tc.want) || !utf8.ValidString(warning) || len(warning) > 512 {
				t.Fatalf("unexpected warning: %q", warning)
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(warning, unwanted) {
					t.Errorf("warning retained unwanted sample %q: %q", unwanted, warning)
				}
			}
		})
	}
}

func TestUnconfiguredOptionalFieldsDoNotWarn(t *testing.T) {
	r := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}
	p, err := Run([]byte(`<article><h2>Story</h2></article>`), "https://example.com", r)
	if err != nil || len(p.Items) != 1 || len(p.Warnings) != 0 {
		t.Fatal(p, err)
	}
}

func TestImageWarningDescribesSelectedNode(t *testing.T) {
	body := []byte(`<article><h2>Story</h2><section><img src="javascript:alert(1)"></section></article>`)
	for _, typ := range []string{"css", "xpath"} {
		t.Run(typ, func(t *testing.T) {
			r := model.Recipe{Type: typ, Items: "article", Title: model.Field{Selector: "h2"}, Image: model.Field{Selector: "section img"}}
			if typ == "xpath" {
				r.Items, r.Title.Selector, r.Image.Selector = "//article", ".//h2", ".//section//img"
			}
			p, err := Run(body, "https://example.com", r)
			if err != nil || len(p.Items) != 1 || p.Items[0].Image != "" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], `received URL "javascript:alert(1)"`) {
				t.Fatalf("nested selected image did not report its invalid source: %v %+v", err, p)
			}
			// A configured selector reads the selected element's attributes.
			// Its descendants are not substituted for that explicit selection.
			r.Image.Selector = "section"
			if typ == "xpath" {
				r.Image.Selector = ".//section"
			}
			p, err = Run(body, "https://example.com", r)
			if err != nil || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], `received nothing (selected element has no "src" attribute)`) || strings.Contains(p.Warnings[0], "javascript:") {
				t.Fatalf("wrapper diagnostic claimed an unselected descendant's value: %v %+v", err, p)
			}
		})
	}
}
