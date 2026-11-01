package ledger

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"uuid"
)

// Presentation rounds and definition edits over a story's ledger (sty_5262592e).
//
// These are ENUMERATIONS of rows the binary already wrote — how many times an
// edge was presented and rejected, which definition edits postdate an accepted
// edge. Neither decides anything: the budget a count is compared against is a
// step's declared reject_budget, and whether an edit blocks an advance is a
// gate's configuration. Pure functions over entries, so they are testable
// without a store and callable from any layer that already holds the rows.

// NewAttemptID returns a fresh id for ONE presentation of an edge to its gate.
// Every verdict row a single gate run writes carries it, so a round is countable
// however many reviewers the gate fanned out to (parallel or serial).
func NewAttemptID() string { return fmt.Sprintf("att_%s", uuid.NewV4().String()[:8]) }

// NewBundleID returns a fresh id for ONE bundled reviewer session (sty_23e10d92):
// stamped on the session's single agent_invocation row and on every verdict row
// it produced, so the rows of one measured call can be found together.
func NewBundleID() string { return fmt.Sprintf("bun_%s", uuid.NewV4().String()[:8]) }

// edgeRow is the from/to/attempt/notes shape a review or transition row's
// payload carries. Extra keys are ignored.
type edgeRow struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Skill   string `json:"skill"`
	Notes   string `json:"notes"`
	Attempt string `json:"attempt"`
	Agent   string `json:"agent"`
	// Operation names the lifecycle hook (create_review, amend_review) a reviewer
	// row records; empty on an edge's own rows.
	Operation string `json:"operation"`
	// StepSummary marks the step summariser's own invocation row, which
	// evidences no presentation.
	StepSummary bool `json:"step_summary"`
}

func decodeEdgeRow(e Entry) (edgeRow, bool) {
	var r edgeRow
	if len(e.Payload) == 0 || json.Unmarshal(e.Payload, &r) != nil {
		return edgeRow{}, false
	}
	return r, true
}

// RejectedRounds is the outcome of counting an edge's rejected presentations.
type RejectedRounds struct {
	// Rounds is the number of distinct presentations with at least one reject.
	Rounds int
	// LastSkill and LastNotes are the newest reject row's reviewer and objection.
	LastSkill string
	LastNotes string
}

// AttemptOutcome is the verdict of ONE presentation (one attempt id) of an edge.
// It is the single place that decides whether a presentation was accepted or
// rejected: every counter and every reader of readiness asks it, so the
// edge-rounds tally, the reject budget and readiness-holds cannot disagree
// (sty_a32bd6a9).
type AttemptOutcome struct {
	// Attempt is the id the attempt's rows carry; "legacy-N" for a row written
	// before attempt ids existed, each of which is a round of its own.
	Attempt string
	// Rejected is true when any row of the attempt is a reviewer reject or a
	// performer reject (a performer that rejected the premise or failed to run).
	Rejected bool
	// Accepted is the complement: no row of the attempt rejected.
	Accepted bool
	// Skill and Notes are the newest rejecting row's reviewer (or performer) and
	// objection; empty on an accepted attempt.
	Skill string
	Notes string
	// AcceptAt is the index in the entries slice of the attempt's
	// readiness_accept row, or -1 when it has none.
	AcceptAt int
	// Rows are the indexes in the entries slice of the attempt's verdict rows,
	// oldest first.
	Rows []int
}

func isRejectKind(kind string) bool {
	return kind == KindReviewReject || kind == KindPerformerReject
}

func isVerdictKind(kind string) bool {
	return isRejectKind(kind) || kind == KindReviewAccept || kind == KindReadinessAccept
}

// ProgressKinds are the kinds a story's progress (its stages and gate rounds) is
// built from: its status transitions and every verdict kind. A reader that has
// all of a story's rows of these kinds has all it needs to fold its progress, so
// an export can send them whatever their age. A superset of isVerdictKind, kept
// apart from it: a status_transition is never a verdict.
var ProgressKinds = []string{KindStatusTransition, KindReviewAccept, KindReviewReject, KindPerformerReject, KindReadinessAccept}

// edgeAttempts enumerates the from→to edge's attempts in entries, oldest first,
// grouped by the attempt id their verdict rows carry. A step with reviewers and a
// step with a performer only are read the same way: the rows of an attempt are
// the reviewer verdicts, the performer's reject, and the readiness accept.
func edgeAttempts(entries []Entry, from, to string) []AttemptOutcome {
	return edgeAttemptsOf(entries, from, to, false)
}

