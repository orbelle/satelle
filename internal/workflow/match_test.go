package workflow

import "testing"

func TestMatchTransitionIsFindsRule(t *testing.T) {
	park := map[string]State{"s": {}, "t": {}, "p": {Park: true, Resumes: true}, "x": {Terminal: true}}
	for _, c := range []struct {
		name            string
		trs             []Transition
		from, to, prior string
		want            int
	}{
		{"explicit beats an earlier wildcard", []Transition{{From: []string{AnyState}, To: "t"}, {From: []string{"s"}, To: "t"}}, "s", "t", "", 1},
		{"first explicit in file order", []Transition{{From: []string{"s"}, To: "t"}, {From: []string{"s"}, To: "t"}}, "s", "t", "", 0},
		{"@prior before explicit, prior matches", []Transition{{From: []string{"s"}, To: Prior}, {From: []string{"s"}, To: "t"}}, "s", "t", "t", 0},
		{"@prior does not match another prior", []Transition{{From: []string{"s"}, To: Prior}, {From: []string{"s"}, To: "t"}}, "s", "t", "x", 1},
		{"explicit before @prior", []Transition{{From: []string{"s"}, To: "t"}, {From: []string{"s"}, To: Prior}}, "s", "t", "t", 0},
		{"no wildcard from a park", []Transition{{From: []string{AnyState}, To: "t"}}, "p", "t", "", -1},
		{"wildcard from an open state", []Transition{{From: []string{AnyState}, To: "t"}}, "s", "t", "", 0},
		{"no match", []Transition{{From: []string{"s"}, To: "t"}}, "s", "x", "", -1},
	} {
		w := &Workflow{Name: "w", States: park, Transitions: c.trs}
		got := MatchTransition(len(w.Transitions), func(i int) ([]string, string) { return w.Transitions[i].From, w.Transitions[i].To }, w.open, c.from, c.to, c.prior)
		if got != c.want {
			t.Errorf("%s: MatchTransition = %d, want %d", c.name, got, c.want)
		}
		tr, err := w.Find(c.from, c.to, c.prior)
		if (c.want < 0) != (err != nil) || (c.want >= 0 && tr != &w.Transitions[c.want]) {
			t.Errorf("%s: Find = %p, %v; want transition %d", c.name, tr, err, c.want)
		}
	}
}
