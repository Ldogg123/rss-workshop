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
		// An article body is fetched once and then left alone, and the item merge
		// preserves it, so a selector that grabbed the wrong block would survive
		// every later refresh and every edit. Clearing the stored bodies when the
		// article selector changes makes the editor a repair path: the next
		// refreshes refetch them with the new selector, or leave the list-page
		// description in place if the operator turned the feature off.
		cleared, err := s.articleSelectorChanged(ctx, tx, f)
		if err != nil {
			return "", err
		}
		if cleared {
			if _, err := tx.ExecContext(ctx, s.bind("UPDATE items SET content_full='' WHERE feed_id=?"), f.ID); err != nil {
				return "", err
			}
		}
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

// articleSelectorChanged reports whether the saved recipe asks for a different
// article body than the stored one does. Only the selector and attribute
// matter: the fetch mode changes how a page is retrieved, not which part of it
// becomes the story.
func (s *Store) articleSelectorChanged(ctx context.Context, tx *sql.Tx, f model.Feed) (bool, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, s.bind("SELECT recipe FROM feeds WHERE id=?"), f.ID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	var stored model.Recipe
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		// An unreadable stored recipe cannot be compared. Refetching is the safe
		// direction: it costs requests, never content.
		return true, nil
	}
	was, now := stored.Full, f.Recipe.Full
	if was == nil || now == nil {
		// One side asks for an article body and the other does not: either the
		// feature was just turned on, or it was turned off and the bodies it
		// left behind would otherwise keep being published.
		return (was == nil) != (now == nil), nil
	}
	return was.Selector != now.Selector || was.Attr != now.Attr, nil
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
