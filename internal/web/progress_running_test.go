package web

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func (h *hist) start(from, to, g, attempt string) *hist {
	return h.add(mirror.EventGateStart, mirror.GateStart{Transition: h.n(from) + mirror.TransitionSep + h.n(to), Gate: h.n(g), Attempt: attempt})
}

func withoutStarts(h *hist) *hist {
	out := &hist{id: h.id, n: h.n, t0: h.t0}
	for _, e := range h.evs {
		if e.Kind != mirror.EventGateStart {
			out.evs = append(out.evs, e)
		}
	}
	return out
}

func TestProgressGatesRunning(t *testing.T) {
	w := progressWorkflow(same)
	h := newHist("sto_1", same).place("s0").move("s0", "s1").start("s1", "s2", "g1", "A")
	c := chip(t, chipsFor(t, w, "s1", h), "s1")
	if c.Running != 1 || c.Passed || c.ToneWord != "1 still running" || c.To != "s2" {
		t.Errorf("one of two gates started: %+v, want 1 still running to s2", c)
	}
	row := renderRows(t, []rowVM{rowOf(w, "s1", h, chipsFor(t, w, "s1", h))})
	if !strings.Contains(row, `<b class="gates tone-live">2</b> <b class="run live">1</b>`) || !strings.Contains(row, "2 exit gates to s2 (g1, g2): 1 still running") {
		t.Errorf("the running chip: %s", row)
	}
	h.start("s1", "s2", "g2", "A")
	if c := chip(t, chipsFor(t, w, "s1", h), "s1"); c.Running != 2 {
		t.Errorf("both started: running %d, want 2", c.Running)
	}
	h.gate("s1", "s2", "g1", "accept", "j1", "", "A")
	if c := chip(t, chipsFor(t, w, "s1", h), "s1"); c.Running != 1 {
		t.Errorf("one result in: running %d, want 1", c.Running)
	}
	h.gate("s1", "s2", "g2", "accept", "j2", "", "A")
	c = chip(t, chipsFor(t, w, "s1", h), "s1")
	if c.Running != 0 || c.Passed || c.ToneWord != toneNotLeft {
		t.Errorf("all results in, no move: %+v, want idle, not left", c)
	}
	if bare := chip(t, chipsFor(t, w, "s1", withoutStarts(h)), "s1"); !reflect.DeepEqual(c, bare) {
		t.Errorf("finished chip %+v differs from the chip without starts %+v", c, bare)
	}
	if row := renderRows(t, []rowVM{rowOf(w, "s1", h, chipsFor(t, w, "s1", h))}); strings.Contains(row, "tone-live") || strings.Contains(row, `class="run`) {
		t.Errorf("a finished presentation still draws running: %s", row)
	}
	h.move("s1", "s2")
	c = chip(t, chipsFor(t, w, "s2", h), "s1")
	if !c.Passed || c.ToneWord != tonePassed || c.Running != 0 {
		t.Errorf("after the move: %+v, want passed", c)
	}
	if bare := chip(t, chipsFor(t, w, "s2", withoutStarts(h)), "s1"); !reflect.DeepEqual(c, bare) {
		t.Errorf("moved chip %+v differs from the chip without starts %+v", c, bare)
	}

	sup := newHist("sto_2", same).place("s0").move("s0", "s1").start("s1", "s2", "g1", "A").start("s1", "s2", "g1", "B").gate("s1", "s2", "g1", "reject", "j1", "", "B")
	if c := chip(t, chipsFor(t, w, "s1", sup), "s1"); c.Running != 0 {
		t.Errorf("starts superseded by a later attempt: running %d", c.Running)
	}
	park := newHist("sto_3", same).place("s0").move("s0", "s1").start("s1", "s2", "g1", "A").move("s1", "pk")
	if c := chip(t, chipsFor(t, w, "pk", park), "s1"); c.Running != 0 {
		t.Errorf("starts followed by a park: running %d", c.Running)
	}
	open := newHist("sto_4", same).place("s0").move("s0", "s1").start("s1", "s2", "g1", "A")
	open.add(mirror.EventAttachmentAdded, map[string]string{"name": "x"}).perf("s1", "b1")
	if c := chip(t, chipsFor(t, w, "s1", open), "s1"); c.Running != 1 {
		t.Errorf("unrelated events after an open start: running %d, want 1", c.Running)
	}
	twice := newHist("sto_5", same).start("s1", "s2", "g", "A").start("s1", "s2", "g", "A").gate("s1", "s2", "g", "accept", "j", "", "A")
	if _, _, n := runningGates(twice.evs); n != 1 {
		t.Errorf("one name started twice, finished once: %d running, want 1", n)
	}

	rw := progressWorkflow(same)
	rw.States = append(rw.States, mirror.WorkflowState{Name: "s6"})
	rw.Transitions = append(rw.Transitions, mirror.WorkflowTransition{From: []string{"s1"}, To: "s6", Groups: []mirror.WorkflowGroup{{Mode: "parallel", Gates: []string{"g", "g"}}}})
	rep := newHist("sto_6", same).place("s0").move("s0", "s1").start("s1", "s6", "g", "A").start("s1", "s6", "g", "A")
	row = renderRows(t, []rowVM{rowOf(rw, "s1", rep, chipsFor(t, rw, "s1", rep))})
	if !strings.Contains(row, `<b class="gates tone-live">2</b> <b class="run live">2</b>`) || !strings.Contains(row, "2 exit gates to s6 (g, g): 2 still running") {
		t.Errorf("a gate listed twice, both started: %s", row)
	}
	rep.gate("s1", "s6", "g", "accept", "j", "", "A")
	if row = renderRows(t, []rowVM{rowOf(rw, "s1", rep, chipsFor(t, rw, "s1", rep))}); !strings.Contains(row, `<b class="gates tone-live">2</b> <b class="run live">1</b>`) {
		t.Errorf("a gate listed twice, one finished: %s", row)
	}
	rep.gate("s1", "s6", "g", "reject", "j", "", "A")
	c = chip(t, chipsFor(t, rw, "s1", rep), "s1")
	if row = renderRows(t, []rowVM{rowOf(rw, "s1", rep, chipsFor(t, rw, "s1", rep))}); strings.Contains(row, `class="run`) {
		t.Errorf("a gate listed twice, both finished: %s", row)
	}
	if bare := chip(t, chipsFor(t, rw, "s1", withoutStarts(rep)), "s1"); !reflect.DeepEqual(c, bare) {
		t.Errorf("finished chip %+v differs from the chip without starts %+v", c, bare)
	}
}

