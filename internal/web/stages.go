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
// A stage is named by a state the story was in: the start state once the story
// has left it, and every state a forward transition landed in. Its chip counts
// the step's EXIT gates — the gates on the edge leaving it, which judge the work
// done in it (sty_0b1abacc) — and how many times they rejected that work across
// every round (ledger.EdgeRounds: one attempt id is one round, however many
// reviewers voted). The step being worked now shows its exit gates as not yet
// run, or, while they run, how many are running. Park and recover are not
// stages: a park is a note on the stage whose gate was in flight. Everything
// here is read-side presentation over rows the binary already wrote and the
// seat's activity stamp — no verdict is decided.

// stageVM is one entry of the PROGRESS cell.
type stageVM struct {
	Name  string
	State string // done | current | performing
	// Gates is the step's exit gates (exitGates): the edge leaving it. Nil when
	// the step has no gated exit edge to count.
	Gates    *gateCountsVM
	Parked   int    // parks taken while this stage's gate was in flight
	ParkName string // the state those parks went into (the route's park-state name)
	Title    string // tooltip: the exit gates and their rejections in words
	// Performer is the agent binding working on a performing chip's step.
	Performer string
}

// gateCountsVM is one step's exit gates: how many the workflow declares on the
// edge leaving it, and how many times they rejected the step's work.
type gateCountsVM struct {
	Gates int
	// Rejected is the edge's rejected rounds over the whole history.
	Rejected int
	// Passed and Failed are the seat's running round so far, read only to derive
	// Running; the chip never shows them.
	Passed, Failed, Running int
	// Live marks a round whose gates are still running: the running count pulses.
	Live bool
	// NotRun marks exit gates of the step being worked that have not run since
	// it was entered.
	NotRun bool
	// Tone is the gate count's colour on the chip: "live" (orange) while a
	// round's gates are still running, "ok" (green) when they all passed (a taken
	// edge, or a seat round with every gate passed), otherwise "idle" (grey) — not
	// yet run, or nothing running (a rejected round, a park or cancel out of the
	// step). The names keep the words the chip no longer shows ("running",
	// "passed") out of its markup.
	Tone string
	Edge string // "from → to" the counts are for
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
	route := stageRoute(spec, tags)

	var stages []stageVM
	at := map[string]int{} // stage name → index in stages
	type park struct{ target, within, name string }
	var parks []park
	lastEdgeTo, curName := "", ""
	offRoute := map[string]bool{} // edges of a park, recover or sink: not a stage's gate
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
				// The start state is a step like any other: once the story leaves
				// it, it has a chip that carries its exit gates.
				if _, ok := at[t.from]; !ok && len(stages) == 0 && t.from != "" && t.from != t.to {
					at[t.from] = len(stages)
					stages = append(stages, stageVM{Name: t.from, State: "done"})
				}
				if _, ok := at[t.to]; !ok {
					at[t.to] = len(stages)
					stages = append(stages, stageVM{Name: t.to, State: "done"})
				}
				curName = t.to
				lastEdgeTo = ""
			}
		}
	}
	off := func(from, to string) bool { return offRoute[from+"→"+to] || routeEdgeOff(spec, from, to) }

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
	var curGates *gateCountsVM
	if !fin {
		curGates = exitGates(es, ts, status, true, route, off, seat)
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
	} else if seatHeld && curGates == nil {
		stages = append(stages, stageVM{Name: status, State: "current", Title: "starting"})
	} else if seatHeld || (curGates != nil && !curGates.NotRun) {
		// A story still at its start status with its exit gates running, or that
		// has only been readied (`satelle story ready` holds no seat and takes no
		// edge): those rounds are the only trace it is under way.
		stages = append(stages, stageVM{Name: status, State: "current"})
	}
	for i := range stages {
		if stages[i].State == "current" {
			stages[i].Gates = curGates
		} else {
			stages[i].Gates = exitGates(es, ts, stages[i].Name, false, route, off, nil)
		}
		if stages[i].Title == "" {
			stages[i].Title = stageTitle(stages[i])
		}
	}
	return stages
}

// stageRoute is the route the stage builder reads gate counts from: the Spec's
// derived route for the story's tags, nil when no route resolved.
func stageRoute(spec wfdot.Spec, tags []string) *wfroute.Route {
	if len(spec.States) == 0 {
		return nil
	}
	r := wfroute.Build(spec, "", tags, nil, nil)
	return &r
}

