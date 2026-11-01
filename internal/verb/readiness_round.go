package verb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/epicset"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfroute"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A readiness round (sty_a32bd6a9) is ONE presentation of a story's readiness
// step: its performer runs, then the step's reviewers judge what it produced.
// Both routes run it — a presented edge (`story set --status <step>`, which
// engages the story) and `satelle story ready` (which does not) — through the
// helpers in this file, so the rows a round writes, the attempt id they share
// and the rule for whether the round was accepted are written once.
//
// Which step is the readiness step, which tag marks an accepted round and how
// many rejected rounds are allowed are all the route's declarations
// (propose/waits_on_children, mark, reject_budget); nothing here names a status
// or a tag.

// parkAttemptKey carries a readiness round's attempt id into the park transition
// that follows a performer reject, so that row names the round it ended.
type parkAttemptKey struct{}

func withParkAttempt(ctx context.Context, attempt string) context.Context {
	if attempt == "" {
		return ctx
	}
	return context.WithValue(ctx, parkAttemptKey{}, attempt)
}

func parkAttemptFrom(ctx context.Context) string {
	a, _ := ctx.Value(parkAttemptKey{}).(string)
	return a
}

// readinessEdge is the edge a story's readiness is judged on, resolved from its
// route: the start status and the step entered from it that declares the
// readiness knob (`propose` for a story, `waits_on_children` for a container).
type readinessEdge struct {
	From string
	To   string
	Step wfdot.State
	// Advisors are the advisors the governing route declares, as governingSpec
	// resolved them for the item; epicAdvice picks the readiness step's own.
	Advisors []wfroute.Advisor
}

// name is the step's obligation (its key in step.toml), falling back to its
// status, for a message that names the step.
func (e readinessEdge) name() string {
	if e.Step.Obligation != "" {
		return e.Step.Obligation
	}
	return e.To
}

// readinessEdgeFor resolves item's readiness edge from its governing route.
func readinessEdgeFor(ctx context.Context, item workitem.Item) (readinessEdge, error) {
	if item.Kind != workitem.KindStory {
		return readinessEdge{}, fmt.Errorf("%s is a %s: only a story or an epic-parent has a readiness step", item.ID, item.Kind)
	}
	spec, _, advisors, ok := governingSpec(ctx, item)
	if !ok {
		return readinessEdge{}, fmt.Errorf("cannot resolve the route that governs %s", item.ID)
	}
	from := spec.Start()
	container := epicset.IsEpicParent(item)
	for _, to := range spec.Successors(from) {
		st, found := spec.StateNamed(to)
		if !found {
			continue
		}
		if (container && st.WaitsOnChildren) || (!container && st.Propose) {
			return readinessEdge{From: from, To: to, Step: st, Advisors: advisors}, nil
		}
	}
	knob := "propose"
	if container {
		knob = "waits_on_children"
	}
	return readinessEdge{}, fmt.Errorf("the route of %s %w: no step entered from %q declares %s", item.ID, errNoReadinessStep, from, knob)
}

// errNoReadinessStep is what readinessEdgeFor wraps when the route has no step
// entered from its start that declares the readiness knob, so a caller can tell
// that case from a lookup that failed.
var errNoReadinessStep = errors.New("declares no readiness step")

// gateRound is what one gate run recorded: the reviewers' verdicts and the gate
// skills that did not resolve.
type gateRound struct {
	Reviewers  []ReviewerVerdict
	Unresolved []string
}

