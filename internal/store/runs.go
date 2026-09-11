package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"rss-workshop/internal/diagnostics"
	"rss-workshop/internal/model"
)

const maxDiagnosticsBytes = 64 << 10

// MaxRuns is the per-feed refresh history kept by Save and returned by Runs.
// The editor renders it too, so the stored, served and displayed limits agree.
const MaxRuns = 50

// ArticleState is what a refresh needs to decide whether to fetch an item's
// article page: whether one was already stored, and how long the item has been
// known. Loading whole stories for this would pull every stored article body
// into memory, which at the per-item size limit is far more than the decision
// requires.
type ArticleState struct {
	HasBody   bool
	FirstSeen time.Time
}

// ArticleStates maps item key to that decision data for one feed.
func (s *Store) ArticleStates(ctx context.Context, feedID string) (_ map[string]ArticleState, err error) {
	defer s.cleanError(&err)
	rows, err := s.DB.QueryContext(ctx, s.bind("SELECT key,content_full<>'',first_seen FROM items WHERE feed_id=?"), feedID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ArticleState{}
	for rows.Next() {
		var key string
		var hasBody bool
		var firstSeen int64
		if err := rows.Scan(&key, &hasBody, &firstSeen); err != nil {
			return nil, err
		}
		out[key] = ArticleState{HasBody: hasBody, FirstSeen: stamp(firstSeen)}
	}
	return out, rows.Err()
}

func encodeDiagnostics(details *model.RunDiagnostics) string {
	safe := diagnostics.Sanitize(details)
	if safe == nil {
		return ""
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(safe); err != nil || out.Len() > maxDiagnosticsBytes {
		// A malformed trace must not prevent recording the run or advancing its
		// schedule. Summary-only history remains useful and compatible.
		return ""
	}
	return out.String()
}

func decodeDiagnostics(raw string) *model.RunDiagnostics {
	if len(raw) == 0 || len(raw) > maxDiagnosticsBytes {
		return nil
	}
	var details *model.RunDiagnostics
	if json.Unmarshal([]byte(raw), &details) != nil || details == nil || details.Version != 1 {
		return nil
	}
	return diagnostics.Sanitize(details)
}

// Runs returns the latest MaxRuns refresh results for this feed, including older
// summary-only rows. Invalid stored details cannot hide the surrounding history.
func (s *Store) Runs(ctx context.Context, feedID string) (_ []model.Run, err error) {
	defer s.cleanError(&err)
	rows, err := s.DB.QueryContext(ctx, s.bind(`SELECT id,ended,status,count,error,
CASE WHEN length(diagnostics)<=? THEN diagnostics ELSE '' END
FROM runs WHERE feed_id=? ORDER BY id DESC LIMIT ?`), maxDiagnosticsBytes, feedID, MaxRuns)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Run{}
	for rows.Next() {
		var run model.Run
		var ended int64
		var raw sql.NullString
		if err := rows.Scan(&run.ID, &ended, &run.Status, &run.Count, &run.Error, &raw); err != nil {
			return nil, err
		}
		run.Ended = stamp(ended)
		run.Error = diagnostics.SafeText(run.Error, 1000)
		if raw.Valid {
			run.Diagnostics = decodeDiagnostics(raw.String)
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
