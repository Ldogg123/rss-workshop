package fetch

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

type page struct{ contentType, body string }

// serve answers each path with its page, through the real fetcher.
func serve(t *testing.T, pages map[string]page) (*Client, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header()["Content-Type"] = []string{p.contentType}
		w.Write([]byte(p.body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(time.Second, "127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	return c, srv.URL
}

func utf16Page(order binary.AppendByteOrder, s string) string {
	b := []byte{}
	for _, u := range utf16.Encode([]rune("\ufeff" + s)) {
		b = order.AppendUint16(b, u)
	}
	return string(b)
}

// Static mode decodes a page the way browser mode's Chromium does: from a byte
// order mark, then the Content-Type charset, then a <meta> in the first 1024
// bytes. Whatever declares nothing a browser would use keeps today's bytes.
func TestFetchDecodesDeclaredEncodings(t *testing.T) {
	metaAt := func(offset int) string {
		meta := `<meta charset="windows-1252">`
		return strings.Repeat(" ", offset) + meta + "<title>Caf\xe9</title>"
	}
	const latin1 = "<title>Caf\xe9 cr\xe8me</title>"
	for _, tc := range []struct {
		name string
		page page
		want string
	}{
		{"Content-Type", page{"text/html; charset=windows-1252", latin1}, "<title>Café crème</title>"},
		{"Content-Type label in any case", page{`text/html; charset="ISO-8859-1"`, latin1}, "<title>Café crème</title>"},
		// Browsers read ISO-8859-1 as windows-1252, which has curly quotes.
		{"meta charset", page{"text/html", "<meta charset=iso-8859-1><title>\x93Caf\xe9\x94</title>"}, "<meta charset=iso-8859-1><title>“Café”</title>"},
		{"meta http-equiv", page{"text/html", `<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=Shift_JIS"><title>` + "\x93\xfa\x96{\x8c\xea</title>"}, `<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=Shift_JIS"><title>日本語</title>`},
		{"meta content needs http-equiv", page{"text/html", `<meta content="text/html; charset=Shift_JIS"><title>Caf` + "\xe9</title>"}, `<meta content="text/html; charset=Shift_JIS"><title>Caf` + "\xe9</title>"},
		{"meta in a comment", page{"text/html", "<!-- <meta charset=windows-1252> --><title>Caf\xe9</title>"}, "<!-- <meta charset=windows-1252> --><title>Caf\xe9</title>"},
		{"unknown meta, then a known one", page{"text/html", "<meta charset=x-made-up><meta charset=windows-1252><title>Caf\xe9</title>"}, "<meta charset=x-made-up><meta charset=windows-1252><title>Café</title>"},
		{"meta ending at byte 1024", page{"text/html", metaAt(1024 - len(`<meta charset="windows-1252">`))}, strings.Replace(metaAt(1024-len(`<meta charset="windows-1252">`)), "\xe9", "é", 1)},
		{"meta past byte 1024", page{"text/html", metaAt(1025 - len(`<meta charset="windows-1252">`))}, metaAt(1025 - len(`<meta charset="windows-1252">`))},
		{"Content-Type beats meta", page{"text/html; charset=windows-1252", "<meta charset=shift_jis><title>Caf\xe9</title>"}, "<meta charset=shift_jis><title>Café</title>"},
		{"unknown Content-Type label falls through to meta", page{"text/html; charset=x-made-up", "<meta charset=windows-1252><title>Caf\xe9</title>"}, "<meta charset=windows-1252><title>Café</title>"},
		{"UTF-16LE byte order mark beats Content-Type", page{"text/html; charset=windows-1252", utf16Page(binary.LittleEndian, "<title>Café 日本語</title>")}, "<title>Café 日本語</title>"},
		{"UTF-16BE byte order mark", page{"text/html", utf16Page(binary.BigEndian, "<title>Café</title>")}, "<title>Café</title>"},
		{"seven-bit ISO-2022-JP", page{"text/html; charset=iso-2022-jp", "<title>\x1b$BF|K\\8l\x1b(B</title>"}, "<title>日本語</title>"},
		{"EUC-KR", page{"text/html; charset=euc-kr", "<title>\xc7\xd1\xb1\xb9\xbe\xee</title>"}, "<title>한국어</title>"},

		// Left as they are.
		{"no declaration", page{"text/html", latin1}, latin1},
		{"no Content-Type", page{"", latin1}, latin1},
		{"unknown label", page{"text/html; charset=x-made-up", latin1}, latin1},
		{"replacement encoding", page{"text/html; charset=iso-2022-kr", latin1}, latin1},
		{"UTF-8 declared", page{"text/html; charset=utf-8", latin1}, latin1},
		{"UTF-8 byte order mark beats Content-Type", page{"text/html; charset=windows-1252", "\xef\xbb\xbf<title>Café</title>"}, "\xef\xbb\xbf<title>Café</title>"},
		{"meta UTF-16 on an ASCII page", page{"text/html", `<meta charset="utf-16"><title>Cafe</title>`}, `<meta charset="utf-16"><title>Cafe</title>`},
		// A site that moved to UTF-8 while its server or template still names a
		// legacy encoding. A real legacy page is practically never valid UTF-8.
		{"Latin-1 declared on a UTF-8 page", page{"text/html; charset=iso-8859-1", "<title>Café crème</title>"}, "<title>Café crème</title>"},
		{"Shift_JIS declared on a UTF-8 page", page{"text/html", "<meta charset=shift_jis><title>日本語</title>"}, "<meta charset=shift_jis><title>日本語</title>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, base := serve(t, map[string]page{"/": tc.page})
			r, err := c.Fetch(context.Background(), base+"/", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if string(r.Body) != tc.want {
				t.Fatalf("body\n got %q\nwant %q", r.Body, tc.want)
			}
		})
	}
}

// The decoded page is held to the same 4 MiB as the response, because the
// visual selector and browser mode apply that limit to decoded pages too. Each
// é is one byte in windows-1252 and two in UTF-8.
func TestFetchLimitsTheDecodedPage(t *testing.T) {
	c, base := serve(t, map[string]page{
		"/fits":    {"text/html; charset=windows-1252", strings.Repeat("x", MaxBody-2) + "\xe9"},
		"/grows":   {"text/html; charset=windows-1252", strings.Repeat("x", MaxBody-1) + "\xe9"},
		"/doubles": {"text/html; charset=windows-1252", strings.Repeat("\xe9", MaxBody)},
		"/raw":     {"text/html", strings.Repeat("\xe9", MaxBody)},
	})
	r, err := c.Fetch(context.Background(), base+"/fits", "", "")
	if err != nil || len(r.Body) != MaxBody || !strings.HasSuffix(string(r.Body), "xé") {
		t.Fatalf("a page decoding to exactly 4 MiB: %d bytes, %v", len(r.Body), err)
	}
	for _, path := range []string{"/grows", "/doubles"} {
		if r, err = c.Fetch(context.Background(), base+path, "", ""); err == nil || !strings.Contains(err.Error(), "4 MiB") || r.Body != nil {
			t.Fatalf("%s: a page decoding past 4 MiB gave %d bytes, %v", path, len(r.Body), err)
		}
	}
	// An undeclared page is not decoded, so its size is the response's.
	if r, err = c.Fetch(context.Background(), base+"/raw", "", ""); err != nil || len(r.Body) != MaxBody {
		t.Fatalf("an undeclared 4 MiB page: %d bytes, %v", len(r.Body), err)
	}
}
