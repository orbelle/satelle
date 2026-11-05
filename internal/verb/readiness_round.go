package verb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
	"strings"
	"time"
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

// describeItem names item for a refusal: its id, or "a new <kind>" before it has one.
func describeItem(item workitem.Item) string {
	if item.ID == "" {
		return "a new " + string(item.Kind)
	}
	return item.ID
}

// errEpicAcceptFound stops firstEpicReadiness's ledger scan at the first match.
var errEpicAcceptFound = errors.New("an epic-parent readiness accept exists")

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
