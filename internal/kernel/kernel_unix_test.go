//go:build !windows

package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
)

type fakeGit struct{ changed []string }

func (fakeGit) Head() (string, error)                     { return "abc123", nil }
func (g fakeGit) ChangedPaths(string) ([]string, error)   { return g.changed, nil }
func (fakeGit) Diff(_ string, p []string) (string, error) { return "diff:" + strings.Join(p, ","), nil }

type fakeDispatcher struct {
	mu      sync.Mutex
	answers map[string]string
	calls   []string
}

func (f *fakeDispatcher) set(name, text string) { f.mu.Lock(); f.answers[name] = text; f.mu.Unlock() }

func (f *fakeDispatcher) Dispatch(_ context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, string(role)+":"+req.Binding.Name)
	text, ok := f.answers[req.Binding.Name]
	if !ok {
		return agent.Result{}, errors.New("no answer")
	}
	return agent.Result{Text: text, TokensIn: 3, TokensOut: 2, Model: req.Binding.Model}, nil
}

const accept, reject = `{"verdict":"accept","notes":"ok"}`, `{"verdict":"reject","notes":"no"}`

func multiState(t *testing.T) (*Kernel, *fakeDispatcher, string) {
	t.Helper()
	repo := t.TempDir()
	root := filepath.Join(repo, ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"flow\"\n[[workflows.rule]]\nworkflow = \"default\"\nkind = \"chore\"\n[items]\nprefix = { thing = \"th\" }\n")
	write(t, filepath.Join(root, "workflows", "default.toml"), "[states.a]\nstart = true\n[states.z]\nterminal = true\n[[transition]]\nfrom = [\"a\"]\nto = \"z\"\n")
	write(t, filepath.Join(root, "agents.toml"), `
[agents.doer]
command = "claude"
model = "big"
tools = ["Read", "Edit", "Bash"]
[agents.r1]
command = "claude"
model = "m1"
tools = ["Read", "Grep"]
[agents.r2]
command = "grok"
model = "m2"
tools = ["Read"]
[agents.r3]
command = "pi"
model = "m3"
tools = ["Read"]
[agents.strict]
command = "claude"
model = "m4"
tools = ["Read"]
`)
	write(t, filepath.Join(root, "skills", "lint.md"), "---\ntype: check\n---\n```sh\ntest -f marker && echo \"lint $SATELLE_ITEM $SATELLE_BASE\"\n```\n")
	write(t, filepath.Join(root, "skills", "review.md"), "Review the change.\n")
	write(t, filepath.Join(root, "skills", "do.md"), "Do the work.\n")
	write(t, filepath.Join(root, "workflows", "flow.toml"), `
[states.open]
start = true
[states.work]
performer = { agent = "doer", skill = "do" }
[states.hold]
park = true
resumes = true
[states.closed]
terminal = true

[gates.lint]
skill = "lint"
[gates.docs]
skill = "review"
agent = "r1"
paths = ["docs/**"]
[gates.review]
skill = "review"
agent = "r1"
paths = ["**/*.go"]
carry = true
[gates.panel]
skill = "review"
panel = ["r1", "r2", "r3"]
combine = "majority"
[gates.strict]
skill = "review"
agent = "strict"

[[transition]]
from = ["open"]
to = "work"
groups = [{ mode = "sequential", gates = ["lint", "docs", "review"] }]
[[transition]]
from = ["work"]
to = "closed"
groups = [{ mode = "sequential", gates = ["review"] }, { mode = "parallel", gates = ["panel", "strict"] }]
[[transition]]
from = ["*"]
to = "hold"
[[transition]]
from = ["hold"]
to = "@prior"
`)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d := &fakeDispatcher{answers: map[string]string{"r1": accept, "r2": reject, "r3": accept, "strict": reject, "doer": "did it"}}
	k.Dispatcher = d
	k.Git = fakeGit{changed: []string{"src/a.go"}}
	return k, d, repo
}

func gates(r TransitionResult) string {
	var out []string
	for _, g := range r.Gates {
		out = append(out, g.Gate+"="+g.Result)
	}
	return strings.Join(out, " ")
}

