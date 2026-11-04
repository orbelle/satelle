package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// fakeIngest records each snapshot body it is posted and answers 200 before
// the client sees anything, so the caller's outcome is the test's choice.
type fakeIngest struct {
	mu     sync.Mutex
	bodies []string
}

func (f *fakeIngest) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	f.mu.Lock()
	f.bodies = append(f.bodies, string(b))
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (f *fakeIngest) posts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
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
// logged as a failure and re-run by the retry pass once the retry delay has
// passed, well inside the pass interval; the retry reaches ingest and logs the
// recovery. The clock and the ingest response are the test's, so no real
// deadline or scheduling decides the outcome.
func TestReconcileRetriesFailedReseedBeforeInterval(t *testing.T) {
	ingest := &fakeIngest{}
	srv := httptest.NewServer(ingest)
	t.Cleanup(srv.Close)

	start := time.Unix(0, 0)
	now := start
	var calls []time.Time
	lines := &lineRecorder{}
	rec := &Reconciler{
		Interval:   time.Hour,
		RetryDelay: 30 * time.Second,
		Now:        func() time.Time { return now },
		Bound:      func(string) bool { return false },
		Targets:    func(context.Context) []string { return []string{"/repo/x"} },
		Log:        lines.log,
		Reseed: func(ctx context.Context, path string) error {
			calls = append(calls, now)
			if len(calls) == 1 {
				return context.DeadlineExceeded
			}
			return postStory(ctx, srv.URL, "rk-x", "sty_x", "in_progress")
		},
	}
	ctx := context.Background()

	rec.pass(ctx)
	if len(calls) != 1 {
		t.Fatalf("reseed calls after the pass = %d, want 1", len(calls))
	}
	rec.retryPass(ctx)
	now = start.Add(rec.RetryDelay - time.Nanosecond)
	rec.retryPass(ctx)
	if len(calls) != 1 {
		t.Fatalf("reseed calls before the retry delay = %d, want 1", len(calls))
	}
	now = start.Add(rec.RetryDelay)
	rec.retryPass(ctx)

	if len(calls) != 2 {
		t.Fatalf("reseed calls = %d, want a retry after the failure", len(calls))
	}
	if gap := calls[1].Sub(calls[0]); gap != rec.RetryDelay || gap >= rec.Interval {
		t.Errorf("retry ran %s after the failure, want the %s retry delay, inside the %s interval", gap, rec.RetryDelay, rec.Interval)
	}
	if posts := ingest.posts(); len(posts) != 1 || !strings.Contains(posts[0], `"id":"sty_x"`) {
		t.Errorf("ingest posts = %q, want one snapshot naming sty_x", posts)
	}
	if _, ok := rec.retry["/repo/x"]; ok {
		t.Error("the recovered retry must clear its retry entry")
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