// recordGateRound runs the from→to gate for current and records every
// reviewer's verdict under attempt — the one id of this presentation. Rows
// are all written before a reject is returned, so a parallel round records every
// verdict. A reject comes back as multiRejectError; the caller decides what a
// reject does to the story (the engaged route refuses the transition, `story
// ready` records the round and leaves the status alone).
func recordGateRound(ctx context.Context, current workitem.Item, to, attempt string, now time.Time) (gateRound, error) {
	var out gateRound
	dec, gerr := transitionGater.Gate(ctx, current, to)
	if gerr != nil {
		// A seat attempt that produced no verdict (a failed run, a turn cap)
		// still measured something: record its invocation, under this
		// presentation's attempt id, before the error is returned.
		var failure *GateFailure
		if errors.As(gerr, &failure) {
			for _, stamp := range failure.Stamps {
				appendLedgerEntry(ctx, current.ID, ledger.KindAgentInvocation, "reviewer",
					fmt.Sprintf("invoked reviewer (%s) for %s→%s with @skill:%s — %s", stamp.Command, current.Status, to, stamp.Skill, stamp.Outcome),
					invocationPayload(current.Status, to, attempt, stamp), now)
			}
		}
		return out, gerr
	}
	// An edge may carry several reviewers (a transition's reviewer list plus
	// the always-on system layer). Record each reviewer's verdict as its own
	// ledger row, in order, so the trail names who judged the edge and how. The
	// single-reviewer path returns no Reviewers — synthesise one from the
	// top-level verdict so both paths record identically.
	reviewers := dec.Reviewers
	if len(reviewers) == 0 && dec.Gated {
		reviewers = []ReviewerVerdict{verdictFromDecision(dec)}
	}
	// Ledger ALL verdicts first (accepts and rejects), then refuse if any
	// rejected — so parallel multi-reviewer rounds record every row
	// (sty_4f0a15db AC2). Serial short-circuit still only returns reviewers
	// up to the first reject, so single-reject message format is unchanged.
	var rejects []ReviewerVerdict
	bundled := map[string]bool{}
	for _, rv := range reviewers {
		// A panel fold leaves Command empty on the skill row and carries one
		// stamp per seat that started. Write those first, then the single
		// skill-level verdict. A dissent stays inside seats — it is not a
		// second review_reject, and a seat that never started has no stamp.
		for _, stamp := range rv.SeatStamps {
			if stamp.Command == "" {
				continue
			}
			seat := stamp.Seat
			if seat == "" {
				seat = stamp.Skill
			}
			appendLedgerEntry(ctx, current.ID, ledger.KindAgentInvocation, "reviewer",
				fmt.Sprintf("invoked reviewer (%s) for %s→%s with @skill:%s seat %s", stamp.Command, current.Status, to, stamp.Skill, seat),
				invocationPayload(current.Status, to, attempt, stamp), now)
		}
		// Record HOW the isolated agent was invoked before its verdict — the
		// resolved command/harness and the injected skill/rubric file — so the
		// timeline shows what command ran with what context (sty_fb3e0873). Only
		// an LLM reviewer carries a Command; a functional check invokes no agent.
		// A bundled session is ONE invocation however many rubrics it judged
		// (sty_23e10d92): its row is written once, with the bundle's usage, on
		// the first of its verdicts. A panel fold leaves Command empty so this
		// write does not fire a duplicate for the skill row.
		if rv.Command != "" && !(rv.BundleID != "" && bundled[rv.BundleID]) {
			inv, rubrics := rv, rv.Context
			if rv.BundleID != "" {
				bundled[rv.BundleID] = true
				inv.Skill = strings.Join(rv.BundleSkills, "+")
				rubrics = strings.Join(rv.BundleSkills, ",")
			}
			appendLedgerEntry(ctx, current.ID, ledger.KindAgentInvocation, "reviewer",
				fmt.Sprintf("invoked reviewer (%s) for %s→%s with @skill:%s", rv.Command, current.Status, to, rubrics),
				invocationPayload(current.Status, to, attempt, inv), now)
		}
		if !rv.Accept {
			appendLedgerEntry(ctx, current.ID, ledger.KindReviewReject, "reviewer",
				fmt.Sprintf("rejected %s→%s by %s: %s", current.Status, to, rv.Skill, rv.Notes),
				reviewerPayload(current.Status, to, rv, attempt), now)
			rejects = append(rejects, rv)
			continue
		}
		acceptBody := fmt.Sprintf("accepted %s→%s by %s: decision=accept notes=%s%s",
			current.Status, to, rv.Skill, rv.Notes, formatReasoningSuffix(rv.Reasoning))
		appendLedgerEntry(ctx, current.ID, ledger.KindReviewAccept, "reviewer",
			acceptBody, reviewerPayload(current.Status, to, rv, attempt), now)
		// Transparency (design §6.2 / epic AC): surface accept verdicts too —
		// decision, notes, reasoning on stderr (reject already returns as error).
		EmitVerdict(acceptBody)
	}
	if len(rejects) > 0 {
		notifyChange(panelTopic(current.Kind))
		return out, multiRejectError(current.Status, to, rejects)
	}
	// An edge may have DECLARED a gate whose skill does not resolve. That gate
	// degraded to advisory and judged nothing, so this advance is ungated —
	// record it as such. Without this row the trail is indistinguishable from
	// an edge that never carried a gate at all, which is the silence
	// sty_d59ec6a9 is about. It does not block: fail-open is deliberate.
	for _, skill := range dec.Unresolved {
		skippedBody := fmt.Sprintf(
			"advanced %s→%s UNGATED — gate skill %q does not resolve in the substrate, so no reviewer judged this transition",
			current.Status, to, skill)
		appendLedgerEntry(ctx, current.ID, ledger.KindGateSkipped, "reviewer",
			skippedBody, attemptPayload(current.Status, to, skill, ""), now)
		EmitVerdict(skippedBody)
	}
	out.Reviewers, out.Unresolved = reviewers, dec.Unresolved
	return out, nil
}

