package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

const dashboard = "https://rss.example"

// captureLog sends the default logger to a buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	restore := slog.Default()
	t.Cleanup(func() { slog.SetDefault(restore) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &out
}

// received is one request a test endpoint saw.
type received struct {
	at     time.Time
	method string
	path   string
	header http.Header
	body   []byte
}

// endpoint answers each request with respond, by attempt number from 1, and
// records it. It is an http.RoundTripper, so a notifier can use it inside a
// synctest bubble, where real sockets would stop the fake clock.
type endpoint struct {
	mu       sync.Mutex
	requests []received
	respond  func(attempt int, r *http.Request) (*http.Response, error)
}

func (e *endpoint) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	e.requests = append(e.requests, received{at: time.Now(), method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
	attempt := len(e.requests)
	e.mu.Unlock()
	if e.respond == nil {
		return status(r, 200), nil
	}
	return e.respond(attempt, r)
}

func (e *endpoint) seen() []received {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]received(nil), e.requests...)
}

func status(r *http.Request, code int) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}
}

func newNotifier(t *testing.T, rawURL, format, token string) *Notifier {
	t.Helper()
	n, err := New(rawURL, format, token, dashboard)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// fake builds a notifier whose requests go to e instead of the network.
func fake(t *testing.T, format string, e *endpoint) *Notifier {
	t.Helper()
	n := newNotifier(t, "https://notify.example/topic", format, "")
	n.client.Transport = e
	return n
}

func failing(feed string, failures int) Event {
	return Event{Kind: Failing, Feed: feed, FeedID: "id-" + feed, Failures: failures, Error: "source returned HTTP 503"}
}

// ntfy reads the title, priority and tags from headers and the message from
// the body. Only fixed values go in headers: a feed title can hold a line
// break, which would add a header of its own.
func TestNtfyRequest(t *testing.T) {
	var got []received
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, received{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
		mu.Unlock()
	}))
	defer server.Close()
	n := newNotifier(t, server.URL+"/rss-alerts", Ntfy, "tk_fixture")
	n.Start()
	injected := "Evening news\r\nX-Injected: yes"
	n.Notify(failing(injected, 3))
	n.Notify(Event{Kind: Recovered, Feed: "Morning briefing", FeedID: "b", Failures: 4})
	// Unicode's line and paragraph separators start a line in clients that
	// follow its line breaking rules, so a title cannot use them to add lines
	// of its own to a real alert.
	n.Notify(failing("Weekend guide\u2028Database available again\u2029Recovered: Bank alerts", 3))
	// Close sends what is pending without waiting out the window.
	n.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("got %d requests; want both events in one", len(got))
	}
	r := got[0]
	want := map[string]string{"Title": "RSS Workshop", "Priority": "high", "Tags": "warning", "Click": dashboard,
		"Authorization": "Bearer tk_fixture", "Content-Type": "text/plain; charset=utf-8"}
	for header, value := range want {
		if r.header.Get(header) != value {
			t.Errorf("%s = %q, want %q", header, r.header.Get(header), value)
		}
	}
	if r.method != "POST" || r.path != "/rss-alerts" {
		t.Errorf("request %s %s", r.method, r.path)
	}
	for name, values := range r.header {
		if strings.Contains(strings.Join(values, " "), "Evening") || strings.EqualFold(name, "X-Injected") {
			t.Errorf("a feed title reached the header %s", name)
		}
	}
	body := string(r.body)
	for _, line := range []string{
		"Failing: Evening news  X-Injected: yes - 3 refreshes in a row failed. Error: source returned HTTP 503",
		"Recovered: Morning briefing - refreshed successfully after 4 refreshes in a row failed.",
		"Failing: Weekend guide Database available again Recovered: Bank alerts - 3 refreshes in a row failed.",
		"Library: " + dashboard,
	} {
		if !strings.Contains(body, line) {
			t.Errorf("body lacks %q:\n%s", line, body)
		}
	}
	if strings.ContainsAny(body, "\u2028\u2029") {
		t.Errorf("a separator from a title reached the body: %q", body)
	}
}

