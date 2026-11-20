//go:build !windows

package kernel

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func dryFixture(t *testing.T) (k *Kernel, d *fakeDispatcher, dumps string) {
	t.Helper()
	repo := t.TempDir()
	dumps = t.TempDir()
	root := filepath.Join(repo, ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"flow\"\n[items]\nprefix = { thing = \"th\" }\n")
	write(t, filepath.Join(root, "agents.toml"), `
[agents.r1]
command = "claude"
model = "m1"
tools = ["Read"]
[agents.r2]
command = "grok"
model = "m2"
tools = ["Read"]
`)
	write(t, filepath.Join(root, "skills", "probe.md"), "---\ntype: check\n---\n```sh\nn=$(ls '"+dumps+"' | wc -l | tr -d ' ')\n{ env | grep '^SATELLE_' | sort; pwd; cat; } > '"+dumps+"'/$n\necho probed\n```\n")
	write(t, filepath.Join(root, "skills", "pass.md"), "---\ntype: check\n---\n```sh\necho fine\necho second line\n```\n")
	write(t, filepath.Join(root, "skills", "review.md"), "Review the change.\n")
	write(t, filepath.Join(root, "workflows", "flow.toml"), `
[states.open]
start = true
[states.plan]
[states.work]
hold = { rounds = 1, to = "held" }
[states.held]
park = true
resumes = true
[states.closed]
terminal = true

[gates.probe]
skill = "probe"
[gates.review]
skill = "review"
agent = "r1"
[gates.panel]
skill = "review"
panel = ["r1", "r2"]
[gates.pick]
skill = "pass"
[gates.guarded]
skill = "probe"
trigger = "probe"

[[transition]]
from = ["open"]
to = "plan"
[[transition]]
from = ["plan"]
to = "work"
groups = [{ gates = ["review"] }]
[[transition]]
from = ["open"]
to = "work"
groups = [{ gates = ["probe"] }]
[[transition]]
from = ["work"]
to = "closed"
groups = [{ gates = ["panel", "pick"] }]
[[transition]]
from = ["work"]
to = "plan"
groups = [{ gates = ["pick", "guarded"] }]
[[transition]]
from = ["*"]
to = "held"
[[transition]]
from = ["held"]
to = "@prior"
`)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d = &fakeDispatcher{answers: map[string]string{"r1": accept, "r2": reject}}
	k.Dispatcher = d
	k.Git = fakeGit{changed: []string{"src/a.go"}}
	return k, d, dumps
}

func newThing(t *testing.T, k *Kernel) item.Item {
	t.Helper()
	it, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t"), Body: ptr("b"), Criteria: ptr([]string{"it works"})}, "me")
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func dumpsOf(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		out = append(out, string(b))
	}
	return out
}

func chain(t *testing.T, k *Kernel, id string) (int, string) {
	t.Helper()
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return len(events), events[len(events)-1].Hash
}

func dry(t *testing.T, k *Kernel, req GateDryRequest) GateDryResult {
	t.Helper()
	res, err := k.GateDry(context.Background(), req)
	if err != nil {
		t.Fatalf("dry %+v: %v", req, err)
	}
	return res
}

func TestGateDryCheckGetsTheTransitionsInput(t *testing.T) {
	k, _, dumps := dryFixture(t)
	it := newThing(t, k)
	res := dry(t, k, GateDryRequest{Workflow: "flow", Gate: "probe", Item: it.ID})
	if res.From != "open" || res.To != "work" || res.Result.Result != gate.Accept || res.Result.Notes != "probed" {
		t.Fatalf("dry run %+v %+v", res, res.Result)
	}
	moveTo(t, k, it.ID, "work")
	got := dumpsOf(t, dumps)
	if len(got) != 2 {
		t.Fatalf("dumps %d, want 2", len(got))
	}
	if got[0] != got[1] {
		t.Fatalf("the dry run's input differs from the transition's:\n--- dry\n%s\n--- transition\n%s", got[0], got[1])
	}
	if !strings.Contains(got[0], "SATELLE_ITEM="+it.ID) || !strings.Contains(got[0], `"from":"open"`) {
		t.Fatalf("dump:\n%s", got[0])
	}
}

func normalised(prompt string) string {
	if dir := folderOf(prompt); dir != "" {
		return strings.ReplaceAll(prompt, dir, "<record>")
	}
	return prompt
}

