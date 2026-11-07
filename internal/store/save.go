package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"rss-workshop/internal/filter"
	"rss-workshop/internal/model"
)

type SaveOptions struct {
	// ApplyFiltersToHistory removes saved nonmatches when editing a feed, or
	// from every feed using an edited library filter. The option applies only
	// to this save and is never persisted in its recipe.
	ApplyFiltersToHistory bool
	// Validate checks a feed with its library filters merged into the recipe,
	// inside the save transaction. Callers pass the same validation they apply
	// to a submitted feed, so merged rules stay within the limits a recipe
	// export must satisfy to be imported again.
	Validate func(model.Feed) error
	// KeepEnabled saves an existing feed without changing whether it refreshes
	// automatically, for an editor whose setting the operator left alone. A
	// pause or resume made from the library while the editor was open then
	// survives the editor's save instead of being undone by its stale copy.
	KeepEnabled bool
}

const historyFilterTimeout = 5 * time.Second

// SaveWithOptions updates the recipe and optional historical pruning together.
// Updating the feed row also holds PostgreSQL's refresh/version lock until the
// transaction ends, so an old in-flight refresh cannot restore pruned items.
//
// FilterIDs replaces the feed's library filters when it is non-nil. A nil list
// keeps the current ones, so a client that predates the library, or only
// pauses a feed, cannot unlink filters by omitting the field.
func (s *Store) SaveWithOptions(ctx context.Context, f model.Feed, options SaveOptions) (_ string, err error) {
	if _, err := filter.Compile(f.Recipe.Filters); err != nil {
		return "", err
	}
	recipe, err := json.Marshal(f.Recipe)
	if err != nil {
		return "", err
	}
	defer s.cleanError(&err)
	// Whether pruning happens depends on the merged rules, which are only known
	// inside the transaction. Bound every editing save that asks for it.
	prune := f.ID != "" && options.ApplyFiltersToHistory
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
	links := f.FilterIDs
	if links == nil && f.ID != "" {
		if f.FilterIDs, err = s.feedFilterIDs(ctx, tx, f.ID); err != nil {
			return "", err
		}
	}
	// Read (and on PostgreSQL share-lock) the library filters before the feed
	// row, the same order a library filter edit takes its locks in.
	effective, matcher, err := s.effective(ctx, tx, f, true, options)
	if err != nil {
		return "", err
	}
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
		cleared, err := s.articleBodiesChanged(ctx, tx, f)
		if err != nil {
			return "", err
		}
		// Output changes an edit makes before any refresh must still move the
		// feed's modification time, or a reader holding Last-Modified keeps
		// getting 304 for a renamed, cleared or pruned feed that is paused. The
		// interval is published only as whole minutes of ttl.
		now := time.Now().Unix()
		title, url := readableText(f.Title), readableText(f.URL)
		args := []any{title, url, f.Interval / 60, now, now, title, url, string(recipe), f.Interval}
		enabled := ""
		if !options.KeepEnabled {
			enabled = "enabled=?,"
			args = append(args, f.Enabled)
		}
		args = append(args, now, f.ID)
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE feeds SET last_changed=CASE WHEN title<>? OR url<>? OR interval/60<>? THEN `+bumpChanged+` ELSE last_changed END,
title=?,url=?,recipe=?,interval=?,`+enabled+`next_run=?,etag='',modified='',error='',failures=0,version=version+1 WHERE id=?`), args...)
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
		// Clear only while holding the feed row. On PostgreSQL a refresh merging
		// bodies from the old selector holds that lock; clearing before it would
		// miss the rows it has not committed yet, and they would survive.
		if cleared != keepBodies {
			if err := s.clearArticleBodies(ctx, tx, f.ID, cleared == clearListedBodies); err != nil {
				return "", err
			}
		}
		if links == nil {
			// The links kept were read before this row was locked. If a concurrent
			// save changed them while this one waited, the rules validated above
			// are not the ones that would be stored, so let the operator retry.
			current, err := s.feedFilterIDs(ctx, tx, f.ID)
			if err != nil {
				return "", err
			}
			if !slices.Equal(current, f.FilterIDs) {
				return "", invalid(errors.New("this feed's library filters changed while saving; reload and try again"))
			}
		}
		if prune && hasRules(effective.Recipe.Filters) {
			removed, err := s.pruneFilteredHistory(ctx, tx, f.ID, matcher)
			if err != nil {
				return "", err
			}
			if removed > 0 {
				if err := s.markChanged(ctx, tx, f.ID); err != nil {
					return "", err
				}
			}
		}
	}
	if links != nil {
		if err := s.linkFilters(ctx, tx, f.ID, links); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return f.ID, nil
}

// bodyClear says which stored article bodies an edit discards.
type bodyClear int

const (
	keepBodies bodyClear = iota
	// clearListedBodies discards the bodies of the stories the latest stored
	// merge listed, which the next refreshes can fetch again.
	clearListedBodies
	clearAllBodies
)

// articleBodiesChanged reports whether the saved recipe asks for a different
// article body than the stored one does. The selector and attribute choose the
// block, so changing either discards every body. The fetch mode changes how a
// page is retrieved, not which part of it becomes the story.
//
// Cleanup rules only trim that block, so changing them discards just the
// bodies of the stories still listed. Older stories keep the body they have:
// they are no longer on the list page, so a discarded body would never be
// fetched again, and stored bodies have lost the class names the rules match,
// so they cannot be cleaned again in place. Rules do not apply to a body read
// from an attribute.
func (s *Store) articleBodiesChanged(ctx context.Context, tx *sql.Tx, f model.Feed) (bodyClear, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, s.bind("SELECT recipe FROM feeds WHERE id=?"), f.ID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return keepBodies, nil
		}
		return keepBodies, err
	}
	var stored model.Recipe
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		// An unreadable stored recipe cannot be compared. Refetching is the safe
		// direction: it costs requests, never content.
		return clearAllBodies, nil
	}
	was, now := stored.Full, f.Recipe.Full
	switch {
	case was == nil || now == nil:
		// One side asks for an article body and the other does not: either the
		// feature was just turned on, or it was turned off and the bodies it
		// left behind would otherwise keep being published.
		if (was == nil) != (now == nil) {
			return clearAllBodies, nil
		}
	case was.Selector != now.Selector || was.Attr != now.Attr:
		return clearAllBodies, nil
	case now.Attr == "" && (!slices.Equal(stored.Remove, f.Recipe.Remove) || len(f.Recipe.Remove) > 0 && stored.Type != f.Recipe.Type):
		return clearListedBodies, nil
	}
	return keepBodies, nil
}

// bumpChanged is the modification time for a feed whose output just changed:
// now, or one second past the previous value when that is not earlier, so a
// Last-Modified from the same second is never mistaken for the new output.
// It takes now twice as parameters.
const bumpChanged = `CASE WHEN last_changed>=? THEN last_changed+1 ELSE ? END`

// markChanged records that an edit changed a feed's published output.
func (s *Store) markChanged(ctx context.Context, tx *sql.Tx, feedID string) error {
	now := time.Now().Unix()
	_, err := tx.ExecContext(ctx, s.bind("UPDATE feeds SET last_changed="+bumpChanged+" WHERE id=?"), now, now, feedID)
	return err
}

// clearArticleBodies discards a feed's stored article bodies, or with
// listedOnly those of the stories the latest stored merge listed. Each cleared
// story takes the feed's next change time rather than the clock, so its Atom
// updated moves forward even when earlier changes in the same second already
// pushed the feed's time ahead.
func (s *Store) clearArticleBodies(ctx context.Context, tx *sql.Tx, feedID string, listedOnly bool) error {
	now := time.Now().Unix()
	var changedAt int64
	if err := tx.QueryRowContext(ctx, s.bind("SELECT "+bumpChanged+" FROM feeds WHERE id=?"), now, now, feedID).Scan(&changedAt); err != nil {
		return err
	}
	query, args := "UPDATE items SET content_full='',last_changed=? WHERE feed_id=? AND content_full<>''", []any{changedAt, feedID}
	if listedOnly {
		// A merge gives every story it lists its own time as last_seen. This is
		// not last_success: a 304, or a refresh whose stories were all
		// filtered out, succeeds without merging a story or moving last_seen.
		query += " AND last_seen>=(SELECT COALESCE(MAX(last_seen),0) FROM items WHERE feed_id=?)"
		args = append(args, feedID)
	}
	result, err := tx.ExecContext(ctx, s.bind(query), args...)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, s.bind("UPDATE feeds SET last_changed=? WHERE id=?"), changedAt, feedID)
	return err
}

// pruneFilteredHistory removes stored stories the matcher rejects and reports
// how many it removed.
func (s *Store) pruneFilteredHistory(ctx context.Context, tx *sql.Tx, feedID string, matcher *filter.Matcher) (int, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT key,title,url,html FROM items WHERE feed_id=? LIMIT 10001`), feedID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var removed []string
	count := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		count++
		if count > 10000 {
			return 0, errors.New("saved history exceeds 10000 items; cannot apply filters")
		}
		var item model.Item
		if err := rows.Scan(&item.Key, &item.Title, &item.URL, &item.HTML); err != nil {
			return 0, err
		}
		if !matcher.Match(item) {
			removed = append(removed, item.Key)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	// Close the cursor before mutations, and keep every query under ordinary
	// driver parameter limits. Keys remain opaque bytes on PostgreSQL.
	total := len(removed)
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
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n != int64(len(batch)) {
			return 0, errors.New("saved history changed while applying filters")
		}
		removed = removed[len(batch):]
	}
	return total, nil
}
