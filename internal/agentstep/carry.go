package agentstep

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// Carried reviewer verdicts (sty_59720b8b). A step that declares
// `reviewer_carry = "until_accept"` does not re-dispatch a reviewer whose
// earlier accept still rests on words that appear, unchanged, in the material it
// would judge — while any other reviewer is still being dispatched. The carried
// accept is recorded as a review_accept row carrying carried_from, with no
// agent_invocation. Once every dispatched reviewer accepts, the carried
// reviewers are dispatched fresh in the same call, so the round that advances
// the edge holds a fresh verdict from every reviewer.

// edgeCarry returns the active workflow's reviewer_carry policy for the from→to
// edge: "" or wfdot.CarryUntilAccept. Empty on any resolution failure — carry is
// an opt-in optimisation and must never be what breaks or changes a gate.
func (g *Engine) edgeCarry(ctx context.Context, item workitem.Item, from, to string) string {
	spec, _, err := g.activeSpec(ctx, item)
	if err != nil {
		return ""
	}
	for _, tr := range spec.Transitions {
		if tr.From == from && tr.To == to {
			return tr.ReviewerCarry
		}
	}
	return ""
}

// staleExemption is what the edge's step lets a prior verdict survive: nothing
// unless the step declares reviewer_carry, else the performer's output document
// and its directed messages. The engine is the only reader of the route key.
func (g *Engine) staleExemption(ctx context.Context, item workitem.Item, toStatus string) verb.StaleExemption {
	if g.edgeCarry(ctx, item, item.Status, toStatus) != wfdot.CarryUntilAccept {
		return verb.StaleExemption{}
	}
	perf, err := g.StepPerformer(ctx, item, toStatus)
	return exemptionOf(true, perf, err)
}

// exemptionOf is the stale exemption for a step already resolved: the performer's
// output document and its directed messages when the edge carries and the step is
// dispatched, nothing otherwise.
func exemptionOf(carry bool, perf verb.PerformerResolution, err error) verb.StaleExemption {
	if !carry || err != nil || !perf.Dispatched {
		return verb.StaleExemption{}
	}
	return verb.StaleExemption{DocName: perf.OutputName, Performer: perf.Agent}
}

// carriedRef is one reviewer whose earlier accept is carried: idx is its
// position in the edge's ordered list, verdict the row to record.
type carriedRef struct {
	idx     int
	verdict verb.ReviewerVerdict
}

// judgedText is the material a carried quotation is checked against: the story
// card and every document fillPayloadDocs retained for the reviewer.
func judgedText(item workitem.Item, docs []DocState) string {
	var b strings.Builder
	b.WriteString(item.Title)
	b.WriteByte('\n')
	b.WriteString(item.Body)
	b.WriteByte('\n')
	b.WriteString(item.AcceptanceCriteria)
	for _, d := range docs {
		b.WriteByte('\n')
		b.WriteString(d.Body)
	}
	return b.String()
}

// collapseSpace folds every run of whitespace (spaces, tabs, newlines) to one
// space. A model quoting a passage routinely re-flows its line breaks, so a
// quotation of a criterion is matched against the material by words, never by
// layout. Only the match is normalised: the recorded quotation and the judged
// text stay exact.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// quotationCarries is the one rule for whether a prior verdict's accept can be
// carried: an accept with a complete, non-stale quotation whose words appear in
// the material judged now. A reviewer's latest verdict, a panel seat's latest
// verdict (seatPriorVerdicts) and a per-criterion settled check are all held to
// it; an unavailable seat is projected to a reject, so it never carries. A
// partial verdict covered only part of the artifact, so it never carries as
// acceptance of the rest (sty_93359e0f). Every quotation is matched by words
// (see collapseSpace), never by layout: a changed word does not carry.
func quotationCarries(pv PriorVerdict, judged string) bool {
	if pv.Decision != "accept" || pv.Partial || pv.ReviewedStale || pv.ReviewedTruncated || strings.TrimSpace(pv.Reviewed) == "" {
		return false
	}
	return quotationPresent(pv.Reviewed, judged)
}

