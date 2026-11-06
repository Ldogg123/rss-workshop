package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
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
