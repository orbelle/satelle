package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/hook"
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
	clearCallerMarkers(t)
	// No test's update reaches this machine's user manager; a test of the
	// service clears it and puts a fake systemctl first on PATH.
	t.Setenv(envNoService, "1")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "init", "-q")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	t.Chdir(dir)
	return dir
}

// clearCallerMarkers unsets every harness session marker (hook.CallerMarkers),
// so a test run from an agent's shell sees no calling harness unless it sets
// one.
func clearCallerMarkers(t *testing.T) {
	t.Helper()
	names, prefixes := hook.CallerMarkers()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				names = append(names, name)
			}
		}
	}
	for _, n := range names {
		t.Setenv(n, "")
	}
}

// run executes one satelle command line with stdin, returning its output. A
// real call is its own process, so a --session (which sets SATELLE_SESSION for
// the call) and a -C (which changes directory) end with it: run puts both back.
func run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	prev, had := os.LookupEnv(config.SessionEnv)
	wd, wdErr := os.Getwd()
	defer func() {
		if had {
			os.Setenv(config.SessionEnv, prev)
		} else {
			os.Unsetenv(config.SessionEnv)
		}
		if wdErr == nil {
			_ = os.Chdir(wd)
		}
	}()
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
timeout = "30m"
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

// workflow show lists a gate's own timeout beside its other keys.
func TestWorkflowShowGateTimeout(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	must(t, "validate")
	out := must(t, "workflow", "show", "gated")
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "skill has-ok") {
			line = l
		}
	}
	if !strings.HasSuffix(line, "timeout 30m") {
		t.Fatalf("gate line %q in:\n%s", line, out)
	}
}

// appendRule appends a [[workflows.rule]] selecting workflow for tag.
func appendRule(t *testing.T, dir, workflow, tag string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ".satelle", "satelle.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n[[workflows.rule]]\nworkflow = %q\ntag = %q\n", workflow, tag); err != nil {
		t.Fatal(err)
	}
}

// workflow select prints the rules' choice and its start state for the given
// fields: the rule's workflow when they match, the default otherwise.
func TestWorkflowSelect(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	appendRule(t, dir, "gated", "via-g")
	must(t, "validate")
	if out := must(t, "workflow", "select", "--kind", "story", "--tag", "x", "--tag", "via-g"); out != "gated st-one\n" {
		t.Errorf("select with the rule's tag: %q", out)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(must(t, "workflow", "select", "--kind", "story", "--tag", "via-g", "--json")), &got); err != nil {
		t.Fatal(err)
	}
	if got["workflow"] != "gated" || got["start"] != "st-one" {
		t.Errorf("select --json: %v", got)
	}
	if out := must(t, "workflow", "select", "--kind", "story"); out != "default backlog\n" {
		t.Errorf("select without the rule's tag: %q", out)
	}
	if _, err := run(t, "", "workflow", "select", "--tag", "via-g"); err == nil || ExitCodeOf(err) != ExitError {
		t.Errorf("select without --kind: %v", err)
	}
}

// --reselect takes no other field flag and no --status: either is refused with
// exit 1 before anything is written. With neither, an item the rules now place
// elsewhere moves there in place.
func TestSetReselectConflicts(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	appendRule(t, dir, "gated", "via-g")
	must(t, "validate")
	id := createID(t, "story", "create", "--title", "r")
	must(t, "story", "set", id, "--add-tag", "via-g")
	events := func() string { return must(t, "ledger", "list", id) }
	before := events()
	for _, extra := range [][]string{{"--title", "other"}, {"--status", "done"}, {"--add-tag", "z"}} {
		args := append([]string{"story", "set", id, "--reselect", "gated"}, extra...)
		_, err := run(t, "", args...)
		if err == nil || ExitCodeOf(err) != ExitError || !strings.Contains(err.Error(), extra[0]) {
			t.Errorf("%v: %v", args, err)
		}
		if after := events(); after != before {
			t.Fatalf("%v wrote to the ledger:\n%s", args, after)
		}
	}
	out := must(t, "story", "set", id, "--reselect", "gated")
	if want := id + " moved to workflow gated (backlog -> st-one)\n"; out != want {
		t.Errorf("reselect printed %q, want %q", out, want)
	}
	if it := getStory(t, id); it.Workflow != "gated" || it.Status != "st-one" {
		t.Errorf("reselected story at %s/%s", it.Workflow, it.Status)
	}
}

