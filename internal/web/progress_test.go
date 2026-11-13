package web

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// The progress fixture: every name is the test's own, and none appears in
// internal/web's Go source. s0 is the start, s1..s3 steps (s3 never reached),
// pk a resumes park, qk a park that does not resume, t1 and t2 terminals.
func progressWorkflow(n func(string) string) *mirror.Workflow {
	gates := func(g ...string) []mirror.WorkflowGroup {
		for i := range g {
			g[i] = n(g[i])
		}
		return []mirror.WorkflowGroup{{Mode: "sequential", Gates: g}}
	}
	return &mirror.Workflow{
		Name: n("pw"),
		States: []mirror.WorkflowState{
			{Name: n("s0"), Start: true},
			{Name: n("s1")},
			{Name: n("s2"), PerformerAgent: n("pa2")},
			{Name: n("s3")},
			{Name: n("pk"), Park: true, Resumes: true},
			{Name: n("qk"), Park: true},
			{Name: n("t1"), Terminal: true},
			{Name: n("t2"), Terminal: true},
		},
		Transitions: []mirror.WorkflowTransition{
			{From: []string{n("s0")}, To: n("s1")},
			{From: []string{n("s1")}, To: n("s2"), Groups: gates("g1", "g2")},
			{From: []string{n("s2")}, To: n("s1")},
			{From: []string{n("s2")}, To: n("t1"), Groups: gates("g5")},
			{From: []string{"*"}, To: n("t2"), Groups: gates("g3")},
			{From: []string{"*"}, To: n("pk")},
			{From: []string{"*"}, To: n("qk")},
			{From: []string{n("pk")}, To: "@prior"},
			{From: []string{n("qk")}, To: n("pk")},
		},
	}
}

func same(s string) string { return s }

// hist builds one item's events, a minute apart.
type hist struct {
	id  string
	n   func(string) string
	t0  time.Time
	evs []Entry
}

