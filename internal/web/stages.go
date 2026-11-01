package web

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfroute"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// The story list's PROGRESS cell presents the STAGES a story has
// completed and the gates on each step's edge — not one light per ledger row.
// A stage is named by the state a forward transition landed in. Its chip counts
// the gates the workflow declares on the edge it was entered by, and how many of
// them passed and failed in that edge's latest round; while the stage's exit
// gates are running, the current chip counts those instead, with the gates still
// running live (sty_216d5205). The round history — accepted and rejected ROUNDS,
// one attempt id however many reviewers voted (ledger.EdgeRounds) — is spelled
// in the tooltip. Park and recover are not stages: a park is a note on the stage
// whose gate was in flight. Everything here is read-side presentation over rows
// the binary already wrote and the seat's activity stamp — no verdict is decided.

// stageVM is one entry of the PROGRESS cell.
type stageVM struct {
	Name  string
	State string // done | current | performing
	// Gates is what the chip counts: the outgoing edge's running round on a
	// current stage whose exit gates are in flight, else the edge the stage was
	// entered by. Nil when neither applies.
	Gates    *gateCountsVM
	Parked   int    // parks taken while this stage's gate was in flight
	ParkName string // the state those parks went into (the route's park-state name)
	Title    string // tooltip: the counts and the round history spelled in words
	// Performer is the agent binding working on a performing chip's step.
	Performer string

	// The tooltip's material, which the chip no longer shows: the accepted and
	// rejected rounds on the edges into this stage, the state it was last
	// entered from, its entry edge's counts, and its outgoing edge's — presented
	// since the stage was entered, or in flight.
	accepted, rejected int
	from               string
	in, out            *gateCountsVM
}

// gateCountsVM is one edge's gates: how many the workflow declares on it, and
// how many passed, failed and are still running in its latest round.
type gateCountsVM struct {
	Gates, Passed, Failed, Running int
	// Live marks a round whose gates are still running: the running count pulses.
	Live bool
	Edge string // "from → to" the counts are for
	// The edge's round history, for the tooltip.
	accepted, rejected int
	// inFlight marks counts read from the seat's running gate round.
	inFlight bool
}

// seatGateVM is a story's seat activity while its gates are running: the
// round's whole gate count and how many have passed and failed so far. Built
// only from an in-flight, non-stale seat whose stamp is a gate round's.
type seatGateVM struct {
	Total, Passed, Failed int
}

// transition kinds, as the stage builder reads them.
const (
	trStage   = iota // a forward landing in a state — makes a stage
	trPark           // into the route's resume park
	trRecover        // out of the resume park
	trSink           // into a cancel sink: not a park, shown only as the final chip
)

type transition struct {
	pos      int // index into the (sorted) entries
	from, to string
	kind     int
}

// finished reports whether a story at status is past all work: a terminal
// success state or a cancel sink (reviewer-role, never the resume park). With a
// resolved route the Spec decides, so no state name is hardcoded here. A story
// whose category resolved no route (an empty Spec, a non-route workflow) cannot
// ask the Spec, so the DEGRADED fallback applies: the workitem lifecycle names
// done and cancelled — the same pair the rest of the binary reads as a
// lifecycle check.
func finished(spec wfdot.Spec, status string) bool {
	if len(spec.States) > 0 {
		return spec.IsTerminalState(status) || isSink(spec, status)
	}
	return status == workitem.StatusDone || status == workitem.StatusCancelled
}

// isSink is a cancel sink: a park-role state that is neither the resume park nor
// a terminal success marker (a terminal state is a stage, whatever its role).
func isSink(spec wfdot.Spec, name string) bool {
	return spec.IsParkState(name) && !spec.IsResumePark(name) && !spec.IsTerminalState(name)
}

// classifyTransitions labels every status_transition of es. With a resolved
// route the park is the route's own declaration (Spec.IsResumePark), its recover
// the transition out of it, and a cancel sink is neither. With no Spec the
// DEGRADED fallback reads the shape instead: A→X later followed by X→A is a
// park and its recover (the structure of "resumes to origin"); a never-recovered
// park is left to the current-status rule, which names the story's status.
func classifyTransitions(es []ledger.Entry, spec wfdot.Spec) []transition {
	var ts []transition
	for pos, e := range es {
		if e.Kind != ledger.KindStatusTransition {
			continue
		}
		lp := parseEdge(e)
		ts = append(ts, transition{pos: pos, from: lp.From, to: lp.To})
	}
	if len(spec.States) > 0 {
		for i := range ts {
			switch {
			case spec.IsResumePark(ts[i].to):
				ts[i].kind = trPark
			case spec.IsResumePark(ts[i].from):
				ts[i].kind = trRecover
			case isSink(spec, ts[i].to):
				ts[i].kind = trSink
			}
		}
		return ts
	}
	for i := range ts {
		if ts[i].kind != trStage || ts[i].from == ts[i].to {
			continue
		}
		for j := i + 1; j < len(ts); j++ {
			if ts[j].kind == trStage && ts[j].from == ts[i].to && ts[j].to == ts[i].from {
				ts[i].kind, ts[j].kind = trPark, trRecover
				break
			}
		}
	}
	return ts
}

