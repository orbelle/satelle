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

func TestProgressBlankUntilTheFirstMove(t *testing.T) {
	w := progressWorkflow(same)
	for _, tc := range []struct {
		label string
		h     *hist
	}{
		{"no events", newHist("sto_n", same)},
		{"only placed", newHist("sto_n", same).place("s0")},
	} {
		if cs := chipsFor(t, w, "s0", tc.h); cs != nil {
			t.Errorf("%s: chips %v, want none until the first move", tc.label, names(cs))
		}
		if cs := chipsFor(t, w, "s0", tc.h, mirror.Seat{Item: "sto_n", Session: "work1"}); cs != nil {
			t.Errorf("%s, engaged: chips %v, want none until the first move", tc.label, names(cs))
		}
	}
	moved := newHist("sto_m", same).place("s0").move("s0", "s1")
	if got := names(chipsFor(t, w, "s1", moved)); !slices.Equal(got, []string{"s0", "s1"}) {
		t.Errorf("after the first move: chips %v, want [s0 s1]", got)
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
	cellOf := func(sec, id string) string {
		m := regexp.MustCompile(`data-expand-url="fragment/story/` + id + `"[\s\S]*?<td class="col-reviews">(.*?)</td>`).FindStringSubmatch(sec)
		if m == nil {
			t.Fatalf("no progress cell for %s", id)
		}
		return m[1]
	}
	if c := cellOf(section(page, topic), "sto_drift"); c != "—" {
		t.Errorf("the open drift fix item that never moved: progress cell %q, want the dash", c)
	}
	if !regexp.MustCompile(`data-panel="` + topic + `"[^>]*>.*?<span class="n">1</span>`).MatchString(page) {
		t.Error("the Drift tab does not count its one open item")
	}
	if c := cellOf(section(page, "stories"), "sto_fresh"); c != "—" {
		t.Errorf("the backlog story that never moved: progress cell %q, want the dash", c)
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
