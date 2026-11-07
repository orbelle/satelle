//go:build !windows

package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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
	reqs    []agent.Request
	// onDispatch runs inside Dispatch, while the run's record folder exists.
	onDispatch func(role agent.Role, req agent.Request)
}

func (f *fakeDispatcher) set(name, text string) { f.mu.Lock(); f.answers[name] = text; f.mu.Unlock() }

func (f *fakeDispatcher) Dispatch(_ context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, string(role)+":"+req.Binding.Name)
	f.reqs = append(f.reqs, req)
	if f.onDispatch != nil {
		f.onDispatch(role, req)
	}
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
	s, err := k.Seat(it.ID, "sess1", "local:/w", "", "me")
	if err != nil || s.Base != "abc123" {
		t.Fatalf("seat %+v %v", s, err)
	}
	if _, err := k.Seat(it.ID, "sess1", "mars", "", "me"); err == nil {
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
	exec, run, err := k.TaskRun(task.ID, "", "me")
	if err != nil {
		t.Fatal(err)
	}
	if exec.Kind != ExecutionKind || exec.Parent != task.ID || strings.Join(run.Inputs, ",") != it.ID+","+other.ID {
		t.Fatalf("task run %+v %v", exec, run.Inputs)
	}
	if run.BaseRef != "" || exec.Base != "abc123" {
		t.Fatalf("with a git that knows no branch: base %q, base_ref %q", exec.Base, run.BaseRef)
	}
	ev, _ := k.Cache.Events(exec.ID, KindTaskRun)
	var rec TaskRunRecord
	if len(ev) != 1 || ev[0].Decode(&rec) != nil || len(rec.Inputs) != 2 {
		t.Fatalf("task.run %+v", rec)
	}
	if _, _, err := k.TaskRun(it.ID, "", "me"); err == nil {
		t.Fatal("item without inputs ran as a task")
	}
}

// capped is a kernel whose start state a is performed by doer and left to z
// through scope (r1) and a review panel (r1, r2, all must accept); a moves
// gateless to the park held, and through strict (rejecting) to the park blk.
// hold is a's hold line, "" for none.
func capped(t *testing.T, hold string) (*Kernel, *fakeDispatcher) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"capped\"\n[items]\nprefix = { thing = \"th\" }\n")
	write(t, filepath.Join(root, "agents.toml"), `
[agents.doer]
command = "claude"
model = "big"
tools = ["Read", "Edit", "Bash"]
[agents.r1]
command = "claude"
model = "m1"
tools = ["Read"]
[agents.r2]
command = "grok"
model = "m2"
tools = ["Read"]
[agents.strict]
command = "claude"
model = "m4"
tools = ["Read"]
`)
	write(t, filepath.Join(root, "skills", "review.md"), "Review the change.\n")
	write(t, filepath.Join(root, "skills", "do.md"), "Do the work.\n")
	write(t, filepath.Join(root, "workflows", "capped.toml"), `
[states.a]
start = true
performer = { agent = "doer", skill = "do" }
`+hold+`
[states.held]
park = true
resumes = true
[states.blk]
park = true
resumes = true
[states.z]
terminal = true

[gates.scope]
skill = "review"
agent = "r1"
[gates.review]
skill = "review"
panel = ["r1", "r2"]
combine = "all"
[gates.strict]
skill = "review"
agent = "strict"

[[transition]]
from = ["a"]
to = "z"
groups = [{ mode = "sequential", gates = ["scope", "review"] }]
[[transition]]
from = ["a"]
to = "held"
[[transition]]
from = ["held"]
to = "@prior"
[[transition]]
from = ["a"]
to = "blk"
groups = [{ mode = "sequential", gates = ["strict"] }]
[[transition]]
from = ["blk"]
to = "@prior"
`)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d := &fakeDispatcher{answers: map[string]string{
		"r1": accept, "r2": `{"verdict":"reject","notes":"edge case","class":"AC3"}`, "strict": reject, "doer": "did it",
	}}
	k.Dispatcher = d
	k.Git = fakeGit{changed: []string{"src/a.go"}}
	return k, d
}

const capTwo = "hold = { rounds = 2, to = \"held\" }"

func lastMove(t *testing.T, k *Kernel, id string) item.Move {
	t.Helper()
	ev, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(ev) - 1; i >= 0; i-- {
		var m item.Move
		if ev[i].Kind == item.KindTransitioned && ev[i].Decode(&m) == nil {
			return m
		}
	}
	return item.Move{}
}

