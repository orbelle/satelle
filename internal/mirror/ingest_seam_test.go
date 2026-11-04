package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// seamServer mounts an IngestHandler on s whose apply runs through apply and
// whose Log lines are captured.
func seamServer(t *testing.T, s *Store, apply func(ctx context.Context, repoKey string, replace, merge []KindRows, now time.Time) error) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	h := &IngestHandler{Store: s, apply: apply, Log: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}}
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

func postSnapshotStatus(t *testing.T, base string, snap Snapshot) int {
	t.Helper()
	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(base+"/ingest/snapshot", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

var seamSnap = Snapshot{
	RepoKey: "rk-seam", Slug: "seam",
	Stories:      []json.RawMessage{json.RawMessage(`{"id":"sty_1","status":"plan"}`), json.RawMessage(`{"id":"sty_2","status":"done"}`)},
	LedgerEvents: []json.RawMessage{json.RawMessage(`{"id":"evt_1"}`)},
	Docs:         []json.RawMessage{json.RawMessage(`{"path":"a.md"}`)},
}

// TestSlowIngestLogsPhases proves a snapshot ingest past slowIngest writes one
// line naming the repo, body size, rows per kind and each phase's elapsed time.
func TestSlowIngestLogsPhases(t *testing.T) {
	s := openStore(t)
	srv, lines := seamServer(t, s, func(ctx context.Context, repoKey string, replace, merge []KindRows, now time.Time) error {
		time.Sleep(slowIngest + 100*time.Millisecond) // time-subject: the apply phase must exceed the slow-ingest threshold
		return s.ApplySnapshot(ctx, repoKey, replace, merge, now)
	})
	if code := postSnapshotStatus(t, srv.URL, seamSnap); code != http.StatusOK {
		t.Fatalf("slow ingest status %d", code)
	}
	got := lines()
	if len(got) != 1 {
		t.Fatalf("want one ingest line, got %q", got)
	}
	line := got[0]
	for _, want := range []string{"ingest snapshot rk-seam:", "bytes=", "stories=2", "ledger=1", "docs=1", "read=", "apply=1.", "on_ingest=", "total="} {
		if !strings.Contains(line, want) {
			t.Errorf("ingest line missing %q: %s", want, line)
		}
	}
	if strings.Contains(line, "err=") {
		t.Errorf("a successful slow ingest carries no err: %s", line)
	}
}

// TestFailedIngestLogsAndKeepsFreshness proves a push whose apply fails is
// logged with its error and leaves the partition's last confirmed ingest where
// it was, so the page cannot read a failed push as fresh data.
func TestFailedIngestLogsAndKeepsFreshness(t *testing.T) {
	s := openStore(t)
	var failApply atomic.Bool
	srv, lines := seamServer(t, s, func(ctx context.Context, repoKey string, replace, merge []KindRows, now time.Time) error {
		if failApply.Load() {
			return errors.New("apply refused")
		}
		return s.ApplySnapshot(ctx, repoKey, replace, merge, now)
	})
	if code := postSnapshotStatus(t, srv.URL, seamSnap); code != http.StatusOK {
		t.Fatalf("seed ingest status %d", code)
	}
	before, ok := partitionOf(t, s, "rk-seam").LastIngest()
	if !ok {
		t.Fatal("a committed snapshot must record its ingest time")
	}

	failApply.Store(true)
	time.Sleep(2 * time.Millisecond) // time-subject: timestamp separation, so a wrongly moved updated_at is visible
	changed := seamSnap
	changed.Stories = []json.RawMessage{json.RawMessage(`{"id":"sty_1","status":"in_progress"}`)}
	if code := postSnapshotStatus(t, srv.URL, changed); code != http.StatusInternalServerError {
		t.Fatalf("failed apply status %d, want 500", code)
	}
	after, _ := partitionOf(t, s, "rk-seam").LastIngest()
	if !after.Equal(before) {
		t.Errorf("LastIngest moved on a failed apply: %s -> %s", before, after)
	}
	got := lines()
	if len(got) != 1 || !strings.Contains(got[0], "ingest snapshot rk-seam:") || !strings.Contains(got[0], "err=apply refused") {
		t.Fatalf("want one failure line with err=, got %q", got)
	}

	failApply.Store(false)
	time.Sleep(2 * time.Millisecond) // time-subject: timestamp separation, so a committed apply visibly moves updated_at
	if code := postSnapshotStatus(t, srv.URL, changed); code != http.StatusOK {
		t.Fatalf("recovering ingest status %d", code)
	}
	if fresh, _ := partitionOf(t, s, "rk-seam").LastIngest(); !fresh.After(before) {
		t.Errorf("LastIngest must move on a committed apply: %s -> %s", before, fresh)
	}
}