// performerRun is what dispatching a step's performer produced and recorded. A
// zero Err/Refuse is an accept: the performer did not reject.
type performerRun struct {
	Res DispatchResult
	// Err is the dispatch failure, or the performer's reject (see Reject).
	Err error
	// Reject is set when Err is a performer that judged the premise wrong and said
	// why: the engaged route parks the story with those notes.
	Reject *PerformerReject
	// Refuse is a check on the run's output that refuses the step (the
	// skill-evidence check under skill_evidence = reject, or unanswered findings).
	Refuse error
	// Item is the story as the performer's reported changes left it, set only when
	// they were applied (sty_af0b1761). The caller rebinds it before the step's
	// gates run, so they judge the repaired record.
	Item *workitem.Item
}

// rebind returns the story the gates should judge: the one the performer's
// reported changes left, or current when none were applied.
func (r performerRun) rebind(current workitem.Item) workitem.Item {
	if r.Item != nil {
		return *r.Item
	}
	return current
}

// dispatchPerformer runs the named performer a step allocates and records its
// audit rows. readiness marks the call as a readiness round: its rows carry the
// round's attempt id, and a reject or failure also writes the performer_reject
// verdict, so a round whose gate never ran is still counted as a rejected round
// (the single rule is ledger.edgeAttempts). Other steps' rows are unchanged.
func dispatchPerformer(ctx context.Context, current workitem.Item, to, attempt string, readiness bool, now time.Time) performerRun {
	var run performerRun
	res, derr := executorDispatcher.DispatchExecutor(ctx, current, to)
	run.Res = res
	if derr != nil {
		run.Err = derr
		var rejected *PerformerReject
		if errors.As(derr, &rejected) {
			// A reject may carry changes: they are applied within the same limits
			// and the reject is still recorded. A change the limits refuse is added
			// to the notes, so the reject says what was and was not applied.
			if len(rejected.Changes) > 0 {
				item, cerr := applyPerformerChanges(ctx, current, to, res, rejected.Changes, now)
				if cerr != nil {
					rejected = &PerformerReject{Notes: strings.TrimSpace(rejected.Notes + " (" + cerr.Error() + ")")}
					derr = rejected
				} else {
					run.Item = &item
				}
			}
			if strings.TrimSpace(rejected.Notes) != "" {
				run.Reject = rejected
			}
			run.Err = derr
		}
		if readiness {
			recordPerformerReject(ctx, current, to, attempt, res, derr.Error(), now)
		}
		return run
	}
	if !res.Dispatched {
		recordInLoopDelivery(ctx, current, to, attempt, res, &run, now)
		return run
	}
	// Record the resolved model (when the binding pins one) so the ledger shows
	// WHICH model ran the step — the audit trail for per-step model mixing
	// (sty_5d48317b). The env (endpoint + token) is never recorded.
	modelNote := ""
	if res.Model != "" {
		modelNote = " on model " + res.Model
	}
	appendLedgerEntry(ctx, current.ID, ledger.KindAgentInvocation, "executor",
		fmt.Sprintf("dispatched step %q to named agent %q%s (%s) with @skill:%s",
			to, res.Agent, modelNote, res.Command, res.Skill),
		dispatchPayload(current.Status, to, res, attempt), now)
	// The configuration changes the performer reported are applied now, before
	// the step's gates run, so they judge the repaired record (sty_af0b1761). A
	// refused batch applied nothing and fails the round like any dispatch failure.
	if len(res.Changes) > 0 {
		item, cerr := applyPerformerChanges(ctx, current, to, res, res.Changes, now)
		if cerr != nil {
			run.Err = cerr
			if readiness {
				recordPerformerReject(ctx, current, to, attempt, res, cerr.Error(), now)
			}
			return run
		}
		run.Item = &item
	}
	// A dispatched task-execution run's captured output is written through as
	// an OKF run-output doc under its task folder (sty_890b86cb) — the same
	// write seam the in-loop execution-record verb uses. Best-effort: a write
	// failure is recorded, not fatal (the run already happened).
	if current.Kind == workitem.KindExecution && res.Output != "" {
		if _, werr := recordRunOutput(current, res.Output, "", now); werr != nil {
			appendLedgerEntry(ctx, current.ID, ledger.KindAgentInvocation, "executor",
				"run-output record failed: "+werr.Error(),
				attemptPayload(current.Status, to, res.Skill, attempt), now)
		}
	}
	// The skill-evidence check (sty_23ceeca2): a gap is always on the row above and
	// in front of the driver; under the workflow's skill_evidence = reject it also
	// refuses the step.
	var refuse error
	if res.Evidence != nil && len(res.Evidence.Gaps()) > 0 {
		reportSkillEvidence(current.ID, to, res.Evidence)
		if res.Evidence.Blocks() {
			refuse = fmt.Errorf("step %q refused by the skill-evidence check (workflow skill_evidence = reject): %s", to, res.Evidence.Notes())
		}
	}
	run.Refuse = refuse
	if readiness && refuse != nil {
		recordPerformerReject(ctx, current, to, attempt, res, refuse.Error(), now)
	}
	if readiness && run.Refuse == nil {
		if refuse := refuseUnansweredFindings(ctx, current, to, res); refuse != nil {
			run.Refuse = refuse
			rejected := res
			rejected.Skill = UnansweredFindingsSkill
			recordPerformerReject(ctx, current, to, attempt, rejected, refuse.Error(), now)
		}
	}
	return run
}

