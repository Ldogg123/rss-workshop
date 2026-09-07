package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"rss-workshop/internal/model"
)

func TestImportCopiesConfigurationWithFreshPausedState(t *testing.T) {
	ctx := context.Background()
	s, err := testStoreOpener(t, 500)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	source := model.Feed{
		ID: "existing-id", RSSToken: "existing-token", Title: "Imported source", URL: "https://example.com/news", Interval: 1800,
		Recipe: model.Recipe{Mode: "browser", WaitSelector: "article.ready", SettleMS: 1200, Type: "css", Items: "article",
			Title: model.Field{Selector: "h2", Attr: "data-title"}, Link: model.Field{Selector: "a", Attr: "href"},
			Date: model.Field{Selector: "time", Attr: "datetime"}, Content: model.Field{Selector: ".summary"},
			Image: model.Field{Selector: "img", Attr: "data-src"}, DateLayout: "2006-01-02 15:04", Timezone: "Europe/London"},
		Enabled: true, NextRun: time.Now().Add(24 * time.Hour), LastAttempt: time.Now(), LastSuccess: time.Now(),
		Error: "old failure", Failures: 5, ETag: "old-etag", LastModified: "old-modified", Version: 42, Count: 12,
		RSSURL: "https://old.example/feeds/old.xml", AtomURL: "https://old.example/feeds/old.atom",
	}
	ids, err := s.Import(ctx, []model.Feed{source, source})
	if err != nil || len(ids) != 2 {
		t.Fatalf("import = %v, %v", ids, err)
	}
	seen := map[string]bool{source.ID: true, source.RSSToken: true}
	for _, id := range ids {
		got, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, identity := range []string{got.ID, got.RSSToken} {
			if len(identity) != 48 || seen[identity] {
				t.Fatalf("identity was not newly generated: %q", identity)
			}
			seen[identity] = true
		}
		if got.Title != source.Title || got.URL != source.URL || got.Interval != source.Interval || !reflect.DeepEqual(got.Recipe, source.Recipe) {
			t.Fatalf("recipe configuration changed: %+v", got)
		}
		if got.Enabled || !got.LastAttempt.IsZero() || !got.LastSuccess.IsZero() || got.Error != "" || got.Failures != 0 || got.ETag != "" || got.LastModified != "" || got.Count != 0 || got.Version != 1 || got.RSSURL != "" || got.AtomURL != "" {
			t.Fatalf("import retained runtime state: %+v", got)
		}
		if got.NextRun.Equal(source.NextRun) {
			t.Fatal("import copied the old schedule")
		}
		items, err := s.Items(ctx, id)
		if err != nil || len(items) != 0 {
			t.Fatalf("new feed history = %v, %v", items, err)
		}
		var runs int
		if err := s.DB.QueryRowContext(ctx, s.bind("SELECT count(*) FROM runs WHERE feed_id=?"), id).Scan(&runs); err != nil || runs != 0 {
			t.Fatalf("new feed run count = %d, %v", runs, err)
		}
	}
}

func TestImportRollsBackEarlierInsertOnDatabaseFailure(t *testing.T) {
	ctx := context.Background()
	s, err := testStoreOpener(t, 500)()
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	baseline := model.Feed{Title: "Existing feed", URL: "https://example.com/existing", Interval: 60, Enabled: true}
	if _, err := s.Save(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	before, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The first row succeeds; a database constraint failure on the second
	// exercises rollback after writes have occurred.
	reject := `CREATE TRIGGER reject_second_import BEFORE INSERT ON feeds WHEN NEW.title = 'Reject this feed' BEGIN SELECT RAISE(ABORT, 'fixture insert failure'); END`
	dropReject := "DROP TRIGGER reject_second_import"
	if s.postgres {
		reject = `ALTER TABLE feeds ADD CONSTRAINT reject_second_import CHECK(title <> 'Reject this feed')`
		dropReject = "ALTER TABLE feeds DROP CONSTRAINT reject_second_import"
	}
	if _, err := s.DB.ExecContext(ctx, reject); err != nil {
		t.Fatal(err)
	}
	feeds := []model.Feed{
		{Title: "Would be inserted first", URL: "https://example.com/first", Interval: 60},
		{Title: "Reject this feed", URL: "https://example.com/second", Interval: 60},
	}
	if ids, err := s.Import(ctx, feeds); err == nil || len(ids) != 0 {
		t.Fatalf("failed import returned committed IDs: %v, %v", ids, err)
	}
	after, err := s.List(ctx)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("failed import changed existing library: before=%+v after=%+v err=%v", before, after, err)
	}
	if _, err := s.DB.ExecContext(ctx, dropReject); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.Import(ctx, feeds); err != nil || len(ids) != 2 {
		t.Fatalf("database not usable after rollback: %v, %v", ids, err)
	}
}