func TestGateDryJudgeGetsTheTransitionsPrompt(t *testing.T) {
	k, d, _ := dryFixture(t)
	seen := watch(d)
	it := newThing(t, k)
	moveTo(t, k, it.ID, "plan")
	mustAttach(t, k, it.ID, "plan", "the plan\n")
	res := dry(t, k, GateDryRequest{Workflow: "flow", Gate: "review", Item: it.ID})
	if res.To != "work" || res.Result.Result != gate.Accept || res.Result.Binding != "r1" {
		t.Fatalf("dry run %+v", res.Result)
	}
	moveTo(t, k, it.ID, "work")
	if len(d.reqs) != 2 || len(*seen) != 2 {
		t.Fatalf("dispatches %d, seen %d", len(d.reqs), len(*seen))
	}
	a, b := d.reqs[0], d.reqs[1]
	if a.System != b.System || a.Dir != b.Dir || a.Binding.Name != b.Binding.Name {
		t.Fatalf("requests differ:\n%+v\n%+v", a, b)
	}
	if folderOf(a.Prompt) == "" || normalised(a.Prompt) != normalised(b.Prompt) {
		t.Fatalf("prompts differ:\n--- dry\n%s\n--- transition\n%s", a.Prompt, b.Prompt)
	}
	for i, s := range *seen {
		if s.files["attachments/plan"] != "the plan\n" || s.modes["attachments/plan"] != 0o444 {
			t.Fatalf("dispatch %d saw %+v %+v", i, s.files, s.modes)
		}
	}
	runsEmpty(t, k)
}

func TestGateDryRecordsNothing(t *testing.T) {
	k, d, _ := dryFixture(t)
	it := newThing(t, k)
	moveTo(t, k, it.ID, "work")
	n, hash := chain(t, k, it.ID)
	unchanged := func(when string) {
		t.Helper()
		if n2, h2 := chain(t, k, it.ID); n2 != n || h2 != hash {
			t.Fatalf("%s: chain %d %s, was %d %s", when, n2, h2, n, hash)
		}
		runsEmpty(t, k)
		if got, _ := k.Get(it.ID); got.Status != "work" {
			t.Fatalf("%s: status %q", when, got.Status)
		}
	}
	for i := 0; i < 3; i++ {
		if res := dry(t, k, GateDryRequest{Workflow: "flow", Gate: "panel", Item: it.ID}); res.Result.Result != gate.Reject {
			t.Fatalf("panel %+v", res.Result)
		}
		unchanged("reject")
	}
	if res := dry(t, k, GateDryRequest{Workflow: "flow", Gate: "pick", Item: it.ID, To: "closed"}); res.Result.Result != gate.Accept {
		t.Fatalf("pick %+v", res.Result)
	}
	unchanged("accept")
	delete(d.answers, "r1")
	delete(d.answers, "r2")
	if res := dry(t, k, GateDryRequest{Workflow: "flow", Gate: "panel", Item: it.ID}); res.Result.Result != gate.Error {
		t.Fatalf("errored panel %+v", res.Result)
	}
	unchanged("error")
	d.answers["r1"], d.answers["r2"] = accept, reject
	res, err := k.Transition(context.Background(), it.ID, "closed", "me")
	if err != nil || res.Moved || res.Held == nil || res.Held.Rounds != 1 {
		t.Fatalf("the first real reject: %v moved=%v held=%+v", err, res.Moved, res.Held)
	}
}

func TestGateDrySelectsTheTransition(t *testing.T) {
	k, d, dumps := dryFixture(t)
	opened := newThing(t, k)
	working := newThing(t, k)
	moveTo(t, k, working.ID, "work")
	before := len(dumpsOf(t, dumps))

	if res := dry(t, k, GateDryRequest{Workflow: "flow", Gate: "panel", Item: working.ID}); res.From != "work" || res.To != "closed" {
		t.Fatalf("default: %s -> %s", res.From, res.To)
	}
	if res := dry(t, k, GateDryRequest{Workflow: "flow", Gate: "panel", Item: opened.ID, From: "work"}); res.From != "work" || res.To != "closed" {
		t.Fatalf("--from: %s -> %s", res.From, res.To)
	}
	if res := dry(t, k, GateDryRequest{Workflow: "flow", Gate: "pick", Item: working.ID, To: "plan"}); res.To != "plan" || res.Result.Result != gate.Accept {
		t.Fatalf("--to: %+v", res)
	}
	calls := len(d.calls)
	for _, c := range []struct {
		name string
		req  GateDryRequest
		want []string
	}{
		{"two transitions", GateDryRequest{Workflow: "flow", Gate: "pick", Item: working.ID}, []string{"closed, plan", "--to"}},
		{"unknown workflow", GateDryRequest{Workflow: "nope", Gate: "pick", Item: working.ID}, []string{`"nope"`, "flow"}},
		{"unknown gate", GateDryRequest{Workflow: "flow", Gate: "nope", Item: working.ID}, []string{`"nope"`, "guarded, panel, pick, probe, review"}},
		{"not a state", GateDryRequest{Workflow: "flow", Gate: "panel", Item: working.ID, From: "nope"}, []string{`"nope"`, "--from"}},
		{"no transition from the state", GateDryRequest{Workflow: "flow", Gate: "panel", Item: opened.ID}, []string{`"open"`, "--from"}},
		{"--to does not run the gate", GateDryRequest{Workflow: "flow", Gate: "panel", Item: working.ID, To: "held"}, []string{"work -> held", "panel"}},
	} {
		_, err := k.GateDry(context.Background(), c.req)
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q does not name %q", c.name, err, w)
			}
		}
		if len(d.calls) != calls || len(dumpsOf(t, dumps)) != before {
			t.Errorf("%s: something ran", c.name)
		}
	}
}

