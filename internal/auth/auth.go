package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"rss-workshop/internal/store"
)

type Session struct {
	Token   string
	CSRF    string
	Expires time.Time
}
type Auth struct {
	hash     []byte
	pepper   []byte // nil for an externally supplied standard bcrypt hash
	secure   bool
	mu       sync.Mutex
	sessions map[string]Session
	window   time.Time
	attempts int
	hashing  chan struct{}
}

func New(password, hash string, secure bool) (*Auth, error) {
	a := &Auth{secure: secure, sessions: map[string]Session{}, hashing: make(chan struct{}, 1)}
	var e error
	if hash != "" {
		a.hash = []byte(hash)
		if _, e = bcrypt.Cost(a.hash); e != nil {
			return nil, fmt.Errorf("invalid ADMIN_PASSWORD_HASH")
		}
	} else {
		// OWASP's bcrypt prehash construction avoids bcrypt's 72-byte input
		// limit while checking the entire password. Hash and pepper live only
		// in this Auth instance and are recreated from configuration at startup.
		// https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html#pre-hashing-passwords-with-bcrypt
		a.pepper = make([]byte, 32)
		if _, e = rand.Read(a.pepper); e != nil {
			return nil, fmt.Errorf("could not initialize password hashing")
		}
		a.hash, e = bcrypt.GenerateFromPassword(a.passwordInput(password), 12)
		if e != nil {
			return nil, e
		}
	}
	return a, nil
}

func (a *Auth) passwordInput(password string) []byte {
	if a.pepper == nil {
		return []byte(password)
	}
	mac := hmac.New(sha512.New384, a.pepper)
	_, _ = mac.Write([]byte(password))
	// SHA-384's 48 bytes encode to 64 printable bytes, below bcrypt's limit.
	return []byte(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

func (a *Auth) Login(password string) (Session, error) {
	a.mu.Lock()
	now := time.Now()
	if now.Sub(a.window) > time.Minute {
		a.window = now
		a.attempts = 0
	}
	a.attempts++
	limited := a.attempts > 10
	a.mu.Unlock()
	if limited {
		return Session{}, fmt.Errorf("too many login attempts; wait one minute")
	}
	select {
	case a.hashing <- struct{}{}:
		defer func() { <-a.hashing }()
	default:
		return Session{}, fmt.Errorf("login busy; try again shortly")
	}
	// Standard externally supplied bcrypt hashes cannot authenticate suffixes
	// beyond 72 bytes. Reject such candidates instead of accepting a prefix.
	if a.pepper == nil && len(password) > 72 {
		return Session{}, fmt.Errorf("incorrect password")
	}
	if bcrypt.CompareHashAndPassword(a.hash, a.passwordInput(password)) != nil {
		return Session{}, fmt.Errorf("incorrect password")
	}
	s := Session{Token: store.ID(), CSRF: store.ID(), Expires: now.Add(12 * time.Hour)}
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, v := range a.sessions {
		if now.After(v.Expires) {
			delete(a.sessions, k)
		}
	}
	if len(a.sessions) >= 32 {
		for k := range a.sessions {
			delete(a.sessions, k)
			break
		}
	}
	a.sessions[s.Token] = s
	return s, nil
}
func (a *Auth) Get(r *http.Request) (Session, bool) {
	c, e := r.Cookie("rss_session")
	if e != nil {
		return Session{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[c.Value]
	if !ok {
		return s, false
	}
	if time.Now().After(s.Expires) {
		delete(a.sessions, c.Value)
		return Session{}, false
	}
	return s, true
}
func (a *Auth) Cookie(w http.ResponseWriter, s Session) {
	http.SetCookie(w, &http.Cookie{Name: "rss_session", Value: s.Token, Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, Expires: s.Expires, MaxAge: 43200})
}
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if s, ok := a.Get(r); ok {
		a.mu.Lock()
		delete(a.sessions, s.Token)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "rss_session", Value: "", Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}
func CheckCSRF(s Session, token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(s.CSRF), []byte(token)) == 1
}
