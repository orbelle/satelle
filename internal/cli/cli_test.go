package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
)

// isolate gives the test its own home, credentials dir and session, and a
// fresh git repo as the working directory.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SATELLE_HOME", filepath.Join(home, ".satelle"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("SATELLE_SERVER_ENDPOINT", "none")
	t.Setenv("SATELLE_TOKEN", "")
	t.Setenv("SATELLE_SESSION", "")
	t.Setenv("SATELLE_PROJECT_DIR", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "init", "-q")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	t.Chdir(dir)
	return dir
}

// run executes one satelle command line with stdin, returning its output.
func run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func must(t *testing.T, args ...string) string {
	t.Helper()
	out, err := run(t, "", args...)
	if err != nil {
		t.Fatalf("satelle %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func createID(t *testing.T, args ...string) string {
	t.Helper()
	var it item.Item
	if err := json.Unmarshal([]byte(must(t, append(args, "--json")...)), &it); err != nil {
		t.Fatal(err)
	}
	return it.ID
}

func TestInitCreateWalkToDone(t *testing.T) {
	isolate(t)
	must(t, "init")
	must(t, "validate")
	id := createID(t, "story", "create", "--title", "first", "--criteria", "it works", "--tag", "x")
	out := must(t, "story", "get", id)
	if !strings.Contains(out, "first") || !strings.Contains(out, "backlog") {
		t.Fatalf("get:\n%s", out)
	}
	must(t, "story", "set", id, "--status", "done")
	var it item.Item
	if err := json.Unmarshal([]byte(must(t, "story", "get", id, "--json")), &it); err != nil {
		t.Fatal(err)
	}
	if it.Status != "done" {
		t.Fatalf("status %q, want done", it.Status)
	}
	if route := must(t, "story", "route", id); !strings.Contains(route, "▶ done") {
		t.Errorf("route does not mark the position:\n%s", route)
	}
	if out := must(t, "ledger", "verify"); !strings.Contains(out, "1 chains intact") {
		t.Errorf("verify: %s", out)
	}
	must(t, "ledger", "rebuild")
	if out := must(t, "story", "list", "--status", "done"); !strings.Contains(out, id) {
		t.Errorf("list after rebuild:\n%s", out)
	}
	if _, err := run(t, "", "story", "set", id, "--status", "backlog"); err == nil {
		t.Error("a move with no transition was accepted")
	}
}

// writeGatedWorkflow adds a workflow whose second step is performed by the
// session and whose exit is judged by a check that wants ok.txt.
func writeGatedWorkflow(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		".satelle/agents.toml": "[agents.agent-x9]\ncommand = \"session\"\n",
		".satelle/workflows/gated.toml": `name = "gated"
[states.st-one]
start = true
[states.st-two]
performer = { agent = "agent-x9", skill = "work" }
[states.st-three]
terminal = true
[gates.g1]
skill = "has-ok"
[[transition]]
from = ["st-one"]
to = "st-two"
[[transition]]
from = ["st-two"]
to = "st-three"
groups = [{ gates = ["g1"] }]
`,
		".satelle/skills/work.md":   "---\nname: work\n---\nDo it.\n",
		".satelle/skills/has-ok.md": "---\ntype: check\n---\n```sh\ntest -f ok.txt || { echo ok.txt is missing; exit 1; }\n```\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, ".satelle", "satelle.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("\n[gate]\nedit_exempt_paths = [\"notes/**\"]\nlocked_paths = [\".satelle/**\"]\n"); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedTransitionExitsBlockedWithNotes(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	must(t, "validate")
	id := createID(t, "story", "create", "--title", "gated", "--workflow", "gated")
	must(t, "story", "set", id, "--status", "st-two")
	_, err := run(t, "", "story", "set", id, "--status", "st-three")
	if ExitCodeOf(err) != ExitBlocked || !strings.Contains(err.Error(), "g1") || !strings.Contains(err.Error(), "ok.txt is missing") {
		t.Fatalf("rejected move: code %d, err %v", ExitCodeOf(err), err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "set", id, "--status", "st-three")
	route := must(t, "story", "route", id)
	if !strings.Contains(route, "g1") || !strings.Contains(route, "accept") {
		t.Errorf("route lacks the gate verdict:\n%s", route)
	}
}

// hookGate runs the gate hook for an edit of path (relative to dir) by session.
func hookGate(t *testing.T, dir, session, tool, path, command string) (deny bool, reason string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "session_id": session, "cwd": dir, "tool_name": tool,
		"tool_input": map[string]string{"file_path": filepath.Join(dir, path), "command": command},
	})
	out, err := run(t, string(payload), "hook", "gate", "--harness", "claude")
	if err != nil {
		t.Fatalf("hook gate: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) == "" {
		return false, ""
	}
	var doc struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("hook output %q: %v", out, err)
	}
	return doc.HookSpecificOutput.PermissionDecision == "deny", doc.HookSpecificOutput.PermissionDecisionReason
}

func TestEditGateFollowsSeatAndPerformingState(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "gated", "--workflow", "gated")

	if deny, _ := hookGate(t, dir, "s-1", "Edit", "main.go", ""); !deny {
		t.Fatal("an unseated edit was allowed")
	}
	if deny, _ := hookGate(t, dir, "s-1", "Edit", "notes/a.md", ""); deny {
		t.Error("an exempt path was denied")
	}
	must(t, "story", "seat", id, "--session", "s-1")
	if deny, _ := hookGate(t, dir, "s-1", "Edit", "main.go", ""); !deny {
		t.Fatal("an edit was allowed while the item's state has no performer")
	}
	must(t, "story", "set", id, "--status", "st-two")
	if deny, why := hookGate(t, dir, "s-1", "Edit", "main.go", ""); deny {
		t.Fatalf("a seated edit in a performing state was denied: %s", why)
	}
	if deny, _ := hookGate(t, dir, "s-2", "Edit", "main.go", ""); !deny {
		t.Error("another session's edit was allowed")
	}
	if deny, _ := hookGate(t, dir, "s-1", "Bash", "", "echo hi > out.txt"); deny {
		t.Error("a seated shell write was denied")
	}
	if deny, _ := hookGate(t, dir, "s-2", "Bash", "", "echo hi > out.txt"); !deny {
		t.Error("an unseated shell write was allowed")
	}
	if deny, _ := hookGate(t, dir, "s-1", "Edit", ".satelle/satelle.toml", ""); !deny {
		t.Error("a locked path was allowed")
	}
	deny, why := hookGate(t, dir, "s-2", "Edit", "main.go", "")
	if !deny || strings.Contains(why, "st-two") || strings.Contains(why, "agent-x9") || strings.Contains(why, "gated") {
		t.Errorf("deny reason names the workflow's own names: %q", why)
	}
	t.Setenv("SATELLE_SESSION", "s-1")
	if out := must(t, "status"); !strings.Contains(out, id) || !strings.Contains(out, "performing") {
		t.Errorf("status does not show the seat:\n%s", out)
	}
	must(t, "story", "release", id)
	if deny, _ := hookGate(t, dir, "s-1", "Edit", "main.go", ""); !deny {
		t.Error("an edit was allowed after release")
	}
}

func TestHookOutsideARepoSaysNothing(t *testing.T) {
	isolate(t)
	out, err := run(t, `{"tool_name":"Edit","tool_input":{"file_path":"/x/y"}}`, "hook", "gate")
	if err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestAttachmentsAndContext(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "docs")
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "attach", id, "--name", "plan", "--file", "plan.md")
	if out := must(t, "story", "attachments", id); !strings.Contains(out, "plan") {
		t.Errorf("attachments:\n%s", out)
	}
	if out := must(t, "story", "attachment", id, "plan"); out != "# plan\n" {
		t.Errorf("attachment body %q", out)
	}
	out, err := run(t, `{"session_id":"abc"}`, "hook", "context")
	if err != nil || !strings.Contains(out, "abc") {
		t.Errorf("context: %v %s", err, out)
	}
}