// The JSON payload is the documented schema, with message and body carrying
// the same text, and errors redacted and shortened as the card shows them.
func TestJSONPayload(t *testing.T) {
	var got [][]byte
	var header http.Header
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, body)
		header = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer server.Close()
	n := newNotifier(t, server.URL+"/hook", JSON, "")
	n.Start()
	long := "fetch failed at https://source.example/private?key=1 " + strings.Repeat("x", 600)
	n.Notify(Event{Kind: Failing, Feed: "Line\nbreak\u2029here", FeedID: "feed-1", Failures: 1, Error: long})
	n.Notify(Event{Kind: DBUnavailable, Error: "database is locked"})
	n.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("got %d requests", len(got))
	}
	if header.Get("Content-Type") != "application/json" || header.Get("Authorization") != "" || header.Get("Title") != "" {
		t.Errorf("headers %v", header)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(got[0], &raw); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for key := range raw {
		keys = append(keys, key)
	}
	if want := []string{"body", "dashboard", "events", "message", "more", "title"}; !sameSet(keys, want) {
		t.Errorf("payload keys %v, want %v", keys, want)
	}
	var payload struct {
		Title, Message, Body, Dashboard string
		More                            int
		Events                          []map[string]any
	}
	if err := json.Unmarshal(got[0], &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Title != "RSS Workshop" || payload.Message == "" || payload.Message != payload.Body || payload.Dashboard != dashboard || payload.More != 0 {
		t.Errorf("payload %+v", payload)
	}
	if len(payload.Events) != 2 {
		t.Fatalf("events %v", payload.Events)
	}
	// The database event concerns every feed, so it comes first.
	database, feed := payload.Events[0], payload.Events[1]
	if database["event"] != "db_unavailable" || database["feed"] != "" || database["feed_id"] != "" || database["error"] != "database is locked" {
		t.Errorf("database event %v", database)
	}
	if feed["event"] != "failing" || feed["feed"] != "Line break here" || feed["feed_id"] != "feed-1" || feed["failures"] != float64(1) {
		t.Errorf("feed event %v", feed)
	}
	for _, event := range payload.Events {
		if want := []string{"error", "event", "failures", "feed", "feed_id"}; !sameSet(mapKeys(event), want) {
			t.Errorf("event keys %v, want %v", mapKeys(event), want)
		}
	}
	reason := feed["error"].(string)
	if strings.Contains(reason, "source.example") || !strings.Contains(reason, "[URL omitted]") || len([]rune(reason)) > maxError {
		t.Errorf("error was not redacted and shortened (%d characters): %q", len([]rune(reason)), reason)
	}
	if strings.Contains(payload.Message, "source.example") || strings.Contains(payload.Message, "Line\nbreak") {
		t.Errorf("message %q", payload.Message)
	}
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, v := range got {
		seen[v] = true
	}
	for _, v := range want {
		if !seen[v] {
			return false
		}
	}
	return true
}

func mapKeys(m map[string]any) []string {
	keys := []string{}
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

// A network outage fails every feed at once. One message per window keeps
// that to one notification, listing 20 feeds and counting the rest.
func TestEventsAreCoalescedIntoOneMessagePerWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := &endpoint{}
		n := fake(t, JSON, e)
		n.Start()
		defer n.Close()
		start := time.Now()
		for i := range 25 {
			n.Notify(failing(string(rune('A'+i)), 3))
		}
		time.Sleep(30 * time.Second)
		n.Notify(failing("Late", 3))
		time.Sleep(Window - 30*time.Second - time.Millisecond)
		synctest.Wait()
		if len(e.seen()) != 0 {
			t.Fatal("a message went out before the window closed")
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		seen := e.seen()
		if len(seen) != 1 || seen[0].at.Sub(start) != Window {
			t.Fatalf("got %d messages; want one at the end of the window", len(seen))
		}
		var payload struct {
			Message string
			More    int
			Events  []struct{ Feed string }
		}
		if err := json.Unmarshal(seen[0].body, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Events) != MaxLines || payload.More != 6 || payload.Events[0].Feed != "A" {
			t.Errorf("listed %d events and %d more; want %d and 6", len(payload.Events), payload.More, MaxLines)
		}
		if lines := strings.Count(payload.Message, "Failing: "); lines != MaxLines || !strings.Contains(payload.Message, "...and 6 more.") {
			t.Errorf("message has %d feed lines:\n%s", lines, payload.Message)
		}

		// The next event starts a new window of its own.
		n.Notify(failing("Next", 3))
		time.Sleep(Window)
		synctest.Wait()
		if seen := e.seen(); len(seen) != 2 || !bytes.Contains(seen[1].body, []byte(`"feed":"Next"`)) {
			t.Fatalf("the next event was not sent one window later: %d messages", len(seen))
		}
	})
}

