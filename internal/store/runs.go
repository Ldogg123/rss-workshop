package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"rss-workshop/internal/diagnostics"
	"rss-workshop/internal/model"
)

const maxDiagnosticsBytes = 64 << 10

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

// Runs returns the latest 50 refresh results for this feed, including older
// summary-only rows. Invalid stored details cannot hide the surrounding history.
func (s *Store) Runs(ctx context.Context, feedID string) (_ []model.Run, err error) {
	defer s.cleanError(&err)
	rows, err := s.DB.QueryContext(ctx, s.bind(`SELECT id,ended,status,count,error,
CASE WHEN length(diagnostics)<=? THEN diagnostics ELSE '' END
FROM runs WHERE feed_id=? ORDER BY id DESC LIMIT 50`), maxDiagnosticsBytes, feedID)
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
