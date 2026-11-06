package workflow

import (
	"fmt"
	"slices"
	"strings"
)

// SkillLookup finds a skill by name.
type SkillLookup func(name string) (Skill, bool)

// AgentLookup reports whether an agent binding exists.
type AgentLookup func(name string) bool

// Validate returns every problem with the workflow; none means it is usable.
func (w *Workflow) Validate(skill SkillLookup, agent AgentLookup) []error {
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("workflow %s: "+format, append([]any{w.Name}, args...)...))
	}
	var starts, terminals []string
	for _, name := range w.stateNames() {
		st := w.States[name]
		if st.Start {
			starts = append(starts, name)
		}
		if st.Terminal {
			terminals = append(terminals, name)
		}
		if st.Terminal && st.Park {
			bad("state %q is both terminal and park", name)
		}
		if st.Start && (st.Terminal || st.Park) {
			bad("start state %q may not be terminal or park", name)
		}
		if st.Resumes && !st.Park {
			bad("state %q resumes but is not a park state", name)
		}
		seenQ := map[string]bool{}
		for _, q := range st.Enqueue {
			switch {
			case strings.TrimSpace(q) == "":
				bad("state %q enqueues into an empty queue name", name)
			case seenQ[q]:
				bad("state %q enqueues into %s twice", name, q)
			}
			seenQ[q] = true
		}
		if p := st.Performer; p != nil {
			if p.Agent == "" || !agent(p.Agent) {
				bad("state %q performer agent %q is not in the agents catalog", name, p.Agent)
			}
			if _, ok := skill(p.Skill); p.Skill == "" || !ok {
				bad("state %q performer skill %q is not in skills/", name, p.Skill)
			}
		}
	}
	if len(starts) != 1 {
		bad("needs exactly one start state, has %d %v", len(starts), starts)
	}
	if len(terminals) == 0 {
		bad("needs at least one terminal state")
	}
	for name, g := range w.Gates {
		errs = append(errs, w.validateGate(name, g, skill, agent)...)
	}
	type pair struct{ from, to string }
	seen := map[pair]bool{}
	for i, t := range w.Transitions {
		where := fmt.Sprintf("transition %d (%s -> %s)", i+1, strings.Join(t.From, ","), t.To)
		if len(t.From) == 0 {
			bad("%s has no from", where)
		}
		for _, f := range t.From {
			switch st, ok := w.States[f]; {
			case f == AnyState:
				if t.To == Prior {
					bad("%s: %q cannot return to %s (it excludes park states)", where, AnyState, Prior)
				}
			case !ok:
				bad("%s: from state %q does not exist", where, f)
			case st.Terminal:
				bad("%s: from state %q is terminal", where, f)
			case t.To == Prior && !(st.Park && st.Resumes):
				bad("%s: %s is only allowed from a resuming park state, not %q", where, Prior, f)
			}
			if seen[pair{f, t.To}] {
				bad("%s duplicates an earlier transition", where)
			}
			seen[pair{f, t.To}] = true
		}
		if _, ok := w.States[t.To]; !ok && t.To != Prior {
			bad("%s: to state %q does not exist", where, t.To)
		}
		for j, g := range t.Groups {
			if g.Mode != Sequential && g.Mode != Parallel {
				bad("%s group %d: mode %q is not %s or %s", where, j+1, g.Mode, Sequential, Parallel)
			}
			if len(g.Gates) == 0 {
				bad("%s group %d has no gates", where, j+1)
			}
			for _, name := range g.Gates {
				if _, ok := w.Gates[name]; !ok {
					bad("%s group %d: gate %q is not defined", where, j+1, name)
				}
			}
		}
	}
	if len(starts) == 1 {
		reached := w.reachable(starts[0])
		anyTerminal := false
		for _, name := range w.stateNames() {
			if !reached[name] && !w.States[name].Terminal {
				bad("state %q is not reachable from start %q", name, starts[0])
			}
			if reached[name] && w.States[name].Terminal {
				anyTerminal = true
			}
		}
		if len(terminals) > 0 && !anyTerminal {
			bad("no terminal state is reachable from start %q", starts[0])
		}
	}
	return errs
}

func (w *Workflow) validateGate(name string, g Gate, skill SkillLookup, agent AgentLookup) []error {
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("workflow %s: gate %q: "+format, append([]any{w.Name, name}, args...)...))
	}
	sk, ok := skill(g.Skill)
	if g.Skill == "" || !ok {
		bad("skill %q is not in skills/", g.Skill)
	}
	switch {
	case g.Agent != "" && len(g.Panel) > 0:
		bad("names both agent and panel")
	case ok && sk.IsCheck() && len(g.Judges()) > 0:
		bad("skill %q is a check; a check runs no agent", g.Skill)
	case ok && !sk.IsCheck() && len(g.Judges()) == 0:
		bad("names neither agent nor panel")
	}
	for _, a := range g.Judges() {
		if !agent(a) {
			bad("agent %q is not in the agents catalog", a)
		}
	}
	if g.Combine != "" && len(g.Panel) == 0 {
		bad("combine is only meaningful with a panel")
	}
	if len(g.Panel) > 0 && !slices.Contains([]string{CombineAll, CombineAny, CombineMost}, g.Combine) {
		bad("combine %q is not %s, %s or %s", g.Combine, CombineAll, CombineAny, CombineMost)
	}
	if g.Trigger != "" {
		if t, ok := skill(g.Trigger); !ok || !t.IsCheck() {
			bad("trigger %q is not a check skill in skills/", g.Trigger)
		}
	}
	for _, p := range g.Paths {
		if strings.TrimSpace(p) == "" {
			bad("has an empty path pattern")
		}
	}
	return errs
}

// reachable walks transitions from start. "*" stands for every open state; a
// "@prior" edge returns to any state with a transition into the park state.
func (w *Workflow) reachable(start string) map[string]bool {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for _, t := range w.Transitions {
			if !slices.Contains(t.From, s) && !(slices.Contains(t.From, AnyState) && w.open(s)) {
				continue
			}
			targets := []string{t.To}
			if t.To == Prior {
				targets = w.enteredFrom(s)
			}
			for _, to := range targets {
				if _, ok := w.States[to]; ok && !seen[to] {
					seen[to] = true
					queue = append(queue, to)
				}
			}
		}
	}
	return seen
}

// enteredFrom lists the states with a transition into park.
func (w *Workflow) enteredFrom(park string) []string {
	var out []string
	for _, t := range w.Transitions {
		if t.To != park {
			continue
		}
		for _, f := range t.From {
			if f == AnyState {
				for _, n := range w.stateNames() {
					if w.open(n) {
						out = append(out, n)
					}
				}
			} else {
				out = append(out, f)
			}
		}
	}
	return out
}