func edgeWorkflow() *mirror.Workflow {
	g := func(names ...string) []mirror.WorkflowGroup {
		return []mirror.WorkflowGroup{{Mode: "sequential", Gates: names}}
	}
	return &mirror.Workflow{
		Name: "pw",
		States: []mirror.WorkflowState{
			{Name: "s1", Start: true}, {Name: "s2"}, {Name: "s3"}, {Name: "s4"}, {Name: "s5"},
			{Name: "blocked", Park: true, Resumes: true}, {Name: "cancelled", Terminal: true},
		},
		Transitions: []mirror.WorkflowTransition{
			{From: []string{"blocked"}, To: "@prior", Groups: g("r1")},
			{From: []string{"s2"}, To: "@prior", Groups: g("p1")},
			{From: []string{"s2"}, To: "s1", Groups: g("q1", "q2")},
			{From: []string{"s1"}, To: "s2", Groups: g("g1", "g2")},
			{From: []string{"s1"}, To: "s3", Groups: g("h1")},
			{From: []string{"s1"}, To: "s4", Groups: g("i1")},
			{From: []string{"s1"}, To: "s5", Groups: g("j1")},
			{From: []string{"s1"}, To: "blocked", Groups: g("b1")},
			{From: []string{"*", "blocked"}, To: "cancelled", Groups: g("c1")},
			{From: []string{"*"}, To: "s4", Groups: g("w1", "w2")},
			{From: []string{"blocked"}, To: "s1", Groups: g("x1", "x2", "x3")},
		},
	}
}