func performs(d *fakeDispatcher) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if strings.HasPrefix(c, string(agent.Perform)+":") {
			n++
		}
	}
	return n
}

func TestHoldAfterRoundCap(t *testing.T) {
	k, d := capped(t, capTwo)
	ctx := context.Background()
	it, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	res, err := k.Transition(ctx, it.ID, "z", "me")
	if err != nil || res.Moved || res.Held != nil || res.Item.Status != "a" {
		t.Fatalf("round 1: %v moved=%v held=%+v status=%s", err, res.Moved, res.Held, res.Item.Status)
	}
	res, err = k.Transition(ctx, it.ID, "z", "me")
	if err != nil || res.Moved || res.Held == nil || res.Item.Status != "held" {
		t.Fatalf("round 2: %v moved=%v held=%+v status=%s", err, res.Moved, res.Held, res.Item.Status)
	}
	if m := lastMove(t, k, it.ID); m.From != "a" || m.To != "held" || m.Hold == nil || m.Hold.Rounds != 2 {
		t.Fatalf("last move %+v", m)
	}
	if got, _ := k.Get(it.ID); got.Status != "held" {
		t.Fatalf("folded status %q", got.Status)
	}
	// Held, it is not performed again.
	before := performs(d)
	if _, err := k.Perform(ctx, it.ID, "me"); err == nil {
		t.Fatal("a held item was performed")
	}
	if performs(d) != before {
		t.Fatal("a perform dispatch reached the performer of a held item")
	}

	// A drive stops at the hold and names it.
	other, _ := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("u")}, "me")
	dr, err := k.Drive(ctx, other.ID, "me")
	if err != nil || dr.Verdict != VerdictReject || dr.Item.Status != "a" || strings.Contains(dr.Notes, "held") {
		t.Fatalf("drive 1: %v %+v", err, dr)
	}
	dr, err = k.Drive(ctx, other.ID, "me")
	if err != nil || dr.Verdict != VerdictReject || dr.Item.Status != "held" || dr.Notes != "a held at held after 2 rounds" {
		t.Fatalf("drive 2: %v %+v", err, dr)
	}

	// The operator's resume re-enters a and starts the count again.
	if res, err = k.Transition(ctx, it.ID, "a", "me"); err != nil || !res.Moved {
		t.Fatalf("resume: %v %+v", err, res)
	}
	res, err = k.Transition(ctx, it.ID, "z", "me")
	if err != nil || res.Held != nil || res.Item.Status != "a" {
		t.Fatalf("after resume: %v held=%+v status=%s", err, res.Held, res.Item.Status)
	}
	if h, err := k.HoldOf(it.ID); err != nil || h != nil {
		t.Fatalf("HoldOf after resume: %+v %v", h, err)
	}
}

func TestNoHoldWithoutCap(t *testing.T) {
	k, _ := capped(t, "")
	ctx := context.Background()
	it, _ := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t")}, "me")
	for i := range 5 {
		res, err := k.Transition(ctx, it.ID, "z", "me")
		if err != nil || res.Moved || res.Held != nil || res.Item.Status != "a" {
			t.Fatalf("round %d: %v held=%+v status=%s", i+1, err, res.Held, res.Item.Status)
		}
	}
}

func TestParkRejectsDoNotCount(t *testing.T) {
	k, _ := capped(t, capTwo)
	ctx := context.Background()
	it, _ := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t")}, "me")
	for i := range 3 {
		res, err := k.Transition(ctx, it.ID, "blk", "me")
		if err != nil || res.Moved || res.Held != nil {
			t.Fatalf("park request %d: %v %+v", i+1, err, res)
		}
	}
	res, err := k.Transition(ctx, it.ID, "z", "me")
	if err != nil || res.Held != nil || res.Item.Status != "a" {
		t.Fatalf("first counted round held: %v %+v", err, res.Held)
	}
	res, err = k.Transition(ctx, it.ID, "z", "me")
	if err != nil || res.Held == nil || res.Item.Status != "held" {
		t.Fatalf("second counted round: %v held=%+v status=%s", err, res.Held, res.Item.Status)
	}
}