// writePlanWorkflow adds a workflow st-one -> st-plan -> st-build -> st-done
// whose st-plan performer produces only attachments (edits = "attachments")
// and whose st-build performer may change the tree, with notes/** exempt and
// locked/** locked.
func writePlanWorkflow(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		".satelle/agents.toml": "[agents.agent-x9]\ncommand = \"session\"\n",
		".satelle/workflows/planned.toml": `name = "planned"
[states.st-one]
start = true
[states.st-plan]
performer = { agent = "agent-x9", skill = "work" }
edits = "attachments"
[states.st-build]
performer = { agent = "agent-x9", skill = "work" }
[states.st-done]
terminal = true
[[transition]]
from = ["st-one"]
to = "st-plan"
[[transition]]
from = ["st-plan"]
to = "st-build"
[[transition]]
from = ["st-build"]
to = "st-done"
`,
		".satelle/skills/work.md": "---\nname: work\n---\nDo it.\n",
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
	if _, err := f.WriteString("\n[gate]\nedit_exempt_paths = [\"notes/**\"]\nlocked_paths = [\"locked/**\"]\n"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEditsKey(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writePlanWorkflow(t, dir)
	must(t, "validate")
	p := filepath.Join(dir, ".satelle", "workflows", "planned.toml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), `edits = "attachments"`, `edits = "nothing"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "", "validate"); err == nil || !strings.Contains(err.Error(), `state "st-plan" edits "nothing"`) {
		t.Fatalf("validate with edits = nothing: %v", err)
	}
}

// hookContext runs the context hook for session.
func hookContext(t *testing.T, session string) string {
	t.Helper()
	out, err := run(t, `{"session_id":"`+session+`"}`, "hook", "context", "--harness", "claude")
	if err != nil {
		t.Fatalf("hook context: %v\n%s", err, out)
	}
	return hookText(out)
}

func TestEditGateAttachmentsOnlyState(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writePlanWorkflow(t, dir)
	a := createID(t, "story", "create", "--title", "planned", "--workflow", "planned")
	b := createID(t, "story", "create", "--title", "built", "--workflow", "planned")
	must(t, "story", "seat", a, "--session", "s-1")
	if out := must(t, "status", "--session", "s-1"); !strings.Contains(out, "(st-one: no edits)") {
		t.Errorf("status in a state no agent performs:\n%s", out)
	}
	must(t, "story", "set", a, "--status", "st-plan")

	attach := []string{"st-plan", a, "produces an attachment", "`satelle <kind> attach <id> --name <name> --file <file> --session <session>`"}
	refused := func(what string, deny bool, why string) {
		t.Helper()
		if !deny {
			t.Errorf("%s: allowed from a seat on an attachments-only state", what)
			return
		}
		for _, w := range attach {
			if !strings.Contains(why, w) {
				t.Errorf("%s: reason does not contain %q: %s", what, w, why)
			}
		}
	}
	deny, why := hookGate(t, dir, "s-1", "Edit", "main.go", "")
	refused("Edit main.go", deny, why)
	deny, why = hookGate(t, dir, "s-1", "Write", "main.go", "")
	refused("Write main.go", deny, why)
	deny, why = hookGate(t, dir, "s-1", "Edit", "notes/a.md", "")
	refused("Edit exempt notes/a.md", deny, why)
	deny, why = hookGate(t, dir, "s-1", "Edit", "locked/x", "")
	refused("Edit locked locked/x", deny, why)
	if strings.Contains(why, "is a locked path") {
		t.Errorf("locked path refused with the locked reason, not the seat's: %s", why)
	}
	deny, why = hookGate(t, dir, "s-1", "Bash", "", "echo hi > out.txt")
	refused("shell write", deny, why)
	if deny, why := hookGate(t, dir, "s-2", "Edit", "notes/a.md", ""); deny {
		t.Errorf("an unseated session was denied an exempt path: %s", why)
	}
	if deny, why := hookGate(t, dir, "s-1", "Bash", "", "satelle story attach "+a+" --name plan --file - --session s-1"); deny {
		t.Errorf("attach was denied: %s", why)
	}
	draft := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(draft, []byte("# the plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "attach", a, "--name", "plan", "--file", draft)
	if body := must(t, "story", "attachment", a, "plan"); body != "# the plan\n" {
		t.Errorf("attachment body %q", body)
	}

	for _, out := range map[string]string{"status": must(t, "status", "--session", "s-1"), "context": hookContext(t, "s-1")} {
		if !strings.Contains(out, "performing st-plan: attachments only") {
			t.Errorf("seat line does not name the attachments-only state:\n%s", out)
		}
	}
	ctx := hookContext(t, "s-1")
	for _, w := range []string{"`satelle help edit-gate --session <session>`", "`satelle <kind> attach <id> --name <name> --file <file> --session <session>`"} {
		if !strings.Contains(ctx, w) {
			t.Errorf("context does not contain %s:\n%s", w, ctx)
		}
	}
	var st struct {
		Seats []seatView `json:"seats"`
	}
	if err := json.Unmarshal([]byte(must(t, "status", "--json", "--session", "s-1")), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Seats) != 1 || st.Seats[0].Edits != "attachments" || !st.Seats[0].Performing {
		t.Errorf("status --json seats %+v, want one with edits attachments", st.Seats)
	}

	// A second seat at the same worktree on a tree state does not lift it.
	must(t, "story", "set", b, "--status", "st-plan")
	must(t, "story", "set", b, "--status", "st-build")
	must(t, "story", "seat", b, "--session", "s-1")
	deny, why = hookGate(t, dir, "s-1", "Edit", "main.go", "")
	refused("Edit with a second, tree seat", deny, why)
	must(t, "story", "release", a, "--session", "s-1")
	if deny, why := hookGate(t, dir, "s-1", "Edit", "main.go", ""); deny {
		t.Errorf("released the plan seat; the tree seat's edit was denied: %s", why)
	}
	must(t, "story", "release", b, "--session", "s-1")

	must(t, "story", "seat", a, "--session", "s-1")
	must(t, "story", "set", a, "--status", "st-build")
	if deny, why := hookGate(t, dir, "s-1", "Edit", "main.go", ""); deny {
		t.Errorf("an edit in the tree state was denied: %s", why)
	}
	if deny, why := hookGate(t, dir, "s-1", "Bash", "", "echo hi > out.txt"); deny {
		t.Errorf("a shell write in the tree state was denied: %s", why)
	}
	for _, out := range map[string]string{"status": must(t, "status", "--session", "s-1"), "context": hookContext(t, "s-1")} {
		if !strings.Contains(out, "performing st-build: edits allowed") {
			t.Errorf("seat line does not name the tree state:\n%s", out)
		}
	}
	if ctx := hookContext(t, "s-1"); !strings.Contains(ctx, "`satelle help edit-gate --session <session>`") {
		t.Errorf("context lost the edit-gate help:\n%s", ctx)
	}
	if err := json.Unmarshal([]byte(must(t, "status", "--json", "--session", "s-1")), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Seats) != 1 || st.Seats[0].Edits != "tree" {
		t.Errorf("status --json seats %+v, want one with edits tree", st.Seats)
	}
}

func TestEditGateAttachmentsOnlyInvalidConfig(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writePlanWorkflow(t, dir)
	a := createID(t, "story", "create", "--title", "planned", "--workflow", "planned")
	b := createID(t, "story", "create", "--title", "built", "--workflow", "planned")
	must(t, "story", "set", a, "--status", "st-plan")
	must(t, "story", "set", b, "--status", "st-plan")
	must(t, "story", "set", b, "--status", "st-build")
	must(t, "story", "seat", a, "--session", "s-1")
	must(t, "story", "seat", b, "--session", "s-3")

	settings := filepath.Join(dir, ".satelle", "satelle.toml")
	flow := filepath.Join(dir, ".satelle", "workflows", "planned.toml")
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	write := func(p, body string) {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	goodSettings, goodFlow := read(settings), read(flow)
	check := func(name string) {
		t.Helper()
		if _, err := run(t, "", "validate"); err == nil {
			t.Fatalf("%s: the configuration is valid", name)
		}
		for _, p := range []string{".satelle/satelle.toml", "main.go"} {
			deny, why := hookGate(t, dir, "s-1", "Edit", p, "")
			if !deny || !strings.Contains(why, "st-plan") || !strings.Contains(why, "produces an attachment") || !strings.Contains(why, "configuration is also invalid") {
				t.Errorf("%s: s-1 (plan seat) edit of %s: deny %v, %s", name, p, deny, why)
			}
		}
		for _, s := range []string{"s-3", "s-2"} {
			if deny, why := hookGate(t, dir, s, "Edit", ".satelle/satelle.toml", ""); deny {
				t.Errorf("%s: %s was denied the configuration's repair: %s", name, s, why)
			}
			deny, why := hookGate(t, dir, s, "Edit", "main.go", "")
			if !deny || !strings.Contains(why, "configuration is invalid, so only its own files may be edited") {
				t.Errorf("%s: %s edit of main.go: deny %v, %s", name, s, deny, why)
			}
		}
	}
	write(settings, goodSettings+"\n[mystery]\nx = 1\n")
	check("unknown settings key")
	write(settings, goodSettings)
	write(flow, goodFlow+"\nmystery = 1\n")
	check("unknown workflow key")

	write(flow, goodFlow+"\n[states.broken\n")
	if _, err := run(t, "", "validate"); err == nil {
		t.Fatal("syntax error: the configuration is valid")
	}
	for _, s := range []struct{ session, state string }{{"s-1", "st-plan"}, {"s-3", "st-build"}} {
		deny, why := hookGate(t, dir, s.session, "Edit", ".satelle/workflows/planned.toml", "")
		if !deny || !strings.Contains(why, s.state) || !strings.Contains(why, "cannot be read") {
			t.Errorf("syntax error: %s edit of the workflow file: deny %v, %s", s.session, deny, why)
		}
	}
	if deny, why := hookGate(t, dir, "s-2", "Edit", ".satelle/workflows/planned.toml", ""); deny {
		t.Errorf("syntax error: the unseated session was denied the repair: %s", why)
	}
}