// quotationPresent is the one word-match rule for whether a recorded quotation
// still appears in material: quotationCarries holds an accept to it, and the
// answer_progress rule holds a rejected finding's quotation to it, so carry and
// progress can never disagree on a re-flowed passage.
func quotationPresent(reviewed, material string) bool {
	return strings.Contains(collapseSpace(material), collapseSpace(reviewed))
}

// carryRound is the one sequence of a carried round, over units that are
// reviewers (gateCarrying) or panel seats (runReviewerRef). carried and fresh
// partition the units' positions. handled is false when nothing is carried, or
// nothing is dispatched (carry is moot) — the caller then dispatches every unit
// as it would without the key. Otherwise the fresh units are dispatched first.
// A rejection decides the round and withCarried records the carried units beside
// it. An acceptance dispatches the carried units fresh and merges the two
// passes, so the round that advances holds a fresh verdict from every unit.
func carryRound[R any](carried, fresh []int, dispatch func(idx []int) (R, error), rejected func(R) bool, withCarried func(R) R, merge func(first, confirm R) R) (res R, handled bool, err error) {
	if len(carried) == 0 || len(fresh) == 0 {
		return res, false, nil
	}
	first, err := dispatch(fresh)
	if err != nil {
		return first, true, err
	}
	if rejected(first) {
		return withCarried(first), true, nil
	}
	confirm, err := dispatch(carried)
	return merge(first, confirm), true, err
}

// carryable splits ordered into the reviewers whose latest verdict on the edge
// is an accept with a complete, non-stale quotation that quotationCarries
// finds in the current material, and the indexes that must be dispatched. A reject, a missing
// or truncated or stale quotation, a changed quotation, a partial verdict, a
// functional check and a panel are always dispatched (a check may then be
// satisfied by runCheck from a green suite_run on the same tree — sty_dc554488).
func (g *Engine) carryable(ctx context.Context, item workitem.Item, toStatus string, ordered []reviewerRef, sysStart int) (carried []carriedRef, fresh []int) {
	all := make([]int, len(ordered))
	for i := range ordered {
		all[i] = i
	}
	if g.priorVerdicts == nil {
		return nil, all
	}
	perf, perr := g.StepPerformer(ctx, item, toStatus)
	prior := g.priorVerdicts(ctx, item.ID, item.Status, toStatus, exemptionOf(true, perf, perr))
	if perr != nil {
		perf = verb.PerformerResolution{}
	}
	latest := make(map[string]int, len(prior))
	for i, v := range prior {
		latest[v.Skill] = i
	}
	var mat reviewerMaterial
	var tp transitionPayload
	g.fillPayloadDocs(ctx, item.ID, &tp, &mat)
	judged := judgedText(item, mat.Docs)
	criteria, _ := judgedCriteria(item, mat.Docs, perf)
	for i, ref := range ordered {
		pi, ok := latest[ref.skill]
		if !ok || ref.skill == "" || len(ref.panel) > 0 || ref.combine != "" {
			fresh = append(fresh, i)
			continue
		}
		pv := prior[pi]
		if !quotationCarries(pv, judged) || !criteriaUnchanged(pv, criteria) {
			fresh = append(fresh, i)
			continue
		}
		// A functional check is never carried; it is always dispatched to
		// runCheck, which may satisfy it from a green suite_run on the same tree
		// when the skill declares check_reuse: tree (sty_dc554488).
		body, err := g.skillBody(ctx, ref.skill)
		if err != nil || skillCheck(body) != "" {
			fresh = append(fresh, i)
			continue
		}
		carried = append(carried, carriedRef{idx: i, verdict: verb.ReviewerVerdict{
			Skill: ref.skill, Order: i, Accept: true, System: i >= sysStart,
			Reviewed:    pv.Reviewed,
			Criteria:    carriedCriteria(pv.Criteria, pi+1),
			CarriedFrom: pi + 1,
			Notes:       fmt.Sprintf("carried from attempt %d: quotation unchanged", pi+1),
		}})
	}
	return carried, fresh
}