func TestHoldNamesDissent(t *testing.T) {
	k, _ := capped(t, capTwo)
	ctx := context.Background()
	it, _ := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t")}, "me")
	k.Transition(ctx, it.ID, "z", "me")
	res, err := k.Transition(ctx, it.ID, "z", "me")
	if err != nil || res.Held == nil {
		t.Fatalf("not held: %v %+v", err, res)
	}
	want := item.Hold{
		State: "a", Rounds: 2,
		Dissent:  []item.Dissent{{Gate: "review", Binding: "r2", Rejected: 2, Class: "AC3", ClassRounds: 2}},
		Accepted: []string{"scope/r1", "review/r1"},
	}
	if !reflect.DeepEqual(*res.Held, want) {
		t.Fatalf("hold %+v, want %+v", *res.Held, want)
	}
	got, err := k.HoldOf(it.ID)
	if err != nil || got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("HoldOf %+v %v", got, err)
	}
}

func TestHoldOfClasses(t *testing.T) {
	round := func(cls ...string) attempt {
		a := attempt{ID: "x"}
		for i, c := range cls {
			a.Results = append(a.Results, gate.Result{Transition: "a->z", Gate: "g" + strconv.Itoa(i), Binding: "j", Result: gate.Reject, Class: c})
		}
		return a
	}
	// g0: AC1, AC3, AC3 -> AC3 in 2; g1: AC1, AC3, "" -> tie, latest AC3 in 1;
	// g2: never classified.
	h := holdOf("a", []attempt{round("AC1", "AC1", ""), round("AC3", "AC3", ""), round("AC3", "", "")})
	want := []item.Dissent{
		{Gate: "g0", Binding: "j", Rejected: 3, Class: "AC3", ClassRounds: 2},
		{Gate: "g1", Binding: "j", Rejected: 3, Class: "AC3", ClassRounds: 1},
		{Gate: "g2", Binding: "j", Rejected: 3, Class: unclassified, ClassRounds: 3},
	}
	if !reflect.DeepEqual(h.Dissent, want) || len(h.Accepted) != 0 {
		t.Fatalf("dissent %+v", h)
	}
}

func TestReplanReplacesJudgedPlan(t *testing.T) {
	k, d := capped(t, "")
	ctx := context.Background()
	it, _ := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t")}, "me")
	d.set("doer", `{"artifact":{"name":"plan","type":"plan","body":"# Plan\nPLAN-ROUND-ONE\n"}}`)
	if _, err := k.Perform(ctx, it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	atts, _ := k.Cache.Attachments(it.ID)
	if len(atts) != 1 {
		t.Fatalf("attachments %+v", atts)
	}
	first := atts[0].SHA256
	if res, _ := k.Transition(ctx, it.ID, "z", "me"); res.Moved {
		t.Fatal("round 1 moved")
	}
	answer := `{"artifact":{"name":"plan","type":"plan","body":"# Plan\nPLAN-ROUND-TWO\n"},"answers":[{"gate":"review","binding":"r2","answer":"adopted","evidence":"rewrote AC3"}]}`
	d.set("doer", answer)
	if _, err := k.Perform(ctx, it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.reqs = nil
	d.mu.Unlock()
	k.Transition(ctx, it.ID, "z", "me")
	if len(d.reqs) == 0 {
		t.Fatal("no judge was asked")
	}
	for _, r := range d.reqs {
		if !strings.Contains(r.Prompt, "PLAN-ROUND-TWO") || strings.Contains(r.Prompt, "PLAN-ROUND-ONE") {
			t.Fatalf("judge %s prompt does not hold only the newest plan:\n%s", r.Binding.Name, r.Prompt)
		}
	}
	if _, body, err := k.AttachmentBody(it.ID, "plan", first); err != nil || !strings.Contains(string(body), "PLAN-ROUND-ONE") {
		t.Fatalf("round-one plan not kept on the ledger: %q %v", body, err)
	}
	runs, _ := k.Cache.Events(it.ID, KindPerformerRun)
	var pr PerformerRun
	if len(runs) != 2 || runs[1].Decode(&pr) != nil {
		t.Fatalf("performer runs %d", len(runs))
	}
	if b, _ := k.Ledger.GetBlob(pr.Output); !strings.Contains(string(b), `"answers":[{"gate":"review"`) {
		t.Fatalf("answers not on the ledger: %s", b)
	}
}
