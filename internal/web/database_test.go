package web

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rss-workshop/internal/store"
	"rss-workshop/internal/testutil"
)

func openTestStore(t *testing.T, maxItems int) (*store.Store, error) {
	t.Helper()
	if databaseURL := testutil.PostgresURL(t); databaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return store.OpenPostgres(ctx, databaseURL, maxItems)
	}
	return store.Open(filepath.Join(t.TempDir(), "rss.db"), maxItems)
}
