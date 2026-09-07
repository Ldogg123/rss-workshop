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
	"strings"
	"time"

	_ "modernc.org/sqlite"
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
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", schema} {
		if _, e = db.Exec(q); e != nil {
			db.Close()
			return nil, e
		}
	}
	var version int
	if e = db.QueryRow("SELECT version FROM schema_version").Scan(&version); e != nil || version != 1 {
		db.Close()
		return nil, fmt.Errorf("unsupported database schema version")
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

const columns = `id,rss_token,title,url,recipe,interval,enabled,next_run,last_attempt,last_success,error,failures,etag,modified,version,(SELECT count(*) FROM items WHERE feed_id=feeds.id)`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (model.Feed, error) {
	var f model.Feed
	var r string
	var next, attempt, success int64
	e := row.Scan(&f.ID, &f.RSSToken, &f.Title, &f.URL, &r, &f.Interval, &f.Enabled, &next, &attempt, &success, &f.Error, &f.Failures, &f.ETag, &f.LastModified, &f.Version, &f.Count)
	if e != nil {
		return f, e
	}
	e = json.Unmarshal([]byte(r), &f.Recipe)
	f.NextRun = stamp(next)
	f.LastAttempt = stamp(attempt)
	f.LastSuccess = stamp(success)
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
	return scan(s.DB.QueryRowContext(ctx, s.bind("SELECT "+columns+" FROM feeds WHERE id=?"), id))
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
	return out, rows.Err()
}
func (s *Store) Save(ctx context.Context, f model.Feed) (_ string, err error) {
	defer s.cleanError(&err)
	r, e := json.Marshal(f.Recipe)
	if e != nil {
		return "", e
	}
	if f.ID == "" {
		f.ID = ID()
		_, e = s.DB.ExecContext(ctx, s.bind(`INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run) VALUES(?,?,?,?,?,?,?,?)`), f.ID, ID(), readableText(f.Title), readableText(f.URL), string(r), f.Interval, f.Enabled, time.Now().Unix())
		return f.ID, e
	}
	res, e := s.DB.ExecContext(ctx, s.bind(`UPDATE feeds SET title=?,url=?,recipe=?,interval=?,enabled=?,next_run=?,etag='',modified='',error='',failures=0,version=version+1 WHERE id=?`), readableText(f.Title), readableText(f.URL), string(r), f.Interval, f.Enabled, time.Now().Unix(), f.ID)
	if e != nil {
		return "", e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", sql.ErrNoRows
	}
	return f.ID, nil
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
	rows, e := s.DB.QueryContext(ctx, s.bind(`SELECT key,guid,title,url,html,image,published,first_seen,last_seen FROM items WHERE feed_id=? ORDER BY published DESC,key`), id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Item{}
	for rows.Next() {
		var it model.Item
		var p, f, l int64
		if e = rows.Scan(&it.Key, &it.GUID, &it.Title, &it.URL, &it.HTML, &it.Image, &p, &f, &l); e != nil {
			return nil, e
		}
		it.Published = stamp(p)
		it.FirstSeen = stamp(f)
		it.LastSeen = stamp(l)
		out = append(out, it)
	}
	return out, rows.Err()
}

// Complete checks the recipe version in the same transaction as the merge.
// PostgreSQL locks the feed row until commit so an edit or delete on another
// pooled connection cannot invalidate that check halfway through the merge.
func (s *Store) Complete(ctx context.Context, f model.Feed, items []model.Item, etag, modified string, status int, runErr error, retry time.Duration) (err error) {
	defer s.cleanError(&err)
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var version int
	versionQuery := "SELECT version FROM feeds WHERE id=?"
	if s.postgres {
		versionQuery += " FOR UPDATE"
	}
	if e = tx.QueryRowContext(ctx, s.bind(versionQuery), f.ID).Scan(&version); e != nil {
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
		msg = readableText(runErr.Error())
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
		// Truncation can split UTF-8; PostgreSQL text requires valid encoding.
		msg = strings.ToValidUTF8(msg, "")
		failures = f.Failures + 1
		delay = min(time.Duration(1<<min(failures, 10))*time.Minute, 24*time.Hour)
		delay = max(delay, retry)
	} else {
		for _, it := range items {
			if it.Published.IsZero() {
				it.Published = now
			}
			guid := fmt.Sprintf("urn:sha256:%x", sha256.Sum256([]byte(f.ID+"\x00"+it.Key)))
			identity := "feed_id,key"
			if s.postgres {
				identity = "feed_id,guid"
			}
			_, e = tx.ExecContext(ctx, s.bind(`INSERT INTO items(feed_id,key,guid,title,url,html,image,published,first_seen,last_seen) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(`+identity+`) DO UPDATE SET title=excluded.title,url=excluded.url,html=excluded.html,image=excluded.image,last_seen=excluded.last_seen`), f.ID, s.opaque(it.Key), guid, readableText(it.Title), readableText(it.URL), readableText(it.HTML), readableText(it.Image), it.Published.Unix(), now.Unix(), now.Unix())
			if e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, s.bind(`DELETE FROM items WHERE feed_id=? AND key NOT IN (SELECT key FROM items WHERE feed_id=? ORDER BY published DESC,key LIMIT ?)`), f.ID, f.ID, s.MaxItems); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, s.bind("UPDATE feeds SET last_success=?,etag=?,modified=? WHERE id=?"), now.Unix(), s.opaque(etag), s.opaque(modified), f.ID); e != nil {
			return e
		}
	}
	// Stable per-feed jitter spreads due times without a shared random generator.
	jitter := time.Duration(sha256.Sum256([]byte(f.ID))[0]%30) * time.Second
	_, e = tx.ExecContext(ctx, s.bind("UPDATE feeds SET last_attempt=?,next_run=?,error=?,failures=? WHERE id=?"), now.Unix(), now.Add(delay+jitter).Unix(), msg, failures, f.ID)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, s.bind("INSERT INTO runs(feed_id,ended,status,count,error) VALUES(?,?,?,?,?)"), f.ID, now.Unix(), status, len(items), msg); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, s.bind("DELETE FROM runs WHERE feed_id=? AND id NOT IN (SELECT id FROM runs WHERE feed_id=? ORDER BY id DESC LIMIT 50)"), f.ID, f.ID); e != nil {
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
