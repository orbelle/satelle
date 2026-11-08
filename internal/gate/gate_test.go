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

// A relevance skip reports an empty diff, or one that lost paths of the item's
// last judged diff; a judged result records the diff it saw.
func TestRelevanceSkipReportsAnEmptyOrShrunkDiff(t *testing.T) {
	gates := map[string]workflow.Gate{"code": {Skill: "judge", Agent: "y1", Paths: []string{"**/*.go"}}, "all": {Skill: "judge", Agent: "y1"}}
	run := func(changed []string, prior ...Result) []Result {
		r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"code", "all"}}}, gates)
		r.Item.Kind, r.Changed, r.Prior, r.Base = "story", changed, prior, "0123456789abcdef"
		rs, _ := r.Execute(context.Background())
		return rs
	}
	judged := Result{Transition: "a->b", Gate: "all", Result: Accept, Coverage: "c", Changed: []string{"a.go", "b.md"}}

	// (a) an empty diff
	if rs := run(nil); rs[0].Result != Skipped || !strings.Contains(rs[0].Warning, "diff is empty") || !strings.Contains(rs[0].Warning, "satelle story diff i1") {
		t.Errorf("empty diff: %+v", rs[0])
	}
	// (b) a diff that lost a.go and b.md
	if rs := run([]string{"c.md"}, judged); !strings.Contains(rs[0].Warning, "shrunk") || !strings.Contains(rs[0].Warning, "a.go, b.md") || !strings.Contains(rs[0].Warning, "all on a->b") {
		t.Errorf("shrunk diff: %+v", rs[0])
	}
	// (c) a superset of the last judged diff
	if rs := run([]string{"a.go", "b.md", "c.md"}, judged); rs[0].Result != Accept || rs[0].Warning != "" {
		t.Errorf("superset: %+v", rs[0])
	}
	if rs := run([]string{"b.md", "c.md"}, Result{Transition: "a->b", Gate: "all", Result: Accept, Coverage: "c", Changed: []string{"b.md"}}); rs[0].Result != Skipped || rs[0].Warning != "" {
		t.Errorf("a skip on a diff holding everything last judged warned: %+v", rs[0])
	}
	// (d) skipped and carried results are not a reference
	newer := []Result{judged, {Gate: "all", Result: Skipped, Changed: []string{"x.md"}}, {Gate: "all", Result: Carried, Coverage: "c", Changed: []string{"y.md"}}}
	if rs := run([]string{"c.md"}, newer...); !strings.Contains(rs[0].Warning, "a.go, b.md") {
		t.Errorf("the reference was not the last judged diff: %+v", rs[0])
	}
	// (f) a judged accept records the diff it saw; a skip does not
	rs := run([]string{"c.md"}, judged)
	if rs[0].Changed != nil || strings.Join(rs[1].Changed, ",") != "c.md" {
		t.Errorf("recorded diffs: skip %v, accept %v", rs[0].Changed, rs[1].Changed)
	}
}

// (e) A trigger skip carries the warning too.
func TestTriggerSkipReportsAnEmptyDiff(t *testing.T) {
	r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"t"}}}, map[string]workflow.Gate{"t": {Skill: "judge", Agent: "y1", Trigger: "never"}})
	r.Skills["never"] = workflow.Skill{Name: "never", Meta: map[string]string{"type": "check"}, Script: "exit 1"}
	r.Changed, r.Dir = nil, t.TempDir()
	rs, _ := r.Execute(context.Background())
	if rs[0].Result != Skipped || !strings.Contains(rs[0].Warning, "diff is empty") {
		t.Errorf("trigger skip: %+v", rs[0])
	}
}

func TestVerdictClassAndPanelMembers(t *testing.T) {
	v, ok := parseVerdict(`notes first {"verdict":"reject","notes":"gap","class":"AC3"}`)
	if !ok || v.Verdict != Reject || v.Class != "AC3" {
		t.Fatalf("class not read: %+v %v", v, ok)
	}
	if v := ParseVerdict(`{"verdict":"accept","notes":"ok"}`); v.Class != "" {
		t.Fatalf("class without one given: %+v", v)
	}
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"p"}}},
		map[string]workflow.Gate{"p": {Skill: "judge", Panel: []string{"y1", "n1"}, Combine: workflow.CombineAll}, "one": {Skill: "judge", Agent: "n2"}})
	d.answers["n1"] = `{"verdict":"reject","notes":"bad","class":"AC3"}`
	d.answers["n2"] = `{"verdict":"reject","notes":"bad","class":"plan"}`
	rs, pass := r.Execute(context.Background())
	if pass || len(rs) != 1 {
		t.Fatalf("panel: %v %+v", pass, rs)
	}
	want := []Member{{Binding: "y1", Result: Accept}, {Binding: "n1", Result: Reject, Class: "AC3"}}
	if got := rs[0].Members; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("members %+v, want %+v", got, want)
	}
	r.Transition.Groups = []workflow.Group{{Mode: workflow.Sequential, Gates: []string{"one"}}}
	rs, _ = r.Execute(context.Background())
	if len(rs) != 1 || rs[0].Class != "plan" || rs[0].Binding != "n2" || len(rs[0].Members) != 0 {
		t.Fatalf("single judge class: %+v", rs)
	}
	if ToOf(TransitionKey("a", "b")) != "b" || FromOf(TransitionKey("a", "b")) != "a" {
		t.Fatal("ToOf/FromOf")
	}
}

func TestLegacyAnswerKey(t *testing.T) {
	for _, c := range []struct {
		text   string
		legacy bool
	}{
		{"## Verdict\n\n```json\n{\"decision\": \"accept\", \"notes\": \"\"}\n```\n", true},
		{"{\"decision\":\"reject\"}", true},
		{"`decision` is accept or reject.", true},
		{"## Verdict\n\n```json\n{\"verdict\": \"accept\", \"notes\": \"\"}\n```\n", false},
		{"Record the decision in notes; the decision is yours.", false},
	} {
		key, legacy := LegacyAnswerKey(c.text)
		if legacy != c.legacy || (legacy && key != "decision") {
			t.Errorf("LegacyAnswerKey(%q) = %q, %v; want legacy=%v", c.text, key, legacy, c.legacy)
		}
	}
}
