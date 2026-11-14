//go:build !windows

package kernel

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

// dryFixture is a kernel whose flow workflow has a check that dumps its input
// (probe), a single judge (review), a panel that rejects (panel), a check on
// two transitions from one state (pick), a gate with a trigger (guarded), and
// a hold cap of one round on work. dumps is where probe writes, one file per
// run.
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

// AC1: a check gets the env, stdin and working directory its transition
// gives it.
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
	// The dry run adds only the marker that makes a satelle the script starts
	// refuse to write; everything else is the transition's input.
	const marker = gate.DryRunEnv + "=1\n"
	if !strings.Contains(got[0], marker) || strings.Contains(got[1], marker) {
		t.Fatalf("the dry-run marker:\n--- dry\n%s\n--- transition\n%s", got[0], got[1])
	}
	if strings.Replace(got[0], marker, "", 1) != got[1] {
		t.Fatalf("the dry run's input differs from the transition's:\n--- dry\n%s\n--- transition\n%s", got[0], got[1])
	}
	if !strings.Contains(got[0], "SATELLE_ITEM="+it.ID) || !strings.Contains(got[0], `"from":"open"`) {
		t.Fatalf("dump:\n%s", got[0])
	}
}

// normalised replaces the record folder a prompt names with a fixed token.
func normalised(prompt string) string {
	if dir := folderOf(prompt); dir != "" {
		return strings.ReplaceAll(prompt, dir, "<record>")
	}
	return prompt
}

// AC2: a judge gets the same system, prompt and read-only record folder.
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

// AC3: a dry run that accepts, rejects or errors leaves the chain, the runs
// folder and the hold count as they were.
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

// AC4: the transition is the item's state and the one move that runs the
// gate, or what --from and --to name; anything else is refused naming the
// choices, with nothing run.
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

// AC6: --show-input reports what AC1 and AC2 capture and starts nothing.
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
		if v == gate.DryRunEnv+"=1" {
			continue
		}
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

// AC7: a draft plan attached to an item that has not been presented is what
// the dry-run judge reads.
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
