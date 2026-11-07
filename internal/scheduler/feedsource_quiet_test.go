package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/model"
)

// A feed source is Quiet on the basis every feed has. A well-formed feed with
// no entries is a successful refresh that stores no story, so a feed source
// whose feed has listed none counts from its last output change and turns
// Quiet once its threshold passes; staying empty does not restart the count.
// A story is new when an entry with an identity the feed has not stored is
// first stored, and an entry whose link and text change under the same guid
// is a changed story, not a new one.
func TestFeedSourceQuietBasis(t *testing.T) {
	ctx := context.Background()
	s, err := openTestStore(t, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	p := newPageFetcher(`<?xml version="1.0"?><rss version="2.0"><channel><title>Quiet source</title></channel></rss>`, nil)
	jobs := New(s, p, 1, 10*time.Second)
	f := saveFeed(t, jobs, model.Recipe{Mode: "static", Type: extract.FeedType})
	after, flagged := f.QuietThreshold()
	if !flagged {
		t.Fatal("a feed source is never quiet")
	}

	jobs.refresh(ctx, f)
	f = reload(t, s, f.ID)
	if items, err := s.Items(ctx, f.ID); err != nil || len(items) != 0 {
		t.Fatalf("stories %+v, %v", items, err)
	}
	if f.Failures != 0 || f.Error != "" || f.LastSuccess.IsZero() || f.LastChanged.IsZero() || !f.LastNew.IsZero() {
		t.Fatalf("an empty feed: failures %d, error %q, last success %s, last change %s, last new %s", f.Failures, f.Error, f.LastSuccess, f.LastChanged, f.LastNew)
	}
	if !f.QuietSince().Equal(f.LastChanged) || f.IsQuiet(f.LastChanged.Add(after-time.Second)) || !f.IsQuiet(f.LastChanged.Add(after)) {
		t.Fatalf("an empty feed is not quiet %s after its last change", after)
	}
	changed := f.LastChanged
	jobs.refresh(ctx, f)
	f = reload(t, s, f.ID)
	if f.Failures != 0 || !f.LastChanged.Equal(changed) || !f.LastNew.IsZero() {
		t.Fatalf("staying empty: failures %d, last change %s then %s, last new %s", f.Failures, changed, f.LastChanged, f.LastNew)
	}

	p.list = `<rss version="2.0"><channel><item><title>First</title><link>https://example.com/one</link><guid isPermaLink="false">one</guid><description>Text</description></item></channel></rss>`
	jobs.refresh(ctx, f)
	f = reload(t, s, f.ID)
	if f.LastNew.IsZero() || !f.QuietSince().Equal(f.LastNew) || f.IsQuiet(f.LastNew.Add(after-time.Second)) {
		t.Fatalf("the first stored entry is not a new story: last new %s", f.LastNew)
	}

	// Store the story two days earlier, so a refresh that counted the edited
	// entry as new would move the last new story forward again. A merge tells
	// an inserted story by its first-seen second.
	old := f.LastNew.Add(-48 * time.Hour)
	for _, query := range []string{"UPDATE feeds SET last_new=%d WHERE id='%s'", "UPDATE items SET first_seen=%d WHERE feed_id='%s'"} {
		if _, err := s.DB.ExecContext(ctx, fmt.Sprintf(query, old.Unix(), f.ID)); err != nil {
			t.Fatal(err)
		}
	}
	changed = f.LastChanged
	p.list = `<rss version="2.0"><channel><item><title>First</title><link>https://example.com/one-moved</link><guid isPermaLink="false">one</guid><description>Edited text</description></item></channel></rss>`
	jobs.refresh(ctx, reload(t, s, f.ID))
	f = reload(t, s, f.ID)
	items, err := s.Items(ctx, f.ID)
	if err != nil || len(items) != 1 || items[0].URL != "https://example.com/one-moved" || items[0].HTML != "Edited text" {
		t.Fatalf("stories %+v, %v", items, err)
	}
	if !f.LastNew.Equal(old) || !f.LastChanged.After(changed) {
		t.Fatalf("an edited entry: last new %s, want %s; last change %s then %s", f.LastNew, old, changed, f.LastChanged)
	}
}
