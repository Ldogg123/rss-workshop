package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const MaxBody = 4 << 20

// DefaultUserAgent names the project to sources. It carries no version, so an
// upgrade alone never changes what a source sees, and an instance does not
// advertise which release it runs. Chromium and FlareSolverr send their own.
const DefaultUserAgent = "RSS-Workshop (+https://github.com/Ldogg123/rss-workshop)"

// MaxUserAgent is the longest User-Agent CheckUserAgent accepts, in bytes.
const MaxUserAgent = 256

// CheckUserAgent trims surrounding white space from a User-Agent and reports
// whether the rest can be sent: at most MaxUserAgent bytes of printable ASCII.
// A line break would let the value add headers of its own, and Go refuses one
// at request time, failing every fetch that carries it; other control and
// non-ASCII characters are refused too, so a value that passes here is sent
// exactly as written. FETCH_USER_AGENT and a recipe's own User-Agent both go
// through this. The trimmed value is returned whether or not it passes.
func CheckUserAgent(raw string) (string, bool) {
	ua := strings.TrimSpace(raw)
	return ua, len(ua) <= MaxUserAgent && !strings.ContainsFunc(ua, func(r rune) bool { return r < ' ' || r > '~' })
}

// ValidateUserAgent accepts a User-Agent that can be sent exactly as given:
// one CheckUserAgent accepts and leaves unchanged, so blank too. It does not
// trim; a caller that wants trimming runs CheckUserAgent first and validates
// what it returns. A recipe's value is checked with this when it is saved,
// previewed or loaded by the visual selector, and again by Fetch before
// anything is sent. The errors do not repeat the value.
func ValidateUserAgent(ua string) error {
	checked, ok := CheckUserAgent(ua)
	if !ok {
		return fmt.Errorf("User-Agent must be at most %d printable ASCII characters", MaxUserAgent)
	}
	if checked != ua {
		return errors.New("User-Agent must not begin or end with white space")
	}
	return nil
}

// MaxAcceptLanguage is the longest Accept-Language ValidateAcceptLanguage
// accepts, in bytes, and MaxLanguageRanges the most language ranges it lists.
const (
	MaxAcceptLanguage = 128
	MaxLanguageRanges = 10
)

// ValidateAcceptLanguage accepts an Accept-Language that can be sent exactly
// as given: blank, or at most MaxLanguageRanges comma-separated language
// ranges in at most MaxAcceptLanguage bytes. A range is * or a language tag
// such as en or de-CH, letters then hyphen-separated letters or digits of at
// most 8 each, and may carry a weight from 0 to 1 with at most three decimals,
// such as ;q=0.8. Spaces may surround the commas and semicolons, not the
// value: like ValidateUserAgent it does not trim, and the handlers trim what
// they accept first. A recipe's value is checked with this when it is saved,
// previewed or loaded by the visual selector, and again by Fetch before
// anything is sent. The errors do not repeat the value.
func ValidateAcceptLanguage(v string) error {
	if v == "" {
		return nil
	}
	if strings.TrimSpace(v) != v {
		return errors.New("Accept-Language must not begin or end with white space")
	}
	if len(v) > MaxAcceptLanguage {
		return fmt.Errorf("Accept-Language must be at most %d characters", MaxAcceptLanguage)
	}
	ranges := strings.Split(v, ",")
	if len(ranges) > MaxLanguageRanges {
		return fmt.Errorf("Accept-Language must list at most %d languages", MaxLanguageRanges)
	}
	for i, r := range ranges {
		if !languageRange(strings.Trim(r, " ")) {
			return fmt.Errorf("Accept-Language entry %d must be a language tag such as en or de-CH, or *, with an optional weight such as ;q=0.8", i+1)
		}
	}
	return nil
}

// languageRange reports whether s is one entry of an Accept-Language list:
// RFC 4647's basic language range with RFC 9110's optional weight.
func languageRange(s string) bool {
	tag, weight, weighted := strings.Cut(s, ";")
	if weighted && !qvalue(strings.TrimLeft(weight, " ")) {
		return false
	}
	if tag = strings.TrimRight(tag, " "); tag == "*" {
		return true
	}
	for i, sub := range strings.Split(tag, "-") {
		if len(sub) < 1 || len(sub) > 8 {
			return false
		}
		for _, c := range []byte(sub) {
			letter := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
			if !letter && (i == 0 || c < '0' || c > '9') {
				return false
			}
		}
	}
	return true
}