// Notify runs on refresh goroutines, so it must never wait for the worker. A
// full queue drops the event, counts it, logs it once with the next message,
// and the message counts it among the events it does not list.
func TestFullQueueDropsWithoutBlocking(t *testing.T) {
	logs := captureLog(t)
	synctest.Test(t, func(t *testing.T) {
		e := &endpoint{}
		n := fake(t, JSON, e)
		// Not started yet, so nothing drains the queue.
		for i := range QueueSize + 50 {
			n.Notify(failing(string(rune('a'+i%26)), 3))
		}
		if _, _, dropped := n.Stats(); dropped != 50 {
			t.Fatalf("dropped %d events; want 50", dropped)
		}
		n.Start()
		time.Sleep(Window)
		synctest.Wait()
		seen := e.seen()
		if len(seen) != 1 {
			t.Fatalf("got %d messages", len(seen))
		}
		var payload struct {
			More   int
			Events []any
		}
		if err := json.Unmarshal(seen[0].body, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Events) != MaxLines || payload.More != QueueSize+50-MaxLines {
			t.Errorf("listed %d and counted %d more; want %d and %d", len(payload.Events), payload.More, MaxLines, QueueSize+50-MaxLines)
		}
		n.Close()
	})
	if lines := strings.Count(logs.String(), `msg="notifications dropped"`); lines != 1 || !strings.Contains(logs.String(), "events=50") {
		t.Errorf("want one drop line counting 50 events:\n%s", logs.String())
	}
}

// One window keeps at most 256 feed events however many arrive, as the
// operations guide documents, and counts the rest. Server-wide events are kept
// beyond that, so the one that says the database is down is always listed,
// and the priority reflects every event, listed or only counted.
func TestBatchBoundsFeedEventsButKeepsDatabaseEvents(t *testing.T) {
	var b batch
	for range 256 + 7 {
		b.add(Event{Kind: Recovered, Feed: "Filler", Failures: 3})
	}
	b.add(Event{Kind: Failing, Feed: "Counted only", Failures: 3})
	if b.extra != 7+1 || !b.alert {
		t.Fatalf("a failure only counted did not raise the priority: extra=%d alert=%v", b.extra, b.alert)
	}
	b.add(Event{Kind: DBUnavailable, Error: "database is locked"})
	if len(b.events) != 256+1 || b.extra != 7+1 {
		t.Fatalf("kept %d events and counted %d; want 257 and 8", len(b.events), b.extra)
	}
	if b.events[256].Kind != DBUnavailable || !b.alert {
		t.Fatalf("the database event was not kept, or the alert was lost: %+v alert=%v", b.events[256], b.alert)
	}
	// It arrived after the cap, and the message still lists it first.
	m := (&Notifier{format: JSON}).compose(b.events, b.extra)
	if len(m.events) != MaxLines || m.events[0].Kind != DBUnavailable || !strings.HasPrefix(m.text, "Database unavailable: ") || m.more != 256+1+7+1-MaxLines {
		t.Fatalf("the message lists %d events, first %q, and counts %d more:\n%.200s", len(m.events), m.events[0].Kind, m.more, m.text)
	}
	// The scheduler sends a few server-wide events an hour, so a bound far
	// above that still applies to them.
	for range 100 {
		b.add(Event{Kind: NotSaved, Feed: "x"})
	}
	if len(b.events) != 256+maxGlobal || b.extra != 7+1+100-(maxGlobal-1) {
		t.Fatalf("kept %d events and counted %d", len(b.events), b.extra)
	}
}

