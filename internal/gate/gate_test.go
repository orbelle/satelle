package gate

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestParseVerdictInvalidEscapeInNotes(t *testing.T) {
	fenced := "The paths filter is right.\n\n```json\n{\"verdict\": \"accept\", \"notes\": \"trigger uses ^\\.azuredevops/ as asked\"}\n```\n"
	v, ok := parseVerdict(fenced)
	if !ok || v.Verdict != Accept || !strings.Contains(v.Notes, `^\.azuredevops/`) {
		t.Fatalf("fenced \\.: %+v %v", v, ok)
	}
	v, ok = parseVerdict(`{"verdict":"reject","notes":"writes to C:\Users\x"}`)
	if !ok || v.Verdict != Reject || v.Notes != `writes to C:\Users\x` {
		t.Fatalf("windows path: %+v %v", v, ok)
	}
	v, ok = parseVerdict(`an example: {"verdict":"reject","notes":"quoted"} and mine: {"verdict":"accept","notes":"matches \.go"}`)
	if !ok || v.Verdict != Accept || v.Notes != `matches \.go` {
		t.Fatalf("last object wins: %+v %v", v, ok)
	}
	v, ok = parseVerdict(`{"x":{"verdict":"accept","notes":"a \. b"}}`)
	valid, vok := parseVerdict(`{"x":{"verdict":"accept","notes":"a \\. b"}}`)
	if !ok || v.Verdict != Accept || v.Notes != `a \. b` || !vok || v != valid {
		t.Fatalf("nested: %+v %v, valid escape gives %+v %v", v, ok, valid, vok)
	}
}

func TestParseVerdictNeverRepairsVerdict(t *testing.T) {
	const prefix = `no {"verdict":"accept"|"reject","notes":"..."} object in the judge's answer: `
	for _, text := range []string{
		`{"verdict":"maybe","notes":"a \. b"}`,
		`{"verdict":"acc\ept","notes":"x"}`,
		`{"verdict":"acc\ept","notes":"a \. b"}`,
		`{"verd\ict":"accept","notes":"x"}`,
		`{"notes":"a \. b"}`,
		`{"x":{"verdict":"maybe","notes":"a \. b"}}`,
		`{"verdict":"accept","notes":"x","class":"A\C1"}`,
	} {
		v, ok := parseVerdict(text)
		if ok || v.Verdict != Reject || !strings.HasPrefix(v.Notes, prefix) {
			t.Errorf("%s: %+v %v, want the no-verdict reject", text, v, ok)
		}
	}
}

func TestParseVerdictValidEscapesUnchanged(t *testing.T) {
	text := `{"verdict":"accept","notes":"a\nb\tc\"d\\.e\/f\u00e9g\bh"}`
	var want Verdict
	if err := json.Unmarshal([]byte(text), &want); err != nil {
		t.Fatal(err)
	}
	v, ok := parseVerdict(text)
	if !ok || v != want || v.Notes != "a\nb\tc\"d\\.e/fég\bh" {
		t.Fatalf("%+v %v, want %+v", v, ok, want)
	}
	if v, ok := parseVerdict(`{"verdict":"\u0061ccept","notes":"x"}`); !ok || v.Verdict != Accept {
		t.Fatalf("escaped verdict: %+v %v", v, ok)
	}
}

func TestJudgedGateInvalidEscapeRecordsAccept(t *testing.T) {
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"ok"}}}, map[string]workflow.Gate{"ok": {Skill: "judge", Agent: "y1"}})
	d.answers["y1"] = "Looks right.\n\n```json\n{\"verdict\": \"accept\", \"notes\": \"trigger ^\\.azuredevops/ kept\"}\n```\n"
	rs, pass := r.Execute(context.Background())
	if !pass || len(rs) != 1 || rs[0].Result != Accept || !strings.Contains(rs[0].Notes, `^\.azuredevops/`) || strings.Contains(rs[0].Notes, "no {") {
		t.Fatalf("%v %+v", pass, rs)
	}
}

type fakeDispatcher struct {
	mu      sync.Mutex
	answers map[string]string
	usage   map[string]agent.Result
	calls   []string
	prompts map[string]string
}