// qvalue reports whether s is a weight: q= then 0 to 1 with at most three
// decimals.
func qvalue(s string) bool {
	if len(s) < 3 || s[0] != 'q' && s[0] != 'Q' || s[1] != '=' {
		return false
	}
	whole, decimals, _ := strings.Cut(s[2:], ".")
	if whole != "0" && whole != "1" || len(decimals) > 3 {
		return false
	}
	for _, c := range []byte(decimals) {
		if c < '0' || c > '9' || whole == "1" && c != '0' {
			return false
		}
	}
	return true
}

// Headers are the request headers a feed sets for itself. A blank field
// leaves the client's own: its User-Agent, and no Accept-Language at all.
type Headers struct {
	UserAgent      string
	AcceptLanguage string
}

type headersKey struct{}

// WithHeaders returns a context whose static requests send h. A feed's
// refresh, preview and snapshot carry its recipe's headers this way, so the
// Fetcher interface stays as it is and every static request made for the
// feed, article pages included, sends them. Fetch refuses a value that
// ValidateUserAgent or ValidateAcceptLanguage refuses.
func WithHeaders(ctx context.Context, h Headers) context.Context {
	if h == (Headers{}) {
		return ctx
	}
	return context.WithValue(ctx, headersKey{}, h)
}

// htmlAccept is what every static request asks for, except the list request
// of a feed source. It must not change: a source that negotiates content would
// start sending existing feeds something else.
const htmlAccept = "text/html, application/xhtml+xml"

// feedAccept is what a feed source's list request asks for. The final */*
// keeps a server that answers only what a request lists from refusing a feed
// it labels some other way, such as text/plain.
const feedAccept = "application/rss+xml, application/atom+xml, application/rdf+xml;q=0.9, application/xml;q=0.8, text/xml;q=0.8, */*;q=0.1"

type feedSourceKey struct{}

// AsFeedSource returns a context whose static requests ask for an RSS, RDF or
// Atom feed and decode the answer as XML: its encoding comes from a byte order
// mark, the Content-Type charset, then the XML declaration, in place of an
// HTML <meta>. Only a feed source's own list request carries it; the article
// pages its stories link to are HTML, and keep the HTML Accept and decoding.
func AsFeedSource(ctx context.Context) context.Context {
	return context.WithValue(ctx, feedSourceKey{}, true)
}

type Result struct {
	// Body is UTF-8 when the page declares an encoding Fetch can decode; see
	// toUTF8. Rendered pages are always UTF-8.
	Body                    []byte
	URL, ETag, LastModified string
	Status                  int
	RetryAfter              time.Duration
}
type HTTPError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string { return fmt.Sprintf("source returned HTTP %d", e.Status) }

type Fetcher interface {
	Fetch(context.Context, string, string, string) (Result, error)
}
type Client struct {
	// UserAgent replaces DefaultUserAgent when it is not blank, and a
	// context's Headers replace both. Set it before the client is
	// shared; config.Load has already validated it.
	UserAgent string
	client    *http.Client
	allow     []netip.Prefix
	mu        sync.Mutex
	origins   map[string]*origin
}
type origin struct {
	sem   chan struct{}
	users int
}

func New(timeout time.Duration, allow string) (*Client, error) {
	c := &Client{origins: map[string]*origin{}}
	for _, s := range strings.Split(allow, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, e := netip.ParsePrefix(s)
		if e != nil {
			return nil, fmt.Errorf("invalid ALLOW_CIDRS entry")
		}
		c.allow = append(c.allow, p)
	}
	tr := &http.Transport{Proxy: nil, MaxIdleConns: 64, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: timeout, DialContext: c.dial}
	c.client = &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return ValidateURL(req.URL.String())
	}}
	return c, nil
}
func ValidateURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("source must be an HTTP/HTTPS URL without credentials")
	}
	return nil
}
func (c *Client) Allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range c.allow {
		if p.Contains(ip) {
			return true
		}
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "2001::/23", "2002::/16"} {
		if netip.MustParsePrefix(s).Contains(ip) {
			return false
		}
	}
	return true
}
func (c *Client) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return nil, fmt.Errorf("invalid destination")
	}
	ips, e := c.resolve(ctx, host)
	if e != nil {
		return nil, e
	}
	// Dial the validated IP directly: DNS is never resolved a second time.
	d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	for _, ip := range ips {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("source connection failed")
}

