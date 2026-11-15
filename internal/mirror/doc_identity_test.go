package mirror_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

func TestApplySnapshotSameNameDifferentPaths(t *testing.T) {
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

	pathA := ".satelle/documents/a/epic-review.md"
	pathB := ".satelle/documents/b/epic-review.md"
	snap := mirror.Snapshot{
		RepoKey: "rk-dup-name",
		Slug:    "dup-name",
		Docs: []json.RawMessage{
			[]byte(`{"name":"epic-review","kind":"documents","path":"` + pathA + `","body":"a"}`),
			[]byte(`{"name":"epic-review","kind":"documents","path":"` + pathB + `","body":"b"}`),
		},
	}
	body, _ := json.Marshal(snap)
	resp, err := http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("snapshot status %d", resp.StatusCode)
	}

	docs, err := s.ListItems(t.Context(), "rk-dup-name", "doc")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("docs = %d, want 2", len(docs))
	}
	ids := map[string]bool{}
	for _, d := range docs {
		ids[d.ID] = true
	}
	if !ids[pathA] || !ids[pathB] {
		t.Fatalf("doc ids = %v, want %q and %q", ids, pathA, pathB)
	}
}

func TestApplySnapshotDuplicateIdentityCollision(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now()
	if _, err := s.TouchPartition(ctx, "rk-col", "col", now); err != nil {
		t.Fatal(err)
	}

	dupPath := ".satelle/documents/a/epic-review.md"
	err = s.ApplySnapshot(ctx, "rk-col", []mirror.KindRows{{
		Kind: "doc",
		Items: []mirror.ItemRow{
			{ID: dupPath, Payload: `{"name":"epic-review","path":"` + dupPath + `","body":"a"}`},
			{ID: dupPath, Payload: `{"name":"epic-review","path":"` + dupPath + `","body":"b"}`},
		},
	}}, nil, now)
	if err == nil {
		t.Fatal("expected CollisionError")
	}
	var ce *mirror.CollisionError
	if !errors.As(err, &ce) {
		t.Fatalf("err type %T: %v", err, err)
	}
	if ce.Kind != "doc" || ce.ID != dupPath {
		t.Fatalf("CollisionError = %+v, want kind=doc id=%q", ce, dupPath)
	}
	if !bytes.Contains([]byte(ce.Error()), []byte("doc")) || !bytes.Contains([]byte(ce.Error()), []byte(dupPath)) {
		t.Fatalf("Error() missing kind/id: %s", ce.Error())
	}
}

func TestSnapshotDocKeylessFallsBackToKindName(t *testing.T) {
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

	snap := mirror.Snapshot{
		RepoKey: "rk-keyless",
		Slug:    "keyless",
		Docs: []json.RawMessage{
			[]byte(`{"name":"parent-body","body":"x"}`),
			[]byte(`{"name":"wf","kind":"workflows","body":"y"}`),
		},
	}
	body, _ := json.Marshal(snap)
	resp, err := http.Post(srv.URL+"/ingest/snapshot", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("snapshot status %d", resp.StatusCode)
	}

	docs, err := s.ListItems(t.Context(), "rk-keyless", "doc")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, d := range docs {
		ids[d.ID] = true
	}
	if !ids["documents/parent-body"] {
		t.Fatalf("missing documents/parent-body fallback id; got %v", ids)
	}
	if !ids["workflows/wf"] {
		t.Fatalf("missing workflows/wf fallback id; got %v", ids)
	}
}
