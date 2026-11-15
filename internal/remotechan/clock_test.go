package remotechan

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

const hangBound = 60 * time.Second

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
	log     []time.Duration
}

type fakeTicker struct {
	d       time.Duration
	next    time.Time
	c       chan time.Time
	done    chan struct{}
	stopped bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)} }

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Tick(d time.Duration) (<-chan time.Time, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTicker{d: d, next: f.now.Add(d), c: make(chan time.Time, 1), done: make(chan struct{})}
	f.tickers = append(f.tickers, t)
	f.log = append(f.log, d)
	return t.c, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !t.stopped {
			t.stopped = true
			close(t.done)
		}
	}
}

func (f *fakeClock) tickLog() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.log...)
}

func (f *fakeClock) waitLive(t *testing.T, d time.Duration, n int) {
	t.Helper()
	eventually(t, fmt.Sprintf("%d live %v tickers on the fake clock", n, d), func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		live := 0
		for _, tk := range f.tickers {
			if tk.d == d && !tk.stopped {
				live++
			}
		}
		return live >= n
	})
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	target := f.now.Add(d)
	f.mu.Unlock()
	for {
		f.mu.Lock()
		var due *fakeTicker
		for _, t := range f.tickers {
			if !t.stopped && !t.next.After(target) && (due == nil || t.next.Before(due.next)) {
				due = t
			}
		}
		if due == nil {
			f.now = target
			f.mu.Unlock()
			return
		}
		f.now = due.next
		due.next = due.next.Add(due.d)
		at := f.now
		f.mu.Unlock()
		select {
		case due.c <- at:
		case <-due.done:
		case <-time.After(hangBound):
			panic(fmt.Sprintf("fakeClock: a %v tick due at %s was not received within %v", due.d, at.Format(time.RFC3339Nano), hangBound))
		}
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(hangBound)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*hangBound)
	t.Cleanup(cancel)
	return ctx
}
