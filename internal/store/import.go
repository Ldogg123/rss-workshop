package store

import (
	"context"
	"encoding/json"
	"time"

	"rss-workshop/internal/filter"
	"rss-workshop/internal/model"
)

// Import creates an entire set of new, paused feeds in one transaction. Only
// recipe configuration is copied; identities, links and run state are fresh.
func (s *Store) Import(ctx context.Context, feeds []model.Feed) (_ []string, err error) {
	recipes := make([][]byte, len(feeds))
	for i, f := range feeds {
		if _, err := filter.Compile(f.Recipe.Filters); err != nil {
			return nil, err
		}
		var err error
		recipes[i], err = json.Marshal(f.Recipe)
		if err != nil {
			return nil, err
		}
	}
	defer s.cleanError(&err)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, s.bind(`INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run) VALUES(?,?,?,?,?,?,?,?)`))
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	ids := make([]string, 0, len(feeds))
	now := time.Now().Unix()
	for i, f := range feeds {
		id := ID()
		if _, err := stmt.ExecContext(ctx, id, ID(), readableText(f.Title), readableText(f.URL), string(recipes[i]), f.Interval, false, now); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}