// UnansweredFindingsSkill is the skill name on the performer_reject row of a
// round refused for leaving the prior blocking findings unprogressed
// (sty_73224b86). It is distinct from the rework relay's no_progress outcome:
// that ends a coder↔consultant conversation, this refuses a presentation.
const UnansweredFindingsSkill = "unanswered-findings"

// refuseUnansweredFindings applies the destination step's answer_progress rule to
// how a re-presented performer's output stands against the blocking findings it
// was offered (res.Progress): fewer than min(min_progress, offered) answered or
// changed refuses the presentation before any gate runs. Nil when the rule is off,
// nothing was offered, or enough progressed. The performer_reject row it is
// recorded as counts against the step's reject_budget like any rejected round.
func refuseUnansweredFindings(ctx context.Context, current workitem.Item, to string, res DispatchResult) error {
	p := res.Progress
	if p == nil || len(p.Offered) == 0 {
		return nil
	}
	st, _, found := destinationStep(ctx, current, to)
	if !found || !st.AnswerProgress {
		return nil
	}
	required := p.Required(st.MinProgress)
	if len(p.Progressed) >= required {
		return nil
	}
	return fmt.Errorf("unanswered findings — %d of %d blocking findings answered or changed (required %d); unanswered: %s; answer each in `answers` or change the quoted text",
		len(p.Progressed), len(p.Offered), required, strings.Join(p.Unprogressed, ", "))
}

