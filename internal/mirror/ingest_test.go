package mirror_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// sty_a003a459 AC1: a merge-only snapshot for a partition only a change event
// has touched is answered 409 partition_unknown and writes no rows; once a full
// body has landed, the same merge body is applied.
func TestIngestMergeOnlyNeedsSeededPartition(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	mux := http.NewServeMux()
	(&mirror.IngestHandler{Store: s}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	post := func(path string, v any) (int, string) {
		t.Helper()
		body, _ := json.Marshal(v)
		resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, _ := post("/ingest/change", mirror.ChangeEvent{RepoKey: "rk-new", Topic: "stories"}); code != 200 {
		t.Fatalf("change event: %d", code)
	}
	delta := mirror.Snapshot{
		RepoKey:    "rk-new",
		Kinds:      []string{"seat", "identity"},
		MergeKinds: []string{"story", "task", "execution", "wave", "ledger_event"},
		Stories:    []json.RawMessage{[]byte(`{"id":"sty_edit","title":"Edited"}`)},
	}
	code, body := post("/ingest/snapshot", delta)
	if code != http.StatusConflict || !strings.HasPrefix(body, mirror.CodePartitionUnknown) {
		t.Fatalf("delta to an unseeded partition: %d %q, want 409 %s", code, body, mirror.CodePartitionUnknown)
	}
	if n, _ := s.CountItems(t.Context(), "rk-new", "story"); n != 0 {
		t.Fatalf("a refused delta wrote %d story row(s)", n)
	}

	full := mirror.Snapshot{RepoKey: "rk-new", Stories: []json.RawMessage{
		[]byte(`{"id":"sty_edit","title":"Old"}`), []byte(`{"id":"sty_other","title":"Other"}`),
	}}
	if code, body := post("/ingest/snapshot", full); code != 200 {
		t.Fatalf("full body: %d %s", code, body)
	}
	if code, body := post("/ingest/snapshot", delta); code != 200 {
		t.Fatalf("delta after a full body: %d %s", code, body)
	}
	stories, _ := s.ListItems(t.Context(), "rk-new", "story")
	if len(stories) != 2 || !strings.Contains(stories[0].Payload, "Edited") {
		t.Fatalf("delta must upsert its row and keep the rest: %v", stories)
	}
}

func TestIngestRemove(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := &mirror.IngestHandler{Store: s}
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Seed via snapshot.
	body, _ := json.Marshal(mirror.Snapshot{
		RepoKey: "rk-rm",
		Slug:    "rm",
		Stories: []json.RawMessage{[]byte(`{"id":"sty_x","title":"X"}`)},
	})
	resp, err := http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("snapshot %d", resp.StatusCode)
	}

	rmBody, _ := json.Marshal(mirror.RemoveEvent{RepoKey: "rk-rm"})
	resp, err = http.Post(srv.URL+"/ingest/remove", "application/json", bytes.NewReader(rmBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("remove %d", resp.StatusCode)
	}
	parts, err := s.ListPartitions(t.Context())
	if err != nil || len(parts) != 0 {
		t.Fatalf("parts=%v err=%v", parts, err)
	}
	// Unknown key is still 200 (idempotent delete).
	resp, err = http.Post(srv.URL+"/ingest/remove", "application/json", bytes.NewReader(rmBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("idempotent remove %d", resp.StatusCode)
	}
	// Missing repo_key → 400.
	resp, err = http.Post(srv.URL+"/ingest/remove", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty key status %d", resp.StatusCode)
	}
}

func TestIngestChangeAndSnapshot(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var doorbells []string
	h := &mirror.IngestHandler{Store: s, OnChange: func(topic string) { doorbells = append(doorbells, topic) }}
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// change event
	body, _ := json.Marshal(mirror.ChangeEvent{RepoKey: "rk1", Topic: "stories", Entity: "story", At: "t"})
	resp, err := http.Post(srv.URL+"/ingest/change", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("change status %d", resp.StatusCode)
	}

	// snapshot — full kinds (sty_400c022b)
	snap := mirror.Snapshot{
		RepoKey:      "rk1",
		Slug:         "demo",
		Stories:      []json.RawMessage{[]byte(`{"id":"sty_a","title":"Hello"}`)},
		Tasks:        []json.RawMessage{[]byte(`{"id":"tsk_a","title":"Task"}`)},
		Docs:         []json.RawMessage{[]byte(`{"name":"wf","kind":"workflows","body":"x","mod_time":"2026-01-01T00:00:00Z","provenance":"authored","source":"/x"}`)},
		LedgerEvents: []json.RawMessage{[]byte(`{"id":"evt_1","story_id":"sty_a","kind":"status_transition"}`)},
		Seats:        []json.RawMessage{[]byte(`{"id":"sty_a","in_flight":true,"stale":false}`)},
		Identity:     json.RawMessage(`{"project_name":"demo","repo_root":"/tmp/demo","footer_email":"op@example.com"}`),
		Settings:     json.RawMessage(`{"repo_root":"/tmp/demo","rows":[]}`),
	}
	body, _ = json.Marshal(snap)
	resp, err = http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("snapshot status %d", resp.StatusCode)
	}

	stories, err := s.ListItems(t.Context(), "rk1", "story")
	if err != nil || len(stories) != 1 {
		t.Fatalf("stories=%v err=%v", stories, err)
	}
	if !bytes.Contains([]byte(stories[0].Payload), []byte("Hello")) {
		t.Errorf("payload %s", stories[0].Payload)
	}
	for _, kind := range []string{"doc", "ledger_event", "seat", "identity", "settings"} {
		items, err := s.ListItems(t.Context(), "rk1", kind)
		if err != nil || len(items) == 0 {
			t.Errorf("kind %s: items=%v err=%v", kind, items, err)
		}
	}
	idPayload, err := s.GetItem(t.Context(), "rk1", "identity", "meta")
	if err != nil || !bytes.Contains([]byte(idPayload), []byte("op@example.com")) {
		t.Errorf("identity meta: %q err=%v", idPayload, err)
	}
	// Capture per-kind counts, then re-push — AC3: counts stable (ReplaceKind).
	counts := map[string]int{}
	for _, kind := range []string{"story", "task", "doc", "ledger_event", "seat", "identity", "settings"} {
		items, err := s.ListItems(t.Context(), "rk1", kind)
		if err != nil {
			t.Fatal(err)
		}
		counts[kind] = len(items)
	}
	// Enriched doc fields survive ListItems.
	docs, _ := s.ListItems(t.Context(), "rk1", "doc")
	if !bytes.Contains([]byte(docs[0].Payload), []byte("mod_time")) ||
		!bytes.Contains([]byte(docs[0].Payload), []byte("provenance")) ||
		!bytes.Contains([]byte(docs[0].Payload), []byte("source")) {
		t.Errorf("doc payload missing mod_time/provenance/source: %s", docs[0].Payload)
	}

	resp, err = http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, kind := range []string{"story", "task", "doc", "ledger_event", "seat", "identity", "settings"} {
		items, err := s.ListItems(t.Context(), "rk1", kind)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != counts[kind] {
			t.Errorf("after re-push kind %s count = %d, want %d", kind, len(items), counts[kind])
		}
	}
	if len(doorbells) == 0 {
		t.Error("expected OnChange doorbells")
	}
}

// TestIngestFiresOnIngest (sty_c526753a D3): change, committed snapshot, and
// the unchanged short-circuit all notify the hosted-push hook.
func TestIngestFiresOnIngest(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var keys []string
	h := &mirror.IngestHandler{Store: s, OnIngest: func(repoKey string, _ mirror.IngestOrigin) { keys = append(keys, repoKey) }}
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(mirror.ChangeEvent{RepoKey: "rk1", Topic: "stories", Entity: "story", At: "t"})
	resp, err := http.Post(srv.URL+"/ingest/change", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	snap := mirror.Snapshot{
		RepoKey: "rk1",
		Stories: []json.RawMessage{[]byte(`{"id":"sty_a","title":"Hello"}`)},
	}
	body, _ = json.Marshal(snap)
	resp, err = http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if len(keys) != 3 {
		t.Fatalf("OnIngest calls = %d, want 3 (change+snapshot+unchanged): %v", len(keys), keys)
	}
	for i, k := range keys {
		if k != "rk1" {
			t.Errorf("keys[%d] = %q", i, k)
		}
	}
}

func TestIngestRejectsNonPOST(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := &mirror.IngestHandler{Store: s}
	mux := http.NewServeMux()
	h.Mount(mux)
	req := httptest.NewRequest(http.MethodGet, "/ingest/change", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d", rr.Code)
	}
}

// TestIngestPartialSnapshotLeavesOtherKinds (sty_3562c820): a light drain with
// Kinds set replaces only those partitions; docs (etc.) stay put. snap_hash is
// cleared so the next full reconcile cannot short-circuit against a light body.
func TestIngestPartialSnapshotLeavesOtherKinds(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := &mirror.IngestHandler{Store: s}
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	post := func(snap mirror.Snapshot) int {
		t.Helper()
		body, _ := json.Marshal(snap)
		resp, err := http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	full := mirror.Snapshot{
		RepoKey:  "rk-partial",
		Slug:     "partial",
		Stories:  []json.RawMessage{[]byte(`{"id":"sty_old","title":"Old"}`)},
		Docs:     []json.RawMessage{[]byte(`{"name":"wf","kind":"workflows","body":"keep-me"}`)},
		Identity: json.RawMessage(`{"project_name":"p","repo_root":"/tmp/p","footer_email":""}`),
	}
	if code := post(full); code != 200 {
		t.Fatalf("full seed %d", code)
	}
	hash, err := s.SnapshotHash(t.Context(), "rk-partial")
	if err != nil || hash == "" {
		t.Fatalf("full path must set snap_hash, got %q err=%v", hash, err)
	}
	docsBefore, _ := s.ListItems(t.Context(), "rk-partial", "doc")
	if len(docsBefore) != 1 {
		t.Fatalf("docs before: %v", docsBefore)
	}

	// Light drain: new story, no docs in body, Kinds omit doc.
	light := mirror.Snapshot{
		RepoKey:      "rk-partial",
		Slug:         "partial",
		Kinds:        []string{"story", "task", "execution", "seat", "identity"},
		MergeKinds:   []string{"ledger_event"},
		Stories:      []json.RawMessage{[]byte(`{"id":"sty_new","title":"New"}`)},
		LedgerEvents: []json.RawMessage{[]byte(`{"id":"evt_1","story_id":"sty_new","kind":"status_transition"}`)},
		Identity:     json.RawMessage(`{"project_name":"p","repo_root":"/tmp/p","footer_email":""}`),
	}
	if code := post(light); code != 200 {
		t.Fatalf("light drain %d", code)
	}
	stories, _ := s.ListItems(t.Context(), "rk-partial", "story")
	if len(stories) != 1 || !bytes.Contains([]byte(stories[0].Payload), []byte("sty_new")) {
		t.Fatalf("stories after light: %v", stories)
	}
	docsAfter, _ := s.ListItems(t.Context(), "rk-partial", "doc")
	if len(docsAfter) != 1 || !bytes.Contains([]byte(docsAfter[0].Payload), []byte("keep-me")) {
		t.Fatalf("docs must survive light drain: %v", docsAfter)
	}
	led, _ := s.ListItems(t.Context(), "rk-partial", "ledger_event")
	if len(led) != 1 {
		t.Fatalf("merge ledger: %v", led)
	}
	hashAfter, _ := s.SnapshotHash(t.Context(), "rk-partial")
	if hashAfter != "" {
		t.Fatalf("partial apply must clear snap_hash, got %q", hashAfter)
	}
}

// TestApplySnapshotCancelledLeavesPriorState (sty_3562c820 AC2): a cancelled
// apply context fails before commit and leaves the previous rows intact.
func TestApplySnapshotCancelledLeavesPriorState(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	now := time.Now()
	if err := s.ReplaceKind(ctx, "rk", "story", []mirror.ItemRow{
		{ID: "sty_a", Payload: `{"id":"sty_a","title":"A"}`},
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceKind(ctx, "rk", "doc", []mirror.ItemRow{
		{ID: "wf", Payload: `{"name":"wf","body":"old"}`},
	}, now); err != nil {
		t.Fatal(err)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	err = s.ApplySnapshot(cancelCtx, "rk",
		[]mirror.KindRows{
			{Kind: "story", Items: []mirror.ItemRow{{ID: "sty_b", Payload: `{"id":"sty_b"}`}}},
			{Kind: "doc", Items: []mirror.ItemRow{{ID: "wf", Payload: `{"name":"wf","body":"new"}`}}},
		},
		nil, now)
	if err == nil {
		t.Fatal("expected cancelled ApplySnapshot to fail")
	}
	stories, _ := s.ListItems(ctx, "rk", "story")
	if len(stories) != 1 || stories[0].ID != "sty_a" {
		t.Fatalf("story rows after cancelled apply: %v", stories)
	}
	docs, _ := s.ListItems(ctx, "rk", "doc")
	if len(docs) != 1 || !bytes.Contains([]byte(docs[0].Payload), []byte("old")) {
		t.Fatalf("doc rows after cancelled apply: %v", docs)
	}
}

// TestIngestSnapshotSlugConflict (sty_57d5ce25): same slug + different repo_key
// is 409; same repo_key re-push and empty slugs stay OK.
func TestIngestSnapshotSlugConflict(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := &mirror.IngestHandler{Store: s}
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	post := func(snap mirror.Snapshot) (int, string) {
		t.Helper()
		body, _ := json.Marshal(snap)
		resp, err := http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(raw)
	}

	first := mirror.Snapshot{
		RepoKey:  "x-aaaa",
		Slug:     "x",
		Identity: json.RawMessage(`{"project_name":"x","repo_root":"/tmp/x-a","footer_email":""}`),
	}
	code, _ := post(first)
	if code != 200 {
		t.Fatalf("first seed status %d", code)
	}

	// Different repo_key, same slug → 409 with existing key + path in body.
	code, body := post(mirror.Snapshot{
		RepoKey:  "x-bbbb",
		Slug:     "x",
		Identity: json.RawMessage(`{"project_name":"x","repo_root":"/tmp/x-b","footer_email":""}`),
	})
	if code != http.StatusConflict {
		t.Fatalf("collision status %d, want 409; body=%s", code, body)
	}
	for _, want := range []string{"x-aaaa", "/tmp/x-a", "x-bbbb", `slug "x"`} {
		if !strings.Contains(body, want) {
			t.Errorf("409 body missing %q: %s", want, body)
		}
	}

	// Same repo_key re-push is fine.
	code, _ = post(first)
	if code != 200 {
		t.Fatalf("re-push same key status %d", code)
	}

	// Empty slugs never conflict.
	code, _ = post(mirror.Snapshot{RepoKey: "empty-1", Slug: ""})
	if code != 200 {
		t.Fatalf("empty slug 1 status %d", code)
	}
	code, _ = post(mirror.Snapshot{RepoKey: "empty-2", Slug: ""})
	if code != 200 {
		t.Fatalf("empty slug 2 status %d", code)
	}
}

// The queue partition replaces on a full snapshot and merges on a delta, and
// a snapshot and a queues change event ring the queues doorbell.
func TestIngestQueues(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var doorbells []string
	h := &mirror.IngestHandler{Store: s, OnChange: func(topic string) { doorbells = append(doorbells, topic) }}
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	post := func(path string, v any) {
		t.Helper()
		body, _ := json.Marshal(v)
		resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s status %d", path, resp.StatusCode)
		}
	}
	ids := func() []string {
		rows, err := s.ListItems(t.Context(), "rk1", "queue")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	q := func(id string) json.RawMessage { return []byte(`{"id":"` + id + `","kind":"queue","title":"q"}`) }
	post("/ingest/snapshot", mirror.Snapshot{RepoKey: "rk1", Slug: "demo", Queues: []json.RawMessage{q("que_a"), q("que_b")},
		Identity: json.RawMessage(`{"project_name":"demo"}`)})
	if got := ids(); len(got) != 2 {
		t.Fatalf("full: %v", got)
	}
	if !slices.Contains(doorbells, "queues") {
		t.Fatalf("snapshot doorbells %v", doorbells)
	}
	post("/ingest/snapshot", mirror.Snapshot{RepoKey: "rk1", Slug: "demo", Queues: []json.RawMessage{q("que_c")},
		Kinds: []string{"identity"}, MergeKinds: []string{"queue"}, Identity: json.RawMessage(`{"project_name":"demo"}`)})
	if got := ids(); len(got) != 3 {
		t.Fatalf("delta merge: %v", got)
	}
	post("/ingest/snapshot", mirror.Snapshot{RepoKey: "rk1", Slug: "demo", Queues: []json.RawMessage{q("que_z")},
		Identity: json.RawMessage(`{"project_name":"demo"}`)})
	if got := ids(); len(got) != 1 || got[0] != "que_z" {
		t.Fatalf("full replace: %v", got)
	}
	doorbells = nil
	post("/ingest/change", mirror.ChangeEvent{RepoKey: "rk1", Topic: "queues", Entity: "queue", At: "t"})
	if !slices.Contains(doorbells, "queues") {
		t.Fatalf("change doorbells %v", doorbells)
	}
}