// A message lists the database events first and raises the ntfy priority
// for them, even when a large library's feed events fill the window or the
// queue before they arrive.
func TestDatabaseEventsSurviveABusyWindow(t *testing.T) {
	recovered := Event{Kind: Recovered, Feed: "Filler", Failures: 3}
	for _, tc := range []struct {
		name    string
		started bool // the worker drains the queue as events arrive
		feeds   int
		then    Event
		first   string
		dropped uint64
	}{
		// More feed events than one window keeps, as when a large library
		// recovers together, then the database goes down.
		{"past the window's limit", true, 266, Event{Kind: DBUnavailable, Error: "database is locked"}, "Database unavailable: ", 0},
		// A slow service lets the queue fill before the database event.
		{"full queue", false, QueueSize + 10, Event{Kind: DBUnavailable, Error: "database is locked"}, "Database unavailable: ", 10},
		{"full queue, not saved", false, QueueSize, Event{Kind: NotSaved, Feed: "Alpha", Error: "disk full"}, "Not saved: Alpha", 0},
		// A failing event dropped from a full queue cannot be listed, but the
		// message still says something failed.
		{"dropped failure", false, QueueSize, failing("Dropped", 3), "Recovered: Filler", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := &endpoint{}
				n := fake(t, Ntfy, e)
				defer n.Close()
				if tc.started {
					n.Start()
				}
				for i := range tc.feeds {
					n.Notify(recovered)
					if tc.started && i%64 == 63 {
						synctest.Wait()
					}
				}
				synctest.Wait()
				n.Notify(tc.then)
				n.Start()
				time.Sleep(Window)
				synctest.Wait()
				seen := e.seen()
				if len(seen) != 1 {
					t.Fatalf("got %d messages", len(seen))
				}
				body := string(seen[0].body)
				if !strings.HasPrefix(body, tc.first) {
					t.Errorf("the message starts %.60q; want %q", body, tc.first)
				}
				if p, tags := seen[0].header.Get("Priority"), seen[0].header.Get("Tags"); p != "high" || tags != "warning" {
					t.Errorf("Priority %q, Tags %q; want high and warning", p, tags)
				}
				if _, _, dropped := n.Stats(); dropped != tc.dropped {
					t.Errorf("dropped %d events; want %d", dropped, tc.dropped)
				}
			})
		})
	}
}

// A message that fails in a way that may pass is tried three times in all,
// two and then four seconds apart; one the service refuses outright is not
// repeated.
func TestRetriesBackOffAndStop(t *testing.T) {
	for _, tc := range []struct {
		name     string
		respond  func(attempt int, r *http.Request) (*http.Response, error)
		attempts int
		sent     bool
	}{
		{"server error", func(_ int, r *http.Request) (*http.Response, error) { return status(r, 503), nil }, 3, false},
		{"refused", func(int, *http.Request) (*http.Response, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		}, 3, false},
		{"recovers", func(attempt int, r *http.Request) (*http.Response, error) {
			if attempt < 3 {
				return status(r, 503), nil
			}
			return status(r, 200), nil
		}, 3, true},
		{"unauthorized", func(_ int, r *http.Request) (*http.Response, error) { return status(r, 401), nil }, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			synctest.Test(t, func(t *testing.T) {
				e := &endpoint{respond: tc.respond}
				n := fake(t, Ntfy, e)
				n.Start()
				start := time.Now()
				n.Notify(failing("Feed", 3))
				time.Sleep(time.Hour)
				synctest.Wait()
				seen := e.seen()
				if len(seen) != tc.attempts {
					t.Fatalf("made %d attempts; want %d", len(seen), tc.attempts)
				}
				for i, offset := range []time.Duration{Window, Window + 2*time.Second, Window + 6*time.Second}[:tc.attempts] {
					if got := seen[i].at.Sub(start); got != offset {
						t.Errorf("attempt %d at %v; want %v", i+1, got, offset)
					}
				}
				sent, failed, _ := n.Stats()
				if (sent == 1) != tc.sent || (failed == 1) == tc.sent {
					t.Errorf("sent=%d failed=%d", sent, failed)
				}
				n.Close()
			})
			if !tc.sent && !strings.Contains(logs.String(), `msg="notification failed"`) {
				t.Errorf("no failure line:\n%s", logs)
			}
		})
	}
}

