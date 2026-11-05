package gate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		pat, path string
		want      bool
	}{
		{"**/*.go", "a.go", true},
		{"**/*.go", "x/y/a.go", true},
		{"**/*.go", "x/y/a.txt", false},
		{"*.go", "x/a.go", false},
		{"internal/**", "internal/a/b", true},
		{"internal/**", "cmd/a", false},
		{"a/**/b/*.md", "a/b/c.md", true},
		{"a/**/b/*.md", "a/x/y/b/c.md", true},
		{"docs/[ab].md", "docs/a.md", true},
	} {
		if got := Match(c.pat, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.pat, c.path, got)
		}
	}
}

func TestParseVerdict(t *testing.T) {
	v := ParseVerdict("thinking {\"verdict\":\"reject\",\"notes\":\"first\"} then {\"x\":1} and finally {\"verdict\":\"accept\",\"notes\":\"ok\"} done")
	if v.Verdict != Accept || v.Notes != "ok" {
		t.Fatalf("%+v", v)
	}
	v = ParseVerdict("no json here")
	if v.Verdict != Reject || !strings.Contains(v.Notes, "no json here") {
		t.Fatalf("%+v", v)
	}
	if v := ParseVerdict(`{"verdict":"maybe"}`); v.Verdict != Reject {
		t.Fatalf("%+v", v)
	}
}

// fakeDispatcher answers each binding with a canned text and counts calls.
type fakeDispatcher struct {
	mu      sync.Mutex
	answers map[string]string
	calls   []string
}

func (f *fakeDispatcher) Dispatch(_ context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if role != agent.Judge {
		return agent.Result{}, errors.New("not a judge")
	}
	f.calls = append(f.calls, req.Binding.Name)
	text, ok := f.answers[req.Binding.Name]
	if !ok {
		return agent.Result{}, errors.New("no such fake")
	}
	return agent.Result{Text: text, TokensIn: 10, TokensOut: 1}, nil
}

const yes, no = `{"verdict":"accept","notes":"fine"}`, `{"verdict":"reject","notes":"bad"}`

func setup(groups []workflow.Group, gates map[string]workflow.Gate) (Run, *fakeDispatcher) {
	d := &fakeDispatcher{answers: map[string]string{"y1": yes, "y2": yes, "n1": no, "n2": no}}
	agents := agent.Catalog{}
	for _, n := range []string{"y1", "y2", "n1", "n2"} {
		agents[n] = agent.Binding{Name: n, Command: "claude", Model: "model-" + n}
	}
	agents["doer"] = agent.Binding{Name: "doer", Command: "claude", Model: "model-y1"}
	wf := &workflow.Workflow{
		Name:   "w",
		States: map[string]workflow.State{"s0": {Start: true, Performer: &workflow.Performer{Agent: "doer", Skill: "judge"}}, "s1": {Terminal: true}},
		Gates:  gates,
	}
	tr := &workflow.Transition{From: []string{"s0"}, To: "s1", Groups: groups}
	return Run{
		Item: item.Item{ID: "i1", Title: "t"}, From: "s0", To: "s1", Transition: tr, Workflow: wf,
		Skills: map[string]workflow.Skill{"judge": {Name: "judge", Meta: map[string]string{}, Body: "judge it"}},
		Agents: agents, Dispatcher: d, Changed: []string{"a/b.go"},
		Coverage: func(paths []string) (string, error) { return "cov:" + strings.Join(paths, ","), nil },
	}, d
}

func results(rs []Result) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Gate+"="+r.Result)
	}
	return strings.Join(out, " ")
}

