package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
)

func testPusher(t *testing.T, p *Pusher) (context.CancelFunc, *pushRecorder) {
	t.Helper()
	if p.Debounce == 0 {
		p.Debounce = 8 * time.Millisecond
	}
	if p.Timeout == 0 {
		p.Timeout = waitBudget
	}
	rec := &pushRecorder{}
	if p.Push == nil {
		p.Push = rec.push
	}
	if p.Resolve == nil {
		p.Resolve = func(_ context.Context, repoKey string) (string, bool) {
			return "/repo/" + repoKey, true
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go p.Run(ctx)
	return cancel, rec
}

type pushRecorder struct {
	mu    sync.Mutex
	paths []string
}

func (r *pushRecorder) push(_ context.Context, repoPath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, repoPath)
	return nil
}

func (r *pushRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

func (r *pushRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

// countPath is how many times path was pushed.
func (r *pushRecorder) countPath(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.paths {
		if p == path {
			n++
		}
	}
	return n
}

// settleKey is the sentinel repo key settle pushes; settlePath is where the
// default Resolve maps it.
const (
	settleKey  = "settle"
	settlePath = "/repo/" + settleKey
)

// settle returns once every flush that began before the call has finished, so a
// test can assert "nothing further was pushed" from the pusher's own state and
// not from a sleep. The worker flushes serially: a flush that pushes the
// sentinel the SECOND time began only after the flush that pushed it the first
// time ended, and that first flush took every key dirtied before settle ran.
// pushed reports how many times the sentinel has been pushed so far.
func settle(t *testing.T, p *Pusher, pushed func() int) {
	t.Helper()
	base := pushed()
	p.Notify(settleKey)
	waitFor(t, waitBudget, func() bool { return pushed() >= base+1 })
	p.Notify(settleKey)
	waitFor(t, waitBudget, func() bool { return pushed() >= base+2 })
}

// TestPusherSingleNotifyPushesOnce proves AC3: one ingest notification becomes
// one push after debounce, with no reconcile interval elapsed.
func TestPusherSingleNotifyPushesOnce(t *testing.T) {
	p := &Pusher{}
	_, rec := testPusher(t, p)
	p.Notify("rk")
	waitFor(t, waitBudget, func() bool { return rec.countPath("/repo/rk") == 1 })
	if got := rec.snapshot(); got[0] != "/repo/rk" {
		t.Fatalf("push path = %q", got[0])
	}
	settle(t, p, func() int { return rec.countPath(settlePath) })
	if n := rec.countPath("/repo/rk"); n != 1 {
		t.Fatalf("extra pushes: %v", rec.snapshot())
	}
}

// TestPusherCoalescesRapidNotifies proves D4: N mutations for one key collapse
// into one push. The notifies land before the worker starts, so no debounce
// window can be missed by a slow machine; the dirty set is what coalesces them.
func TestPusherCoalescesRapidNotifies(t *testing.T) {
	p := &Pusher{}
	for i := 0; i < 5; i++ {
		p.Notify("rk")
	}
	_, rec := testPusher(t, p)
	waitFor(t, waitBudget, func() bool { return rec.countPath("/repo/rk") >= 1 })
	settle(t, p, func() int { return rec.countPath(settlePath) })
	if n := rec.countPath("/repo/rk"); n != 1 {
		t.Fatalf("pushes = %d, want 1 (coalesced): %v", n, rec.snapshot())
	}
}

// TestPusherUnresolvablePushesNothing proves a missing/unguarded key is dropped.
func TestPusherUnresolvablePushesNothing(t *testing.T) {
	p := &Pusher{
		// Only the ghost is unresolvable, so the sentinel settle pushes can prove
		// the flush that took the ghost has finished.
		Resolve: func(_ context.Context, key string) (string, bool) { return "/repo/" + key, key != "ghost" },
	}
	_, rec := testPusher(t, p)
	p.Notify("ghost")
	settle(t, p, func() int { return rec.countPath(settlePath) })
	if n := rec.countPath("/repo/ghost"); n != 0 {
		t.Fatalf("unresolvable key pushed: %v", rec.snapshot())
	}
}

// TestPusherNotifyNeverBlocks proves Notify does not wait on a stalled worker.
func TestPusherNotifyNeverBlocks(t *testing.T) {
	started := make(chan struct{})
	block := make(chan struct{})
	p := &Pusher{
		Debounce: time.Millisecond,
		Push: func(context.Context, string) error {
			select {
			case <-started:
			default:
				close(started)
			}
			<-block
			return nil
		},
	}
	testPusher(t, p)
	p.Notify("rk")
	select {
	case <-started:
	case <-time.After(waitBudget):
		t.Fatal("worker never entered Push")
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			p.Notify("rk")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitBudget):
		t.Fatal("Notify blocked while worker stalled")
	}
	close(block)
}

// TestPusherCatchUpOnRun proves AC4: Run pushes every resolved target before
// any Notify arrives.
func TestPusherCatchUpOnRun(t *testing.T) {
	p := &Pusher{
		Keys: func(context.Context) []string { return []string{"a", "b"} },
	}
	_, rec := testPusher(t, p)
	waitFor(t, waitBudget, func() bool { return rec.count() == 2 })
	got := map[string]bool{}
	for _, path := range rec.snapshot() {
		got[path] = true
	}
	if !got["/repo/a"] || !got["/repo/b"] {
		t.Fatalf("catch-up pushes = %v", rec.snapshot())
	}
}

// sty_0dc600e1 AC8: a bound repo gets no catch-up push on Run; it is pushed when
// a local append notifies it.
func TestPusherSkipsCatchUpForABoundRepo(t *testing.T) {
	p := &Pusher{
		Keys:  func(context.Context) []string { return []string{"bound", "unbound"} },
		Bound: func(path string) bool { return path == "/repo/bound" },
	}
	_, rec := testPusher(t, p)
	waitFor(t, waitBudget, func() bool { return rec.count() == 1 })
	if got := rec.snapshot(); got[0] != "/repo/unbound" {
		t.Fatalf("catch-up pushes = %v, want the unbound repo only", got)
	}
	p.Notify("bound")
	waitFor(t, waitBudget, func() bool { return rec.count() == 2 })
	if got := rec.snapshot(); got[1] != "/repo/bound" {
		t.Fatalf("pushes = %v, want the notified bound repo", got)
	}
}

// TestPusherHonoursOffSwitch proves hermetic SATELLE_SERVER_ENDPOINT=none
// never starts the worker (risk 2).
func TestPusherHonoursOffSwitch(t *testing.T) {
	t.Setenv(config.EnvServerEndpoint, "none")
	var called bool
	p := &Pusher{
		Debounce: time.Millisecond,
		Keys:     func(context.Context) []string { return []string{"rk"} },
		Resolve:  func(context.Context, string) (string, bool) { return "/repo/rk", true },
		Push: func(context.Context, string) error {
			called = true
			return nil
		},
	}
	// Run returns at once when disabled; the bound is only reached if the
	// off-switch is ignored, and a longer bound lets a broken Run push first.
	ctx, cancel := context.WithTimeout(context.Background(), waitBudget)
	defer cancel()
	p.Run(ctx)
	if called {
		t.Fatal("pusher ran with the off-switch set")
	}
}

// TestPusherBackoffDoesNotSpin proves AC6: K failures produce fewer than K
// pushes while the fake clock stays inside the first backoff window.
func TestPusherBackoffDoesNotSpin(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var pushes, settled int
	p := &Pusher{
		Debounce: 5 * time.Millisecond,
		Now:      func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		Push: func(_ context.Context, path string) error {
			mu.Lock()
			defer mu.Unlock()
			if path == settlePath { // the flush barrier, not a push under test
				settled++
				return nil
			}
			pushes++
			return errors.New("hosted down")
		},
	}
	testPusher(t, p)
	p.Notify("rk")
	waitFor(t, waitBudget, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushes >= 1
	})
	// One flush per notify: settle waits out the flush the notify caused, so
	// each attempt lands inside the backoff window without a spacing sleep.
	for i := 0; i < 8; i++ {
		p.Notify("rk")
		settle(t, p, func() int { mu.Lock(); defer mu.Unlock(); return settled })
	}
	mu.Lock()
	got := pushes
	mu.Unlock()
	if got != 1 {
		t.Fatalf("pushes = %d, want 1 inside backoff window", got)
	}
}

// TestPusherDirtySetBounded proves AC6: many Notifies stay O(partitions).
func TestPusherDirtySetBounded(t *testing.T) {
	p := &Pusher{}
	for i := 0; i < 200; i++ {
		p.Notify("a")
		p.Notify("b")
	}
	if n := p.dirtyCount(); n != 2 {
		t.Fatalf("dirty = %d, want 2", n)
	}
}

// TestPusherSuccessResetsBackoff proves a success after failures lets the next
// Notify push immediately.
func TestPusherSuccessResetsBackoff(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var pushes, settled int
	var fail bool = true
	p := &Pusher{
		Debounce: 5 * time.Millisecond,
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		},
		Push: func(_ context.Context, path string) error {
			mu.Lock()
			defer mu.Unlock()
			if path == settlePath { // the flush barrier, not a push under test
				settled++
				return nil
			}
			pushes++
			if fail {
				return errors.New("hosted down")
			}
			return nil
		},
	}
	testPusher(t, p)
	p.Notify("rk")
	waitFor(t, waitBudget, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushes >= 1
	})
	// The push is counted before the worker arms its backoff from the clock, so
	// moving the clock now would race it. Wait that flush out first.
	settle(t, p, func() int { mu.Lock(); defer mu.Unlock(); return settled })

	mu.Lock()
	now = now.Add(time.Hour)
	fail = false
	mu.Unlock()
	p.Notify("rk")
	waitFor(t, waitBudget, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushes >= 2
	})

	// Backoff reset: another Notify without advancing time must push.
	p.Notify("rk")
	waitFor(t, waitBudget, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pushes >= 3
	})
}