// A redirect is a failure: following it would send the message, and the
// token, to an address the operator did not configure.
func TestRedirectIsNotFollowed(t *testing.T) {
	var elsewhere atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
	}))
	defer server.Close()
	n := newNotifier(t, server.URL+"/topic", Ntfy, "tk_fixture")
	defer n.Close()
	var failure *Failure
	if err := n.Test(context.Background()); !errors.As(err, &failure) || failure.Status != 302 || failure.Reason != "redirect" {
		t.Fatalf("a redirect was reported as %v", err)
	}
	if elsewhere.Load() != 0 {
		t.Fatal("the redirect was followed")
	}
}

// Environment proxies are for sources, which the guarded fetcher checks; a
// message through one would hand feed titles and the token to a third party.
func TestEnvironmentProxyIsIgnored(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxied.Add(1) }))
	defer proxy.Close()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY"} {
		t.Setenv(key, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	// Not a loopback address, which Go never proxies, and one that does not
	// resolve, so only a proxy could answer it.
	n := newNotifier(t, "http://notify.invalid/topic", Ntfy, "")
	defer n.Close()
	if n.transport.Proxy != nil {
		t.Error("the transport consults an environment proxy")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := n.Test(ctx); err == nil {
		t.Error("an unresolvable address was reported as reached")
	}
	if proxied.Load() != 0 {
		t.Fatal("the message went through the environment proxy")
	}
}

// The answer is not used, so only a little of it is read: a service that
// streams a long one must not hold the worker until the request times out.
func TestResponseIsReadOnlyBriefly(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write(bytes.Repeat([]byte("x"), 2*maxResponse))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	n := newNotifier(t, server.URL+"/topic", Ntfy, "")
	defer n.Close()
	start := time.Now()
	if err := n.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > attemptTimeout/2 {
		t.Fatalf("delivery waited %v for the rest of the answer", elapsed)
	}
}

// stalledBody is an answer that stops after its headers.
type stalledBody struct{ ctx context.Context }

func (b stalledBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (stalledBody) Close() error { return nil }

// There is one worker, so a service that accepts a request and then stalls,
// before its answer or in the middle of it, would hold every later message
// until shutdown while the queue filled and dropped alerts. Each attempt ends
// after attemptTimeout, with the client New builds.
func TestEachAttemptIsBoundedInTime(t *testing.T) {
	logs := captureLog(t)
	synctest.Test(t, func(t *testing.T) {
		silent := &endpoint{respond: func(_ int, r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}}
		n := fake(t, Ntfy, silent)
		defer n.Close()
		n.Start()
		start := time.Now()
		n.Notify(failing("Feed", 3))
		time.Sleep(time.Hour)
		synctest.Wait()
		seen := silent.seen()
		want := []time.Duration{Window, Window + attemptTimeout + 2*time.Second, Window + 2*attemptTimeout + 6*time.Second}
		if len(seen) != len(want) {
			t.Fatalf("made %d attempts; want %d, each ending after %v", len(seen), len(want), attemptTimeout)
		}
		for i, offset := range want {
			if got := seen[i].at.Sub(start); got != offset {
				t.Errorf("attempt %d at %v; want %v", i+1, got, offset)
			}
		}
		if _, failed, _ := n.Stats(); failed != 1 {
			t.Errorf("failed=%d; want the message counted as failed", failed)
		}
		n.Close()

		stalled := &endpoint{respond: func(_ int, r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: stalledBody{r.Context()}, Request: r}, nil
		}}
		n = fake(t, Ntfy, stalled)
		defer n.Close()
		start = time.Now()
		n.Test(context.Background())
		if elapsed := time.Since(start); elapsed != attemptTimeout {
			t.Errorf("an answer that stalled after its headers held delivery for %v; want %v", elapsed, attemptTimeout)
		}
	})
	if !strings.Contains(logs.String(), `msg="notification failed" kind=ntfy events=1 attempts=3 reason=timeout`) {
		t.Errorf("no timeout line:\n%s", logs)
	}
}

// ntfy turns a message past its default limit of 4,096 bytes into an
// attachment, or refuses it. Twenty lines with the longest titles and errors
// Notify keeps would pass that several times over, so an ntfy message lists
// the lines that fit and counts the rest, within the 4,000 bytes the
// operations guide promises. That holds with the longest public address a
// host name allows, whose link ends every message; an address longer than
// any host name is left out rather than crowding out the events.
func TestNtfyMessageStaysWithinItsSizeLimit(t *testing.T) {
	// 253 characters, the most a DNS name may have.
	host := strings.Repeat(strings.Repeat("h", 62)+".", 4) + "net"
	for _, tc := range []struct {
		name, dashboard, link string
	}{
		{"short address", dashboard, dashboard},
		{"longest host name", "https://" + host + ":65535", "https://" + host + ":65535"},
		{"longer than a host name", "https://" + strings.Repeat("h", 5000) + ".net", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := &endpoint{}
				n, err := New("https://notify.example/topic", Ntfy, "", tc.dashboard)
				if err != nil {
					t.Fatal(err)
				}
				n.client.Transport = e
				defer n.Close()
				// More than the queue holds, so the count of what is left out
				// includes drops and runs to four digits.
				for i := range QueueSize + 1000 {
					// Longer than Notify keeps, in characters of two and three bytes.
					n.Notify(Event{Kind: Failing, Feed: strings.Repeat(string(rune('A'+i%26))+"é", maxTitle), FeedID: "id",
						Failures: 3, Error: strings.Repeat("界", 2*maxError)})
				}
				n.Start()
				time.Sleep(Window)
				synctest.Wait()
				seen := e.seen()
				if len(seen) != 1 {
					t.Fatalf("got %d messages", len(seen))
				}
				body := string(seen[0].body)
				lines := strings.Count(body, "Failing: ")
				if len(body) > 4000 || lines == 0 {
					t.Fatalf("a %d-byte message with %d lines; want at most 4,000 bytes and at least one line", len(body), lines)
				}
				if !strings.Contains(body, "...and "+strconv.Itoa(QueueSize+1000-lines)+" more.") {
					t.Errorf("the message does not count what it leaves out:\n%s", body)
				}
				if click := seen[0].header.Get("Click"); click != tc.link {
					t.Errorf("Click = %q; want %q", click, tc.link)
				}
				if tc.link != "" && !strings.HasSuffix(body, "Library: "+tc.link) || tc.link == "" && strings.Contains(body, "Library:") {
					t.Errorf("the message does not end with the right link:\n%s", body)
				}
			})
		})
	}
}

