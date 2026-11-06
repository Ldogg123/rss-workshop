package flaresolverr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
)

type checkURL func(context.Context, string) error

func (p checkURL) CheckURL(ctx context.Context, raw string) error { return p(ctx, raw) }

var permit = checkURL(func(context.Context, string) error { return nil })

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(server.URL, 1, 5*time.Second, permit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, server
}

func solution(raw, body string, status int) map[string]any {
	return map[string]any{"status": "ok", "solution": map[string]any{"url": raw, "status": status, "response": body}}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("write fixture: %v", err)
	}
}

func TestServiceConfiguration(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"http://10.20.30.40:8191", "http://10.20.30.40:8191/v1"},
		{"https://solver.example/v1", "https://solver.example/v1"},
		{"https://solver.example/v1/", "https://solver.example/v1"},
		{"https://solver.example/proxy/", "https://solver.example/proxy/v1"},
	} {
		t.Run(test.input, func(t *testing.T) {
			c, err := New(test.input, 1, time.Minute, permit)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if c.endpoint != test.want || c.transport.Proxy != nil {
				t.Fatalf("endpoint=%q, environment proxy enabled=%v", c.endpoint, c.transport.Proxy != nil)
			}
		})
	}
	for _, endpoint := range []string{"", "10.20.30.40:8191", "ftp://solver.example", "http://user:secret@solver.example", "http://solver.example?token=secret", "http://solver.example?", "http://solver.example/#", "http://solver.example/#fragment", "http://solver.example:bad", "http://"} {
		if c, err := New(endpoint, 1, time.Minute, permit); err == nil {
			c.Close()
			t.Errorf("accepted invalid endpoint %q", endpoint)
		}
	}
	for _, test := range []struct {
		slots   int
		timeout time.Duration
		policy  URLPolicy
	}{{0, time.Minute, permit}, {5, time.Minute, permit}, {1, 4 * time.Second, permit}, {1, 121 * time.Second, permit}, {1, time.Minute, nil}} {
		if c, err := New("http://solver.example", test.slots, test.timeout, test.policy); err == nil {
			c.Close()
			t.Errorf("accepted invalid settings %+v", test)
		}
	}
}

func TestFreshRequestAndValidatedResult(t *testing.T) {
	const source = "https://source.example/news"
	const final = "https://source.example/news/"
	var calls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("incorrect API request: %s %s %s", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 3 || body["cmd"] != "request.get" || body["url"] != source || body["maxTimeout"].(float64) <= 0 || body["maxTimeout"].(float64) > 5000 {
			t.Errorf("incorrect request body: %#v", body)
		}
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" || r.Header.Get("Cookie") != "" {
			t.Error("forwarded browser or conditional state")
		}
		result := solution(final, "<article>Story</article>", 200)
		result["solution"].(map[string]any)["cookies"] = []map[string]string{{"name": "private", "value": "cookie-secret"}}
		result["solution"].(map[string]any)["headers"] = map[string]string{"Set-Cookie": "private=cookie-secret", "ETag": "stale-tag", "Last-Modified": "stale-date"}
		writeJSON(t, w, result)
	})
	var checked []string
	c.policy = checkURL(func(_ context.Context, raw string) error {
		checked = append(checked, raw)
		return nil
	})
	for range 2 {
		out, err := c.Fetch(context.Background(), source, "old-tag", "old-date")
		if err != nil {
			t.Fatal(err)
		}
		if out.Status != 200 || out.URL != final || string(out.Body) != "<article>Story</article>" || out.ETag != "" || out.LastModified != "" {
			t.Fatalf("unexpected result: %+v", out)
		}
	}
	if fmt.Sprint(checked) != fmt.Sprint([]string{source, final, source, final}) || calls.Load() != 2 {
		t.Fatalf("checked=%v calls=%d", checked, calls.Load())
	}
	if active, capacity := c.Stats(); active != 0 || capacity != 1 {
		t.Fatalf("stats=(%d,%d)", active, capacity)
	}
}