func kernelForm(w *mirror.Workflow) *workflow.Workflow {
	k := &workflow.Workflow{Name: w.Name, States: map[string]workflow.State{}}
	for _, s := range w.States {
		k.States[s.Name] = workflow.State{Start: s.Start, Park: s.Park, Resumes: s.Resumes, Terminal: s.Terminal}
	}
	for _, t := range w.Transitions {
		var gs []workflow.Group
		for _, g := range t.Groups {
			gs = append(gs, workflow.Group{Mode: workflow.Sequential, Gates: g.Gates})
		}
		k.Transitions = append(k.Transitions, workflow.Transition{From: t.From, To: t.To, Groups: gs})
	}
	return k
}

func TestProgressRunningFollowsItsEdge(t *testing.T) {
	w := edgeWorkflow()
	kw := kernelForm(w)
	running := func(label, status string, h *hist, step, wantTitle string, wantGates int, prior, to string) {
		t.Helper()
		cs := chipsFor(t, w, status, h)
		c := chip(t, cs, step)
		row := renderRows(t, []rowVM{rowOf(w, status, h, cs)})
		if c.Running != 1 || !strings.Contains(row, `<b class="run live">1</b>`) || len(c.Gates) != wantGates || !strings.Contains(c.Title, wantTitle) {
			t.Errorf("%s: chip %+v\n%s", label, c, row)
		}
		for _, o := range cs {
			if o.Name != step && o.Running != 0 {
				t.Errorf("%s: chip %s also runs", label, o.Name)
			}
		}
		tr, err := kw.Find(step, to, prior)
		if err != nil {
			t.Fatalf("%s: kernel Find: %v", label, err)
		}
		var want []string
		for _, g := range tr.Groups {
			want = append(want, g.Gates...)
		}
		if !slices.Equal(c.Gates, want) {
			t.Errorf("%s: gates %v, the kernel's transition has %v", label, c.Gates, want)
		}
	}
	quiet := func(label, status string, h *hist) {
		t.Helper()
		cs := chipsFor(t, w, status, h)
		row := renderRows(t, []rowVM{rowOf(w, status, h, cs)})
		if strings.Contains(row, "tone-live") || strings.Contains(row, `class="run`) {
			t.Errorf("%s: draws running: %s", label, row)
		}
		if bare := chipsFor(t, w, status, withoutStarts(h)); !reflect.DeepEqual(cs, bare) {
			t.Errorf("%s: chips %+v differ from the history without starts %+v", label, cs, bare)
		}
	}
	base := func() *hist { return newHist("sto_e", same).place("s1") }

	running("explicit before wildcard", "s1", base().start("s1", "s4", "i1", "A"), "s1", "1 exit gate to s4 (i1): 1 still running", 1, "", "s4")
	running("to the park state", "s1", base().start("s1", "blocked", "b1", "A"), "s1", "to blocked (b1)", 1, "", "blocked")
	running("to the terminal", "s1", base().start("s1", "cancelled", "c1", "A"), "s1", "to cancelled (c1)", 1, "", "cancelled")
	for _, to := range []string{"s4", "blocked", "cancelled"} {
		h := base().start("s1", to, "x", "A").gate("s1", to, "x", "reject", "j", "", "A")
		quiet("after a reject to "+to, "s1", h)
	}
	prior := base().move("s1", "s2").start("s2", "s1", "p1", "A")
	running("@prior first in file order", "s2", prior, "s2", "(p1)", 1, "s1", "s1")

	quiet("leaving the park to s1", "blocked", base().move("s1", "blocked").start("blocked", "s1", "x1", "A"))
	quiet("leaving the park to cancelled", "blocked", base().move("s1", "blocked").start("blocked", "cancelled", "c1", "A"))
	quiet("no such transition", "s1", base().start("s1", "gone", "z", "A"))
}
