package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"rss-workshop/internal/model"
)

const recipeFormat = "rss-workshop.recipes"
const recipeVersion = 1
const maxRecipeBytes = 2 << 20
const maxRecipes = 1000

// Deliberately separate from model.Feed: portable configuration must never
// include bearer tokens, internal IDs, sessions, or stored item/run history.
type portableFeed struct {
	Title    string       `json:"title"`
	URL      string       `json:"url"`
	Interval int          `json:"interval"`
	Recipe   model.Recipe `json:"recipe"`
}

func (p portableFeed) model() model.Feed {
	return model.Feed{Title: p.Title, URL: p.URL, Interval: p.Interval, Recipe: p.Recipe, Enabled: false}
}

type recipeDocument struct {
	Format  string         `json:"format"`
	Version int            `json:"version"`
	Feeds   []portableFeed `json:"feeds"`
}

func readRecipes(w http.ResponseWriter, r *http.Request) (recipeDocument, error) {
	var doc recipeDocument
	r.Body = http.MaxBytesReader(w, r.Body, maxRecipeBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&doc); err != nil {
		return doc, fmt.Errorf("could not read recipe file (maximum 2 MiB): %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return doc, errors.New("recipe file must contain one JSON document")
	}
	if doc.Format != recipeFormat {
		return doc, fmt.Errorf("unsupported recipe format; expected %q", recipeFormat)
	}
	if doc.Version != recipeVersion {
		return doc, fmt.Errorf("unsupported recipe version %d; this server supports version %d", doc.Version, recipeVersion)
	}
	if len(doc.Feeds) == 0 || len(doc.Feeds) > maxRecipes {
		return doc, fmt.Errorf("recipe file must contain 1–%d feeds", maxRecipes)
	}
	for i, f := range doc.Feeds {
		if err := validate(f.model()); err != nil {
			return doc, fmt.Errorf("feed %d: %w", i+1, err)
		}
	}
	return doc, nil
}

func (a *App) exportRecipes(w http.ResponseWriter, r *http.Request) {
	var feeds []model.Feed
	var err error
	if id := r.URL.Query().Get("id"); id != "" {
		var f model.Feed
		f, err = a.Store.Get(r.Context(), id)
		feeds = []model.Feed{f}
	} else {
		feeds, err = a.Store.List(r.Context())
	}
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "could not read recipes", 500)
		return
	}
	doc := recipeDocument{Format: recipeFormat, Version: recipeVersion, Feeds: make([]portableFeed, 0, len(feeds))}
	for _, f := range feeds {
		doc.Feeds = append(doc.Feeds, portableFeed{Title: f.Title, URL: f.URL, Interval: f.Interval, Recipe: f.Recipe})
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		http.Error(w, "could not export recipes", 500)
		return
	}
	if len(feeds) > maxRecipes || len(data)+1 > maxRecipeBytes {
		http.Error(w, "library exceeds the export limit (1,000 feeds / 2 MiB); use individual recipe exports", 422)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="rss-workshop-recipes-v1.json"`)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(append(data, '\n'))
}

func (a *App) previewRecipes(w http.ResponseWriter, r *http.Request) {
	doc, err := readRecipes(w, r)
	if err != nil {
		failure(w, 400, err)
		return
	}
	reply(w, 200, map[string]any{"count": len(doc.Feeds), "feeds": doc.Feeds})
}

func (a *App) importRecipes(w http.ResponseWriter, r *http.Request) {
	// Validate again on commit: preview results are never trusted as authority.
	doc, err := readRecipes(w, r)
	if err != nil {
		failure(w, 400, err)
		return
	}
	feeds := make([]model.Feed, 0, len(doc.Feeds))
	for _, f := range doc.Feeds {
		feeds = append(feeds, f.model())
	}
	ids, err := a.Store.Import(r.Context(), feeds)
	if err != nil {
		http.Error(w, "could not import recipes; no feeds were added", 500)
		return
	}
	reply(w, 201, map[string]any{"count": len(ids), "ids": ids})
}
