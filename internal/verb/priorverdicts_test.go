package verb_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
)

// wireLedgerOnly opens a store with just the stores PriorVerdicts reads, so the
// ledger→verdict read is exercised without a workflow in the way.
func wireLedgerOnly(t *testing.T) *store.DB {
	t.Helper()
	withWiring(t)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "satelle.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	verb.SetWorkItemStore(db.Stories)
	verb.SetLedgerStore(db.Ledger)
	verb.SetTxRunner(db.InTx)
	verb.SetStoryDir(filepath.Join(dir, "stories"))
	t.Cleanup(func() {
		db.Close()
	})
	return db
}

// seedVerdict appends a reviewer row in the shape reviewerPayload writes.
func seedVerdict(t *testing.T, storyID, kind, from, to, skill, notes string) {
	t.Helper()
	call(t, "ledger-append", map[string]any{
		"story_id": storyID,
		"kind":     kind,
		"actor":    "reviewer",
		"body":     from + "→" + to + " by " + skill,
		"payload": map[string]any{
			"from": from, "to": to, "skill": skill, "order": 0,
			"notes": notes, "accept": kind == "review_accept",
		},
	})
}

// TestPriorVerdictsFiltersToTheEdge (sty_0f5e600c AC2/AC4): the read returns this
// story's verdicts on THIS edge, oldest first, and no other edge's.
func TestPriorVerdictsFiltersToTheEdge(t *testing.T) {
	wireLedgerOnly(t)
	ctx := context.Background()

	seedVerdict(t, "sty_pv1", "review_reject", "backlog", "plan", "satelle-story-intent-review", "OTHER-EDGE-NOTE")
	seedVerdict(t, "sty_pv1", "review_reject", "plan", "in_progress", "satelle-story-plan-review", "FIRST-EDGE-NOTE")
	seedVerdict(t, "sty_pv1", "review_accept", "plan", "in_progress", "satelle-story-plan-review", "SECOND-EDGE-NOTE")
	seedVerdict(t, "sty_pv1", "status_transition", "plan", "in_progress", "", "NOT-A-VERDICT")
	seedVerdict(t, "sty_other", "review_reject", "plan", "in_progress", "satelle-story-plan-review", "OTHER-STORY-NOTE")

	got, err := verb.PriorVerdicts(ctx, "sty_pv1", "plan", "in_progress", verb.StaleExemption{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d verdicts, want 2: %+v", len(got), got)
	}
	if got[0].Notes != "FIRST-EDGE-NOTE" || got[0].Decision != "reject" {
		t.Errorf("first verdict = %+v, want the reject, oldest first", got[0])
	}
	if got[1].Notes != "SECOND-EDGE-NOTE" || got[1].Decision != "accept" {
		t.Errorf("second verdict = %+v, want the accept", got[1])
	}
	if got[0].Skill != "satelle-story-plan-review" {
		t.Errorf("skill = %q, want the judging skill", got[0].Skill)
	}
	if got[0].CreatedAt == "" {
		t.Error("created_at must be stamped from the ledger row")
	}

	// A first attempt on an untried edge reads as nothing at all.
	none, err := verb.PriorVerdicts(ctx, "sty_pv1", "in_progress", "done", verb.StaleExemption{})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("untried edge returned %+v, want none", none)
	}
}

// TestPriorVerdictsWithoutLedgerIsInert (sty_0f5e600c): prior verdicts are
// additive context — an unwired ledger degrades to nothing, never an error that
// could fail the transition it decorates.
func TestPriorVerdictsWithoutLedgerIsInert(t *testing.T) {
	withWiring(t)
	verb.SetLedgerStore(nil)
	got, err := verb.PriorVerdicts(context.Background(), "sty_none", "plan", "in_progress", verb.StaleExemption{})
	if err != nil || got != nil {
		t.Fatalf("PriorVerdicts with no ledger = (%+v, %v), want (nil, nil)", got, err)
	}
}

// seedAt appends a ledger row with an explicit created_at, so a test can place
// real timestamps either side of a verdict's recorded_at.
func seedAt(t *testing.T, db *store.DB, storyID, kind string, payload any, at time.Time) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{
		StoryID: storyID, Kind: kind, Actor: "test", Body: kind, Payload: raw,
	}, at); err != nil {
		t.Fatal(err)
	}
}

