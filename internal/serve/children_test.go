package serve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// AC3 (sty_0367d20c): at most one child per repo and kind. 20 background
// callers and 20 waiting callers on one repo each peak at 1; the background
// losers are skipped, every waiting caller eventually runs, and a different
// kind or repo is not held up.
func TestChildGatePeakConcurrencyIsOnePerKey(t *testing.T) {
	g := &childGate{}
	type key struct{ repo, kind string }
	var mu sync.Mutex
	active, peak := map[key]int{}, map[key]int{}
	release := make(chan struct{})
	body := func(k key) func() error {
		return func() error {
			mu.Lock()
			active[k]++
			if active[k] > peak[k] {
				peak[k] = active[k]
			}
			mu.Unlock()
			<-release
			mu.Lock()
			active[k]--
			mu.Unlock()
			return nil
		}
	}

	var wg sync.WaitGroup
	var busy, ranTry, ranWait atomic.Int32
	push, fetch := key{"/r", kindPush}, key{"/r", kindFetch}
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			err := g.tryRun(push.repo, push.kind, body(push))
			switch {
			case errors.Is(err, errChildBusy):
				busy.Add(1)
			case err == nil:
				ranTry.Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			if err := g.run(context.Background(), fetch.repo, fetch.kind, body(fetch)); err == nil {
				ranWait.Add(1)
			}
		}()
	}
	// A different kind on the same repo, and the same kind on another repo, run
	// while both keys above are held.
	other := make(chan error, 2)
	go func() { other <- g.tryRun("/r", kindSnapshot, func() error { return nil }) }()
	go func() { other <- g.tryRun("/other", kindPush, func() error { return nil }) }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-other:
			if err != nil {
				t.Fatalf("an unrelated key was held up: %v", err)
			}
		case <-time.After(waitBudget):
			t.Fatal("an unrelated key did not run while /r push and fetch were busy")
		}
	}
	// Let every waiter through one at a time.
	go func() {
		for {
			select {
			case release <- struct{}{}:
			case <-time.After(waitBudget):
				return
			}
		}
	}()
	wg.Wait()

	if peak[push] != 1 || peak[fetch] != 1 {
		t.Fatalf("peak concurrency push %d fetch %d, want 1 each", peak[push], peak[fetch])
	}
	if ranWait.Load() != 20 {
		t.Fatalf("waiting callers that ran = %d, want all 20", ranWait.Load())
	}
	if ranTry.Load()+busy.Load() != 20 || busy.Load() == 0 {
		t.Fatalf("background callers ran %d, skipped busy %d; want some skipped and 20 in all", ranTry.Load(), busy.Load())
	}
}

// A waiting caller gives up when its context ends rather than queueing forever.
func TestChildGateRunHonoursContext(t *testing.T) {
	g := &childGate{}
	hold := make(chan struct{})
	go func() { _ = g.tryRun("/r", kindFetch, func() error { <-hold; return nil }) }()
	defer close(hold)
	waitFor(t, waitBudget, func() bool { return g.isBusy(childKey{"/r", kindFetch}) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := g.run(ctx, "/r", kindFetch, func() error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run = %v, want the context's deadline", err)
	}
}

func (g *childGate) isBusy(k childKey) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.busy[k]
	return ok
}

// Every real *ViaCLI helper runs its exec under the gate: 20 concurrent calls on
// one repo never overlap a child. Background helpers skip the losers as
// errChildBusy; the story-page fetch runs all 20, one after another.
func TestViaCLIHelpersRunOneChildPerRepoAndKind(t *testing.T) {
	f := newFakeCLI(t)
	repo := t.TempDir()
	t.Setenv("SATELLE_TEST_LOCK", filepath.Join(t.TempDir(), "lock"))
	t.Setenv("SATELLE_TEST_SLEEP", "0.1")
	helpers := []struct {
		name string
		call func(context.Context) error
		wait bool
	}{
		{"push", func(ctx context.Context) error { return pushViaCLI(ctx, repo) }, false},
		{"snapshot", func(ctx context.Context) error { return snapshotViaCLI(ctx, repo) }, false},
		{"follow", func(ctx context.Context) error { return followViaCLI(ctx, repo) }, false},
		{"reseed", func(ctx context.Context) error { return reseedViaCLI(ctx, repo, "") }, false},
		{"substrate", func(ctx context.Context) error { return substratePushViaCLI(ctx, repo) }, false},
		{"fetch", func(ctx context.Context) error { return fetchViaCLI(ctx, repo, "sty_1") }, true},
	}
	for _, h := range helpers {
		t.Run(h.name, func(t *testing.T) {
			_ = os.Remove(f.calls)
			var wg sync.WaitGroup
			var ran, busy atomic.Int32
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					switch err := h.call(context.Background()); {
					case err == nil:
						ran.Add(1)
					case errors.Is(err, errChildBusy):
						busy.Add(1)
					default:
						t.Errorf("%s: %v", h.name, err)
					}
				}()
			}
			wg.Wait()
			if got := f.lines(t); strings.Contains(strings.Join(got, "\n"), "overlap") {
				t.Fatalf("%s children overlapped:\n%s", h.name, strings.Join(got, "\n"))
			}
			if h.wait {
				if ran.Load() != 20 {
					t.Fatalf("fetch: %d of 20 ran, want every page's fetch to wait and run", ran.Load())
				}
				return
			}
			if busy.Load() == 0 || ran.Load()+busy.Load() != 20 {
				t.Fatalf("%s: ran %d, busy %d; want the losers skipped as busy", h.name, ran.Load(), busy.Load())
			}
		})
	}
}
