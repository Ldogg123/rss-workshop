package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPolicy(t *testing.T) {
	c, _ := New(time.Second, "")
	for _, s := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "224.0.0.1", "0.0.0.0", "fc00::1", "64:ff9b::7f00:1"} {
		if c.Allowed(netip.MustParseAddr(s)) {
			t.Errorf("allowed %s", s)
		}
	}
	if !c.Allowed(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("blocked public IP")
	}
	c, _ = New(time.Second, "127.0.0.1/32")
	if !c.Allowed(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("allowlist ignored")
	}
	for _, u := range []string{"file:///etc/passwd", "http://user:secret@example.com", "javascript:alert(1)"} {
		if ValidateURL(u) == nil {
			t.Fatal(u)
		}
	}
}

func TestExternalSourcePreflight(t *testing.T) {
	c, err := New(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://127.0.0.1/", "http://10.20.30.40/", "http://[::1]/", "http://[::ffff:127.0.0.1]/", "http://169.254.169.254/", "file:///etc/passwd", "https://user:secret@8.8.8.8/"} {
		if err := c.CheckURL(context.Background(), raw); err == nil {
			t.Fatalf("accepted blocked external source %s", raw)
		}
	}
	if err := c.CheckURL(context.Background(), "https://8.8.8.8/"); err != nil {
		t.Fatal("blocked public literal IP", err)
	}
	c, err = New(time.Second, "10.20.30.40/32")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CheckURL(context.Background(), "http://10.20.30.40/"); err != nil {
		t.Fatal("ignored explicit source allowlist", err)
	}
}
func TestFetchValidationLimitsAnd304(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/large":
			w.Write([]byte(strings.Repeat("x", MaxBody+1)))
		case "/redirect":
			http.Redirect(w, r, "http://169.254.169.254/", 302)
		default:
			if r.Header.Get("If-None-Match") == "test" {
				w.WriteHeader(304)
				return
			}
			w.Header().Set("ETag", "test")
			w.Write([]byte("hello"))
		}
	}))
	defer srv.Close()
	c, _ := New(time.Second, "")
	if _, e := c.Fetch(context.Background(), srv.URL, "", ""); e == nil {
		t.Fatal("fetched loopback without allowlist")
	}
	c, _ = New(time.Second, "127.0.0.1/32")
	r, e := c.Fetch(context.Background(), srv.URL, "", "")
	if e != nil || string(r.Body) != "hello" || r.ETag != "test" {
		t.Fatal(r, e)
	}
	r, e = c.Fetch(context.Background(), srv.URL, "test", "")
	if e != nil || r.Status != 304 {
		t.Fatal(r, e)
	}
	for _, path := range []string{"/large", "/redirect"} {
		if _, e = c.Fetch(context.Background(), srv.URL+path, "", ""); e == nil {
			t.Fatal("accepted", path)
		}
	}
}

