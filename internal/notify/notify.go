// Package notify tells the operator when feeds start failing or recover, and
// when the database stops storing or serving the library, by sending a message
// to one configured endpoint: an ntfy topic or a generic JSON webhook.
//
// The endpoint is trusted operator configuration, like FLARESOLVERR_URL: a
// private address is allowed and ALLOW_CIDRS does not apply. Environment
// proxies and redirects are not followed, so a message goes to that address
// and nowhere else. Its URL and token are secrets, because a webhook URL often
// embeds a token and a topic on a public ntfy server works as a password, so
// neither appears in an error, a log line or a message. Feed titles go only
// into request bodies: an imported title can hold a line break, which in a
// header would add headers of its own.
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"rss-workshop/internal/diagnostics"
)

// Kind names what an event reports.
type Kind string

const (
	// Failing: a feed's failed refreshes in a row reached the threshold.
	Failing Kind = "failing"
	// Recovered: a feed at or past the threshold refreshed successfully.
	Recovered Kind = "recovered"
	// NotSaved: the database refused a finished refresh, and the scheduler
	// started holding the feed.
	NotSaved Kind = "not_saved"
	// DBUnavailable: the scheduler could not read the library at all.
	DBUnavailable Kind = "db_unavailable"
	// DBAvailable: it can again, after a DBUnavailable was sent.
	DBAvailable Kind = "db_available"
	// Test: the dashboard's test button, with no feed data.
	Test Kind = "test"
)

// Event is one thing worth telling the operator. Feed and FeedID are empty
// for database events. Failures is the feed's failed refreshes in a row for
// Failing and Recovered, and 0 otherwise. Error is redacted and shortened again
// here, so a caller cannot send more than the stored error would show.
type Event struct {
	Kind     Kind
	Feed     string
	FeedID   string
	Failures int
	Error    string
}

// Delivery formats.
const (
	JSON = "json"
	Ntfy = "ntfy"
)

const (
	// QueueSize bounds the feed events waiting for the worker. Notify never
	// waits for room: a refresh must not stall on a slow notification
	// service, so an event that finds the queue full is counted and dropped.
	QueueSize = 256
	// Window is how long the worker collects events into one message, so a
	// burst of failures, as when a network outage hits every feed, sends one
	// message instead of one per feed.
	Window = 60 * time.Second
	// MaxLines bounds the events a message lists; the rest are counted.
	MaxLines = 20
	// FlushGrace bounds how long Close waits to deliver what is pending.
	FlushGrace = 2 * time.Second

	maxBatch = QueueSize // feed events kept for one message; later ones are only counted
	// maxGlobal bounds the server-wide events, which have a queue of their
	// own and are kept beside maxBatch. The scheduler sends a few an hour at
	// most, so each one is kept and listed first, however many feed events
	// the window holds.
	maxGlobal      = 8
	attemptTimeout = 10 * time.Second
	maxAttempts    = 3
	maxResponse    = 4 << 10
	maxError       = 300
	maxTitle       = 200
	// ntfy turns a longer message into an attachment, or refuses it, at its
	// default size limit of 4,096 bytes, so its body stays below that.
	maxNtfyBody = 4000
	// A public origin is a few hundred bytes at most, since a host name has
	// at most 253 characters. A longer one cannot be a real address, and its
	// link would leave an ntfy message no room for the events.
	maxDashboard = 512
	title        = "RSS Workshop"
	userAgent    = "RSS-Workshop"
)

// Notifier delivers events. Create it with New, call Start, and Close it
// after the scheduler has stopped, so its last events are sent.
type Notifier struct {
	format    string
	endpoint  string
	token     string
	dashboard string
	client    *http.Client
	transport *http.Transport
	backoff   []time.Duration
	window    time.Duration

	queue    chan Event
	urgent   chan Event // server-wide events, which a burst of feed events cannot crowd out
	stop     chan struct{}
	done     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	starting sync.Once
	closing  sync.Once
	started  atomic.Bool

	sent, failed, dropped atomic.Uint64
	unlogged              atomic.Uint64 // dropped since the last log line
	alertDropped          atomic.Bool   // a dropped event needed attention
}

