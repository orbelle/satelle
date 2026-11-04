package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// ingestServer is a real mirror ingest endpoint over ms.
func ingestServer(t *testing.T, ms *mirror.Store) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	(&mirror.IngestHandler{Store: ms}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func postStory(ctx context.Context, base, repoKey, id, status string) error {
	body, _ := json.Marshal(mirror.Snapshot{RepoKey: repoKey, Slug: repoKey,
		Stories: []json.RawMessage{json.RawMessage(`{"id":"` + id + `","status":"` + status + `"}`)}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/ingest/snapshot", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// TestReconcileRetriesFailedReseedBeforeInterval: a reseed that times out is
// logged as a failure and re-run on the retry tick, well inside the pass
// interval; the retry lands in the mirror and logs the recovery.
func TestReconcileRetriesFailedReseedBeforeInterval(t *testing.T) {
	ms := openTestMirror(t)
	srv := ingestServer(t, ms)

	var mu sync.Mutex
	var calls []time.Time
	lines := &lineRecorder{}
	rec := &Reconciler{
		Interval:   time.Hour,
		RetryDelay: 20 * time.Millisecond,
		RetryTick:  5 * time.Millisecond,
		Targets:    func(context.Context) []string { return []string{"/repo/x"} },
		Log:        lines.log,
		Reseed: func(ctx context.Context, path string) error {
			mu.Lock()
			calls = append(calls, time.Now())
			n := len(calls)
			mu.Unlock()
			if n == 1 {
				return context.DeadlineExceeded
			}
			return postStory(ctx, srv.URL, "rk-x", "sty_x", "in_progress")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	done := make(chan struct{})
	go func() { rec.Loop(ctx); close(done) }()

	waitFor(t, waitBudget, func() bool { return storyStatus(t, ms, "rk-x", "sty_x") == "in_progress" })
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(calls) < 2 {
		t.Fatalf("reseed calls = %d, want a retry after the failure", len(calls))
	}
	if gap := calls[1].Sub(calls[0]); gap < rec.RetryDelay || gap > time.Minute {
		t.Errorf("retry ran %s after the failure, want at least the retry delay and far inside the %s interval", gap, rec.Interval)
	}
	if time.Since(start) >= rec.Interval {
		t.Fatal("repair waited for the full interval")
	}
	got := lines.snapshot()
	if countLines(got, "mirror reconcile /repo/x: context deadline exceeded") != 1 {
		t.Errorf("failure line not written once: %q", got)
	}
	if countLines(got, "mirror reconcile /repo/x: recovered after 1 failed pass(es)") != 1 {
		t.Errorf("recovered line not written once: %q", got)
	}
}

// TestRetryDelayDoublesToInterval: consecutive failures double the retry
// delay, capped at the pass interval, and a success clears it.
func TestRetryDelayDoublesToInterval(t *testing.T) {
	now := time.Unix(0, 0)
	r := &Reconciler{Interval: 100 * time.Second, RetryDelay: 30 * time.Second, Now: func() time.Time { return now }}
	fail := context.DeadlineExceeded
	for _, want := range []time.Duration{30 * time.Second, 60 * time.Second, 100 * time.Second, 100 * time.Second} {
		r.schedule("/p", fail)
		if got := r.retry["/p"].at.Sub(now); got != want {
			t.Fatalf("retry delay = %s, want %s", got, want)
		}
	}
	r.schedule("/p", nil)
	if _, ok := r.retry["/p"]; ok {
		t.Fatal("a success must clear the retry")
	}
}

// TestRetryTickReseedsDirtyTarget: a repo the CLI marked dirty is reseeded on
// the retry tick and its mark cleared; a mark for a path that is not a target
// is dropped without a reseed.
func TestRetryTickReseedsDirtyTarget(t *testing.T) {
	home := t.TempDir()
	var mu sync.Mutex
	var reseeded []string
	r := &Reconciler{
		Interval:  time.Hour,
		DirtyHome: home,
		Targets:   func(context.Context) []string { return []string{"/repo/a"} },
		Reseed: func(_ context.Context, path string) error {
			mu.Lock()
			defer mu.Unlock()
			reseeded = append(reseeded, path)
			return nil
		},
	}
	for _, p := range []string{"/repo/a", "/elsewhere"} {
		if err := mirror.MarkDirty(home, p); err != nil {
			t.Fatal(err)
		}
	}
	r.retryPass(context.Background())
	if len(reseeded) != 1 || reseeded[0] != "/repo/a" {
		t.Fatalf("reseeded = %v, want only the dirty target", reseeded)
	}
	if left := mirror.DirtyRepos(home); len(left) != 0 {
		t.Fatalf("dirty marks left = %v, want none", left)
	}
	r.retryPass(context.Background())
	if len(reseeded) != 1 {
		t.Fatalf("a clean repo was reseeded again: %v", reseeded)
	}
}