func TestGateDryShowInputRunsNothing(t *testing.T) {
	k, d, dumps := dryFixture(t)
	it := newThing(t, k)
	show := dry(t, k, GateDryRequest{Workflow: "flow", Gate: "probe", Item: it.ID, ShowInput: true})
	if show.Result != nil || show.Input == nil || show.Input.Kind != gate.InputCheck || show.Input.Check == nil {
		t.Fatalf("show-input %+v", show)
	}
	guarded := newThing(t, k)
	moveTo(t, k, guarded.ID, "work")
	trig := dry(t, k, GateDryRequest{Workflow: "flow", Gate: "guarded", Item: guarded.ID, ShowInput: true})
	if trig.Input.Trigger == nil || trig.Input.Trigger.Skill != "probe" || trig.To != "plan" {
		t.Fatalf("trigger input %+v", trig.Input)
	}
	if n := len(dumpsOf(t, dumps)); n != 1 {
		t.Fatalf("dumps %d, want only the move's one", n)
	}
	moveTo(t, k, it.ID, "work")
	dump := dumpsOf(t, dumps)[1]
	for _, v := range show.Input.Check.Env {
		if !strings.Contains(dump, v+"\n") {
			t.Fatalf("env %q is not what the check got:\n%s", v, dump)
		}
	}
	if !strings.HasSuffix(dump, show.Input.Check.Stdin) {
		t.Fatalf("stdin %q is not what the check got:\n%s", show.Input.Check.Stdin, dump)
	}

	judged := newThing(t, k)
	moveTo(t, k, judged.ID, "plan")
	in := dry(t, k, GateDryRequest{Workflow: "flow", Gate: "review", Item: judged.ID, ShowInput: true}).Input
	if len(d.calls) != 0 || in.Kind != gate.InputJudge || len(in.Judges) != 1 || in.Judges[0] != "r1" {
		t.Fatalf("judge input %+v, calls %v", in, d.calls)
	}
	runsEmpty(t, k)
	moveTo(t, k, judged.ID, "work")
	if normalised(in.Prompt) != normalised(d.reqs[0].Prompt) {
		t.Fatalf("prompt is not what the judge got:\n--- shown\n%s\n--- judged\n%s", in.Prompt, d.reqs[0].Prompt)
	}
}

func TestGateDryShowInputListsUntrackedApart(t *testing.T) {
	k, d, _ := dryFixture(t)
	k.Git = fakeGit{changed: []string{"src/a.go"}, untracked: []string{"scratch.txt"}}
	it := newThing(t, k)
	moveTo(t, k, it.ID, "plan")
	in := dry(t, k, GateDryRequest{Workflow: "flow", Gate: "review", Item: it.ID, ShowInput: true}).Input
	if len(d.calls) != 0 || in == nil || in.Kind != gate.InputJudge {
		t.Fatalf("judge input %+v, calls %v", in, d.calls)
	}
	const header = "Untracked files in the worktree (not in the item's change):\n"
	c, u := strings.Index(in.Prompt, "Changed paths:\nsrc/a.go\n"), strings.Index(in.Prompt, header)
	if c < 0 || u < c || strings.Contains(in.Prompt[c:u], "scratch.txt") {
		t.Fatalf("the changed block is not the item's change only:\n%s", in.Prompt)
	}
	if !strings.HasPrefix(in.Prompt[u+len(header):], "scratch.txt\n") {
		t.Fatalf("the untracked file is not under its block:\n%s", in.Prompt)
	}
}

