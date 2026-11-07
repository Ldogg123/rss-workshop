package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Every static request asks for HTML exactly as it always has, so a source
// that negotiates content keeps sending existing feeds the same page. Only a
// feed source's list request, marked with AsFeedSource, asks for a feed.
func TestAcceptHeaders(t *testing.T) {
	accepts := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accepts <- strings.Join(r.Header.Values("Accept"), "|")
	}))
	defer srv.Close()
	c, err := New(time.Second, "127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithHeaders(context.Background(), Headers{UserAgent: "Reader/1", AcceptLanguage: "en"})
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"page", ctx, "text/html, application/xhtml+xml"},
		{"feed source", AsFeedSource(ctx), "application/rss+xml, application/atom+xml, application/rdf+xml;q=0.9, application/xml;q=0.8, text/xml;q=0.8, */*;q=0.1"},
	} {
		if _, err := c.Fetch(tc.ctx, srv.URL, "", ""); err != nil {
			t.Fatal(err)
		}
		if got := <-accepts; got != tc.want {
			t.Errorf("%s: Accept %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A feed names its encoding in its XML declaration, which RFC 7303 has a
// recipient read after a byte order mark and the charset parameter, where an
// HTML page has its <meta>. Fetch decodes once, so the parser reads UTF-8.
func TestFeedSourceDecodesByTheXMLDeclaration(t *testing.T) {
	latin1, err := os.ReadFile("../../testdata/feeds/latin1.xml")
	if err != nil {
		t.Fatal(err)
	}
	decoded := strings.NewReplacer("\xe9", "é", "\xe8", "è", "\xfb", "û").Replace(string(latin1))
	metaOnly := "<rss><channel><meta charset=\"windows-1252\"/><item><title>Caf\xe9</title></item></channel></rss>"
	c, base := serve(t, map[string]page{
		"/latin1":            {"application/rss+xml", string(latin1)},
		"/header-and-xml":    {"text/xml; charset=iso-8859-1", string(latin1)},
		"/header-wins":       {"text/xml; charset=windows-1252", strings.Replace(string(latin1), "ISO-8859-1", "Shift_JIS", 1)},
		"/mostly-utf8":       {"application/rss+xml", `<?xml version="1.0" encoding="ISO-8859-1"?><rss><title>Café crème brûlée</title></rss>`},
		"/utf16-label":       {"application/xml", "<?xml version='1.0' encoding='UTF-16'?><rss><title>Caf\xe9</title></rss>"},
		"/late-declaration":  {"application/xml", strings.Repeat(" ", 1024) + `<?xml version="1.0" encoding="ISO-8859-1"?><rss><title>Caf` + "\xe9</title></rss>"},
		"/leading-newline":   {"application/xml", "\r\n<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><rss><title>Caf\xe9</title></rss>"},
		"/meta-is-not-xml":   {"application/xml", metaOnly},
		"/unknown-encoding":  {"application/xml", `<?xml version="1.0" encoding="x-made-up"?><rss><title>Caf` + "\xe9</title></rss>"},
		"/declaration-later": {"application/xml", "<rss><?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><title>Caf\xe9</title></rss>"},
	})
	for _, tc := range []struct {
		path, feed, page string
	}{
		{"/latin1", decoded, string(latin1)},
		{"/header-and-xml", decoded, decoded},
		{"/header-wins", strings.Replace(decoded, "ISO-8859-1", "Shift_JIS", 1), strings.Replace(decoded, "ISO-8859-1", "Shift_JIS", 1)},
		{"/mostly-utf8", `<?xml version="1.0" encoding="ISO-8859-1"?><rss><title>Café crème brûlée</title></rss>`, `<?xml version="1.0" encoding="ISO-8859-1"?><rss><title>Café crème brûlée</title></rss>`},
		{"/utf16-label", "<?xml version='1.0' encoding='UTF-16'?><rss><title>Caf\xe9</title></rss>", "<?xml version='1.0' encoding='UTF-16'?><rss><title>Caf\xe9</title></rss>"},
		{"/late-declaration", strings.Repeat(" ", 1024) + `<?xml version="1.0" encoding="ISO-8859-1"?><rss><title>Caf` + "\xe9</title></rss>", strings.Repeat(" ", 1024) + `<?xml version="1.0" encoding="ISO-8859-1"?><rss><title>Caf` + "\xe9</title></rss>"},
		// Feeds served after a stray line break are common.
		{"/leading-newline", "\r\n<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><rss><title>Café</title></rss>", "\r\n<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><rss><title>Caf\xe9</title></rss>"},
		// A feed's <meta> is text, not a declaration; a page's is.
		{"/meta-is-not-xml", metaOnly, strings.Replace(metaOnly, "\xe9", "é", 1)},
		{"/unknown-encoding", `<?xml version="1.0" encoding="x-made-up"?><rss><title>Caf` + "\xe9</title></rss>", `<?xml version="1.0" encoding="x-made-up"?><rss><title>Caf` + "\xe9</title></rss>"},
		{"/declaration-later", "<rss><?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><title>Caf\xe9</title></rss>", "<rss><?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><title>Caf\xe9</title></rss>"},
	} {
		feed, err := c.Fetch(AsFeedSource(context.Background()), base+tc.path, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if string(feed.Body) != tc.feed {
			t.Errorf("%s as a feed: %q, want %q", tc.path, feed.Body, tc.feed)
		}
		page, err := c.Fetch(context.Background(), base+tc.path, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if string(page.Body) != tc.page {
			t.Errorf("%s as a page: %q, want %q", tc.path, page.Body, tc.page)
		}
	}
}
