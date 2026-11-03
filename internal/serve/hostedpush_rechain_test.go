package serve

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// rechainWorld is a Pusher whose push always fails for the repo under test, with
// conflicts stories recorded in conflict there and rechainErr as the rechain's
// answer, on a clock that never moves so the backoff stays armed.
type rechainWorld struct {
	mu        sync.Mutex
	pushes    int
	settled   int
	rechained []string
	reasons   []string
}

func newRechainWorld(t *testing.T, conflicts int, rechainErr error) (*Pusher, *rechainWorld) {
	t.Helper()
	w := &rechainWorld{}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := &Pusher{
		Debounce: 5 * time.Millisecond,
		Now:      func() time.Time { return now },
		Push: func(_ context.Context, path string) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			if path == settlePath {
				w.settled++
				return nil
			}
			w.pushes++
			return errors.New("1 stor(y/ies) not published")
		},
		Conflicted: func(path string) int {
			if path == settlePath {
				return 0
			}
			return conflicts
		},
		Rechain: func(_ context.Context, path string) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.rechained = append(w.rechained, path)
			return rechainErr
		},
		Record: func(path string, success bool, reason string) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if path != settlePath {
				w.reasons = append(w.reasons, reason)
			}
		},
	}
	testPusher(t, p)
	return p, w
}

func (w *rechainWorld) settledCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.settled
}

func (w *rechainWorld) state() (pushes int, rechained []string, reasons []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pushes, append([]string(nil), w.rechained...), append([]string(nil), w.reasons...)
}

// sty_f321c29e AC7: after a push that ran and failed, satelled runs rechain once
// for the repo when its conflict set is non-empty and never when it is empty; a
// push the repo's backoff skipped runs no rechain either.
func TestPusherRechainsOnlyConflictedAfterAFailedPush(t *testing.T) {
	for _, c := range []struct {
		name      string
		conflicts int
		want      int
	}{
		{"no conflicts", 0, 0},
		{"two conflicts", 2, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, w := newRechainWorld(t, c.conflicts, nil)
			p.Notify("rk")
			waitFor(t, waitBudget, func() bool { n, _, _ := w.state(); return n >= 1 })
			settle(t, p, w.settledCount)
			pushes, rechained, _ := w.state()
			if pushes != 1 || len(rechained) != c.want {
				t.Fatalf("pushes %d rechains %v, want 1 push and %d rechain(s)", pushes, rechained, c.want)
			}
			if c.want > 0 && rechained[0] != "/repo/rk" {
				t.Errorf("rechain ran in %q, want /repo/rk", rechained[0])
			}

			// Inside the backoff window the push is skipped, and so is the rechain.
			for range 3 {
				p.Notify("rk")
				settle(t, p, w.settledCount)
			}
			if pushes, rechained, _ = w.state(); pushes != 1 || len(rechained) != c.want {
				t.Errorf("under backoff: pushes %d rechains %v, want no more of either", pushes, rechained)
			}
		})
	}
}

// sty_f321c29e AC7: a rechain that fails is reported with the push's failure, so
// the same backoff arms for it.
func TestPusherReportsAFailedRechainWithThePush(t *testing.T) {
	p, w := newRechainWorld(t, 1, errors.New("refused sty_x: builds on evt_nowhere"))
	p.Notify("rk")
	waitFor(t, waitBudget, func() bool { _, r, _ := w.state(); return len(r) >= 1 })
	settle(t, p, w.settledCount)
	_, _, reasons := w.state()
	if len(reasons) != 1 || !strings.Contains(reasons[0], "not published") || !strings.Contains(reasons[0], "evt_nowhere") {
		t.Fatalf("recorded reasons = %q, want the push's and the rechain's failure in one", reasons)
	}
	if st := p.backoff["rk"]; st == nil || st.attempts != 1 {
		t.Errorf("backoff = %+v, want one failed attempt armed", st)
	}
}
