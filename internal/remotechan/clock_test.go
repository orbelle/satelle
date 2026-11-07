package remotechan

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// hangBound is the only real-time bound in this package's tests. A wait that
// reaches it has hung: it never decides an outcome a slow machine could change,
// because every wait it bounds is for an observable state change, never a count
// that must be reached within a window.
const hangBound = 60 * time.Second

// fakeClock is a Clock a test advances. Each Advance step delivers its ticks
// with a blocking send, so a tick is never dropped under load.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
	log     []time.Duration // the period of every ticker ever registered, in order
}

type fakeTicker struct {
	d       time.Duration
	next    time.Time
	c       chan time.Time
	done    chan struct{} // closed by stop, so a send to a consumer that left ends
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

// tickLog is the period of every ticker registered so far, in order.
func (f *fakeClock) tickLog() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.log...)
}

// waitLive waits until at least n live (registered, not stopped) tickers of
// period d are on the clock. A session starts its loss monitor in a goroutine,
// so a test that advances the clock straight after connecting can step past
// the moment the monitor's ticker would have been due. The handshake's own
// tickers (key poll, hello resend) register and stop before a session exists,
// so the wait counts only live tickers of the monitor's period.
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

// Advance moves time forward by d, firing every ticker whose deadline passes.
// Each tick is delivered, or its ticker stopped, before time moves on: a tick is
// never dropped. A tick that is neither taken nor stopped within hangBound is a
// hang, and panics.
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

// eventually polls cond until it holds. cond must be a state change that, once
// reached, stays reached (a count at least n, a ticker registered), so how long
// it takes decides nothing; only a hang, at hangBound, fails it.
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*hangBound) // outlasts every hang bound
	t.Cleanup(cancel)
	return ctx
}
