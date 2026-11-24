//go:build !windows

package kernel

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
)

func TestGateCheckNamesTheWorktreeSubstrate(t *testing.T) {
	t.Setenv("SATELLE_SUBSTRATE_DIR", "/not/the/run")
	dir := t.TempDir()
	root := filepath.Join(dir, ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"flow\"\n")
	write(t, filepath.Join(root, "skills", "where.md"), "---\ntype: check\n---\n```sh\nprintf '%s\\n' \"$SATELLE_SUBSTRATE_DIR\"\npwd\ntest -f .satelle/satelle.toml && echo relative-ok\n```\n")
	write(t, filepath.Join(root, "workflows", "flow.toml"), `
[states.open]
start = true
[states.done]
terminal = true
[gates.where]
skill = "where"
[[transition]]
from = ["open"]
to = "done"
groups = [{ gates = ["where"] }]
`)
	k, err := Open(Options{Root: root, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	k.Git = fakeGit{}
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("t")}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Transition(context.Background(), it.ID, "done", "ada"); err != nil {
		t.Fatal(err)
	}
	events, err := k.Ledger.Read(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	var run gate.Result
	for _, r := range GateRuns(events) {
		if r.Gate == "where" {
			run = r
		}
	}
	if run.Result != gate.Accept {
		t.Fatalf("gate where = %+v, want accept", run)
	}
	lines := strings.Split(strings.TrimSpace(run.Notes), "\n")
	if len(lines) != 3 {
		t.Fatalf("notes %q, want the substrate, the working directory and relative-ok", run.Notes)
	}
	if got, want := evalPath(t, lines[0]), evalPath(t, root); got != want {
		t.Errorf("SATELLE_SUBSTRATE_DIR = %q, want %q", got, want)
	}
	if got, want := evalPath(t, lines[1]), evalPath(t, dir); got != want {
		t.Errorf("working directory = %q, want the worktree %q", got, want)
	}
	if lines[2] != "relative-ok" {
		t.Errorf("a relative .satelle read printed %q, want relative-ok", lines[2])
	}
}

func evalPath(t *testing.T, p string) string {
	t.Helper()
	e, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("resolve %q: %v", p, err)
	}
	return e
}
