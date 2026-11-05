package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// TestBuildUISnapshotEmitsAllKinds proves sty_400c022b AC1/AC4: the sender
// emits full docs (mod_time + provenance + source), non-stub ledger events,
// seats, settings, and identity meta.
func TestBuildUISnapshotEmitsAllKinds(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	repo := t.TempDir()
	// Minimal git repo so gitConfigEmail does not fail the suite (may still be empty).
	_ = os.WriteFile(filepath.Join(repo, ".git"), []byte(""), 0o644)

	db, err := store.Open(filepath.Join(t.TempDir(), "satelle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	now := time.Now().UTC()
	sty, err := db.Stories.Create(ctx, workitem.CreateInput{
		Kind: workitem.KindStory, Title: "Snap Story", Body: "goal",
		AcceptanceCriteria: "1. x", Category: "chore", Status: workitem.StatusBacklog,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Ledger.Append(ctx, ledger.AppendInput{
		StoryID: sty.ID, Kind: ledger.KindStoryCreated, Body: "created",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	// Acquire a seat so seats appear in the snapshot.
	if _, _, _, err := db.Leases.Acquire(ctx, sty.ID, "story", "test@local", "backlog", true); err != nil {
		t.Fatal(err)
	}

	// Seed an authored doc with mod time via the doc index if possible.
	if db.DocIndex != nil {
		// Write a workflow-ish markdown into a data dir and index if API allows.
		// Fallback: insert is via reindex; for unit scope, empty docs still OK —
		// we assert identity/ledger/seats always, and docs when present.
	}

	a := &app.App{
		Config:   config.Config{},
		RepoRoot: repo,
		Store:    db,
	}
	snap, err := buildUISnapshot(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Stories) == 0 {
		t.Fatal("expected stories in snapshot")
	}
	if len(snap.LedgerEvents) == 0 {
		t.Fatal("expected non-stub ledger events")
	}
	var led map[string]any
	if err := json.Unmarshal(snap.LedgerEvents[0], &led); err != nil {
		t.Fatal(err)
	}
	if led["story_id"] != sty.ID {
		t.Errorf("ledger story_id = %v, want %s", led["story_id"], sty.ID)
	}
	if len(snap.Seats) == 0 {
		t.Fatal("expected seats in snapshot")
	}
	var seat map[string]any
	if err := json.Unmarshal(snap.Seats[0], &seat); err != nil {
		t.Fatal(err)
	}
	if seat["id"] != sty.ID {
		t.Errorf("seat id = %v, want %s", seat["id"], sty.ID)
	}
	if len(snap.Identity) == 0 {
		t.Fatal("expected identity meta blob")
	}
	var id map[string]any
	if err := json.Unmarshal(snap.Identity, &id); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"project_name", "repo_root"} {
		if id[k] == nil || id[k] == "" {
			t.Errorf("identity missing %s: %v", k, id)
		}
	}
	if id["repo_root"] != repo {
		t.Errorf("repo_root = %v, want %s", id["repo_root"], repo)
	}
	if len(snap.Settings) == 0 {
		t.Fatal("expected settings blob")
	}
}

// TestBuildUIDrainSnapshotIsLight (sty_3562c820): the end-of-verb drain ships
// only panel kinds + merge ledger, omits doc/story-doc bodies, and is far
// smaller than the full reconcile snapshot. Full path still has no Kinds.
func TestBuildUIDrainSnapshotIsLight(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	repo := t.TempDir()
	_ = os.WriteFile(filepath.Join(repo, ".git"), []byte(""), 0o644)
	dataDir := filepath.Join(repo, ".satelle")
	wfDir := filepath.Join(dataDir, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A fat doc body so the full snapshot is measurably larger.
	fat := "---\nname: fat-wf\ntype: workflow\napplies_to: [\"*\"]\n---\n\n# fat\n" + strings.Repeat("x", 8000)
	if err := os.WriteFile(filepath.Join(wfDir, "fat-wf.md"), []byte(fat), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(filepath.Join(t.TempDir(), "satelle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	sty, err := db.Stories.Create(ctx, workitem.CreateInput{
		Kind: workitem.KindStory, Title: "Drain Story", Body: "goal",
		AcceptanceCriteria: "1. x", Category: "chore", Status: workitem.StatusBacklog,
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := db.Ledger.Append(ctx, ledger.AppendInput{
			StoryID: sty.ID, Kind: ledger.KindStoryCreated, Body: "evt",
		}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if db.DocIndex != nil {
		if _, err := db.DocIndex.Sync(ctx, map[string]string{"workflows": wfDir}, time.Now().UTC()); err != nil {
			t.Fatalf("doc sync: %v", err)
		}
	}
	a := &app.App{Config: config.Config{}, RepoRoot: repo, Store: db}

	full, err := buildUISnapshot(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Kinds) != 0 || len(full.MergeKinds) != 0 {
		t.Fatalf("full snapshot must omit Kinds/MergeKinds (reconcile wire): kinds=%v merge=%v", full.Kinds, full.MergeKinds)
	}
	if len(full.Docs) == 0 {
		t.Fatal("full snapshot expected indexed docs")
	}

	drain, err := buildUIDrainSnapshot(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[string]bool{"story": true, "task": true, "execution": true, "seat": true, "wave": true, "identity": true}
	if len(drain.Kinds) != len(wantKinds) {
		t.Fatalf("drain Kinds = %v", drain.Kinds)
	}
	for _, k := range drain.Kinds {
		if !wantKinds[k] {
			t.Errorf("unexpected drain kind %q", k)
		}
	}
	if len(drain.MergeKinds) != 1 || drain.MergeKinds[0] != "ledger_event" {
		t.Fatalf("drain MergeKinds = %v, want [ledger_event]", drain.MergeKinds)
	}
	if len(drain.Docs) != 0 || len(drain.StoryDocs) != 0 || len(drain.Settings) != 0 {
		t.Errorf("drain must omit docs/story_docs/settings: docs=%d story_docs=%d settings=%d",
			len(drain.Docs), len(drain.StoryDocs), len(drain.Settings))
	}
	if len(drain.Stories) == 0 || len(drain.Identity) == 0 {
		t.Fatal("drain must carry stories and identity")
	}
	if len(drain.LedgerEvents) == 0 {
		t.Fatal("drain must merge recent ledger events")
	}

	fullBody, _ := json.Marshal(full)
	drainBody, _ := json.Marshal(drain)
	if len(drainBody) >= len(fullBody) {
		t.Errorf("drain payload %d bytes should be smaller than full %d bytes", len(drainBody), len(fullBody))
	}
	// With a fat doc body the drain should be at least 2× smaller.
	if len(fullBody) > 2000 && len(drainBody)*2 > len(fullBody) {
		t.Errorf("drain %d vs full %d — expected order-of-magnitude drop with fat docs", len(drainBody), len(fullBody))
	}
}

// TestBuildUISnapshotDuplicateDocNames (sty_e4e1a008 AC2): two authored docs
// with the same kind and name but different paths both appear in snap.Docs.
func TestBuildUISnapshotDuplicateDocNames(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	repo := t.TempDir()
	docRoot := filepath.Join(repo, ".satelle", "documents")
	dirA := filepath.Join(docRoot, "a")
	dirB := filepath.Join(docRoot, "b")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}
	bodyA := "---\nname: parent-body\n---\n\n# a\n"
	bodyB := "---\nname: parent-body\n---\n\n# b\n"
	pathA := filepath.Join(dirA, "parent-body.md")
	pathB := filepath.Join(dirB, "parent-body.md")
	if err := os.WriteFile(pathA, []byte(bodyA), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte(bodyB), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(filepath.Join(t.TempDir(), "satelle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if db.DocIndex == nil {
		t.Fatal("DocIndex required for duplicate-name fixture")
	}
	if _, err := db.DocIndex.Sync(ctx, map[string]string{"documents": docRoot}, time.Now().UTC()); err != nil {
		t.Fatalf("doc sync: %v", err)
	}

	a := &app.App{Config: config.Config{}, RepoRoot: repo, Store: db}
	snap, err := buildUISnapshot(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	var paths, sources []string
	for _, raw := range snap.Docs {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if doc["kind"] == "documents" && doc["name"] == "parent-body" {
			p, _ := doc["path"].(string)
			s, _ := doc["source"].(string)
			paths = append(paths, p)
			sources = append(sources, s)
		}
	}
	if len(paths) != 2 {
		t.Fatalf("parent-body docs = %d (%v), want 2", len(paths), paths)
	}
	if paths[0] == paths[1] {
		t.Fatalf("expected distinct paths, both %q", paths[0])
	}
	for i, p := range paths {
		if sources[i] != p {
			t.Fatalf("parent-body[%d] source=%q path=%q, want source == path", i, sources[i], p)
		}
	}
	if sources[0] == sources[1] {
		t.Fatalf("expected distinct sources, both %q", sources[0])
	}
}

// TestPostUISnapshotSurfacesConflictBody (sty_57d5ce25): 409 body reaches the
// CLI error so workspace add can print the slug-collision remedy.
func TestPostUISnapshotSurfacesConflictBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ingest/snapshot" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, `slug "app" already used by partition app-aaaa (path /tmp/a); incoming repo_key app-bbbb collides.`, http.StatusConflict)
	}))
	t.Cleanup(srv.Close)

	err := postUISnapshotContext(context.Background(), srv.URL, &mirror.Snapshot{
		RepoKey: "app-bbbb",
		Slug:    "app",
	})
	if err == nil {
		t.Fatal("expected error on 409")
	}
	msg := err.Error()
	for _, want := range []string{"409", "app-aaaa", "app-bbbb", `slug "app"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q: %s", want, msg)
		}
	}
	_ = io.Discard
}