func parseEdge(e ledger.Entry) lightPayload {
	var lp lightPayload
	_ = json.Unmarshal(e.Payload, &lp)
	return lp
}

// buildStages folds a story's ledger into the PROGRESS stages. Entries may arrive newest- or oldest-first; they are re-sorted here.
// Stages are in the order they were first reached — never numbered, never by
// route depth — so a story whose category resolved no route reads the same as
// one that did, and no stage appears twice. stepOf is consulted only to tell an
// on-route status from an unstarted one when the ledger is empty. tags are the
// story's, which decide the tag-scoped gates on each edge; seat is its gate
// activity while its exit gates are running, nil otherwise.
func buildStages(entries []ledger.Entry, status string, seatHeld bool, stepOf func(state string) int, spec wfdot.Spec, tags []string, seat *seatGateVM) []stageVM {
	es := append([]ledger.Entry(nil), entries...)
	ledger.SortEntries(es)
	ts := classifyTransitions(es, spec)
	fin := finished(spec, status)
	var route *wfroute.Route
	if len(spec.States) > 0 {
		r := wfroute.Build(spec, "", tags, nil, nil)
		route = &r
	}

	var stages []stageVM
	at := map[string]int{} // stage name → index in stages
	type park struct{ target, within, name string }
	var parks []park
	lastEdgeTo, curName := "", ""
	offRoute := map[string]bool{}     // edges of a park, recover or sink: not a stage's gate
	lastIn := map[string]transition{} // stage name → the landing that last entered it
	ti := 0
	for _, e := range es {
		switch e.Kind {
		case ledger.KindReviewAccept, ledger.KindReviewReject:
			lastEdgeTo = parseEdge(e).To
		case ledger.KindStatusTransition:
			t := ts[ti]
			ti++
			switch t.kind {
			case trPark:
				offRoute[t.from+"→"+t.to] = true
				// The park interrupted the gate in flight: note it on that gate's
				// target stage if the story ever reaches it, else on the stage the
				// story was in (else the state it left).
				target, within := lastEdgeTo, curName
				if within == "" {
					within = t.from
				}
				if target == "" {
					target = within
				}
				parks = append(parks, park{target, within, t.to})
				lastEdgeTo = ""
			case trRecover:
				offRoute[t.from+"→"+t.to] = true
				lastEdgeTo = ""
			case trSink:
				offRoute[t.from+"→"+t.to] = true
				lastEdgeTo = ""
			default:
				if _, ok := at[t.to]; !ok {
					at[t.to] = len(stages)
					stages = append(stages, stageVM{Name: t.to, State: "done"})
				}
				curName = t.to
				lastIn[t.to] = t
				lastEdgeTo = ""
			}
		}
	}

	// Totals: the rounds on every edge into a stage, across parks and re-entries.
	seen := map[string]bool{}
	for _, e := range es {
		if e.Kind != ledger.KindReviewAccept && e.Kind != ledger.KindReviewReject {
			continue
		}
		lp := parseEdge(e)
		key := lp.From + "→" + lp.To
		i, ok := at[lp.To]
		if !ok || seen[key] || offRoute[key] || routeEdgeOff(spec, lp.From, lp.To) {
			continue
		}
		seen[key] = true
		a, r := ledger.EdgeRounds(es, lp.From, lp.To)
		stages[i].accepted += a
		stages[i].rejected += r
	}
	// Each stage's entry edge: the gates of the landing that last entered it, its
	// latest round accepted (the landing is the accept).
	for i := range stages {
		t, ok := lastIn[stages[i].Name]
		if !ok {
			continue
		}
		stages[i].from = t.from
		if g := completedGates(es[:t.pos], route, t.from, t.to); g != nil {
			g.accepted, g.rejected = ledger.EdgeRounds(es[:t.pos], t.from, t.to)
			stages[i].in, stages[i].Gates = g, g
		}
	}
	for _, p := range parks {
		i, ok := at[p.target]
		if !ok {
			i, ok = at[p.within]
		}
		if ok {
			stages[i].Parked++
			stages[i].ParkName = p.name
		}
	}

	// The current stage. A landed stage named by the status pulses in place; a
	// status with no landing stage (a park the story is still in, a story whose
	// ledger is missing, a cancel sink) gets its own chip. A finished story never
	// pulses — its last chip is simply done.
	entered := len(ts) > 0
	var out *gateCountsVM
	if !fin {
		out = outgoingGates(es, ts, status, route, seat)
	}
	if i, ok := at[status]; ok {
		if !fin {
			stages[i].State = "current"
		}
	} else if fin {
		if entered {
			stages = append(stages, stageVM{Name: status, State: "done"})
		}
	} else if entered || (stepOf != nil && stepOf(status) > 0) {
		stages = append(stages, stageVM{Name: status, State: "current"})
	} else if seatHeld {
		stages = append(stages, stageVM{Name: status, State: "current", Title: "starting"})
	} else if out != nil {
		// A story still at its start status that has only been readied (`satelle
		// story ready` holds no seat and takes no edge) has no transition, step or
		// seat — its readiness rounds are the only trace it is under way. Its chip
		// counts that edge's latest round.
		stages = append(stages, stageVM{Name: status, State: "current", Gates: out, out: out,
			Title: "readying · " + edgeWords(*out)})
	}
	if out != nil {
		for i := range stages {
			if stages[i].State != "current" || stages[i].out != nil {
				continue
			}
			stages[i].out = out
			if out.inFlight {
				// The exit gates are running: the chip counts them, not the entry.
				stages[i].Gates, stages[i].Title = out, ""
			}
		}
	}
	for i := range stages {
		if stages[i].Title == "" {
			stages[i].Title = stageTitle(stages[i])
		}
	}
	return stages
}

