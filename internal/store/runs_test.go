package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"rss-workshop/internal/model"
)

func testRunDiagnostics() *model.RunDiagnostics {
	matches, items := 20, 0
	return &model.RunDiagnostics{Version: 1, RecipeVersion: 1, RequestedMode: "auto", SelectorType: "css", Started: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), DurationMS: 350, Attempts: []model.FetchAttempt{
		{Mode: "static", Outcome: "failed", Stage: "extract", Status: 200, DurationMS: 120, Bytes: 4000, Matches: &matches, Items: &items, Error: "no valid items", Warnings: []string{"Match 1 skipped: empty title"}},
		{Mode: "browser", Outcome: "failed", Stage: "fetch", Status: 503, DurationMS: 230, Error: "source unavailable", Warnings: []string{}},
	}}
}

func savedRunFeed(t *testing.T, s *Store) model.Feed {
	t.Helper()
	id, err := s.Save(t.Context(), model.Feed{Title: "Run history", URL: "https://example.com", Interval: 600})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRunDiagnosticsRetentionIsolationAndRestart(t *testing.T) {
	open := testStoreOpener(t, 10)
	s, err := open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	f, other := savedRunFeed(t, s), savedRunFeed(t, s)
	if err := s.Complete(t.Context(), other, nil, "", "", 304, nil, 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 53; i++ {
		details := testRunDiagnostics()
		details.DurationMS = int64(i)
		if err := s.CompleteWithDiagnostics(t.Context(), f, nil, "", "", 503, errors.New("source unavailable"), 0, details); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = open()
	if err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs(t.Context(), f.ID)
	if err != nil || len(runs) != 50 {
		t.Fatal("run retention/restart failed", err)
	}
	for i, run := range runs {
		if run.Diagnostics == nil || run.Diagnostics.DurationMS != int64(52-i) || run.Status != 503 || run.Count != 0 || run.Error != "source unavailable" || run.Ended.IsZero() {
			t.Fatal("ordered run history differs after restart")
		}
		if i > 0 && run.ID >= runs[i-1].ID {
			t.Fatal("run order is not newest-first")
		}
	}
	var count int
	if err := s.DB.QueryRowContext(t.Context(), s.bind("SELECT count(*) FROM runs WHERE feed_id=?"), f.ID).Scan(&count); err != nil || count != 50 {
		t.Fatal("old diagnostics were hidden rather than pruned", err)
	}
	otherRuns, err := s.Runs(t.Context(), other.ID)
	if err != nil || len(otherRuns) != 1 || otherRuns[0].Diagnostics != nil || otherRuns[0].Status != 304 {
		t.Fatal("feed histories mixed or wrapper added diagnostics", err)
	}
	if err := s.Delete(t.Context(), f.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := s.Runs(t.Context(), f.ID)
	if err != nil || remaining == nil || len(remaining) != 0 {
		t.Fatal("deleted feed retained run details", err)
	}
	if err := s.DB.QueryRowContext(t.Context(), "SELECT count(*) FROM runs").Scan(&count); err != nil || count != 1 {
		t.Fatal("cascade deleted another feed's history", err)
	}
}

func TestRunDiagnosticsStaleAndDeletedResultsDiscarded(t *testing.T) {
	s, err := testStoreOpener(t, 10)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f := savedRunFeed(t, s)
	if err := s.CompleteWithDiagnostics(t.Context(), f, []model.Item{{Key: "saved", Title: "Saved"}}, "original", "", 200, nil, 0, testRunDiagnostics()); err != nil {
		t.Fatal(err)
	}
	before, err := s.Runs(t.Context(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteWithDiagnostics(t.Context(), f, nil, "stale", "", 500, errors.New("stale error"), 0, testRunDiagnostics()); !errors.Is(err, ErrStale) {
		t.Fatal("stale refresh recorded", err)
	}
	after, err := s.Runs(t.Context(), f.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("stale refresh changed history", err)
	}
	if err := s.Delete(t.Context(), f.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteWithDiagnostics(t.Context(), f, nil, "", "", 500, errors.New("deleted error"), 0, testRunDiagnostics()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("deleted refresh recorded", err)
	}
	after, err = s.Runs(t.Context(), f.ID)
	if err != nil || len(after) != 0 {
		t.Fatal("deleted refresh left history", err)
	}
}

func TestRunInsertFailureRollsBackItemsAndSchedule(t *testing.T) {
	s, err := testStoreOpener(t, 10)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f := savedRunFeed(t, s)
	query := `CREATE TRIGGER prevent_run_insert BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT, 'fixture blocks run insert'); END`
	if s.postgres {
		query = `CREATE FUNCTION prevent_run_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture blocks run insert'; END $$;
CREATE TRIGGER prevent_run_insert BEFORE INSERT ON runs FOR EACH ROW EXECUTE FUNCTION prevent_run_insert()`
	}
	if _, err := s.DB.ExecContext(t.Context(), query); err != nil {
		t.Fatal("cannot install run failure fixture", err)
	}
	if err := s.CompleteWithDiagnostics(t.Context(), f, []model.Item{{Key: "new", Title: "New"}}, "new-etag", "new-modified", 200, nil, 0, testRunDiagnostics()); err == nil {
		t.Fatal("failed run insert was accepted")
	}
	current, err := s.Get(t.Context(), f.ID)
	if err != nil || !reflect.DeepEqual(current, f) {
		t.Fatal("failed run insert committed schedule or validators", err)
	}
	items, err := s.Items(t.Context(), f.ID)
	if err != nil || len(items) != 0 {
		t.Fatal("failed run insert committed extracted items", err)
	}
	runs, err := s.Runs(t.Context(), f.ID)
	if err != nil || len(runs) != 0 {
		t.Fatal("failed run insert left partial diagnostics", err)
	}
}

func TestRunDiagnosticsAreBoundedSanitizedAndDetached(t *testing.T) {
	s, err := testStoreOpener(t, 10)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f := savedRunFeed(t, s)
	details := testRunDiagnostics()
	hostile := "bad\xff\x00 https://user:private-password@example.com/private?token=secret\nCookie: private-cookie\n" + strings.Repeat("\"<>日", 700)
	details.Attempts[0].Error = hostile
	details.Attempts[0].Warnings = make([]string, 30)
	for i := range details.Attempts[0].Warnings {
		details.Attempts[0].Warnings[i] = hostile
	}
	details.Attempts = append(details.Attempts, details.Attempts[0])
	if err := s.CompleteWithDiagnostics(t.Context(), f, nil, "", "", 500, errors.New(hostile), 0, details); err != nil {
		t.Fatal("hostile diagnostics prevented scheduling", err)
	}
	details.Attempts[0].Warnings[0] = "caller changed this"
	*details.Attempts[0].Matches = 999
	var raw, summary string
	if err := s.DB.QueryRowContext(t.Context(), s.bind("SELECT diagnostics,error FROM runs WHERE feed_id=?"), f.ID).Scan(&raw, &summary); err != nil {
		t.Fatal(err)
	}
	if raw == "" || len(raw) > maxDiagnosticsBytes || !json.Valid([]byte(raw)) || !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) || len(summary) > 1000 || !utf8.ValidString(summary) || strings.ContainsRune(summary, 0) {
		t.Fatal("stored diagnostics are missing, oversized or invalid")
	}
	for _, forbidden := range []string{"private-password", "private-cookie", "/private?token=secret", "caller changed this"} {
		if strings.Contains(raw, forbidden) || strings.Contains(summary, forbidden) {
			t.Fatal("stored diagnostic text retained unsafe data or aliased caller memory")
		}
	}
	runs, err := s.Runs(t.Context(), f.ID)
	if err != nil || len(runs) != 1 || runs[0].Diagnostics == nil {
		t.Fatal("cannot read sanitized history", err)
	}
	saved := runs[0].Diagnostics
	if len(saved.Attempts) != 2 || len(saved.Attempts[0].Warnings) != 20 || saved.Attempts[0].WarningsOmitted != 10 || *saved.Attempts[0].Matches != 20 {
		t.Fatal("stored trace lost bounds, counts or snapshot isolation")
	}
	for _, warning := range saved.Attempts[0].Warnings {
		if len(warning) > 512 || !utf8.ValidString(warning) {
			t.Fatal("stored warning exceeds readable-text bound")
		}
	}
	feed, err := s.Get(t.Context(), f.ID)
	if err != nil || feed.Failures != 1 || feed.NextRun.Before(time.Now()) || feed.Error != summary {
		t.Fatal("diagnostics failure did not update the matching feed state", err)
	}
}

func TestMalformedRunDetailsPreserveSummaries(t *testing.T) {
	s, err := testStoreOpener(t, 10)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	f := savedRunFeed(t, s)
	for _, raw := range []string{"", "null", "{broken", `{"version":99}`, strings.Repeat("x", maxDiagnosticsBytes+1)} {
		if _, err := s.DB.ExecContext(t.Context(), s.bind("INSERT INTO runs(feed_id,ended,status,count,error,diagnostics) VALUES(?,?,?,?,?,?)"), f.ID, 123, 500, 0, "Legacy https://example.com/private?token=secret", raw); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs(t.Context(), f.ID)
	if err != nil || len(runs) != 5 {
		t.Fatal("malformed details hid run summaries", err)
	}
	for _, run := range runs {
		if run.Diagnostics != nil || run.Error != "Legacy [URL omitted]" || run.Ended.Unix() != 123 || run.Status != 500 {
			t.Fatal("malformed or legacy diagnostics were exposed")
		}
	}
}