// recordDispatchFailure writes the audit row for a dispatch that failed and was
// not turned into a park, carrying the round's attempt.
func recordDispatchFailure(ctx context.Context, current workitem.Item, to, attempt string, res DispatchResult, derr error, now time.Time) {
	appendLedgerEntry(ctx, current.ID, ledger.KindAgentInvocation, "executor",
		fmt.Sprintf("named-agent dispatch failed for %s→%s: %v", current.Status, to, derr),
		performerFailurePayload(current.Status, to, res.Skill, attempt, res.LastMessage, res.Skills), now)
	notifyChange(panelTopic(current.Kind))
}

// RecordStepPerformance writes the agent_invocation row for a step performed
// AFTER its entry — the headless run of an executor-allocated step — in the shape
// dispatchPerformer writes for a named performer's step, so the cost and
// timeline views read both alike. A failed run is recorded the same way a failed
// dispatch is.
//
// The skill-evidence check (sty_23ceeca2) is applied as it is to a dispatched
// step: its gap is in front of the driver, and under the workflow's
// skill_evidence = reject the returned error says the run is refused. The run's
// tool calls are recorded too, so the step's exit gate judges the same calls.
func RecordStepPerformance(ctx context.Context, item workitem.Item, step string, res DispatchResult, runErr error) error {
	now := time.Now()
	if runErr != nil {
		appendLedgerEntry(ctx, item.ID, ledger.KindAgentInvocation, "executor",
			fmt.Sprintf("executor run for step %q failed: %v", step, runErr),
			performerFailurePayload(step, step, res.Skill, "", res.LastMessage, res.Skills), now)
		notifyChange(panelTopic(item.Kind))
		return nil
	}
	modelNote := ""
	if res.Model != "" {
		modelNote = " on model " + res.Model
	}
	appendLedgerEntry(ctx, item.ID, ledger.KindAgentInvocation, "executor",
		fmt.Sprintf("performed step %q with the %s binding%s (%s) with @skill:%s",
			step, res.Agent, modelNote, res.Command, res.Skill),
		dispatchPayload(step, step, res, ""), now)
	recordRunTrace(ctx, item.ID, step, res.Trace, now)
	if res.Evidence != nil && len(res.Evidence.Gaps()) > 0 {
		reportSkillEvidence(item.ID, step, res.Evidence)
		if res.Evidence.Blocks() {
			return fmt.Errorf("step %q refused by the skill-evidence check (workflow skill_evidence = reject): %s", step, res.Evidence.Notes())
		}
	}
	return nil
}

// recordPerformerReject writes a readiness round's performer verdict when the
// performer did not accept — it rejected the premise or failed to run — under
// the round's attempt id, with the notes that say why.
func recordPerformerReject(ctx context.Context, current workitem.Item, to, attempt string, res DispatchResult, notes string, now time.Time) {
	skill := res.Skill
	if skill == "" {
		skill = res.Agent
	}
	payload, err := json.Marshal(struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Attempt string `json:"attempt,omitempty"`
		Skill   string `json:"skill,omitempty"`
		Agent   string `json:"agent,omitempty"`
		Notes   string `json:"notes,omitempty"`
	}{From: current.Status, To: to, Attempt: attempt, Skill: skill, Agent: res.Agent, Notes: notes})
	if err != nil {
		return
	}
	appendLedgerEntry(ctx, current.ID, ledger.KindPerformerReject, "executor",
		fmt.Sprintf("performer rejected %s→%s: %s", current.Status, to, notes), payload, now)
}

