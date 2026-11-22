package remotenode

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/filebundle"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

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
	if err := stageIn(o, filepath.Join(nodeDir, "in"), node, []string{itemID}); err != nil {
		t.Fatal(err)
	}
	return nodeDir
}

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
	added, err := importBack(k, filepath.Join(nodeDir, "runtime"), []string{it.ID}, "nod_aaaa")
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

type taskFixture struct {
	tree   string
	k      *kernel.Kernel
	task   string
	inputs []string
	other  string
	def    FireOptions
}

func newTaskFixture(t *testing.T, check string) taskFixture {
	t.Helper()
	tree, k, it := localFixture(t, "1.0.0")
	root := filepath.Join(tree, ".satelle")
	writeFiles(t, root, map[string]string{
		"skills/chk.md": "---\ntype: check\n---\n```sh\n" + check + "\n```\n",
		"workflows/chk.toml": `[states.backlog]
start = true
[states.done]
terminal = true
[gates.chk]
skill = "chk"
[[transition]]
from = ["backlog"]
to = "done"
groups = [{ gates = ["chk"] }]
`,
	})
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "chk"}} {
		gittest.Git(t, tree, args...)
	}
	f := taskFixture{tree: tree, k: k, other: it.ID}
	task, err := k.Create(item.Fields{Kind: ptr("task"), Title: ptr("probe a task"), Inputs: &item.Selection{Kind: "story", Tag: "x"}}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	f.task = task.ID
	for _, title := range []string{"one", "two"} {
		in, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr(title), Tags: &[]string{"x"}}, "ada")
		if err != nil {
			t.Fatal(err)
		}
		f.inputs = append(f.inputs, in.ID)
	}
	if _, _, err := k.TaskRun(task.ID, "", "ada"); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, root, map[string]string{
		"remotes/tprobe/remote.toml":  "version = 1\nname = \"tprobe\"\nprovider = \"docker-local\"\nsatelle_version = \"1.0.0\"\n\n[drive]\nkind = \"task\"\ntask = \"" + task.ID + "\"\n\n[lifecycle]\nscope = \"run\"\n",
		"remotes/tprobe/satelle.toml": "[workflows]\ndefault = \"chk\"\n",
	})
	def, bundle, err := LoadLocal(root, "tprobe")
	if err != nil {
		t.Fatal(err)
	}
	f.def = FireOptions{Kernel: k, Tree: tree, DataDir: root, Remote: "tprobe", Definition: def, Bundle: bundle, Item: task.ID}
	return f
}

func (f taskFixture) chains() []string { return append([]string{f.task}, f.inputs...) }

func (f taskFixture) stage(t *testing.T, node string) string {
	t.Helper()
	inputs, err := f.k.TaskInputs(f.task)
	if err != nil {
		t.Fatal(err)
	}
	nodeDir := t.TempDir()
	if err := stageIn(f.def, filepath.Join(nodeDir, "in"), node, append([]string{f.task}, inputs...)); err != nil {
		t.Fatal(err)
	}
	return nodeDir
}

func nodeCreated(t *testing.T, runtime string, sent []string, actor string) []string {
	t.Helper()
	st, err := ledger.Open(runtime)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := st.Items()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, id := range ids {
		events, err := st.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(sent, id) && len(events) > 0 && events[0].Actor == actor {
			out = append(out, id)
		}
	}
	return out
}

func TestStageInHandsTheTaskAndItsInputs(t *testing.T) {
	f := newTaskFixture(t, "echo checked")
	nodeDir := f.stage(t, "nod_cccc")
	entries, err := os.ReadDir(filepath.Join(nodeDir, "in", "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, strings.TrimSuffix(e.Name(), ".jsonl"))
	}
	want := f.chains()
	slices.Sort(want)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("the node was handed %v, want %v (not %s, nor an earlier execution)", got, want, f.other)
	}
	var run LocalRun
	b, err := os.ReadFile(filepath.Join(nodeDir, "in", localRunFile))
	if err != nil || json.Unmarshal(b, &run) != nil || run.Run.Mode != ModeTask || run.Run.ItemID != f.task || run.NodeID != "nod_cccc" {
		t.Fatalf("run.json = %s, %v", b, err)
	}
}

