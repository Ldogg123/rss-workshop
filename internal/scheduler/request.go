package scheduler

import (
	"context"

	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
)

// requestContext carries a recipe's own request headers to every static
// request made with the returned context: the list page and the plain-HTML
// article pages of a refresh or a preview, and the visual selector's static
// snapshot. Refresh, Preview and Snapshot each call it once, so a header a
// recipe gains is added here and reaches all of them. Chromium and
// FlareSolverr ignore it and send their browser's own headers.
func requestContext(ctx context.Context, r model.Recipe) context.Context {
	return fetch.WithHeaders(ctx, fetch.Headers{UserAgent: r.UserAgent, AcceptLanguage: r.AcceptLanguage})
}
