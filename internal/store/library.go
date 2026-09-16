package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"rss-workshop/internal/filter"
	"rss-workshop/internal/model"
)

const (
	// MaxLibraryFilters bounds the library the editor lists on every open.
	MaxLibraryFilters = 200
	// MaxFiltersPerFeed bounds the lookups a refresh makes before fetching.
	// The merged rules must still fit the ordinary filter limits.
	MaxFiltersPerFeed = 16
)

var (
	ErrFilterInUse   = errors.New("library filter is used by feeds; remove it from those feeds first")
	ErrUnknownFilter = errors.New("a selected library filter no longer exists; reload and choose again")
)

// InvalidError is a save the store rejected because of what was submitted,
// such as merged rules that exceed the filter limits, rather than a database
// failure. Its message is written for the operator.
type InvalidError struct{ Err error }

func (e *InvalidError) Error() string { return e.Err.Error() }
func (e *InvalidError) Unwrap() error { return e.Err }

func invalid(err error) error { return &InvalidError{Err: err} }

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Filters lists the library by name, with the feeds using each filter.
func (s *Store) Filters(ctx context.Context) (_ []model.LibraryFilter, err error) {
	defer s.cleanError(&err)
	rows, err := s.DB.QueryContext(ctx, "SELECT id,name,rules FROM filters ORDER BY name,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.LibraryFilter{}
	index := map[string]int{}
	for rows.Next() {
		var lf model.LibraryFilter
		var rules string
		if err := rows.Scan(&lf.ID, &lf.Name, &rules); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(rules), &lf.Filters); err != nil {
			return nil, err
		}
		lf.FeedIDs = []string{}
		index[lf.ID] = len(out)
		out = append(out, lf)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	links, err := s.DB.QueryContext(ctx, "SELECT filter_id,feed_id FROM feed_filters ORDER BY feed_id")
	if err != nil {
		return nil, err
	}
	defer links.Close()
	for links.Next() {
		var filterID, feedID string
		if err := links.Scan(&filterID, &feedID); err != nil {
			return nil, err
		}
		if i, ok := index[filterID]; ok {
			out[i].FeedIDs = append(out[i].FeedIDs, feedID)
		}
	}
	return out, links.Err()
}

// SaveFilter creates a library filter, or edits one and applies the change to
// every feed using it in the same transaction. Each of those feeds must still
// pass options.Validate with its merged rules, gets a new recipe version so an
// in-flight refresh with the old rules is discarded, and loses its conditional
// request validators so the next refresh reapplies the rules to a full page
// rather than keeping the old result on a 304.
func (s *Store) SaveFilter(ctx context.Context, lf model.LibraryFilter, options SaveOptions) (_ string, err error) {
	if _, err := filter.Compile(&lf.Filters); err != nil {
		return "", invalid(err)
	}
	if lf.Filters.Include == nil && lf.Filters.Exclude == nil {
		return "", invalid(errors.New("a library filter needs at least one include or exclude rule"))
	}
	rules, err := json.Marshal(lf.Filters)
	if err != nil {
		return "", err
	}
	defer s.cleanError(&err)
	prune := lf.ID != "" && options.ApplyFiltersToHistory
	if prune {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, historyFilterTimeout)
		defer cancel()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	name := readableText(strings.TrimSpace(lf.Name))
	if lf.ID == "" {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM filters").Scan(&count); err != nil {
			return "", err
		}
		if count >= MaxLibraryFilters {
			return "", invalid(fmt.Errorf("the filter library is limited to %d filters", MaxLibraryFilters))
		}
		lf.ID = ID()
		if _, err := tx.ExecContext(ctx, s.bind("INSERT INTO filters(id,name,rules) VALUES(?,?,?)"), lf.ID, name, string(rules)); err != nil {
			return "", err
		}
		return lf.ID, tx.Commit()
	}
	// Updating the filter row first means a concurrent feed save, which locks
	// its library filters before its feed row, waits here instead of deadlocking.
	// It also makes the linked-feed list below include any link such a save
	// committed while this update waited.
	result, err := tx.ExecContext(ctx, s.bind("UPDATE filters SET name=?,rules=? WHERE id=?"), name, string(rules), lf.ID)
	if err != nil {
		return "", err
	}
	if n, err := result.RowsAffected(); err != nil {
		return "", err
	} else if n == 0 {
		return "", sql.ErrNoRows
	}
	feedIDs, err := s.filterFeeds(ctx, tx, lf.ID)
	if err != nil {
		return "", err
	}
	// Feed rows are locked in ID order, the same order every concurrent filter
	// edit uses, so two edits sharing feeds cannot wait on each other. Each row
	// is locked before anything about the feed is read: a concurrent save or
	// edit of the same feed then commits first, and the reads below see its
	// links and its other library filters, so the rules validated and used for
	// pruning are the ones this transaction stores.
	for _, feedID := range feedIDs {
		f, err := s.lockFeed(ctx, tx, feedID)
		if errors.Is(err, sql.ErrNoRows) {
			continue // deleted since the list was read
		}
		if err != nil {
			return "", err
		}
		if f.FilterIDs, err = s.feedFilterIDs(ctx, tx, feedID); err != nil {
			return "", err
		}
		if !slices.Contains(f.FilterIDs, lf.ID) {
			continue // a concurrent save removed this filter from the feed
		}
		effective, matcher, err := s.effective(ctx, tx, f, false, options)
		if err != nil {
			var bad *InvalidError
			if errors.As(err, &bad) {
				return "", invalid(fmt.Errorf("feed %q: %w", f.Title, bad.Err))
			}
			return "", err
		}
		if _, err := tx.ExecContext(ctx, s.bind("UPDATE feeds SET etag='',modified='',next_run=?,version=version+1 WHERE id=?"), time.Now().Unix(), feedID); err != nil {
			return "", err
		}
		if prune && hasRules(effective.Recipe.Filters) {
			if err := s.pruneFilteredHistory(ctx, tx, feedID, matcher); err != nil {
				return "", err
			}
		}
	}
	return lf.ID, tx.Commit()
}

