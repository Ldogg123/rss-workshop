package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	"rss-workshop/internal/diagnostics"
	"rss-workshop/internal/model"
)

//go:embed schema.sql
var schema string
var ErrStale = errors.New("recipe changed during refresh; result discarded")

type Store struct {
	DB       *sql.DB
	MaxItems int
	postgres bool
}

func Open(path string, maxItems int) (*Store, error) {
	return OpenWithOptions(path, maxItems, OpenOptions{})
}

// OpenWithOptions opens an SQLite database. Before upgrading an older schema it
// saves a verified copy beside the database, because an older release cannot
// open the upgraded one; if that copy fails, the database is left unmigrated.
func OpenWithOptions(path string, maxItems int, options OpenOptions) (*Store, error) {
	// Connection settings belong in the DSN, which the driver applies to every
	// connection it opens. Running them once is not enough: database/sql
	// replaces a connection whose statement was interrupted, for example by a
	// reader disconnecting mid-read, and the replacement would silently run
	// without foreign keys, so deleting a feed would leave its rows behind.
	absolute, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	// A file: URI with an escaped path, so a directory name containing ? or #
	// cannot cut the path short and drop the settings that follow it.
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "_foreign_keys=1&_busy_timeout=5000"}).String()
	db, e := sql.Open("sqlite", dsn)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	version, e := storedSchema(db)
	if e != nil {
		db.Close()
		return nil, e
	}
	if version >= 1 && version <= currentSchema {
		// Earlier releases could lose foreign key enforcement that way, so a
		// supported database may hold rows of feeds already deleted. They would
		// fail the copy's foreign key check below and block the upgrade.
		if err := removeOrphanedRows(db, version); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version >= 1 && version < currentSchema {
		if options.SkipUpgradeBackup {
			slog.Warn("upgrading database schema without an automatic backup", "from_schema", version, "to_schema", currentSchema)
		} else {
			saved, reused, err := backupBeforeUpgrade(db, path, version)
			if err != nil {
				db.Close()
				return nil, upgradeBackupError(version, err)
			}
			slog.Info("database copied before schema upgrade", "backup", saved, "reused_identical_copy", reused, "from_schema", version, "to_schema", currentSchema,
				"note", "an older release cannot open the upgraded database; restore this copy to roll back")
		}
	}
	if e = initializeSQLite(db); e != nil {
		db.Close()
		return nil, e
	}
	// Check the version before changing persistent journal settings, so an
	// unsupported database is left untouched by this older application.
	if _, e = db.Exec("PRAGMA journal_mode=WAL"); e != nil {
		db.Close()
		return nil, e
	}
	return &Store{DB: db, MaxItems: maxItems}, nil
}
func ID() string {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}

const columns = `id,rss_token,title,url,recipe,interval,enabled,next_run,last_attempt,last_success,error,failures,etag,modified,version,last_changed,(SELECT count(*) FROM items WHERE feed_id=feeds.id)`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (model.Feed, error) {
	var f model.Feed
	var r string
	var next, attempt, success, changed int64
	e := row.Scan(&f.ID, &f.RSSToken, &f.Title, &f.URL, &r, &f.Interval, &f.Enabled, &next, &attempt, &success, &f.Error, &f.Failures, &f.ETag, &f.LastModified, &f.Version, &changed, &f.Count)
	if e != nil {
		return f, e
	}
	e = json.Unmarshal([]byte(r), &f.Recipe)
	f.NextRun = stamp(next)
	f.LastAttempt = stamp(attempt)
	f.LastSuccess = stamp(success)
	f.LastChanged = stamp(changed)
	return f, e
}
func stamp(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// Source HTML can contain legacy-encoded bytes and NULs even after extraction.
// Persist readable text in a form both databases and XML readers can represent.
// This must never be applied to identity keys or conditional-fetch validators.
func readableText(value string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(value, "\uFFFD"), "\x00", "\uFFFD")
}

// PostgreSQL TEXT cannot represent arbitrary bytes. BYTEA preserves opaque
// keys and HTTP validators exactly; database/sql scans it back into strings.
func (s *Store) opaque(value string) any {
	if s.postgres {
		return []byte(value)
	}
	return value
}