func TestSourceAndFinalURLPolicy(t *testing.T) {
	for _, blockInitial := range []bool{true, false} {
		t.Run(fmt.Sprint(blockInitial), func(t *testing.T) {
			var calls atomic.Int32
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				writeJSON(t, w, solution("http://127.0.0.1/private", "private body", 200))
			})
			c.policy = checkURL(func(_ context.Context, raw string) error {
				if blockInitial || strings.Contains(raw, "127.0.0.1") {
					return errors.New("destination blocked by outbound policy")
				}
				return nil
			})
			out, err := c.Fetch(context.Background(), "https://source.example", "", "")
			if err == nil || !strings.Contains(err.Error(), "outbound policy") || len(out.Body) != 0 {
				t.Fatalf("result=%+v error=%v", out, err)
			}
			if blockInitial && calls.Load() != 0 || !blockInitial && calls.Load() != 1 {
				t.Fatalf("API calls=%d", calls.Load())
			}
		})
	}
}

func TestServiceDoesNotFollowHTTPRedirects(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer target.Close()
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	_, err := c.Fetch(context.Background(), "https://source.example", "", "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") || followed.Load() != 0 {
		t.Fatalf("error=%v followed=%d", err, followed.Load())
	}
}

func TestRejectedResponsesAndSafeErrors(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
		status           int
	}{
		{"malformed", `<html>cookie-secret</html>`, "replied, but not as FlareSolverr (HTTP 200)", 200},
		{"trailing JSON", `{"status":"ok"} {}`, "replied, but not as FlareSolverr (HTTP 200)", 200},
		{"unknown status", `{"status":"hmm"}`, "incomplete solution", 200},
		{"missing solution", `{"status":"ok"}`, "incomplete solution", 200},
		{"missing URL", `{"status":"ok","solution":{"status":200,"response":"page"}}`, "incomplete solution", 200},
		{"invalid final URL", `{"status":"ok","solution":{"url":"file:///etc/passwd","status":200,"response":"page"}}`, "invalid final source URL", 200},
		{"missing status", `{"status":"ok","solution":{"url":"https://source.example","response":"page"}}`, "incomplete solution", 200},
		{"missing response", `{"status":"ok","solution":{"url":"https://source.example","status":200}}`, "empty page", 200},
		{"empty response", `{"status":"ok","solution":{"url":"https://source.example","status":200,"response":"  "}}`, "empty page", 200},
		{"captcha", `{"status":"error","message":"Captcha detected but no automatic solver is configured. cookie-secret"}`, "CAPTCHA", 200},
		{"captcha HTTP 500", `{"status":"error","message":"Captcha detected but no automatic solver is configured. cookie-secret"}`, "CAPTCHA", 500},
		{"timeout", `{"status":"error","message":"Error solving the challenge. Timeout after 60 seconds! cookie-secret"}`, "timeout", 200},
		{"browser", `{"status":"error","message":"session not created: Chrome failed to start. cookie-secret"}`, "browser request", 200},
		{"unknown error", `{"status":"error","message":"<body>cookie-secret</body>"}`, "check its service logs", 200},
		{"service HTTP error", `cookie-secret`, "replied, but not as FlareSolverr (HTTP 502)", 502},
		{"not the API", `404 page not found cookie-secret`, "HTTP 404: FLARESOLVERR_URL is not the FlareSolverr /v1 API", 404},
		{"another web app", `<html>cookie-secret</html>`, "replied, but not as FlareSolverr (HTTP 405)", 405},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			})
			out, err := c.Fetch(context.Background(), "https://source.example", "", "")
			if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "cookie-secret") || len(err.Error()) > 240 || len(out.Body) != 0 {
				t.Fatalf("result=%+v error=%v", out, err)
			}
		})
	}
}

