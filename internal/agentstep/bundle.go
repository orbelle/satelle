package agentstep

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/structure"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// Bundled reviewer gates (sty_23e10d92). An edge that declares `bundle` runs the
// LLM gates that can share one session as ONE reviewer session: every rubric
// rides the system prompt as a labelled section, the session returns one verdict
// per rubric, and each verdict is recorded under its own skill. Bundling never
// merges or drops a rubric, never touches a functional check, and never lets a
// rubric that returns nothing accept by silence.

// bundlePreamble opens a bundled reviewer's rubric. It carries the bundled
// output contract — the one thing each rubric's own single-verdict contract
// cannot say — so the contract lives here, in one place, and in no adapter.
const bundlePreamble = "## Bundled review — several rubrics, one session\n\n" +
	"This session judges SEVERAL rubrics over ONE payload. Each rubric below is a " +
	"separate section headed `### Rubric: <skill>`; the payload's `review_skills` lists them. " +
	"Judge every rubric on its own merits, independently: a finding under one rubric " +
	"never excuses or condemns another, and you may not skip, merge or drop one.\n\n" +
	"Where a rubric's own text asks you to return a single `{decision, notes}` object, " +
	"IGNORE that instruction here. Return exactly ONE JSON object, once, at the end of " +
	"your reply, with one entry per rubric — `skill` is the exact name in its heading:\n\n" +
	"```json\n" +
	"{\"verdicts\": [{\"skill\": \"<skill name>\", \"decision\": \"accept|reject\", \"notes\": \"...\", \"reasoning\": \"...\", \"reviewed\": \"...\"}]}\n" +
	"```\n\n" +
	"A rubric you give no verdict for is treated as a REJECT."

// bundleRubric composes the bundled session's rubric: the preamble, then every
// rubric verbatim under its own heading, in run order.
func bundleRubric(preps []reviewerPrep) string {
	var b strings.Builder
	b.WriteString(bundlePreamble)
	for _, p := range preps {
		b.WriteString("\n\n---\n\n### Rubric: ")
		b.WriteString(p.skill)
		b.WriteString("\n\n")
		b.WriteString(p.body)
	}
	return b.String()
}

type bundleEntry struct {
	Skill             string `json:"skill"`
	Decision          string `json:"decision"`
	Notes             string `json:"notes"`
	Reasoning         string `json:"reasoning"`
	Reviewed          string `json:"reviewed,omitempty"`
	ReviewedTruncated bool   `json:"reviewed_truncated,omitempty"`
	// Criteria is the optional per-criterion verdict (sty_99d3162b).
	Criteria []rawCriterion `json:"criteria,omitempty"`
	// Coverage is what the reviewer says the verdict covered (sty_93359e0f).
	Coverage json.RawMessage `json:"coverage,omitempty"`
	// Waived is the checks this rubric honoured a payload waiver for (sty_bef97244).
	Waived []string `json:"waived,omitempty"`
}

type bundleEnvelope struct {
	Verdicts []bundleEntry `json:"verdicts"`
}