// TestPusherLogsOnReasonTransition proves AC6: one log line per
// failure-reason transition, plus recovered.
func TestPusherLogsOnReasonTransition(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var reason string = "boom-a"
	var fail bool = true
	var settled int
	logs := &lineRecorder{}
	p := &Pusher{
		Debounce: 5 * time.Millisecond,
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		},
		Log: logs.log,
		Push: func(_ context.Context, path string) error {
			mu.Lock()
			defer mu.Unlock()
			if path == settlePath { // the flush barrier, not a push under test
				settled++
				return nil
			}
			if !fail {
				return nil
			}
			return errors.New(reason)
		},
	}
	testPusher(t, p)
	p.Notify("rk")
	waitFor(t, waitBudget, func() bool { return countLines(logs.snapshot(), "hosted push", "boom-a") == 1 })

	// Same reason, after backoff window — no new line.
	mu.Lock()
	now = now.Add(time.Hour)
	mu.Unlock()
	p.Notify("rk")
	settle(t, p, func() int { mu.Lock(); defer mu.Unlock(); return settled })
	if n := countLines(logs.snapshot(), "boom-a"); n != 1 {
		t.Fatalf("same-reason logs = %d, want 1: %v", n, logs.snapshot())
	}

	mu.Lock()
	now = now.Add(time.Hour)
	reason = "boom-b"
	mu.Unlock()
	p.Notify("rk")
	waitFor(t, waitBudget, func() bool { return countLines(logs.snapshot(), "boom-b") == 1 })

	mu.Lock()
	now = now.Add(time.Hour)
	fail = false
	mu.Unlock()
	p.Notify("rk")
	waitFor(t, waitBudget, func() bool { return countLines(logs.snapshot(), "recovered") == 1 })
}

