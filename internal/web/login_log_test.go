package web

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/scheduler"
)

// POST /api/login is unauthenticated, and the rate limiter rejects attempts past
// its window without checking the password, so those rejections are as cheap as
// opening a connection. Logging one warning per rejected request would let any
// client fill the host's disk. Warnings must stay bounded by the limiter.
func TestRejectedLoginsCannotFloodTheLog(t *testing.T) {
	s, err := openTestStore(t, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	a, err := auth.New("long-test-password", "", false)
	if err != nil {
		t.Fatal(err)
	}
	f, err := fetch.New(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Store: s, Auth: a, BaseURL: "https://reader.example", Scheduler: scheduler.New(s, f, 2, time.Second)}
	h := app.Handler()

	var out bytes.Buffer
	restore := slog.Default()
	defer slog.SetDefault(restore)
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelWarn})))

	const attempts = 500
	for i := 0; i < attempts; i++ {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"wrong"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", app.BaseURL)
		r.RemoteAddr = "203.0.113.7:40000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("attempt %d returned %d", i, w.Code)
		}
	}

	warnings := strings.Count(out.String(), `msg="login rejected"`)
	// The limiter allows ten password checks per minute; everything beyond that
	// is throttled and must not reach warn.
	if warnings > 11 {
		t.Errorf("%d rejected logins produced %d warnings; an unauthenticated client can flood the log", attempts, warnings)
	}
	if warnings == 0 {
		t.Error("a genuinely wrong password produced no warning; the security signal is gone")
	}
	if strings.Contains(out.String(), "wrong") {
		t.Error("the attempted password reached the log")
	}
}
