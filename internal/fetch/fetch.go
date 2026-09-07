package fetch

import (
	"context"
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

type Result struct {
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
	client  *http.Client
	allow   []netip.Prefix
	mu      sync.Mutex
	origins map[string]*origin
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
	req.Header.Set("User-Agent", "GoRSS/0.1 (+self-hosted feed builder)")
	req.Header.Set("Accept", "text/html, application/xhtml+xml")
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
	return out, nil
}

// DialContext validates resolved destination addresses and connects without a second DNS lookup.
func (c *Client) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return c.dial(ctx, network, address)
}