// DeleteFilter removes a library filter that no feed uses. Deleting one in use
// would silently change what those feeds collect, so the operator removes it
// from each feed first.
func (s *Store) DeleteFilter(ctx context.Context, id string) (err error) {
	defer s.cleanError(&err)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Lock the filter, then look for links in a separate statement. A feed save
	// linking it holds a share lock, so on PostgreSQL this waits for that save
	// and the check then sees its link; a single DELETE ... NOT EXISTS would use
	// the snapshot from before the wait and fail on the foreign key instead.
	lock := "SELECT id FROM filters WHERE id=?"
	if s.postgres {
		lock += " FOR UPDATE"
	}
	if err := tx.QueryRowContext(ctx, s.bind(lock), id).Scan(&id); err != nil {
		return err
	}
	var used bool
	if err := tx.QueryRowContext(ctx, s.bind("SELECT EXISTS(SELECT 1 FROM feed_filters WHERE filter_id=?)"), id).Scan(&used); err != nil {
		return err
	}
	if used {
		return ErrFilterInUse
	}
	if _, err := tx.ExecContext(ctx, s.bind("DELETE FROM filters WHERE id=?"), id); err != nil {
		return err
	}
	return tx.Commit()
}

// EffectiveRecipe returns the recipe a refresh, preview or export evaluates:
// the feed's own rules merged with the library filters in f.FilterIDs.
func (s *Store) EffectiveRecipe(ctx context.Context, f model.Feed) (_ model.Recipe, err error) {
	defer s.cleanError(&err)
	if len(f.FilterIDs) == 0 {
		return f.Recipe, nil
	}
	shared, err := s.libraryRules(ctx, s.DB, f.FilterIDs, false)
	if err != nil {
		return f.Recipe, err
	}
	r := f.Recipe
	r.Filters = filter.Combine(r.Filters, shared)
	return r, nil
}

// effective merges and validates f's rules inside a save transaction. A feed
// save passes lock, which holds the library filters it read until commit on
// PostgreSQL so an edit to one of them waits for this validation. A library
// filter edit instead holds the feed row, which serializes it with every other
// edit and save touching that feed.
func (s *Store) effective(ctx context.Context, tx *sql.Tx, f model.Feed, lock bool, options SaveOptions) (model.Feed, *filter.Matcher, error) {
	shared, err := s.libraryRules(ctx, tx, f.FilterIDs, lock)
	if err != nil {
		return f, nil, err
	}
	f.Recipe.Filters = filter.Combine(f.Recipe.Filters, shared)
	matcher, err := filter.Compile(f.Recipe.Filters)
	if err != nil {
		if len(shared) > 0 {
			err = fmt.Errorf("with its library filters, %w", err)
		}
		return f, nil, invalid(err)
	}
	if options.Validate != nil && len(shared) > 0 {
		if err := options.Validate(f); err != nil {
			return f, nil, invalid(fmt.Errorf("with its library filters, %w", err))
		}
	}
	return f, matcher, nil
}

