//go:build !windows

package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/drive"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// nodeTaskDone and nodeTaskStep are a task remote's own route for its executions:
// the run step is the executor's, so a test's stub performer does it, and neither
// the run nor its close names a validator, so nothing needs a reviewer agent. A
// real remote authors its validators here; the shipped execution route's would
// run a reviewer the test node does not have.
const nodeTaskDone = `[meta]
name = "done"
type = "workflow"
scope = "project"
tags = ["type:workflow"]
description = "The task node's declaration of done."

["*"]
obligations = ["raised", "coded", "closed"]

[execution]
obligations = ["raised", "run", "run-verified"]
cancel = { state = "cancelled" }
`

const nodeTaskStep = `[meta]
name = "step"
type = "workflow"
scope = "project"
tags = ["type:workflow"]
description = "The task node's step catalogue."

[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "worker"
skills = ["remote-work"]
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[run]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[run-verified]
status = "done"
terminal = true
requires = ["run"]

# Declaring any always-on gate replaces the shipped ones, so a run is judged by
# nothing here.
[[gate]]
skill = "satelle-step-summary"
for = ["unused"]
`

// nodeTaskFile is a task that declares two of the run descriptor's fields as inputs.
const nodeTaskFile = `---
id: tsk_nightly-sweep
type: task
status: backlog
inputs_decl: story_id:string:required, trigger:string
---

# Nightly sweep

Sweep what the story names; record no-op when there is nothing to do.
`

// taskPerformers is the headless performer a task node's execution is driven with.
// step runs as the executor's performance of the working step; entered sees each
// step the drive enters.
type taskPerformers struct {
	step    func(item workitem.Item) error
	entered func(item workitem.Item, step string)
}

func (p *taskPerformers) StepPerformer(_ context.Context, item workitem.Item, step string) (verb.PerformerResolution, error) {
	if p.entered != nil {
		p.entered(item, step)
	}
	if step == "in_progress" {
		return verb.PerformerResolution{Kind: verb.PerformerExecutor}, nil
	}
	return verb.PerformerResolution{Kind: verb.PerformerNone}, nil
}

func (p *taskPerformers) ExecutorBinding() (config.AgentBinding, bool) {
	return config.AgentBinding{Command: "fake-cli -p {system}"}, true
}

func (p *taskPerformers) PerformExecutorStep(_ context.Context, item workitem.Item, _, _ string) (verb.DispatchResult, error) {
	res := verb.DispatchResult{Dispatched: true, Agent: "executor", Command: "fake-cli", Skill: "s"}
	if p.step != nil {
		return res, p.step(item)
	}
	return res, nil
}

// useTaskNode points a task node's drive at perf and its heartbeat resend at every.
func useTaskNode(t *testing.T, perf drive.Performers, every time.Duration) {
	t.Helper()
	prevOpts, prevEvery := nodeDriveOptions, nodeStatusResend
	// The drive logs to the join's log, as production's driveOptions has it.
	nodeDriveOptions = func(cmd *cobra.Command) (drive.Options, error) {
		return drive.Options{Performers: perf, Out: cmd.ErrOrStderr()}, nil
	}
	nodeStatusResend = every
	t.Cleanup(func() { nodeDriveOptions, nodeStatusResend = prevOpts, prevEvery })
}

