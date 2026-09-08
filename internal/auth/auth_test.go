package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestPlaintextPasswordsCheckEveryByte(t *testing.T) {
	for _, tc := range []struct {
		name, password string
		wrong          []string
	}{
		{"short", "x", []string{"", "X", "xx"}},
		{"long ASCII", strings.Repeat("a", 80) + "right", []string{strings.Repeat("a", 80) + "wrong", strings.Repeat("a", 72)}},
		{"Unicode", strings.Repeat("密", 24) + "é🔑", []string{strings.Repeat("密", 24) + "é🔒", strings.Repeat("密", 24) + "e\u0301🔑", strings.Repeat("密", 24)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := New(tc.password, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if cost, err := bcrypt.Cost(a.hash); err != nil || cost != 12 {
				t.Fatalf("plaintext hashing cost = %d, error = %v", cost, err)
			}
			if _, err := a.Login(tc.password); err != nil {
				t.Fatalf("exact password rejected: %v", err)
			}
			for i, wrong := range tc.wrong {
				if _, err := a.Login(wrong); err == nil {
					t.Errorf("incorrect password variant %d accepted", i)
				}
			}
		})
	}
}

func externalAuth(t *testing.T, password string, secure bool) *Auth {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New("ignored plaintext setting", string(hash), secure)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestExternalBcryptCompatibilityAndPrecedence(t *testing.T) {
	password := strings.Repeat("x", 72)
	a := externalAuth(t, password, false)
	if a.pepper != nil {
		t.Fatal("external bcrypt hash must retain standard verification")
	}
	if _, err := a.Login(password); err != nil {
		t.Fatalf("standard bcrypt password rejected: %v", err)
	}
	for i, wrong := range []string{password + "extra", password[:71], "ignored plaintext setting", ""} {
		if _, err := a.Login(wrong); err == nil {
			t.Errorf("external hash accepted incorrect candidate %d", i)
		}
	}
	for _, malformed := range []string{"not-a-bcrypt-hash", "$2a$12$short", "$2a$99$" + strings.Repeat("a", 53)} {
		if _, err := New("valid plaintext fallback", malformed, false); err == nil || err.Error() != "invalid ADMIN_PASSWORD_HASH" {
			t.Fatalf("malformed external hash must fail without a plaintext fallback: %v", err)
		}
	}
}

func TestPlaintextRestartCreatesIndependentHashingState(t *testing.T) {
	const password = "a short password"
	first, err := New(password, "", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(password, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.pepper) != 32 || len(second.pepper) != 32 || bytes.Equal(first.pepper, second.pepper) {
		t.Fatal("each instance must use an independent random pepper")
	}
	if bytes.Equal(first.passwordInput(password), second.passwordInput(password)) {
		t.Fatal("separate instances reused the same bcrypt input")
	}
	if _, err := second.Login(password); err != nil {
		t.Fatalf("same configured password must work after restart: %v", err)
	}
}

func TestSessionLifecycleAndCookies(t *testing.T) {
	a := externalAuth(t, "p", true)
	before := time.Now()
	session, err := a.Login("p")
	if err != nil {
		t.Fatal(err)
	}
	if session.Token == "" || session.CSRF == "" || session.Token == session.CSRF || session.Expires.Before(before.Add(12*time.Hour)) || session.Expires.After(time.Now().Add(12*time.Hour)) {
		t.Fatal("invalid session identity or lifetime")
	}
	response := httptest.NewRecorder()
	a.Cookie(response, session)
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "rss_session" || cookies[0].Value != session.Token || cookies[0].Path != "/" || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].MaxAge != 43200 {
		t.Fatal("session cookie protections changed")
	}
	request := httptest.NewRequest("GET", "https://example.test/", nil)
	request.AddCookie(cookies[0])
	if got, ok := a.Get(request); !ok || got != session {
		t.Fatal("saved session was not recovered")
	}
	if !CheckCSRF(session, session.CSRF) || CheckCSRF(session, "") || CheckCSRF(session, "incorrect") {
		t.Fatal("CSRF validation changed")
	}
	logout := httptest.NewRecorder()
	a.Logout(logout, request)
	if _, ok := a.Get(request); ok {
		t.Fatal("logged-out session remains valid")
	}
	if deleted := logout.Result().Cookies(); len(deleted) != 1 || deleted[0].MaxAge != -1 || deleted[0].Value != "" {
		t.Fatal("logout did not expire its cookie")
	}
	a.sessions[session.Token] = Session{Token: session.Token, Expires: time.Now().Add(-time.Second)}
	if _, ok := a.Get(request); ok || len(a.sessions) != 0 {
		t.Fatal("expired session was not removed")
	}
}

func TestLoginLimitsStillApply(t *testing.T) {
	a := externalAuth(t, "p", false)
	for attempt := 0; attempt < 10; attempt++ {
		if _, err := a.Login("wrong"); err == nil || err.Error() != "incorrect password" {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if _, err := a.Login("p"); err == nil || !strings.Contains(err.Error(), "too many login attempts") {
		t.Fatal("minute rate limit no longer blocks correct passwords")
	}
	a.window = time.Now().Add(-2 * time.Minute)
	a.hashing <- struct{}{}
	if _, err := a.Login("p"); err == nil || !strings.Contains(err.Error(), "login busy") {
		t.Fatal("hashing concurrency limit changed")
	}
	<-a.hashing
	if _, err := a.Login("p"); err != nil {
		t.Fatalf("login should recover after rate window/busy slot clear: %v", err)
	}
}