func TestTaskRunAndExecutions(t *testing.T) {
	isolate(t)
	must(t, "init")
	createID(t, "story", "create", "--title", "a", "--tag", "pick")
	createID(t, "story", "create", "--title", "b")
	task := createID(t, "task", "create", "--title", "review", "--input-kind", "story", "--input-tag", "pick")
	out := must(t, "task", "run", task)
	if !strings.Contains(out, "over 1 items") {
		t.Fatalf("task run:\n%s", out)
	}
	if out := must(t, "execution", "list", "--parent", task); strings.TrimSpace(out) == "" {
		t.Error("no execution listed")
	}
}

func TestInvalidConfigurationRefusesToRun(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	if err := os.WriteFile(filepath.Join(dir, ".satelle", "satelle.toml"), []byte("[mystery]\nx = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "", "story", "list"); err == nil {
		t.Fatal("an invalid configuration ran")
	}
	_, err := run(t, "", "validate")
	if err == nil || !strings.Contains(err.Error(), "mystery") {
		t.Fatalf("validate: %v", err)
	}
	if !errors.Is(err, err) || ExitCodeOf(err) != ExitError {
		t.Errorf("exit code %d", ExitCodeOf(err))
	}
	if deny, _ := hookGate(t, dir, "s", "Edit", ".satelle/satelle.toml", ""); deny {
		t.Error("the configuration's own repair was denied")
	}
	if deny, _ := hookGate(t, dir, "s", "Edit", "main.go", ""); !deny {
		t.Error("an edit was allowed under an invalid configuration")
	}
}

// getStory loads a story through the CLI.
func getStory(t *testing.T, id string) item.Item {
	t.Helper()
	var it item.Item
	if err := json.Unmarshal([]byte(must(t, "story", "get", id, "--json")), &it); err != nil {
		t.Fatal(err)
	}
	return it
}

func TestSetCriteriaAddRemove(t *testing.T) {
	isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "c", "--criteria", "a", "--criteria", "b")
	want := func(w ...string) {
		t.Helper()
		if got := getStory(t, id).Criteria; strings.Join(got, "|") != strings.Join(w, "|") {
			t.Fatalf("criteria %q, want %q", got, w)
		}
	}
	must(t, "story", "set", id, "--add-criteria", "c")
	want("a", "b", "c")
	must(t, "story", "set", id, "--remove-criteria", "b")
	want("a", "c")
	must(t, "story", "set", id, "--add-criteria", "a")
	want("a", "c")
	out, err := run(t, "", "story", "set", id, "--remove-criteria", "zz")
	if err == nil || !strings.Contains(err.Error(), "zz") {
		t.Fatalf("removing a missing criterion: %v\n%s", err, out)
	}
	want("a", "c")
}

