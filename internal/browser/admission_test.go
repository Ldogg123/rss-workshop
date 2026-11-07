package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/notify"
	"rss-workshop/internal/scheduler"
	"rss-workshop/internal/store"
)

// A render that finds every slot taken waits for one inside its deadline. Its
// error then says so rather than reading as a slow source, leaves the URL
// out, and still matches the context's own error.
func TestRenderNamesTheWaitForAChromiumSlot(t *testing.T) {
	const source = "https://example.com/private/story?token=reader-secret"
	p := &Pool{timeout: 50 * time.Millisecond, slots: make(chan struct{}, 1)}
	p.slots <- struct{}{} // taken, as by another render
	_, err := p.Render(context.Background(), source, "", 0)
	if err == nil || err.Error() != "no free Chromium slot before the deadline: context deadline exceeded" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slot wait past the deadline: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Render(ctx, source, "", 0)
	if err == nil || err.Error() != "canceled while waiting for a free Chromium slot: context canceled" || !errors.Is(err, context.Canceled) {
		t.Fatalf("slot wait cancelled: %v", err)
	}
	if p.root != nil || p.active != 0 {
		t.Fatal("a render that never had a slot started Chromium")
	}
}

// The scheduler reads the capacity while holding its own lock, so reading it
// must not wait for the pool's, which a Chromium start holds for up to 15 s.
func TestCapacityDoesNotWaitForThePoolLock(t *testing.T) {
	p := &Pool{slots: make(chan struct{}, 3)}
	p.mu.Lock()
	defer p.mu.Unlock()
	got := make(chan int, 1)
	go func() { got <- p.Capacity() }()
	select {
	case n := <-got:
		if n != 3 {
			t.Fatalf("capacity %d; want 3", n)
		}
	case <-time.After(time.Second):
		t.Fatal("Capacity waited for the pool's lock")
	}
}

type heard struct {
	mu     sync.Mutex
	events []notify.Event
}

func (h *heard) Notify(e notify.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The scheduler admits Chromium refreshes against this pool's capacity, and
// the slot-wait error it stores, logs and sends in a notification names no
// source. The pool's only slot stays taken throughout, as by a render that
// holds no admission: an Auto fallback, or a preview that found none free.
func TestSchedulerAdmitsAgainstThePool(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "rss.db"), 50)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	policy, err := fetch.New(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	pool := &Pool{timeout: 200 * time.Millisecond, slots: make(chan struct{}, 1)}
	pool.slots <- struct{}{}
	jobs := scheduler.New(db, policy, 4, 200*time.Millisecond)
	jobs.Renderer = pool
	notifications := &heard{}
	jobs.Notifier, jobs.NotifyAfter = notifications, 1
	var log syncBuffer
	restore := slog.Default()
	defer slog.SetDefault(restore)
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ids []string
	for i := range 2 {
		id, err := db.Save(ctx, model.Feed{Title: fmt.Sprintf("Rendered %d", i), URL: fmt.Sprintf("https://example.com/private-%d", i), Interval: 3600, Enabled: true,
			Recipe: model.Recipe{Mode: "browser", Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	failed := func() bool {
		for _, id := range ids {
			if f, err := db.Get(ctx, id); err != nil || f.Failures == 0 {
				return false
			}
		}
		return true
	}
	jobs.Start(ctx)
	peak := 0
	for deadline := time.Now().Add(10 * time.Second); !failed(); time.Sleep(time.Millisecond) {
		active, _ := jobs.Stats()
		peak = max(peak, active)
		if time.Now().After(deadline) {
			cancel()
			jobs.Wait()
			t.Fatal("the refreshes did not finish")
		}
	}
	cancel()
	jobs.Wait()
	if peak != 1 {
		t.Fatalf("%d Chromium refreshes ran at once with one slot", peak)
	}
	const want = "no free Chromium slot before the deadline: context deadline exceeded"
	for _, id := range ids {
		if f, err := db.Get(context.Background(), id); err != nil || f.Error != want {
			t.Fatalf("stored error %q, %v; want %q", f.Error, err, want)
		}
	}
	notifications.mu.Lock()
	events := notifications.events
	notifications.mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("notifications: %+v", events)
	}
	for _, e := range events {
		if e.Kind != notify.Failing || e.Error != want {
			t.Fatalf("notification %+v; want a failing feed with %q", e, want)
		}
	}
	if logged := log.String(); strings.Count(logged, want) != 2 || strings.Contains(logged, "example.com") {
		t.Fatalf("log:\n%s", logged)
	}
}