func (s *Store) Get(ctx context.Context, id string) (_ model.Feed, err error) {
	defer s.cleanError(&err)
	f, err := scan(s.DB.QueryRowContext(ctx, s.bind("SELECT "+columns+" FROM feeds WHERE id=?"), id))
	if err != nil {
		return f, err
	}
	f.FilterIDs, err = s.feedFilterIDs(ctx, s.DB, id)
	return f, err
}
func (s *Store) List(ctx context.Context) (_ []model.Feed, err error) {
	defer s.cleanError(&err)
	rows, e := s.DB.QueryContext(ctx, "SELECT "+columns+" FROM feeds ORDER BY title,id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Feed{}
	for rows.Next() {
		f, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, s.attachFilterIDs(ctx, out)
}
func (s *Store) Save(ctx context.Context, f model.Feed) (_ string, err error) {
	return s.SaveWithOptions(ctx, f, SaveOptions{})
}
func (s *Store) Delete(ctx context.Context, id string) (err error) {
	defer s.cleanError(&err)
	_, e := s.DB.ExecContext(ctx, s.bind("DELETE FROM feeds WHERE id=?"), id)
	return e
}
func (s *Store) Queue(ctx context.Context, id string) (err error) {
	defer s.cleanError(&err)
	_, e := s.DB.ExecContext(ctx, s.bind("UPDATE feeds SET next_run=? WHERE id=?"), time.Now().Unix(), id)
	return e
}
func (s *Store) Items(ctx context.Context, id string) (_ []model.Item, err error) {
	defer s.cleanError(&err)
	rows, e := s.DB.QueryContext(ctx, s.bind(`SELECT key,guid,title,url,html,image,content_full,published,first_seen,last_seen,last_changed FROM items WHERE feed_id=? ORDER BY published DESC,key`), id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Item{}
	for rows.Next() {
		var it model.Item
		var p, f, l, c int64
		if e = rows.Scan(&it.Key, &it.GUID, &it.Title, &it.URL, &it.HTML, &it.Image, &it.FullHTML, &p, &f, &l, &c); e != nil {
			return nil, e
		}
		it.Published = stamp(p)
		it.FirstSeen = stamp(f)
		it.LastSeen = stamp(l)
		it.LastChanged = stamp(c)
		out = append(out, it)
	}
	return out, rows.Err()
}

// Complete checks the recipe version in the same transaction as the merge.
// PostgreSQL locks the feed row until commit so an edit or delete on another
// pooled connection cannot invalidate that check halfway through the merge.
func (s *Store) Complete(ctx context.Context, f model.Feed, items []model.Item, etag, modified string, status int, runErr error, retry time.Duration) (err error) {
	return s.CompleteWithDiagnostics(ctx, f, items, etag, modified, status, runErr, retry, nil)
}

// CompleteWithDiagnostics records the bounded trace with the same transaction
// as its run summary and saved items. Stale or deleted recipes leave no run.
func (s *Store) CompleteWithDiagnostics(ctx context.Context, f model.Feed, items []model.Item, etag, modified string, status int, runErr error, retry time.Duration, details *model.RunDiagnostics) (err error) {
	defer s.cleanError(&err)
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var version int
	var lastChanged int64
	versionQuery := "SELECT version,last_changed FROM feeds WHERE id=?"
	if s.postgres {
		versionQuery += " FOR UPDATE"
	}
	if e = tx.QueryRowContext(ctx, s.bind(versionQuery), f.ID).Scan(&version, &lastChanged); e != nil {
		return e
	}
	if version != f.Version {
		return ErrStale
	}
	now := time.Now().UTC()
	msg := ""
	failures := 0
	delay := time.Duration(f.Interval) * time.Second
	if runErr != nil {
		msg = diagnostics.SafeText(runErr.Error(), 1000)
		failures = f.Failures + 1
		delay = min(time.Duration(1<<min(failures, 10))*time.Minute, 24*time.Hour)
		delay = max(delay, retry)
	} else {
		// Output that changes gets a time strictly after the last one, so a
		// reader holding a Last-Modified from earlier in the same second still
		// sees the change; output that does not change keeps its time, so an
		// unchanged refresh -- including a 304 from the source -- publishes
		// identical bytes and headers.
		changedAt := max(now.Unix(), lastChanged+1)
		changed := false
		// Keys this merge inserted or changed, and whether each was inserted.
		// Whether they changed the output depends on retention below.
		touched := map[string]bool{}
		for _, it := range items {
			if it.Published.IsZero() {
				it.Published = now
			}
			guid := fmt.Sprintf("urn:sha256:%x", sha256.Sum256([]byte(f.ID+"\x00"+it.Key)))
			identity := "feed_id,key"
			if s.postgres {
				identity = "feed_id,guid"
			}
			// html and image are replaced every refresh so a late-published
			// preview image is picked up. content_full is only replaced when
			// this refresh actually fetched an article body; an empty value
			// means "not fetched this time", never "the article is empty".
			// last_changed moves only when a published field differs. A row not
			// merged since schema 5 first takes the last_seen it was rendered
			// with, so its published date does not move either.
			var itemChanged, firstSeen int64
			e = tx.QueryRowContext(ctx, s.bind(`INSERT INTO items(feed_id,key,guid,title,url,html,image,content_full,published,first_seen,last_seen,last_changed) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(`+identity+`) DO UPDATE SET title=excluded.title,url=excluded.url,html=excluded.html,image=excluded.image,content_full=CASE WHEN excluded.content_full='' THEN items.content_full ELSE excluded.content_full END,last_seen=excluded.last_seen,
last_changed=CASE WHEN items.title<>excluded.title OR items.url<>excluded.url OR items.html<>excluded.html OR items.image<>excluded.image OR (excluded.content_full<>'' AND excluded.content_full<>items.content_full) THEN excluded.last_changed WHEN items.last_changed=0 THEN items.last_seen ELSE items.last_changed END
RETURNING last_changed,first_seen`), f.ID, s.opaque(it.Key), guid, readableText(it.Title), readableText(it.URL), readableText(it.HTML), readableText(it.Image), readableText(it.FullHTML), it.Published.Unix(), now.Unix(), now.Unix(), changedAt).Scan(&itemChanged, &firstSeen)
			if e != nil {
				return e
			}
			if itemChanged == changedAt {
				touched[it.Key] = firstSeen == now.Unix()
			}
		}
		// Retention applies to everything already stored, not only to what this
		// refresh added, so lowering MAX_ITEMS deletes existing stories the next
		// time each feed refreshes. That is the one irreversible effect an
		// operator can cause by editing configuration, so say when it happens.
		pruned, e := tx.QueryContext(ctx, s.bind(`DELETE FROM items WHERE feed_id=? AND key NOT IN (SELECT key FROM items WHERE feed_id=? ORDER BY published DESC,key LIMIT ?) RETURNING key`), f.ID, f.ID, s.MaxItems)
		if e != nil {
			return e
		}
		removed := 0
		for pruned.Next() {
			var key string
			if e = pruned.Scan(&key); e != nil {
				pruned.Close()
				return e
			}
			removed++
			// A story inserted by this merge and removed again was never
			// published, as happens every refresh to a listed story older than
			// the retention window. Removing any other story changes output.
			if inserted, ok := touched[key]; !ok || !inserted {
				changed = true
			}
			delete(touched, key)
		}
		if e = pruned.Err(); e != nil {
			pruned.Close()
			return e
		}
		if e = pruned.Close(); e != nil {
			return e
		}
		// Inserted or changed stories that retention kept.
		changed = changed || len(touched) > 0
		if removed > 0 {
			slog.Info("stories removed by retention", "feed", f.Title, "feed_id", f.ID,
				"removed", removed, "max_items", s.MaxItems)
		}
		// A first success publishes output even when every story was filtered.
		if changed || lastChanged == 0 {
			lastChanged = changedAt
		}
		if _, e = tx.ExecContext(ctx, s.bind("UPDATE feeds SET last_success=?,etag=?,modified=?,last_changed=? WHERE id=?"), now.Unix(), s.opaque(etag), s.opaque(modified), lastChanged, f.ID); e != nil {
			return e
		}
	}
	// Stable per-feed jitter spreads due times without a shared random generator.
	jitter := time.Duration(sha256.Sum256([]byte(f.ID))[0]%30) * time.Second
	_, e = tx.ExecContext(ctx, s.bind("UPDATE feeds SET last_attempt=?,next_run=?,error=?,failures=? WHERE id=?"), now.Unix(), now.Add(delay+jitter).Unix(), msg, failures, f.ID)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, s.bind("INSERT INTO runs(feed_id,ended,status,count,error,diagnostics) VALUES(?,?,?,?,?,?)"), f.ID, now.Unix(), status, len(items), msg, encodeDiagnostics(details)); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, s.bind("DELETE FROM runs WHERE feed_id=? AND id NOT IN (SELECT id FROM runs WHERE feed_id=? ORDER BY id DESC LIMIT ?)"), f.ID, f.ID, MaxRuns); e != nil {
		return e
	}
	return tx.Commit()
}

func (s *Store) GetByToken(ctx context.Context, token string) (_ model.Feed, err error) {
	defer s.cleanError(&err)
	return scan(s.DB.QueryRowContext(ctx, s.bind("SELECT "+columns+" FROM feeds WHERE rss_token=?"), token))
}
func (s *Store) RotateToken(ctx context.Context, id string) (err error) {
	defer s.cleanError(&err)
	res, e := s.DB.ExecContext(ctx, s.bind("UPDATE feeds SET rss_token=? WHERE id=?"), ID(), id)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