func TestPusherReportRecordsEveryOutcome(t *testing.T) {
	var recs []string
	p := &Pusher{
		Record: func(path string, success bool, reason string) {
			if success {
				recs = append(recs, path+":ok")
			} else {
				recs = append(recs, path+":"+reason)
			}
		},
	}
	p.report("rk", "/repo", errors.New("boom"))
	p.report("rk", "/repo", errors.New("boom"))
	p.report("rk", "/repo", nil)
	if len(recs) != 3 || recs[0] != "/repo:boom" || recs[2] != "/repo:ok" {
		t.Fatalf("recordings = %v", recs)
	}
}

func TestResolvePushPathGuards(t *testing.T) {
	ms := openTestMirror(t)
	home := t.TempDir()
	live := t.TempDir()
	seedRepoStore(t, home, live)
	seedStory(t, ms, "rk-live", "sty_1", "plan")
	seedIdentity(t, ms, "rk-live", live)
	seedStory(t, ms, "rk-dead", "sty_2", "plan")
	seedIdentity(t, ms, "rk-dead", filepath.Join(t.TempDir(), "gone"))
	seedStory(t, ms, "rk-nopath", "sty_3", "plan")
	seedStory(t, ms, "rk-nostore", "sty_4", "plan")
	nostore := t.TempDir()
	seedIdentity(t, ms, "rk-nostore", nostore)

	resolve := resolvePushPath(ms, home)
	ctx := context.Background()
	if path, ok := resolve(ctx, "rk-live"); !ok || path != live {
		t.Fatalf("live = (%q, %v), want (%q, true)", path, ok, live)
	}
	if _, ok := resolve(ctx, "rk-dead"); ok {
		t.Fatal("missing path must not resolve")
	}
	if _, ok := resolve(ctx, "rk-nopath"); ok {
		t.Fatal("empty path must not resolve")
	}
	if _, ok := resolve(ctx, "rk-nostore"); ok {
		t.Fatal("no store here must not resolve")
	}
	if _, ok := resolve(ctx, "rk-unknown"); ok {
		t.Fatal("unknown key must not resolve")
	}
}

