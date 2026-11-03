package serve

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
)

// AC1 (sty_0367d20c): satelled stops re-running a sync that cannot converge.
// These drive the real pushViaCLI and snapshotViaCLI against the fake satelle
// through Pusher.Run and the Reconciler, and count the children over a
// simulated hour.

// simClock is simulated time for Pusher.Run: After advances the clock by d and
// fires at once, so Run runs as fast as its children; past the horizon it ends
// the run instead.
type simClock struct {
	mu      sync.Mutex
	start   time.Time
	now     time.Time
	horizon time.Duration
	cancel  context.CancelFunc
}

func newSimClock(horizon time.Duration, cancel context.CancelFunc) *simClock {
	t0 := time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC)
	return &simClock{start: t0, now: t0, horizon: horizon, cancel: cancel}
}

func (c *simClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *simClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	over := c.now.Sub(c.start) > c.horizon
	now := c.now
	c.mu.Unlock()
	if over {
		c.cancel()
		return nil
	}
	ch := make(chan time.Time, 1)
	ch <- now
	return ch
}

// elapsed is the simulated time since start.
func (c *simClock) elapsed() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now.Sub(c.start)
}

// pushRun is one simulated hour of Pusher.Run over one repo whose push is the
// real pushViaCLI. After each child the test delivers that child's end-of-verb
// drain to OnIngest with the origin the child stamps — what the real CLI's
// drain posts to satelled's ingest endpoint.
type pushRun struct {
	p       *Pusher
	clock   *simClock
	fake    *fakeCLI
	done    chan struct{}
	mu      sync.Mutex
	at      []time.Duration // simulated time of each push child
	records []bool          // Record's success per push
	lines   *lineRecorder
}

func startPushRun(t *testing.T, childOrigin mirror.IngestOrigin) *pushRun {
	t.Helper()
	fake := newFakeCLI(t)
	repo := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := &pushRun{clock: newSimClock(time.Hour, cancel), fake: fake, done: make(chan struct{}), lines: &lineRecorder{}}
	r.p = &Pusher{
		Now:     r.clock.Now,
		After:   r.clock.After,
		Timeout: waitBudget,
		Resolve: func(context.Context, string) (string, bool) { return repo, true },
		Log:     r.lines.log,
		Record: func(_ string, success bool, _ string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.records = append(r.records, success)
		},
	}
	r.p.Push = func(ctx context.Context, path string) error {
		err := pushViaCLI(ctx, path)
		r.mu.Lock()
		r.at = append(r.at, r.clock.elapsed())
		r.mu.Unlock()
		r.p.OnIngest("rk", childOrigin)
		return err
	}
	go func() { r.p.Run(ctx); close(r.done) }()
	r.p.Notify("rk") // the one genuine local change that starts it
	return r
}

func (r *pushRun) runs() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.at)
}

func (r *pushRun) wait(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * waitBudget):
		t.Fatalf("the simulated hour did not end; %d runs so far", r.runs())
	}
}

// Pre-fix shape: the push child's own writes reach OnIngest as local, so each
// push schedules the next one debounce later — "the loop re-runs at once". The
// fixture reproduces the field report: about one push per 2s for the hour.
func TestPushSelfTriggerReRunsAtOnceWhenTheChildIsLocalOrigin(t *testing.T) {
	t.Setenv("SATELLE_TEST_OUTCOME", `{"held":1,"conflict":0}`)
	r := startPushRun(t, mirror.OriginLocal)
	r.wait(t)
	got := r.runs()
	if want := int(time.Hour / pushDebounce); got < want-2 || got > want {
		t.Fatalf("push children over the hour = %d, want ≈%d (one per debounce)", got, want)
	}
	if n := r.fake.count(t, "sync workstate push"); n != got {
		t.Fatalf("fake saw %d push children, Pusher ran %d", n, got)
	}
}

// Fixed: a push that exits 0 having only held in-flight stories runs once,
// records no failure, leaves nothing dirty and arms no retry; its own
// hosted-origin writes schedule nothing. The next genuine local change runs the
// next push.
func TestPushHeldOnlyRunsOnceUntilALocalChange(t *testing.T) {
	t.Setenv("SATELLE_TEST_OUTCOME", `{"held":1,"conflict":0}`)
	r := startPushRun(t, mirror.OriginHosted)
	waitFor(t, waitBudget, func() bool { return r.runs() == 1 })
	// Nothing dirty means nothing can run: Run waits on Notify alone.
	waitFor(t, waitBudget, func() bool { return r.p.dirtyCount() == 0 })
	time.Sleep(100 * time.Millisecond) // simulated time is instant; any re-run would be here
	if n := r.runs(); n != 1 {
		t.Fatalf("held-only push ran %d times with no local change, want 1", n)
	}
	r.mu.Lock()
	records := append([]bool(nil), r.records...)
	r.mu.Unlock()
	if len(records) != 1 || !records[0] {
		t.Fatalf("records = %v, want one success: held is in flight, not a failure", records)
	}
	for _, ln := range r.lines.snapshot() {
		if strings.HasPrefix(ln, "hosted push") {
			t.Fatalf("a held push logged a failure transition: %q", ln)
		}
	}
	if countLines(r.lines.snapshot(), "sync push", "held 1", "next in on change") != 1 {
		t.Fatalf("no per-run held line: %v", r.lines.snapshot())
	}

	r.p.OnIngest("rk", mirror.OriginLocal)
	waitFor(t, waitBudget, func() bool { return r.runs() == 2 })
	r.mu.Lock()
	gap := r.at[1] - r.at[0]
	r.mu.Unlock()
	if gap != pushDebounce {
		t.Fatalf("the local change ran the next push %s later, want one debounce (%s)", gap, pushDebounce)
	}
}