func TestGateDryJudgesAnUnpresentedPlan(t *testing.T) {
	k, d, _ := dryFixture(t)
	seen := watch(d)
	it := newThing(t, k)
	moveTo(t, k, it.ID, "plan")
	start, _ := chain(t, k, it.ID)
	mustAttach(t, k, it.ID, "plan", "draft one\n")
	dry(t, k, GateDryRequest{Workflow: "flow", Gate: "review", Item: it.ID})
	mustAttach(t, k, it.ID, "plan", "draft two\n")
	dry(t, k, GateDryRequest{Workflow: "flow", Gate: "review", Item: it.ID})
	if len(d.reqs) != 2 || !strings.Contains(d.reqs[0].Prompt, "attachments/plan") {
		t.Fatalf("prompts %d", len(d.reqs))
	}
	if (*seen)[0].files["attachments/plan"] != "draft one\n" || (*seen)[1].files["attachments/plan"] != "draft two\n" {
		t.Fatalf("seen %+v", *seen)
	}
	events, _ := k.Ledger.Read(it.ID)
	added := events[start:]
	if len(added) != 2 || !allKind(added, "attachment.added") {
		t.Fatalf("events since the start: %d", len(added))
	}
}

func allKind(events []ledger.Event, kind string) bool {
	for _, e := range events {
		if e.Kind != kind {
			return false
		}
	}
	return true
}

type liveDispatcher struct {
	mu      sync.Mutex
	answers map[string]string
	calls   []string
	on      func(req agent.Request)
}

func (d *liveDispatcher) Dispatch(_ context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	d.mu.Lock()
	d.calls = append(d.calls, req.Binding.Name)
	text := d.answers[req.Binding.Name]
	on := d.on
	d.mu.Unlock()
	if on != nil {
		on(req)
	}
	return agent.Result{Text: text, TokensIn: 1, TokensOut: 1}, nil
}

func (d *liveDispatcher) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.calls)
}

func recFixture(t *testing.T, transition string, extra string) (*Kernel, *liveDispatcher, string, string) {
	t.Helper()
	repo := t.TempDir()
	root := filepath.Join(repo, ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"flow\"\n[items]\nprefix = { thing = \"th\" }\n")
	agents := ""
	gates := ""
	for _, n := range []string{"first", "second", "third", "fourth", "g"} {
		agents += "[agents.r" + n + "]\ncommand = \"claude\"\nmodel = \"m" + n + "\"\ntools = [\"Read\"]\n"
		gates += "[gates." + n + "]\nskill = \"review\"\nagent = \"r" + n + "\"\n"
	}
	write(t, filepath.Join(root, "agents.toml"), agents)
	write(t, filepath.Join(root, "skills", "review.md"), "Review the change.\n")
	write(t, filepath.Join(root, "skills", "mark1.md"), "---\ntype: check\n---\n```sh\ntouch c1.marker\n```\n")
	write(t, filepath.Join(root, "skills", "mark2.md"), "---\ntype: check\n---\n```sh\ntouch c2.marker\n```\n")
	write(t, filepath.Join(root, "workflows", "flow.toml"), `
[states.a]
start = true
`+extra+`
[states.held]
park = true
resumes = true
[states.z]
terminal = true
`+gates+`
[gates.c1]
skill = "mark1"
[gates.c2]
skill = "mark2"

[[transition]]
from = ["a"]
to = "z"
`+transition+`
[[transition]]
from = ["*"]
to = "held"
[[transition]]
from = ["held"]
to = "@prior"
`)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d := &liveDispatcher{answers: map[string]string{}}
	for _, n := range []string{"first", "second", "third", "fourth", "g"} {
		d.answers["r"+n] = accept
	}
	k.Dispatcher = d
	it, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	return k, d, it.ID, repo
}

type startRec struct {
	Transition, Gate, Attempt string
	Seq                       int
}

func gateEvents(t *testing.T, events []ledger.Event) (starts []startRec, runs []gate.Result) {
	t.Helper()
	for _, e := range events {
		switch e.Kind {
		case gate.KindStart:
			var s gate.Start
			if err := e.Decode(&s); err != nil {
				t.Fatal(err)
			}
			starts = append(starts, startRec{s.Transition, s.Gate, s.Attempt, s.Seq})
		case gate.KindRun:
			var r gate.Result
			if err := e.Decode(&r); err != nil {
				t.Fatal(err)
			}
			runs = append(runs, r)
		}
	}
	return starts, runs
}

type progressCount struct {
	mu  sync.Mutex
	ids []string
}

func (p *progressCount) hook(id string) { p.mu.Lock(); p.ids = append(p.ids, id); p.mu.Unlock() }
func (p *progressCount) n() int         { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) }

