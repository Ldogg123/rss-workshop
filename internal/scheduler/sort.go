package scheduler

import (
	"rss-workshop/internal/model"
	"sort"
)

func sortFeeds(fs []model.Feed) {
	sort.Slice(fs, func(i, j int) bool { return fs[i].NextRun.Before(fs[j].NextRun) })
}
