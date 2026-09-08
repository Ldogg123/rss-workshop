package web

import (
	"database/sql"
	"errors"
	"net/http"
)

// Reading history never schedules or fetches a source, even for an empty feed.
func (a *App) runs(w http.ResponseWriter, r *http.Request) {
	if _, err := a.Store.Get(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			failure(w, http.StatusNotFound, errors.New("feed not found"))
		} else {
			failure(w, http.StatusInternalServerError, errors.New("refresh history could not be read"))
		}
		return
	}
	runs, err := a.Store.Runs(r.Context(), r.PathValue("id"))
	if err != nil {
		failure(w, http.StatusInternalServerError, errors.New("refresh history could not be read"))
		return
	}
	reply(w, http.StatusOK, map[string]any{"runs": runs, "limit": 50})
}