// parseBundleDecisions reads a bundled reviewer's verdicts out of its output —
// lenient on surrounding prose and code fences like parseDecision, strict on
// shape. The LAST valid verdict for a skill wins (a model reasons, then
// concludes), verdicts naming a skill that was not asked for are ignored, and a
// requested skill with no valid verdict comes back as a fail-closed REJECT
// (never an accept, never absent). Output carrying no verdict for ANY requested
// skill is an error, which the caller's retry loop treats as no-verdict.
func parseBundleDecisions(out []byte, skills []string) ([]verb.GateDecision, error) {
	found := map[string]verb.GateDecision{}
	offer := func(e bundleEntry) {
		skill := matchBundleSkill(e.Skill, skills)
		if skill == "" {
			return
		}
		switch strings.ToLower(strings.TrimSpace(e.Decision)) {
		case "accept", "reject":
			reviewed, truncated := applyReviewed(e.Reviewed)
			found[skill] = verb.GateDecision{
				Accept:            strings.ToLower(strings.TrimSpace(e.Decision)) == "accept",
				Notes:             e.Notes,
				Reasoning:         e.Reasoning,
				Reviewed:          reviewed,
				ReviewedTruncated: truncated,
				Criteria:          parseCriteria(e.Criteria),
				CriteriaSent:      len(e.Criteria) > 0,
				Coverage:          applyCoverage(e.Coverage),
				Waived:            e.Waived,
			}
		}
	}
	for _, obj := range jsonObjectCandidates(out) {
		var env bundleEnvelope
		if json.Unmarshal(obj, &env) == nil && len(env.Verdicts) > 0 {
			for _, e := range env.Verdicts {
				offer(e)
			}
			continue
		}
		var e bundleEntry
		if json.Unmarshal(obj, &e) == nil {
			offer(e)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no {\"verdicts\": [{\"skill\", \"decision\": \"accept\"|\"reject\"}]} object naming a requested rubric in bundled reviewer output")
	}
	decs := make([]verb.GateDecision, 0, len(skills))
	for _, skill := range skills {
		d, ok := found[skill]
		if !ok {
			d = failClosedDecision(skill, "")
		}
		d.Skill = skill
		decs = append(decs, d)
	}
	return decs, nil
}

// failClosedDecision is the REJECT a rubric gets when a bundled response gave it
// no verdict: silence never accepts. why is optional extra context.
func failClosedDecision(skill, why string) verb.GateDecision {
	notes := "bundled response returned no verdict for " + skill + " (fail-closed)"
	if why != "" {
		notes += ": " + why
	}
	return verb.GateDecision{Accept: false, Notes: notes, Skill: skill}
}

// matchBundleSkill maps a verdict's skill label onto a requested skill: exact
// first, then case-insensitively. "" when it names none of them.
func matchBundleSkill(label string, skills []string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	for _, s := range skills {
		if s == label {
			return s
		}
	}
	for _, s := range skills {
		if strings.EqualFold(s, label) {
			return s
		}
	}
	return ""
}

// edgeBundle reports whether the active workflow declares the from→to edge
// bundled. False on any resolution failure: bundling is an opt-in optimisation
// and must never be what breaks or changes a gate.
func (g *Engine) edgeBundle(ctx context.Context, item workitem.Item, from, to string) bool {
	spec, _, err := g.activeSpec(ctx, item)
	if err != nil {
		return false
	}
	for _, tr := range spec.Transitions {
		if tr.From == from && tr.To == to {
			return tr.Bundle
		}
	}
	return false
}

// gateUnit is one dispatch of the bundled scheduler: the indexes (into the
// edge's ordered gate list) it judges. More than one index is a bundle and
// carries the seat they share; exactly one runs through the ordinary
// single-gate path.
type gateUnit struct {
	idx  []int
	seat reviewerSeat
}

// bundleSeatKey is what two gates must share to be judged in one session: the
// agents.toml binding, the model it resolved to, the effort, the tool grant and
// the harness command.
func bundleSeatKey(s reviewerSeat) string {
	return fmt.Sprintf("%q|%q|%q|%q|%q|%q", s.section, s.binding.Model, s.binding.Effort,
		s.binding.Tools, s.binding.CommandTemplate(), s.binding.ResolvedPrinciples())
}

// partitionBundles groups an edge's gates into dispatch units. A gate stays its
// own unit when it has no rubric to bundle (absent — the ordinary path degrades
// it), carries a functional check (a command decides it, never a session), is
// marked `independent: true` by its rubric, or resolves to a seat no other gate
// shares. Gates that resolve to the same seat form one bundle. Units are ordered
// by their first gate's position, and a gate whose seat cannot resolve is left
// to the ordinary path to refuse with its own message.
func (g *Engine) partitionBundles(ctx context.Context, item workitem.Item, toStatus string, ordered []reviewerRef) []gateUnit {
	var units []gateUnit
	groupAt := map[string]int{} // seat key → index into units
	for i, ref := range ordered {
		if ref.skill == "" {
			continue
		}
		single := gateUnit{idx: []int{i}}
		body, err := g.skillBody(ctx, ref.skill)
		if err != nil || skillCheck(body) != "" || structure.Independent(body) {
			units = append(units, single)
			continue
		}
		seat, serr := g.reviewerSeatFor(ctx, item, toStatus, ref.skill, ref.agent)
		if serr != nil {
			units = append(units, single)
			continue
		}
		// Gates of different review modes never share a session: one brief cannot
		// both stop at the first finding and judge everything (sty_93359e0f).
		key := bundleSeatKey(seat) + "|" + ref.mode
		if at, ok := groupAt[key]; ok {
			units[at].idx = append(units[at].idx, i)
			continue
		}
		groupAt[key] = len(units)
		single.seat = seat
		units = append(units, single)
	}
	return units
}

// runGateBundled runs an edge whose gates may bundle. handled is false when no
// two gates share a seat — the caller then runs the edge exactly as it would
// without `bundle`. Otherwise units are scheduled like gates are elsewhere:
// concurrently up to parallelCap when the edge sets one, else serially with the
// first-reject short-circuit; the outcome is assembled as the parallel path does.
func (g *Engine) runGateBundled(ctx context.Context, item workitem.Item, toStatus string, ordered []reviewerRef, sysStart, parallelCap int, tally *gateTally) (verb.GateDecision, bool, error) {
	units := g.partitionBundles(ctx, item, toStatus, ordered)
	bundled := false
	for _, u := range units {
		if len(u.idx) > 1 {
			bundled = true
			break
		}
	}
	if !bundled {
		return verb.GateDecision{}, false, nil
	}
	results := make([]gateSlot, len(ordered))
	g.gatesOpened(item.ID, "gates (bundled)", tally)
	run := func(u gateUnit) {
		label := ordered[u.idx[0]].skill
		if len(u.idx) > 1 {
			label = fmt.Sprintf("%d bundled gates", len(u.idx))
		}
		g.gateStarted(item.ID, label, tally)
		g.runGateUnit(ctx, item, toStatus, ordered, u, results)
		// Each gate of the unit counts on its own: a bundle of three is three gates.
		slots := make([]gateSlot, len(u.idx))
		for k, i := range u.idx {
			slots[k] = results[i]
		}
		g.gateReturned(item.ID, label, tally, slots...)
	}
	if parallelCap > 0 {
		cap := parallelCap
		if cap > len(units) {
			cap = len(units)
		}
		sem := make(chan struct{}, cap)
		var wg sync.WaitGroup
		for _, u := range units {
			wg.Add(1)
			go func(u gateUnit) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					for _, i := range u.idx {
						results[i].err = ctx.Err()
					}
					return
				}
				run(u)
			}(u)
		}
		wg.Wait()
	} else {
		for _, u := range units {
			run(u)
			if unitStopsEdge(results, u) {
				break // a reject or an error blocks the edge — do not run later gates
			}
		}
	}
	dec, err := assembleGate(ordered, results, sysStart)
	return dec, true, err
}