// applyPerforming shows the step a dispatched performer is working on. The
// recorded status stays at the origin until the performer hands back, so the
// ledger alone would keep the origin live: with a dispatch for another step in
// flight, the origin chip is done, keeping its exit gates (the edge it is
// leaving), and the dispatched step is the performing chip, its own exit gates
// not yet run. Read-side only — no status changes.
func applyPerforming(stages []stageVM, status string, d *dispatchVM, route *wfroute.Route) []stageVM {
	if d == nil || d.Step == "" || d.Step == status {
		return stages
	}
	for i := range stages {
		if stages[i].Name == status && stages[i].State == "current" {
			stages[i].State = "done"
			stages[i].Title = stageTitle(stages[i])
		}
	}
	g := notRunGates(route, d.Step)
	for i := range stages {
		if stages[i].Name == d.Step {
			if prev := stages[i].Gates; g != nil && prev != nil && prev.Edge == g.Edge {
				g.Rejected = prev.Rejected
			}
			stages[i].State, stages[i].Performer, stages[i].Gates = "performing", d.Agent, g
			stages[i].Title = stageTitle(stages[i])
			return stages
		}
	}
	s := stageVM{Name: d.Step, State: "performing", Performer: d.Agent, Gates: g}
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

// exitEdge is the edge leaving stage — the gates on an edge judge the step
// being left (sty_0b1abacc), so this is the same rule for every step, the start
// state included. A step the story has left is judged by the last forward
// transition out of it; es[:upTo] then holds that edge's rounds. A step being
// worked (current) has not left since it was last entered: its exit edge is the
// one presented since that entry, else the route's next step. A step left by no
// forward transition (a park or cancel out of it) falls back to the last edge
// presented out of it. ran reports a taken edge or one with verdict rows; taken
// reports only the former — a forward transition out of the step, so its exit
// gates passed — never verdict rows alone; to is "" when the step has no exit
// edge to name.
func exitEdge(route *wfroute.Route, es []ledger.Entry, ts []transition, stage string, current bool, off func(from, to string) bool) (to string, upTo int, ran, taken bool) {
	upTo, from := len(es), 0
	if current {
		// A recovery returns a story to the state it left, so the entry is the one
		// it first arrived by.
		for _, t := range ts {
			if t.to == stage && t.kind != trRecover {
				from = t.pos + 1
			}
		}
	} else {
		for _, t := range ts {
			if t.kind == trStage && t.from == stage && t.from != t.to {
				to, upTo, ran = t.to, t.pos, true
			}
		}
		if ran {
			return to, upTo, true, true
		}
	}
	for _, e := range es[from:] {
		if e.Kind != ledger.KindReviewAccept && e.Kind != ledger.KindReviewReject {
			continue
		}
		if lp := parseEdge(e); lp.From == stage && !off(lp.From, lp.To) {
			to, ran = lp.To, true
		}
	}
	if to == "" && current {
		to = nextStep(route, stage)
	}
	return to, upTo, ran, false
}

// exitGates is a step's exit gates: the edge exitEdge names, its gate count and
// its rejected rounds over the whole history. On the step being worked, seat is
// its gate round while it runs (the counts are that round's, the running count
// live), and an edge with no round since the step was entered is not yet run.
// Nil when the step has no exit edge or no gates on it.
func exitGates(es []ledger.Entry, ts []transition, stage string, current bool, route *wfroute.Route, off func(from, to string) bool, seat *seatGateVM) *gateCountsVM {
	to, upTo, ran, taken := exitEdge(route, es, ts, stage, current, off)
	if current && seat != nil {
		if next := nextStep(route, stage); next != "" {
			to = next
		}
		g := &gateCountsVM{Gates: seat.Total, Passed: seat.Passed, Failed: seat.Failed, Edge: stage + " →"}
		if to != "" {
			g.Edge = stage + " → " + to
			_, g.Rejected = ledger.EdgeRounds(es, stage, to)
		}
		g.Running = max(g.Gates-g.Passed-g.Failed, 0)
		g.Live = g.Running > 0
		switch {
		case g.Live:
			g.Tone = "live"
		case g.Gates > 0 && g.Passed == g.Gates:
			g.Tone = "ok"
		default:
			g.Tone = "idle"
		}
		return g
	}
	if to == "" {
		return nil
	}
	if !ran {
		g := notRunGates(route, stage)
		if g != nil {
			_, g.Rejected = ledger.EdgeRounds(es, stage, to)
		}
		return g
	}
	n := len(edgeGateSet(es[:upTo], route, stage, to))
	if n == 0 {
		return nil
	}
	g := &gateCountsVM{Gates: n, Tone: "idle", Edge: stage + " → " + to}
	if taken {
		g.Tone = "ok"
	}
	_, g.Rejected = ledger.EdgeRounds(es, stage, to)
	return g
}

// notRunGates is the exit gates of a step being worked whose round has not run:
// the reviewers the route declares on its next step. Nil when the route names no
// next step or no gate on it.
func notRunGates(route *wfroute.Route, stage string) *gateCountsVM {
	to := nextStep(route, stage)
	step := routeStep(route, to)
	if step == nil || len(step.Reviewers) == 0 {
		return nil
	}
	return &gateCountsVM{Gates: len(step.Reviewers), NotRun: true, Tone: "idle", Edge: stage + " → " + to}
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

// stageTitle spells a chip in words: "<step>: <N> exit gates, <R> rejections",
// the rejections only when there were any, then whether the exit gates are not
// yet run or how many are running, then the park and performer notes.
func stageTitle(s stageVM) string {
	var parts []string
	if g := s.Gates; g != nil && g.Gates > 0 {
		w := countWords(g.Gates, "exit gate", "exit gates")
		if g.Rejected > 0 {
			w += ", " + countWords(g.Rejected, "rejection", "rejections")
		}
		if g.Live {
			w += fmt.Sprintf(", %d running", g.Running)
		} else if g.NotRun {
			w += ", not yet run"
		}
		parts = append(parts, w)
	}
	switch {
	case s.Parked == 1:
		parts = append(parts, s.ParkName+" once")
	case s.Parked > 1:
		parts = append(parts, fmt.Sprintf("%s %d times", s.ParkName, s.Parked))
	}
	if s.State == "performing" && s.Performer != "" {
		parts = append(parts, "performing: "+s.Performer)
	}
	if len(parts) == 0 {
		return s.Name
	}
	return s.Name + ": " + strings.Join(parts, "; ")
}

// countWords spells a count with its noun, so a number is never mistaken for a
// stage index.
func countWords(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
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
