package verb

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wfdot"
)

// PriorVerdict is one verdict already recorded for an item on one from→to edge.
// Decision is "accept" or "reject"; CreatedAt is RFC3339.
type PriorVerdict struct {
	Skill             string `json:"skill,omitempty"`
	Decision          string `json:"decision"`
	Notes             string `json:"notes,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
	Reviewed          string `json:"reviewed,omitempty"`
	ReviewedTruncated bool   `json:"reviewed_truncated,omitempty"`
	// Criteria is the verdict's per-criterion entries (sty_99d3162b). markStale
	// clears each entry's Reviewed together with the row's.
	Criteria []CriterionVerdict `json:"criteria,omitempty"`
	// ReviewedStale marks a skill's latest verdict whose review cycle ended
	// before new evidence was recorded on the story. Reviewed is then cleared —
	// the rubric's "no reviewed string, re-judge" rule fires — and
	// EvidenceSince lists what was recorded since (sty_0225fc2f).
	ReviewedStale bool            `json:"reviewed_stale,omitempty"`
	EvidenceSince []EvidenceSince `json:"evidence_since,omitempty"`
	// ReviewMode is the mode the verdict's gate ran in when it was fail-fast;
	// empty is full. Coverage is what the reviewer said it covered. Partial is
	// derived: true when the verdict did not cover the whole artifact, so the
	// remainder it did not review is still unjudged. A partial reject is never
	// acceptance of that remainder (sty_93359e0f).
	ReviewMode string    `json:"review_mode,omitempty"`
	Coverage   *Coverage `json:"coverage,omitempty"`
	Partial    bool      `json:"partial,omitempty"`
	// Seats is the panel fold recorded on a panel gate's verdict row, each seat
	// with its own quotation. markStale clears every seat's quotation together
	// with the row's, so a stale seat is re-judged in full (sty_da9e8d0a).
	Seats []PanelSeat `json:"seats,omitempty"`
}

// EvidenceSince is one ledger row recorded after a verdict's review cycle
// ended. Event is the telemetry envelope's kind when the row is a telemetry
// event (e.g. plan-consumed), else empty.
type EvidenceSince struct {
	Kind      string `json:"kind"`
	Event     string `json:"event,omitempty"`
	CreatedAt string `json:"created_at"`
}

// maxEvidenceSince bounds the evidence list a stale verdict carries.
const maxEvidenceSince = 20

// isSelfLedgerKind reports whether kind is a ledger kind the binary writes about
// its own review, dispatch, engagement or start-of-work bookkeeping. These are
// never evidence a rubric could be waiting on, so they never mark a verdict
// stale — even when written after the cycle ended. Every re-presentation re-engages
// the story, which writes trunk_check and engagement_baseline; counting them
// would clear the prior verdict's quotation on every such edge.
func isSelfLedgerKind(kind string) bool {
	switch kind {
	case ledger.KindAgentInvocation, ledger.KindStepCost, ledger.KindDriverUsage,
		ledger.KindSessionModel, ledger.KindToolPermission, ledger.KindToolTrace, ledger.KindSkillDelivery,
		ledger.KindGateSkipped, ledger.KindCommandRule,
		ledger.KindReviewAccept, ledger.KindReviewReject, ledger.KindMessageMark,
		ledger.KindTrunkCheck, ledger.KindEngagementBaseline:
		return true
	}
	return false
}

// isSelfTelemetryKind reports whether a telemetry envelope kind is one the
// dispatch engine writes for itself. Driver-recorded telemetry (story log) is
// not among them — including a plan-consumed row, which is the engine's own only
// when its data names source=dispatch.
func isSelfTelemetryKind(env telemetryEnvelope) bool {
	if env.Kind == TelemetryPlanConsumed {
		return env.Data["source"] == TelemetrySourceDispatch
	}
	switch env.Kind {
	case "agent-attempt", "gate-bundled", "gate-carried", "agent-retry", "agent-failure",
		"agent-timeout", "agent-stalled", "scoped-gate-skipped",
		"reviewer-isolation-warned", "agent-secondary-failover", "agent-interactive-denied":
		return true
	}
	return false
}

// priorVerdictRow is the reviewer row's payload as reviewerPayload
// (workitem.go) writes it — the decision itself comes from the row's KIND,
// which is what the trail is indexed by.
type priorVerdictRow struct {
	From              string `json:"from"`
	To                string `json:"to"`
	Skill             string `json:"skill,omitempty"`
	Notes             string `json:"notes,omitempty"`
	Reviewed          string `json:"reviewed,omitempty"`
	ReviewedTruncated bool   `json:"reviewed_truncated,omitempty"`
	// Criteria is the verdict's per-criterion entries (reviewerPayload).
	Criteria   []CriterionVerdict `json:"criteria,omitempty"`
	ReviewMode string             `json:"review_mode,omitempty"`
	// Coverage / Seats are read to decide whether the verdict was partial.
	// Seats is the panel fold, one record per seat (reviewerPayload).
	Coverage *Coverage   `json:"coverage,omitempty"`
	Seats    []PanelSeat `json:"seats,omitempty"`
	// RecordedAt is the wall-clock time the verdict row was appended, after the
	// gate run that produced it finished. Rows written before the field existed
	// carry none and are never marked stale.
	RecordedAt string `json:"recorded_at,omitempty"`
}

// PriorVerdicts returns every review verdict already recorded for itemID on the
// from→to edge, oldest first — the enumeration a re-reviewing gate needs to
// judge the delta rather than re-deriving a verdict from scratch
// (sty_0f5e600c). Rows for other edges of the same story are excluded. The
// ledger already carries the edge in each reviewer row's payload, so this is a
// read: no schema change, and history written before the feature works
// retroactively.
//
// Each skill's LATEST verdict is checked for staleness: when a story ledger row
// that is not a binary self-report was recorded after that verdict's cycle ended
// (its recorded_at), the verdict loses its Reviewed quotation and carries
// ReviewedStale and EvidenceSince instead. The gate then re-judges rather than
// re-issuing a verdict whose material it could not see changing. The binary only
// reports that evidence arrived; whether it answers the objection stays the
// rubric's call.
//
// Nil-safe when no ledger is wired, and never an error the caller must handle
// specially: prior verdicts are additive context, so an unreadable row is
// skipped rather than failing the transition it decorates.
func PriorVerdicts(ctx context.Context, itemID, from, to string, ex StaleExemption) ([]PriorVerdict, error) {
	if ledgerStore == nil || strings.TrimSpace(itemID) == "" {
		return nil, nil
	}
	entries, err := ledgerStore.ListByStory(ctx, itemID, "")
	if err != nil {
		return nil, err
	}
	return PriorVerdictsFrom(entries, from, to, ex), nil
}

// StaleExemption names the two kinds of row that do not mark a verdict stale
// when a step declares reviewer_carry (sty_59720b8b): a re-attach of the step
// performer's output document, and a directed message to that performer. Both
// land in the document the reviewer's quotation is checked against, so a
// quotation whose words still appear in it (agentstep's quotationCarries) has
// captured the change. The zero
// value exempts nothing — staleness is exactly the unqualified rule. The engine
// is the only reader of the route key; it hands the resolved names in.
type StaleExemption struct {
	// DocName is the performer's output document name (e.g. "plan").
	DocName string
	// Performer is the agent the performer's directed messages are addressed to.
	Performer string
}

// exempts reports whether e is a row the exemption excludes from evidence.
func (x StaleExemption) exempts(e ledger.Entry) bool {
	switch e.Kind {
	case ledger.KindStoryDocAttached:
		if x.DocName == "" {
			return false
		}
		var d struct {
			Name string `json:"name"`
		}
		return json.Unmarshal(e.Payload, &d) == nil && d.Name == x.DocName
	case ledger.KindAgentMessage:
		if x.Performer == "" {
			return false
		}
		var m AgentMessage
		return json.Unmarshal(e.Payload, &m) == nil && m.Cc == "" && m.To == x.Performer
	}
	return false
}

// PriorVerdictsFrom is the enumeration PriorVerdicts runs over a story's ledger
// rows, oldest first. It is exported so a replay can rebuild what a gate injected
// from rows alone.
func PriorVerdictsFrom(entries []ledger.Entry, from, to string, ex StaleExemption) []PriorVerdict {
	var out []PriorVerdict
	var recorded []time.Time // parallel to out; zero when the row has no recorded_at
	for _, e := range entries {
		var decision string
		switch e.Kind {
		case ledger.KindReviewAccept:
			decision = "accept"
		case ledger.KindReviewReject:
			decision = "reject"
		default:
			continue
		}
		var row priorVerdictRow
		if err := json.Unmarshal(e.Payload, &row); err != nil {
			continue
		}
		if row.From != from || row.To != to {
			continue
		}
		out = append(out, PriorVerdict{
			Skill:             row.Skill,
			Decision:          decision,
			Notes:             row.Notes,
			CreatedAt:         e.CreatedAt.UTC().Format(time.RFC3339),
			Reviewed:          row.Reviewed,
			ReviewedTruncated: row.ReviewedTruncated,
			Criteria:          row.Criteria,
			ReviewMode:        row.ReviewMode,
			Coverage:          row.Coverage,
			Partial:           partialVerdict(decision, row),
			Seats:             row.Seats,
		})
		var at time.Time
		if row.RecordedAt != "" {
			at, _ = time.Parse(time.RFC3339Nano, row.RecordedAt)
		}
		recorded = append(recorded, at)
	}
	markStale(out, recorded, entries, ex)
	return out
}

// partialVerdict reports whether a recorded verdict left part of the artifact
// unjudged: a reject from a fail-fast gate (it stopped at the first blocking
// finding, so it said nothing about the rest), or any verdict — or panel seat —
// whose stated coverage is not complete. An accept that stated no coverage, in
// either mode, covered everything it was asked to.
func partialVerdict(decision string, row priorVerdictRow) bool {
	if row.Coverage != nil && !row.Coverage.Complete {
		return true
	}
	for _, seat := range row.Seats {
		if seat.Coverage != nil && !seat.Coverage.Complete {
			return true
		}
	}
	return decision == "reject" && row.ReviewMode == wfdot.ReviewModeFailFast
}

// markStale applies the staleness rule to each skill's latest verdict in out.
func markStale(out []PriorVerdict, recorded []time.Time, entries []ledger.Entry, ex StaleExemption) {
	latest := make(map[string]int, len(out))
	for i, v := range out {
		latest[v.Skill] = i
	}
	for _, i := range latest {
		if recorded[i].IsZero() {
			continue
		}
		since := evidenceAfter(entries, recorded[i], ex)
		if len(since) == 0 {
			continue
		}
		out[i].Reviewed, out[i].ReviewedTruncated = "", false
		out[i].Criteria = withoutQuotations(out[i].Criteria)
		if len(out[i].Seats) > 0 {
			seats := append([]PanelSeat(nil), out[i].Seats...)
			for j := range seats {
				seats[j].Reviewed, seats[j].ReviewedTruncated = "", false
				seats[j].Criteria = withoutQuotations(seats[j].Criteria)
			}
			out[i].Seats = seats
		}
		out[i].ReviewedStale, out[i].EvidenceSince = true, since
	}
}

// IsEvidenceRow reports whether a ledger row is evidence a reviewer or a relay
// could be waiting on: not a binary self-report, and not the rework relay's own
// transcript. The transcript is a cc="*" agent_message (the relay broadcasts
// every turn through story-message) — conversation about the work, not work. A
// directed message (no cc), a story log and an attachment are evidence.
func IsEvidenceRow(e ledger.Entry) bool {
	if isSelfLedgerKind(e.Kind) {
		return false
	}
	switch e.Kind {
	case ledger.KindTelemetryEvent:
		var env telemetryEnvelope
		if json.Unmarshal(e.Payload, &env) == nil && isSelfTelemetryKind(env) {
			return false
		}
	case ledger.KindAgentMessage:
		var m AgentMessage
		if json.Unmarshal(e.Payload, &m) == nil && m.Cc == "*" {
			return false
		}
	}
	return true
}

// evidenceAfter lists the rows recorded strictly after cycleEnd that are
// evidence (IsEvidenceRow) and not exempted by ex, oldest first, capped at
// maxEvidenceSince.
func evidenceAfter(entries []ledger.Entry, cycleEnd time.Time, ex StaleExemption) []EvidenceSince {
	var since []EvidenceSince
	for _, e := range entries {
		if !e.CreatedAt.After(cycleEnd) || !IsEvidenceRow(e) || ex.exempts(e) {
			continue
		}
		var event string
		if e.Kind == ledger.KindTelemetryEvent {
			var env telemetryEnvelope
			if json.Unmarshal(e.Payload, &env) == nil {
				event = env.Kind
			}
		}
		since = append(since, EvidenceSince{Kind: e.Kind, Event: event, CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339Nano)})
		if len(since) == maxEvidenceSince {
			break
		}
	}
	return since
}

// AgentMessage is the addressing of one agent_message row, all a prior-verdict
// reader needs of it.
type AgentMessage struct {
	From string `json:"from"`
	To   string `json:"to"`
	Cc   string `json:"cc,omitempty"`
	Body string `json:"body"`
}