func (f *fakeDispatcher) Dispatch(_ context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if role != agent.Judge {
		return agent.Result{}, errors.New("not a judge")
	}
	f.calls = append(f.calls, req.Binding.Name)
	if f.prompts == nil {
		f.prompts = map[string]string{}
	}
	f.prompts[req.Binding.Name] = req.Prompt
	text, ok := f.answers[req.Binding.Name]
	if !ok {
		return agent.Result{}, errors.New("no such fake")
	}
	if u, ok := f.usage[req.Binding.Name]; ok {
		return agent.Result{Text: text, TokensIn: u.TokensIn, TokensOut: u.TokensOut, Duration: u.Duration}, nil
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
		Coverage: func(paths []string) (string, string, error) {
			return "cov:" + strings.Join(paths, ","), "chg:" + strings.Join(paths, ","), nil
		},
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
	if rs[1].CarriedFrom == nil || rs[1].CarriedFrom.Transition != "s0->s1" || !rs[1].CarriedFrom.At.IsZero() || rs[1].Notes != "reuses accept on s0->s1" {
		t.Fatalf("carried provenance: %+v %q", rs[1].CarriedFrom, rs[1].Notes)
	}
	r.Prior[0].Coverage = "older"
	rs, _ = r.Execute(context.Background())
	if results(rs) != "docs=skipped code=accept fresh=accept" || rs[1].CarriedFrom != nil {
		t.Fatalf("%s %+v", results(rs), rs[1].CarriedFrom)
	}
	r.Prior = append(r.Prior, Result{Transition: "s0->s1", Gate: "code", Result: Reject, Coverage: "cov:**/*.go"})
	if rs, _ = r.Execute(context.Background()); rs[1].Result != Accept || rs[1].CarriedFrom != nil {
		t.Fatalf("%s %+v", results(rs), rs[1].CarriedFrom)
	}
}

func TestCarriedAcrossTransitions(t *testing.T) {
	t1 := time.Date(2026, 11, 7, 10, 11, 12, 0, time.UTC)
	t2 := t1.Add(5 * time.Minute)
	wantNote := "reuses accept on s0->s1 at " + t1.Format(time.RFC3339)

	gates := map[string]workflow.Gate{
		"code":  {Skill: "judge", Agent: "y1", Paths: []string{"**/*.go"}, Carry: true},
		"suite": {Skill: "fail-if-run", Carry: true},
	}
	mk := func(from, to string, prior ...Result) (Run, *fakeDispatcher) {
		r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"code", "suite"}}}, gates)
		r.Workflow.States["s2"] = workflow.State{}
		r.Workflow.States["s3"] = workflow.State{Terminal: true}
		r.Skills["fail-if-run"] = workflow.Skill{Name: "fail-if-run", Meta: map[string]string{"type": workflow.TypeCheck}, Script: "echo ran; exit 1"}
		r.Dir = t.TempDir()
		r.From, r.To = from, to
		r.Transition = &workflow.Transition{From: []string{from}, To: to, Groups: r.Transition.Groups}
		r.Prior = prior
		return r, d
	}

	r, d := mk("s1", "s2",
		Result{Transition: "s0->s1", Gate: "code", Result: Accept, Coverage: "cov:**/*.go", At: t1},
		Result{Transition: "s0->s1", Gate: "suite", Result: Accept, Coverage: "cov:", At: t1},
	)
	rs, pass := r.Execute(context.Background())
	if !pass || results(rs) != "code=carried suite=carried" || len(d.calls) != 0 {
		t.Fatalf("cross-transition carry: %v %s calls=%v", pass, results(rs), d.calls)
	}
	for _, g := range rs {
		if g.CarriedFrom == nil || g.CarriedFrom.Transition != "s0->s1" || !g.CarriedFrom.At.Equal(t1) || g.Notes != wantNote {
			t.Fatalf("%s carried provenance = %+v %q, want s0->s1 at %v", g.Gate, g.CarriedFrom, g.Notes, t1)
		}
		raw, err := json.Marshal(g)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if _, hasAt := doc["At"]; hasAt {
			t.Errorf("%s marshaled At field: %s", g.Gate, raw)
		}
		if _, hasAt := doc["at"]; hasAt {
			t.Errorf("%s marshaled top-level at field: %s", g.Gate, raw)
		}
	}

	r, d = mk("s2", "s3",
		Result{Transition: "s0->s1", Gate: "code", Result: Accept, Coverage: "cov:**/*.go", At: t1},
		Result{Transition: "s1->s2", Gate: "code", Result: Carried, Coverage: "cov:**/*.go", CarriedFrom: &CarriedFrom{Transition: "s0->s1", At: t1}, At: t2},
		Result{Transition: "s0->s1", Gate: "suite", Result: Accept, Coverage: "cov:", At: t1},
		Result{Transition: "s1->s2", Gate: "suite", Result: Carried, Coverage: "cov:", CarriedFrom: &CarriedFrom{Transition: "s0->s1", At: t1}, At: t2},
	)
	rs, pass = r.Execute(context.Background())
	if !pass || results(rs) != "code=carried suite=carried" || len(d.calls) != 0 {
		t.Fatalf("chained carry: %v %s calls=%v", pass, results(rs), d.calls)
	}
	for _, g := range rs {
		if g.CarriedFrom == nil || g.CarriedFrom.Transition != "s0->s1" || !g.CarriedFrom.At.Equal(t1) || g.Notes != wantNote {
			t.Fatalf("chained %s provenance = %+v %q, want s0->s1 at %v", g.Gate, g.CarriedFrom, g.Notes, t1)
		}
	}

	r, d = mk("s2", "s3",
		Result{Transition: "s0->s1", Gate: "code", Result: Accept, Coverage: "cov:**/*.go", At: t1},
		Result{Transition: "s1->s2", Gate: "code", Result: Skipped, At: t2},
		Result{Transition: "s0->s1", Gate: "suite", Result: Accept, Coverage: "cov:", At: t1},
		Result{Transition: "s1->s2", Gate: "suite", Result: Skipped, At: t2},
	)
	rs, pass = r.Execute(context.Background())
	if !pass || results(rs) != "code=carried suite=carried" || len(d.calls) != 0 {
		t.Fatalf("intervening skipped: %v %s calls=%v", pass, results(rs), d.calls)
	}
	for _, g := range rs {
		if g.CarriedFrom == nil || g.CarriedFrom.Transition != "s0->s1" || !g.CarriedFrom.At.Equal(t1) || g.Notes != wantNote {
			t.Fatalf("after skipped %s provenance = %+v %q, want s0->s1 at %v", g.Gate, g.CarriedFrom, g.Notes, t1)
		}
	}

	r, d = mk("s1", "s2",
		Result{Transition: "s0->s1", Gate: "code", Result: Accept, Coverage: "cov:old", At: t1},
	)
	r.Transition.Groups = []workflow.Group{{Mode: workflow.Sequential, Gates: []string{"code"}}}
	rs, pass = r.Execute(context.Background())
	if !pass || results(rs) != "code=accept" || len(d.calls) != 1 || rs[0].CarriedFrom != nil {
		t.Fatalf("changed coverage: %v %s calls=%v carried=%+v", pass, results(rs), d.calls, rs[0].CarriedFrom)
	}

	r, d = mk("s1", "s2",
		Result{Transition: "s0->s1", Gate: "code", Result: Accept, Coverage: "cov:**/*.go", At: t1},
		Result{Transition: "s1->s2", Gate: "code", Result: Accept, Coverage: "cov:other", At: t2},
	)
	r.Transition.Groups = []workflow.Group{{Mode: workflow.Sequential, Gates: []string{"code"}}}
	rs, pass = r.Execute(context.Background())
	if !pass || results(rs) != "code=accept" || len(d.calls) != 1 || rs[0].CarriedFrom != nil {
		t.Fatalf("reverted coverage: %v %s calls=%v carried=%+v", pass, results(rs), d.calls, rs[0].CarriedFrom)
	}

	for _, bad := range []string{Reject, Error} {
		r, d = mk("s1", "s2",
			Result{Transition: "s0->s1", Gate: "code", Result: Accept, Coverage: "cov:**/*.go", At: t1},
			Result{Transition: "s1->s2", Gate: "code", Result: bad, Coverage: "cov:**/*.go", At: t2},
		)
		r.Transition.Groups = []workflow.Group{{Mode: workflow.Sequential, Gates: []string{"code"}}}
		rs, pass = r.Execute(context.Background())
		if !pass || results(rs) != "code=accept" || len(d.calls) != 1 || rs[0].CarriedFrom != nil {
			t.Fatalf("after %s: %v %s calls=%v carried=%+v", bad, pass, results(rs), d.calls, rs[0].CarriedFrom)
		}
	}

	r, d = mk("s1", "s2",
		Result{Transition: "s0->s1", Gate: "code", Result: Accept, Coverage: "cov:**/*.go", Tier: "t1", At: t1},
	)
	r.Transition.Groups = []workflow.Group{{Mode: workflow.Sequential, Gates: []string{"code"}}}
	r.Review = workflow.Review{TierTag: "k", Panels: map[string]map[string][]string{"code": {"t1": {"y1"}, "t2": {"y2"}}}}
	r.Item.Tags = []string{"k:t2"}
	rs, pass = r.Execute(context.Background())
	if !pass || results(rs) != "code=accept" || !sameSet(d.calls, []string{"y2"}) || rs[0].Tier != "t2" || rs[0].CarriedFrom != nil {
		t.Fatalf("tier change across transitions: %v %s calls=%v tier=%q carried=%+v", pass, results(rs), d.calls, rs[0].Tier, rs[0].CarriedFrom)
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

func TestJudgeOnThePerformersModelIsDispatched(t *testing.T) {
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"g"}}}, map[string]workflow.Gate{"g": {Skill: "judge", Agent: "y1"}})
	rs, pass := r.Execute(context.Background())
	if !pass || len(d.calls) != 1 || strings.Contains(rs[0].Notes, "refused") {
		t.Fatalf("%v %d %+v", pass, len(d.calls), rs[0])
	}
}

