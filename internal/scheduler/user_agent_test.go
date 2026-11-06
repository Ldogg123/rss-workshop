package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
)

// Every static request a source receives names RSS Workshop, or the operator's
// FETCH_USER_AGENT: the visual selector's snapshot, and the list and article
// pages of both a preview and a refresh. The real guarded fetcher is used, so
// a path that bypassed it would show up as Go's own User-Agent.
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
				seen = append(seen, r.URL.Path+" "+r.UserAgent())
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
				want[i] += " " + tc.want
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(seen, want) {
				t.Errorf("the source received\n%q\nwant\n%q", seen, want)
			}
		})
	}
}

// A recipe's own User-Agent replaces the server's for every static request
// made for that feed: the visual selector's static snapshot, the list page and
// article pages of a preview and a refresh, Auto's static attempt, and the
// plain-HTML articles of a FlareSolverr feed. A feed without one sends
// FETCH_USER_AGENT, else the built-in name. Feeds on the same host refresh at
// the same time and each keeps its own.
func TestFeedUserAgentReplacesTheServers(t *testing.T) {
	for _, tc := range []struct{ name, configured, fallback string }{
		{"default", "", fetch.DefaultUserAgent},
		{"override", "GoRSS/0.1 (+self-hosted feed builder)", "GoRSS/0.1 (+self-hosted feed builder)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			seen := map[string][]string{}
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen[r.URL.Path] = append(seen[r.URL.Path], r.UserAgent())
				mu.Unlock()
				switch path.Base(r.URL.Path) {
				case "list":
					w.Write([]byte(`<article><h2>Story</h2><a href="story">Read</a><p>Teaser</p></article>`))
				case "story":
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
			jobs := New(s, client, 8, 5*time.Second)
			// The solver serves the FlareSolverr feed's list page; its articles
			// are fetched as plain HTML, through the static fetcher.
			jobs.FlareSolverr = &solverFetcher{fetch: func(ctx context.Context, u, _, _ string) (fetch.Result, error) {
				return fetch.Result{Body: []byte(`<article><h2>Story</h2><a href="story">Read</a><p>Teaser</p></article>`), URL: u, Status: 200}, nil
			}}

			want := map[string]string{}
			var feeds []model.Feed
			for _, f := range []struct{ name, mode, agent string }{
				{"static", "static", "Static-Feed/1 (+mailto:ops@example.net)"},
				{"auto", "auto", "Auto-Feed/2"},
				{"solver", "flaresolverr", "Solver-Feed/3"},
				{"plain", "static", ""},
			} {
				recipe := fullRecipe()
				recipe.Mode, recipe.UserAgent = f.mode, f.agent
				feed := model.Feed{Title: f.name, URL: source.URL + "/" + f.name + "/list", Interval: 60, Enabled: true, Recipe: recipe}
				agent := f.agent
				if agent == "" {
					agent = tc.fallback
				}
				if f.mode != "flaresolverr" {
					want["/"+f.name+"/list"] = agent
				}
				want["/"+f.name+"/story"] = agent
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
			if _, err := jobs.Snapshot(ctx, source.URL+"/selector/list", model.Recipe{Mode: "static", UserAgent: "Selector/4"}); err != nil {
				t.Fatal(err)
			}
			if _, err := jobs.Snapshot(ctx, source.URL+"/selector-plain/list", model.Recipe{Mode: "static"}); err != nil {
				t.Fatal(err)
			}
			want["/selector/list"], want["/selector-plain/list"] = "Selector/4", tc.fallback

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
			for p, agent := range want {
				// A preview and a refresh for each feed page, one request for
				// each snapshot.
				n := 2
				if strings.HasPrefix(p, "/selector") {
					n = 1
				}
				if got := seen[p]; len(got) != n || slices.ContainsFunc(got, func(ua string) bool { return ua != agent }) {
					t.Errorf("%s received %q, want %d requests from %q", p, got, n, agent)
				}
			}
			if len(seen) != len(want) {
				t.Errorf("the source received requests for %d paths, want %d: %q", len(seen), len(want), seen)
			}
		})
	}
}