func TestOriginHTTPErrorAndRetryAfter(t *testing.T) {
	for _, test := range []struct {
		status int
		header string
		wait   time.Duration
	}{{429, "120", 2 * time.Minute}, {503, "9999999", 24 * time.Hour}, {403, "-10", 0}, {304, "", 0}} {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				result := solution("https://source.example", "error page cookie-secret", test.status)
				result["solution"].(map[string]any)["headers"] = map[string]string{"retry-after": test.header}
				writeJSON(t, w, result)
			})
			out, err := c.Fetch(context.Background(), "https://source.example", "", "")
			var httpErr *fetch.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Status != test.status || httpErr.RetryAfter != test.wait || out.RetryAfter != test.wait || len(out.Body) != 0 {
				t.Fatalf("result=%+v error=%v", out, err)
			}
		})
	}
	if got := retryAfter(time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)); got < 58*time.Second || got > time.Minute {
		t.Fatalf("HTTP date retry-after=%v", got)
	}
}

func TestResponseLimitsIncludingWorstCaseJSONEscaping(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
		raw              bool
	}{
		{"max escaped HTML", strings.Repeat("<", fetch.MaxBody), "", false},
		{"oversized HTML", strings.Repeat("x", fetch.MaxBody+1), "source exceeds 4 MiB", false},
		{"oversized envelope", strings.Repeat("x", maxResponseBody+1), "response exceeds", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if test.raw {
					_, _ = w.Write([]byte(test.body))
					return
				}
				writeJSON(t, w, solution("https://source.example", test.body, 200))
			})
			out, err := c.Fetch(context.Background(), "https://source.example", "", "")
			if test.want == "" {
				if err != nil || string(out.Body) != test.body {
					t.Fatalf("body length=%d error=%v", len(out.Body), err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) || len(out.Body) != 0 {
				t.Fatalf("body length=%d error=%v", len(out.Body), err)
			}
		})
	}
}

func TestBoundedConcurrencyAndQueueCancellation(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		writeJSON(t, w, solution("https://source.example", "page", 200))
	})
	defer close(release)
	finished := make(chan error, 1)
	go func() { _, err := c.Fetch(context.Background(), "https://source.example", "", ""); finished <- err }()
	<-entered
	if active, capacity := c.Stats(); active != 1 || capacity != 1 {
		t.Fatalf("stats=(%d,%d)", active, capacity)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Fetch(ctx, "https://source.example/queued", "", "")
	if !errors.Is(err, context.DeadlineExceeded) || err.Error() != "timed out waiting for a free FlareSolverr slot" {
		t.Fatalf("queued request error=%v", err)
	}
	select {
	case <-entered:
		t.Fatal("queued request reached the API while the slot was occupied")
	default:
	}
	// Close cancels the active request and does not retain cooling slots.
	c.Close()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("closed client request error=%v", err)
	}
	if _, err := c.Fetch(context.Background(), "https://source.example", "", ""); err == nil {
		t.Fatal("closed client accepted a request")
	}
}