func (s *Store) libraryRules(ctx context.Context, q querier, ids []string, lock bool) ([]model.FilterSet, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > MaxFiltersPerFeed {
		return nil, invalid(fmt.Errorf("a feed can use at most %d library filters", MaxFiltersPerFeed))
	}
	args := make([]any, len(ids))
	seen := make(map[string]bool, len(ids))
	for i, id := range ids {
		if seen[id] {
			return nil, invalid(errors.New("a library filter is selected more than once"))
		}
		seen[id] = true
		args[i] = id
	}
	query := "SELECT id,rules FROM filters WHERE id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ") ORDER BY id"
	if lock && s.postgres {
		query += " FOR SHARE"
	}
	rows, err := q.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make(map[string]model.FilterSet, len(ids))
	for rows.Next() {
		var id, rules string
		if err := rows.Scan(&id, &rules); err != nil {
			return nil, err
		}
		var set model.FilterSet
		if err := json.Unmarshal([]byte(rules), &set); err != nil {
			return nil, err
		}
		found[id] = set
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]model.FilterSet, 0, len(ids))
	for _, id := range ids {
		set, ok := found[id]
		if !ok {
			return nil, invalid(ErrUnknownFilter)
		}
		out = append(out, set)
	}
	return out, nil
}

// lockFeed reads a feed while holding its row lock until the transaction ends.
// SQLite runs one transaction at a time, so only PostgreSQL needs the lock.
func (s *Store) lockFeed(ctx context.Context, tx *sql.Tx, feedID string) (model.Feed, error) {
	if s.postgres {
		var id string
		if err := tx.QueryRowContext(ctx, s.bind("SELECT id FROM feeds WHERE id=? FOR UPDATE"), feedID).Scan(&id); err != nil {
			return model.Feed{}, err
		}
	}
	return scan(tx.QueryRowContext(ctx, s.bind("SELECT "+columns+" FROM feeds WHERE id=?"), feedID))
}

func (s *Store) feedFilterIDs(ctx context.Context, q querier, feedID string) ([]string, error) {
	return s.strings(ctx, q, "SELECT filter_id FROM feed_filters WHERE feed_id=? ORDER BY position", feedID)
}

func (s *Store) filterFeeds(ctx context.Context, q querier, filterID string) ([]string, error) {
	return s.strings(ctx, q, "SELECT feed_id FROM feed_filters WHERE filter_id=? ORDER BY feed_id", filterID)
}

func (s *Store) strings(ctx context.Context, q querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

// linkFilters replaces a feed's library filter list in the save transaction.
func (s *Store) linkFilters(ctx context.Context, tx *sql.Tx, feedID string, ids []string) error {
	if _, err := tx.ExecContext(ctx, s.bind("DELETE FROM feed_filters WHERE feed_id=?"), feedID); err != nil {
		return err
	}
	for position, id := range ids {
		if _, err := tx.ExecContext(ctx, s.bind("INSERT INTO feed_filters(feed_id,filter_id,position) VALUES(?,?,?)"), feedID, id, position); err != nil {
			return err
		}
	}
	return nil
}

// attachFilterIDs fills FilterIDs for listed feeds with one query.
func (s *Store) attachFilterIDs(ctx context.Context, feeds []model.Feed) error {
	index := make(map[string]int, len(feeds))
	for i := range feeds {
		feeds[i].FilterIDs = []string{}
		index[feeds[i].ID] = i
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT feed_id,filter_id FROM feed_filters ORDER BY feed_id,position")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var feedID, filterID string
		if err := rows.Scan(&feedID, &filterID); err != nil {
			return err
		}
		if i, ok := index[feedID]; ok {
			feeds[i].FilterIDs = append(feeds[i].FilterIDs, filterID)
		}
	}
	return rows.Err()
}

func hasRules(set *model.FilterSet) bool {
	return set != nil && (set.Include != nil || set.Exclude != nil)
}
