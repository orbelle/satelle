package remotenode

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/filebundle"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotedef"
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
	if _, _, err := stageIn(o, filepath.Join(nodeDir, "in"), node, []string{itemID}); err != nil {
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
	res, err := importBack(k, filepath.Join(nodeDir, "runtime"), []string{it.ID}, "nod_aaaa", nil)
	if err != nil || res.Added != len(chain)-len(staged) || len(res.Forked) != 0 {
		t.Fatalf("importBack = %+v, %v", res, err)
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
	if _, _, err := stageIn(f.def, filepath.Join(nodeDir, "in"), node, append([]string{f.task}, inputs...)); err != nil {
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
	res, err := importBack(f.k, runtime, f.chains(), node, nil)
	if err != nil || res.Added != len(events)+1 || len(res.Forked) != 0 {
		t.Fatalf("importBack = %+v, %v; want %d added and no fork", res, err, len(events)+1)
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
	_, err = importBack(f.k, runtime, f.chains(), node, nil)
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

func TestImportBackRefusesAForkedChainAndImportsTheRest(t *testing.T) {
	f := newTaskFixture(t, "echo checked")
	const node = "nod_f0rk"
	nodeDir := f.stage(t, node)
	if _, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(nodeDir, "runtime")
	exec := nodeCreated(t, runtime, f.chains(), "node:"+node)[0]
	st, err := ledger.Open(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append(f.inputs[0], item.KindUpdated, "node:"+node, item.Fields{Title: ptr("one, changed by the run")}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append(f.inputs[1], item.KindUpdated, "node:"+node, item.Fields{Title: ptr("two, changed by the run")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.k.Update(f.inputs[0], item.Fields{Title: ptr("one, changed here meanwhile")}, "ada"); err != nil {
		t.Fatal(err)
	}
	here, err := f.k.Ledger.Read(f.inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	there, err := st.Read(f.inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	hereHead, nodeHead := here[len(here)-1].Hash, there[len(there)-1].Hash
	nodeTask, err := st.Read(f.task)
	if err != nil {
		t.Fatal(err)
	}
	res, err := importBack(f.k, runtime, f.chains(), node, nil)
	if err != nil {
		t.Fatalf("importBack = %v", err)
	}
	want := ForkedChain{Item: f.inputs[0], Seq: len(here) - 1, Here: hereHead, Node: nodeHead}
	if len(res.Forked) != 1 || res.Forked[0] != want {
		t.Fatalf("forked = %+v, want [%+v]", res.Forked, want)
	}
	after, err := f.k.Ledger.Read(f.inputs[0])
	if err != nil || len(after) != len(here) || after[len(after)-1].Hash != hereHead {
		t.Fatalf("the forked chain here = %d events, %v", len(after), err)
	}
	if in, err := f.k.Get(f.inputs[0]); err != nil || in.Title != "one, changed here meanwhile" {
		t.Fatalf("the forked input here = %+v, %v", in, err)
	}
	if got, err := f.k.Get(exec); err != nil || got.Parent != f.task {
		t.Fatalf("the execution here = %+v, %v", got, err)
	}
	if in, err := f.k.Get(f.inputs[1]); err != nil || in.Title != "two, changed by the run" {
		t.Fatalf("the other input here = %+v, %v", in, err)
	}
	if task, err := f.k.Ledger.Read(f.task); err != nil || task[len(task)-1].Hash != nodeTask[len(nodeTask)-1].Hash {
		t.Fatalf("the task chain here does not hold the node's: %v", err)
	}
	if err := f.k.Ledger.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestConcludeImportRejectsAndNamesBothHeads(t *testing.T) {
	h1, h2 := strings.Repeat("a", 64), strings.Repeat("b", 64)
	out := concludeImport(LocalOutcome{Verdict: kernel.VerdictAccept, Notes: "ran\n"},
		imported{Added: 3, Forked: []ForkedChain{{Item: "sto_x", Seq: 2, Here: h1, Node: h2}}})
	if out.Verdict != kernel.VerdictReject {
		t.Fatalf("verdict = %q", out.Verdict)
	}
	line := "diverged: sto_x at event 2: this machine holds " + ledger.ShortHash(h1) + ", the node " + ledger.ShortHash(h2) + "; not imported"
	if !strings.Contains(out.Notes, "imported 3 event(s)") || !strings.Contains(out.Notes, line) {
		t.Fatalf("notes = %q", out.Notes)
	}
}

func TestConcludeImportWithoutAForkKeepsTheVerdict(t *testing.T) {
	out := concludeImport(LocalOutcome{Verdict: kernel.VerdictAccept, Notes: "ran"}, imported{Added: 3})
	if out.Verdict != kernel.VerdictAccept || !strings.Contains(out.Notes, "imported 3 event(s)") || strings.Contains(out.Notes, "diverged") {
		t.Fatalf("outcome = %+v", out)
	}
}

const probeSecret = "SATELLE_PROBE_SECRET"

func secretFixture(t *testing.T, check string, setup []string, extra string) taskFixture {
	t.Helper()
	f := tprobeFixture(t, check, setup, extra, "[secrets]\nnames = [\""+probeSecret+"\"]\n\n")
	t.Setenv(probeSecret, "")
	return f
}

func setupFixture(t *testing.T, check string, setup []string) taskFixture {
	t.Helper()
	return tprobeFixture(t, check, setup, "", "")
}

func tprobeFixture(t *testing.T, check string, setup []string, extra, secrets string) taskFixture {
	t.Helper()
	f := newTaskFixture(t, check)
	root := filepath.Join(f.tree, ".satelle")
	var on strings.Builder
	for i, c := range setup {
		if i > 0 {
			on.WriteString(", ")
		}
		on.WriteString("'''" + c + "'''")
	}
	def := "version = 1\nname = \"tprobe\"\nprovider = \"docker-local\"\nsatelle_version = \"1.0.0\"\n\n" + secrets +
		"[drive]\nkind = \"task\"\ntask = \"" + f.task + "\"\n\n[setup]\non_create = [" + on.String() + "]\n\n[lifecycle]\nscope = \"run\"\n" + extra
	writeFiles(t, root, map[string]string{"remotes/tprobe/remote.toml": def})
	d, bundle, err := LoadLocal(root, "tprobe")
	if err != nil {
		t.Fatal(err)
	}
	f.def.Definition, f.def.Bundle = d, bundle
	t.Setenv("HOME", t.TempDir())
	return f
}

func secretPayload(t *testing.T, values map[string]string) *strings.Reader {
	t.Helper()
	b, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReader(string(b))
}

func probeCheckScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", ".satelle", "skills", "satelle-probe-secret-check.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(b), "```sh\n")
	if !ok {
		t.Fatal("the secret-probe skill has no sh block")
	}
	body, _, _ = strings.Cut(body, "```")
	return strings.TrimSpace(body)
}

func probeSetup(t *testing.T) []string {
	t.Helper()
	def, _, err := LoadLocal(filepath.Join("..", "..", ".satelle"), "docker-secret-probe")
	if err != nil {
		t.Fatal(err)
	}
	return def.Setup.OnCreate
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func multiLineSecret(t *testing.T, lines int) string {
	t.Helper()
	var b strings.Builder
	for i := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(randomHex(t, 32))
	}
	return b.String()
}

func assertNoSecret(t *testing.T, where, s, value string) {
	t.Helper()
	if !strings.Contains(s, "[secret]") {
		t.Errorf("%s holds no [secret]:\n%s", where, s)
	}
	assertNoPart(t, where, s, value)
}

func assertNoPart(t *testing.T, where, s, value string) {
	t.Helper()
	for i := 0; i+16 <= len(value); i++ {
		if strings.Contains(s, value[i:i+16]) {
			t.Errorf("%s holds a 16-byte window of the value at %d", where, i)
			return
		}
	}
	for _, line := range strings.Split(value, "\n") {
		if len(line) >= 8 && strings.Contains(s, line) {
			t.Errorf("%s holds a line of the value", where)
			return
		}
	}
}

func gateNotes(t *testing.T, runtime, id string) []string {
	t.Helper()
	st, err := ledger.Open(runtime)
	if err != nil {
		t.Fatal(err)
	}
	events, err := st.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, e := range events {
		if e.Kind != "gate.run" {
			continue
		}
		var p struct {
			Notes string `json:"notes"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		notes = append(notes, p.Notes)
	}
	return notes
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestRunLocalHandsSecretsToSetupAndSteps(t *testing.T) {
	f := secretFixture(t, probeCheckScript(t), probeSetup(t), "")
	v := randomHex(t, 32)
	const node = "nod_5ec1"
	nodeDir := f.stage(t, node)
	var log strings.Builder
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0", Log: &log,
		Secrets: secretPayload(t, map[string]string{probeSecret: v})})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != kernel.VerdictAccept {
		t.Fatalf("outcome = %+v\n%s", out, log.String())
	}
	runtime := filepath.Join(nodeDir, "runtime")
	exec := nodeCreated(t, runtime, f.chains(), "node:"+node)[0]
	notes := strings.Join(gateNotes(t, runtime, exec), "\n")
	for _, want := range []string{"step sha256:" + sha256Hex(v), "setup sha256:" + sha256Hex(v), "argv clean:", "env names: "} {
		if !strings.Contains(notes, want) {
			t.Errorf("gate.run notes miss %q:\n%s", want, notes)
		}
	}
	for where, s := range map[string]string{"notes": notes, "outcome": out.Notes, "log": log.String()} {
		if strings.Contains(s, v) {
			t.Errorf("the %s holds the value", where)
		}
	}
	if _, err := importBack(f.k, runtime, f.chains(), node, map[string]string{probeSecret: v}); err != nil {
		t.Fatalf("importBack of a clean run = %v", err)
	}
}

func TestRunLocalSetsOnlyDeclaredSecrets(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	const extra = "SATELLE_SECRET_TEST_UNDECLARED"
	t.Setenv(extra, "")
	os.Unsetenv(extra)
	nodeDir := f.stage(t, "nod_5ec2")
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0",
		Secrets: secretPayload(t, map[string]string{probeSecret: "declared-value", extra: "undeclared-value"})})
	if err != nil || out.Verdict != kernel.VerdictAccept {
		t.Fatalf("RunLocal = %+v, %v", out, err)
	}
	if v, ok := os.LookupEnv(extra); ok {
		t.Fatalf("an undeclared payload key was set: %q", v)
	}
	if v := os.Getenv(probeSecret); v != "declared-value" {
		t.Fatalf("the declared secret is %q", v)
	}
}

func TestRunLocalRefusesAMissingSecret(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	for name, in := range map[string]*strings.Reader{
		"absent": secretPayload(t, map[string]string{"OTHER": "x"}),
		"empty":  secretPayload(t, map[string]string{probeSecret: ""}),
		"broken": strings.NewReader("not json"),
	} {
		_, err := RunLocal(context.Background(), LocalOptions{Dir: f.stage(t, "nod_5ec3"), Version: "1.0.0", Secrets: in})
		if err == nil {
			t.Errorf("%s: RunLocal ran without its secret", name)
		}
	}
	if _, err := RunLocal(context.Background(), LocalOptions{Dir: f.stage(t, "nod_5ec3"), Version: "1.0.0"}); err == nil ||
		!strings.Contains(err.Error(), probeSecret) {
		t.Fatalf("RunLocal with no stdin = %v", err)
	}
}

func TestRunLocalRedactsSetupAndFailure(t *testing.T) {
	f := secretFixture(t, "echo checked", []string{`printf '%s\n' "$SATELLE_PROBE_SECRET"; exit 1`}, "")
	v := "-----BEGIN PRIVATE KEY-----\n" + multiLineSecret(t, 45) + "\n-----END PRIVATE KEY-----"
	var log strings.Builder
	_, err := RunLocal(context.Background(), LocalOptions{Dir: f.stage(t, "nod_5ec4"), Version: "1.0.0", Log: &log,
		Secrets: secretPayload(t, map[string]string{probeSecret: v})})
	if err == nil {
		t.Fatal("a failing setup did not fail the run")
	}
	assertNoSecret(t, "the log", log.String(), v)
	if !strings.Contains(err.Error(), "setup on_create command 1") {
		t.Fatalf("error = %v", err)
	}
	for i := 0; i+16 <= len(v); i++ {
		if strings.Contains(err.Error(), v[i:i+16]) {
			t.Fatal("the error holds part of the value")
		}
	}
}

func TestRunLocalRedactsCheckOutputAtTheSource(t *testing.T) {
	check := `printf '%s\n' "$SATELLE_PROBE_SECRET"
head -c 1147 /dev/zero | tr '\0' a
printf '%s' "$SATELLE_PROBE_SECRET"
head -c 5000 /dev/zero | tr '\0' b
exit 1`
	f := secretFixture(t, check, nil, "")
	v := multiLineSecret(t, 10)[:600]
	const node = "nod_5ec5"
	nodeDir := f.stage(t, node)
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0",
		Secrets: secretPayload(t, map[string]string{probeSecret: v})})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != kernel.VerdictReject {
		t.Fatalf("outcome = %+v", out)
	}
	runtime := filepath.Join(nodeDir, "runtime")
	exec := nodeCreated(t, runtime, f.chains(), "node:"+node)[0]
	notes := gateNotes(t, runtime, exec)
	if len(notes) == 0 || !strings.Contains(notes[0], "bytes elided") {
		t.Fatalf("the check's notes were not bounded: %q", notes)
	}
	assertNoSecret(t, "the gate.run notes", strings.Join(notes, "\n"), v)
	assertNoSecret(t, "the outcome (the drive's stop reason)", out.Notes, v)
	b, err := os.ReadFile(filepath.Join(nodeDir, "out", localOutcome))
	if err != nil {
		t.Fatal(err)
	}
	var written LocalOutcome
	if err := json.Unmarshal(b, &written); err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "outcome.json", written.Notes, v)
	if !strings.Contains(written.Notes, "chk: [secret]") {
		t.Errorf("the stop reason is not the redacted first line: %s", written.Notes)
	}
	if _, err := importBack(f.k, runtime, f.chains(), node, map[string]string{probeSecret: v}); err != nil {
		t.Fatalf("importBack of a run redacted at the source = %v", err)
	}
}

func TestRunLocalProgressHoldsNoSetupOutput(t *testing.T) {
	v := "-----BEGIN PRIVATE KEY-----\n" + multiLineSecret(t, 45) + "\n-----END PRIVATE KEY-----"
	steps := regexp.MustCompile(`^(start|setup|task|drive|dispatch|concluded) `)
	clean := func(where, s string) {
		t.Helper()
		assertNoPart(t, where, s, v)
		for _, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
			if !steps.MatchString(l) {
				t.Errorf("%s holds a line that is not a step: %q", where, l)
			}
		}
	}
	print := "printf '%s' '" + v + "'"
	for _, tc := range []struct {
		name, copy string
		fails      bool
	}{{"concludes", "cp ../out/progress ../snap", false}, {"fails", "cp ../out/progress ../snap; exit 1", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupFixture(t, "echo checked", []string{print, tc.copy})
			nodeDir := f.stage(t, "nod_5ec6")
			var log strings.Builder
			out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0", Log: &log, Progress: true})
			if !strings.Contains(log.String(), "-----END PRIVATE KEY-----") {
				t.Fatalf("the setup printed nothing:\n%s", log.String())
			}
			snap, err2 := os.ReadFile(filepath.Join(nodeDir, "snap"))
			if err2 != nil || string(snap) != "setup on_create 2/2\n" {
				t.Fatalf("progress during setup = %q, %v", snap, err2)
			}
			clean("the snapshot", string(snap))
			final, err2 := os.ReadFile(filepath.Join(nodeDir, "out", detachedProgress))
			if err2 != nil {
				t.Fatal(err2)
			}
			clean("the progress file", string(final))
			if tc.fails {
				if err == nil || !strings.Contains(err.Error(), "setup on_create command 2: ") {
					t.Fatalf("a failing second setup command = %v", err)
				}
				if string(final) != "setup on_create 2/2\n" {
					t.Fatalf("the progress after a failed setup = %q", final)
				}
				return
			}
			if err != nil || out.Verdict != kernel.VerdictAccept {
				t.Fatalf("RunLocal = %+v, %v\n%s", out, err, log.String())
			}
			if !regexp.MustCompile(`^concluded accept at \S+\n$`).Match(final) {
				t.Fatalf("the last progress = %q", final)
			}
		})
	}
}

func TestRunLocalRefusesProgressWithSecrets(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	f := secretFixture(t, "echo checked", []string{"touch " + marker}, "")
	nodeDir := f.stage(t, "nod_5ec7")
	_, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0", Progress: true,
		Secrets: secretPayload(t, map[string]string{probeSecret: "the-value"})})
	if err == nil || !strings.Contains(err.Error(), probeSecret) || !strings.Contains(err.Error(), "--progress") {
		t.Fatalf("RunLocal --progress with [secrets] = %v", err)
	}
	if _, err := os.Stat(filepath.Join(nodeDir, "out", detachedProgress)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused run wrote progress: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused run ran its setup: %v", err)
	}
}

func TestTakeSecretsUnsetsDeclaredNames(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	const other = "SATELLE_SECRET_TEST_OTHER"
	t.Setenv(probeSecret, "the-value")
	t.Setenv(other, "keep")
	s, err := TakeSecrets(f.def.Definition, os.LookupEnv, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.Values[probeSecret] != "the-value" || !slices.Equal(s.Names, []string{probeSecret}) || !slices.Equal(s.Redact, []string{"the-value"}) {
		t.Fatalf("TakeSecrets = %+v", s)
	}
	if v, ok := os.LookupEnv(probeSecret); ok {
		t.Fatalf("%s is still set after it was taken: %q", probeSecret, v)
	}
	if os.Getenv(other) != "keep" {
		t.Fatal("an undeclared variable was dropped")
	}
}

func TestTakeSecretsRefusesUnsetName(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	def := f.def.Definition
	def.Secrets.Names = []string{"SET_ONE", "UNSET_ONE", "EMPTY_ONE"}
	env := map[string]string{"SET_ONE": "set-value-123", "EMPTY_ONE": ""}
	lookup := func(n string) (string, bool) { v, ok := env[n]; return v, ok }
	const want = "secrets UNSET_ONE, EMPTY_ONE are not set in the firing shell's environment or in .satelle/.env"
	if _, err := TakeSecrets(def, lookup, t.TempDir()); err == nil || err.Error() != want {
		t.Fatalf("TakeSecrets with no file = %v", err)
	}
	dataDir := secretsRepo(t, true, "EMPTY_ONE=\nUNRELATED_IN_FILE=unrelated\n")
	if _, err := TakeSecrets(def, lookup, dataDir); err == nil || err.Error() != want {
		t.Fatalf("TakeSecrets with a file = %v", err)
	}
}

func TestTakeHeldSecretsTakesTheRecordedNames(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	def := f.def.Definition
	def.Secrets.Names = []string{"SATELLE_HELD_B"}
	t.Setenv("SATELLE_HELD_A", "value-of-a")
	t.Setenv("SATELLE_HELD_B", "value-of-b")
	s, err := TakeHeldSecrets(def, []string{"SATELLE_HELD_A"}, os.LookupEnv, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(s.Values, map[string]string{"SATELLE_HELD_A": "value-of-a"}) || !slices.Equal(s.Names, []string{"SATELLE_HELD_A"}) ||
		!slices.Equal(s.Redact, []string{"value-of-a"}) {
		t.Fatalf("TakeHeldSecrets = %+v", s)
	}
	for _, n := range []string{"SATELLE_HELD_A", "SATELLE_HELD_B"} {
		if v, ok := os.LookupEnv(n); ok {
			t.Fatalf("%s is still set after it was taken: %q", n, v)
		}
	}
	const want = "secret SATELLE_HELD_A was handed to the node and is not set in the shell or .satelle/.env: it is needed to redact what the node sends back and to check its record before import; set it and run the command again"
	if _, err := TakeHeldSecrets(def, []string{"SATELLE_HELD_A"}, os.LookupEnv, t.TempDir()); err == nil || err.Error() != want {
		t.Fatalf("TakeHeldSecrets with A set nowhere = %v", err)
	}
	t.Setenv("SATELLE_HELD_B", "value-of-b")
	if s, err := TakeHeldSecrets(def, nil, os.LookupEnv, t.TempDir()); err != nil || len(s.Values) != 0 || len(s.Names) != 0 {
		t.Fatalf("TakeHeldSecrets of a run handed nothing = %+v, %v", s, err)
	}
	if v, ok := os.LookupEnv("SATELLE_HELD_B"); ok {
		t.Fatalf("a declared name is still set after a run handed nothing: %q", v)
	}
}

func secretsRepo(t *testing.T, ignore bool, body string) string {
	t.Helper()
	tree := t.TempDir()
	gittest.Git(t, tree, "init", "-q")
	if ignore {
		if err := os.WriteFile(filepath.Join(tree, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return secretsDataDir(t, tree, body)
}

func secretsDataDir(t *testing.T, tree, body string) string {
	t.Helper()
	dataDir := filepath.Join(tree, ".satelle")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dataDir
}

func setLookup(names ...string) func(string) (string, bool) {
	return func(n string) (string, bool) {
		if slices.Contains(names, n) {
			return "shell-" + n, true
		}
		return "", false
	}
}

func TestTakeSecretsReadsRepoEnvFile(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	dataDir := secretsRepo(t, true, "# a comment\n"+probeSecret+"=file-value-1\nOTHER_IN_FILE=other-value\n")
	s, err := TakeSecrets(f.def.Definition, setLookup(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(s.Values, map[string]string{probeSecret: "file-value-1"}) || !slices.Equal(s.Names, []string{probeSecret}) || !slices.Equal(s.Redact, []string{"file-value-1"}) {
		t.Fatalf("TakeSecrets = %+v", s)
	}
	if slices.Contains(s.Redact, "other-value") {
		t.Fatalf("an undeclared line of the file was taken: %+v", s)
	}
}

func TestTakeSecretsShellWinsOverRepoEnvFile(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	dataDir := secretsRepo(t, true, probeSecret+"=file-value\n")
	s, err := TakeSecrets(f.def.Definition, func(string) (string, bool) { return "shell-value", true }, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Values[probeSecret] != "shell-value" || !slices.Equal(s.Redact, []string{"shell-value"}) {
		t.Fatalf("TakeSecrets = %+v", s)
	}
}

func TestTakeSecretsRefusesMalformedRepoEnvFile(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	dataDir := secretsRepo(t, true, "A=b\nSATELLE_MALFORMED_CONTENT_xyz\n")
	_, err := TakeSecrets(f.def.Definition, setLookup(probeSecret), dataDir)
	if err == nil || !strings.Contains(err.Error(), ".satelle/.env line 2") || strings.Contains(err.Error(), "SATELLE_MALFORMED_CONTENT_xyz") {
		t.Fatalf("TakeSecrets = %v", err)
	}
}

const unreadableBody = "NOT_A_PAIR_LINE\n"

func TestTakeSecretsRefusesUnignoredRepoEnvFile(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	dataDir := secretsRepo(t, false, unreadableBody)
	_, err := TakeSecrets(f.def.Definition, setLookup(probeSecret), dataDir)
	if err == nil || err.Error() != ".satelle/.env is not ignored by git: a secrets file must be untracked and ignored; no value was read from it" {
		t.Fatalf("TakeSecrets = %v", err)
	}
}

func TestTakeSecretsRefusesTrackedRepoEnvFile(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	dataDir := secretsRepo(t, true, unreadableBody)
	gittest.Git(t, filepath.Dir(dataDir), "add", "-f", ".satelle/.env")
	_, err := TakeSecrets(f.def.Definition, setLookup(probeSecret), dataDir)
	if err == nil || err.Error() != ".satelle/.env is tracked by git: a secrets file must be untracked and ignored; no value was read from it" {
		t.Fatalf("TakeSecrets = %v", err)
	}
}

func TestTakeSecretsRefusesRepoEnvFileOutsideGit(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	dataDir := secretsDataDir(t, t.TempDir(), unreadableBody)
	_, err := TakeSecrets(f.def.Definition, setLookup(probeSecret), dataDir)
	if err == nil || !strings.HasPrefix(err.Error(), ".satelle/.env: cannot tell whether git ignores it") {
		t.Fatalf("TakeSecrets = %v", err)
	}
}

func TestTakeSecretsRefusesWhenTracksErrors(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	dataDir := secretsRepo(t, true, unreadableBody)
	was := repoTracks
	t.Cleanup(func() { repoTracks = was })
	repoTracks = func(string, string) (bool, error) { return false, errors.New("ls-files failed") }
	_, err := TakeSecrets(f.def.Definition, setLookup(probeSecret), dataDir)
	if err == nil || !strings.HasPrefix(err.Error(), ".satelle/.env: cannot tell whether git tracks it") {
		t.Fatalf("TakeSecrets = %v", err)
	}
}

func TestLocalRefusalSecretWithNetworkAllow(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	def := f.def.Definition
	if err := localRefusal(def); err != nil {
		t.Fatalf("a secret with no allow entry was refused: %v", err)
	}
	allowed := def
	allowed.Network.Allow = []string{"example.com"}
	if err := localRefusal(allowed); err != nil {
		t.Fatalf("a secret beside an allow entry was refused: %v", err)
	}
	over := def
	over.Secrets.Override = map[string]remotedef.OverrideValue{"TOKEN": "satelle:token"}
	if err := localRefusal(over); err == nil || !strings.Contains(err.Error(), "[secrets.override]") {
		t.Fatalf("an override entry = %v", err)
	}
	if err := gateNodeRefusal(def); err == nil {
		t.Fatal("the gate path serves [secrets]")
	}
}

func TestRunsHereAndLocalRefusalByProvider(t *testing.T) {
	for _, p := range remotedef.Providers {
		def := remotedef.Definition{Provider: p}
		want := p == remotedef.ProviderDockerLocal || p == remotedef.ProviderCommand
		if RunsHere(def) != want {
			t.Errorf("RunsHere(%s) = %v, want %v", p, !want, want)
		}
		if err := localRefusal(def); (err == nil) != want {
			t.Errorf("localRefusal(%s) = %v", p, err)
		}
	}
	over := remotedef.Definition{Provider: remotedef.ProviderCommand,
		Secrets: remotedef.Secrets{Override: map[string]remotedef.OverrideValue{"TOKEN": "satelle:token"}}}
	if err := localRefusal(over); err == nil || !strings.Contains(err.Error(), "[secrets.override]") {
		t.Fatalf("a command remote's override entry = %v", err)
	}
}

func fakeDockerFor(t *testing.T, body string) (script, log string) {
	t.Helper()
	dir := t.TempDir()
	script, log = filepath.Join(dir, "docker"), filepath.Join(dir, "log")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$*\" >> "+log+"\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, log
}

func TestFireLocalRefusesUntakenSecrets(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	script, log := fakeDockerFor(t, "exit 0\n")
	o := f.def
	o.Docker, o.Runs = provider.DockerLocal{Docker: script}, LocalRuns{Dir: t.TempDir()}
	if _, err := FireLocal(context.Background(), o); err == nil || !strings.Contains(err.Error(), "secrets were not taken before the fire") {
		t.Fatalf("FireLocal with untaken secrets = %v", err)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("docker ran for a fire whose secrets were not taken")
	}
}

func TestFireRedactsTheNodeReport(t *testing.T) {
	v := multiLineSecret(t, 50)[:3000]
	for _, tc := range []struct {
		name, out string
		code      int
	}{
		{"straddling the tail", v + strings.Repeat("z", 1000), 1},
		{"ending with the value", "node: starting\n" + v, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := secretFixture(t, "echo checked", nil, "")
			dir := t.TempDir()
			outFile, stdinFile := filepath.Join(dir, "out"), filepath.Join(dir, "stdin")
			if err := os.WriteFile(outFile, []byte(tc.out), 0o644); err != nil {
				t.Fatal(err)
			}
			script, log := fakeDockerFor(t, `case "$1" in
  exec) if [ "$2" = -i ]; then cat > `+stdinFile+`; cat `+outFile+`; exit `+strconv.Itoa(tc.code)+`; fi; exit 0 ;;
  cp) case "$2" in satelle-node-*) exit 1 ;; esac; exit 0 ;;
  *) exit 0 ;;
esac
`)
			var out strings.Builder
			o := f.def
			o.Docker, o.Runs, o.Out = provider.DockerLocal{Docker: script}, LocalRuns{Dir: t.TempDir()}, &out
			o.Secrets = Secrets{Names: []string{probeSecret}, Values: map[string]string{probeSecret: v}, Redact: []string{v}}
			run, err := FireLocal(context.Background(), o)
			if err == nil || run == nil || run.GetState() != RunStopped {
				t.Fatalf("FireLocal = %v, %v", run, err)
			}
			assertNoSecret(t, "the run's reason", run.GetReason(), v)
			assertNoSecret(t, "the error", err.Error(), v)
			saved, serr := os.ReadFile(o.Runs.path(o.Remote, o.Item))
			if serr != nil {
				t.Fatal(serr)
			}
			if strings.Contains(string(saved), v[:16]) || strings.Contains(string(saved), v[len(v)-16:]) {
				t.Error("the saved run holds part of the value")
			}
			if strings.Contains(out.String(), v[:16]) {
				t.Error("the fire's output holds part of the value")
			}
			b, _ := os.ReadFile(log)
			if strings.Contains(string(b), v[:16]) {
				t.Error("docker's argv held the value")
			}
			if !strings.Contains(string(b), "exec -i satelle-node-") {
				t.Errorf("the node was not run with stdin:\n%s", b)
			}
			if got, _ := os.ReadFile(stdinFile); !strings.Contains(string(got), probeSecret) {
				t.Errorf("the node's stdin = %q", got)
			}
		})
	}
}

func appendToNodeChain(t *testing.T, runtime, id string, payload any) {
	t.Helper()
	st, err := ledger.Open(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append(id, item.KindUpdated, "node:x", payload); err != nil {
		t.Fatal(err)
	}
}

func TestImportBackRefusesASecretInTheRecord(t *testing.T) {
	plain := randomHex(t, 24)
	special := "line one \"quoted\" <&>\nline two " + randomHex(t, 8)
	for _, tc := range []struct {
		name  string
		value string
		leak  func(t *testing.T, f taskFixture, runtime, exec string)
	}{
		{"a string field", plain, func(t *testing.T, _ taskFixture, runtime, exec string) {
			appendToNodeChain(t, runtime, exec, item.Fields{Title: ptr("leaked " + plain)})
		}},
		{"an escaped multi-line value", special, func(t *testing.T, _ taskFixture, runtime, exec string) {
			appendToNodeChain(t, runtime, exec, item.Fields{Body: ptr("leaked:\n" + special)})
		}},
		{"an object key", plain, func(t *testing.T, _ taskFixture, runtime, exec string) {
			appendToNodeChain(t, runtime, exec, map[string]any{plain: "x"})
		}},
		{"a key in JSON held as a string", special, func(t *testing.T, _ taskFixture, runtime, exec string) {
			inner, _ := json.Marshal(map[string]int{special: 1})
			appendToNodeChain(t, runtime, exec, map[string]any{"notes": string(inner)})
		}},
		{"a blob", plain, func(t *testing.T, _ taskFixture, runtime, _ string) {
			st, err := ledger.Open(runtime)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.PutBlob([]byte("a step wrote " + plain)); err != nil {
				t.Fatal(err)
			}
		}},
		{"a forked event", plain, func(t *testing.T, f taskFixture, runtime, _ string) {
			appendToNodeChain(t, runtime, f.inputs[0], item.Fields{Title: ptr("leaked " + plain)})
			if _, err := f.k.Update(f.inputs[0], item.Fields{Title: ptr("changed here meanwhile")}, "ada"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTaskFixture(t, "echo checked")
			const node = "nod_1eak"
			nodeDir := f.stage(t, node)
			if _, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0"}); err != nil {
				t.Fatal(err)
			}
			runtime := filepath.Join(nodeDir, "runtime")
			exec := nodeCreated(t, runtime, f.chains(), "node:"+node)[0]
			tc.leak(t, f, runtime, exec)
			itemsBefore, _ := f.k.Ledger.Items()
			blobsDir := filepath.Join(f.k.Ledger.Dir(), "blobs")
			blobsBefore, _ := os.ReadDir(blobsDir)
			_, err := importBack(f.k, runtime, f.chains(), node, map[string]string{probeSecret: tc.value})
			var leak *SecretInRecord
			if !errors.As(err, &leak) || leak.Name != probeSecret {
				t.Fatalf("importBack = %v", err)
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Fatal("the refusal holds the value")
			}
			if itemsAfter, _ := f.k.Ledger.Items(); len(itemsAfter) != len(itemsBefore) {
				t.Fatalf("items %v, were %v", itemsAfter, itemsBefore)
			}
			if blobsAfter, _ := os.ReadDir(blobsDir); len(blobsAfter) != len(blobsBefore) {
				t.Fatal("blobs were copied from a refused record")
			}
		})
	}
	t.Run("a value already held here", func(t *testing.T) {
		f := newTaskFixture(t, "echo checked")
		if _, err := f.k.Update(f.inputs[0], item.Fields{Title: ptr("held " + plain)}, "ada"); err != nil {
			t.Fatal(err)
		}
		const node = "nod_be1d"
		nodeDir := f.stage(t, node)
		if _, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0"}); err != nil {
			t.Fatal(err)
		}
		if _, err := importBack(f.k, filepath.Join(nodeDir, "runtime"), f.chains(), node, map[string]string{probeSecret: plain}); err != nil {
			t.Fatalf("importBack with the value only in events held here = %v", err)
		}
	})
}