// seedCycleVerdict appends a review_reject the way reviewerPayload writes it:
// created_at is the transition's start, recorded_at is after the gate ran.
func seedCycleVerdict(t *testing.T, db *store.DB, storyID, reviewed string, created, recorded time.Time) {
	t.Helper()
	payload := map[string]any{
		"from": "plan", "to": "in_progress", "skill": "pv-plan-review", "order": 0,
		"notes": "objection", "reviewed": reviewed, "accept": false,
	}
	if !recorded.IsZero() {
		payload["recorded_at"] = recorded.UTC().Format(time.RFC3339Nano)
	}
	seedAt(t, db, storyID, ledger.KindReviewReject, payload, created)
}

func telemetryKind(kind string) map[string]any {
	return map[string]any{"kind": kind}
}

func latestVerdict(t *testing.T, storyID string) verb.PriorVerdict {
	t.Helper()
	got, err := verb.PriorVerdicts(context.Background(), storyID, "plan", "in_progress", verb.StaleExemption{})
	if err != nil || len(got) == 0 {
		t.Fatalf("PriorVerdicts = (%+v, %v)", got, err)
	}
	return got[len(got)-1]
}

// TestPriorVerdictStaleAfterNewEvidence (sty_0225fc2f AC1): a story log event
// recorded after the reject's cycle ended withholds the quotation and names the
// evidence.
func TestPriorVerdictStaleAfterNewEvidence(t *testing.T) {
	db := wireLedgerOnly(t)
	t0 := time.Now().Add(-time.Hour)
	seedCycleVerdict(t, db, "sty_stale", "QUOTED-WORDS", t0, t0.Add(3*time.Second))
	seedAt(t, db, "sty_stale", ledger.KindTelemetryEvent, telemetryKind("plan-consumed"), t0.Add(4*time.Second))

	v := latestVerdict(t, "sty_stale")
	if v.Reviewed != "" || v.ReviewedTruncated || !v.ReviewedStale {
		t.Fatalf("verdict = %+v, want quotation withheld and reviewed_stale", v)
	}
	if len(v.EvidenceSince) != 1 || v.EvidenceSince[0].Kind != ledger.KindTelemetryEvent ||
		v.EvidenceSince[0].Event != "plan-consumed" || v.EvidenceSince[0].CreatedAt == "" {
		t.Fatalf("evidence_since = %+v, want the plan-consumed telemetry row", v.EvidenceSince)
	}

	// An attach after the cycle counts too.
	seedAt(t, db, "sty_stale", verb.KindStoryDocAttached, map[string]any{"name": "ac-evidence"}, t0.Add(5*time.Second))
	if v := latestVerdict(t, "sty_stale"); len(v.EvidenceSince) != 2 || v.EvidenceSince[1].Kind != verb.KindStoryDocAttached {
		t.Fatalf("evidence_since = %+v, want the attach listed second", v.EvidenceSince)
	}
}

// TestPriorVerdictIgnoresEngagementBookkeeping (sty_2d877fa2 AC1-3): the rows the
// binary writes when a re-presented edge re-engages the story — trunk_check and
// engagement_baseline — never end a verdict cycle, while a real plan attachment
// recorded after them still does and is the only evidence listed.
func TestPriorVerdictIgnoresEngagementBookkeeping(t *testing.T) {
	db := wireLedgerOnly(t)
	t0 := time.Now().Add(-time.Hour)

	seedCycleVerdict(t, db, "sty_engage", "ENGAGE-WORDS", t0, t0.Add(3*time.Second))
	for _, kind := range []string{ledger.KindTrunkCheck, ledger.KindEngagementBaseline} {
		seedAt(t, db, "sty_engage", kind, map[string]any{}, t0.Add(time.Minute))
		if v := latestVerdict(t, "sty_engage"); v.Reviewed != "ENGAGE-WORDS" || v.ReviewedStale || v.EvidenceSince != nil {
			t.Fatalf("after %s verdict = %+v, want quotation kept and no stale marker", kind, v)
		}
	}

	seedAt(t, db, "sty_engage", verb.KindStoryDocAttached, map[string]any{"name": "plan"}, t0.Add(2*time.Minute))
	v := latestVerdict(t, "sty_engage")
	if v.Reviewed != "" || !v.ReviewedStale || len(v.EvidenceSince) != 1 || v.EvidenceSince[0].Kind != verb.KindStoryDocAttached {
		t.Fatalf("verdict = %+v, want stale with only the plan attachment listed", v)
	}
}