func TestRunLocalRunsATask(t *testing.T) {
	f := newTaskFixture(t, "echo checked")
	const node = "nod_dddd"
	nodeDir := f.stage(t, node)
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != kernel.VerdictAccept || !strings.Contains(out.Notes, "task "+f.task+": execution exe_") ||
		!strings.Contains(out.Notes, " over 2 items") {
		t.Fatalf("outcome = %+v", out)
	}
	runtime := filepath.Join(nodeDir, "runtime")
	created := nodeCreated(t, runtime, f.chains(), "node:"+node)
	if len(created) != 1 || !strings.HasPrefix(created[0], "exe_") {
		t.Fatalf("the node created %v, want one execution", created)
	}
	exec := created[0]
	st, err := ledger.Open(runtime)
	if err != nil {
		t.Fatal(err)
	}
	events, err := st.Read(exec)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range events {
		if e.Actor != "node:"+node {
			t.Fatalf("execution event %d (%s) by %q", i, e.Kind, e.Actor)
		}
	}
	if _, err := st.Append(f.inputs[0], item.KindUpdated, "node:"+node, item.Fields{Title: ptr("one, changed by the run")}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append("que_stray", item.KindCreated, "", item.Fields{Kind: ptr("queue"), Title: ptr("a mirror")}); err != nil {
		t.Fatal(err)
	}
	before, err := f.k.Ledger.Items()
	if err != nil {
		t.Fatal(err)
	}
	added, err := importBack(f.k, runtime, f.chains(), node)
	if err != nil || added != len(events)+1 {
		t.Fatalf("importBack = %d, %v; want %d", added, err, len(events)+1)
	}
	got, err := f.k.Get(exec)
	if err != nil || got.Parent != f.task || got.Status != "done" || got.Workflow != "chk" {
		t.Fatalf("the execution here = %+v, %v", got, err)
	}
	if in, err := f.k.Get(f.inputs[0]); err != nil || in.Title != "one, changed by the run" {
		t.Fatalf("the changed input here = %+v, %v", in, err)
	}
	after, err := f.k.Ledger.Items()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(after, "que_stray") || len(after) != len(before)+1 {
		t.Fatalf("imported chains %v, from %v", after, before)
	}
	if err := f.k.Ledger.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestImportBackRefusesABrokenChain(t *testing.T) {
	f := newTaskFixture(t, "echo checked")
	const node = "nod_eeee"
	nodeDir := f.stage(t, node)
	if _, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(nodeDir, "runtime")
	exec := nodeCreated(t, runtime, f.chains(), "node:"+node)[0]
	p := filepath.Join(runtime, "ledger", exec+".jsonl")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(b), "probe a task")
	if i < 0 {
		t.Fatalf("no title in %s", b)
	}
	b[i] = 'P'
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Open(runtime); err != nil {
		t.Fatal(err)
	}
	blobs := func() int {
		e, _ := os.ReadDir(filepath.Join(filepath.Dir(f.k.Ledger.Path(f.task)), "..", "blobs"))
		return len(e)
	}
	itemsBefore, _ := f.k.Ledger.Items()
	lens := map[string]int{}
	for _, id := range f.chains() {
		ev, _ := f.k.Ledger.Read(id)
		lens[id] = len(ev)
	}
	blobsBefore := blobs()
	_, err = importBack(f.k, runtime, f.chains(), node)
	var broken *ledger.BrokenLink
	if !errors.As(err, &broken) || broken.Item != exec {
		t.Fatalf("importBack of a broken chain = %v", err)
	}
	itemsAfter, _ := f.k.Ledger.Items()
	if strings.Join(itemsAfter, " ") != strings.Join(itemsBefore, " ") {
		t.Fatalf("items %v, were %v", itemsAfter, itemsBefore)
	}
	if _, err := f.k.Get(exec); err == nil {
		t.Fatal("the broken execution was imported")
	}
	for id, n := range lens {
		if ev, _ := f.k.Ledger.Read(id); len(ev) != n {
			t.Fatalf("%s has %d events, had %d", id, len(ev), n)
		}
	}
	if blobs() != blobsBefore {
		t.Fatal("blobs were copied for a broken run")
	}
}