// edgeAttemptsOf is edgeAttempts; edgeOnly leaves out the rows a lifecycle hook
// (create_review, amend_review) wrote, which guard an operation and are not a
// presentation of the workflow's edge.
func edgeAttemptsOf(entries []Entry, from, to string, edgeOnly bool) []AttemptOutcome {
	var out []AttemptOutcome
	index := map[string]int{}
	legacy := 0
	for i, e := range entries {
		if !isVerdictKind(e.Kind) {
			continue
		}
		r, ok := decodeEdgeRow(e)
		if !ok || r.From != from || r.To != to || (edgeOnly && r.Operation != "") {
			continue
		}
		id := r.Attempt
		if id == "" {
			legacy++
			id = fmt.Sprintf("legacy-%d", legacy)
		}
		n, seen := index[id]
		if !seen {
			n = len(out)
			index[id] = n
			out = append(out, AttemptOutcome{Attempt: id, AcceptAt: -1})
		}
		out[n].Rows = append(out[n].Rows, i)
		switch {
		case isRejectKind(e.Kind):
			out[n].Rejected = true
			out[n].Skill, out[n].Notes = r.Skill, r.Notes
		case e.Kind == KindReadinessAccept:
			out[n].AcceptAt = i
		}
	}
	for i := range out {
		out[i].Accepted = !out[i].Rejected
	}
	return out
}

// AttemptAccepted reports whether the from→to edge's attempt has no rejecting
// row in entries: the rule a readiness accept is written under. An attempt with
// no rows yet (a performer-only step before its accept is written) has nothing
// that rejects it.
func AttemptAccepted(entries []Entry, from, to, attempt string) bool {
	for _, o := range edgeAttempts(entries, from, to) {
		if o.Attempt == attempt {
			return o.Accepted
		}
	}
	return true
}

// CountRejectedRounds counts the rejected PRESENTATIONS of the from→to edge in
// entries (oldest first). A presentation is one round, identified by the attempt
// id its rows carry; it is rejected by the single rule edgeAttempts applies, so a
// round where 3 of 4 reviewers reject counts once, and a round whose performer
// rejected or failed counts once even though its gate never ran. A row written
// before attempt ids existed carries none and counts as a round of its own
// (conservative).
//
// The count restarts at every status_transition row of the story: a rejected
// presentation leaves the status unchanged, so consecutive rejected rounds have
// no transition between them, while a park, a resume or an accepted advance
// always writes one.
func CountRejectedRounds(entries []Entry, from, to string) RejectedRounds {
	start := 0
	for i, e := range entries {
		if e.Kind == KindStatusTransition {
			start = i + 1
		}
	}
	var out RejectedRounds
	for _, o := range edgeAttempts(entries[start:], from, to) {
		if o.Rejected {
			out.Rounds++
			out.LastSkill, out.LastNotes = o.Skill, o.Notes
		}
	}
	return out
}

// EdgeRounds counts the presentations of the from→to edge in entries, as
// accepted and rejected rounds, under the same rule as CountRejectedRounds: a
// round is rejected when any of its rows rejects, accepted otherwise (a panel
// with a dissenting seat that still passes is an accept). A row written before
// attempt ids existed carries none and counts as a round of its own
// (conservative).
//
// Unlike CountRejectedRounds the tally does not restart at a status_transition:
// it is the edge's whole history in entries, so a caller that wants a window
// passes the slice it means. Pure enumeration — which total is good or bad is
// the reader's, not decided here.
func EdgeRounds(entries []Entry, from, to string) (accepted, rejected int) {
	for _, o := range edgeAttempts(entries, from, to) {
		if o.Rejected {
			rejected++
		} else {
			accepted++
		}
	}
	return accepted, rejected
}

// Presentation is ONE presentation of an edge to its gate: every row that
// carries one attempt id. Accepted and Rejected follow the single rule
// edgeAttempts applies to its verdict rows; NoVerdict marks a presentation that
// wrote none — every seat that ran failed (an error, a turn cap), so only its
// reviewer invocation rows exist — and is neither accepted nor rejected.
type Presentation struct {
	Attempt   string
	Accepted  bool
	Rejected  bool
	NoVerdict bool
	// Verdicts and Invocations are the indexes in the entries slice of the
	// attempt's verdict rows and reviewer agent_invocation rows, oldest first.
	Verdicts    []int
	Invocations []int
	// Performers are the indexes of the attempt's performer agent_invocation rows
	// (the named agent that re-presented the edge), oldest first: several when a
	// retry ran. A performer row never makes a presentation of its own.
	Performers []int
}

// reviewerInvocation reports whether e is a reviewer agent_invocation row of the
// workflow's own edge — not a performer's, a lifecycle hook's, the step
// summariser's or a tool-permission event's — and decodes its edge fields.
func reviewerInvocation(e Entry) (edgeRow, bool) {
	if e.Kind != KindAgentInvocation || IsToolPermissionRow(e) {
		return edgeRow{}, false
	}
	r, ok := decodeEdgeRow(e)
	return r, ok && r.Agent == "reviewer" && r.Operation == "" && !r.StepSummary
}