// applyPerforming shows the step a dispatched performer is working on. The
// recorded status stays at the origin until the performer hands back, so the
// ledger alone would keep the origin live: with a dispatch for another step in
// flight, the origin chip is done (its outgoing gate has passed) and the
// dispatched step is the performing chip. Read-side only — no status changes.
func applyPerforming(stages []stageVM, status string, d *dispatchVM) []stageVM {
	if d == nil || d.Step == "" || d.Step == status {
		return stages
	}
	for i := range stages {
		if stages[i].Name == status && stages[i].State == "current" {
			stages[i].State, stages[i].out, stages[i].Gates = "done", nil, stages[i].in
			stages[i].Title = stageTitle(stages[i])
		}
	}
	for i := range stages {
		if stages[i].Name == d.Step {
			stages[i].State, stages[i].Performer = "performing", d.Agent
			stages[i].Title = stageTitle(stages[i])
			return stages
		}
	}
	s := stageVM{Name: d.Step, State: "performing", Performer: d.Agent}
	s.Title = stageTitle(s)
	return append(stages, s)
}

// routeEdgeOff reports whether a review edge is a park, recover or sink edge of
// the resolved route — its rounds belong to no stage's gate.
func routeEdgeOff(spec wfdot.Spec, from, to string) bool {
	if len(spec.States) == 0 {
		return false
	}
	return spec.IsResumePark(from) || spec.IsResumePark(to) || isSink(spec, to)
}

// outgoingGates is the current stage's exit edge since the story entered status:
// running — the seat's live tally for its gate round, with the edge named by the
// route step after status — or, with nothing running, the latest presented
// round's counts (an edge out of status with verdict rows since the entry), for
// the tooltip. Nil when neither: the gates are not running and nothing has been
// presented since the entry.
func outgoingGates(es []ledger.Entry, ts []transition, status string, route *wfroute.Route, seat *seatGateVM) *gateCountsVM {
	// The last transition into status that is not a recover: a recovery returns a
	// story to the state it left, so its entry gate is the one it first arrived by.
	enterPos := -1
	for _, t := range ts {
		if t.to == status && t.kind != trRecover {
			enterPos = t.pos
		}
	}
	since := es[enterPos+1:]
	to := ""
	for _, e := range since {
		if e.Kind != ledger.KindReviewAccept && e.Kind != ledger.KindReviewReject {
			continue
		}
		if lp := parseEdge(e); lp.From == status {
			to = lp.To
		}
	}
	if seat != nil {
		if next := nextStep(route, status); next != "" {
			to = next
		}
		g := &gateCountsVM{Gates: seat.Total, Passed: seat.Passed, Failed: seat.Failed, Edge: status + " →", inFlight: true}
		if to != "" {
			g.Edge = status + " → " + to
			g.accepted, g.rejected = ledger.EdgeRounds(since, status, to)
		}
		g.Running = max(g.Gates-g.Passed-g.Failed, 0)
		g.Live = g.Running > 0
		return g
	}
	if to == "" {
		return nil
	}
	a, r := ledger.EdgeRounds(since, status, to)
	if a == 0 && r == 0 {
		return nil
	}
	g := closedGates(since, route, status, to)
	g.accepted, g.rejected = a, r
	return g
}