// New checks the settings and builds the HTTP client. Its errors never repeat
// the endpoint or the token. dashboard is the public origin shown as the link
// to the library.
func New(endpoint, format, token, dashboard string) (*Notifier, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return nil, errors.New("NOTIFY_URL must be an absolute http:// or https:// URL with a host and no credentials")
	}
	if format != JSON && format != Ntfy {
		return nil, errors.New("NOTIFY_FORMAT must be json or ntfy")
	}
	if strings.ContainsFunc(token, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return nil, errors.New("NOTIFY_TOKEN must be printable ASCII without spaces")
	}
	if d, err := url.Parse(dashboard); err != nil || (d.Scheme != "http" && d.Scheme != "https") || d.Host == "" || len(dashboard) > maxDashboard {
		dashboard = ""
	}
	tr := &http.Transport{
		// Never an environment proxy: the message would reach a third party,
		// and the operator chose this address.
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: attemptTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   attemptTimeout,
		ResponseHeaderTimeout: attemptTimeout,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          1,
		MaxIdleConnsPerHost:   1,
		MaxConnsPerHost:       2,
		IdleConnTimeout:       90 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Notifier{
		format: format, endpoint: u.String(), token: token, dashboard: dashboard,
		transport: tr,
		client: &http.Client{Transport: tr, Timeout: attemptTimeout,
			// A redirect is a failure rather than a second destination.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		backoff: []time.Duration{2 * time.Second, 4 * time.Second},
		window:  Window,
		queue:   make(chan Event, QueueSize),
		urgent:  make(chan Event, maxGlobal),
		stop:    make(chan struct{}), done: make(chan struct{}),
		ctx: ctx, cancel: cancel,
	}, nil
}

// Kind names the delivery format, or "off" for a nil Notifier, for the startup
// log and the dashboard. It never reveals the endpoint.
func (n *Notifier) Kind() string {
	if n == nil {
		return "off"
	}
	return n.format
}

// Stats reports the messages delivered, the messages that failed after every
// attempt, and the events dropped because the queue was full.
func (n *Notifier) Stats() (sent, failed, dropped uint64) {
	if n == nil {
		return 0, 0, 0
	}
	return n.sent.Load(), n.failed.Load(), n.dropped.Load()
}

// Notify queues an event without waiting. A full queue drops it; the drop is
// counted, logged with the next message and listed in it as one more event.
// Server-wide events have a queue of their own, so feed events that fill
// theirs while the service is slow cannot drop the one that says the
// database is down.
func (n *Notifier) Notify(e Event) {
	if n == nil {
		return
	}
	e.Feed = truncate(oneLine(e.Feed), maxTitle)
	e.FeedID = truncate(oneLine(e.FeedID), maxTitle)
	e.Error = truncate(oneLine(diagnostics.SafeText(e.Error, 4*maxError)), maxError)
	queue := n.queue
	if global(e.Kind) {
		queue = n.urgent
	}
	select {
	case queue <- e:
	default:
		n.dropped.Add(1)
		n.unlogged.Add(1)
		if alerting(e.Kind) {
			n.alertDropped.Store(true)
		}
	}
}

// Start runs the worker that collects and delivers events.
func (n *Notifier) Start() {
	n.starting.Do(func() {
		n.started.Store(true)
		go n.run()
	})
}

// Close sends what is pending at once and returns within FlushGrace, abandoning
// a delivery still in progress then. A nil Notifier has nothing to close.
func (n *Notifier) Close() {
	if n == nil {
		return
	}
	n.closing.Do(func() {
		close(n.stop)
		if n.started.Load() {
			grace := time.NewTimer(FlushGrace)
			select {
			case <-n.done:
			case <-grace.C:
			}
			grace.Stop()
		}
		n.cancel()
		if n.started.Load() {
			<-n.done
		}
		n.transport.CloseIdleConnections()
	})
}

// Test sends a fixed message with no feed data, once, and reports why it failed
// in words that are safe to show.
func (n *Notifier) Test(ctx context.Context) error {
	return n.deliver(ctx, n.compose([]Event{{Kind: Test}}, 0), 1)
}

// batch collects one message's events: up to maxBatch feed events and up to
// maxGlobal server-wide ones, so a window full of feed events still lists the
// database's. Later events are only counted.
type batch struct {
	events  []Event
	globals int  // server-wide events among events
	extra   int  // events past those bounds, counted only
	alert   bool // whether any event added, kept or counted, needs attention
}

func (b *batch) add(e Event) {
	b.alert = b.alert || alerting(e.Kind)
	switch {
	case global(e.Kind) && b.globals < maxGlobal:
		b.globals++
	case !global(e.Kind) && len(b.events)-b.globals < maxBatch:
	default:
		b.extra++
		return
	}
	b.events = append(b.events, e)
}

func (n *Notifier) run() {
	defer close(n.done)
	for {
		var b batch
		select {
		case e := <-n.urgent:
			b.add(e)
		case e := <-n.queue:
			b.add(e)
		case <-n.stop:
			n.flush(&b)
			return
		}
		window := time.NewTimer(n.window)
	collect:
		for {
			select {
			case e := <-n.urgent:
				b.add(e)
			case e := <-n.queue:
				b.add(e)
			case <-window.C:
				break collect
			case <-n.stop:
				window.Stop()
				n.flush(&b)
				return
			}
		}
		n.send(&b)
	}
}

// flush sends whatever is queued when the server stops, without waiting out
// the window. Close cancels the delivery once FlushGrace has passed.
func (n *Notifier) flush(b *batch) {
	for {
		select {
		case e := <-n.urgent:
			b.add(e)
		case e := <-n.queue:
			b.add(e)
		default:
			n.send(b)
			return
		}
	}
}

func (n *Notifier) send(b *batch) {
	if dropped := n.unlogged.Swap(0); dropped > 0 {
		// At most once per message, so at most once per window.
		slog.Warn("notifications dropped", "kind", n.format, "events", dropped, "reason", "queue full")
		b.extra += int(dropped)
	}
	b.alert = n.alertDropped.Swap(false) || b.alert
	if len(b.events) == 0 && b.extra == 0 {
		return
	}
	m := n.compose(b.events, b.extra)
	// An event only counted still decides how urgent the message is.
	m.alert = m.alert || b.alert
	_ = n.deliver(n.ctx, m, maxAttempts)
}

// message is what one request carries.
type message struct {
	text   string
	events []Event // the events the text lists
	more   int     // events counted but not listed
	alert  bool    // anything that needs attention
	test   bool
}

func (m message) count() int { return len(m.events) + m.more }

// global events concern the whole server, so they are listed before any feed.
func global(k Kind) bool { return k != Failing && k != Recovered }

// alerting events report something that needs attention.
func alerting(k Kind) bool { return k == Failing || k == NotSaved || k == DBUnavailable }

func (n *Notifier) compose(events []Event, extra int) message {
	events = slices.Clone(events)
	slices.SortStableFunc(events, func(a, b Event) int {
		switch ga, gb := global(a.Kind), global(b.Kind); {
		case ga && !gb:
			return -1
		case gb && !ga:
			return 1
		}
		return 0
	})
	m := message{}
	// What follows the lines in an ntfy body: the count of those left out,
	// which has at most 20 digits, and the link to the library.
	reserve := len("...and  more.\n") + 20
	if n.dashboard != "" {
		reserve += len("\nLibrary: \n") + len(n.dashboard)
	}
	var b strings.Builder
	for i, e := range events {
		m.alert = m.alert || alerting(e.Kind)
		m.test = m.test || e.Kind == Test
		if m.more > 0 {
			continue
		}
		line := describe(e) + "\n"
		if i == MaxLines || (n.format == Ntfy && b.Len()+len(line) > maxNtfyBody-reserve) {
			m.more = len(events) - i
			continue
		}
		b.WriteString(line)
		m.events = append(m.events, e)
	}
	m.more += extra
	if m.more > 0 {
		fmt.Fprintf(&b, "...and %d more.\n", m.more)
	}
	if n.dashboard != "" {
		fmt.Fprintf(&b, "\nLibrary: %s\n", n.dashboard)
	}
	m.text = strings.TrimSuffix(b.String(), "\n")
	return m
}

func describe(e Event) string {
	reason := ""
	if e.Error != "" {
		reason = " Error: " + e.Error
	}
	switch e.Kind {
	case Failing:
		return fmt.Sprintf("Failing: %s - %s in a row failed.%s", e.Feed, refreshes(e.Failures), reason)
	case Recovered:
		if e.Failures == 0 {
			// A feed reported as failing, repaired in the editor, whose save
			// reset the count.
			return fmt.Sprintf("Recovered: %s - refreshed successfully.", e.Feed)
		}
		return fmt.Sprintf("Recovered: %s - refreshed successfully after %s in a row failed.", e.Feed, refreshes(e.Failures))
	case NotSaved:
		return fmt.Sprintf("Not saved: %s - the database refused this refresh, so nothing from it was stored and the feed waits to retry. Other feeds are likely affected; this alert repeats at most once an hour while results are refused.%s", e.Feed, reason)
	case DBUnavailable:
		return "Database unavailable: the scheduler cannot read the feed library, so no feed refreshes until it recovers. This alert repeats at most once an hour while it lasts." + reason
	case DBAvailable:
		return "Database available again: the scheduler can read the feed library, and refreshes have resumed."
	case Test:
		return "Test notification: RSS Workshop can reach this notification service. Test messages carry no feed data."
	}
	return string(e.Kind)
}

func refreshes(n int) string {
	if n == 1 {
		return "1 refresh"
	}
	return fmt.Sprintf("%d refreshes", n)
}

// deliver posts a message, with up to attempts tries for a failure that may
// pass, and logs the outcome by kind and reason only.
func (n *Notifier) deliver(ctx context.Context, m message, attempts int) error {
	attempt := 1
	var failure *Failure
	for ; ; attempt++ {
		failure = n.post(ctx, m)
		if failure == nil {
			n.sent.Add(1)
			slog.Info("notification sent", "kind", n.format, "events", m.count())
			return nil
		}
		if attempt >= attempts || !failure.retry || ctx.Err() != nil {
			break
		}
		wait := time.NewTimer(n.backoff[min(attempt, len(n.backoff))-1])
		select {
		case <-wait.C:
		case <-ctx.Done():
			wait.Stop()
		}
		if ctx.Err() != nil {
			failure = classify(ctx, ctx.Err())
			break
		}
	}
	n.failed.Add(1)
	attrs := []any{"kind", n.format, "events", m.count(), "attempts", attempt, "reason", failure.Reason}
	if failure.Status != 0 {
		attrs = append(attrs, "status", failure.Status)
	}
	slog.Warn("notification failed", attrs...)
	return failure
}

func (n *Notifier) post(ctx context.Context, m message) *Failure {
	var body []byte
	contentType := "text/plain; charset=utf-8"
	if n.format == JSON {
		events := make([]jsonEvent, len(m.events))
		for i, e := range m.events {
			events[i] = jsonEvent{Event: string(e.Kind), Feed: e.Feed, FeedID: e.FeedID, Failures: e.Failures, Error: e.Error}
		}
		// Gotify-style services read message, and some bridges require body,
		// so both carry the text.
		body, _ = json.Marshal(jsonPayload{Title: title, Message: m.text, Body: m.text, Dashboard: n.dashboard, Events: events, More: m.more})
		contentType = "application/json"
	} else {
		body = []byte(m.text)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.endpoint, bytes.NewReader(body))
	if err != nil {
		return &Failure{Reason: "request"}
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", userAgent)
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}
	if n.format == Ntfy {
		// Fixed values only: no feed title ever goes into a header.
		req.Header.Set("Title", title)
		switch {
		case m.alert:
			req.Header.Set("Priority", "high")
			req.Header.Set("Tags", "warning")
		case m.test:
			req.Header.Set("Priority", "default")
			req.Header.Set("Tags", "bell")
		default:
			req.Header.Set("Priority", "default")
			req.Header.Set("Tags", "white_check_mark")
		}
		if n.dashboard != "" {
			req.Header.Set("Click", n.dashboard)
		}
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return classify(ctx, err)
	}
	defer resp.Body.Close()
	// The answer is not used. Reading a little lets a short one free the
	// connection for reuse; a long one is not read further.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponse))
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return nil
	case code >= 300 && code < 400:
		return &Failure{Status: code, Reason: "redirect"}
	default:
		return &Failure{Status: code, Reason: "status", retry: code == 408 || code == 429 || code >= 500}
	}
}