// A push that exits non-zero (drifted or refused rows) keeps the error backoff:
// 30s doubling to the 15m cap, so about 8 children in the hour, each with no
// other trigger than Run's retry timer.
func TestPushThatFailsBacksOffToTheCap(t *testing.T) {
	t.Setenv("SATELLE_TEST_EXIT", "1")
	r := startPushRun(t, mirror.OriginHosted)
	r.wait(t)
	r.mu.Lock()
	at := append([]time.Duration(nil), r.at...)
	r.mu.Unlock()
	var gaps []time.Duration
	for i := 1; i < len(at); i++ {
		gaps = append(gaps, at[i]-at[i-1])
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	if len(gaps) != len(want) {
		t.Fatalf("failing push ran %d times at %v, want %d", len(at), at, len(want)+1)
	}
	for i := range want {
		if gaps[i] != want[i] {
			t.Fatalf("gaps %v, want %v", gaps, want)
		}
	}
}

// The retry timer is the backstop: a push that fails once and then would
// succeed runs again when its backoff ends, with no Notify between.
func TestPushRetryTimerRunsAfterTheBackoffWithNoNotify(t *testing.T) {
	exitFile := filepath.Join(t.TempDir(), "exit")
	if err := os.WriteFile(exitFile, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFakeCLI(t)
	bin, _ := resolveSatelle()
	writeScript(t, bin, "#!/bin/sh\necho \"$*\" >> \""+f.calls+"\"\nexit \"$(cat "+exitFile+")\"\n")
	repo := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clock := newSimClock(time.Hour, cancel)
	var mu sync.Mutex
	var at []time.Duration
	p := &Pusher{
		Now: clock.Now, After: clock.After, Timeout: waitBudget,
		Resolve: func(context.Context, string) (string, bool) { return repo, true },
	}
	p.Push = func(ctx context.Context, path string) error {
		err := pushViaCLI(ctx, path)
		mu.Lock()
		at = append(at, clock.elapsed())
		mu.Unlock()
		_ = os.WriteFile(exitFile, []byte("0"), 0o644) // healed after the first run
		return err
	}
	go p.Run(ctx)
	p.Notify("rk")
	waitFor(t, waitBudget, func() bool { mu.Lock(); defer mu.Unlock(); return len(at) == 2 })
	mu.Lock()
	gap := at[1] - at[0]
	mu.Unlock()
	if gap != pushBackoffBase {
		t.Fatalf("the retry ran %s after the failure, want the backoff %s", gap, pushBackoffBase)
	}
	waitFor(t, waitBudget, func() bool { return p.dirtyCount() == 0 })
}

// A snapshot that exits 0 having stopped on a conflict backs off from 1m,
// doubling to the 15m cap, and a local-origin ingest (Touch) lifts it for the
// next pass. Passes run every simulated minute; a converging snapshot runs on
// every one of them.
func TestSnapshotConflictBacksOffUntilALocalChange(t *testing.T) {
	fake := newFakeCLI(t)
	snapshotRuns := func(t *testing.T) int { return fake.count(t, "sync workstate snapshot") }
	repo := t.TempDir()
	run := func(t *testing.T, outcome string) (*Reconciler, func(int) []int) {
		t.Setenv("SATELLE_TEST_OUTCOME", outcome)
		clock := newSimClock(24*time.Hour, func() {})
		r := &Reconciler{
			Targets: func(context.Context) []string { return []string{repo} },
			Bound:   func(string) bool { return true },
			Now:     clock.Now,
			Timeout: waitBudget,
		}
		passes := func(n int) []int {
			var ranAt []int
			for i := 0; i < n; i++ {
				f := snapshotRuns(t)
				r.pass(context.Background())
				if snapshotRuns(t) > f {
					ranAt = append(ranAt, int(clock.elapsed()/time.Minute))
				}
				for r.following(repo) { // the fake follow exits at once
					time.Sleep(time.Millisecond)
				}
				clock.After(time.Minute)
			}
			return ranAt
		}
		return r, passes
	}

	t.Run("converging snapshot runs every pass", func(t *testing.T) {
		_, passes := run(t, `{"held":0,"conflict":0}`)
		if got := passes(60); len(got) != 60 {
			t.Fatalf("converging snapshot ran on %d of 60 passes", len(got))
		}
	})
	t.Run("conflict stop backs off and Touch resets it", func(t *testing.T) {
		r, passes := run(t, `{"held":0,"conflict":1}`)
		got := passes(60)
		want := []int{0, 1, 3, 7, 15, 30, 45}
		if !slices.Equal(got, want) {
			t.Fatalf("snapshot ran at minutes %v, want %v (1m,2m,4m,8m,15m,15m)", got, want)
		}
		r.Touch(config.RepoKey(repo))
		if again := passes(1); len(again) != 1 {
			t.Fatal("the pass after a local change did not snapshot")
		}
	})
}