// Sources see the product's name with no version, so an upgrade alone never
// changes what they receive. A redirect keeps it, and an operator's
// FETCH_USER_AGENT replaces it on every request, conditional ones included.
func TestUserAgent(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.UserAgent())
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/page", http.StatusFound)
			return
		}
		w.Write([]byte("page"))
	}))
	defer srv.Close()
	c, _ := New(time.Second, "127.0.0.1/32")
	for _, path := range []string{"/page", "/moved"} {
		if _, err := c.Fetch(context.Background(), srv.URL+path, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	c.UserAgent = "GoRSS/0.1 (+self-hosted feed builder)"
	if _, err := c.Fetch(context.Background(), srv.URL+"/page", `"tag"`, "Mon, 02 Jan 2006 15:04:05 GMT"); err != nil {
		t.Fatal(err)
	}
	// A feed's own User-Agent, carried by its context, replaces both, through
	// a redirect too. A blank one leaves the client's.
	for _, path := range []string{"/page", "/moved"} {
		if _, err := c.Fetch(WithUserAgent(context.Background(), "Feed/1 (+mailto:ops@example.net)"), srv.URL+path, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Fetch(WithUserAgent(context.Background(), ""), srv.URL+"/page", "", ""); err != nil {
		t.Fatal(err)
	}
	c.UserAgent = ""
	if _, err := c.Fetch(WithUserAgent(context.Background(), "Feed/1 (+mailto:ops@example.net)"), srv.URL+"/page", "", ""); err != nil {
		t.Fatal(err)
	}
	const name, old, own = "RSS-Workshop (+https://github.com/Ldogg123/rss-workshop)", "GoRSS/0.1 (+self-hosted feed builder)", "Feed/1 (+mailto:ops@example.net)"
	want := []string{name, name, name, old, own, own, own, old, own}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("sources saw %q, want %q", got, want)
	}
}

// FETCH_USER_AGENT and a recipe's User-Agent share one rule: trimmed, then at
// most 256 bytes of printable ASCII. Anything a header cannot carry as written
// is refused, a line break above all, since it would add a header of its own.
func TestCheckUserAgent(t *testing.T) {
	for raw, want := range map[string]string{
		"":      "",
		" \t\n": "",
		" GoRSS/0.1 (+self-hosted feed builder)\r\n": "GoRSS/0.1 (+self-hosted feed builder)",
		"Reader/2 (+mailto:ops@example.net; ~x)":     "Reader/2 (+mailto:ops@example.net; ~x)",
		strings.Repeat("a", 256):                     strings.Repeat("a", 256),
		" " + strings.Repeat("a", 256) + " ":         strings.Repeat("a", 256),
	} {
		if got, ok := CheckUserAgent(raw); !ok || got != want {
			t.Errorf("CheckUserAgent(%q) = %q, %v; want %q", raw, got, ok, want)
		}
		// ValidateUserAgent does not trim: it accepts only what is already
		// trimmed, which is what a request sends.
		if err := ValidateUserAgent(raw); (err == nil) != (raw == want) {
			t.Errorf("ValidateUserAgent(%q) = %v", raw, err)
		}
	}
	for _, raw := range []string{
		"agent\r\nX-Injected: 1", "agent\nX-Injected: 1", "agent\rX-Injected: 1", "agent\tname",
		"agent\x00", "agent\x1b[0m", "agent\x7f", "agenté", "agent\xff", "agent\u2028name",
		strings.Repeat("a", 257), strings.Repeat("é", 100),
	} {
		if got, ok := CheckUserAgent(raw); ok {
			t.Errorf("CheckUserAgent(%q) accepted %q", raw, got)
		}
		if ValidateUserAgent(raw) == nil {
			t.Errorf("ValidateUserAgent(%q) accepted it", raw)
		}
	}
}

// A context's User-Agent is sent exactly as given or not at all. Recipes are
// trimmed and checked before they get here; a value that skipped that, with
// space around it, a line break or anything else a header cannot carry as
// written, is refused before the source is contacted. It is neither sent
// altered nor left to fail in the transport behind a generic connection
// error, and the error does not repeat it.
func TestFetchRefusesAContextUserAgentItCannotSendAsGiven(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write([]byte("page"))
	}))
	defer srv.Close()
	c, _ := New(time.Second, "127.0.0.1/32")
	for ua, want := range map[string]string{
		" Secret/1":                  "User-Agent must not begin or end with white space",
		"Secret/1\n":                 "User-Agent must not begin or end with white space",
		"\vSecret/1":                 "User-Agent must not begin or end with white space",
		"Secret/1\u00a0":             "User-Agent must not begin or end with white space",
		"\u0085Secret/1":             "User-Agent must not begin or end with white space",
		"Secret/1\r\nX-Injected: 1":  "User-Agent must be at most 256 printable ASCII characters",
		"Secret/é":                   "User-Agent must be at most 256 printable ASCII characters",
		strings.Repeat("Secret", 43): "User-Agent must be at most 256 printable ASCII characters",
	} {
		if _, err := c.Fetch(WithUserAgent(context.Background(), ua), srv.URL+"/page", "", ""); err == nil || err.Error() != want {
			t.Errorf("Fetch with User-Agent %q: %v, want %q", ua, err, want)
		}
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("the source received %d requests", n)
	}
}
