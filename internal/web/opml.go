package web

import (
	"net/http"
	"time"

	"rss-workshop/internal/feed"
)

// An OPML export is a bulk subscription list, so it repeats the reader links
// for the whole library. Those links are bearer credentials: this stays behind
// authentication, and the browser is told never to cache the response.
const maxOPMLFeeds = 1000

func (a *App) exportOPML(w http.ResponseWriter, r *http.Request) {
	feeds, err := a.Store.List(r.Context())
	if err != nil {
		http.Error(w, "could not read the feed library", 500)
		return
	}
	if len(feeds) > maxOPMLFeeds {
		http.Error(w, "library exceeds the OPML export limit (1,000 feeds)", 422)
		return
	}
	for i := range feeds {
		a.readerLinks(&feeds[i])
	}
	// Readers overwhelmingly prefer RSS; Atom stays available for the ones that
	// handle its richer dates better. Anything else is a typo worth reporting.
	atom := false
	switch format := r.URL.Query().Get("format"); format {
	case "", "rss":
	case "atom":
		atom = true
	default:
		http.Error(w, "format must be rss or atom", 400)
		return
	}
	data, err := feed.RenderOPML("RSS Workshop", feeds, atom, time.Now())
	if err != nil {
		http.Error(w, "could not build the OPML document", 500)
		return
	}
	name := "rss-workshop.opml"
	if atom {
		name = "rss-workshop-atom.opml"
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}
