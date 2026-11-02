//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
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
	nodeDriveOptions = func(*cobra.Command) (drive.Options, error) { return drive.Options{Performers: perf}, nil }
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
	subject := &subjectStory{}
	r.fakeSetup = func(f *devtest.Node) {
		f.Run = subject
		f.LostAfterSeconds = 3
	}
	return r, subject
}

// subjectStory is the story a wake names: a real story in the node's own store,
// made when the node reads its run, so the test can show the run leaves it alone.
// Its JSON is the run descriptor the fake serves.
type subjectStory struct {
	mu   sync.Mutex
	item workitem.Item
	err  error
}

func (s *subjectStory) MarshalJSON() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.item.ID == "" && s.err == nil {
		s.err = dispatchInto(context.Background(), "story-create", map[string]any{
			"title": "subject", "body": "the story a task wake names, which the run must not move", "category": "chore",
		}, &s.item)
	}
	if s.err != nil {
		return nil, s.err
	}
	return json.Marshal(map[string]string{"mode": "node", "story_id": s.item.ID, "trigger": "nightly", "trigger_source": "schedule"})
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

// Status is the subject's status in the node's store now, and what it was when the
// wake's subject was made.
func (s *subjectStory) Status() (now, created string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cur workitem.Item
	if err := dispatchInto(context.Background(), "story-get", map[string]any{"id": s.item.ID}, &cur); err != nil {
		return "", s.item.Status, err
	}
	return cur.Status, s.item.Status, nil
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

// A task node woken by satelle.dev (no local end on the relay, which here never
// pairs) runs its task against the node API alone: it concludes accept, ends once
// and never dials the relay.
func TestNodeJoinTaskNodeRunsWithoutTheRelay(t *testing.T) {
	r, subject := taskRig(t, "nightly-sweep", true)
	useTaskNode(t, recordingPerformers(), 0)
	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	st := w.fake.Statuses()
	if len(st) == 0 {
		t.Fatal("the node sent no status")
	}
	if last := st[len(st)-1]; last["run_state"] != "concluded" || last["outcome"] != verb.OutcomeAccept || last["story_id"] != subject.ID() {
		t.Errorf("last status = %v, want the wake's run concluded accept", last)
	}
	if n := w.relayHits.Load(); n != 0 {
		t.Errorf("relay requests = %d, want none", n)
	}
	if n := r.relay.Attempts(); n != 0 {
		t.Errorf("rig relay dials = %d, want none", n)
	}
	if n := w.fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
	if !strings.Contains(r.log.String(), "join: ready: definition=v7 ") {
		t.Errorf("node log does not record the ready report:\n%s", r.log)
	}
}

// A wake of a task node: one execution of the task, its inputs the descriptor's
// fields the task declares, driven through the task's own route, reported as queued, running with the step (and re-sent while
// the step runs, at an interval under the server's lost-after window) and
// concluded with the outcome; the subject story is not touched and the node ends
// after its run.
func TestNodeWakeRunsOneExecutionAndReportsIt(t *testing.T) {
	r, subject := taskRig(t, "nightly-sweep", true)
	var (
		mu      sync.Mutex
		inStep  []workitem.Item
		movedTo string
		entered = make(chan struct{})
		release = make(chan struct{})
		once    sync.Once
	)
	perf := &taskPerformers{
		step: func(item workitem.Item) error {
			mu.Lock()
			if err := dispatchInto(context.Background(), "execution-list", map[string]any{"parent_id": "tsk_nightly-sweep"}, &inStep); err != nil {
				t.Errorf("execution-list: %v", err)
			}
			mu.Unlock()
			once.Do(func() { close(entered) })
			<-release
			// The performer's verdict: an outcome the code would not default to.
			return dispatchInto(context.Background(), "execution-record",
				map[string]any{"id": item.ID, "output": "swept", "outcome": verb.OutcomeAccept}, &struct{}{})
		},
		entered: func(item workitem.Item, step string) {
			if step != "done" || item.Kind != workitem.KindExecution {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if now, _, err := subject.Status(); err == nil {
				movedTo = now
			}
		},
	}
	useTaskNode(t, perf, 10*time.Millisecond)

	// The run is held in its step until the test lets it go.
	w := r.wake(t)
	fake, joined, spec := w.fake, w.joined, w.spec
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatalf("the drive never reached the working step\nnode log:\n%s", r.log)
	}

	// The step is running: the heartbeat is re-sent while it does.
	deadline := time.Now().Add(30 * time.Second)
	count := func() int {
		n := 0
		for _, s := range fake.Statuses() {
			if s["run_state"] == "running" && s["step"] == "in_progress" {
				n++
			}
		}
		return n
	}
	for count() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := count(); n < 3 {
		t.Fatalf("running heartbeats during the step = %d, want the first plus at least two re-sends", n)
	}
	mu.Lock()
	execs := append([]workitem.Item(nil), inStep...)
	mu.Unlock()
	if len(execs) != 1 {
		t.Fatalf("executions of the task = %d, want exactly one: %+v", len(execs), execs)
	}
	if !reflect.DeepEqual(execs[0].InputValues, map[string]any{"story_id": subject.ID(), "trigger": "nightly"}) {
		t.Errorf("inputs = %v, want the descriptor's declared fields only", execs[0].InputValues)
	}
	if want := "remote-node:" + spec.Session; !containsTag(execs[0].Tags, want) {
		t.Errorf("execution tags = %v, want %s", execs[0].Tags, want)
	}

	close(release)
	if err := waitJoin(t, joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}

	if got, want := runSteps(t, fake, subject.ID()), []string{"queued//", "running/in_progress/", "running/done/", "concluded//accept"}; !reflect.DeepEqual(got, want) {
		t.Errorf("heartbeats = %v, want %v", got, want)
	}
	// The concluded outcome is the one the performer recorded on the run-output doc,
	// not the code's fallback.
	docs, _ := filepath.Glob(filepath.Join(r.nodeRoot, ".satelle", "tasks", "tsk_nightly-sweep", "output-exe_*.md"))
	if len(docs) != 1 {
		t.Fatalf("run-output docs = %v, want one", docs)
	}
	if body, err := os.ReadFile(docs[0]); err != nil || !strings.Contains(string(body), "\noutcome: accept\n") {
		t.Errorf("the run-output doc does not record the performer's outcome: %v\n%s", err, body)
	}
	// The subject is the story the wake named, and the run left its status as it was:
	// read when the drive entered its last step, the node's store is gone after.
	mu.Lock()
	if _, created := subject.Created(); created == "" || movedTo != created {
		t.Errorf("the subject story went from %q to %q over the run, want it unchanged", created, movedTo)
	}
	mu.Unlock()
	// One end call, and nothing was sent after it or after the concluded heartbeat.
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
	calls := fake.Calls()
	if last := calls[len(calls)-1]; last.Method != http.MethodPost || !strings.HasSuffix(last.Path, "/end") {
		t.Errorf("the last call = %+v, want the end call", last)
	}
	if st := fake.Statuses(); st[len(st)-1]["run_state"] != "concluded" {
		t.Errorf("the last heartbeat = %+v, want concluded", st[len(st)-1])
	}
}

// A task whose performer records outcome no-op concludes without error, and the
// node reports concluded with outcome no-op and ends once.
func TestNodeWakeThatFindsNothingToDoConcludesNoOp(t *testing.T) {
	r, subject := taskRig(t, "nightly-sweep", true)
	perf := &taskPerformers{step: func(item workitem.Item) error {
		return dispatchInto(context.Background(), "execution-record",
			map[string]any{"id": item.ID, "output": "nothing to do", "outcome": "no-op"}, &struct{}{})
	}}
	useTaskNode(t, perf, 0)

	w := r.wake(t)
	fake, joined := w.fake, w.joined
	if err := waitJoin(t, joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	if got, want := runSteps(t, fake, subject.ID()), []string{"queued//", "running/in_progress/", "running/done/", "concluded//no-op"}; !reflect.DeepEqual(got, want) {
		t.Errorf("heartbeats = %v, want %v", got, want)
	}
	if n := fake.Count(http.MethodPost, "/end"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
	docs, _ := filepath.Glob(filepath.Join(r.nodeRoot, ".satelle", "tasks", "tsk_nightly-sweep", "output-exe_*.md"))
	if len(docs) != 1 {
		t.Fatalf("run-output docs = %v, want one", docs)
	}
	if body, err := os.ReadFile(docs[0]); err != nil || !strings.Contains(string(body), "\noutcome: no-op\n") {
		t.Errorf("the run-output doc does not record the outcome: %v\n%s", err, body)
	}
}

// A definition bundle that carries no workflow tree runs on the embedded
// defaults: the node prepares, reports the embedded done.toml's categories, and
// drives its task through the shipped execution route — whose reviewer gates a
// stub reviewer bound in the bundle's agents layer answers — to a concluded
// outcome, never failing on a missing done.toml.
func TestNodeTaskRunsWithoutWorkflowTree(t *testing.T) {
	r, subject := taskRig(t, "nightly-sweep", true)
	delete(r.bundle, "workflows/done.toml")
	delete(r.bundle, "workflows/step.toml")
	stubDir := t.TempDir()
	reviewer, judged := filepath.Join(stubDir, "stub-reviewer"), filepath.Join(stubDir, "judged")
	script := "#!/bin/sh\necho judged >> " + judged + "\ncat > /dev/null\n" + `echo '{"decision":"accept","notes":"stub"}'` + "\n"
	if err := os.WriteFile(reviewer, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r.bundle["workflows/agents.toml"] += "\n[reviewer]\nrole = \"reviewer\"\ninterface = \"command\"\ncommand = \"" + reviewer +
		" -p {system}\"\ntools = \"Read,Grep,Glob\"\nisolation = \"operator-attested\"\n"
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	st := w.fake.Statuses()
	if len(st) == 0 {
		t.Fatal("the node sent no status")
	}
	last := st[len(st)-1]
	if last["run_state"] != "concluded" || last["story_id"] != subject.ID() || strings.Contains(last["findings"], "done.toml") {
		t.Errorf("last status = %v, want the wake's run concluded with no done.toml finding\nnode log:\n%s", last, r.log)
	}
	// The gates were answered by the bundle's own [reviewer], not a binding
	// init scaffolded before the definition was installed.
	if body, err := os.ReadFile(judged); err != nil || strings.Count(string(body), "judged") < 2 {
		t.Errorf("the bundle's stub reviewer judged %q (%v), want at least the run's two gates", body, err)
	}
	assertReadyWorkflows(t, r.log.String(), embeddedCategories(t))
}

// A bundle that carries its own done.toml reports the categories satelle
// resolves from it: the file's categories merged by name over the embedded
// default, so a category only the file declares is listed beside the embedded
// ones it does not mention.
func TestNodeTaskReportsAuthoredCategories(t *testing.T) {
	r, _ := taskRig(t, "nightly-sweep", true)
	r.bundle["workflows/done.toml"] += "\n[sweep]\nobligations = [\"raised\", \"run\", \"run-verified\"]\n"
	useTaskNode(t, recordingPerformers(), 0)

	w := r.wake(t)
	if err := waitJoin(t, w.joined); err != nil {
		t.Fatalf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
	rs := wfgovern.RouteSourceOf([]docindex.Doc{{Kind: "workflows", Name: wfgovern.RouteSourceDone, Body: r.bundle["workflows/done.toml"]}})
	want := categoriesOf(t, rs.Done)
	for _, c := range []string{"sweep", "epic-parent"} {
		if !containsTag(want, c) {
			t.Fatalf("the effective categories %v lack %q: the fixture no longer proves the merge", want, c)
		}
	}
	assertReadyWorkflows(t, r.log.String(), want)
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
