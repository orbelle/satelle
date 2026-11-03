//go:build clislow && !windows

package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A task node woken by satelle.dev (no local end on the relay, which here never
// pairs) runs its task against the node API alone: it concludes accept, ends once
// and never dials the relay.
func TestSlowNodeJoinTaskNodeRunsWithoutTheRelay(t *testing.T) {
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
func TestSlowNodeWakeRunsOneExecutionAndReportsIt(t *testing.T) {
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
func TestSlowNodeWakeThatFindsNothingToDoConcludesNoOp(t *testing.T) {
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
func TestSlowNodeTaskRunsWithoutWorkflowTree(t *testing.T) {
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
func TestSlowNodeTaskReportsAuthoredCategories(t *testing.T) {
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
