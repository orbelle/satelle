package web

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/mirror"
)

func parkCount(c stageVM) int {
	n := 0
	for _, p := range c.Parks {
		n += p.N
	}
	return n
}

func parkedWorkflow(parks ...string) *mirror.Workflow {
	w := &mirror.Workflow{
		Name: "pw",
		States: []mirror.WorkflowState{
			{Name: "s0", Start: true},
			{Name: "s1"},
			{Name: "t1", Terminal: true},
		},
		Transitions: []mirror.WorkflowTransition{
			{From: []string{"s0"}, To: "s1"},
			{From: []string{"s1"}, To: "t1", Groups: []mirror.WorkflowGroup{{Mode: "sequential", Gates: []string{"g1"}}}},
		},
	}
	for _, p := range parks {
		w.States = append(w.States, mirror.WorkflowState{Name: p, Park: true, Resumes: true})
		w.Transitions = append(w.Transitions,
			mirror.WorkflowTransition{From: []string{"*"}, To: p},
			mirror.WorkflowTransition{From: []string{p}, To: "@prior"})
	}
	return w
}

var parkWord = regexp.MustCompile(`\bparks?\b`)

func TestProgressNeverMovedShowsCurrentStep(t *testing.T) {
	w := progressWorkflow(same)
	for _, tc := range []struct {
		label string
		h     *hist
	}{
		{"no events", newHist("sto_n", same)},
		{"only placed", newHist("sto_n", same).place("s0")},
	} {
		cs := chipsFor(t, w, "s0", tc.h)
		if got := names(cs); !slices.Equal(got, []string{"s0"}) {
			t.Fatalf("%s: chips %v, want [s0]", tc.label, got)
		}
		if c := cs[0]; !c.Current || c.Performing || c.To != "s1" || c.ToneWord != toneNotRun {
			t.Errorf("%s: %+v, want s0 current, not performing, exit to s1 not yet run", tc.label, c)
		}
	}
	if cs := chipsFor(t, w, "s0", newHist("sto_n", same), mirror.Seat{Item: "sto_n", Session: "work1"}); len(cs) != 1 || !cs[0].Performing {
		t.Errorf("an engaged never-moved story: %+v, want one performing chip", cs)
	}
	if cs := chipsFor(t, w, "t1", newHist("sto_n", same)); cs != nil {
		t.Errorf("a never-moved item in a terminal state: %v, want no chip", names(cs))
	}

	fix := &mirror.Workflow{Name: "fix", Panel: "Drift",
		States:      []mirror.WorkflowState{{Name: "open", Start: true}, {Name: "done", Terminal: true}},
		Transitions: []mirror.WorkflowTransition{{From: []string{"open"}, To: "done", Groups: []mirror.WorkflowGroup{{Mode: "sequential", Gates: []string{"drift-review"}}}}},
	}
	drift := Item{ID: "sto_drift", Kind: "story", Title: "Drift fix", Status: "open", Workflow: "fix"}
	closed := Item{ID: "sto_closed", Kind: "story", Title: "Closed fix", Status: "done", Workflow: "fix"}
	fresh := Item{ID: "sto_fresh", Kind: "story", Title: "Fresh", Status: "s0", Workflow: "pw"}
	srv := mirrorWith(t, progressSnapshot([]*mirror.Workflow{w, fix}, []Item{drift, closed, fresh}, nil, nil, nil))
	page := httpGetBody(t, srv.URL+"/r/proj/")
	topic := panelTopic("Drift")
	sec := section(page, topic)
	cell := regexp.MustCompile(`data-expand-url="fragment/story/sto_drift"[\s\S]*?<td class="col-reviews">(.*?)</td>`).FindStringSubmatch(sec)
	if cell == nil || !strings.Contains(cell[1], `<span class="stage-chip stage-current"`) || !strings.Contains(cell[1], `>open <b class="gates tone-idle">1</b></span>`) || !strings.Contains(cell[1], "drift-review") {
		t.Errorf("the open drift fix item's progress cell is not its open chip with its one exit gate: %v", cell)
	}
	if !regexp.MustCompile(`data-panel="` + topic + `"[^>]*>.*?<span class="n">1</span>`).MatchString(page) {
		t.Error("the Drift tab does not count its one open item")
	}
	if cell := regexp.MustCompile(`data-expand-url="fragment/story/sto_fresh"[\s\S]*?<td class="col-reviews">(.*?)</td>`).FindStringSubmatch(section(page, "stories")); cell == nil || !strings.Contains(cell[1], `>s0 <b class="gates tone-idle">`) {
		t.Errorf("the never-moved backlog story shows no s0 chip: %v", cell)
	}
}

func TestProgressParksNamedByWorkflow(t *testing.T) {
	for _, name := range []string{"blocked", "on-hold"} {
		w := parkedWorkflow(name)
		h := newHist("sto_k", same).place("s0").move("s0", "s1").move("s1", name).move(name, "s1")
		c := chip(t, chipsFor(t, w, "s1", h), "s1")
		if !slices.Equal(c.Parks, []parkVM{{State: name, N: 1}}) {
			t.Errorf("%s: parks %+v, want one %s", name, c.Parks, name)
		}
		if want := name + " once while at this step"; !strings.Contains(c.Title, want) {
			t.Errorf("%s: title %q lacks %q", name, c.Title, want)
		}
		row := renderRows(t, []rowVM{rowOf(w, "s1", h, chipsFor(t, w, "s1", h))})
		if !strings.Contains(row, `<b class="blk">`+name+` 1</b>`) {
			t.Errorf("%s: the chip does not read %s 1: %s", name, name, row)
		}
		if parkWord.MatchString(c.Title) || parkWord.MatchString(row) {
			t.Errorf("%s: the chip or its title says park: %q", name, c.Title)
		}
	}

	w := parkedWorkflow("blocked", "on-hold")
	h := newHist("sto_k", same).place("s0").move("s0", "s1").
		move("s1", "blocked").move("blocked", "s1").
		move("s1", "on-hold").move("on-hold", "s1").
		move("s1", "on-hold").move("on-hold", "s1")
	c := chip(t, chipsFor(t, w, "s1", h), "s1")
	if !slices.Equal(c.Parks, []parkVM{{State: "blocked", N: 1}, {State: "on-hold", N: 2}}) {
		t.Errorf("two park states: %+v, want blocked 1 and on-hold 2, counted apart", c.Parks)
	}
	for _, want := range []string{"blocked once while at this step", "on-hold twice while at this step"} {
		if !strings.Contains(c.Title, want) {
			t.Errorf("two park states: title %q lacks %q", c.Title, want)
		}
	}
	row := renderRows(t, []rowVM{rowOf(w, "s1", h, chipsFor(t, w, "s1", h))})
	if !strings.Contains(row, `<b class="blk">blocked 1</b> <b class="blk">on-hold 2</b>`) {
		t.Errorf("two park states are not drawn apart: %s", row)
	}
	if c := chip(t, chipsFor(t, w, "s1", newHist("sto_k", same).place("s0").move("s0", "s1")), "s1"); len(c.Parks) != 0 || parkWord.MatchString(c.Title) || strings.Contains(c.Title, "while at this step") {
		t.Errorf("a step never parked: %+v, want no park named", c)
	}
}