func TestBackoffDelayCaps(t *testing.T) {
	if got := backoffDelay(0); got != pushBackoffBase {
		t.Fatalf("attempt 0 = %s, want %s", got, pushBackoffBase)
	}
	if got := backoffDelay(1); got != 2*pushBackoffBase {
		t.Fatalf("attempt 1 = %s", got)
	}
	if got := backoffDelay(20); got != pushBackoffCap {
		t.Fatalf("attempt 20 = %s, want cap %s", got, pushBackoffCap)
	}
}

// postIngest POSTs body to path on srv and returns the decoded reply; a non-200 fails.
func postIngest(t *testing.T, srv *httptest.Server, path string, body any) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s = %d", path, resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

// TestHostedOriginIngestNeverPushes proves sty_a21d9c07 AC1: the ingest a pull,
// snapshot, rehydrate or align posts carries the hosted origin, and none of its
// shapes — a full snapshot, its byte-identical re-post, a light drain, a change
// event — becomes a push. The same snapshot with no origin still does.
func TestHostedOriginIngestNeverPushes(t *testing.T) {
	ms, err := mirror.Open(filepath.Join(t.TempDir(), "mirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Close() })
	p := &Pusher{}
	_, rec := testPusher(t, p)
	mux := http.NewServeMux()
	(&mirror.IngestHandler{Store: ms, OnIngest: p.OnIngest}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	full := mirror.Snapshot{
		RepoKey: "rk",
		Origin:  mirror.OriginHosted,
		Stories: []json.RawMessage{[]byte(`{"id":"sty_a","title":"Hello"}`)},
	}
	postIngest(t, srv, "/ingest/snapshot", full)
	if got := postIngest(t, srv, "/ingest/snapshot", full); got["unchanged"] != true {
		t.Fatalf("re-post was not the unchanged path: %v", got)
	}
	postIngest(t, srv, "/ingest/snapshot", mirror.Snapshot{
		RepoKey:    "rk",
		Origin:     mirror.OriginHosted,
		Kinds:      []string{"story"},
		MergeKinds: []string{"ledger_event"},
		Stories:    full.Stories,
	})
	postIngest(t, srv, "/ingest/change", mirror.ChangeEvent{RepoKey: "rk", Topic: "stories", Origin: mirror.OriginHosted})

	settle(t, p, func() int { return rec.countPath(settlePath) })
	if n := rec.countPath("/repo/rk"); n != 0 {
		t.Fatalf("hosted-origin ingests pushed %d time(s), want 0: %v", n, rec.snapshot())
	}

	full.Origin = mirror.OriginLocal
	postIngest(t, srv, "/ingest/snapshot", full)
	waitFor(t, waitBudget, func() bool { return rec.countPath("/repo/rk") == 1 })
}

// sty_c203fd62 AC2: a push that both holds stories and is refused records only
// the refusal as its failure reason. The held lines the push prints to stdout
// are no failure, and never reach the reason.
func TestPusherRecordsOnlyRefusalsAsTheReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binary is a shell script")
	}
	bin := filepath.Join(t.TempDir(), "satelle")
	script := "#!/bin/sh\n" +
		"echo 'held sty_x: uncommitted changes in /wt/sty_x'\n" +
		"echo 'the story sty_r was not published: archived task refused' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	var mu sync.Mutex
	var reasons []string
	p := &Pusher{
		Push:    func(ctx context.Context, path string) error { return pushWithBinary(ctx, bin, path) },
		Resolve: func(context.Context, string) (string, bool) { return repo, true },
		Record: func(_ string, success bool, reason string) {
			mu.Lock()
			defer mu.Unlock()
			if !success {
				reasons = append(reasons, reason)
			}
		},
	}
	testPusher(t, p)
	p.Notify("rk")
	waitFor(t, waitBudget, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(reasons) == 1
	})
	mu.Lock()
	reason := reasons[0]
	mu.Unlock()
	if !strings.Contains(reason, "the story sty_r was not published: archived task refused") {
		t.Errorf("the refusal is missing from the reason: %q", reason)
	}
	if strings.Contains(reason, "held sty_x") || strings.Contains(reason, "uncommitted changes") {
		t.Errorf("a held story leaked into the failure reason: %q", reason)
	}
}

// A failing push that writes nothing to stderr still has a reason: its stdout.
func TestPushWithBinaryFallsBackToStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binary is a shell script")
	}
	bin := filepath.Join(t.TempDir(), "satelle")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'only on stdout'\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := pushWithBinary(context.Background(), bin, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "only on stdout") {
		t.Fatalf("err = %v, want the stdout as the reason", err)
	}
}
