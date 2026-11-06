package fetch

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// prescanBytes is how far into a page browsers look for a <meta> naming its
// encoding.
const prescanBytes = 1024

var errDecodedTooLarge = errors.New("source exceeds 4 MiB limit once decoded to UTF-8")

// toUTF8 returns a page as UTF-8, which extraction, the visual selector and the
// store all assume. Static mode used to parse a windows-1252 or Shift_JIS page
// as UTF-8, so the store replaced its accented and CJK characters with U+FFFD,
// while Chromium decodes the page itself and browser mode showed it correctly.
//
// The encoding comes from the first of these that names one browsers support:
// a byte order mark, the Content-Type charset, then a <meta> in the first 1024
// bytes. Anything else returns body unchanged, as before: a page declaring no
// encoding, or one no browser knows, UTF-8 itself, and the "replacement"
// encoding browsers use to blank out ISO-2022-KR and HZ pages. So does a page
// that declares a legacy encoding but is mostly UTF-8 (see mostlyUTF8), which
// is a server or template still naming Latin-1 after the site moved to UTF-8.
// A UTF-8 page therefore keeps its exact bytes, byte order mark included.
//
// The result is bounded by MaxBody like the raw response, because the visual
// selector and browser mode apply that limit to the decoded page too. A page
// refused for that returns body unchanged with the error, as a page over the
// raw limit does, so diagnostics still report the size the source sent.
func toUTF8(body []byte, contentType string) ([]byte, error) {
	name, skip := "", 0
	switch {
	case bytes.HasPrefix(body, []byte{0xef, 0xbb, 0xbf}):
		return body, nil
	case bytes.HasPrefix(body, []byte{0xfe, 0xff}):
		name, skip = "utf-16be", 2
	case bytes.HasPrefix(body, []byte{0xff, 0xfe}):
		name, skip = "utf-16le", 2
	default:
		name = declaredCharset(body, contentType)
		if name == "" || name == "utf-8" || name == "replacement" || mostlyUTF8(body) {
			return body, nil
		}
	}
	enc, _ := charset.Lookup(name)
	out, err := io.ReadAll(io.LimitReader(enc.NewDecoder().Reader(bytes.NewReader(body[skip:])), MaxBody+1))
	if err != nil {
		return body, nil
	}
	if len(out) > MaxBody {
		return body, errDecodedTooLarge
	}
	return out, nil
}

// mostlyUTF8 reports whether body has more valid multibyte UTF-8 characters
// than bytes that are invalid as UTF-8. A page declaring a legacy encoding that
// passes is a UTF-8 page whose server or template kept an old declaration, and
// decoding it would turn every accented or CJK character into mojibake where it
// reads correctly today. One stray byte, such as an excerpt cut in the middle
// of a character, must not tip such a page over, so the test is a majority
// rather than validity. Real legacy text is the other way round: an accented
// windows-1252 letter is almost always followed by ASCII, which UTF-8 rejects,
// and legacy CJK and Thai text is several times more invalid than valid.
// A page with no non-ASCII bytes fails too, so it is still decoded, as 7-bit
// ISO-2022-JP and ASCII text in UTF-16 need.
func mostlyUTF8(body []byte) bool {
	valid, invalid := 0, 0
	for i := 0; i < len(body); {
		if body[i] < utf8.RuneSelf {
			i++
			continue
		}
		r, n := utf8.DecodeRune(body[i:])
		if r == utf8.RuneError && n == 1 {
			invalid++
		} else {
			valid++
		}
		i += n
	}
	return valid > invalid
}

// declaredCharset returns the canonical name of the encoding the response
// header or the page declares, or "" when neither names a known one.
func declaredCharset(body []byte, contentType string) string {
	if _, params, err := mime.ParseMediaType(contentType); err == nil {
		if _, name := charset.Lookup(params["charset"]); name != "" {
			return name
		}
	}
	return metaCharset(body[:min(len(body), prescanBytes)])
}

// metaCharset is the HTML standard's prescan: the first <meta> naming a known
// encoding, through its charset attribute or through http-equiv="content-type"
// with a charset in content.
func metaCharset(head []byte) string {
	z := html.NewTokenizer(bytes.NewReader(head))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken, html.SelfClosingTagToken:
			tag, more := z.TagName()
			if string(tag) != "meta" {
				continue
			}
			label, content := "", ""
			declared, pragma := false, false
			seen := map[string]bool{}
			for more {
				var k, v []byte
				k, v, more = z.TagAttr()
				if seen[string(k)] {
					continue
				}
				seen[string(k)] = true
				switch string(k) {
				case "charset":
					label, declared = string(v), true
				case "http-equiv":
					pragma = strings.EqualFold(string(v), "content-type")
				case "content":
					content = string(v)
				}
			}
			if !declared && pragma {
				label = contentCharset(content)
			}
			_, name := charset.Lookup(label)
			switch {
			case name == "":
				continue
			case strings.HasPrefix(name, "utf-16"):
				// A page read this far as ASCII is not UTF-16.
				return "utf-8"
			case name == "x-user-defined":
				return "windows-1252"
			}
			return name
		}
	}
}

// contentCharset extracts the charset from a <meta> content value such as
// "text/html; charset=Shift_JIS", following the HTML standard.
func contentCharset(s string) string {
	s = strings.ToLower(s)
	for {
		i := strings.Index(s, "charset")
		if i < 0 {
			return ""
		}
		s = strings.TrimLeft(s[i+len("charset"):], "\t\n\f\r ")
		if !strings.HasPrefix(s, "=") {
			continue
		}
		s = strings.TrimLeft(s[1:], "\t\n\f\r ")
		if s == "" {
			return ""
		}
		if q := s[0]; q == '"' || q == '\'' {
			if end := strings.IndexByte(s[1:], q); end >= 0 {
				return s[1 : 1+end]
			}
			return ""
		}
		if end := strings.IndexAny(s, "\t\n\f\r ;"); end >= 0 {
			return s[:end]
		}
		return s
	}
}