// unitStopsEdge reports whether a finished unit rejected or errored, which ends
// a serial edge.
func unitStopsEdge(results []gateSlot, u gateUnit) bool {
	for _, i := range u.idx {
		if results[i].err != nil || (results[i].dec.Gated && !results[i].dec.Accept) {
			return true
		}
	}
	return false
}

// runGateUnit runs one unit and writes each of its gates' outcome into results.
func (g *Engine) runGateUnit(ctx context.Context, item workitem.Item, toStatus string, ordered []reviewerRef, u gateUnit, results []gateSlot) {
	if len(u.idx) == 1 {
		i := u.idx[0]
		dec, err := g.runReviewerRef(ctx, item, toStatus, ordered[i], nil)
		results[i] = gateSlot{dec: dec, err: err}
		return
	}
	refs := make([]reviewerRef, len(u.idx))
	for j, i := range u.idx {
		refs[j] = ordered[i]
	}
	decs, err := g.runReviewerBundle(ctx, item, toStatus, refs, u.seat)
	for j, i := range u.idx {
		if err != nil {
			results[i] = gateSlot{dec: verb.GateDecision{Gated: true, Skill: ordered[i].skill}, err: err}
			continue
		}
		results[i] = gateSlot{dec: decs[j]}
	}
}

// runReviewerBundle judges refs — gates that share seat — in ONE reviewer
// session and returns one decision per ref, in order. Every rubric goes through
// the same pre-flight a lone gate gets (prepareReviewer); the session goes
// through the shared Invoke, so it passes the same isolation preflight and tool
// trim. The session's usage is recorded once, on the FIRST verdict: the others
// carry no usage, because there was one measured call, not several.
func (g *Engine) runReviewerBundle(ctx context.Context, item workitem.Item, toStatus string, refs []reviewerRef, seat reviewerSeat) ([]verb.GateDecision, error) {
	decs := make([]verb.GateDecision, len(refs))
	var live []int // indexes into refs that have a rubric to judge
	var preps []reviewerPrep
	for j, ref := range refs {
		prep, err := g.prepareReviewer(ctx, item, toStatus, ref.skill, ref.agent, withReviewMode(ref.mode, nil))
		if err != nil {
			return nil, err
		}
		if prep.unresolved {
			// Absent since it was partitioned: advisory, exactly as it is alone.
			decs[j] = verb.GateDecision{Gated: false, Skill: ref.skill, Unresolved: []string{ref.skill}}
			continue
		}
		live = append(live, j)
		preps = append(preps, prep)
	}
	switch len(live) {
	case 0:
		return decs, nil
	case 1:
		// Nothing left to share a session with: judge it as any lone gate is.
		dec, err := g.runReviewerRef(ctx, item, toStatus, refs[live[0]], nil)
		if err != nil {
			return nil, err
		}
		decs[live[0]] = dec
		return decs, nil
	}

	skills := make([]string, len(preps))
	for k, p := range preps {
		skills[k] = p.skill
	}
	// Every payload differs only in review_skill and in the deferrals its gate
	// owns, so the first stands for all once the deferrals are merged (each item
	// keeps its owner skill); the session is told which rubrics it judges.
	tp := preps[0].tp
	tp.ReviewSkill = ""
	tp.ReviewSkills = skills
	tp.Deferred = mergeDeferred(preps)
	mat := preps[0].material
	mat.Deferred = append([]DeferredItem(nil), tp.Deferred...)
	tp.Waivers = mergeWaivers(preps)
	mat.Waivers = append([]WaiverItem(nil), tp.Waivers...)
	tp.Citations, mat.Citations = bundleCitations(preps)
	defer closeSeat(seat)
	dlog, _, closeLog := g.openDispatchLog(seat.section, item.ID, seat.binding)
	defer closeLog()
	res := g.Invoke(ctx, InvokeRequest{
		Binding:     seat.binding,
		Section:     seat.section,
		Rubric:      bundleRubric(preps),
		Payload:     tp,
		Material:    &mat,
		Charter:     reviewerCharter(),
		Expect:      ExpectVerdicts,
		Timeout:     g.agentTimeout,
		IdleTimeout: seat.idle,
		Runner:      seat.runner,
		OnEvent:     dlog.handler(),
		Log:         dlog,
		Attempts:    g.attempts,
		StoryID:     item.ID,
		Step:        toStatus,
		Skill:       strings.Join(skills, "+"),
		Skills:      skills,
		Actor:       seat.section,
	})
	if res.Err != nil {
		if res.Command == "" {
			return nil, res.Err
		}
		// One session judged every rubric, so its failure is one record naming
		// them all, the way a judged bundle writes its single row.
		stamp := g.failureStamp(strings.Join(skills, "+"), res, seat)
		stamp.BundleSkills = skills
		return nil, &verb.GateFailure{Err: res.Err, Stamps: []verb.ReviewerVerdict{stamp}}
	}
	if len(res.Decisions) != len(skills) {
		return nil, fmt.Errorf("reviewer: bundled session for %s returned %d decisions, want %d",
			strings.Join(skills, "+"), len(res.Decisions), len(skills))
	}
	bundleID := ledger.NewBundleID()
	g.telemetryEvent(ctx, item.ID, seat.section, "gate-bundled", map[string]any{
		"bundle_id": bundleID, "skills": skills, "step": toStatus,
	})
	var lead verb.GateDecision
	for k, d := range res.Decisions {
		d.BundleID, d.BundleSkills = bundleID, skills
		d.ReviewMode = refs[live[k]].mode
		if k == 0 {
			g.setDecisionUsage(&d, res.Usage, res.RanModel(seat.binding.Model))
			d.RunCounts = res.Counts
		}
		// After the usage: setDecisionUsage replaces the whole UsageNote.
		stampInvocation(&d, res, seat)
		preps[k].criteria.apply(&d)
		preps[k].keepWaived(&d)
		if k == 0 {
			lead = d
		} else {
			// One measured call: name the seat's model on every verdict so the
			// rows group together, but carry no usage — it is on the first.
			d.Model, d.ModelResolved = lead.Model, lead.ModelResolved
		}
		decs[live[k]] = d
	}
	return decs, nil
}