func TestMultiStateGates(t *testing.T) {
	k, d, repo := multiState(t)
	ctx := context.Background()
	it, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t"), Body: ptr("b")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if it.Workflow != "flow" || it.Status != "open" || !strings.HasPrefix(it.ID, "th_") || it.Base != "abc123" {
		t.Fatalf("created %+v", it)
	}
	if chore, _ := k.Create(item.Fields{Kind: ptr("chore"), Title: ptr("c")}, "me"); chore.Workflow != "default" || chore.Status != "a" {
		t.Fatalf("rule selection %+v", chore)
	}

	// Fail-fast: the check rejects, so nothing after it runs.
	res, err := k.Transition(ctx, it.ID, "work", "me")
	if err != nil || res.Moved || gates(res) != "lint=reject" || len(d.calls) != 0 {
		t.Fatalf("fail-fast: %v %s %v", err, gates(res), d.calls)
	}
	os.WriteFile(filepath.Join(repo, "marker"), nil, 0o644)
	res, err = k.Transition(ctx, it.ID, "work", "me")
	if err != nil || !res.Moved || gates(res) != "lint=accept docs=skipped review=accept" {
		t.Fatalf("open->work: %v %s", err, gates(res))
	}
	if res.Gates[0].Notes != "lint "+it.ID+" abc123" || res.Gates[2].TokensIn != 3 {
		t.Fatalf("gate details %+v", res.Gates)
	}

	// Perform dispatches the state's performer and records the run.
	out, err := k.Perform(ctx, it.ID, "me")
	if err != nil || out.Text != "did it" {
		t.Fatalf("perform %+v %v", out, err)
	}
	runs, _ := k.Cache.Events(it.ID, KindPerformerRun)
	var pr PerformerRun
	if len(runs) != 1 || runs[0].Decode(&pr) != nil || pr.Binding != "doer" || pr.Output == "" {
		t.Fatalf("performer.run %+v", pr)
	}
	if b, _ := k.Ledger.GetBlob(pr.Output); string(b) != "did it" {
		t.Fatal("performer output blob")
	}

	// Park and resume to the prior state.
	if res, err = k.Transition(ctx, it.ID, "hold", "me"); err != nil || !res.Moved {
		t.Fatalf("park: %v", err)
	}
	if res, err = k.Transition(ctx, it.ID, "open", "me"); err == nil {
		t.Fatal("resumed to a state it was not parked from")
	}
	if res, err = k.Transition(ctx, it.ID, "work", "me"); err != nil || !res.Moved {
		t.Fatalf("resume: %v", err)
	}

	// Parallel group: both run, one rejects; the earlier review accept is
	// recorded on this transition.
	d.calls = nil
	res, err = k.Transition(ctx, it.ID, "closed", "me")
	if err != nil || res.Moved || gates(res) != "review=accept panel=accept strict=reject" {
		t.Fatalf("work->closed 1: %v %s", err, gates(res))
	}
	if len(res.Rejects()) != 1 || !strings.Contains(res.Gates[1].Notes, "2 of 3 accept") {
		t.Fatalf("rejects %+v", res.Gates)
	}
	// Re-presented: review is carried, the rest judged again.
	d.set("strict", accept)
	d.calls = nil
	res, err = k.Transition(ctx, it.ID, "closed", "me")
	if err != nil || !res.Moved || gates(res) != "review=carried panel=accept strict=accept" {
		t.Fatalf("work->closed 2: %v %s", err, gates(res))
	}
	for _, c := range d.calls {
		if c == "judge:r1" {
			continue
		}
		if !strings.HasPrefix(c, "judge:") {
			t.Fatalf("non-judge dispatch %v", d.calls)
		}
	}
	if len(d.calls) != 4 { // r1, r2, r3 panel + strict; review carried
		t.Fatalf("calls %v", d.calls)
	}
	recorded, _ := k.Cache.Events(it.ID, gate.KindRun)
	if len(recorded) != 10 {
		t.Fatalf("gate.run events: %d", len(recorded))
	}
	if err := k.Ledger.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestSeatsAttachmentsTasks(t *testing.T) {
	k, _, _ := multiState(t)
	it, _ := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t")}, "me")
	s, err := k.Seat(it.ID, "sess1", "local:/w", "me")
	if err != nil || s.Base != "abc123" {
		t.Fatalf("seat %+v %v", s, err)
	}
	if _, err := k.Seat(it.ID, "sess1", "mars", "me"); err == nil {
		t.Fatal("bad location accepted")
	}
	if seats, _ := k.Cache.Seats("sess1", ""); len(seats) != 1 {
		t.Fatalf("seats %+v", seats)
	}
	if err := k.Release(it.ID, "sess1", "me"); err != nil {
		t.Fatal(err)
	}
	if err := k.Release(it.ID, "sess1", "me"); err == nil {
		t.Fatal("double release")
	}
	a, err := k.Attach(it.ID, "plan.md", []byte("# plan"), "text/markdown", "me")
	if err != nil || a.Size != 6 {
		t.Fatalf("attach %+v %v", a, err)
	}
	if atts, _ := k.Cache.Attachments(it.ID); len(atts) != 1 || atts[0].SHA256 != a.SHA256 {
		t.Fatalf("attachments %+v", atts)
	}

	task, err := k.Create(item.Fields{Kind: ptr("task"), Title: ptr("sweep"), Inputs: &item.Selection{Kind: "thing", Status: "open"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("u")}, "me")
	exec, ids, err := k.TaskRun(task.ID, "me")
	if err != nil {
		t.Fatal(err)
	}
	if exec.Kind != ExecutionKind || exec.Parent != task.ID || strings.Join(ids, ",") != it.ID+","+other.ID {
		t.Fatalf("task run %+v %v", exec, ids)
	}
	ev, _ := k.Cache.Events(exec.ID, KindTaskRun)
	var rec TaskRunRecord
	if len(ev) != 1 || ev[0].Decode(&rec) != nil || len(rec.Inputs) != 2 {
		t.Fatalf("task.run %+v", rec)
	}
	if _, _, err := k.TaskRun(it.ID, "me"); err == nil {
		t.Fatal("item without inputs ran as a task")
	}
}