func TestRelevanceSkipReportsAnEmptyOrShrunkDiff(t *testing.T) {
	gates := map[string]workflow.Gate{"code": {Skill: "judge", Agent: "y1", Paths: []string{"**/*.go"}}, "all": {Skill: "judge", Agent: "y1"}}
	run := func(changed []string, prior ...Result) []Result {
		r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"code", "all"}}}, gates)
		r.Item.Kind, r.Changed, r.Prior, r.Base = "story", changed, prior, "0123456789abcdef"
		rs, _ := r.Execute(context.Background())
		return rs
	}
	judged := Result{Transition: "a->b", Gate: "all", Result: Accept, Coverage: "c", Changed: []string{"a.go", "b.md"}}

	if rs := run(nil); rs[0].Result != Skipped || !strings.Contains(rs[0].Warning, "diff is empty") || !strings.Contains(rs[0].Warning, "satelle story diff i1") {
		t.Errorf("empty diff: %+v", rs[0])
	}
	if rs := run([]string{"c.md"}, judged); !strings.Contains(rs[0].Warning, "shrunk") || !strings.Contains(rs[0].Warning, "a.go, b.md") || !strings.Contains(rs[0].Warning, "all on a->b") {
		t.Errorf("shrunk diff: %+v", rs[0])
	}
	if rs := run([]string{"a.go", "b.md", "c.md"}, judged); rs[0].Result != Accept || rs[0].Warning != "" {
		t.Errorf("superset: %+v", rs[0])
	}
	if rs := run([]string{"b.md", "c.md"}, Result{Transition: "a->b", Gate: "all", Result: Accept, Coverage: "c", Changed: []string{"b.md"}}); rs[0].Result != Skipped || rs[0].Warning != "" {
		t.Errorf("a skip on a diff holding everything last judged warned: %+v", rs[0])
	}
	newer := []Result{judged, {Gate: "all", Result: Skipped, Changed: []string{"x.md"}}, {Gate: "all", Result: Carried, Coverage: "c", Changed: []string{"y.md"}}}
	if rs := run([]string{"c.md"}, newer...); !strings.Contains(rs[0].Warning, "a.go, b.md") {
		t.Errorf("the reference was not the last judged diff: %+v", rs[0])
	}
	rs := run([]string{"c.md"}, judged)
	if rs[0].Changed != nil || strings.Join(rs[1].Changed, ",") != "c.md" {
		t.Errorf("recorded diffs: skip %v, accept %v", rs[0].Changed, rs[1].Changed)
	}
}

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
	d.usage = map[string]agent.Result{"y1": {TokensIn: 300, TokensOut: 20, Duration: 1500 * time.Millisecond}, "n1": {TokensIn: 700, TokensOut: 40, Duration: 4 * time.Second}}
	rs, pass := r.Execute(context.Background())
	if pass || len(rs) != 1 {
		t.Fatalf("panel: %v %+v", pass, rs)
	}
	want := []Member{
		{Binding: "y1", Result: Accept, Command: "claude", Model: "model-y1", PromptBytes: len(d.prompts["y1"]), TokensIn: 300, TokensOut: 20, DurationMS: 1500},
		{Binding: "n1", Result: Reject, Class: "AC3", Command: "claude", Model: "model-n1", PromptBytes: len(d.prompts["n1"]), TokensIn: 700, TokensOut: 40, DurationMS: 4000},
	}
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

