package scheduler

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/notify"
)

// Every static request a source receives names RSS Workshop, or the operator's
// FETCH_USER_AGENT: the visual selector's snapshot, and the list and article
// pages of both a preview and a refresh. The real guarded fetcher is used, so
// a path that bypassed it would show up as Go's own User-Agent. None of them
// sends an Accept-Language: that takes a feed of its own that sets one.
func TestStaticRequestsSendTheConfiguredUserAgent(t *testing.T) {
	for _, tc := range []struct{ name, configured, want string }{
		{"default", "", "RSS-Workshop (+https://github.com/Ldogg123/rss-workshop)"},
		{"override", "GoRSS/0.1 (+self-hosted feed builder)", "GoRSS/0.1 (+self-hosted feed builder)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var seen []string
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.URL.Path+" "+r.UserAgent()+" lang="+strings.Join(r.Header.Values("Accept-Language"), "|"))
				mu.Unlock()
				switch r.URL.Path {
				case "/selector", "/list":
					w.Write([]byte(`<article><h2>Story</h2><a href="/story">Read</a><p>Teaser</p></article>`))
				case "/story":
					w.Write([]byte(`<div class="body"><p>Full story</p></div>`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer source.Close()

			ctx := context.Background()
			s, err := openTestStore(t, 50)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			client, err := fetch.New(time.Second, "127.0.0.1/32")
			if err != nil {
				t.Fatal(err)
			}
			client.UserAgent = tc.configured
			jobs := New(s, client, 2, 5*time.Second)

			if _, err := jobs.Snapshot(ctx, source.URL+"/selector", model.Recipe{Mode: "static"}); err != nil {
				t.Fatal(err)
			}
			recipe := fullRecipe()
			recipe.Mode = "static"
			f := model.Feed{Title: "Agent", URL: source.URL + "/list", Interval: 60, Enabled: true, Recipe: recipe}
			p, err := jobs.Preview(ctx, f)
			if err != nil || len(p.Items) != 1 || p.Items[0].FullHTML == "" {
				t.Fatalf("preview: %+v, %v", p, err)
			}
			if f.ID, err = s.Save(ctx, f); err != nil {
				t.Fatal(err)
			}
			if f, err = s.Get(ctx, f.ID); err != nil {
				t.Fatal(err)
			}
			jobs.refresh(ctx, f)

			want := []string{"/selector", "/list", "/story", "/list", "/story"}
			for i := range want {
				want[i] += " " + tc.want + " lang="
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(seen, want) {
				t.Errorf("the source received\n%q\nwant\n%q", seen, want)
			}
		})
	}
}

// renderFunc stands in for Chromium.
type renderFunc func(context.Context, string, string, int) (fetch.Result, error)

func (f renderFunc) Render(ctx context.Context, u, wait string, settle int) (fetch.Result, error) {
	return f(ctx, u, wait, settle)
}

// A recipe's own User-Agent replaces the server's, and its Accept-Language is
// sent, on every static request made for that feed: the visual selector's
// static snapshot, the list page and article pages of a preview and a
// refresh, Auto's static attempt, and the plain-HTML articles of a Chromium or
// FlareSolverr feed. A feed without them sends FETCH_USER_AGENT, else the
// built-in name, and no Accept-Language. Pages that Chromium renders or
// FlareSolverr loads are not static requests: they reach neither the static
// fetcher nor, here, the source, so their browsers keep their own headers.
// Feeds on the same host refresh at the same time and each keeps its own.
func TestFeedRequestHeadersReachEveryStaticRequest(t *testing.T) {
	for _, tc := range []struct{ name, configured, fallback string }{
		{"default", "", fetch.DefaultUserAgent},
		{"override", "GoRSS/0.1 (+self-hosted feed builder)", "GoRSS/0.1 (+self-hosted feed builder)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			seen, loaded := map[string][]string{}, map[string]int{}
			page := func(p string) []byte {
				if path.Base(p) == "story" {
					return []byte(`<div class="body"><p>Full story</p></div>`)
				}
				return []byte(`<article><h2>Story</h2><a href="story">Read</a><p>Teaser</p></article>`)
			}
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen[r.URL.Path] = append(seen[r.URL.Path], r.UserAgent()+" lang="+strings.Join(r.Header.Values("Accept-Language"), "|"))
				mu.Unlock()
				w.Write(page(r.URL.Path))
			}))
			defer source.Close()

			ctx := context.Background()
			s, err := openTestStore(t, 50)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			client, err := fetch.New(time.Second, "127.0.0.1/32")
			if err != nil {
				t.Fatal(err)
			}
			client.UserAgent = tc.configured
			jobs := New(s, client, 8, 5*time.Second)
			// Chromium and FlareSolverr load the pages given to them, here
			// from memory, and are told nothing new.
			load := func(ctx context.Context, u string) (fetch.Result, error) {
				p := strings.TrimPrefix(u, source.URL)
				mu.Lock()
				loaded[p]++
				mu.Unlock()
				return fetch.Result{Body: page(p), URL: u, Status: 200}, nil
			}
			jobs.FlareSolverr = &solverFetcher{fetch: func(ctx context.Context, u, _, _ string) (fetch.Result, error) { return load(ctx, u) }}
			jobs.Renderer = renderFunc(func(ctx context.Context, u, _ string, _ int) (fetch.Result, error) { return load(ctx, u) })

			// The headers each static path should arrive with, and the paths
			// only a browser loads.
			want, browsers := map[string]string{}, map[string]int{}
			var feeds []model.Feed
			for _, f := range []struct {
				name, mode, agent, lang string
				renderArticles          bool
			}{
				{"static", "static", "Static-Feed/1 (+mailto:ops@example.net)", "de-CH, de;q=0.9, en;q=0.5", false},
				{"auto", "auto", "Auto-Feed/2", "", false},
				{"language", "static", "", "fr", false},
				{"solver", "flaresolverr", "Solver-Feed/3", "en", false},
				{"browser", "browser", "Browser-Feed/4", "en-GB", false},
				{"rendered", "browser", "Rendered-Feed/5", "en-GB", true},
				{"plain", "static", "", "", false},
			} {
				recipe := fullRecipe()
				recipe.Mode, recipe.UserAgent, recipe.AcceptLanguage, recipe.Full.Browser = f.mode, f.agent, f.lang, f.renderArticles
				feed := model.Feed{Title: f.name, URL: source.URL + "/" + f.name + "/list", Interval: 60, Enabled: true, Recipe: recipe}
				agent := f.agent
				if agent == "" {
					agent = tc.fallback
				}
				// A preview and a refresh for each page.
				if f.mode == "static" || f.mode == "auto" {
					want["/"+f.name+"/list"] = agent + " lang=" + f.lang
				} else {
					browsers["/"+f.name+"/list"] = 2
				}
				if f.renderArticles {
					browsers["/"+f.name+"/story"] = 2
				} else {
					want["/"+f.name+"/story"] = agent + " lang=" + f.lang
				}
				if p, err := jobs.Preview(ctx, feed); err != nil || len(p.Items) != 1 || p.Items[0].FullHTML == "" {
					t.Fatalf("preview of %s: %+v, %v", f.name, p, err)
				}
				if feed.ID, err = s.Save(ctx, feed); err != nil {
					t.Fatal(err)
				}
				if feed, err = s.Get(ctx, feed.ID); err != nil {
					t.Fatal(err)
				}
				feeds = append(feeds, feed)
			}
			for p, snapshot := range map[string]model.Recipe{
				"/selector/list":         {Mode: "static", UserAgent: "Selector/4", AcceptLanguage: "nl-BE, nl;q=0.9"},
				"/selector-plain/list":   {Mode: "static"},
				"/selector-browser/list": {Mode: "browser", UserAgent: "Selector/5", AcceptLanguage: "nl"},
				"/selector-solver/list":  {Mode: "flaresolverr", UserAgent: "Selector/6", AcceptLanguage: "nl"},
			} {
				if _, err := jobs.Snapshot(ctx, source.URL+p, snapshot); err != nil {
					t.Fatal(err)
				}
			}
			want["/selector/list"], want["/selector-plain/list"] = "Selector/4 lang=nl-BE, nl;q=0.9", tc.fallback+" lang="
			browsers["/selector-browser/list"], browsers["/selector-solver/list"] = 1, 1

			var wg sync.WaitGroup
			for _, f := range feeds {
				wg.Add(1)
				go func() { defer wg.Done(); jobs.refresh(ctx, f) }()
			}
			wg.Wait()
			for _, f := range feeds {
				if got, err := s.Get(ctx, f.ID); err != nil || got.Error != "" || got.LastSuccess.IsZero() {
					t.Fatalf("refresh of %s: %+v, %v", f.Title, got, err)
				}
			}

			mu.Lock()
			defer mu.Unlock()
			for p, sent := range want {
				n := 2
				if strings.HasPrefix(p, "/selector") {
					n = 1
				}
				if got := seen[p]; len(got) != n || slices.ContainsFunc(got, func(h string) bool { return h != sent }) {
					t.Errorf("%s received %q, want %d requests with %q", p, got, n, sent)
				}
			}
			if len(seen) != len(want) {
				t.Errorf("the source received requests for %d paths, want %d: %q", len(seen), len(want), seen)
			}
			if !maps.Equal(loaded, browsers) {
				t.Errorf("Chromium and FlareSolverr loaded %v, want %v", loaded, browsers)
			}
		})
	}
}

