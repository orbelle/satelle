package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/help"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

// driveFixture is a repo whose route is
//
//	backlog → plan (planner, propose, gated) → in_progress (coder) → integration (executor) → done
//
// with every performer a shell script that logs what it was handed and every
// gate a functional check whose verdict a counter file scripts. A drive over it
// exercises the real engine, the real verbs and the real ledger; only the
// agents behind the bindings are stubs.
type driveFixture struct {
	t    *testing.T
	repo string
	dir  string // scripts, logs and the gate counter
}

type driveOpts struct {
	// gateRejects is how many presentations of the plan edge the gate rejects
	// before it accepts (999 = always).
	gateRejects int
	// rejectBudget is the plan step's reject_budget (0 = undeclared).
	rejectBudget int
	// executorCommand / coderCommand override the headless bindings ("" = a
	// logging stub script; "in-loop" = the driving session performs).
	executorCommand string
	coderCommand    string
	// noPark drops the route's park state.
	noPark bool
	// wait puts a wait step (awaiting_release) between integration and done, and
	// makes the repo a git repository with no remote, the tree it queues from.
	wait bool
}

const driveIntegrateSkill = `---
name: drv-integrate
type: skill
description: the integration step's executor rubric for the drive tests
---

INTEGRATE-RUBRIC-MARKER: integrate the slice.
`

func (f *driveFixture) stub(name string) string {
	f.t.Helper()
	path := filepath.Join(f.dir, name)
	log := filepath.Join(f.dir, name+".log")
	script := "#!/bin/sh\n{ echo \"=== ARGV: $*\"; cat; echo; } >> " + log + "\necho performed\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return path
}

// log is everything the named stub was handed, one "=== ARGV" block per run.
func (f *driveFixture) log(name string) []string {
	b, err := os.ReadFile(filepath.Join(f.dir, name+".log"))
	if err != nil {
		return nil
	}
	parts := strings.Split(string(b), "=== ARGV:")
	return parts[1:]
}