func TestJudgedResultRecordsChange(t *testing.T) {
	gates := map[string]workflow.Gate{
		"skip":   {Skill: "judge", Agent: "y1", Paths: []string{"docs/**"}},
		"ok":     {Skill: "judge", Agent: "y1"},
		"carry":  {Skill: "judge", Agent: "y1", Paths: []string{"**/*.go"}, Carry: true},
		"check":  {Skill: "check", Carry: true},
		"reject": {Skill: "judge", Agent: "n1"},
	}
	r, _ := setup([]workflow.Group{{Mode: workflow.Parallel, Gates: []string{"skip", "ok", "carry", "check", "reject"}}}, gates)
	r.Skills["check"] = workflow.Skill{Name: "check", Meta: map[string]string{"type": workflow.TypeCheck}, Script: "exit 0"}
	r.Span = Change{Base: "b0", Head: "h1", Branch: "feat/x"}
	r.Prior = []Result{
		{Transition: "s0->s1", Gate: "carry", Result: Accept, Coverage: "cov:**/*.go"},
		{Transition: "s0->s1", Gate: "check", Result: Accept, Coverage: "cov:"},
	}
	rs, _ := r.Execute(context.Background())
	got := map[string]Result{}
	for _, x := range rs {
		got[x.Gate] = x
	}
	if results(rs) != "skip=skipped ok=accept carry=carried check=carried reject=reject" {
		t.Fatalf("results = %s", results(rs))
	}
	for name, hash := range map[string]string{"ok": "chg:", "carry": "chg:**/*.go", "reject": "chg:"} {
		want := Change{Base: "b0", Head: "h1", Branch: "feat/x", Hash: hash}
		if c := got[name].Change; c == nil || *c != want {
			t.Errorf("%s change = %+v, want %+v", name, c, want)
		}
	}
	for _, name := range []string{"skip", "check"} {
		if c := got[name].Change; c != nil {
			t.Errorf("%s records a change %+v; only a model gate's verdict does", name, c)
		}
	}
}

