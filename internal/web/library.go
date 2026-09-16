package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"rss-workshop/internal/filter"
	"rss-workshop/internal/model"
	"rss-workshop/internal/store"
)

const maxFilterNameCharacters = 100

func (a *App) listFilters(w http.ResponseWriter, r *http.Request) {
	filters, err := a.Store.Filters(r.Context())
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	reply(w, 200, map[string]any{"filters": filters})
}

func (a *App) saveFilter(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name                  string          `json:"name"`
		Filters               model.FilterSet `json:"filters"`
		ApplyFiltersToHistory bool            `json:"apply_filters_to_history"`
	}
	if err := decode(w, r, &in); err != nil {
		failure(w, 400, err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > maxFilterNameCharacters {
		failure(w, 400, errors.New("filter name is required (maximum 100 characters)"))
		return
	}
	if _, err := filter.Compile(&in.Filters); err != nil {
		failure(w, 400, err)
		return
	}
	lf := model.LibraryFilter{ID: r.PathValue("id"), Name: name, Filters: in.Filters}
	id, err := a.Store.SaveFilter(r.Context(), lf, store.SaveOptions{ApplyFiltersToHistory: in.ApplyFiltersToHistory, Validate: validate})
	if err != nil {
		a.saveFailure(w, r, err, in.ApplyFiltersToHistory, "could not save filter")
		return
	}
	reply(w, 200, map[string]string{"id": id})
}

func (a *App) removeFilter(w http.ResponseWriter, r *http.Request) {
	err := a.Store.DeleteFilter(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		reply(w, 200, map[string]bool{"ok": true})
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case errors.Is(err, store.ErrFilterInUse):
		failure(w, 409, err)
	default:
		http.Error(w, "could not delete filter", 500)
	}
}

// saveFailure maps store errors shared by feed and library filter saves. A
// rejected combination of rules is the operator's to fix, so its reason is
// returned; database failures stay generic.
func (a *App) saveFailure(w http.ResponseWriter, r *http.Request, err error, pruning bool, generic string) {
	var bad *store.InvalidError
	switch {
	case errors.As(err, &bad):
		failure(w, 400, bad)
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case pruning && errors.Is(err, context.DeadlineExceeded):
		failure(w, 503, errors.New("applying filters to saved history timed out; try narrowing the rules or leave the history option unchecked"))
	default:
		http.Error(w, generic, 500)
	}
}