// recordReadinessAccept writes the readiness_accept row naming the attempt, but
// only for an attempt the ledger's single rule accepts: no performer reject or
// failure and no reviewer reject among its rows. It reports whether it wrote.
// The row is stamped at the moment it is written — after every row the round
// made, the plan document included — so a plan attached by the round itself is
// never "attached after" its own accept.
func recordReadinessAccept(ctx context.Context, current workitem.Item, from, to, attempt, step string) bool {
	if !ledger.AttemptAccepted(storyLedgerEntries(ctx, current.ID), from, to, attempt) {
		return false
	}
	payload, err := json.Marshal(struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Attempt string `json:"attempt"`
		Step    string `json:"step,omitempty"`
	}{From: from, To: to, Attempt: attempt, Step: step})
	if err != nil {
		return false
	}
	appendLedgerEntry(ctx, current.ID, ledger.KindReadinessAccept, "executor",
		fmt.Sprintf("readiness accepted for %s→%s (attempt %s)", from, to, attempt), payload, time.Now())
	return true
}

// recordReadinessReuse writes the row that says a presented readiness edge did
// not run its performer or reviewers, because the readiness accepted in attempt
// still holds.
func recordReadinessReuse(ctx context.Context, current workitem.Item, from, to, attempt string) {
	appendLedgerEntry(ctx, current.ID, ledger.KindReadinessReuse, "executor",
		fmt.Sprintf("readiness for %s→%s reused from accepted attempt %s: the definition and plan are unchanged since", from, to, attempt),
		attemptPayload(from, to, "", attempt), time.Now())
}

// readinessReusable returns the accepted attempt a presented readiness edge can
// rely on, or "". Only a non-container story reuses: an epic's ready step judges
// its children, which can change without the epic's own definition changing.
func readinessReusable(ctx context.Context, current workitem.Item, to string) string {
	if current.Kind != workitem.KindStory || epicset.IsEpicParent(current) || !stepProposes(ctx, current, to) {
		return ""
	}
	attempt, _ := ledger.ReadinessHolds(storyLedgerEntries(ctx, current.ID), current.Status, to, readinessDocName(ctx, current, to))
	return attempt
}

// readinessDocName is the name of the document the readiness step's performer
// attaches (its skill's output_name), as the engine's performer rule reports it.
// Empty when the step has no dispatched performer or its skill declares no output
// contract: the binary names no document of its own.
func readinessDocName(ctx context.Context, item workitem.Item, to string) string {
	resolver, ok := executorDispatcher.(PerformerResolver)
	if !ok {
		return ""
	}
	perf, err := resolver.StepPerformer(ctx, item, to)
	if err != nil {
		return ""
	}
	return perf.OutputName
}

// voidReadinessOnAttach drops the ready mark when the document just attached is
// the readiness step's plan, which voids an accepted readiness. It is the hook the
// shared attach core calls, so `story attach` and the engine behave alike.
func voidReadinessOnAttach(ctx context.Context, item workitem.Item, name string) {
	if item.Kind != workitem.KindStory {
		return
	}
	edge, err := readinessEdgeFor(ctx, item)
	if err != nil {
		return
	}
	if doc := readinessDocName(ctx, item, edge.To); doc != "" && doc == name {
		syncReadyMark(ctx, item.ID)
	}
}

// roundResult is the outcome of one unengaged readiness round.
type roundResult struct {
	Attempt  string
	Accepted bool
	// Reason is why the round was not accepted: the performer's or reviewers'
	// notes, as recorded on the ledger.
	Reason string
}

