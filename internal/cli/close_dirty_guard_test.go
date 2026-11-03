package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A close releases the engagement seat, and with it the permission to commit the
// slice. These tests drive the real story-set verb over a real store and a real
// git tree, with the verb's dirty-path probe wired to the edit gate's own
// classifier (dirtyGatedPaths), the way app.go wires it.

// countingGater counts the gate runs a transition reaches and accepts each one.
type countingGater struct{ calls atomic.Int32 }

func (g *countingGater) Gate(context.Context, workitem.Item, string) (verb.GateDecision, error) {
	g.calls.Add(1)
	return verb.GateDecision{Gated: true, Accept: true}, nil
}

// The close route is minimal and declares no gate on any edge: backlog →
// in_progress → done, plus a park state and a cancel.
const closeRouteDone = `["*"]
obligations = ["raised", "coded", "closed"]
park = { state = "blocked" }
cancel = { state = "cancelled" }
`

const closeRouteStep = `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`

type closeEnv struct {
	t     *testing.T
	repo  string
	db    *store.DB
	gater *countingGater
}

// closeGuardEnv is a committed git repo carrying the close route, the verb layer
// wired to its own store, and a counting gater on every edge.
func closeGuardEnv(t *testing.T) *closeEnv {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	// tempRepo's config exempts nothing; the edit gate's default exempts the
	// substrate directory, which is what the exempt-only case relies on.
	exempt := "[review]\ngate_create = false\n\n[gate]\nedit_exempt_paths = [\".satelle/\"]\n"
	if err := os.WriteFile(os.Getenv("SATELLE_CONFIG"), []byte(exempt), 0o644); err != nil {
		t.Fatal(err)
	}
	if r, err := filepath.EvalSymlinks(repo); err == nil {
		repo = r
	}
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	writeRoute(t, wfDir, closeRouteDone, closeRouteStep)
	gitInitCommit(t, repo)

	withVerbWiring(t)
	cfg, cfgPath, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	root := config.RepoRootFromConfigPath(cfgPath)
	db, err := store.Open(filepath.Join(t.TempDir(), "satelle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.DocIndex.Sync(context.Background(), map[string]string{"workflows": wfDir}, time.Now()); err != nil {
		t.Fatalf("sync workflows: %v", err)
	}
	verb.SetWorkItemStore(db.Stories)
	verb.SetLedgerStore(db.Ledger)
	verb.SetTxRunner(db.InTx)
	verb.SetDocIndexStore(db.DocIndex)
	verb.SetLeaseStore(db.Leases)
	verb.SetStoryDir(filepath.Join(t.TempDir(), "stories"))
	verb.SetAuthoredDirs(cfg.ResolveAuthoredDirs(root))
	verb.SetSubstrateConfigDir(cfg.ResolveDataDir(root))
	g := &countingGater{}
	verb.SetTransitionGater(g)
	verb.SetDirtyPathsProbe(func(_ context.Context, tree string) ([]string, error) { return dirtyGatedPaths(tree) })
	return &closeEnv{t: t, repo: repo, db: db, gater: g}
}

// probeWith replaces the wired dirty-path probe for the rest of the test.
func (e *closeEnv) probeWith(f func(context.Context, string) ([]string, error)) {
	withVerbWiring(e.t)
	verb.SetDirtyPathsProbe(f)
}

// story creates a backlog story.
func (e *closeEnv) story(title string) string {
	e.t.Helper()
	var it workitem.Item
	if err := json.Unmarshal(e.dispatch("story-create", map[string]any{
		"title": title, "body": "goal", "acceptance_criteria": "1. ok", "category": "chore",
	}), &it); err != nil || it.ID == "" {
		e.t.Fatalf("story-create: id %q, err %v", it.ID, err)
	}
	return it.ID
}

func (e *closeEnv) dispatch(name string, req map[string]any) json.RawMessage {
	e.t.Helper()
	raw, _ := json.Marshal(req)
	out, err := verb.Dispatch(context.Background(), name, raw)
	if err != nil {
		e.t.Fatalf("%s: %v", name, err)
	}
	return out
}

func (e *closeEnv) set(id, status string) error {
	raw, _ := json.Marshal(map[string]any{"id": id, "status": status})
	_, err := verb.Dispatch(context.Background(), "story-set", raw)
	return err
}

func (e *closeEnv) status(id string) string {
	e.t.Helper()
	it, err := e.db.Stories.Get(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return it.Status
}

// seatHeld reports whether the story still holds a live seat.
func (e *closeEnv) seatHeld(id string) bool {
	l, err := e.db.Leases.Get(context.Background(), id)
	if errors.Is(err, lease.ErrNotFound) {
		return false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return lease.Alive(l, time.Now())
}

func (e *closeEnv) write(rel, body string) {
	e.t.Helper()
	writeRepoFile(e.t, e.repo, rel, body)
}

func (e *closeEnv) git(args ...string) {
	e.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", e.repo}, args...)...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// engage puts a fresh story in_progress and returns it.
func (e *closeEnv) engage() string {
	e.t.Helper()
	id := e.story("close guard")
	if err := e.set(id, "in_progress"); err != nil {
		e.t.Fatalf("engage: %v", err)
	}
	if !e.seatHeld(id) {
		e.t.Fatal("engage did not take the seat")
	}
	return id
}

// AC1: a cancel releases the seat just as done does, but the route synthesises
// the cancel sink without a terminal shape. It is refused the same way.
func TestCancelRefusedWhileGatedChangesUncommitted(t *testing.T) {
	e := closeGuardEnv(t)
	id := e.engage()
	e.write("foo.go", "package foo\n")

	err := e.set(id, "cancelled")
	if err == nil {
		t.Fatal("cancel with an uncommitted gated file must be refused")
	}
	if !strings.Contains(err.Error(), "foo.go") {
		t.Errorf("refusal must name foo.go, got: %v", err)
	}
	if got := e.status(id); got != "in_progress" {
		t.Errorf("status after a refused cancel = %q, want in_progress", got)
	}
	if !e.seatHeld(id) {
		t.Error("a refused cancel must leave the seat held")
	}
}
