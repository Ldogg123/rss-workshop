package web

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/browser"
	"rss-workshop/internal/diagnostics"
	"rss-workshop/internal/extract"
	"rss-workshop/internal/feed"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/filter"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
	"rss-workshop/internal/store"
)

//go:embed templates/* assets/*
var files embed.FS

type App struct {
	Browsers  *browser.Pool
	Store     *store.Store
	Scheduler *scheduler.Scheduler
	Auth      *auth.Auth
	BaseURL   string
	Version   string
	// MetricsToken enables GET /metrics. Blank leaves the route returning 404,
	// so counters labelled with feed titles are never published by default.
	MetricsToken string
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if e := a.Store.DB.PingContext(ctx); e != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
		w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /assets/", http.FileServer(http.FS(files)))
	tmpl := template.Must(template.ParseFS(files, "templates/index.html"))
	version := a.Version
	if version == "" {
		version = "dev"
	}
	// The editor renders these bounds and rejects oversized filters before saving.
	// They come from internal/filter so the UI cannot drift from the validator.
	page := struct {
		Version                                               string
		MaxKeywords, MaxNodes, MaxDepth, MaxKeywordCharacters int
		MaxRuns, MaxArticles                                  int
	}{version, filter.MaxKeywords, filter.MaxNodes, filter.MaxDepth, filter.MaxKeywordCharacters, store.MaxRuns, scheduler.MaxArticlesPerRun}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = tmpl.Execute(w, page)
	})
	mux.HandleFunc("GET /feeds/{id}", a.rss)
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("GET /api/session", a.protect(func(w http.ResponseWriter, r *http.Request) {
		s, _ := a.Auth.Get(r)
		reply(w, 200, map[string]string{"csrf": s.CSRF})
	}))
	mux.HandleFunc("POST /api/logout", a.protect(func(w http.ResponseWriter, r *http.Request) {
		a.Auth.Logout(w, r)
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /api/feeds", a.protect(a.list))
	mux.HandleFunc("POST /api/feeds", a.protect(a.save))
	mux.HandleFunc("PUT /api/feeds/{id}", a.protect(a.save))
	mux.HandleFunc("DELETE /api/feeds/{id}", a.protect(a.remove))
	mux.HandleFunc("POST /api/feeds/{id}/refresh", a.protect(a.refresh))
	mux.HandleFunc("GET /api/feeds/{id}/runs", a.protect(a.runs))
	mux.HandleFunc("POST /api/feeds/{id}/rotate-token", a.protect(a.rotateToken))
	mux.HandleFunc("POST /api/preview", a.protect(a.preview))
	// Not under /api and not session-authenticated: a scraper presents a bearer
	// token instead. The handler returns 404 when no token is configured.
	mux.HandleFunc("GET /metrics", a.metrics)
	mux.HandleFunc("GET /api/opml", a.protect(a.exportOPML))
	mux.HandleFunc("GET /api/recipes/export", a.protect(a.exportRecipes))
	mux.HandleFunc("POST /api/recipes/preview", a.protect(a.previewRecipes))
	mux.HandleFunc("POST /api/recipes/import", a.protect(a.importRecipes))
	mux.HandleFunc("POST /api/selector/snapshot", a.protect(a.selectorSnapshot))
	mux.HandleFunc("GET /selector/frame", a.selectorFrame)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' https: http:; frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" && r.Method != "HEAD" {
			if !a.acceptsOrigin(r) {
				reply(w, 403, map[string]string{"error": "cross-origin request rejected; open the app using its configured address"})
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "JSON required", 415)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// Port forwarding can rewrite Host while preserving the browser's Origin.
// Sec-Fetch-Site is browser-controlled: a page on another port sends same-site,
// not same-origin. Only accept a different port when that signal is present,
// and keep the configured scheme and hostname as the trust boundary.
func (a *App) acceptsOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == a.BaseURL && origin != "" {
		return true
	}
	if r.Header.Get("Sec-Fetch-Site") != "same-origin" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	base, err := url.Parse(a.BaseURL)
	return err == nil && u.Scheme == base.Scheme && strings.EqualFold(u.Hostname(), base.Hostname())
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, e error) {
	reply(w, status, map[string]string{"error": e.Error()})
}

const maxRequestBytes = 64 << 10

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}
func (a *App) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := a.Auth.Get(r)
		if !ok {
			http.Error(w, "sign in required", 401)
			return
		}
		if r.Method != "GET" && !auth.CheckCSRF(s, r.Header.Get("X-CSRF-Token")) {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		next(w, r)
	}
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if e := decode(w, r, &in); e != nil {
		http.Error(w, "invalid login request", 400)
		return
	}
	s, e := a.Auth.Login(in.Password)
	if e != nil {
		// A self-hosted instance is often reachable from a LAN or a proxy, so a
		// run of these is the signal an operator wants. The attempted password
		// is never logged, only that one was rejected and from where.
		//
		// Throttled attempts are logged at debug instead. They are rejected
		// before any password check, so an unauthenticated client can produce
		// them as fast as it can open connections; at warn they would let anyone
		// fill the host's disk with log lines that carry no extra signal. A real
		// password rejection costs a bcrypt comparison, which the same limiter
		// caps at ten per minute.
		if errors.Is(e, auth.ErrThrottled) {
			slog.Debug("login throttled", "remote", clientAddr(r))
		} else {
			slog.Warn("login rejected", "remote", clientAddr(r), "error", e)
		}
		failure(w, 401, e)
		return
	}
	slog.Info("admin signed in", "remote", clientAddr(r))
	a.Auth.Cookie(w, s)
	reply(w, 200, map[string]string{"csrf": s.CSRF})
}