func newDriveFixture(t *testing.T, o driveOpts) *driveFixture {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	f := &driveFixture{t: t, repo: repo, dir: t.TempDir()}
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	park := "park = { state = \"blocked\" }\n"
	if o.noPark {
		park = ""
	}
	budget := ""
	if o.rejectBudget > 0 {
		budget = fmt.Sprintf("reject_budget = %d\n", o.rejectBudget)
	}
	queued, closedAfter := "", "integrated"
	if o.wait {
		for _, args := range [][]string{
			{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
			{"commit", "--allow-empty", "-m", "init"},
		} {
			if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		queued, closedAfter = "\n[queued]\nstatus = \"awaiting_release\"\nwait = true\nrequires = [\"integrated\"]\n", "queued"
	}
	obligations := `"raised", "readied", "coded", "integrated", "closed"`
	if o.wait {
		obligations = `"raised", "readied", "coded", "integrated", "queued", "closed"`
	}
	writeRoute(t, wfDir,
		`["*"]
obligations = [`+obligations+`]
`+park+`cancel = { state = "cancelled" }
`,
		`[raised]
status = "backlog"
start = true

[readied]
status = "plan"
agent = "planner"
skills = ["drv-plan"]
propose = true
reviewers = ["drv-gate"]
`+budget+`requires = ["raised"]

[coded]
status = "in_progress"
agent = "coder"
requires = ["readied"]

[integrated]
status = "integration"
agent = "executor"
skills = ["drv-integrate"]
requires = ["coded"]
`+queued+`
[closed]
status = "done"
terminal = true
requires = ["`+closedAfter+`"]
`)
	skills := filepath.Join(repo, ".satelle", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(f.dir, "gate-count")
	gate := "---\nname: drv-gate\ntype: skill\ndescription: scripted gate for the drive tests\n---\n\n```check\n" +
		"n=$(cat " + counter + " 2>/dev/null || echo 0)\nn=$((n+1))\necho $n > " + counter + "\n" +
		fmt.Sprintf("if [ \"$n\" -le %d ]; then echo \"NOTE-FIX-THE-PLAN (presentation $n)\"; exit 1; fi\nexit 0\n```\n", o.gateRejects)
	plan := "---\nname: drv-plan\ntype: skill\ndescription: the plan step's rubric for the drive tests\n---\n\nPlan the slice.\n"
	for name, body := range map[string]string{"drv-gate.md": gate, "drv-integrate.md": driveIntegrateSkill, "drv-plan.md": plan} {
		if err := os.WriteFile(filepath.Join(skills, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tools := "tools = \"Read,Grep,Glob,Edit,Write,Bash(satelle:*)\"\n"
	exec := o.executorCommand
	if exec == "" {
		exec = f.stub("fake-executor") + " -p {system}"
	}
	coder := o.coderCommand
	if coder == "" {
		coder = f.stub("fake-coder") + " -p {system}"
	}
	agents := "[executor]\nrole = \"agent\"\ncommand = \"" + exec + "\"\n" + tools + "\n" +
		"[orchestrator]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n" +
		"[planner]\nrole = \"agent\"\ncommand = \"" + f.stub("fake-planner") + " -p {system}\"\n" + tools + "\n" +
		"[coder]\nrole = \"agent\"\ncommand = \"" + coder + "\"\n" + tools
	if err := os.WriteFile(filepath.Join(wfDir, config.AgentsConfigName), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(repo, ".satelle", config.AgentsConfigName))
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	return f
}

func (f *driveFixture) create() string {
	f.t.Helper()
	out, err := runRoot(f.t, "story", "create", "--title", "drive me", "--body", "walk the route",
		"--acceptance", "1. the route is walked", "--category", "chore")
	if err != nil {
		f.t.Fatalf("create: %v\n%s", err, out)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		f.t.Fatalf("parse create: %v\n%s", err, out)
	}
	return created["id"].(string)
}

func (f *driveFixture) rows(id string) []ledger.Entry {
	f.t.Helper()
	db, err := store.Open(runtimeDBPath(f.t))
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	es, err := db.Ledger.ListByStory(context.Background(), id, "")
	if err != nil {
		f.t.Fatal(err)
	}
	return es
}

// edges is the story's status_transition rows as "from>to", in order.
func (f *driveFixture) edges(id string) []string {
	var out []string
	for _, e := range f.rows(id) {
		if e.Kind != ledger.KindStatusTransition {
			continue
		}
		var p struct{ From, To string }
		_ = json.Unmarshal(e.Payload, &p)
		out = append(out, p.From+">"+p.To)
	}
	return out
}

// AC2, case 1: an executor-allocated step while [executor] is in-loop is refused
// before any transition, naming the step and the agents.toml key.
func TestStoryDriveRefusesAnInLoopExecutor(t *testing.T) {
	f := newDriveFixture(t, driveOpts{executorCommand: "in-loop"})
	id := f.create()
	before := len(f.rows(id))
	out, err := runRoot(t, "story", "drive", id)
	if err == nil {
		t.Fatalf("an in-loop executor must be refused:\n%s", out)
	}
	for _, want := range []string{`step "integration"`, "[executor] command"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q, got: %v", want, err)
		}
	}
	if ExitCodeOf(err) != 1 {
		t.Errorf("a refusal exits 1, got %d", ExitCodeOf(err))
	}
	if got := storyStatus(t, id); got != "backlog" || len(f.edges(id)) != 0 {
		t.Errorf("a refused drive makes no transition: status %q, edges %v", got, f.edges(id))
	}
	if after := f.rows(id); len(after) != before {
		t.Errorf("a refusal before the first move writes nothing: %d ledger rows became %d", before, len(after))
	}
}

// AC2, case 2: a step whose named agent is bound in-loop is refused the same way.
func TestStoryDriveRefusesANamedAgentBoundInLoop(t *testing.T) {
	f := newDriveFixture(t, driveOpts{coderCommand: "in-loop"})
	id := f.create()
	before := len(f.rows(id))
	out, err := runRoot(t, "story", "drive", id)
	if err == nil {
		t.Fatalf("an in-loop named agent must be refused:\n%s", out)
	}
	for _, want := range []string{`step "in_progress"`, `agent "coder"`, "[coder]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q, got: %v", want, err)
		}
	}
	if ExitCodeOf(err) != 1 || storyStatus(t, id) != "backlog" {
		t.Errorf("a refusal exits 1 and moves nothing: %d, %q", ExitCodeOf(err), storyStatus(t, id))
	}
	if after := f.rows(id); len(after) != before {
		t.Errorf("a refusal before the first move writes nothing: %d ledger rows became %d", before, len(after))
	}
}

// AC4: the process exit code is read off the ExitError chain.
func TestExitCodeOf(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{errors.New("plain"), 1},
		{&ExitError{Code: 3, Err: errors.New("parked")}, 3},
		{fmt.Errorf("wrapped: %w", &ExitError{Code: 3}), 3},
		{&gateExitError{msg: "x"}, 1},
		{&gateExitError{msg: "x", code: 3}, 3},
	} {
		if got := ExitCodeOf(tc.err); got != tc.want {
			t.Errorf("ExitCodeOf(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

func TestStoryDriveNeedsAnID(t *testing.T) {
	_ = tempRepo(t)
	if _, err := runRoot(t, "story", "drive"); err == nil {
		t.Fatal("story drive with no id must fail")
	}
}

// AC7: the command's help states the exit codes and the in-loop refusal beside
// the command and points at the topic that holds the rest; the topic holds the
// full contract, so neither is the only place it is written.
func TestStoryDriveHelpDocumentsTheContract(t *testing.T) {
	long := storyDriveCommand().Long
	for _, want := range []string{"0 terminal, 3 parked, 1 refused", "in-loop", "[executor]", "parks", "satelle help agent-dispatch"} {
		if !strings.Contains(long, want) {
			t.Errorf("story drive --help must state %q", want)
		}
	}
	topic, ok := help.Get("agent-dispatch")
	if !ok {
		t.Fatal("no agent-dispatch help topic")
	}
	for _, want := range []string{"satelle story drive", "StepPerformer", "Exit status", "reject_budget", "no park state"} {
		if !strings.Contains(topic.Body, want) {
			t.Errorf("satelle help agent-dispatch must document the drive: missing %q", want)
		}
	}
}
