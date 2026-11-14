package kernel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/config"
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
[agents.cursorjudge]
command = "cursor-agent"
model = "cj"
tools = ["Read", "Grep", "Glob"]
[agents.cursorwriter]
command = "cursor-agent"
model = "cw"
tools = ["Read", "Grep", "Glob", "Edit", "Write"]
[agents.cursorshell]
command = "cursor-agent"
tools = ["Read", "Grep", "Glob", "Edit", "Write", "Bash"]
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
[gates.cj]
skill = "s"
agent = "cursorjudge"
[gates.cw]
skill = "s"
agent = "cursorwriter"
[[transition]]
from = ["a"]
to = "b"
groups = [{ gates = ["w", "u", "m", "cj", "cw"] }]
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
		`agent "cursorwriter" may not judge`,
		// A Bash-granted cursor-agent binding wired to no state is refused
		// at validate (sto_0f41dda9 AC3).
		`agents.cursorshell: cursor-agent cannot hold a shell`,
		`grant no Bash`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	// A read-only cursor-agent judge validates (sto_0f41dda9 AC2).
	if strings.Contains(joined, "cursorjudge") {
		t.Errorf("a read-only cursor-agent judge was refused:\n%s", joined)
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

// TestInitShipsTheUpgradeSkill: init writes the upgrade skill, it loads as a
// plain (judged, not check) skill, the configuration still validates, and its
// body names every old key its Map covers and the rules that keep the port
// valid (sto_5725d112).
func TestInitShipsTheUpgradeSkill(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	// UpgradeSkill is the name every needs-upgrade line prints (sto_0f76dd4e).
	if _, err := os.Stat(filepath.Join(root, "skills", UpgradeSkill+".md")); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	sk, ok := c.Skills[UpgradeSkill]
	if !ok || sk.Meta["name"] != UpgradeSkill || sk.IsCheck() {
		t.Fatalf("skill = %+v, %v", sk.Meta, ok)
	}
	if probs := Validate(root); len(probs) != 0 {
		t.Fatalf("validate: %v", probs)
	}
	for _, tok := range []string{
		// AC2: every old key the Map covers.
		"[[gate]]", "on", "for", "when", "applies_to", "mandatory", "parallel",
		"reviewer_agent", "propose = true", "after_children", "waits_on_children", "advise",
		"interface", "park", "cancel", "recover",
		// AC2: the judge rule and the resume rule.
		"session", "claude", "pi", "grok", "read-only",
		"@prior", "resumes = true", "satelle workflow show --session <session>",
		// AC6: retype and command renames.
		"type: check", "story attachment <id> <name>", "story attachments <id>",
		"ledger list <id>", "--file -",
		// AC8: the category check.
		"SATELLE_HOME", "--json", "<kind>/<status>",
	} {
		if !strings.Contains(sk.Body, "`"+tok+"`") {
			t.Errorf("the upgrade skill does not name `%s`", tok)
		}
	}
}

// WorkflowFor is the one selection rule: the item's own workflow, else the
// first matching rule, else the default, with that workflow's start state.
func TestWorkflowFor(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "workflows", "bugflow.toml"), []byte("[states.triage]\nstart = true\n[states.done]\nterminal = true\n[[transition]]\nfrom = [\"triage\"]\nto = \"done\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	toml, _ := os.ReadFile(filepath.Join(root, "satelle.toml"))
	if err := os.WriteFile(filepath.Join(root, "satelle.toml"), append(toml, []byte("\n[[workflows.rule]]\nworkflow = \"bugflow\"\ncategory = \"bug\"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	for _, c := range []struct {
		it          item.Item
		name, start string
	}{
		{item.Item{Kind: "story", Category: "bug"}, "bugflow", "triage"},
		{item.Item{Kind: "story"}, "default", "backlog"},
		{item.Item{Kind: "story", Category: "bug", Workflow: "default"}, "default", "backlog"},
	} {
		name, start, err := k.WorkflowFor(c.it)
		if err != nil || name != c.name || start != c.start {
			t.Errorf("WorkflowFor(%+v) = %s %s %v, want %s %s", c.it, name, start, err, c.name, c.start)
		}
	}
	if _, _, err := k.WorkflowFor(item.Item{Kind: "story", Workflow: "nope"}); err == nil {
		t.Error("an unknown workflow was accepted")
	}
}

// Open's logs dir is config.LogsDir of the runtime it opened, given or
// defaulted.
func TestOpenLogsIsConfigLogsDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	for _, rt := range []string{filepath.Join(t.TempDir(), "rt"), ""} {
		k, err := Open(Options{Root: root, Runtime: rt})
		if err != nil {
			t.Fatal(err)
		}
		want := rt
		if want == "" {
			want = filepath.Join(root, "runtime")
		}
		if got := k.Logs; got != config.LogsDir(want) {
			t.Errorf("runtime %q: Logs = %q, want %q", rt, got, config.LogsDir(want))
		}
		k.Close()
	}
}
