package flaresolverr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
		{"malformed", `<html>cookie-secret</html>`, "invalid JSON", 200},
		{"trailing JSON", `{"status":"ok"} {}`, "invalid JSON", 200},
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
		{"service HTTP error", `cookie-secret`, "HTTP 502", 502},
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
	if !errors.Is(err, context.DeadlineExceeded) {
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