// TestPriorVerdictUnchangedKeepsQuotation (AC2): no later evidence, or a legacy
// verdict with no recorded_at, passes through exactly as before.
func TestPriorVerdictUnchangedKeepsQuotation(t *testing.T) {
	db := wireLedgerOnly(t)
	t0 := time.Now().Add(-time.Hour)

	seedCycleVerdict(t, db, "sty_same", "QUOTED-WORDS", t0, t0.Add(time.Second))
	seedAt(t, db, "sty_same", ledger.KindTelemetryEvent, telemetryKind("plan-consumed"), t0.Add(-time.Second)) // before the cycle
	got := latestVerdict(t, "sty_same")
	want := verb.PriorVerdict{Skill: "pv-plan-review", Decision: "reject", Notes: "objection", Reviewed: "QUOTED-WORDS", CreatedAt: got.CreatedAt}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unchanged verdict = %+v, want %+v", got, want)
	}

	// Legacy row: no recorded_at, evidence recorded after it.
	seedCycleVerdict(t, db, "sty_legacy", "OLD-WORDS", t0, time.Time{})
	seedAt(t, db, "sty_legacy", ledger.KindTelemetryEvent, telemetryKind("plan-consumed"), t0.Add(time.Minute))
	if v := latestVerdict(t, "sty_legacy"); v.Reviewed != "OLD-WORDS" || v.ReviewedStale || v.EvidenceSince != nil {
		t.Fatalf("legacy verdict = %+v, want passed through unchanged", v)
	}
}

// TestPriorVerdictIgnoresCycleOwnRows (AC3): rows the review cycle itself wrote —
// real timestamps later than the verdict's transition start — and binary
// self-reports never mark the verdict stale; a story log after the cycle does.
func TestPriorVerdictIgnoresCycleOwnRows(t *testing.T) {
	db := wireLedgerOnly(t)
	t0 := time.Now().Add(-time.Hour)

	// Bundled cycle: transition now = t0, rows written during the run at t0+2s,
	// the verdict appended (recorded_at) at t0+3s.
	seedCycleVerdict(t, db, "sty_bundle", "BUNDLE-WORDS", t0, t0.Add(3*time.Second))
	for _, kind := range []string{"agent-attempt", "gate-bundled", "agent-retry"} {
		seedAt(t, db, "sty_bundle", ledger.KindTelemetryEvent, telemetryKind(kind), t0.Add(2*time.Second))
	}
	seedAt(t, db, "sty_bundle", ledger.KindAgentInvocation, map[string]any{"agent": "reviewer"}, t0.Add(2*time.Second))

	// Propose cycle: the planner's artifact and its telemetry precede the verdict.
	seedCycleVerdict(t, db, "sty_propose", "PROPOSE-WORDS", t0, t0.Add(2*time.Second))
	seedAt(t, db, "sty_propose", verb.KindStoryDocAttached, map[string]any{"name": "plan"}, t0.Add(time.Second))
	seedAt(t, db, "sty_propose", ledger.KindTelemetryEvent, telemetryKind("agent-attempt"), t0.Add(time.Second))

	// Every binary self-report written AFTER the cycle still does not count.
	selfTelemetry := []string{"agent-attempt", "gate-bundled", "agent-retry", "agent-failure", "agent-timeout",
		"agent-stalled", "scoped-gate-skipped", "reviewer-isolation-warned", "agent-secondary-failover", "agent-interactive-denied"}
	selfLedger := []string{ledger.KindAgentInvocation, ledger.KindStepCost, ledger.KindDriverUsage, ledger.KindSessionModel,
		ledger.KindToolPermission, ledger.KindGateSkipped,
		ledger.KindTrunkCheck, ledger.KindEngagementBaseline}
	for _, id := range []string{"sty_bundle", "sty_propose"} {
		for _, kind := range selfTelemetry {
			seedAt(t, db, id, ledger.KindTelemetryEvent, telemetryKind(kind), t0.Add(time.Minute))
		}
		for _, kind := range selfLedger {
			seedAt(t, db, id, kind, map[string]any{}, t0.Add(time.Minute))
		}
	}
	for id, want := range map[string]string{"sty_bundle": "BUNDLE-WORDS", "sty_propose": "PROPOSE-WORDS"} {
		if v := latestVerdict(t, id); v.Reviewed != want || v.ReviewedStale || v.EvidenceSince != nil {
			t.Fatalf("%s verdict = %+v, want quotation kept and no stale marker", id, v)
		}
	}

	// A driver story log after the cycle makes both stale.
	for _, id := range []string{"sty_bundle", "sty_propose"} {
		seedAt(t, db, id, ledger.KindTelemetryEvent, telemetryKind("plan-consumed"), t0.Add(2*time.Minute))
		v := latestVerdict(t, id)
		if v.Reviewed != "" || !v.ReviewedStale || len(v.EvidenceSince) != 1 || v.EvidenceSince[0].Event != "plan-consumed" {
			t.Fatalf("%s verdict = %+v, want stale with only the story log listed", id, v)
		}
	}
}