func TestInterruptedRequestRetainsSlotUntilRemoteDeadline(t *testing.T) {
	entered := make(chan int64, 1)
	stopHandler := make(chan struct{})
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTimeout int64 `json:"maxTimeout"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		entered <- request.MaxTimeout
		<-stopHandler
	})
	defer close(stopHandler)
	c.grace = 30 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	started := time.Now()
	go func() { _, err := c.Fetch(ctx, "https://source.example", "", ""); finished <- err }()
	remoteTimeout := <-entered
	if remoteTimeout <= 0 || remoteTimeout >= 250 {
		t.Fatalf("remote maxTimeout does not reserve deadline grace: %d", remoteTimeout)
	}
	// An interrupted request may have spent time connecting before the remote
	// browser started. The cooldown must start at failure, not at request start.
	time.Sleep(80 * time.Millisecond)
	interruptedAt := time.Now()
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted request error=%v", err)
	}
	if active, _ := c.Stats(); active != 1 {
		t.Fatal("canceled request released its remote browser slot early")
	}
	for active, _ := c.Stats(); active != 0; active, _ = c.Stats() {
		if time.Since(started) > time.Second {
			t.Fatal("remote deadline did not release its slot")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if time.Since(interruptedAt) < time.Duration(remoteTimeout)*time.Millisecond+c.grace {
		t.Fatal("remote slot released before the full timeout and grace elapsed after interruption")
	}
}

// The endpoint, the source URL and anything Go or the far end said are kept
// out of every message: the text is stored, logged and shown in the editor.
func assertFixedError(t *testing.T, err error, want string, private ...string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) || len(err.Error()) > 240 {
		t.Fatalf("error=%v, want %q", err, want)
	}
	for _, value := range private {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("error %q contains %q", err, value)
		}
	}
}

const privateSource = "https://source.example/private-feed?token=reader-secret"

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "dial timeout detail" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// A request that never opened a connection never reached the remote browser,
// so its slot is free again at once; holding it would only report the solver
// busy to the next preview, when the real problem is its address.
func TestConnectionFailuresAreNamedAndReleaseTheirSlot(t *testing.T) {
	dialFailure := func(err error) func(context.Context, string, string) (net.Conn, error) {
		return func(context.Context, string, string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: err}
		}
	}
	for _, test := range []struct {
		name string
		dial func(context.Context, string, string) (net.Conn, error)
		want string
	}{
		{"DNS lookup", dialFailure(&net.DNSError{Err: "no such host", Name: "solver.internal", IsNotFound: true}), "DNS lookup failed"},
		{"connect timeout", dialFailure(timeoutErr{}), "connection timed out"},
		{"no route", dialFailure(os.NewSyscallError("connect", syscall.EHOSTUNREACH)), "no network route"},
		{"TLS or other", dialFailure(errors.New("tls: handshake failure cookie-secret")), "connection failed before a request was sent"},
		{"deadline while connecting", func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: ctx.Err()}
		}, "connection timed out"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, err := New("http://solver.internal:8191", 1, 5*time.Second, permit)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.transport.DialContext = test.dial
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			_, err = c.Fetch(ctx, privateSource, "", "")
			assertFixedError(t, err, test.want, "solver.internal", "8191", "source.example", "reader-secret", "cookie-secret", "detail")
			if active, _ := c.Stats(); active != 0 || c.Cooling() != 0 {
				t.Fatalf("a request that never connected kept its slot: active=%d cooling=%d", active, c.Cooling())
			}
		})
	}
	t.Run("connection refused", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		listener.Close()
		c, err := New("http://"+address, 1, 5*time.Second, permit)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, err = c.Fetch(context.Background(), privateSource, "", "")
		_, port, _ := net.SplitHostPort(address)
		assertFixedError(t, err, "connection refused", port, "source.example", "reader-secret")
		if active, _ := c.Stats(); active != 0 || c.Cooling() != 0 {
			t.Fatalf("a refused request kept its slot: active=%d cooling=%d", active, c.Cooling())
		}
	})
}

// Once a request may have reached the service, its slot stays held after a
// failure, as before; Cooling tells that hold apart from a running request.
func TestFailuresAfterConnectingAreNamedAndHoldTheirSlot(t *testing.T) {
	// A handler that never answers must still return before the test server
	// closes, which waits for it.
	silent := func(t *testing.T) http.HandlerFunc {
		stop := make(chan struct{})
		t.Cleanup(func() { close(stop) })
		return func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-stop:
			}
		}
	}
	for _, test := range []struct {
		name    string
		handler func(*testing.T) http.HandlerFunc
		want    string
	}{
		{"closed without a reply", func(*testing.T) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				conn, _, err := http.NewResponseController(w).Hijack()
				if err == nil {
					conn.Close()
				}
			}
		}, "closed the connection without a complete reply"},
		{"no reply in time", silent, "did not reply before the deadline"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, server := testClient(t, test.handler(t))
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			_, err := c.Fetch(ctx, privateSource, "", "")
			assertFixedError(t, err, test.want, strings.TrimPrefix(server.URL, "http://"), "source.example", "reader-secret")
			if active, _ := c.Stats(); active != 1 || c.Cooling() != 1 {
				t.Fatalf("slot after a failure past connecting: active=%d cooling=%d", active, c.Cooling())
			}
		})
	}
	c, _ := testClient(t, silent(t))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Fetch(ctx, privateSource, "", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reply timeout no longer matches context.DeadlineExceeded: %v", err)
	}
}