func TestUntrackedFilesAreListedApartFromTheChange(t *testing.T) {
	gates := map[string]workflow.Gate{
		"ok":   {Skill: "judge", Agent: "y1"},
		"docs": {Skill: "judge", Agent: "y1", Paths: []string{"docs/**"}},
	}
	const header = "Untracked files in the worktree (not in the item's change):\n"
	r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"ok"}}}, gates)
	r.Untracked = []string{"scratch.txt"}
	in, err := r.Input("ok")
	if err != nil {
		t.Fatal(err)
	}
	c, u := strings.Index(in.Prompt, "Changed paths:\n"), strings.Index(in.Prompt, header)
	if c < 0 || u < c {
		t.Fatalf("the prompt has no changed block followed by an untracked block:\n%s", in.Prompt)
	}
	if strings.Contains(in.Prompt[c:u], "scratch.txt") || !strings.Contains(in.Prompt[c:u], "a/b.go") {
		t.Fatalf("the changed block is not the item's change only:\n%s", in.Prompt[c:u])
	}
	if !strings.HasPrefix(in.Prompt[u+len(header):], "scratch.txt\n") {
		t.Fatalf("the untracked file is not under its block:\n%s", in.Prompt[u:])
	}
	rs, pass := r.Execute(context.Background())
	if !pass || results(rs) != "ok=accept" || strings.Join(rs[0].Changed, ",") != "a/b.go" {
		t.Fatalf("recorded change %s %v; want a/b.go only", results(rs), rs[0].Changed)
	}

	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"docs"}}}, gates)
	r.Changed, r.Untracked = nil, []string{"docs/x.md"}
	if rs, pass := r.Execute(context.Background()); !pass || results(rs) != "docs=skipped" || len(d.calls) != 0 {
		t.Fatalf("an untracked docs file ran the docs gate: %s %v", results(rs), d.calls)
	}
}

func TestDispatchFailureIsAnError(t *testing.T) {
	gates := map[string]workflow.Gate{
		"dead": {Skill: "judge", Agent: "dead"},
		"ok":   {Skill: "judge", Agent: "y1"},
	}
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"dead", "ok"}}}, gates)
	r.Agents["dead"] = agent.Binding{Name: "dead", Command: "claude", Model: "model-dead"}
	rs, pass := r.Execute(context.Background())
	if pass || results(rs) != "dead=error" || len(d.calls) != 1 {
		t.Fatalf("sequential: %v %s %v", pass, results(rs), d.calls)
	}
	if !strings.HasPrefix(rs[0].Notes, "dispatch failed: no such fake") || rs[0].Change != nil || rs[0].Changed != nil {
		t.Fatalf("error result %+v", rs[0])
	}
	r, _ = setup([]workflow.Group{{Mode: workflow.Parallel, Gates: []string{"dead", "ok"}}}, gates)
	r.Agents["dead"] = agent.Binding{Name: "dead", Command: "claude", Model: "model-dead"}
	if rs, pass := r.Execute(context.Background()); pass || results(rs) != "dead=error ok=accept" {
		t.Fatalf("parallel: %v %s", pass, results(rs))
	}
	for _, c := range []struct {
		combine string
		panel   []string
		want    string
	}{
		{workflow.CombineAll, []string{"dead", "y1"}, Error},
		{workflow.CombineAll, []string{"dead", "n1"}, Reject},
		{workflow.CombineAny, []string{"dead", "y1"}, Accept},
		{workflow.CombineMost, []string{"dead", "dead2", "y1"}, Error},
	} {
		r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"p"}}}, map[string]workflow.Gate{
			"p": {Skill: "judge", Panel: c.panel, Combine: c.combine},
		})
		for _, n := range []string{"dead", "dead2"} {
			r.Agents[n] = agent.Binding{Name: n, Command: "claude", Model: "model-" + n}
		}
		rs, pass := r.Execute(context.Background())
		if rs[0].Result != c.want || pass != (c.want == Accept) || !strings.Contains(rs[0].Notes, "dead: error: dispatch failed") {
			t.Errorf("%s %v: %v %+v", c.combine, c.panel, pass, rs[0])
		}
	}
}

func TestJudgeDispatchLogPath(t *testing.T) {
	r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"ok"}}}, map[string]workflow.Gate{"ok": {Skill: "judge", Agent: "y1"}})
	rec := &logRecorder{Dispatcher: r.Dispatcher}
	r.Dispatcher = rec
	r.Execute(context.Background())
	r.Logs = "/logs"
	r.Execute(context.Background())
	if len(rec.logs) != 2 || rec.logs[0] != "" || !strings.HasPrefix(rec.logs[1], filepath.Join("/logs", "dispatch", "dispatch-y1-")) || !strings.HasSuffix(rec.logs[1], "-i1.log") {
		t.Fatalf("logs %q", rec.logs)
	}
}

type logRecorder struct {
	agent.Dispatcher
	logs []string
}

func (l *logRecorder) Dispatch(ctx context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	l.logs = append(l.logs, req.Log)
	return l.Dispatcher.Dispatch(ctx, role, req)
}