// Lines that fill an ntfy message almost to its limit leave room for the
// count of the events left out only through what compose reserves for it:
// the count's fixed text, and its digits.
func TestNtfyMessageKeepsRoomForTheCount(t *testing.T) {
	// The bytes of a line other than its title.
	fixed := len(describe(Event{Kind: Failing, Failures: 3})) + 1
	for _, tc := range []struct {
		name   string
		events int
		widths []int // the first lines' lengths in bytes; the rest have 200
	}{
		// MaxLines lines of 4,000 bytes in all, and one more, so the count
		// has a single digit.
		{"one digit", MaxLines + 1, nil},
		// 19 lines of 3,985 bytes, and more events than the queue holds, so
		// the count runs to four digits and needs 18 bytes: 3 more than its
		// fixed text alone would leave.
		{"four digits", QueueSize + 1044, append(slices.Repeat([]int{210}, 18), 205)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := &endpoint{}
				n, err := New("https://notify.example/topic", Ntfy, "", "")
				if err != nil {
					t.Fatal(err)
				}
				n.client.Transport = e
				defer n.Close()
				for i := range tc.events {
					width := 200
					if i < len(tc.widths) {
						width = tc.widths[i]
					}
					n.Notify(Event{Kind: Failing, Feed: strings.Repeat("x", width-fixed), FeedID: "id", Failures: 3})
				}
				n.Start()
				time.Sleep(Window)
				synctest.Wait()
				seen := e.seen()
				if len(seen) != 1 {
					t.Fatalf("got %d messages", len(seen))
				}
				body := string(seen[0].body)
				lines := strings.Count(body, "Failing: ")
				if len(body) > 4000 || !strings.HasSuffix(body, "\n...and "+strconv.Itoa(tc.events-lines)+" more.") {
					t.Fatalf("a %d-byte message with %d lines; want at most 4,000 bytes, ending with the count:\n%s", len(body), lines, body[max(0, len(body)-80):])
				}
			})
		})
	}
}