func TestGatesRecordedAsTheyRun(t *testing.T) {
	k, d, id, _ := recFixture(t, `groups = [{ mode = "sequential", gates = ["first", "second"] }, { mode = "parallel", gates = ["third", "fourth"] }]`, "")
	var p progressCount
	k.Progress = p.hook
	var mu sync.Mutex
	seen := map[string][]ledger.Event{}
	d.on = func(req agent.Request) {
		ev, err := k.Ledger.Read(id)
		if err != nil {
			t.Error(err)
		}
		mu.Lock()
		seen[req.Binding.Name] = ev
		mu.Unlock()
	}
	res, err := k.Transition(context.Background(), id, "z", "me")
	if err != nil || !res.Moved {
		t.Fatalf("transition: %v moved=%v", err, res.Moved)
	}
	seqOf := map[string]int{"rfirst": 1, "rsecond": 2, "rthird": 3, "rfourth": 4}
	for b, seq := range seqOf {
		starts, runs := gateEvents(t, seen[b])
		own := slices.ContainsFunc(starts, func(s startRec) bool {
			return s.Seq == seq && s.Gate == strings.TrimPrefix(b, "r") && s.Transition == "a->z" && s.Attempt != ""
		})
		if !own {
			t.Errorf("%s's judge did not see its own gate.start (seq %d): %+v", b, seq, starts)
		}
		if slices.ContainsFunc(runs, func(r gate.Result) bool { return r.Seq == seq }) {
			t.Errorf("%s's judge saw its own gate.run before it answered", b)
		}
	}
	if _, runs := gateEvents(t, seen["rsecond"]); !slices.ContainsFunc(runs, func(r gate.Result) bool { return r.Gate == "first" && r.Seq == 1 }) {
		t.Errorf("the second sequential judge did not see the first gate's gate.run: %+v", runs)
	}
	all, _ := k.Ledger.Read(id)
	starts, runs := gateEvents(t, all)
	if len(starts) != 4 || len(runs) != 4 {
		t.Fatalf("%d starts and %d runs, want 4 and 4", len(starts), len(runs))
	}
	at := starts[0].Attempt
	for _, r := range runs {
		i := slices.IndexFunc(all, func(e ledger.Event) bool {
			var s gate.Start
			return e.Kind == gate.KindStart && e.Decode(&s) == nil && s.Seq == r.Seq && s.Gate == r.Gate && s.Attempt == r.Attempt
		})
		j := slices.IndexFunc(all, func(e ledger.Event) bool {
			var x gate.Result
			return e.Kind == gate.KindRun && e.Decode(&x) == nil && x.Seq == r.Seq
		})
		if i < 0 || i > j || r.Attempt != at {
			t.Errorf("gate.run %s seq %d: start at %d, run at %d, attempt %s (want %s)", r.Gate, r.Seq, i, j, r.Attempt, at)
		}
	}
	if p.n() != 9 || !slices.Equal(slices.Compact(slices.Clone(p.ids)), []string{id}) {
		t.Errorf("Progress called %d times with %v, want 9 times with %s", p.n(), p.ids, id)
	}

	k2, d2, id2, _ := recFixture(t, `groups = [{ mode = "sequential", gates = ["first", "second"] }]`, "")
	d2.answers["rfirst"] = reject
	var p2 progressCount
	k2.Progress = p2.hook
	if res, err := k2.Transition(context.Background(), id2, "z", "me"); err != nil || res.Moved {
		t.Fatalf("rejecting transition: %v moved=%v", err, res.Moved)
	}
	ev2, _ := k2.Ledger.Read(id2)
	if starts, _ := gateEvents(t, ev2); len(starts) != 1 || p2.n() != 2 {
		t.Errorf("after the first gate rejects: starts %+v, Progress %d; want one start and 2 calls", starts, p2.n())
	}
}