// nextStep is the route step after status, "" when status is not a step of the
// route or is its last.
func nextStep(route *wfroute.Route, status string) string {
	if route == nil {
		return ""
	}
	for i, st := range route.Steps {
		if st.Status == status && i+1 < len(route.Steps) {
			return route.Steps[i+1].Status
		}
	}
	return ""
}

// completedGates is an edge the story advanced along: its latest round was
// accepted, so every gate on it passed. Nil when the edge has no gates to count.
func completedGates(es []ledger.Entry, route *wfroute.Route, from, to string) *gateCountsVM {
	n := len(edgeGateSet(es, route, from, to))
	if n == 0 {
		return nil
	}
	return &gateCountsVM{Gates: n, Passed: n, Edge: from + " → " + to}
}

// closedGates is an edge's latest round that has returned: its gate count, and
// the gates that passed and failed in it (ledger.LatestRoundVerdicts). A
// serial round stops at its first reject, so passed plus failed may fall short
// of the count.
func closedGates(es []ledger.Entry, route *wfroute.Route, from, to string) *gateCountsVM {
	g := &gateCountsVM{Gates: len(edgeGateSet(es, route, from, to)), Edge: from + " → " + to}
	for _, v := range ledger.LatestRoundVerdicts(es, from, to) {
		if v == "accept" {
			g.Passed++
		} else {
			g.Failed++
		}
	}
	return g
}

// edgeGateSet is the from→to edge's gates for its latest round in es: the entry
// gates the route declares on step to for the story's tags (wfroute leaves out a
// scoped gate applies_to excludes), less any `when` gate that did not trigger —
// one with no verdict in the round and a scoped-gate-skipped row (reason "when")
// written for it in the round. With no route, or to not a step of it, the
// DEGRADED fallback is the skills with a verdict in the latest round.
func edgeGateSet(es []ledger.Entry, route *wfroute.Route, from, to string) []string {
	verdicts := ledger.LatestRoundVerdicts(es, from, to)
	var set []string
	if step := routeStep(route, to); step != nil {
		lo, hi := latestRoundWindow(es, from, to)
		for _, rv := range step.Reviewers {
			if _, voted := verdicts[rv.Skill]; rv.When != "" && !voted && whenSkipped(es, rv.Skill, to, lo, hi) {
				continue
			}
			set = append(set, rv.Skill)
		}
	}
	if len(set) == 0 {
		for skill := range verdicts {
			set = append(set, skill)
		}
	}
	return set
}

func routeStep(route *wfroute.Route, status string) *wfroute.Step {
	if route == nil {
		return nil
	}
	for i := range route.Steps {
		if route.Steps[i].Status == status {
			return &route.Steps[i]
		}
	}
	return nil
}

// latestRoundWindow bounds the from→to edge's latest round in es as (lo, hi]:
// from the previous presentation's last row — or the last transition into from
// before the round — to the round's last verdict row. A closed round's skip rows
// precede its verdict rows: a `when` gate is skipped as the round is enqueued.
func latestRoundWindow(es []ledger.Entry, from, to string) (lo, hi int) {
	ps := ledger.EdgePresentations(es, from, to)
	lo, hi = -1, len(es)-1
	if len(ps) == 0 {
		return lo, hi
	}
	last := ps[len(ps)-1]
	if n := len(last.Verdicts); n > 0 {
		hi = last.Verdicts[n-1]
	}
	first := hi
	if len(last.Verdicts) > 0 {
		first = last.Verdicts[0]
	}
	if len(last.Invocations) > 0 && last.Invocations[0] < first {
		first = last.Invocations[0]
	}
	if len(ps) > 1 {
		prev := ps[len(ps)-2]
		for _, rows := range [][]int{prev.Verdicts, prev.Invocations, prev.Performers} {
			for _, i := range rows {
				if i > lo && i < first {
					lo = i
				}
			}
		}
		return lo, hi
	}
	for i := first - 1; i >= 0; i-- {
		if es[i].Kind == ledger.KindStatusTransition && parseEdge(es[i]).To == from {
			return i, hi
		}
	}
	return lo, hi
}