func newHist(id string, n func(string) string) *hist {
	return &hist{id: id, n: n, t0: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
}

func (h *hist) add(kind string, p any) *hist {
	i := len(h.evs) + 1
	h.evs = append(h.evs, Entry{ID: fmt.Sprintf("%s-%03d", h.id, i), Item: h.id, Kind: kind, At: h.t0.Add(time.Duration(i) * time.Minute), Payload: raw(p)})
	return h
}

// last is the time of the latest event.
func (h *hist) last() time.Time { return h.evs[len(h.evs)-1].At }

func (h *hist) place(to string) *hist {
	h.add(mirror.EventUpdated, map[string]string{"workflow": h.n("pw")})
	return h.add(mirror.EventTransitioned, map[string]string{"from": "", "to": h.n(to)})
}

func (h *hist) move(from, to string) *hist {
	return h.add(mirror.EventTransitioned, map[string]string{"from": h.n(from), "to": h.n(to)})
}

func (h *hist) gate(from, to, g, result, binding, notes, attempt string) *hist {
	return h.add(mirror.EventGateRun, mirror.GateRun{Transition: h.n(from) + mirror.TransitionSep + h.n(to), Gate: h.n(g), Binding: h.n(binding), Result: result, Notes: notes, Attempt: attempt})
}

func (h *hist) perf(state, binding string) *hist {
	return h.add(mirror.EventPerformerRun, mirror.PerformerRun{State: h.n(state), Binding: h.n(binding)})
}

// stubStanding: a session named work* is here and working, gone* is gone.
func stubStanding(s mirror.Seat) (seatlife.Standing, seatlife.Evidence) {
	if strings.HasPrefix(s.Session, "gone") {
		return seatlife.Gone, ""
	}
	return seatlife.Here, ""
}

func chipsFor(t *testing.T, w *mirror.Workflow, status string, h *hist, seats ...mirror.Seat) []stageVM {
	t.Helper()
	it := Item{ID: h.id, Kind: "story", Status: h.n(status), Workflow: w.Name}
	return progressOf(w, it, h.evs, seats, stubStanding)
}

func names(cs []stageVM) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func chip(t *testing.T, cs []stageVM, name string) stageVM {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no chip %q in %v", name, names(cs))
	return stageVM{}
}

// renderRows renders story rows as the stories fragment does.
func renderRows(t *testing.T, rows []rowVM) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mirrorRender(rec, "storyRows", "/", rows)
	if rec.Code != 200 {
		t.Fatalf("render: %d %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func rowOf(w *mirror.Workflow, status string, h *hist, cs []stageVM) rowVM {
	return rowVM{Item: Item{ID: h.id, Kind: "story", Title: h.id, Status: h.n(status), Workflow: w.Name}, Progress: cs}
}

// progressSnapshot pushes w, the stories and tasks, their events and seats.
func progressSnapshot(ws []*mirror.Workflow, stories, tasks []Item, events []Entry, seats []mirror.Seat) mirror.Snapshot {
	snap := mirror.Snapshot{RepoKey: "rk", Slug: "proj",
		Settings: raw(mirror.Settings{RepoRoot: "/p"}),
		Identity: raw(mirror.IdentityMeta{ProjectName: "proj", RepoRoot: "/p"})}
	for _, w := range ws {
		snap.Docs = append(snap.Docs, raw(mirror.Doc{Kind: mirror.DocKindWorkflows, Name: w.Name, Body: "name = \"" + w.Name + "\"", Workflow: w}))
	}
	for _, it := range stories {
		snap.Stories = append(snap.Stories, raw(it))
	}
	for _, it := range tasks {
		snap.Tasks = append(snap.Tasks, raw(it))
	}
	for _, e := range events {
		snap.LedgerEvents = append(snap.LedgerEvents, raw(e))
	}
	for _, s := range seats {
		snap.Seats = append(snap.Seats, raw(s))
	}
	return snap
}

func TestProgressStepsInFirstReachedOrder(t *testing.T) {
	w := progressWorkflow(same)
	a := newHist("sto_a", same).place("s0").move("s0", "s1").move("s1", "pk").move("pk", "s1").move("s1", "s2")
	if got := names(chipsFor(t, w, "s2", a)); !slices.Equal(got, []string{"s0", "s1", "s2"}) {
		t.Errorf("item A: chips %v, want [s0 s1 s2]", got)
	}
	b := newHist("sto_b", same).place("s0")
	if got := chipsFor(t, w, "s0", b); got != nil {
		t.Errorf("item B (only placed): chips %v, want none", names(got))
	}
	c := newHist("sto_c", same).place("s0").move("s0", "s1").move("s1", "s2")
	c.add(mirror.EventUpdated, map[string]string{"workflow": "pw"}).add(mirror.EventTransitioned, map[string]string{"from": "x", "to": "s0"})
	c.move("s0", "s1")
	if got := names(chipsFor(t, w, "s1", c)); !slices.Equal(got, []string{"s0", "s1"}) {
		t.Errorf("item C (reselected): chips %v, want [s0 s1]", got)
	}
	d := newHist("sto_d", same).place("s0").move("s0", "pk")
	if got := names(chipsFor(t, w, "pk", d)); !slices.Equal(got, []string{"s0"}) {
		t.Errorf("item D (parked from the start): chips %v, want [s0]", got)
	}

	it := Item{ID: "sto_a", Kind: "story", Title: "A", Status: "s2", Workflow: "pw"}
	srv := mirrorWith(t, progressSnapshot([]*mirror.Workflow{w}, []Item{it}, nil, a.evs, nil))
	body := httpGetBody(t, srv.URL+"/r/proj/fragment/stories")
	inOrder(t, body, `data-expand-url="fragment/story/sto_a"`, "stage-chip", ">s0 ", ">s1 ", ">s2 ", "</tr>")
}

func TestProgressStoryRowsOnly(t *testing.T) {
	w := progressWorkflow(same)
	pw := progressWorkflow(func(s string) string {
		if s == "pw" {
			return "pw2"
		}
		return s
	})
	pw.Panel = "Ops Board"
	st := newHist("sto_s", same).place("s0").move("s0", "s1")
	pn := newHist("sto_p", same).place("s0").move("s0", "s1")
	pn.evs[0].Payload = raw(map[string]string{"workflow": "pw2"})
	tk := newHist("tsk_t", same).place("s0").move("s0", "s1")
	srv := mirrorWith(t, progressSnapshot([]*mirror.Workflow{w, pw},
		[]Item{{ID: "sto_s", Kind: "story", Title: "S", Status: "s1", Workflow: "pw"}, {ID: "sto_p", Kind: "story", Title: "P", Status: "s1", Workflow: "pw2"}},
		[]Item{{ID: "tsk_t", Kind: "task", Title: "T", Status: "s1", Workflow: "pw"}},
		slices.Concat(st.evs, pn.evs, tk.evs), nil))

	page := httpGetBody(t, srv.URL+"/r/proj/")
	section := func(topic string) string {
		i := strings.Index(page, `data-topic="`+topic+`"`)
		if i < 0 {
			t.Fatalf("no %s panel", topic)
		}
		return page[i : i+strings.Index(page[i:], "</table>")]
	}
	if s := section("stories"); !strings.Contains(s, "<th>Progress</th>") || !strings.Contains(s, "stage-chip") {
		t.Error("the Stories table has no Progress header or chip")
	}
	if s := section("tasks"); strings.Contains(s, "<th>Progress</th>") || strings.Contains(s, "stage-chip") {
		t.Error("the Tasks table has a Progress header or chip")
	}
	if s := section(panelTopic("Ops Board")); !strings.Contains(s, "<th>Progress</th>") || !strings.Contains(s, "stage-chip") {
		t.Error("the workflow panel has no Progress header or chip")
	}
	if b := httpGetBody(t, srv.URL+"/r/proj/fragment/tasks"); strings.Contains(b, "stage-chip") || !strings.Contains(b, "tsk_t") {
		t.Errorf("fragment/tasks: want the task row and no chip:\n%s", b)
	}
	if b := httpGetBody(t, srv.URL+"/r/proj/fragment/stories"); !strings.Contains(b, "stage-chip") {
		t.Error("fragment/stories has no chip")
	}
	if b := httpGetBody(t, srv.URL+"/r/proj/fragment/"+panelTopic("Ops Board")); !strings.Contains(b, "stage-chip") || !strings.Contains(b, "sto_p") {
		t.Errorf("the panel fragment has no chip:\n%s", b)
	}
}

func TestProgressChipStates(t *testing.T) {
	w := progressWorkflow(same)
	at := newHist("sto_1", same).place("s0").move("s0", "s1").move("s1", "s2")
	here := mirror.Seat{Item: "sto_1", Session: "work1", BoundAt: at.last()}
	gone := mirror.Seat{Item: "sto_1", Session: "gone1", BoundAt: at.last()}

	cs := chipsFor(t, w, "s2", at, here)
	if c := chip(t, cs, "s2"); !c.Performing {
		t.Errorf("s2 with a working seat: %+v, want performing", c)
	}
	for _, n := range []string{"s0", "s1"} {
		if c := chip(t, cs, n); c.Current || c.Performing {
			t.Errorf("%s: %+v, want done", n, c)
		}
	}
	for _, seats := range [][]mirror.Seat{nil, {gone}} {
		if c := chip(t, chipsFor(t, w, "s2", at, seats...), "s2"); !c.Current || c.Performing {
			t.Errorf("s2 with seats %v: %+v, want current, not performing", seats, c)
		}
	}

	quiet := func(label string, cs []stageVM, row string) {
		t.Helper()
		for _, c := range cs {
			if c.Current || c.Performing {
				t.Errorf("%s: chip %s pulses", label, c.Name)
			}
		}
		if strings.Contains(row, "stage-current") || strings.Contains(row, "stage-performing") {
			t.Errorf("%s: the row pulses: %s", label, row)
		}
	}
	fin := newHist("sto_f", same).place("s0").move("s0", "s1").move("s1", "s2").move("s2", "t1")
	cs = chipsFor(t, w, "t1", fin, mirror.Seat{Session: "work2"})
	quiet("finished", cs, renderRows(t, []rowVM{rowOf(w, "t1", fin, cs)}))

	can := newHist("sto_c", same).place("s0").move("s0", "s1").move("s1", "t2")
	cs = chipsFor(t, w, "t2", can)
	if slices.Contains(names(cs), "t2") {
		t.Errorf("cancelled: a t2 chip in %v", names(cs))
	}
	quiet("cancelled", cs, renderRows(t, []rowVM{rowOf(w, "t2", can, cs)}))

	pk := newHist("sto_p", same).place("s0").move("s0", "s1").move("s1", "pk")
	cs = chipsFor(t, w, "pk", pk)
	if slices.Contains(names(cs), "pk") || !chip(t, cs, "s1").Current {
		t.Errorf("parked at pk from s1: %v, want s1 current and no pk chip", cs)
	}
	pk.move("pk", "s1")
	if cs = chipsFor(t, w, "s1", pk); slices.Contains(names(cs), "pk") {
		t.Errorf("after the resume: a pk chip in %v", names(cs))
	}

	qk := newHist("sto_q", same).place("s0").move("s0", "s1").move("s1", "qk")
	if cs = chipsFor(t, w, "qk", qk); slices.Contains(names(cs), "qk") || !chip(t, cs, "s1").Current {
		t.Errorf("parked at qk from s1: %v, want s1 current and no qk chip", cs)
	}
	qk.move("qk", "pk")
	if cs = chipsFor(t, w, "pk", qk); !chip(t, cs, "s1").Current {
		t.Errorf("after qk->pk: %v, want s1 current", cs)
	}

	d := newHist("sto_d", same).place("s0").move("s0", "pk")
	if c := chip(t, chipsFor(t, w, "pk", d), "s0"); !c.Current {
		t.Errorf("item D: s0 %+v, want current", c)
	}
}

func TestProgressExitGates(t *testing.T) {
	w := progressWorkflow(same)
	h := newHist("sto_1", same).place("s0").move("s0", "s1")
	s1 := func() stageVM { return chip(t, chipsFor(t, w, "s1", h), "s1") }
	c := s1()
	if c.To != "s2" || !slices.Equal(c.Gates, []string{"g1", "g2"}) || c.Passed || c.ToneWord != toneNotRun {
		t.Errorf("s1 before any presentation: %+v, want s1->s2 [g1 g2], grey, not yet run", c)
	}
	if row := renderRows(t, []rowVM{rowOf(w, "s1", h, chipsFor(t, w, "s1", h))}); !strings.Contains(row, `>s1 <b class="gates tone-idle">2</b>`) {
		t.Errorf("s1's chip does not show 2 grey: %s", row)
	}
	h.gate("s1", "s2", "g1", "reject", "j1", "no", "A")
	if c = s1(); c.Passed || c.ToneWord != toneRejected {
		t.Errorf("after a reject: %+v, want grey, last round rejected", c)
	}
	h.gate("s1", "s2", "g1", "error", "j1", "down", "B")
	if c = s1(); c.Passed || c.ToneWord != toneErrored {
		t.Errorf("after an error: %+v, want grey, last round errored", c)
	}
	h.gate("s1", "s2", "g1", "accept", "j1", "", "C").gate("s1", "s2", "g2", "accept", "j2", "", "C").move("s1", "s2")
	cs := chipsFor(t, w, "s2", h)
	if c = chip(t, cs, "s1"); !c.Passed || c.ToneWord != tonePassed {
		t.Errorf("after accepted and moved: %+v, want green", c)
	}
	if row := renderRows(t, []rowVM{rowOf(w, "s2", h, cs)}); !strings.Contains(row, `>s1 <b class="gates tone-ok">2</b>`) {
		t.Errorf("s1 is not green in the row: %s", row)
	}
	if c = chip(t, cs, "s0"); !c.Passed || len(c.Gates) != 0 || c.To != "s1" {
		t.Errorf("s0 left through its ungated edge: %+v, want green 0 to s1", c)
	}
	if c = chip(t, cs, "s2"); c.To != "t1" {
		t.Errorf("s2's exit: %+v, want s2->t1", c)
	}
	h.move("s2", "s1").gate("s1", "s2", "g1", "reject", "j1", "again", "D")
	cs = chipsFor(t, w, "s1", h)
	if c = chip(t, cs, "s1"); c.Passed || c.ToneWord != toneRejected {
		t.Errorf("after a rework and a rejected round: %+v, want grey", c)
	}
	if c = chip(t, cs, "s2"); c.To != "t1" {
		t.Errorf("s2's exit after the rework was the last move: %+v, want s2->t1", c)
	}
	if row := renderRows(t, []rowVM{rowOf(w, "s1", h, cs)}); strings.Contains(row, "tone-live") || strings.Contains(row, `class="run`) {
		t.Errorf("the row draws a running tone: %s", row)
	}

	// A refused cancel from the start, then the real exit.
	rc := newHist("sto_rc", same).place("s0").gate("s0", "t2", "g3", "reject", "j3", "keep it", "A").move("s0", "s1")
	if c = chip(t, chipsFor(t, w, "s1", rc), "s0"); c.To != "s1" || len(c.Gates) != 0 || !c.Passed || c.Rounds != 0 {
		t.Errorf("start after a refused cancel: %+v, want s0->s1, green 0, no rounds", c)
	}
	// A refused cancel from a step whose exit was never presented.
	rs := newHist("sto_rs", same).place("s0").move("s0", "s1").gate("s1", "t2", "g3", "reject", "j3", "keep it", "A")
	if c = chip(t, chipsFor(t, w, "s1", rs), "s1"); c.To != "s2" || !slices.Equal(c.Gates, []string{"g1", "g2"}) || c.ToneWord != toneNotRun || c.Rounds != 0 {
		t.Errorf("step after a refused cancel: %+v, want s1->s2 [g1 g2], not yet run, no rounds", c)
	}
	// Cancelled through the wildcard: the cancel is the step's exit.
	cw := newHist("sto_cw", same).place("s0").move("s0", "s1").
		gate("s1", "s2", "g1", "reject", "j1", "", "A").
		gate("s1", "t2", "g3", "reject", "j3", "", "B").
		gate("s1", "t2", "g3", "accept", "j3", "", "C").move("s1", "t2")
	if c = chip(t, chipsFor(t, w, "t2", cw), "s1"); c.To != "t2" || !slices.Equal(c.Gates, []string{"g3"}) || !c.Passed || c.Rounds != 1 {
		t.Errorf("cancelled from s1: %+v, want s1->t2 [g3], green, 1 round", c)
	}

	// A step whose only explicit edge is rework takes its wildcard exit.
	rw := &mirror.Workflow{Name: "rw",
		States: []mirror.WorkflowState{{Name: "s0", Start: true}, {Name: "s1"}, {Name: "s2"}, {Name: "t1", Terminal: true}},
		Transitions: []mirror.WorkflowTransition{
			{From: []string{"s0"}, To: "s1"}, {From: []string{"s1"}, To: "s2"}, {From: []string{"s2"}, To: "s1"},
			{From: []string{"*"}, To: "t1", Groups: []mirror.WorkflowGroup{{Mode: "sequential", Gates: []string{"g1", "g2"}}}},
		}}
	r := newHist("sto_r", same).place("s0").move("s0", "s1").move("s1", "s2")
	s2 := func() stageVM { return chip(t, chipsFor(t, rw, "s2", r), "s2") }
	if c = s2(); c.To != "t1" || !slices.Equal(c.Gates, []string{"g1", "g2"}) || c.ToneWord != toneNotRun {
		t.Errorf("rework-only s2: %+v, want the wildcard [g1 g2], not yet run", c)
	}
	r.gate("s2", "t1", "g1", "reject", "j1", "", "A")
	if c = s2(); c.ToneWord != toneRejected || c.Rounds != 1 {
		t.Errorf("rework-only s2 after a reject: %+v, want last round rejected, 1 round", c)
	}
	r.gate("s2", "t1", "g1", "accept", "j1", "", "B").gate("s2", "t1", "g2", "accept", "j2", "", "B").move("s2", "t1")
	cs = chipsFor(t, rw, "t1", r)
	if c = chip(t, cs, "s2"); !c.Passed {
		t.Errorf("rework-only s2 after accepted: %+v, want green", c)
	}
	for _, n := range []string{"s0", "s1"} {
		if c = chip(t, cs, n); !c.Passed || len(c.Gates) != 0 {
			t.Errorf("%s left through its ungated edge: %+v, want green 0", n, c)
		}
	}
	rw.Transitions = append(rw.Transitions, mirror.WorkflowTransition{From: []string{"s2"}, To: "t1", Groups: []mirror.WorkflowGroup{{Mode: "sequential", Gates: []string{"g4"}}}})
	if c = chip(t, chipsFor(t, rw, "t1", r), "s2"); !slices.Equal(c.Gates, []string{"g4"}) {
		t.Errorf("an explicit s2->t1 beside the wildcard: %+v, want [g4]", c)
	}
}

func TestProgressRoundsAndParks(t *testing.T) {
	w := progressWorkflow(same)
	h := newHist("sto_1", same).place("s0").move("s0", "s1").
		gate("s1", "s2", "g1", "reject", "j1", "", "A").gate("s1", "s2", "g2", "reject", "j2", "", "A").
		gate("s1", "s2", "g1", "reject", "j1,j2,j3", "", "B").
		gate("s1", "s2", "g1", "error", "j1", "", "C").
		move("s1", "pk").move("pk", "s1").move("s1", "pk").move("pk", "s1").
		gate("s1", "s2", "g1", "accept", "j1", "", "X").gate("s1", "s2", "g2", "accept", "j2", "", "X").move("s1", "s2").
		move("s2", "s1").
		gate("s1", "s2", "g1", "reject", "j1", "", "D").
		gate("s1", "s2", "g1", "accept", "j1", "", "E").gate("s1", "s2", "g2", "accept", "j2", "", "E").move("s1", "s2").
		move("s2", "pk")
	cs := chipsFor(t, w, "pk", h)
	if c := chip(t, cs, "s1"); c.Rounds != 3 || c.Parks != 2 {
		t.Errorf("s1: %d rounds, %d parks; want 3 and 2", c.Rounds, c.Parks)
	}
	if c := chip(t, cs, "s2"); c.Rounds != 0 || c.Parks != 1 {
		t.Errorf("s2: %d rounds, %d parks; want 0 and 1 (the park taken at s2)", c.Rounds, c.Parks)
	}
	if row := renderRows(t, []rowVM{rowOf(w, "pk", h, cs)}); !strings.Contains(row, `<b class="rej">3</b> <b class="blk">2</b>`) {
		t.Errorf("the row does not draw 3 rejected rounds and 2 parks on s1: %s", row)
	}

	q := newHist("sto_q", same).place("s0").move("s0", "s1").move("s1", "qk")
	if c := chip(t, chipsFor(t, w, "qk", q), "s1"); c.Parks != 0 || !c.Current {
		t.Errorf("into qk: %+v, want 0 parks and s1 current", c)
	}
	q.move("qk", "pk")
	if c := chip(t, chipsFor(t, w, "pk", q), "s1"); c.Parks != 1 || !c.Current {
		t.Errorf("qk->pk: %+v, want 1 park on s1, s1 current", c)
	}

	d := newHist("sto_d", same).place("s0").move("s0", "pk")
	if c := chip(t, chipsFor(t, w, "pk", d), "s0"); c.Parks != 1 {
		t.Errorf("item D: s0 %d parks, want 1", c.Parks)
	}
	e := newHist("sto_e", same).place("s0").move("s0", "pk").move("pk", "s0").move("s0", "pk")
	if c := chip(t, chipsFor(t, w, "pk", e), "s0"); c.Parks != 2 {
		t.Errorf("item E: s0 %d parks, want 2", c.Parks)
	}
}

func TestProgressHoverAndDetailNotes(t *testing.T) {
	w := progressWorkflow(same)
	h := newHist("sto_1", same).place("s0").move("s0", "s1").
		gate("s1", "s2", "g1", "reject", "j1", "", "A").gate("s1", "s2", "g1", "reject", "j1", "", "B").
		gate("s1", "s2", "g1", "accept", "j1", "", "C").gate("s1", "s2", "g2", "accept", "j2", "", "C").move("s1", "s2")
	title := func(status, step string, h *hist, seats ...mirror.Seat) string {
		return chip(t, chipsFor(t, w, status, h, seats...), step).Title
	}
	has := func(label, s string, want ...string) {
		t.Helper()
		for _, x := range want {
			if !strings.Contains(s, x) {
				t.Errorf("%s: title %q lacks %q", label, s, x)
			}
		}
	}
	seat := func(name string, at time.Time) mirror.Seat {
		return mirror.Seat{Item: "sto_1", Session: name, BoundAt: at, User: "user-" + name}
	}

	has("performing, no run", title("s2", "s2", h, seat("work1", h.last())), "performing (pa2)")
	has("s1", title("s2", "s1", h), "2 exit gates", "g1, g2", "2 rejected rounds", "0 parks")
	if s := title("s2", "s1", h); strings.Contains(s, "performed by") {
		t.Errorf("s1 with no run names an agent: %q", s)
	}

	old := newHist("sto_1", same).place("s0").move("s0", "s1").move("s1", "s2").perf("s2", "b-old").move("s2", "s1").move("s1", "s2")
	if s := title("s2", "s2", old, seat("work1", old.last())); !strings.Contains(s, "performing (pa2)") || strings.Contains(s, "b-old") {
		t.Errorf("an earlier run names the working one: %q", s)
	}
	bound := old.last()
	old.perf("s2", "b2")
	has("a run since the bind", title("s2", "s2", old, seat("work1", bound)), "performing (b2)")
	two := newHist("sto_1", same).place("s0").move("s0", "s1").move("s1", "s2")
	b1 := two.last()
	two.perf("s2", "b-mid")
	b2 := two.last().Add(time.Second)
	if s := title("s2", "s2", two, seat("work1", b1), seat("work2", b2)); strings.Contains(s, "b-mid") || !strings.Contains(s, "performing (pa2)") {
		t.Errorf("a run between two binds names the working run: %q", s)
	}
	if s := title("s2", "s2", two, seat("work1", time.Time{})); strings.Contains(s, "b-mid") || !strings.Contains(s, "performing (pa2)") {
		t.Errorf("a seat with no bind time dates the run: %q", s)
	}
	noAgent := progressWorkflow(same)
	noAgent.States[2].PerformerAgent = ""
	it := Item{ID: "sto_1", Kind: "story", Status: "s2", Workflow: "pw"}
	if c := chip(t, progressOf(noAgent, it, two.evs, []mirror.Seat{seat("work1", time.Time{})}, stubStanding), "s2"); !strings.Contains(c.Title, "performing (user-work1)") {
		t.Errorf("no performer agent, a seat's user: %q", c.Title)
	}
	if c := chip(t, progressOf(noAgent, it, two.evs, []mirror.Seat{{Item: "sto_1", Session: "work1"}}, stubStanding), "s2"); !strings.Contains(c.Title, "performing ("+agentUnknown+")") {
		t.Errorf("no agent and no user: %q", c.Title)
	}
	fresh := newHist("sto_1", same).place("s0").move("s0", "s1").move("s1", "s2")
	if c := chip(t, progressOf(noAgent, it, fresh.evs, []mirror.Seat{seat("work1", fresh.last())}, stubStanding), "s2"); !strings.Contains(c.Title, "performing (user-work1)") {
		t.Errorf("no run, no performer agent: %q", c.Title)
	}
	done := newHist("sto_1", same).place("s0").move("s0", "s1").perf("s1", "b1").move("s1", "s2")
	has("done with a run", title("s2", "s1", done, seat("work1", done.last())), "performed by b1")

	errOnly := newHist("sto_2", same).place("s0").move("s0", "s1").gate("s1", "s2", "g1", "error", "j1", "", "A")
	s := title("s1", "s1", errOnly)
	has("error only", s, toneErrored, "0 rejected rounds")
	if strings.Count(s, "rejected") != 1 {
		t.Errorf("error only: the title says rejected beyond its count: %q", s)
	}

	// The detail page: every rejected gate's notes, on its step.
	notes := newHist("sto_n", same).place("s0").move("s0", "s1").
		gate("s1", "s2", "g1", "reject", "j1", "n-one", "A").gate("s1", "s2", "g2", "reject", "j2", "n-two", "A").
		gate("s1", "s2", "g1", "reject", "j1", "n-three", "B").gate("s1", "s2", "g2", "accept", "j2", "n-accepted", "B")
	both := newHist("sto_x", same).place("s0").move("s0", "s1").
		gate("s1", "s2", "g1", "reject", "j1", "n-exit", "A").
		gate("s1", "t2", "g3", "reject", "j3", "n-cancel", "B")
	srv := mirrorWith(t, progressSnapshot([]*mirror.Workflow{w}, []Item{
		{ID: "sto_n", Kind: "story", Title: "N", Status: "s1", Workflow: "pw"},
		{ID: "sto_2", Kind: "story", Title: "E", Status: "s1", Workflow: "pw"},
		{ID: "sto_x", Kind: "story", Title: "X", Status: "s1", Workflow: "pw"},
	}, nil, slices.Concat(notes.evs, errOnly.evs, both.evs), nil))
	between := func(body string) string {
		i, j := strings.Index(body, "<h4>Progress</h4>"), strings.Index(body, "<h4>Route</h4>")
		if i < 0 || j < i {
			t.Fatalf("no Progress section before Route:\n%s", body)
		}
		return body[i:j]
	}
	for _, path := range []string{"/r/proj/story/sto_n", "/r/proj/fragment/story/sto_n"} {
		body := httpGetBody(t, srv.URL+path)
		inOrder(t, body, "<h4>Progress</h4>", ">s1 ", "g1", "n-one", "g2", "n-two", "g1", "n-three", "<h4>Route</h4>")
		sec := between(body)
		if n := strings.Count(sec, "verdict-notes"); n != 3 || strings.Contains(sec, "n-accepted") {
			t.Errorf("%s: %d notes blocks (want 3), accepted notes shown: %v", path, n, strings.Contains(sec, "n-accepted"))
		}
	}
	if sec := between(httpGetBody(t, srv.URL+"/r/proj/story/sto_2")); strings.Contains(sec, "verdict-notes") || strings.Contains(sec, "<ol><li>") {
		t.Errorf("an error-only round is listed: %s", sec)
	}
	sec := between(httpGetBody(t, srv.URL+"/r/proj/story/sto_x"))
	inOrder(t, sec, ">s1 ", "s1", "s2", "g1", "n-exit", "s1", "t2", "g3", "j3", "n-cancel")
	if !strings.Contains(sec, `<b class="rej">1</b>`) || !strings.Contains(sec, "1 rejected round") {
		t.Errorf("the count is not the exit edge's one round: %s", sec)
	}
}

func TestProgressMarkupKeepsOldShape(t *testing.T) {
	w := progressWorkflow(same)
	h := newHist("sto_1", same).place("s0").move("s0", "s1").
		gate("s1", "s2", "g1", "reject", "j1", "", "A").move("s1", "pk").move("pk", "s1").
		gate("s1", "s2", "g1", "accept", "j1", "", "B").gate("s1", "s2", "g2", "accept", "j2", "", "B").move("s1", "s2")
	it := Item{ID: "sto_1", Kind: "story", Title: "One", Status: "s2", Workflow: "pw"}
	srv := mirrorWith(t, progressSnapshot([]*mirror.Workflow{w}, []Item{it}, nil, h.evs, nil))
	body := httpGetBody(t, srv.URL+"/r/proj/fragment/stories")
	cellRe := regexp.MustCompile(`<td class="col-reviews">(.*?)</td>`)
	cell := cellRe.FindStringSubmatch(body)
	if cell == nil {
		t.Fatalf("no Progress cell:\n%s", body)
	}
	chipRe := regexp.MustCompile(`<span class="stage-chip stage-(done|current|performing)" title="[^"]*">[^<]+ <b class="gates tone-(ok|idle)">\d+</b>( <b class="rej">\d+</b>)?( <b class="blk">\d+</b>)?</span>`)
	if rest := chipRe.ReplaceAllString(cell[1], ""); rest != "" || len(chipRe.FindAllString(cell[1], -1)) != 3 {
		t.Errorf("the cell is not three old-shape chips: %q (left %q)", cell[1], rest)
	}
	if page := httpGetBody(t, srv.URL+"/r/proj/"); !strings.Contains(page, "<th>Status</th><th>Progress</th>") {
		t.Error("the Stories header has no Progress after Status")
	}
	css, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, sel := range []string{".col-reviews", ".stage-chip", ".stage-done", ".stage-current", ".stage-performing",
		".stage-chip .gates.tone-idle", ".stage-chip .gates.tone-ok", ".rej", ".stage-chip .blk", "@keyframes review-pulse"} {
		if !strings.Contains(string(css), sel) {
			t.Errorf("app.css lacks %s", sel)
		}
	}
}

func TestProgressNamesFromWorkflow(t *testing.T) {
	rename := map[string]string{"pw": "route", "s0": "intake", "s1": "draft", "s2": "review", "s3": "launch",
		"pk": "paused", "qk": "frozen", "t1": "shipped", "t2": "dropped",
		"g1": "lint", "g2": "proof", "g3": "veto", "g5": "audit", "j1": "judge-x", "j2": "judge-y", "j3": "judge-z", "pa2": "maker", "b1": "crafter"}
	to := func(s string) string {
		if r, ok := rename[s]; ok {
			return r
		}
		return s
	}
	run := func(n func(string) string) (titles []string, chips []string, detail string) {
		w := progressWorkflow(n)
		h := newHist("sto_1", n).place("s0").move("s0", "s1").perf("s1", "b1").
			gate("s1", "s2", "g1", "reject", "j1", "why", "A").gate("s1", "t2", "g3", "reject", "j3", "nope", "B").
			gate("s1", "s2", "g1", "accept", "j1", "", "C").gate("s1", "s2", "g2", "accept", "j2", "", "C").move("s1", "s2").move("s2", "pk")
		it := Item{ID: "sto_1", Kind: "story", Title: "One", Status: n("pk"), Workflow: w.Name}
		for _, c := range progressOf(w, it, h.evs, []mirror.Seat{{Item: "sto_1", Session: "work1", BoundAt: h.last()}}, stubStanding) {
			titles, chips = append(titles, c.Title), append(chips, c.Name)
		}
		srv := mirrorWith(t, progressSnapshot([]*mirror.Workflow{w}, []Item{it}, nil, h.evs, nil))
		body := httpGetBody(t, srv.URL+"/r/proj/story/sto_1")
		return titles, chips, body[strings.Index(body, "<h4>Progress</h4>"):strings.Index(body, "<h4>Route</h4>")]
	}
	_, base, _ := run(same)
	titles, got, detail := run(to)
	var want []string
	for _, b := range base {
		want = append(want, to(b))
	}
	if !slices.Equal(got, want) {
		t.Errorf("renamed chips %v, want %v", got, want)
	}
	all := strings.Join(titles, "\n") + "\n" + detail
	for old, nu := range rename {
		if old == "pw" || old == "s3" || old == "t1" || old == "pk" || old == "qk" || old == "g5" || old == "j2" {
			continue // not drawn by this history (j2 only accepts)
		}
		if !strings.Contains(all, nu) {
			t.Errorf("the renamed %q (was %q) is not drawn", nu, old)
		}
		if regexp.MustCompile(`\b` + old + `\b`).MatchString(all) {
			t.Errorf("the old name %q is still drawn", old)
		}
	}
	if _, err := os.Stat("stages.go"); !os.IsNotExist(err) {
		t.Error("internal/web/stages.go exists: the pre-1.0 stages code is ported")
	}
}

// The progress payloads decode as the kernel writes them.
func TestProgressReadsPushedPayloads(t *testing.T) {
	var g mirror.GateRun
	if err := json.Unmarshal([]byte(`{"transition":"a->b","gate":"x","result":"reject","attempt":"at1"}`), &g); err != nil || g.Attempt != "at1" {
		t.Errorf("gate.run attempt: %+v %v", g, err)
	}
	var p mirror.PerformerRun
	if err := json.Unmarshal([]byte(`{"state":"a","binding":"b","model":"m","tokens_in":1}`), &p); err != nil || p.State != "a" || p.Binding != "b" {
		t.Errorf("performer.run: %+v %v", p, err)
	}
}