func TestReviewPanelByTier(t *testing.T) {
	review := workflow.Review{TierTag: "k", Panels: map[string]map[string][]string{"g": {"t2": {"y2", "n1"}}}}
	run := func(tags []string, rev workflow.Review, gates map[string]workflow.Gate, names ...string) ([]Result, *fakeDispatcher) {
		r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: names}}, gates)
		r.Item.Tags, r.Review = tags, rev
		rs, _ := r.Execute(context.Background())
		return rs, d
	}
	own := map[string]workflow.Gate{"g": {Skill: "judge", Panel: []string{"y1"}, Combine: workflow.CombineAny}, "h": {Skill: "judge", Agent: "y1"}}
	rs, d := run([]string{"k:t2"}, review, own, "g")
	if strings.Join(d.calls, ",") == "" || !sameSet(d.calls, []string{"y2", "n1"}) || rs[0].Tier != "t2" || rs[0].Result != Accept || len(rs[0].Members) != 2 {
		t.Fatalf("tier panel: calls %v result %+v", d.calls, rs[0])
	}
	for _, c := range []struct {
		name string
		tags []string
		rev  workflow.Review
		gate string
	}{
		{"untagged", nil, review, "g"},
		{"tier with no panel", []string{"k:t9"}, review, "g"},
		{"gate not named", []string{"k:t2"}, review, "h"},
		{"no review", []string{"k:t2"}, workflow.Review{}, "g"},
	} {
		rs, d := run(c.tags, c.rev, own, c.gate)
		if !sameSet(d.calls, []string{"y1"}) || rs[0].Tier != "" {
			t.Errorf("%s: calls %v tier %q, want the workflow's own judge", c.name, d.calls, rs[0].Tier)
		}
		if c.name == "no review" {
			b, _ := json.Marshal(rs[0])
			if strings.Contains(string(b), "\"tier\"") {
				t.Errorf("a result with no tier marshals a tier key: %s", b)
			}
		}
	}
	rs, d = run([]string{"k:t1", "k:t2"}, review, own, "g")
	if rs[0].Result != Reject || !strings.Contains(rs[0].Notes, "t1, t2") || len(d.calls) != 0 {
		t.Errorf("two tiers: %+v calls %v", rs[0], d.calls)
	}
}

func TestCarryIsBoundToTheTier(t *testing.T) {
	review := workflow.Review{TierTag: "k", Panels: map[string]map[string][]string{"code": {"t1": {"y1"}, "t2": {"y2"}}}}
	gates := map[string]workflow.Gate{"code": {Skill: "judge", Agent: "y1", Carry: true}}
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"code"}}}, gates)
	r.Review = review
	r.Prior = []Result{{Transition: "s0->s1", Gate: "code", Result: Accept, Coverage: "cov:", Tier: "t1"}}
	r.Item.Tags = []string{"k:t1"}
	if rs, _ := r.Execute(context.Background()); rs[0].Result != Carried || len(d.calls) != 0 {
		t.Fatalf("same tier: %s %v", results(rs), d.calls)
	}
	r.Item.Tags = []string{"k:t2"}
	if rs, _ := r.Execute(context.Background()); rs[0].Result != Accept || !sameSet(d.calls, []string{"y2"}) || rs[0].Tier != "t2" {
		t.Fatalf("raised tier: %s %v", results(rs), d.calls)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range b {
		found := false
		for _, y := range a {
			if x == y {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestReviewLeavesACheckGateAlone(t *testing.T) {
	review := workflow.Review{TierTag: "k", Panels: map[string]map[string][]string{"chk": {"t2": {"y2"}}}}
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"chk"}}}, map[string]workflow.Gate{"chk": {Skill: "ok-check"}})
	r.Skills["ok-check"] = workflow.Skill{Name: "ok-check", Meta: map[string]string{"type": "check"}, Script: "exit 0"}
	r.Item.Tags, r.Review = []string{"k:t2"}, review
	rs, _ := r.Execute(context.Background())
	if rs[0].Tier != "" || len(d.calls) != 0 {
		t.Fatalf("a check gate took a tier: %+v calls %v", rs[0], d.calls)
	}
}

