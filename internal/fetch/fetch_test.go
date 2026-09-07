package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
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
