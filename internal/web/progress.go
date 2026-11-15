package web

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

type stageVM struct {
	Name                string
	Current, Performing bool
	Engaged             string
	Agent               string
	Gates               []string
	To                  string
	Passed              bool
	ToneWord            string
	Rounds              int
	Rejected            []presentationVM
	Parks               int
	Title               string
}

const (
	toneNotRun   = "not yet run"
	tonePassed   = "passed"
	toneRejected = "last round rejected"
	toneErrored  = "last round errored"
	toneNotLeft  = "not left"
	agentUnknown = "a session"
)

func progressOf(w *mirror.Workflow, it Item, events []Entry, seats []mirror.Seat, standing seatStanding, eng *engagementVM) []stageVM {
	if w == nil {
		return nil
	}
	byName := map[string]mirror.WorkflowState{}
	for _, s := range w.States {
		byName[s.Name] = s
	}
	start, placedTo := window(events)
	win := events[start:]
	mv := moves(win)

	var order []string
	for _, m := range mv {
		for _, n := range m {
			if isStep(byName, n) && !slices.Contains(order, n) {
				order = append(order, n)
			}
		}
	}
	if len(order) == 0 {
		return nil
	}

	parks, tracked := parksOf(byName, mv, placedTo)
	cur := ""
	if st, ok := byName[it.Status]; ok {
		switch {
		case isStep(byName, it.Status):
			cur = it.Status
		case st.Park:
			cur = tracked
		}
	}
	if !slices.Contains(order, cur) {
		cur = ""
	}

	all := buildPresentations(events)
	recent := buildPresentations(win)
	var working []mirror.Seat
	for _, s := range seats {
		if st, _ := standing(s); st.Working() {
			working = append(working, s)
		}
	}

	chips := make([]stageVM, len(order))
	for i, name := range order {
		c := &chips[i]
		c.Name, c.Parks = name, parks[name]
		c.ToneWord = toneNotRun
		if t, ok := exitEdge(w, byName, name, order, mv, recent); ok {
			c.To = t.To
			for _, g := range t.Groups {
				for _, n := range g.Gates {
					if !slices.Contains(c.Gates, n) {
						c.Gates = append(c.Gates, n)
					}
				}
			}
			for _, p := range all {
				if p.From == name && p.To == t.To && rejected(p) {
					c.Rounds++
				}
			}
			var last *presentationVM
			for j := range recent {
				if recent[j].From == name && recent[j].To == t.To {
					last = &recent[j]
				}
			}
			switch {
			case last == nil:
			case last.Outcome == outcomeMoved && !refused(*last):
				c.Passed, c.ToneWord = true, tonePassed
			case rejected(*last):
				c.ToneWord = toneRejected
			case refused(*last):
				c.ToneWord = toneErrored
			default:
				c.ToneWord = toneNotLeft
			}
		}
		for _, p := range all {
			if p.From == name && rejected(p) {
				c.Rejected = append(c.Rejected, rejectsOnly(p))
			}
		}
		binding, at, recorded := agentOf(win, name)
		if name == cur {
			c.Current = true
			if eng != nil {
				c.Performing = true
				c.Agent = performerOf(byName[name], working, standing, binding, at, recorded)
				c.Engaged = eng.Words()
			} else if recorded {
				c.Agent = binding
			}
		} else if recorded {
			c.Agent = binding
		}
		c.Title = stageTitle(*c)
	}
	return chips
}

func isStep(byName map[string]mirror.WorkflowState, name string) bool {
	s, ok := byName[name]
	return ok && !s.Terminal && !s.Park
}

func window(events []Entry) (start int, placedTo string) {
	for i := range events {
		if placementAt(events, i) {
			_, to, _ := transitionOf(events[i])
			start, placedTo = i+1, to
		}
	}
	return start, placedTo
}

func placementAt(events []Entry, i int) bool {
	from, _, ok := transitionOf(events[i])
	if !ok {
		return false
	}
	if from == "" {
		return true
	}
	if i == 0 || events[i-1].Kind != mirror.EventUpdated {
		return false
	}
	var f map[string]json.RawMessage
	if json.Unmarshal(events[i-1].Payload, &f) != nil {
		return false
	}
	_, ok = f["workflow"]
	return ok
}

func parksOf(byName map[string]mirror.WorkflowState, mv [][2]string, placedTo string) (map[string]int, string) {
	parks := map[string]int{}
	tracked := ""
	if isStep(byName, placedTo) {
		tracked = placedTo
	}
	for _, m := range mv {
		if isStep(byName, m[0]) {
			tracked = m[0]
		}
		if s, ok := byName[m[1]]; ok && s.Park && s.Resumes && tracked != "" {
			parks[tracked]++
		}
		if isStep(byName, m[1]) {
			tracked = m[1]
		}
	}
	return parks, tracked
}