// readerLinks fills the public RSS and Atom URLs for a feed. Both the library
// listing and the OPML export hand these to readers, so they are derived here
// once rather than reassembled from the token at each call site.
func (a *App) readerLinks(f *model.Feed) {
	f.RSSURL = a.BaseURL + "/feeds/" + f.RSSToken + ".xml"
	f.AtomURL = a.BaseURL + "/feeds/" + f.RSSToken + ".atom"
}

// clientAddr reports who made a request without trusting a forwarded header,
// which any client can set. Behind a reverse proxy this is the proxy.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *App) list(w http.ResponseWriter, r *http.Request) {
	fs, e := a.Store.List(r.Context())
	if e != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	due := 0
	for i := range fs {
		a.readerLinks(&fs[i])
		if fs[i].Enabled && !fs[i].NextRun.After(time.Now()) {
			due++
		}
	}
	active, limit := a.Scheduler.Stats()
	solverActive, solverLimit := 0, 0
	if a.Scheduler.FlareSolverr != nil {
		solverActive, solverLimit = a.Scheduler.FlareSolverr.Stats()
	}
	reply(w, 200, map[string]any{"feeds": fs, "active": active, "capacity": limit, "due": due, "browser": a.browserStats(), "flaresolverr": map[string]int{"active": solverActive, "capacity": solverLimit}})
}
func validate(f model.Feed) error {
	if strings.TrimSpace(f.Title) == "" || len(f.Title) > 200 {
		return errors.New("feed title is required (maximum 200 characters)")
	}
	if len(f.URL) > 4096 {
		return errors.New("source URL is too long")
	}
	if e := fetch.ValidateURL(f.URL); e != nil {
		return e
	}
	if f.Interval < 60 || f.Interval > 604800 {
		return errors.New("refresh interval must be 60–604800 seconds")
	}
	// Archives have a larger total limit, but each configuration must still fit
	// the editor's save request so an imported paused feed can be resumed.
	// Include the longer false spelling for enabled and the optional one-time
	// history action, so a boundary-sized imported recipe remains editable.
	payload := struct {
		portableFeed
		Enabled               bool `json:"enabled"`
		ApplyFiltersToHistory bool `json:"apply_filters_to_history"`
	}{portableFeed: portableFeed{Title: f.Title, URL: f.URL, Interval: f.Interval, Recipe: f.Recipe}}
	data, err := json.Marshal(payload)
	if err != nil || len(data) > maxRequestBytes {
		return errors.New("feed configuration exceeds the 64 KiB editor limit")
	}
	return extract.Validate(f.Recipe)
}
func (a *App) save(w http.ResponseWriter, r *http.Request) {
	var in struct {
		model.Feed
		ApplyFiltersToHistory bool `json:"apply_filters_to_history"`
	}
	if e := decode(w, r, &in); e != nil {
		failure(w, 400, e)
		return
	}
	f := in.Feed
	f.ID = r.PathValue("id")
	if e := validate(f); e != nil {
		failure(w, 400, e)
		return
	}
	id, e := a.Store.SaveWithOptions(r.Context(), f, store.SaveOptions{ApplyFiltersToHistory: in.ApplyFiltersToHistory})
	if e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			http.NotFound(w, r)
		} else if in.ApplyFiltersToHistory && errors.Is(e, context.DeadlineExceeded) {
			failure(w, 503, errors.New("applying filters to saved history timed out; try narrowing the rules or leave the history option unchecked"))
		} else {
			http.Error(w, "could not save feed", 500)
		}
		return
	}
	reply(w, 200, map[string]string{"id": id})
}
func (a *App) remove(w http.ResponseWriter, r *http.Request) {
	if e := a.Store.Delete(r.Context(), r.PathValue("id")); e != nil {
		http.Error(w, "could not delete feed", 500)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (a *App) refresh(w http.ResponseWriter, r *http.Request) {
	f, e := a.Store.Get(r.Context(), r.PathValue("id"))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	if !f.Enabled {
		http.Error(w, "resume this feed before refreshing", 409)
		return
	}
	if e = a.Store.Queue(r.Context(), f.ID); e != nil {
		http.Error(w, "could not queue refresh", 500)
		return
	}
	reply(w, 202, map[string]bool{"queued": true})
}
func (a *App) preview(w http.ResponseWriter, r *http.Request) {
	var f model.Feed
	if e := decode(w, r, &f); e != nil {
		failure(w, 400, e)
		return
	}
	if e := validate(f); e != nil {
		failure(w, 400, e)
		return
	}
	p, e := a.Scheduler.Preview(r.Context(), f)
	if e != nil {
		code := 422
		if errors.Is(e, scheduler.ErrBusy) {
			code = 503
		}
		reply(w, code, map[string]any{"error": diagnostics.SafeText(e.Error(), 1000), "matches": p.Matches, "valid": p.Valid, "filtered": p.Filtered, "filter_examples": p.FilterExamples, "warnings": p.Warnings, "diagnostics": p.Diagnostics})
		return
	}
	reply(w, 200, p)
}
func (a *App) rss(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("id")
	atom := strings.HasSuffix(path, ".atom")
	id := strings.TrimSuffix(path, ".xml")
	if atom {
		id = strings.TrimSuffix(path, ".atom")
	}
	f, e := a.Store.GetByToken(r.Context(), id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	id = f.ID
	if f.LastSuccess.IsZero() {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "Feed pending its first successful refresh", 503)
		return
	}
	items, e := a.Store.Items(r.Context(), id)
	if e != nil {
		http.Error(w, "could not read feed", 500)
		return
	}
	var b []byte
	var etag string
	if atom {
		b, etag, e = feed.RenderAtom(f, items, a.BaseURL)
	} else {
		b, etag, e = feed.Render(f, items)
	}
	if e != nil {
		http.Error(w, "could not render feed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	if atom {
		w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=60")
	for _, tag := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
			w.WriteHeader(304)
			return
		}
	}
	w.Write(b)
}

func (a *App) rotateToken(w http.ResponseWriter, r *http.Request) {
	if e := a.Store.RotateToken(r.Context(), r.PathValue("id")); e != nil {
		http.NotFound(w, r)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}

func (a *App) browserStats() browser.Stats {
	if a.Browsers == nil {
		return browser.Stats{}
	}
	return a.Browsers.Stats()
}
