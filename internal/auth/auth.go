package auth

import (
	"crypto/subtle"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"rss-workshop/internal/store"
	"sync"
	"time"
)

type Session struct {
	Token   string
	CSRF    string
	Expires time.Time
}
type Auth struct {
	hash     []byte
	secure   bool
	mu       sync.Mutex
	sessions map[string]Session
	window   time.Time
	attempts int
	hashing  chan struct{}
}

func New(password, hash string, secure bool) (*Auth, error) {
	var h []byte
	var e error
	if hash != "" {
		h = []byte(hash)
		if _, e = bcrypt.Cost(h); e != nil {
			return nil, fmt.Errorf("invalid ADMIN_PASSWORD_HASH")
		}
	} else {
		h, e = bcrypt.GenerateFromPassword([]byte(password), 12)
		if e != nil {
			return nil, e
		}
	}
	return &Auth{hash: h, secure: secure, sessions: map[string]Session{}, hashing: make(chan struct{}, 1)}, nil
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
	if bcrypt.CompareHashAndPassword(a.hash, []byte(password)) != nil {
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
