package browser

import (
	"context"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/fetch"
)

// fakeChromiumEnv holds the address a stand-in Chromium connects to. The pool
// starts this test binary as its Chromium, and init turns the binary into a
// process that never prints a DevTools address: it connects, so the test knows
// the start is under way, and exits when the test hangs up. An init is used
// because it runs before the flags Chromium is given are parsed.
const fakeChromiumEnv = "RSS_WORKSHOP_TEST_FAKE_CHROMIUM"

func init() {
	addr := os.Getenv(fakeChromiumEnv)
	if addr == "" {
		return
	}
	if conn, err := net.Dial("tcp", addr); err == nil {
		_, _ = io.Copy(io.Discard, conn)
	}
	os.Exit(1)
}

// The library listing, which the dashboard polls every 10 seconds, and
// /metrics call Stats, so it must not wait for the pool's lock, which a
// Chromium start holds for up to 15 s. It still reports what the pool holds.
func TestStatsDoesNotWaitForThePoolLock(t *testing.T) {
	root, stop := context.WithCancel(context.Background())
	defer stop()
	p := &Pool{slots: make(chan struct{}, 3), root: root, active: 2, restarts: 4}
	p.mu.Lock()
	defer p.mu.Unlock()
	read := func() Stats {
		t.Helper()
		got := make(chan Stats, 1)
		go func() { got <- p.Stats() }()
		select {
		case s := <-got:
			return s
		case <-time.After(5 * time.Second):
			t.Fatal("Stats waited for the pool's lock")
			return Stats{}
		}
	}
	if got, want := read(), (Stats{Active: 2, Capacity: 3, Restarts: 4, Ready: true}); got != want {
		t.Fatalf("stats %+v; want %+v", got, want)
	}
	stop() // as when Chromium exits
	if got, want := read(), (Stats{Active: 2, Capacity: 3, Restarts: 4}); got != want {
		t.Fatalf("stats after Chromium stopped %+v; want %+v", got, want)
	}
}

// A Chromium that never reports its DevTools address keeps a start waiting
// for 10 s. Stats must answer meanwhile, and say that nothing is ready, and
// the failed start must leave the pool as it was.
func TestStatsAnswersWhileChromiumStarts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the pool refuses to start Chromium as root")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv(fakeChromiumEnv, listener.Addr().String())
	policy, err := fetch.New(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := New(self, 1, 30*time.Second, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rendered := make(chan error, 1)
	go func() {
		_, err := pool.Render(context.Background(), "https://example.com/story", "", 0)
		rendered <- err
	}()
	// Readers that keep asking through the failing start and Close. This start
	// writes nothing Stats reports; TestStatsReadsWhileThePoolWrites has the
	// writers.
	stopReaders := make(chan struct{})
	readersDone := make(chan struct{})
	go func() {
		defer close(readersDone)
		for {
			select {
			case <-stopReaders:
				return
			default:
				pool.Stats()
				pool.Capacity()
			}
		}
	}()
	defer func() { close(stopReaders); <-readersDone }()

	// The stand-in's connection is the start, which lasts until it is closed.
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Second))
	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("the stand-in Chromium did not start: %v", err)
	}
	defer conn.Close() // ends a start that a failed check left waiting

	got := make(chan Stats, 1)
	go func() { got <- pool.Stats() }()
	select {
	case s := <-got:
		if want := (Stats{Capacity: 1}); s != want {
			t.Fatalf("stats during a start %+v; want %+v", s, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stats waited for Chromium to start")
	}
	select {
	case err := <-rendered:
		rendered <- err
		t.Fatalf("the start ended before Stats was read: %v", err)
	default:
	}

	conn.Close()
	select {
	case err := <-rendered:
		if err == nil || !strings.HasPrefix(err.Error(), "Chromium could not start") {
			t.Fatalf("render with a Chromium that exits: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the render outlived its Chromium")
	}
	if s, want := pool.Stats(), (Stats{Capacity: 1}); s != want || len(pool.slots) != 0 {
		t.Fatalf("stats after a failed start %+v with %d slots taken; want %+v and none", s, len(pool.slots), want)
	}
	pool.Close()
	if s, want := pool.Stats(), (Stats{Capacity: 1}); s != want {
		t.Fatalf("stats after Close %+v; want %+v", s, want)
	}
}