// Shutdown sends what is pending at once, and gives up after FlushGrace
// however slow the service is.
func TestCloseFlushesWithinTheGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fast := &endpoint{}
		n := fake(t, JSON, fast)
		n.Start()
		n.Notify(failing("Pending", 3))
		start := time.Now()
		n.Close()
		if elapsed := time.Since(start); elapsed != 0 || len(fast.seen()) != 1 {
			t.Fatalf("closing took %v and sent %d messages; want the pending event sent at once", elapsed, len(fast.seen()))
		}

		// Server-wide events wait in a queue of their own, which the flush
		// empties too. The worker may take either queue, or the stop, first,
		// so this repeats to cover each order.
		for range 20 {
			both := &endpoint{}
			n = fake(t, JSON, both)
			n.Notify(Event{Kind: DBAvailable})
			n.Notify(failing("Pending", 3))
			n.Start()
			n.Close()
			if seen := both.seen(); len(seen) != 1 || !bytes.Contains(seen[0].body, []byte("Database available again")) || !bytes.Contains(seen[0].body, []byte("Failing: Pending")) {
				t.Fatalf("closing sent %d messages; want one with both pending events", len(seen))
			}
		}

		hung := &endpoint{respond: func(_ int, r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}}
		n = fake(t, JSON, hung)
		n.Start()
		n.Notify(failing("Pending", 3))
		start = time.Now()
		n.Close()
		if elapsed := time.Since(start); elapsed != FlushGrace || len(hung.seen()) != 1 {
			t.Fatalf("closing took %v with %d attempts; want %v and one attempt", elapsed, len(hung.seen()), FlushGrace)
		}
	})
}