func TestSameNamedParallelGateRecordsItsOwnResult(t *testing.T) {
	k, d, id, _ := recFixture(t, `groups = [{ mode = "parallel", gates = ["g", "g"] }]`, "")
	var mu sync.Mutex
	held := false
	var after []ledger.Event
	d.on = func(req agent.Request) {
		mu.Lock()
		first := !held
		held = true
		mu.Unlock()
		if !first {
			return
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			ev, _ := k.Ledger.Read(id)
			if _, runs := gateEvents(t, ev); len(runs) > 0 {
				mu.Lock()
				after = ev
				mu.Unlock()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Error("the other g never recorded its gate.run while this one was held")
	}
	if res, err := k.Transition(context.Background(), id, "z", "me"); err != nil || !res.Moved {
		t.Fatalf("transition: %v moved=%v", err, res.Moved)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, runs := gateEvents(t, after); len(runs) != 1 || runs[0].Gate != "g" {
		t.Errorf("the held judge saw %d gate.run events, want exactly one for g", len(runs))
	}
}

func TestRefusedWriteRunsNoGate(t *testing.T) {
	k, d, id, _ := recFixture(t, `groups = [{ mode = "sequential", gates = ["first", "second"] }]`, "")
	k.Ledger.SetReadOnly("test")
	if _, err := k.Transition(context.Background(), id, "z", "me"); err == nil {
		t.Fatal("a read-only ledger did not refuse the transition")
	}
	k.Ledger.SetReadOnly("")
	ev, _ := k.Ledger.Read(id)
	if starts, runs := gateEvents(t, ev); len(starts)+len(runs) != 0 || len(d.called()) != 0 {
		t.Errorf("read-only before: %d starts, %d runs, judges %v; want none", len(starts), len(runs), d.called())
	}

	d.on = func(req agent.Request) {
		if req.Binding.Name == "rfirst" {
			k.Ledger.SetReadOnly("test")
		}
	}
	res, err := k.Transition(context.Background(), id, "z", "me")
	k.Ledger.SetReadOnly("")
	if err == nil || res.Moved {
		t.Fatalf("read-only mid-run: %v moved=%v", err, res.Moved)
	}
	if slices.Contains(d.called(), "rsecond") {
		t.Error("the second gate's judge ran after the ledger refused a write")
	}
	ev, _ = k.Ledger.Read(id)
	if starts, _ := gateEvents(t, ev); slices.ContainsFunc(starts, func(s startRec) bool { return s.Seq == 2 }) {
		t.Errorf("a gate.start for the second gate is on the ledger: %+v", starts)
	}
}

func TestUnreadableLedgerRunsNoFurtherGate(t *testing.T) {
	k, d, id, _ := recFixture(t, `groups = [{ mode = "sequential", gates = ["first", "second"] }]`, "")
	var p progressCount
	k.Progress = p.hook
	path := k.Ledger.Path(id)
	aside := path + ".aside"
	var calls int
	d.on = func(req agent.Request) {
		if req.Binding.Name != "rfirst" {
			return
		}
		if err := os.Rename(path, aside); err != nil {
			t.Error(err)
		}
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Error(err)
		}
		if _, err := k.Ledger.Read(id); err == nil {
			t.Error("the ledger still reads with a directory at its path")
		}
		calls = p.n()
	}
	if _, err := k.Transition(context.Background(), id, "z", "me"); err == nil {
		t.Fatal("an unreadable ledger did not refuse the transition")
	}
	if slices.Contains(d.called(), "rsecond") || p.n() != calls {
		t.Errorf("judges %v, Progress %d after %d: the second gate must not run and nothing more is pushed", d.called(), p.n(), calls)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(aside, path); err != nil {
		t.Fatal(err)
	}
	ev, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if starts, _ := gateEvents(t, ev); slices.ContainsFunc(starts, func(s startRec) bool { return s.Seq == 2 }) {
		t.Errorf("a gate.start for the second gate is on the ledger: %+v", starts)
	}
}

func TestStartOnTheLedgerRunsAfterAFailedCacheSync(t *testing.T) {
	k, _, id, repo := recFixture(t, `groups = [{ mode = "sequential", gates = ["c1", "c2"] }]`, "")
	var p progressCount
	k.Progress = p.hook
	k.Cache.Close()
	res, err := k.Transition(context.Background(), id, "z", "me")
	if err == nil || res.Moved {
		t.Fatalf("a failed cache sync: %v moved=%v, want an error and no move", err, res.Moved)
	}
	ev, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	starts, runs := gateEvents(t, ev)
	if len(starts) != 1 || starts[0].Gate != "c1" || starts[0].Seq != 1 || len(runs) != 1 || runs[0].Gate != "c1" || runs[0].Seq != 1 || runs[0].Attempt != starts[0].Attempt {
		t.Errorf("starts %+v runs %+v: want c1's start and result, seq 1, one attempt", starts, runs)
	}
	if _, err := os.Stat(filepath.Join(repo, "c1.marker")); err != nil {
		t.Errorf("c1, whose start is on the ledger, did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "c2.marker")); err == nil {
		t.Error("c2 ran after an append error")
	}
	if p.n() != 2 {
		t.Errorf("Progress called %d times, want 2", p.n())
	}
}

func TestRecorderStopsStartsButRecordsResults(t *testing.T) {
	k, _, id, _ := recFixture(t, `groups = [{ mode = "sequential", gates = ["first"] }]`, "")
	rc := &recorder{k: k, id: id, actor: "me", key: "a->z", at: "att_1"}
	if err := rc.start(1, "first"); err != nil {
		t.Fatal(err)
	}
	k.Ledger.SetReadOnly("test")
	if err := rc.start(2, "second"); err == nil {
		t.Fatal("a refused start returned nil")
	}
	k.Ledger.SetReadOnly("")
	rc.finish(1, gate.Result{Transition: "a->z", Gate: "first", Result: gate.Accept})
	if err := rc.start(3, "third"); err == nil {
		t.Error("a start after an error returned nil")
	}
	ev, _ := k.Ledger.Read(id)
	starts, runs := gateEvents(t, ev)
	if len(starts) != 1 || starts[0].Seq != 1 || len(runs) != 1 || runs[0].Attempt != "att_1" || runs[0].Seq != 1 {
		t.Errorf("starts %+v runs %+v: want only seq 1's start and result", starts, runs)
	}

	k2, _, id2, _ := recFixture(t, `groups = [{ mode = "sequential", gates = ["first"] }]`, "")
	k2.Cache.Close()
	rc2 := &recorder{k: k2, id: id2, actor: "me", key: "a->z", at: "att_2"}
	if err := rc2.start(1, "first"); err != nil {
		t.Errorf("a start on the ledger with a failed cache sync returned %v", err)
	}
	rc2.finish(1, gate.Result{Transition: "a->z", Gate: "first", Result: gate.Accept})
	if err := rc2.start(2, "second"); err == nil {
		t.Error("a start after a failed sync returned nil")
	}
	ev2, _ := k2.Ledger.Read(id2)
	if starts, runs := gateEvents(t, ev2); len(starts) != 1 || len(runs) != 1 {
		t.Errorf("closed cache: starts %+v runs %+v, want one of each", starts, runs)
	}
}

func runEvent(t *testing.T, r gate.Result) ledger.Event {
	t.Helper()
	e, err := ledger.NewEventWith("e", time.Now(), "th_1", gate.KindRun, "me", r, "")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestGateRunsReadsDeclaredOrder(t *testing.T) {
	res := func(g, at string, seq int) gate.Result {
		return gate.Result{Transition: "a->z", Gate: g, Result: gate.Accept, Attempt: at, Seq: seq}
	}
	names := func(rs []gate.Result) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Gate+r.Attempt)
		}
		return strings.Join(out, " ")
	}
	start, _ := ledger.NewEventWith("s", time.Now(), "th_1", gate.KindStart, "me", gate.Start{Transition: "a->z", Gate: "x", Attempt: "A", Seq: 9}, "")
	for _, c := range []struct {
		name string
		in   []gate.Result
		want string
	}{
		{"by seq", []gate.Result{res("h", "A", 2), res("g", "A", 1), res("g", "A", 3)}, "gA hA gA"},
		{"no seq", []gate.Result{res("h", "A", 0), res("g", "A", 0)}, "hA gA"},
		{"one without seq", []gate.Result{res("h", "A", 2), res("g", "A", 0), res("k", "A", 1)}, "hA gA kA"},
		{"attempts apart", []gate.Result{res("b", "A", 2), res("a", "A", 1), res("d", "B", 2), res("c", "B", 1)}, "aA bA cB dB"},
		{"no attempt keeps its place", []gate.Result{res("b", "A", 2), res("x", "", 0), res("a", "A", 1)}, "bA x aA"},
	} {
		var ev []ledger.Event
		for i, r := range c.in {
			ev = append(ev, runEvent(t, r))
			if i == 0 {
				ev = append(ev, start)
			}
		}
		if got := names(GateRuns(ev)); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestGateRunsKeepsCarry(t *testing.T) {
	w := &workflow.Workflow{
		Name:   "w",
		States: map[string]workflow.State{"a": {Start: true}, "b": {}, "z": {Terminal: true}},
		Gates:  map[string]workflow.Gate{"g": {Skill: "review", Agent: "r1", Carry: true}},
	}
	tr := &workflow.Transition{From: []string{"a"}, To: "z", Groups: []workflow.Group{{Mode: workflow.Sequential, Gates: []string{"g"}}}}
	tAccept := time.Date(2026, 11, 7, 12, 34, 56, 0, time.UTC)
	tReject := tAccept.Add(-time.Minute)
	recorded := []gate.Result{
		{Transition: "a->b", Gate: "g", Binding: "r1", Result: gate.Accept, Coverage: "cov", Attempt: "A", Seq: 2},
		{Transition: "a->b", Gate: "g", Binding: "r1", Result: gate.Reject, Coverage: "cov", Attempt: "A", Seq: 1},
	}
	evAccept, err := ledger.NewEventWith("e1", tAccept, "th_1", gate.KindRun, "me", recorded[0], "")
	if err != nil {
		t.Fatal(err)
	}
	evReject, err := ledger.NewEventWith("e2", tReject, "th_1", gate.KindRun, "me", recorded[1], "")
	if err != nil {
		t.Fatal(err)
	}
	ev := []ledger.Event{evAccept, evReject}
	runs := GateRuns(ev)
	if len(runs) != 2 || !runs[0].At.Equal(tReject) || !runs[1].At.Equal(tAccept) {
		t.Fatalf("GateRuns timestamps = %v %v, want %v %v", runs[0].At, runs[1].At, tReject, tAccept)
	}
	declared := []gate.Result{
		{Transition: "a->b", Gate: "g", Binding: "r1", Result: gate.Reject, Coverage: "cov", At: tReject},
		{Transition: "a->b", Gate: "g", Binding: "r1", Result: gate.Accept, Coverage: "cov", At: tAccept},
	}
	carriedOver := func(from, to string, prior []gate.Result) (gate.Result, int) {
		d := &fakeDispatcher{answers: map[string]string{"r1": accept}}
		run := gate.Run{
			Item: item.Item{ID: "th_1"}, From: from, To: to, Transition: tr, Workflow: w,
			Skills:     map[string]workflow.Skill{"review": {Name: "review", Meta: map[string]string{}, Body: "r"}},
			Agents:     agent.Catalog{"r1": agent.Binding{Name: "r1", Command: "claude", Model: "m", Tools: []string{"Read"}}},
			Dispatcher: d, Prior: prior,
			Coverage: func([]string) (string, string, error) { return "cov", "chg", nil },
		}
		r := run.Gate(context.Background(), "g")
		return r, len(d.calls)
	}
	if r, n := carriedOver("a", "b", recorded); r.Result == gate.Carried || n != 1 {
		t.Errorf("over recorded order: result %s, judges %d; want not carried and 1", r.Result, n)
	}
	if r, n := carriedOver("b", "z", runs); r.Result != gate.Carried || n != 0 || r.CarriedFrom == nil || r.CarriedFrom.Transition != "a->b" || !r.CarriedFrom.At.Equal(tAccept) || r.Notes != "reuses accept on a->b at "+tAccept.Format(time.RFC3339) {
		t.Errorf("over GateRuns across transitions: result %+v, judges %d", r, n)
	}
	if r, n := carriedOver("b", "z", declared); r.Result != gate.Carried || n != 0 || r.CarriedFrom == nil || r.CarriedFrom.Transition != "a->b" || !r.CarriedFrom.At.Equal(tAccept) {
		t.Errorf("over declared order: result %+v, judges %d", r, n)
	}
}

func TestHoldKeepsDeclaredOrder(t *testing.T) {
	k, d, id, _ := recFixture(t, `groups = [{ mode = "parallel", gates = ["first", "second", "third"] }]`, "hold = { rounds = 2, to = \"held\" }")
	d.answers["rthird"] = reject
	d.on = func(req agent.Request) {
		if req.Binding.Name != "rfirst" {
			return
		}
		ev, _ := k.Ledger.Read(id)
		starts, _ := gateEvents(t, ev)
		at := ""
		for _, s := range starts {
			if s.Gate == "first" {
				at = s.Attempt
			}
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			ev, _ := k.Ledger.Read(id)
			if _, runs := gateEvents(t, ev); slices.ContainsFunc(runs, func(r gate.Result) bool { return r.Gate == "second" && r.Attempt == at }) {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Error("second's gate.run never landed while first was held")
	}
	ctx := context.Background()
	if _, err := k.Transition(ctx, id, "z", "me"); err != nil {
		t.Fatal(err)
	}
	res, err := k.Transition(ctx, id, "z", "me")
	if err != nil || res.Held == nil {
		t.Fatalf("round 2: %v held=%+v", err, res.Held)
	}
	ev, _ := k.Ledger.Read(id)
	_, runs := gateEvents(t, ev)
	last := runs[len(runs)-1].Attempt
	var order []string
	for _, r := range runs {
		if r.Attempt == last {
			order = append(order, r.Gate)
		}
	}
	if slices.Index(order, "second") > slices.Index(order, "first") {
		t.Fatalf("the fixture did not force completion order: %v", order)
	}
	want := []string{item.VerdictKey("first", "rfirst"), item.VerdictKey("second", "rsecond")}
	if !slices.Equal(res.Held.Accepted, want) {
		t.Errorf("Hold.Accepted %v, want declared order %v", res.Held.Accepted, want)
	}
	h, err := k.HoldOf(id)
	if err != nil || !reflect.DeepEqual(h, res.Held) {
		t.Errorf("HoldOf %+v (%v), want %+v", h, err, res.Held)
	}
	if m := lastMove(t, k, id); !reflect.DeepEqual(m.Hold, res.Held) {
		t.Errorf("the last move's hold %+v, want %+v", m.Hold, res.Held)
	}
}