// TestPriorVerdictIgnoresSiblingVerdictRows (sty_0225fc2f AC3): in a bundled
// cycle the other skills' verdict rows land after this skill's recorded_at;
// review_accept and review_reject rows are the gate's own bookkeeping, so they
// never mark an earlier verdict stale — on the same edge or another.
func TestPriorVerdictIgnoresSiblingVerdictRows(t *testing.T) {
	db := wireLedgerOnly(t)
	t0 := time.Now().Add(-time.Hour)
	seedCycleVerdict(t, db, "sty_sibling", "SIBLING-WORDS", t0, t0.Add(3*time.Second))
	for i, edge := range [][2]string{{"plan", "in_progress"}, {"backlog", "plan"}} {
		at := t0.Add(time.Minute + time.Duration(i)*time.Second)
		for _, kind := range []string{ledger.KindReviewAccept, ledger.KindReviewReject} {
			seedAt(t, db, "sty_sibling", kind, map[string]any{
				"from": edge[0], "to": edge[1], "skill": "pv-sibling-review", "order": 1,
				"notes": "sibling", "reviewed": "OTHER", "accept": kind == ledger.KindReviewAccept,
				"recorded_at": at.UTC().Format(time.RFC3339Nano),
			}, at)
		}
	}
	got, err := verb.PriorVerdicts(context.Background(), "sty_sibling", "plan", "in_progress", verb.StaleExemption{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range got {
		if v.Skill != "pv-plan-review" {
			continue
		}
		found = true
		if v.Reviewed != "SIBLING-WORDS" || v.ReviewedStale || v.EvidenceSince != nil {
			t.Fatalf("pv-plan-review verdict = %+v, want quotation kept and no stale marker after sibling verdict rows", v)
		}
	}
	if !found {
		t.Fatalf("no pv-plan-review verdict in %+v", got)
	}
}

// TestPriorVerdictStaleExemption (sty_59720b8b AC1/AC2): with an exemption, a
// re-attach of the performer's output document and directed messages to the
// performer leave the quotation in place; every other evidence row still marks
// it stale, and with no exemption the same rows go stale as before.
func TestPriorVerdictStaleExemption(t *testing.T) {
	ex := verb.StaleExemption{DocName: "plan", Performer: "planner"}
	type row struct {
		name, kind string
		payload    any
	}
	exempt := []row{
		{"re-attach of the output document", verb.KindStoryDocAttached, map[string]any{"name": "plan", "type": "plan"}},
		{"directed message to the performer", ledger.KindAgentMessage, map[string]any{"from": "orchestrator", "to": "planner", "body": "fix AC2"}},
	}
	counted := []row{
		{"story log", ledger.KindTelemetryEvent, telemetryKind("plan-consumed")},
		{"another document", verb.KindStoryDocAttached, map[string]any{"name": "ac-evidence", "type": "output"}},
		{"message to another agent", ledger.KindAgentMessage, map[string]any{"from": "orchestrator", "to": "coder", "body": "x"}},
		{"message to the performer copied to another address", ledger.KindAgentMessage, map[string]any{"from": "orchestrator", "to": "planner", "cc": "reviewer", "body": "x"}},
		{"definition edit", ledger.KindDefinitionEdited, map[string]any{"field": "acceptance_criteria"}},
	}
	verdictWith := func(t *testing.T, rows []row, ex verb.StaleExemption) verb.PriorVerdict {
		t.Helper()
		db := wireLedgerOnly(t)
		t0 := time.Now().Add(-time.Hour)
		seedCycleVerdict(t, db, "sty_ex", "QUOTED-WORDS", t0, t0.Add(3*time.Second))
		for i, r := range rows {
			seedAt(t, db, "sty_ex", r.kind, r.payload, t0.Add(time.Duration(5+i)*time.Second))
		}
		got, err := verb.PriorVerdicts(context.Background(), "sty_ex", "plan", "in_progress", ex)
		if err != nil || len(got) == 0 {
			t.Fatalf("PriorVerdicts = (%+v, %v)", got, err)
		}
		return got[len(got)-1]
	}
	for _, tc := range exempt {
		t.Run("exempt/"+tc.name, func(t *testing.T) {
			if v := verdictWith(t, []row{tc}, ex); v.Reviewed != "QUOTED-WORDS" || v.ReviewedStale || v.EvidenceSince != nil {
				t.Errorf("verdict = %+v, want the quotation kept", v)
			}
			if v := verdictWith(t, []row{tc}, verb.StaleExemption{}); v.Reviewed != "" || !v.ReviewedStale {
				t.Errorf("without an exemption verdict = %+v, want stale", v)
			}
		})
	}
	for _, tc := range counted {
		t.Run("still stale/"+tc.name, func(t *testing.T) {
			if v := verdictWith(t, []row{tc}, ex); v.Reviewed != "" || !v.ReviewedStale || len(v.EvidenceSince) != 1 {
				t.Errorf("verdict = %+v, want stale with the row listed", v)
			}
		})
	}
	t.Run("a mix stays stale", func(t *testing.T) {
		v := verdictWith(t, []row{exempt[0], counted[0], exempt[1]}, ex)
		if v.Reviewed != "" || !v.ReviewedStale || len(v.EvidenceSince) != 1 || v.EvidenceSince[0].Event != "plan-consumed" {
			t.Errorf("verdict = %+v, want stale with only the story log listed", v)
		}
	})
}

// sty_1b0233ba: the dispatch engine writes a plan-consumed row on every coder
// dispatch, after the verdicts of the edge being re-presented. It is the engine's
// own bookkeeping, so it must not clear a verdict's quotation — while the same
// kind from a driver's story log (no source=dispatch) still does.
func TestPriorVerdictIgnoresTheEnginesPlanConsumedRow(t *testing.T) {
	engineRow := map[string]any{"kind": verb.TelemetryPlanConsumed, "data": map[string]any{
		"attachment": "plan", "attempt": 1, "source": verb.TelemetrySourceDispatch,
	}}
	for _, tc := range []struct {
		name      string
		payload   map[string]any
		wantStale bool
	}{
		{"engine row at dispatch", engineRow, false},
		{"driver story log", telemetryKind(verb.TelemetryPlanConsumed), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := wireLedgerOnly(t)
			t0 := time.Now().Add(-time.Hour)
			seedCycleVerdict(t, db, "sty_pc", "QUOTED-WORDS", t0, t0.Add(3*time.Second))
			seedAt(t, db, "sty_pc", ledger.KindTelemetryEvent, tc.payload, t0.Add(5*time.Second))
			got, err := verb.PriorVerdicts(context.Background(), "sty_pc", "plan", "in_progress", verb.StaleExemption{})
			if err != nil || len(got) == 0 {
				t.Fatalf("PriorVerdicts = (%+v, %v)", got, err)
			}
			if v := got[len(got)-1]; v.ReviewedStale != tc.wantStale || (v.Reviewed == "") != tc.wantStale {
				t.Errorf("verdict = %+v, want stale=%t", v, tc.wantStale)
			}
		})
	}
}

// The engine's plan-consumed row carries its content hash as "sha256:<hex>": a
// bare 64-hex value is credential-shaped and the telemetry writer would replace
// it with a refusal marker, losing the hash.
func TestPlanConsumedHashPassesTheTelemetryWriter(t *testing.T) {
	hex64 := strings.Repeat("ab", 32)
	data := func(hash string) map[string]any {
		return map[string]any{"attachment": "plan", "content_hash": hash, "attempt": 1, "source": verb.TelemetrySourceDispatch}
	}
	if err := verb.AppendTelemetry(context.Background(), "sty_pc", "coder", verb.TelemetryPlanConsumed, data("sha256:"+hex64)); err != nil {
		t.Errorf("prefixed hash refused: %v", err)
	}
	if err := verb.AppendTelemetry(context.Background(), "sty_pc", "coder", verb.TelemetryPlanConsumed, data(hex64)); err == nil {
		t.Errorf("a bare 64-hex value is no longer refused; the sha256: prefix may be dropped")
	}
}
