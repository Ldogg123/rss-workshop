package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"rss-workshop/internal/filter"
	"rss-workshop/internal/model"
)

type SaveOptions struct {
	// ApplyFiltersToHistory removes saved nonmatches when editing a feed. The
	// option applies only to this save and is never persisted in its recipe.
	ApplyFiltersToHistory bool
}

const historyFilterTimeout = 5 * time.Second

// SaveWithOptions updates the recipe and optional historical pruning together.
// Updating the feed row also holds PostgreSQL's refresh/version lock until the
// transaction ends, so an old in-flight refresh cannot restore pruned items.
func (s *Store) SaveWithOptions(ctx context.Context, f model.Feed, options SaveOptions) (_ string, err error) {
	matcher, err := filter.Compile(f.Recipe.Filters)
	if err != nil {
		return "", err
	}
	recipe, err := json.Marshal(f.Recipe)
	if err != nil {
		return "", err
	}
	defer s.cleanError(&err)
	prune := f.ID != "" && options.ApplyFiltersToHistory && f.Recipe.Filters != nil && (f.Recipe.Filters.Include != nil || f.Recipe.Filters.Exclude != nil)
	if prune {
		// HTTP write deadlines do not cancel request contexts. Bound the whole
		// pruning transaction independently, including waiting for its locks.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, historyFilterTimeout)
		defer cancel()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if f.ID == "" {
		f.ID = ID()
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run) VALUES(?,?,?,?,?,?,?,?)`), f.ID, ID(), readableText(f.Title), readableText(f.URL), string(recipe), f.Interval, f.Enabled, time.Now().Unix()); err != nil {
			return "", err
		}
	} else {
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE feeds SET title=?,url=?,recipe=?,interval=?,enabled=?,next_run=?,etag='',modified='',error='',failures=0,version=version+1 WHERE id=?`), readableText(f.Title), readableText(f.URL), string(recipe), f.Interval, f.Enabled, time.Now().Unix(), f.ID)
		if err != nil {
			return "", err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", sql.ErrNoRows
		}
		if prune {
			if err := s.pruneFilteredHistory(ctx, tx, f.ID, matcher); err != nil {
				return "", err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return f.ID, nil
}

func (s *Store) pruneFilteredHistory(ctx context.Context, tx *sql.Tx, feedID string, matcher *filter.Matcher) error {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT key,title,url,html FROM items WHERE feed_id=? LIMIT 10001`), feedID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var removed []string
	count := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 10000 {
			return errors.New("saved history exceeds 10000 items; cannot apply filters")
		}
		var item model.Item
		if err := rows.Scan(&item.Key, &item.Title, &item.URL, &item.HTML); err != nil {
			return err
		}
		if !matcher.Match(item) {
			removed = append(removed, item.Key)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// Close the cursor before mutations, and keep every query under ordinary
	// driver parameter limits. Keys remain opaque bytes on PostgreSQL.
	for len(removed) > 0 {
		batch := removed[:min(len(removed), 250)]
		args := make([]any, 1, len(batch)+1)
		args[0] = feedID
		for _, key := range batch {
			args = append(args, s.opaque(key))
		}
		query := `DELETE FROM items WHERE feed_id=? AND key IN (` + strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + `)`
		result, err := tx.ExecContext(ctx, s.bind(query), args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != int64(len(batch)) {
			return errors.New("saved history changed while applying filters")
		}
		removed = removed[len(batch):]
	}
	return nil
}
