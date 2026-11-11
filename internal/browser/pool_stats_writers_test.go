package browser

import (
	"context"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
)

// What Stats reports is written with statsMu held as well as mu. No start
// succeeds without a real Chromium, so this calls each writer as the pool
// does, with mu held, while Stats reads, for the race detector. Each writer
// gets a stretch of its own: a writer that does take statsMu orders the
// reader after itself, which would hide a neighbour that does not.
func TestStatsReadsWhileThePoolWrites(t *testing.T) {
	policy, err := fetch.New(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := newProxy(policy, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	root, cancel := context.WithCancel(context.Background())
	p := &Pool{slots: make(chan struct{}, 1), proxy: proxy, stop: cancel}
	stopReaders := make(chan struct{})
	readersDone := make(chan struct{})
	go func() {
		defer close(readersDone)
		for {
			select {
			case <-stopReaders:
				return
			default:
				p.Stats()
			}
		}
	}()
	starts := 0
	for _, write := range []func(){
		func() { p.addActive(1); p.addActive(-1) }, // a render
		func() { p.setStarted(root); starts++ },    // a start that succeeded
		func() { p.setRoot(nil) },                  // a restart, or Close
	} {
		for end := time.Now().Add(20 * time.Millisecond); time.Now().Before(end); {
			p.mu.Lock()
			write()
			p.mu.Unlock()
		}
	}
	p.mu.Lock()
	p.setStarted(root)
	p.mu.Unlock()
	if s, want := p.Stats(), (Stats{Capacity: 1, Restarts: starts + 1, Ready: true}); s != want {
		t.Fatalf("stats after the last start %+v; want %+v", s, want)
	}
	p.Close()
	close(stopReaders)
	<-readersDone
	if s, want := p.Stats(), (Stats{Capacity: 1, Restarts: starts + 1}); s != want {
		t.Fatalf("stats after Close %+v; want %+v", s, want)
	}
}