type jsonPayload struct {
	Title     string      `json:"title"`
	Message   string      `json:"message"`
	Body      string      `json:"body"`
	Dashboard string      `json:"dashboard"`
	Events    []jsonEvent `json:"events"`
	More      int         `json:"more"`
}

type jsonEvent struct {
	Event    string `json:"event"`
	Feed     string `json:"feed"`
	FeedID   string `json:"feed_id"`
	Failures int    `json:"failures"`
	Error    string `json:"error"`
}

// Failure says why a delivery did not succeed, in terms that are safe to log
// and to show an administrator. Go's own errors here are *url.Error values,
// whose text repeats the whole URL, token and all, so none is kept.
type Failure struct {
	// Status is the HTTP status the service answered with, or 0.
	Status int
	// Reason is one of status, redirect, timeout, dns, refused, unreachable,
	// tls, closed, canceled, connection or request.
	Reason string
	retry  bool
}

var reasons = map[string]string{
	"timeout":     "the notification service did not answer in time",
	"dns":         "the host name in NOTIFY_URL does not resolve from RSS Workshop",
	"refused":     "nothing accepts connections at the host and port in NOTIFY_URL",
	"unreachable": "there is no network route to the host in NOTIFY_URL",
	"tls":         "the TLS connection to the notification service failed; check its certificate",
	"closed":      "the notification service closed the connection without a complete answer",
	"canceled":    "the server stopped before the notification was sent",
	"connection":  "the connection to the notification service failed",
	"request":     "the notification request could not be built",
}