// The web handlers trim a recipe's User-Agent and Accept-Language and check
// them before any of this runs, but the scheduler does not depend on that.
// Snapshot checks the values it would send in every page version, before it
// picks one, and a preview or a refresh of a stored recipe that skipped the
// check is refused by the fetcher. A value with space around it, a line break
// or anything else its header cannot carry as written fails with an error that
// names the setting, and the source receives nothing: no request with a header
// other than the one checked, and no generic connection error from the
// transport. The value appears in no error, log line or notification.
func TestUncheckedFeedHeadersAreRefusedBeforeAnyRequest(t *testing.T) {
	var requests atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write([]byte(`<article><h2>Story</h2><a href="/story">Read</a><p>Teaser</p></article>`))
	}))
	defer source.Close()

	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	client, err := fetch.New(time.Second, "127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	jobs := New(s, client, 2, 5*time.Second)
	log := captureLog(t)
	heard, _ := alerting(jobs, 1)
	for _, tc := range []struct {
		setting, marker string
		set             func(*model.Recipe, string)
		values          []string
	}{
		{"User-Agent", "Agent/1", func(r *model.Recipe, v string) { r.UserAgent = v },
			[]string{" Agent/1", "Agent/1\n", " Agent/1\r\n", "\vAgent/1", "Agent/1 ", "\u0085Agent/1", "Agent/1\r\nX-Injected: 1", "Agent/1é"}},
		{"Accept-Language", "secret", func(r *model.Recipe, v string) { r.AcceptLanguage = v },
			[]string{" secret", "secret\n", "secret\u00a0", "secret\r\nX-Injected: 1", "secret;q=2", "en, secretsecret", "secret, " + strings.Repeat("en,", 41) + "en"}},
	} {
		refused := func(err error) bool {
			return err != nil && strings.HasPrefix(err.Error(), tc.setting+" ") && strings.Contains(err.Error(), " must ") && !strings.Contains(err.Error(), tc.marker)
		}
		for _, value := range tc.values {
			for _, mode := range []string{"static", "browser", "flaresolverr"} {
				recipe := model.Recipe{Mode: mode}
				tc.set(&recipe, value)
				if _, err := jobs.Snapshot(ctx, source.URL+"/list", recipe); !refused(err) {
					t.Errorf("%s snapshot with %s %q: %v", mode, tc.setting, value, err)
				}
			}
			recipe := fullRecipe()
			recipe.Mode = "static"
			tc.set(&recipe, value)
			f := model.Feed{Title: "Headers", URL: source.URL + "/list", Interval: 60, Enabled: true, Recipe: recipe}
			if _, err := jobs.Preview(ctx, f); !refused(err) {
				t.Errorf("preview with %s %q: %v", tc.setting, value, err)
			}
			if f.ID, err = s.Save(ctx, f); err != nil {
				t.Fatal(err)
			}
			if f, err = s.Get(ctx, f.ID); err != nil {
				t.Fatal(err)
			}
			jobs.refresh(ctx, f)
			if got, err := s.Get(ctx, f.ID); err != nil || !refused(errors.New(got.Error)) || !got.LastSuccess.IsZero() {
				t.Errorf("refresh with %s %q: %q, %v", tc.setting, value, got.Error, err)
			}
			events := heard.take()
			if len(events) != 1 || events[0].Kind != notify.Failing || !refused(errors.New(events[0].Error)) {
				t.Errorf("refresh with %s %q notified %+v", tc.setting, value, events)
			}
		}
		if out := log.String(); !strings.Contains(out, `error="`+tc.setting+" ") || strings.Contains(out, tc.marker) {
			t.Errorf("the log does not report the %s refusal, or repeats the value:\n%s", tc.setting, out)
		}
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("the source received %d requests", n)
	}
}