func TestSequentialFailFastAndParallel(t *testing.T) {
	gates := map[string]workflow.Gate{
		"ok": {Skill: "judge", Agent: "y1"}, "bad": {Skill: "judge", Agent: "n1"}, "ok2": {Skill: "judge", Agent: "y2"},
	}
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"ok", "bad", "ok2"}}}, gates)
	rs, pass := r.Execute(context.Background())
	if pass || results(rs) != "ok=accept bad=reject" || len(d.calls) != 2 {
		t.Fatalf("sequential: %v %s %v", pass, results(rs), d.calls)
	}
	if rs[0].Transition != "s0->s1" || rs[0].Binding != "y1" || rs[0].TokensIn != 10 || rs[0].Coverage != "cov:" {
		t.Fatalf("result fields %+v", rs[0])
	}
	r, d = setup([]workflow.Group{
		{Mode: workflow.Parallel, Gates: []string{"bad", "ok", "ok2"}},
		{Mode: workflow.Sequential, Gates: []string{"ok"}},
	}, gates)
	rs, pass = r.Execute(context.Background())
	if pass || results(rs) != "bad=reject ok=accept ok2=accept" || len(d.calls) != 3 {
		t.Fatalf("parallel: %v %s %v", pass, results(rs), d.calls)
	}
	r, _ = setup([]workflow.Group{{Mode: workflow.Parallel, Gates: []string{"ok", "ok2"}}, {Mode: workflow.Sequential, Gates: []string{"ok"}}}, gates)
	if rs, pass = r.Execute(context.Background()); !pass || len(rs) != 3 {
		t.Fatalf("all accept: %v %s", pass, results(rs))
	}
}

func TestSkippedAndCarried(t *testing.T) {
	gates := map[string]workflow.Gate{
		"docs":  {Skill: "judge", Agent: "n1", Paths: []string{"docs/**"}},
		"code":  {Skill: "judge", Agent: "y1", Paths: []string{"**/*.go"}, Carry: true},
		"fresh": {Skill: "judge", Agent: "y2"},
	}
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"docs", "code", "fresh"}}}, gates)
	r.Prior = []Result{
		{Transition: "s0->s1", Gate: "code", Result: Accept, Coverage: "cov:**/*.go"},
		{Transition: "s0->s1", Gate: "fresh", Result: Accept, Coverage: "cov:"},
	}
	rs, pass := r.Execute(context.Background())
	if !pass || results(rs) != "docs=skipped code=carried fresh=accept" || len(d.calls) != 1 {
		t.Fatalf("%v %s %v", pass, results(rs), d.calls)
	}
	// A changed coverage runs the gate again.
	r.Prior[0].Coverage = "older"
	rs, _ = r.Execute(context.Background())
	if results(rs) != "docs=skipped code=accept fresh=accept" {
		t.Fatalf("%s", results(rs))
	}
	// A later reject is not carried past.
	r.Prior = append(r.Prior, Result{Transition: "s0->s1", Gate: "code", Result: Reject, Coverage: "cov:**/*.go"})
	if rs, _ = r.Execute(context.Background()); rs[1].Result != Accept {
		t.Fatalf("%s", results(rs))
	}
}

func TestPanels(t *testing.T) {
	for _, c := range []struct {
		combine string
		panel   []string
		want    string
	}{
		{workflow.CombineAll, []string{"y1", "y2"}, Accept},
		{workflow.CombineAll, []string{"y1", "n1"}, Reject},
		{workflow.CombineAny, []string{"n1", "y1"}, Accept},
		{workflow.CombineAny, []string{"n1", "n2"}, Reject},
		{workflow.CombineMost, []string{"y1", "y2", "n1"}, Accept},
		{workflow.CombineMost, []string{"y1", "n1", "n2"}, Reject},
	} {
		r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"p"}}},
			map[string]workflow.Gate{"p": {Skill: "judge", Panel: c.panel, Combine: c.combine}})
		rs, _ := r.Execute(context.Background())
		if rs[0].Result != c.want || rs[0].Binding != strings.Join(c.panel, ",") || rs[0].TokensIn != 10*len(c.panel) {
			t.Fatalf("%s %v: %+v", c.combine, c.panel, rs[0])
		}
	}
}

func TestReviewerDiffersFromPerformer(t *testing.T) {
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"g"}}}, map[string]workflow.Gate{"g": {Skill: "judge", Agent: "y1"}})
	r.ReviewerDiffers = true
	rs, pass := r.Execute(context.Background())
	if pass || !strings.Contains(rs[0].Notes, "refused") || len(d.calls) != 0 {
		t.Fatalf("%v %+v", pass, rs[0])
	}
	r.ReviewerDiffers = false
	if _, pass = r.Execute(context.Background()); !pass {
		t.Fatal("rule off still refused")
	}
}