// flagReadinessInFlight makes item's seat row effectively in flight for the length
// of a readiness round, so the page's running indicator shows the dispatch
// (sty_a42f4add) — and returns the undo the round defers. It claims no seat and
// takes none over: a story holding no row gets a non-story-seat one the undo
// deletes; a row that is settled (the seat kept after a refused engagement, or an
// engaged child readied from the epic) has only its in-flight columns flagged, and
// the undo settles them again and clears the activity the dispatch stamped; a row
// already effectively in flight belongs to the dispatch running on it and is left
// alone. A row's owner, story_seat, seat_key and state are never written, and a
// row the round did not insert is never deleted. Best-effort: observability must
// not fail a readiness round, so any store error just means no indicator.
func flagReadinessInFlight(ctx context.Context, item workitem.Item, state string) func() {
	ls, err := requireLease()
	if err != nil {
		return func() {}
	}
	// A cancelled round must still put the seat back as it found it.
	done := context.WithoutCancel(ctx)
	owner := lease.ResolveOwner()
	inserted, err := ls.InsertIfAbsent(ctx, lease.AcquireOpts{
		ItemID: item.ID, Kind: string(item.Kind), Owner: owner, State: state, SeatKey: item.ID,
	})
	if err != nil {
		return func() {}
	}
	if inserted {
		return func() { _ = ls.Release(done, item.ID, owner) }
	}
	if marked, err := ls.MarkInFlightIfSettled(ctx, item.ID, time.Now()); err == nil && marked {
		return func() { _ = ls.ClearInFlight(done, item.ID) }
	}
	return func() {}
}

// runReadinessRound runs one readiness round for item on edge without engaging
// it: the performer, then the step's reviewers, under one attempt id created
// here. It writes no status and claims no seat — it only shows its dispatch in
// flight on the seat row the story has or, briefly, a row of its own — and it
// never parks: a rejected round is recorded on the ledger with its notes and the
// story stays where it is. The round is accepted when no row of its attempt
// rejects, and only then is the readiness_accept row written.
func runReadinessRound(ctx context.Context, item workitem.Item, edge readinessEdge) roundResult {
	now := time.Now()
	defer flagReadinessInFlight(ctx, item, edge.From)()
	attempt := ledger.NewAttemptID()
	res := roundResult{Attempt: attempt}
	reject := func(reason string) roundResult {
		res.Reason = reason
		syncReadyMark(ctx, item.ID)
		notifyChange(panelTopic(item.Kind))
		return res
	}
	run := dispatchPerformer(ctx, item, edge.To, attempt, true, now)
	item = run.rebind(item)
	switch {
	case run.Err != nil:
		recordDispatchFailure(ctx, item, edge.To, attempt, run.Res, run.Err, now)
		return reject(run.Err.Error())
	case run.Refuse != nil:
		return reject(run.Refuse.Error())
	}
	if transitionGater != nil {
		if _, gerr := recordGateRound(ctx, item, edge.To, attempt, now); gerr != nil {
			return reject(gerr.Error())
		}
	}
	res.Accepted = recordReadinessAccept(ctx, item, edge.From, edge.To, attempt, edge.name())
	if !res.Accepted {
		return reject("the round's own rows reject it")
	}
	return res
}

// readinessHolds reports whether item's readiness is accepted and still holds,
// from the ledger alone.
func readinessHolds(ctx context.Context, item workitem.Item, edge readinessEdge) (string, bool) {
	return ledger.ReadinessHolds(storyLedgerEntries(ctx, item.ID), edge.From, edge.To, readinessDocName(ctx, item, edge.To))
}

// refuseUnheldReadinessMark is the one rule for who may put the readiness mark on
// a story: only an accepted readiness round. finalTags is the tag set a write
// would leave on item; the write is refused when it newly carries the mark while
// item's readiness does not hold in the ledger. A mark a stored item already
// carries is kept, never newly set, and a route that declares no mark refuses
// nothing. A create passes a draft (no id, so it carries nothing and has no
// ledger): a mark on it is always refused.
func refuseUnheldReadinessMark(ctx context.Context, item workitem.Item, finalTags []string) error {
	edge, err := readinessEdgeFor(ctx, item)
	if err != nil || edge.Step.Mark == "" {
		return nil
	}
	mark := edge.Step.Mark
	if !hasTag(finalTags, mark) || (item.ID != "" && hasTag(item.Tags, mark)) {
		return nil
	}
	if _, ok := readinessHolds(ctx, item, edge); ok {
		return nil
	}
	return fmt.Errorf("readiness mark %q can be set only by an accepted readiness round: %s has none that holds", mark, describeItem(item))
}