// performerInvocation reports whether e is the agent_invocation row of the named
// performer that did the edge's step — any agent but the reviewer, with no
// lifecycle-hook operation and not the step summariser's — and decodes its edge
// fields.
func performerInvocation(e Entry) (edgeRow, bool) {
	if e.Kind != KindAgentInvocation || IsToolPermissionRow(e) {
		return edgeRow{}, false
	}
	r, ok := decodeEdgeRow(e)
	return r, ok && r.Agent != "" && r.Agent != "reviewer" && r.Operation == "" && !r.StepSummary
}

// IsExitReject reports whether e is a reviewer reject of an exit of a step: a
// review_reject row on an edge from→to with a destination other than from. A
// reject of the step's own re-presentation (to = from) is not an exit reject.
func IsExitReject(e Entry) bool {
	if e.Kind != KindReviewReject {
		return false
	}
	r, ok := decodeEdgeRow(e)
	return ok && r.To != "" && r.To != r.From
}

// Redispatches returns the indexes in entries (oldest first) of the performer
// agent_invocation rows that re-ran a step at the status it was already in
// (from = to = the step) after an exit of that step was rejected. An invocation
// at a step before any exit of it was rejected is not a re-dispatch.
func Redispatches(entries []Entry) []int {
	rejected := map[string]bool{}
	var out []int
	for i, e := range entries {
		if IsExitReject(e) {
			if r, ok := decodeEdgeRow(e); ok {
				rejected[r.From] = true
			}
			continue
		}
		if r, ok := performerInvocation(e); ok && r.From != "" && r.From == r.To && rejected[r.From] {
			out = append(out, i)
		}
	}
	return out
}

// EdgePresentations enumerates the presentations of the from→to edge in entries,
// oldest first: the attempts edgeAttempts finds from verdict rows, plus the
// attempts only a reviewer invocation row evidences, so a presentation whose
// seats all failed is counted rather than invisible. A reviewer invocation row
// written before invocation rows carried an attempt id joins the attempt whose
// verdict row names the same skill and was written at the same instant: one gate
// run stamps every row it writes with the same time, so the instant — not row
// order, which ties break by id — and the skill identify the run. A legacy row no
// verdict matches (a step summariser's, say) is not a presentation and is left
// out. It does not decide anything about reject budgets: a presentation with no
// verdict is no reject (CountRejectedRounds and EdgeRounds read verdict rows only
// and are unchanged).
func EdgePresentations(entries []Entry, from, to string) []Presentation {
	var out []Presentation
	index := map[string]int{}
	attempts := edgeAttemptsOf(entries, from, to, true)
	type runSkill struct {
		at    int64
		skill string
	}
	legacy := map[runSkill]string{} // (instant a gate run wrote its rows, skill) -> its attempt
	for _, a := range attempts {
		index[a.Attempt] = len(out)
		out = append(out, Presentation{Attempt: a.Attempt, Accepted: a.Accepted, Rejected: a.Rejected, Verdicts: a.Rows})
		for _, i := range a.Rows {
			if r, ok := decodeEdgeRow(entries[i]); ok && !entries[i].CreatedAt.IsZero() {
				legacy[runSkill{entries[i].CreatedAt.UnixNano(), r.Skill}] = a.Attempt
			}
		}
	}
	for i, e := range entries {
		r, ok := reviewerInvocation(e)
		if !ok || r.From != from || r.To != to {
			continue
		}
		id := r.Attempt
		if id == "" && !e.CreatedAt.IsZero() {
			// A bundle's row names every skill it judged, joined by "+".
			for _, skill := range strings.Split(r.Skill, "+") {
				if id = legacy[runSkill{e.CreatedAt.UnixNano(), skill}]; id != "" {
					break
				}
			}
		}
		if id == "" {
			continue
		}
		n, seen := index[id]
		if !seen {
			n = len(out)
			index[id] = n
			out = append(out, Presentation{Attempt: id, NoVerdict: true})
		}
		out[n].Invocations = append(out[n].Invocations, i)
	}
	// A performer's row joins the presentation its attempt id names. It never makes
	// one, and a row with no attempt id is not joined: the performer writes its row
	// at a different instant from the gate's, so the legacy instant rule does not
	// hold for it.
	for i, e := range entries {
		r, ok := performerInvocation(e)
		if !ok || r.From != from || r.To != to || r.Attempt == "" {
			continue
		}
		if n, seen := index[r.Attempt]; seen {
			out[n].Performers = append(out[n].Performers, i)
		}
	}
	first := func(p Presentation) int {
		at := len(entries)
		if len(p.Verdicts) > 0 {
			at = p.Verdicts[0]
		}
		if len(p.Invocations) > 0 && p.Invocations[0] < at {
			at = p.Invocations[0]
		}
		return at
	}
	sort.SliceStable(out, func(a, b int) bool { return first(out[a]) < first(out[b]) })
	return out
}