func (f *Failure) Error() string {
	switch {
	case f.Reason == "redirect":
		return fmt.Sprintf("the notification service answered HTTP %d, a redirect, which is not followed; use the final address in NOTIFY_URL", f.Status)
	case f.Status != 0:
		return fmt.Sprintf("the notification service answered HTTP %d", f.Status)
	}
	return reasons[f.Reason]
}

// classify reduces a transport error to a fixed reason. Only the error's type
// is examined; its text, which names the URL, is discarded.
func classify(ctx context.Context, err error) *Failure {
	var dns *net.DNSError
	var verification *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var netErr net.Error
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return &Failure{Reason: "canceled"}
	case errors.As(err, &dns):
		return &Failure{Reason: "dns", retry: true}
	case errors.Is(err, syscall.ECONNREFUSED):
		return &Failure{Reason: "refused", retry: true}
	case errors.As(err, &verification), errors.As(err, &record), errors.As(err, &alert),
		errors.As(err, &authority), errors.As(err, &hostname), errors.As(err, &invalid):
		return &Failure{Reason: "tls"}
	case ctx.Err() != nil, errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return &Failure{Reason: "timeout", retry: true}
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return &Failure{Reason: "unreachable", retry: true}
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return &Failure{Reason: "closed", retry: true}
	}
	return &Failure{Reason: "connection", retry: true}
}

// oneLine keeps an operator's label or an error on one line, so it cannot
// pose as a further line of the message. Besides control characters, that
// means the Unicode line and paragraph separators, which are not control
// characters but which clients and forwarded JSON can show as line breaks.
func oneLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "�")))
}

func truncate(s string, limit int) string {
	if len([]rune(s)) <= limit {
		return s
	}
	return string([]rune(s)[:limit-1]) + "…"
}