// whenSkipped reports whether es holds, in (lo, hi], the engine's
// scoped-gate-skipped telemetry row for skill entering to with reason "when".
func whenSkipped(es []ledger.Entry, skill, to string, lo, hi int) bool {
	for i := lo + 1; i <= hi && i < len(es); i++ {
		if es[i].Kind != ledger.KindTelemetryEvent {
			continue
		}
		var p struct {
			Kind string `json:"kind"`
			Data struct {
				Reason string `json:"reason"`
				Skill  string `json:"skill"`
				To     string `json:"to"`
			} `json:"data"`
		}
		if json.Unmarshal(es[i].Payload, &p) == nil && p.Kind == "scoped-gate-skipped" &&
			p.Data.Reason == "when" && p.Data.Skill == skill && p.Data.To == to {
			return true
		}
	}
	return false
}

// stageTitle spells a chip in words. A current stage whose exit gates are
// running leads with that edge; the stage's own part names how it was entered,
// its entry edge's gates and its round history; an outgoing edge presented and
// not running follows it.
func stageTitle(s stageVM) string {
	var segs []string
	running := s.out != nil && s.out.inFlight && s.Gates == s.out
	if running {
		segs = append(segs, edgeWords(*s.out))
	}
	var quals []string
	switch s.State {
	case "current":
		quals = append(quals, "current stage")
	case "performing":
		quals = append(quals, "performing: "+s.Performer)
	}
	if s.from != "" {
		quals = append(quals, "entered from "+s.from)
	}
	head := s.Name
	if len(quals) > 0 {
		head += " (" + strings.Join(quals, ", ") + ")"
	}
	var parts []string
	if s.in != nil {
		parts = append(parts, gatesWords(*s.in))
	}
	if w := roundsWords(s.accepted, s.rejected); w != "" {
		parts = append(parts, "rounds: "+w)
	}
	switch {
	case s.Parked == 1:
		parts = append(parts, s.ParkName+" once")
	case s.Parked > 1:
		parts = append(parts, fmt.Sprintf("%s %d times", s.ParkName, s.Parked))
	}
	if len(parts) > 0 {
		head += ": " + strings.Join(parts, "; ")
	}
	segs = append(segs, head)
	if s.out != nil && !running {
		segs = append(segs, edgeWords(*s.out))
	}
	return strings.Join(segs, " · ")
}

// edgeWords spells one edge: its name, its latest round's gates, and its round
// history.
func edgeWords(g gateCountsVM) string {
	parts := []string{}
	if g.Gates > 0 {
		parts = append(parts, gatesWords(g))
	}
	if w := roundsWords(g.accepted, g.rejected); w != "" {
		parts = append(parts, "rounds: "+w)
	}
	if len(parts) == 0 {
		return g.Edge
	}
	return g.Edge + ": " + strings.Join(parts, "; ")
}

// gatesWords spells a round's gate counts, so a number is never mistaken for a
// stage index; the running count only while gates are running.
func gatesWords(g gateCountsVM) string {
	noun := "gates"
	if g.Gates == 1 {
		noun = "gate"
	}
	w := fmt.Sprintf("%d %s, %d passed, %d failed", g.Gates, noun, g.Passed, g.Failed)
	if g.Running > 0 {
		w += fmt.Sprintf(", %d still running", g.Running)
	}
	return w
}

// roundsWords spells round totals in words — "1 accepted, 2 rejected" — the
// history the chip no longer shows.
func roundsWords(accepted, rejected int) string {
	var parts []string
	if accepted > 0 {
		parts = append(parts, fmt.Sprintf("%d accepted", accepted))
	}
	if rejected > 0 {
		parts = append(parts, fmt.Sprintf("%d rejected", rejected))
	}
	return strings.Join(parts, ", ")
}

// categorySpecOf resolves each category's derived-route Spec, memoised per
// category for one page load (an empty Spec when no route resolves — the read
// surface degrades, never refuses). The same shared front door as the depths
// categoryStepOf numbers by.
func categorySpecOf(docs []docindex.Doc) func(category string) wfdot.Spec {
	memo := map[string]wfdot.Spec{}
	return func(category string) wfdot.Spec {
		if s, ok := memo[category]; ok {
			return s
		}
		s := routeSpecFor(docs, category)
		memo[category] = s
		return s
	}
}