func exitEdge(w *mirror.Workflow, byName map[string]mirror.WorkflowState, step string, order []string, mv [][2]string, recent []presentationVM) (mirror.WorkflowTransition, bool) {
	before := order[:slices.Index(order, step)]
	var explicit, wild []mirror.WorkflowTransition
	for _, t := range w.Transitions {
		s, ok := byName[t.To]
		if !ok || s.Park || !leavesState(byName, t, step) {
			continue
		}
		if t.To == step || slices.Contains(before, t.To) {
			continue
		}
		if slices.Contains(t.From, step) {
			explicit = append(explicit, t)
		} else {
			wild = append(wild, t)
		}
	}
	wild = slices.DeleteFunc(wild, func(t mirror.WorkflowTransition) bool {
		return slices.ContainsFunc(explicit, func(e mirror.WorkflowTransition) bool { return e.To == t.To })
	})
	forward := slices.Concat(explicit, wild)
	structural := explicit
	if len(structural) == 0 {
		structural = wild
	}
	to := func(set []mirror.WorkflowTransition, target string) (mirror.WorkflowTransition, bool) {
		i := slices.IndexFunc(set, func(t mirror.WorkflowTransition) bool { return t.To == target })
		if i < 0 {
			return mirror.WorkflowTransition{}, false
		}
		return set[i], true
	}
	for i := len(mv) - 1; i >= 0; i-- {
		if mv[i][0] != step {
			continue
		}
		if t, ok := to(forward, mv[i][1]); ok {
			return t, true
		}
	}
	for i := len(recent) - 1; i >= 0; i-- {
		if recent[i].From != step {
			continue
		}
		if t, ok := to(structural, recent[i].To); ok {
			return t, true
		}
	}
	for _, t := range structural {
		if gated(t) {
			return t, true
		}
	}
	if len(structural) > 0 {
		return structural[0], true
	}
	return mirror.WorkflowTransition{}, false
}

func gated(t mirror.WorkflowTransition) bool {
	return slices.ContainsFunc(t.Groups, func(g mirror.WorkflowGroup) bool { return len(g.Gates) > 0 })
}

func agentOf(events []Entry, step string) (binding string, at time.Time, ok bool) {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Kind != mirror.EventPerformerRun {
			continue
		}
		var r mirror.PerformerRun
		if json.Unmarshal(e.Payload, &r) != nil || r.State != step {
			continue
		}
		return r.Binding, e.At, true
	}
	return "", time.Time{}, false
}

func performerOf(s mirror.WorkflowState, working []mirror.Seat, standing seatStanding, binding string, at time.Time, recorded bool) string {
	if recorded {
		dated := true
		var bound time.Time
		for _, seat := range working {
			if seat.BoundAt.IsZero() {
				dated = false
				break
			}
			if seat.BoundAt.After(bound) {
				bound = seat.BoundAt
			}
		}
		if dated && at.After(bound) {
			return binding
		}
	}
	if s.PerformerAgent != "" {
		return s.PerformerAgent
	}
	if _, users := assigneesOf(Item{}, working, standing); len(users) > 0 {
		return strings.Join(users, ", ")
	}
	return agentUnknown
}

func rejected(p presentationVM) bool {
	return slices.ContainsFunc(p.Verdicts, func(v verdictVM) bool { return v.Result == mirror.GateReject })
}

func refused(p presentationVM) bool {
	return slices.ContainsFunc(p.Verdicts, func(v verdictVM) bool {
		return v.Result == mirror.GateReject || v.Result == mirror.GateError
	})
}

func rejectsOnly(p presentationVM) presentationVM {
	q := p
	q.Verdicts = nil
	for _, v := range p.Verdicts {
		if v.Result == mirror.GateReject {
			q.Verdicts = append(q.Verdicts, v)
		}
	}
	return q
}

func stageTitle(c stageVM) string {
	var state string
	switch {
	case c.Performing:
		state = "performing (" + c.Agent + ")"
	case c.Current:
		state = "current, not being worked"
	default:
		state = "complete"
	}
	parts := []string{c.Name + ": " + state}
	if c.Engaged != "" {
		parts = append(parts, c.Engaged)
	}
	if !c.Performing && c.Agent != "" {
		parts = append(parts, "performed by "+c.Agent)
	}
	g := counted(len(c.Gates), "exit gate", "exit gates")
	if c.To != "" {
		g += " to " + c.To
		if len(c.Gates) > 0 {
			g += " (" + strings.Join(c.Gates, ", ") + ")"
		}
	}
	parts = append(parts, g+": "+c.ToneWord,
		counted(c.Rounds, "rejected round", "rejected rounds"),
		counted(c.Parks, "park", "parks"))
	return strings.Join(parts, " · ")
}

func counted(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