func (c *Client) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if e != nil {
		return nil, fmt.Errorf("source DNS lookup failed")
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("source has no addresses")
	}
	for _, ip := range ips {
		if !c.Allowed(ip) {
			return nil, fmt.Errorf("destination blocked by outbound policy; internal sources require ALLOW_CIDRS")
		}
	}
	return ips, nil
}

// CheckURL preflights a URL for an external fetch service. The service resolves
// and accesses it independently, so this cannot police its DNS, redirects or
// subresources. Direct local fetches still use dial's validated IP connection.
func (c *Client) CheckURL(ctx context.Context, raw string) error {
	if err := ValidateURL(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	_, err := c.resolve(ctx, u.Hostname())
	return err
}
func (c *Client) acquire(ctx context.Context, key string) (func(), error) {
	c.mu.Lock()
	o := c.origins[key]
	if o == nil {
		o = &origin{sem: make(chan struct{}, 2)}
		c.origins[key] = o
	}
	o.users++
	c.mu.Unlock()
	releaseUser := func() {
		c.mu.Lock()
		o.users--
		if o.users == 0 {
			delete(c.origins, key)
		}
		c.mu.Unlock()
	}
	select {
	case o.sem <- struct{}{}:
		return func() { <-o.sem; releaseUser() }, nil
	case <-ctx.Done():
		releaseUser()
		return nil, ctx.Err()
	}
}
func (c *Client) Fetch(ctx context.Context, raw, etag, modified string) (Result, error) {
	var out Result
	if e := ValidateURL(raw); e != nil {
		return out, e
	}
	// A feed's own User-Agent, else the operator's FETCH_USER_AGENT, else
	// the project's name, and its Accept-Language only when it has one. A
	// feed's values were checked when its recipe was accepted; one that
	// arrives here without that check is refused before anything is sent,
	// rather than sent altered or failed by the transport behind the generic
	// connection error below.
	h, _ := ctx.Value(headersKey{}).(Headers)
	if e := ValidateUserAgent(h.UserAgent); e != nil {
		return out, e
	}
	if e := ValidateAcceptLanguage(h.AcceptLanguage); e != nil {
		return out, e
	}
	ua := h.UserAgent
	if ua == "" {
		ua = c.UserAgent
	}
	if ua == "" {
		ua = DefaultUserAgent
	}
	u, _ := url.Parse(raw)
	release, e := c.acquire(ctx, strings.ToLower(u.Host))
	if e != nil {
		return out, e
	}
	defer release()
	req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return out, fmt.Errorf("invalid source URL")
	}
	feed, _ := ctx.Value(feedSourceKey{}).(bool)
	req.Header.Set("User-Agent", ua)
	if feed {
		req.Header.Set("Accept", feedAccept)
	} else {
		req.Header.Set("Accept", htmlAccept)
	}
	if h.AcceptLanguage != "" {
		req.Header.Set("Accept-Language", h.AcceptLanguage)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if modified != "" {
		req.Header.Set("If-Modified-Since", modified)
	}
	resp, e := c.client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, fmt.Errorf("fetch failed (timeout, connection, TLS, redirect, or outbound policy); check source and allowlist")
	}
	defer resp.Body.Close()
	out = Result{URL: resp.Request.URL.String(), ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), Status: resp.StatusCode}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, e := strconv.Atoi(s); e == nil {
			out.RetryAfter = time.Duration(min(max(n, 0), 86400)) * time.Second
		} else if t, e := http.ParseTime(s); e == nil {
			out.RetryAfter = min(max(time.Until(t), 0), 24*time.Hour)
		}
	}
	if resp.StatusCode == 304 {
		return out, nil
	}
	if resp.StatusCode != 200 {
		return out, &HTTPError{Status: out.Status, RetryAfter: out.RetryAfter}
	}
	out.Body, e = io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if e != nil {
		return out, fmt.Errorf("could not read source body")
	}
	if len(out.Body) > MaxBody {
		return out, fmt.Errorf("source exceeds 4 MiB limit")
	}
	// List pages, article pages and the visual selector all arrive here, so
	// they decode a page the same way. A feed source's list is XML, which
	// names its encoding in its declaration rather than a <meta>.
	if out.Body, e = toUTF8(out.Body, resp.Header.Get("Content-Type"), feed); e != nil {
		return out, e
	}
	return out, nil
}

// DialContext validates resolved destination addresses and connects without a second DNS lookup.
func (c *Client) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return c.dial(ctx, network, address)
}
