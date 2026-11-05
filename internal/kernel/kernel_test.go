package kernel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/item"
)

func ptr[T any](v T) *T { return &v }

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInitTwoStateEndToEnd(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if err := Init(root); err != nil { // idempotent, keeps files
		t.Fatal(err)
	}
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	it, err := k.Create(item.Fields{Kind: ptr("note"), Title: ptr("first"), Criteria: ptr([]string{"works"})}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if it.Status != "backlog" || it.Workflow != "default" || !strings.HasPrefix(it.ID, "not_") {
		t.Fatalf("created %+v", it)
	}
	res, err := k.Transition(context.Background(), it.ID, "done", "tester")
	if err != nil || !res.Moved || res.Item.Status != "done" || len(res.Gates) != 0 {
		t.Fatalf("transition %+v %v", res, err)
	}
	if _, err := k.Transition(context.Background(), it.ID, "backlog", "tester"); err == nil {
		t.Fatal("moved out of a terminal state")
	}
	if got, _ := k.List(cache.Query{Status: "done"}); len(got) != 1 || got[0].ID != it.ID {
		t.Fatalf("list %+v", got)
	}
	if err := k.Ledger.Verify(); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Create(item.Fields{Kind: ptr("note"), Title: ptr("x"), Status: ptr("done")}, ""); err == nil {
		t.Fatal("status accepted at create")
	}
	if _, err := k.Update(it.ID, item.Fields{Status: ptr("backlog")}, ""); err == nil {
		t.Fatal("status accepted at update")
	}
	up, err := k.Update(it.ID, item.Fields{Title: ptr("renamed"), Tags: ptr([]string{"t1"})}, "")
	if err != nil || up.Title != "renamed" || up.Status != "done" {
		t.Fatalf("update %+v %v", up, err)
	}
}

func TestConfigRefusals(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"default\"\nmystery = 1\n")
	if _, err := Open(Options{Root: root}); err == nil || !strings.Contains(err.Error(), "workflows.mystery") {
		t.Fatalf("unknown key: %v", err)
	}
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"default\"\n[rules]\nreviewer_differs_from_performer = true\n")
	write(t, filepath.Join(root, "agents.toml"), `
[agents.writer]
command = "claude"
model = "m1"
tools = ["Read", "Edit"]
[agents.same]
command = "claude"
model = "m1"
tools = ["Read"]
[agents.unenforced]
command = "session"
[agents.pishell]
command = "pi"
tools = ["Bash"]
`)
	write(t, filepath.Join(root, "skills", "s.md"), "judge\n")
	write(t, filepath.Join(root, "workflows", "x.toml"), `
[states.a]
start = true
performer = { agent = "same", skill = "s" }
[states.b]
performer = { agent = "pishell", skill = "s" }
[states.z]
terminal = true
[gates.w]
skill = "s"
agent = "writer"
[gates.u]
skill = "s"
agent = "unenforced"
[gates.m]
skill = "s"
agent = "same"
[[transition]]
from = ["a"]
to = "b"
groups = [{ gates = ["w", "u", "m"] }]
[[transition]]
from = ["b"]
to = "z"
`)
	problems := Validate(root)
	joined := ""
	for _, p := range problems {
		joined += p.Error() + "\n"
	}
	for _, want := range []string{
		`agent "writer" may not judge`,
		`agent "unenforced" may not judge`,
		`judge "same" runs model "m1"`,
		`performer "pishell": pi adapter cannot express the scoped deny`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

// init writes the ignore for run-time record folders.
func TestInitWritesTheRunsIgnore(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil || !strings.Contains(string(b), "runs/") {
		t.Fatalf(".satelle/.gitignore = %q, %v", b, err)
	}
}