func TestJudgeRecordsEffortAndPromptBytes(t *testing.T) {
	marshalMap := func(t *testing.T, v any) map[string]any {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"one"}}},
		map[string]workflow.Gate{"one": {Skill: "judge", Agent: "y1"}})
	b := r.Agents["y1"]
	b.Effort = "high"
	r.Agents["y1"] = b
	r.Item.Body = "plain"
	rs, pass := r.Execute(context.Background())
	if !pass || len(rs) != 1 {
		t.Fatalf("single with effort: %v %+v", pass, rs)
	}
	wantBytes := len(d.prompts["y1"])
	if wantBytes == 0 || rs[0].Effort != "high" || rs[0].PromptBytes != wantBytes {
		t.Fatalf("single with effort: effort=%q prompt_bytes=%d, want high and %d", rs[0].Effort, rs[0].PromptBytes, wantBytes)
	}
	raw := marshalMap(t, rs[0])
	if raw["effort"] != "high" || raw["prompt_bytes"] != float64(wantBytes) {
		t.Fatalf("single with effort JSON: %v", raw)
	}

	const utf8Suffix = "—é"
	if len(utf8Suffix) != 5 {
		t.Fatalf("utf8Suffix len = %d, want 5", len(utf8Suffix))
	}
	r.Item.Body = "plain" + utf8Suffix
	rs2, pass := r.Execute(context.Background())
	if !pass || len(rs2) != 1 || rs2[0].PromptBytes != wantBytes+5 || rs2[0].PromptBytes != len(d.prompts["y1"]) {
		t.Fatalf("utf8 byte delta: got %d, want %d (delivered %d)", rs2[0].PromptBytes, wantBytes+5, len(d.prompts["y1"]))
	}

	r, d = setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"one"}}},
		map[string]workflow.Gate{"one": {Skill: "judge", Agent: "y2"}})
	rs, pass = r.Execute(context.Background())
	if !pass || len(rs) != 1 || rs[0].Effort != "" || rs[0].PromptBytes == 0 || rs[0].PromptBytes != len(d.prompts["y2"]) {
		t.Fatalf("single without effort: %v %+v", pass, rs)
	}
	raw = marshalMap(t, rs[0])
	if _, has := raw["effort"]; has {
		t.Fatalf("single without effort marshaled effort key: %v", raw)
	}
	if raw["prompt_bytes"] != float64(len(d.prompts["y2"])) {
		t.Fatalf("single without effort JSON prompt_bytes = %v, want %d", raw["prompt_bytes"], len(d.prompts["y2"]))
	}

	r, d = setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"p"}}},
		map[string]workflow.Gate{"p": {Skill: "judge", Panel: []string{"y1", "y2", "n1"}, Combine: workflow.CombineAny}})
	y1 := r.Agents["y1"]
	y1.Effort = "high"
	r.Agents["y1"] = y1
	n1 := r.Agents["n1"]
	n1.Effort = "low"
	r.Agents["n1"] = n1
	rs, pass = r.Execute(context.Background())
	if !pass || len(rs) != 1 || len(rs[0].Members) != 3 {
		t.Fatalf("panel: %v %+v", pass, rs)
	}
	raw = marshalMap(t, rs[0])
	for _, key := range []string{"effort", "prompt_bytes"} {
		if _, has := raw[key]; has {
			t.Fatalf("panel top-level marshaled %q: %v", key, raw)
		}
	}
	rawMembers, ok := raw["members"].([]any)
	if !ok || len(rawMembers) != 3 {
		t.Fatalf("panel JSON members: %v", raw["members"])
	}
	wantEfforts := []string{"high", "", "low"}
	for i, name := range []string{"y1", "y2", "n1"} {
		m := rs[0].Members[i]
		delivered := len(d.prompts[name])
		if delivered == 0 || m.Effort != wantEfforts[i] || m.PromptBytes != delivered {
			t.Errorf("member %s: effort=%q prompt_bytes=%d, want %q and %d", name, m.Effort, m.PromptBytes, wantEfforts[i], delivered)
		}
		jm := rawMembers[i].(map[string]any)
		if wantEfforts[i] == "" {
			if _, has := jm["effort"]; has {
				t.Errorf("member %s without effort marshaled effort key: %v", name, jm)
			}
		} else if jm["effort"] != wantEfforts[i] {
			t.Errorf("member %s JSON effort = %v, want %q", name, jm["effort"], wantEfforts[i])
		}
		if jm["prompt_bytes"] != float64(delivered) {
			t.Errorf("member %s JSON prompt_bytes = %v, want %d", name, jm["prompt_bytes"], delivered)
		}
	}

	r, d = setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"dead"}}},
		map[string]workflow.Gate{"dead": {Skill: "judge", Agent: "dead"}})
	r.Agents["dead"] = agent.Binding{Name: "dead", Command: "claude", Model: "model-dead", Effort: "medium"}
	rs, pass = r.Execute(context.Background())
	if pass || len(rs) != 1 || rs[0].Result != Error {
		t.Fatalf("dispatch error: %v %+v", pass, rs)
	}
	if rs[0].Effort != "medium" || rs[0].PromptBytes == 0 || rs[0].PromptBytes != len(d.prompts["dead"]) {
		t.Fatalf("dispatch error fields: effort=%q prompt_bytes=%d, want medium and %d", rs[0].Effort, rs[0].PromptBytes, len(d.prompts["dead"]))
	}

	gates := map[string]workflow.Gate{
		"check":   {Skill: "chk"},
		"skipped": {Skill: "judge", Agent: "y1", Paths: []string{"docs/**"}},
		"carried": {Skill: "judge", Agent: "y1", Paths: []string{"**/*.go"}, Carry: true},
		"missing": {Skill: "judge", Agent: "ghost"},
	}
	r, _ = setup([]workflow.Group{{Mode: workflow.Parallel, Gates: []string{"check", "skipped", "carried", "missing"}}}, gates)
	r.Dir = t.TempDir()
	r.Skills["chk"] = workflow.Skill{Name: "chk", Meta: map[string]string{"type": workflow.TypeCheck}, Script: "exit 0"}
	y1 = r.Agents["y1"]
	y1.Effort = "high"
	r.Agents["y1"] = y1
	r.Prior = []Result{{Transition: "s0->s1", Gate: "carried", Result: Accept, Coverage: "cov:**/*.go"}}
	rs, _ = r.Execute(context.Background())
	if results(rs) != "check=accept skipped=skipped carried=carried missing=reject" {
		t.Fatalf("non-judged results = %s", results(rs))
	}
	for _, x := range rs {
		jm := marshalMap(t, x)
		for _, key := range []string{"effort", "prompt_bytes"} {
			if _, has := jm[key]; has {
				t.Errorf("%s (%s) marshaled %q: %v", x.Gate, x.Result, key, jm)
			}
		}
	}
}

type promptRecorder struct {
	agent.Dispatcher
	mu      sync.Mutex
	prompts map[string]string
}