// LatestRoundVerdicts is the per-gate outcome of the from→to edge's latest
// presentation in entries: each reviewer skill whose verdict row that
// presentation carries, mapped to "accept" or "reject". The seats of a panel
// share their gate's skill and fold under the round's own rule: any reject
// rejects it. A performer's reject is not a gate's verdict and is left out, as
// is a readiness accept. Nil when the edge has no presentation (sty_216d5205).
func LatestRoundVerdicts(entries []Entry, from, to string) map[string]string {
	ps := EdgePresentations(entries, from, to)
	if len(ps) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, i := range ps[len(ps)-1].Verdicts {
		e := entries[i]
		if e.Kind != KindReviewAccept && e.Kind != KindReviewReject {
			continue
		}
		r, ok := decodeEdgeRow(e)
		if !ok || r.Skill == "" {
			continue
		}
		switch {
		case e.Kind == KindReviewReject:
			out[r.Skill] = "reject"
		case out[r.Skill] == "":
			out[r.Skill] = "accept"
		}
	}
	return out
}

// GatedEdges lists the distinct from→to edges that carry a reviewer verdict or a
// reviewer invocation in entries, in the order each first appears. A lifecycle
// hook's rows (create_review, amend_review) guard an operation, not an edge of
// the route, and are left out.
func GatedEdges(entries []Entry) [][2]string {
	var out [][2]string
	seen := map[[2]string]bool{}
	for _, e := range entries {
		var r edgeRow
		var ok bool
		if isVerdictKind(e.Kind) {
			r, ok = decodeEdgeRow(e)
			ok = ok && r.Operation == ""
		} else {
			r, ok = reviewerInvocation(e)
		}
		if !ok || r.From == "" && r.To == "" {
			continue
		}
		k := [2]string{r.From, r.To}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// ReadinessHolds reports whether the from→to edge's readiness was accepted and
// still holds, from the ledger alone, and names the accepted attempt. It holds
// only while the LATEST attempt on the edge is an accepted one with a
// readiness_accept row, no definition edit followed it, and the step's plan
// document was not attached after it. Any later attempt — a rejected re-run
// included — or an edit or a plan attach voids it. A mark tag on the story is
// never read here.
//
// planDoc is the name of the document the step's performer attaches, as its skill
// declares it (output_name): configuration, so nothing here names one. With none
// declared no attach voids the readiness.
func ReadinessHolds(entries []Entry, from, to, planDoc string) (attempt string, ok bool) {
	attempts := edgeAttempts(entries, from, to)
	if len(attempts) == 0 {
		return "", false
	}
	last := attempts[len(attempts)-1]
	if last.Rejected || last.AcceptAt < 0 {
		return "", false
	}
	if len(DefinitionEditedAfter(entries, last.AcceptAt)) > 0 || docAttachedAfter(entries, last.AcceptAt, planDoc) {
		return "", false
	}
	return last.Attempt, true
}

// docAttachedAfter reports whether a document named name was attached after the
// row at index at.
func docAttachedAfter(entries []Entry, at int, name string) bool {
	if name == "" {
		return false
	}
	for _, e := range entries[at+1:] {
		if e.Kind != KindStoryDocAttached {
			continue
		}
		var p struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(e.Payload, &p) == nil && p.Name == name {
			return true
		}
	}
	return false
}

// DefinitionEditedSince returns the definition_edited rows that postdate the
// latest status_transition from→to in entries (oldest first). With no such
// transition it returns nil: there is no accepted edge to have gone stale.
func DefinitionEditedSince(entries []Entry, from, to string) []Entry {
	at := -1
	for i, e := range entries {
		if e.Kind != KindStatusTransition {
			continue
		}
		if r, ok := decodeEdgeRow(e); ok && r.From == from && r.To == to {
			at = i
		}
	}
	if at < 0 {
		return nil
	}
	return DefinitionEditedAfter(entries, at)
}

// DefinitionEditedAfter returns the definition_edited rows after the row at
// index at in entries (oldest first). DefinitionEditedSince anchors it on a
// status transition; ReadinessHolds anchors it on a readiness accept.
func DefinitionEditedAfter(entries []Entry, at int) []Entry {
	if at < 0 || at >= len(entries) {
		return nil
	}
	var out []Entry
	for _, e := range entries[at+1:] {
		if e.Kind == KindDefinitionEdited {
			out = append(out, e)
		}
	}
	return out
}

// ObjectionLine renders a reject as `skill: notes` for a blocked reason,
// collapsing whitespace so it stays one line.
func (r RejectedRounds) ObjectionLine() string {
	line := strings.Join(strings.Fields(r.LastNotes), " ")
	if r.LastSkill != "" {
		return r.LastSkill + ": " + line
	}
	return line
}