// Go's errors for a failed request quote the whole URL, and NOTIFY_URL can
// carry a token in its path or query. Whatever the failure, neither the URL
// nor any part of it, nor NOTIFY_TOKEN, may reach the log or the error shown
// to an administrator.
func TestDeliveryFailuresNeverRevealTheEndpoint(t *testing.T) {
	const path, query, token = "/secret-topic-path", "auth=secret-query-value", "secret-bearer-token"
	status500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer status500.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example"+path+"?"+query, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reading the body lets the server notice when the client hangs up.
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer slow.Close()
	tlsServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	// The rejected handshake is logged by the server through the standard
	// logger, which captureLog also receives, from a goroutine of its own.
	tlsServer.Config.ErrorLog = log.New(io.Discard, "", 0)
	tlsServer.StartTLS()
	defer tlsServer.Close()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := "http://" + closed.Addr().String()
	closed.Close()

	for _, tc := range []struct {
		name, base, reason string
		worker             bool
	}{
		{"status", status500.URL, "status", true},
		{"redirect", redirect.URL, "redirect", false},
		{"timeout", slow.URL, "timeout", false},
		{"dns", "http://secret-host.invalid", "dns", false},
		{"refused", refused, "refused", true},
		{"tls", tlsServer.URL, "tls", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			endpoint := tc.base + path + "?" + query
			n := newNotifier(t, endpoint, Ntfy, token)
			n.client.Timeout = 200 * time.Millisecond
			n.backoff = []time.Duration{time.Millisecond, time.Millisecond}
			var shown string
			if tc.worker {
				// Through the worker, with its retries.
				n.Start()
				n.Notify(failing("Feed", 3))
				n.Close()
			} else {
				err := n.Test(context.Background())
				var failure *Failure
				if !errors.As(err, &failure) || failure.Reason != tc.reason {
					t.Fatalf("classified as %#v; want %s", err, tc.reason)
				}
				shown = err.Error()
				n.Close()
			}
			out := logs.String()
			if !strings.Contains(out, `msg="notification failed"`) || !strings.Contains(out, "reason="+tc.reason) {
				t.Errorf("no classified failure line:\n%s", out)
			}
			for _, secret := range []string{endpoint, tc.base, path, "secret-topic-path", query, "secret-query-value", token, "secret-host", "elsewhere.example"} {
				if strings.Contains(out, secret) {
					t.Errorf("the log contains %q:\n%s", secret, out)
				}
				if strings.Contains(shown, secret) {
					t.Errorf("the error contains %q: %s", secret, shown)
				}
			}
		})
	}
}

// Kind and Stats are read for every dashboard poll and scrape, including
// when notifications are off.
func TestOffNotifierIsSafeToUse(t *testing.T) {
	var n *Notifier
	if n.Kind() != "off" {
		t.Errorf("kind %q", n.Kind())
	}
	if sent, failed, dropped := n.Stats(); sent+failed+dropped != 0 {
		t.Error("an absent notifier reports activity")
	}
	n.Notify(failing("Feed", 3))
	n.Close()
}

func TestNewRejectsWithoutRepeatingSecrets(t *testing.T) {
	for _, raw := range []string{"ftp://hooks.example/secret-path", "https://user:secret-pass@hooks.example/x", "/secret-path", "http://hooks.example/%zzsecret"} {
		_, err := New(raw, JSON, "", dashboard)
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "hooks.example") {
			t.Errorf("%s: %v", raw, err)
		}
	}
	if _, err := New("https://hooks.example/x", JSON, "secret-token\r\nX: y", dashboard); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("token with a line break: %v", err)
	}
	if _, err := New("https://hooks.example/x", "slack", "", dashboard); err == nil {
		t.Error("accepted an unknown format")
	}
}

// The text of each event, which the JSON message and body and the ntfy body
// share.
func TestEventDescriptions(t *testing.T) {
	n := newNotifier(t, "https://hooks.example/x", JSON, "")
	for _, tc := range []struct {
		event Event
		want  string
	}{
		{Event{Kind: Failing, Feed: "News", Failures: 1, Error: "boom"}, "Failing: News - 1 refresh in a row failed. Error: boom"},
		{Event{Kind: Recovered, Feed: "News", Failures: 3}, "Recovered: News - refreshed successfully after 3 refreshes in a row failed."},
		// After an editor save reset the count.
		{Event{Kind: Recovered, Feed: "News"}, "Recovered: News - refreshed successfully.\n"},
		{Event{Kind: NotSaved, Feed: "News", Error: "disk full"}, "Not saved: News - the database refused this refresh"},
		{Event{Kind: DBUnavailable, Error: "database is locked"}, "Database unavailable: the scheduler cannot read the feed library"},
		{Event{Kind: DBAvailable}, "Database available again"},
		{Event{Kind: Test}, "Test notification: RSS Workshop can reach this notification service."},
	} {
		m := n.compose([]Event{tc.event}, 0)
		if !strings.HasPrefix(m.text, tc.want) || !reflect.DeepEqual(m.events, []Event{tc.event}) {
			t.Errorf("%s: %q", tc.event.Kind, m.text)
		}
	}
}