func TestSetReplaceGuard(t *testing.T) {
	isolate(t)
	must(t, "init")
	id := createID(t, "story", "create", "--title", "g",
		"--criteria", "a", "--criteria", "b", "--tag", "x", "--tag", "y")
	crit := func(w ...string) {
		t.Helper()
		if got := getStory(t, id).Criteria; strings.Join(got, "|") != strings.Join(w, "|") {
			t.Fatalf("criteria %q, want %q", got, w)
		}
	}
	tags := func(w ...string) {
		t.Helper()
		if got := getStory(t, id).Tags; strings.Join(got, "|") != strings.Join(w, "|") {
			t.Fatalf("tags %q, want %q", got, w)
		}
	}
	_, err := run(t, "", "story", "set", id, "--criteria", "a")
	if err == nil || !strings.Contains(err.Error(), "  - b") || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("dropping criterion b: %v", err)
	}
	crit("a", "b")
	_, err = run(t, "", "story", "set", id, "--tag", "x")
	if err == nil || !strings.Contains(err.Error(), "  - y") {
		t.Fatalf("dropping tag y: %v", err)
	}
	tags("x", "y")
	must(t, "story", "set", id, "--criteria", "a", "--criteria", "b", "--criteria", "c")
	crit("a", "b", "c")
	must(t, "story", "set", id, "--criteria", "a", "--remove-criteria", "b", "--remove-criteria", "c")
	crit("a")
	must(t, "story", "set", id, "--criteria", "z", "--replace")
	crit("z")
	must(t, "story", "set", id, "--tag", "q", "--replace")
	tags("q")
	before := getStory(t, id).Status
	if _, err := run(t, "", "story", "set", id, "--criteria", "n", "--status", "done"); err == nil {
		t.Fatal("a dropping --criteria with --status was accepted")
	}
	if it := getStory(t, id); it.Status != before || strings.Join(it.Criteria, "|") != "z" {
		t.Fatalf("refused set changed the story: status %q criteria %q", it.Status, it.Criteria)
	}
}