// gateCarrying runs an edge that declares reviewer_carry. handled is false when
// nothing is carryable, or everything is (carry is moot: every reviewer is
// dispatched) — the caller then runs the edge as it would without the key.
//
// Both passes stamp the one round tally, so the confirm pass continues from the
// fresh pass's counts against the edge's whole gate list.
func (g *Engine) gateCarrying(ctx context.Context, item workitem.Item, toStatus string, ordered []reviewerRef, sysStart, parallelCap int, tally *gateTally) (verb.GateDecision, bool, error) {
	carried, fresh := g.carryable(ctx, item, toStatus, ordered, sysStart)
	idx := make([]int, len(carried))
	for j, c := range carried {
		idx[j] = c.idx
	}
	return carryRound(idx, fresh,
		func(at []int) (verb.GateDecision, error) {
			return g.scheduleGate(ctx, item, toStatus, ordered, at, sysStart, parallelCap, tally)
		},
		func(dec verb.GateDecision) bool { return dec.Gated && !dec.Accept },
		func(dec verb.GateDecision) verb.GateDecision {
			// A reject blocks the edge; the carried accepts are recorded beside it.
			for _, c := range carried {
				g.telemetryEvent(ctx, item.ID, "reviewer", "gate-carried", map[string]any{
					"skill": c.verdict.Skill, "to": toStatus, "carried_from": c.verdict.CarriedFrom,
				})
				dec.Reviewers = append(dec.Reviewers, c.verdict)
			}
			sortReviewers(dec.Reviewers)
			return dec
		},
		mergeGate)
}

// scheduleGate dispatches the reviewers at idx (ascending positions in ordered)
// under the edge's bundle, parallel or serial scheduling, and returns the
// decision with each verdict's Order restored to its position in ordered.
func (g *Engine) scheduleGate(ctx context.Context, item workitem.Item, toStatus string, ordered []reviewerRef, idx []int, sysStart, parallelCap int, tally *gateTally) (verb.GateDecision, error) {
	sub := make([]reviewerRef, len(idx))
	subSys := 0
	for j, i := range idx {
		sub[j] = ordered[i]
		if i < sysStart {
			subSys++
		}
	}
	dec, err := g.runScheduled(ctx, item, toStatus, sub, subSys, parallelCap, tally)
	for k := range dec.Reviewers {
		if o := dec.Reviewers[k].Order; o >= 0 && o < len(idx) {
			dec.Reviewers[k].Order = idx[o]
		}
	}
	return dec, err
}

func sortReviewers(rs []verb.ReviewerVerdict) {
	sort.SliceStable(rs, func(a, b int) bool { return rs[a].Order < rs[b].Order })
}

// mergeGate folds the confirming pass into the first pass's accepting decision.
// A reject in the confirming pass decides the edge; otherwise the top-level
// fields mirror whichever pass holds the highest-ordered verdict, as an
// all-accept edge mirrors its last.
func mergeGate(first, confirm verb.GateDecision) verb.GateDecision {
	out := first
	if confirm.Gated && (!first.Gated || !confirm.Accept || maxOrder(confirm.Reviewers) > maxOrder(first.Reviewers)) {
		out = confirm
	}
	out.Gated = first.Gated || confirm.Gated
	out.Accept = (!first.Gated || first.Accept) && (!confirm.Gated || confirm.Accept)
	out.Unresolved = append(append([]string(nil), first.Unresolved...), confirm.Unresolved...)
	out.Reviewers = append(append([]verb.ReviewerVerdict(nil), first.Reviewers...), confirm.Reviewers...)
	sortReviewers(out.Reviewers)
	return out
}

func maxOrder(rs []verb.ReviewerVerdict) int {
	m := -1
	for _, r := range rs {
		if r.Order > m {
			m = r.Order
		}
	}
	return m
}