// taskRig is a node rig whose definition drives the task named task, and whose
// wake is the descriptor the fake serves.
func taskRig(t *testing.T, task string, withTaskFile bool) (*nodeRig, *subjectStory) {
	t.Helper()
	// Read before newNodeRig changes the working directory.
	remote, err := os.ReadFile(filepath.Join("..", "remotedef", "testdata", "task.remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	r := newNodeRig(t)
	r.bundle["remote.toml"] = strings.ReplaceAll(string(remote), "nightly-sweep", task)
	r.bundle["workflows/done.toml"] = nodeTaskDone
	r.bundle["workflows/step.toml"] = nodeTaskStep
	if withTaskFile {
		r.bundle["tasks/tsk_"+task+".md"] = nodeTaskFile
	}
	subject := &subjectStory{sync: r.sync}
	r.fakeSetup = func(f *devtest.Node) {
		subject.fake = f
		f.Run = subject
		f.LostAfterSeconds = 3
	}
	return r, subject
}

// subjectStory is the story a wake names: a real story in the node's own store,
// made when the node reads its run, so the test can show the run leaves it alone.
// Its rows are on the node's project's hosted chain too, as a story of the team
// workspace's are. Its JSON is the run descriptor the fake serves.
type subjectStory struct {
	mu   sync.Mutex
	item workitem.Item
	err  error
	fake *devtest.Node
	sync *nodeSync
}

// ID is the subject's id, "" until the node has read its run.
func (s *subjectStory) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.item.ID
}

// Created is the subject's id and the status it was made with.
func (s *subjectStory) Created() (id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.item.ID, s.item.Status
}

// runSteps is the heartbeats the fake received as run_state/step/outcome, with
// adjacent repeats collapsed, and fails the test on one that is not the node's
// running state for the wake's subject.
func runSteps(t *testing.T, fake *devtest.Node, subject string) []string {
	t.Helper()
	var out []string
	for _, s := range fake.Statuses() {
		// The node's own join heartbeat (no story) covers the install and ends before
		// the run's stream starts: it may lead, and none follows the run's first.
		if s["story_id"] == "" && s["run_state"] == "" && s["step"] == remotenode.StepWaiting {
			if len(out) != 0 {
				t.Errorf("join heartbeat %+v follows the run's own heartbeat", s)
			}
			continue
		}
		if s["state"] != "running" || s["story_id"] != subject {
			t.Errorf("heartbeat %+v is not the running state for the wake's subject", s)
		}
		line := s["run_state"] + "/" + s["step"] + "/" + s["outcome"]
		if len(out) == 0 || out[len(out)-1] != line {
			out = append(out, line)
		}
	}
	return out
}

// A definition that declares [drive] kind = "task" with a task its bundle does
// not carry is refused at prepare, naming the task: the node reports the failed
// start on the node API as a concluded reject whose findings name the task, and
// ends once.
func TestNodePrepareRefusesATaskItDoesNotHave(t *testing.T) {
	r, subject := taskRig(t, "missing", false)
	useTaskNode(t, &taskPerformers{}, 0)
	w := r.wake(t)
	if jerr := waitJoin(t, w.joined); jerr == nil || !strings.Contains(jerr.Error(), "which the node does not have") {
		t.Errorf("join ended with %v, want the task refusal", jerr)
	}
	assertStartFailureStatuses(t, w.fake, 1)
	st := w.fake.Statuses()
	last := st[len(st)-1]
	if f := last["findings"]; !strings.Contains(f, "missing") || !strings.Contains(f, "tsk_missing") {
		t.Errorf("concluded findings = %q, want the refusal naming the task missing (tsk_missing)", f)
	}
	if last["story_id"] != subject.ID() {
		t.Errorf("concluded status story = %q, want the wake's subject %q", last["story_id"], subject.ID())
	}
	// The run is read once, by the failed-start report, to name its story.
	if n := w.fake.Count(http.MethodGet, "/run"); n != 1 {
		t.Errorf("run reads = %d, want 1 (the failed-start report's)", n)
	}
	if n := w.relayHits.Load(); n != 0 {
		t.Errorf("relay requests = %d, want none", n)
	}
}

// embeddedCategories is the categories of the binary's embedded done.toml.
func embeddedCategories(t *testing.T) []string {
	t.Helper()
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "workflows" && d.Name == wfgovern.RouteSourceDone {
			return categoriesOf(t, d.Body)
		}
	}
	t.Fatal("no embedded done.toml")
	return nil
}

// categoriesOf is the categories a done.toml declares, in order.
func categoriesOf(t *testing.T, body string) []string {
	t.Helper()
	lists, err := wfdot.ParseDone(body)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(lists))
	for i, l := range lists {
		out[i] = l.Category
	}
	return out
}

// assertReadyWorkflows checks the ready report the task node logged lists
// exactly want as its workflows.
func assertReadyWorkflows(t *testing.T, log string, want []string) {
	t.Helper()
	for _, line := range strings.Split(log, "\n") {
		if !strings.HasPrefix(line, "join: ready: ") {
			continue
		}
		_, got, ok := strings.Cut(line, " workflows=")
		if !ok {
			t.Fatalf("ready line %q carries no workflows", line)
		}
		if got != strings.Join(want, ",") {
			t.Errorf("reported workflows = %s, want %s", got, strings.Join(want, ","))
		}
		return
	}
	t.Fatalf("the node logged no ready report:\n%s", log)
}

func containsTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}