func (p *promptRecorder) Dispatch(ctx context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	p.mu.Lock()
	if p.prompts == nil {
		p.prompts = map[string]string{}
	}
	p.prompts[req.Binding.Name] = req.Prompt
	p.mu.Unlock()
	return p.Dispatcher.Dispatch(ctx, role, req)
}

func TestJudgeDispatchAppliesDiffBoundAndSummaries(t *testing.T) {
	bound := 500
	deleted := makeDeletedSection("old.go", []string{"package old", "func Removed() {}", "var secret = 1"}, false)
	small := makeModifiedSection("a_keep.go", 2)
	medium := makeModifiedSection("b_cut.go", 30)
	large := makeModifiedSection("c_huge.go", 3000)
	fullDiff := deleted + small + medium + large
	if len(fullDiff) < 100_000 || len(fullDiff) >= 200_000 {
		t.Fatalf("fixture diff size %d must be between 100k and 200k", len(fullDiff))
	}

	gates := map[string]workflow.Gate{
		"single":  {Skill: "judge", Agent: "y1", DiffMaxBytes: &bound},
		"panel":   {Skill: "judge", Panel: []string{"y1", "y2"}, Combine: workflow.CombineAll, DiffMaxBytes: &bound},
		"tiered":  {Skill: "judge", Agent: "y1", DiffMaxBytes: &bound},
		"default": {Skill: "judge", Agent: "y2"},
	}

	runGate := func(gateName string, tags []string, rev workflow.Review) map[string]string {
		t.Helper()
		r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{gateName}}}, gates)
		rec := &promptRecorder{Dispatcher: r.Dispatcher}
		r.Dispatcher = rec
		r.Item.Tags, r.Review = tags, rev
		r.Diff = func([]string) (string, error) { return fullDiff, nil }
		rs, pass := r.Execute(context.Background())
		if !pass || len(rs) != 1 || rs[0].Result != Accept {
			t.Fatalf("%s: pass=%v rs=%+v", gateName, pass, rs)
		}
		return rec.prompts
	}

	checkBounded500 := func(name, prompt string) {
		t.Helper()
		_, diffBlock, ok := strings.Cut(prompt, "\nDiff:\n")
		if !ok {
			t.Fatalf("%s: no Diff block in prompt", name)
		}
		diffHead, trailer, ok := strings.Cut(diffBlock, "… diff truncated at 500 bytes; cut files:\n")
		if !ok {
			t.Fatalf("%s: missing 500-byte truncation header:\n%s", name, diffBlock)
		}
		if len(strings.TrimSuffix(diffHead, "\n")) > 500 {
			t.Fatalf("%s: diff body %d exceeds 500 bytes", name, len(diffHead))
		}
		if !strings.Contains(diffHead, "deleted file: old.go (3 lines)\n") {
			t.Fatalf("%s: missing deleted-file summary:\n%s", name, diffHead)
		}
		if strings.Contains(prompt, "func Removed()") {
			t.Fatalf("%s: deleted file content leaked into prompt", name)
		}
		for _, want := range []string{"- b_cut.go\n", "- c_huge.go\n"} {
			if !strings.Contains(trailer, want) {
				t.Fatalf("%s: cut list lacks %q:\n%s", name, want, trailer)
			}
		}
		if strings.Contains(trailer, "- a_keep.go\n") || strings.Contains(trailer, "- old.go\n") {
			t.Fatalf("%s: uncut file listed in cut files:\n%s", name, trailer)
		}
	}

	ps := runGate("single", nil, workflow.Review{})
	checkBounded500("single", ps["y1"])

	ps = runGate("panel", nil, workflow.Review{})
	if ps["y1"] != ps["y2"] {
		t.Fatal("panel members received different prompts")
	}
	checkBounded500("panel/y1", ps["y1"])

	rev := workflow.Review{TierTag: "k", Panels: map[string]map[string][]string{"tiered": {"t2": {"y1", "y2"}}}}
	ps = runGate("tiered", []string{"k:t2"}, rev)
	if len(ps) != 2 || ps["y1"] != ps["y2"] {
		t.Fatalf("tier panel prompts mismatch: got %d", len(ps))
	}
	checkBounded500("tiered/y1", ps["y1"])

	ps = runGate("default", nil, workflow.Review{})
	_, diffBlock, ok := strings.Cut(ps["y2"], "\nDiff:\n")
	if !ok {
		t.Fatal("default: no Diff block")
	}
	diffHead, trailer, ok := strings.Cut(diffBlock, "… diff truncated at 64000 bytes; cut files:\n")
	if !ok {
		t.Fatalf("default: 100k diff was not cut at 64000 bytes:\n%s", diffBlock[max(0, len(diffBlock)-300):])
	}
	if len(strings.TrimSuffix(diffHead, "\n")) > DefaultDiffMaxBytes {
		t.Fatalf("default: diff body %d exceeds %d", len(diffHead), DefaultDiffMaxBytes)
	}
	if !strings.Contains(trailer, "- c_huge.go\n") || strings.Contains(trailer, "- a_keep.go\n") || strings.Contains(trailer, "- b_cut.go\n") {
		t.Fatalf("default: unexpected cut list:\n%s", trailer)
	}
}
