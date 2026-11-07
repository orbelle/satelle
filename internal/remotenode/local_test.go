package remotenode

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/filebundle"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// probeDefinition is a docker-local item definition pinning pin.
func probeDefinition(pin string) string {
	return `version = 1
name = "probe"
provider = "docker-local"
satelle_version = "` + pin + `"

[drive]
kind = "item"

[lifecycle]
scope = "job"
`
}

// localFixture is a committed git work tree whose .satelle carries lineConfig
// and a probe remote pinning pin, with its kernel open and one item created.
func localFixture(t *testing.T, pin string) (tree string, k *kernel.Kernel, it item.Item) {
	t.Helper()
	tree = t.TempDir()
	root := filepath.Join(tree, ".satelle")
	writeFiles(t, root, lineConfig)
	writeFiles(t, root, map[string]string{"remotes/probe/remote.toml": probeDefinition(pin)})
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"commit", "-q", "-m", "init"}} {
		gittest.Git(t, tree, args...)
	}
	k, err := kernel.Open(kernel.Options{Root: root, Runtime: filepath.Join(t.TempDir(), "runtime"), Dir: tree})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	if it, err = k.Create(item.Fields{Kind: ptr("story"), Title: ptr("probe me")}, "ada"); err != nil {
		t.Fatal(err)
	}
	return tree, k, it
}

// stageNode seats the item at remote:<node> and stages a node's in/ under a
// fresh node root, as FireLocal does before it copies in/ into the container.
func stageNode(t *testing.T, tree string, k *kernel.Kernel, itemID, node string, steps int) string {
	t.Helper()
	def, bundle, err := LoadLocal(filepath.Join(tree, ".satelle"), "probe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Seat(itemID, node, kernel.LocationRemote+node, "", "ada"); err != nil {
		t.Fatal(err)
	}
	nodeDir := t.TempDir()
	o := FireOptions{Kernel: k, Tree: tree, DataDir: filepath.Join(tree, ".satelle"), Remote: "probe",
		Definition: def, Bundle: bundle, Item: itemID, Steps: steps}
	if err := stageIn(o, filepath.Join(nodeDir, "in"), node); err != nil {
		t.Fatal(err)
	}
	return nodeDir
}

// A local node, in process: it reads in/, drives one step as node:<id>, writes
// an accepting outcome, and its runtime chain is the staged chain plus its own
// events, which verify and import back into the starter's ledger.
func TestRunLocalDrivesOneStep(t *testing.T) {
	tree, k, it := localFixture(t, "1.0.0")
	nodeDir := stageNode(t, tree, k, it.ID, "nod_aaaa", 1)
	staged, err := k.Ledger.Read(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != kernel.VerdictAccept || !strings.Contains(out.Notes, "moved a → b; stopped after 1 step(s)") ||
		strings.Contains(out.Notes, "warning") || len(out.Steps) != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	var written LocalOutcome
	b, err := os.ReadFile(filepath.Join(nodeDir, "out", localOutcome))
	if err != nil || json.Unmarshal(b, &written) != nil || written.Verdict != out.Verdict || written.Notes != out.Notes {
		t.Fatalf("outcome.json = %s, %v", b, err)
	}
	st, err := ledger.Open(filepath.Join(nodeDir, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	chain, err := st.Read(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) <= len(staged) {
		t.Fatalf("the node appended nothing: %d events", len(chain))
	}
	for i, e := range chain {
		if i < len(staged) && e.Hash != staged[i].Hash {
			t.Fatalf("event %d differs from the staged chain", i)
		}
		if i >= len(staged) && e.Actor != "node:nod_aaaa" {
			t.Fatalf("event %d (%s) by %q, want node:nod_aaaa", i, e.Kind, e.Actor)
		}
	}
	added, err := importBack(k, filepath.Join(nodeDir, "runtime"), it.ID)
	if err != nil || added != len(chain)-len(staged) {
		t.Fatalf("importBack = %d, %v", added, err)
	}
	if got, err := k.Get(it.ID); err != nil || got.Status != "b" {
		t.Fatalf("the item here after import = %+v, %v", got, err)
	}
	if err := k.Ledger.Verify(); err != nil {
		t.Fatal(err)
	}
}

// A pin that is not the node's version warns in the notes, naming both, and
// the run still concludes.
func TestRunLocalWarnsOnPin(t *testing.T) {
	tree, k, it := localFixture(t, "1.0.1")
	nodeDir := stageNode(t, tree, k, it.ID, "nod_bbbb", 1)
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0+abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != kernel.VerdictAccept || !strings.Contains(out.Notes, "warning: the definition pins satelle 1.0.1; the node runs 1.0.0+abc123") {
		t.Fatalf("outcome = %+v", out)
	}
}

// LoadLocal bundles remote.toml first and the remote's other files in order,
// never a .local one; SubstrateBundle carries the tracked .satelle files only,
// never an untracked or a .local one.
func TestLoadLocalAndSubstrateBundle(t *testing.T) {
	tree, _, _ := localFixture(t, "1.0.0")
	dataDir := filepath.Join(tree, ".satelle")
	writeFiles(t, dataDir, map[string]string{
		"remotes/probe/workflows/w.toml": "w",
		"remotes/probe/skills/a.md":      "a",
		"remotes/probe/notes.local.md":   "never",
		"untracked.md":                   "untracked",
		"satelle.local.toml":             "local",
	})
	def, bundle, err := LoadLocal(dataDir, "probe")
	if err != nil || def.Name != "probe" {
		t.Fatalf("LoadLocal = %+v, %v", def, err)
	}
	entries, err := filebundle.Decode(bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	if strings.Join(paths, " ") != "remote.toml skills/a.md workflows/w.toml" {
		t.Fatalf("definition bundle = %v", paths)
	}
	if _, _, err := LoadLocal(dataDir, "absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("LoadLocal of an absent remote = %v", err)
	}
	if _, _, err := LoadLocal(dataDir, "../probe"); err == nil {
		t.Fatal("LoadLocal took a path for an id")
	}

	gittest.Git(t, tree, "add", "-f", ".satelle/satelle.local.toml")
	sub, err := SubstrateBundle(tree, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if entries, err = filebundle.Decode(sub, nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Path] = true
	}
	for _, want := range []string{"satelle.toml", "agents.toml", "workflows/line.toml", "remotes/probe/remote.toml"} {
		if !got[want] {
			t.Errorf("substrate misses %s: %v", want, got)
		}
	}
	for _, not := range []string{"untracked.md", "satelle.local.toml", "remotes/probe/skills/a.md"} {
		if got[not] {
			t.Errorf("substrate carries %s", not)
		}
	}
}

// LocalRuns round-trips a run; RunFinished is concluded or stopped.
func TestLocalRunsAndRunFinished(t *testing.T) {
	runs := LocalRuns{Dir: t.TempDir()}
	if _, err := runs.Load("probe", "sto_1"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load with no run = %v", err)
	}
	if err := runs.Save("probe", "sto_1", &satellepb.ItemRun{State: RunConcluded, NodeId: "nod_1", Verdict: "accept", Notes: "n"}); err != nil {
		t.Fatal(err)
	}
	run, err := runs.Load("probe", "sto_1")
	if err != nil || run.GetState() != RunConcluded || run.GetNodeId() != "nod_1" || run.GetNotes() != "n" {
		t.Fatalf("Load = %v, %v", run, err)
	}
	if RunFinished(RunRunning) || !RunFinished(RunConcluded) || !RunFinished(RunStopped) {
		t.Fatal("RunFinished")
	}
}
