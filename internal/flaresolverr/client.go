// Package flaresolverr fetches rendered HTML through an explicitly configured
// FlareSolverr service. Each request uses a temporary browser session; cookies,
// service diagnostics, and other browser data are never returned to callers.
package flaresolverr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rss-workshop/internal/fetch"
)

// JSON can encode each HTML byte as a six-byte Unicode escape. The extra MiB
// allows for response metadata without permitting an unbounded response body.
const maxResponseBody = 6*fetch.MaxBody + 1<<20

type URLPolicy interface {
	CheckURL(context.Context, string) error
}

type Client struct {
	endpoint  string
	timeout   time.Duration
	grace     time.Duration
	policy    URLPolicy
	http      *http.Client
	transport *http.Transport
	slots     chan struct{}
	root      context.Context
	stop      context.CancelFunc
}

// New allows a private service address because the operator explicitly chooses
// it. Source and final URLs still pass policy checks, but intermediate redirects
// and subresources are fetched by the remote service and require its own network
// restrictions. Service HTTP redirects and environment proxies are disabled.
func New(endpoint string, slots int, timeout time.Duration, policy URLPolicy) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.ForceQuery || strings.Contains(endpoint, "#") || u.Opaque != "" {
		return nil, errors.New("FLARESOLVERR_URL must be an HTTP/HTTPS service URL without credentials, a query, or a fragment")
	}
	if slots < 1 || slots > 4 || timeout < 5*time.Second || timeout > 2*time.Minute || policy == nil {
		return nil, errors.New("FlareSolverr needs 1–4 slots, a 5–120 second timeout, and an outbound URL policy")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/v1") {
		u.Path += "/v1"
	}
	u.RawPath = ""
	const grace = 2 * time.Second
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          slots,
		MaxIdleConnsPerHost:   slots,
		MaxConnsPerHost:       slots,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout + grace,
	}
	root, stop := context.WithCancel(context.Background())
	return &Client{
		endpoint: u.String(), timeout: timeout, grace: grace, policy: policy,
		transport: tr, slots: make(chan struct{}, slots), root: root, stop: stop,
		http: &http.Client{Transport: tr, Timeout: timeout + grace, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

// Stats includes slots held briefly after interrupted API requests: the remote
// browser may continue until maxTimeout even when its client disconnects.
func (c *Client) Stats() (active, capacity int) { return len(c.slots), cap(c.slots) }

func (c *Client) Close() {
	c.stop()
	c.transport.CloseIdleConnections()
}

// Fetch always requests a fresh rendered page. Conditional headers are ignored:
// FlareSolverr does not expose conditional GET support or a reusable cache here.
func (c *Client) Fetch(ctx context.Context, rawURL, _, _ string) (fetch.Result, error) {
	var out fetch.Result
	ctx, cancel := context.WithTimeout(ctx, c.timeout+c.grace)
	defer cancel()
	stopCancel := context.AfterFunc(c.root, cancel)
	defer stopCancel()
	if c.root.Err() != nil {
		return out, errors.New("FlareSolverr client is closed")
	}
	if err := fetch.ValidateURL(rawURL); err != nil {
		return out, err
	}
	if err := c.policy.CheckURL(ctx, rawURL); err != nil {
		return out, err
	}
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return out, ctx.Err()
	}
	var holdFor time.Duration
	defer func() {
		if holdFor > 0 && c.root.Err() == nil {
			// FlareSolverr offers no cancellation command for temporary sessions.
			// Connection setup can delay the start of remote work, so reserve a
			// full remote budget plus grace from this failure, not from Do's start.
			// This is conservative accounting, not a guarantee about the remote
			// server: it must enforce its own execution and concurrency limits.
			go func() {
				timer := time.NewTimer(holdFor)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-c.root.Done():
				}
				<-c.slots
			}()
		} else {
			<-c.slots
		}
	}()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	deadline, _ := ctx.Deadline()
	remaining := time.Until(deadline)
	// Reserve transport grace even when a caller supplies a shorter deadline.
	// For short requests, one tenth of the remaining budget is the grace.
	budget := min(c.timeout, remaining-min(c.grace, remaining/10))
	maxTimeout := budget.Milliseconds()
	if maxTimeout < 1 {
		return out, context.DeadlineExceeded
	}
	payload, _ := json.Marshal(struct {
		Command    string `json:"cmd"`
		URL        string `json:"url"`
		MaxTimeout int64  `json:"maxTimeout"`
	}{"request.get", rawURL, maxTimeout})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return out, errors.New("invalid FlareSolverr service URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	holdFor = time.Duration(maxTimeout)*time.Millisecond + c.grace
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("FlareSolverr request failed; check the service address, connection, and timeout")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("could not read the FlareSolverr response")
	}
	if len(body) > maxResponseBody {
		return out, errors.New("FlareSolverr response exceeds its size limit")
	}
	// A complete response means the remote handler has finished, including
	// responses which report a failed challenge or contain malformed data.
	holdFor = 0
	var result struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution *struct {
			URL      string            `json:"url"`
			Status   int               `json:"status"`
			Response *string           `json:"response"`
			Headers  map[string]string `json:"headers"`
		} `json:"solution"`
	}
	decodeErr := json.Unmarshal(body, &result)
	if resp.StatusCode != http.StatusOK {
		if decodeErr == nil && result.Status == "error" {
			return out, fmt.Errorf("FlareSolverr service returned HTTP %d: %s", resp.StatusCode, serviceFailure(result.Message))
		}
		return out, fmt.Errorf("FlareSolverr service returned HTTP %d; check its service logs", resp.StatusCode)
	}
	if decodeErr != nil {
		return out, errors.New("FlareSolverr returned an invalid JSON response")
	}
	if result.Status == "error" {
		return out, errors.New(serviceFailure(result.Message))
	}
	if result.Status != "ok" || result.Solution == nil || result.Solution.URL == "" || result.Solution.Status < 100 || result.Solution.Status > 599 {
		return out, errors.New("FlareSolverr returned an incomplete solution")
	}
	solution := result.Solution
	if err := fetch.ValidateURL(solution.URL); err != nil {
		return out, errors.New("FlareSolverr returned an invalid final source URL")
	}
	if err := c.policy.CheckURL(ctx, solution.URL); err != nil {
		return out, fmt.Errorf("FlareSolverr final source URL: %w", err)
	}
	out.URL, out.Status = solution.URL, solution.Status
	for name, value := range solution.Headers {
		if strings.EqualFold(name, "Retry-After") {
			out.RetryAfter = retryAfter(value)
		}
	}
	if out.Status != http.StatusOK {
		return out, &fetch.HTTPError{Status: out.Status, RetryAfter: out.RetryAfter}
	}
	if solution.Response == nil || strings.TrimSpace(*solution.Response) == "" {
		return out, errors.New("FlareSolverr returned an empty page")
	}
	if len(*solution.Response) > fetch.MaxBody {
		return out, errors.New("source exceeds 4 MiB limit")
	}
	out.Body = []byte(*solution.Response)
	return out, nil
}

// Do not relay free-form service diagnostics: they can contain source HTML,
// cookies, URLs with secrets, or browser stack traces. Only known categories
// influence the fixed, bounded message shown to the administrator.
func serviceFailure(raw string) string {
	if len(raw) > 4096 {
		return "FlareSolverr could not fetch this page; check its service logs"
	}
	message := strings.ToLower(raw)
	switch {
	case strings.Contains(message, "captcha"):
		return "FlareSolverr encountered a CAPTCHA that it could not solve"
	case strings.Contains(message, "timeout"), strings.Contains(message, "timed out"):
		return "FlareSolverr could not finish before its timeout; try again or increase the configured timeout"
	case strings.Contains(message, "browser"), strings.Contains(message, "chrome"), strings.Contains(message, "session not created"):
		return "FlareSolverr could not complete its browser request; check its service logs"
	default:
		return "FlareSolverr could not fetch this page; check its service logs"
	}
}

func retryAfter(raw string) time.Duration {
	if n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
		return time.Duration(min(max(n, 0), 86400)) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		return min(max(time.Until(when), 0), 24*time.Hour)
	}
	return 0
}