// describeItem names item for a refusal: its id, or "a new <kind>" before it has one.
func describeItem(item workitem.Item) string {
	if item.ID == "" {
		return "a new " + string(item.Kind)
	}
	return item.ID
}

// syncReadyMark removes the readiness mark from the story when its readiness
// stops holding. The tag is a display marker only: the ledger decides, this just
// keeps the display from claiming what the ledger no longer says. A no-op for a
// story whose route declares no mark, that does not carry it, or whose readiness
// holds. Best-effort, like the other trail rows.
func syncReadyMark(ctx context.Context, id string) {
	if _, err := requireWorkItem(); err != nil {
		return
	}
	item, err := storyState(ctx, id)
	if err != nil || item.Kind != workitem.KindStory {
		return
	}
	edge, err := readinessEdgeFor(ctx, item)
	if err != nil || edge.Step.Mark == "" || !hasTag(item.Tags, edge.Step.Mark) {
		return
	}
	if _, ok := readinessHolds(ctx, item, edge); ok {
		return
	}
	_, _ = workItemSetCore(ctx, tagRequest(setReq{ID: id, RemoveTags: []string{edge.Step.Mark}}))
}

// markReady puts the readiness step's mark tag on the story, through the same
// tag path `story set --add-tags` uses.
func markReady(ctx context.Context, id, mark string) error {
	if mark == "" {
		return nil
	}
	_, err := workItemSetCore(ctx, tagRequest(setReq{ID: id, AddTags: []string{mark}}))
	return err
}

// errEpicAcceptFound stops firstEpicReadiness's ledger scan at the first match.
var errEpicAcceptFound = errors.New("an epic-parent readiness accept exists")

// firstEpicReadiness reports whether an accepted readiness round on item would be
// the repo's first on an epic-parent: item is one, and no epic-parent, item
// included, has a readiness_accept row yet. The ledger row decides, never the
// mark, which syncReadyMark removes when readiness stops holding. A caller reads
// it before the round runs, since the round writes the row it would find.
func firstEpicReadiness(ctx context.Context, item workitem.Item) bool {
	if !epicset.IsEpicParent(item) {
		return false
	}
	led, err := requireLedger()
	if err != nil {
		return false
	}
	store, err := requireWorkItem()
	if err != nil {
		return false
	}
	seen := map[string]bool{}
	err = led.ForEachKind(ctx, "", ledger.KindReadinessAccept, func(e ledger.Entry) error {
		if seen[e.StoryID] {
			return nil
		}
		seen[e.StoryID] = true
		if e.StoryID == item.ID {
			return errEpicAcceptFound
		}
		if it, gerr := store.Get(ctx, e.StoryID); gerr == nil && epicset.IsEpicParent(it) {
			return errEpicAcceptFound
		}
		return nil
	})
	return err == nil
}

// epicAdvice is the skill of each advisor the route declares on the readiness
// step entered at status to, in declared order and deduplicated: what an accepted
// first epic readiness round prompts the orchestrator to run.
func epicAdvice(to string, advisors []wfroute.Advisor) []string {
	var out []string
	for _, ad := range advisors {
		if ad.Step == to && ad.Skill != "" && !slices.Contains(out, ad.Skill) {
			out = append(out, ad.Skill)
		}
	}
	return out
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// tagRequest renders the story-set request that adds or removes a tag.
func tagRequest(req setReq) json.RawMessage {
	b, _ := json.Marshal(req)
	return b
}
