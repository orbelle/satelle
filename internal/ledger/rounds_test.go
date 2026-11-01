package ledger

import (
	"encoding/json"
	"strings"
	"testing"
)

func row(kind string, payload map[string]string) Entry {
	b, _ := json.Marshal(payload)
	return Entry{ID: NewID(), Kind: kind, Payload: b}
}

func reject(attempt, skill, notes string) Entry {
	return row(KindReviewReject, map[string]string{"from": "backlog", "to": "plan", "attempt": attempt, "skill": skill, "notes": notes})
}

func accept(attempt, skill string) Entry {
	return row(KindReviewAccept, map[string]string{"from": "backlog", "to": "plan", "attempt": attempt, "skill": skill})
}

func transition(from, to string) Entry {
	return row(KindStatusTransition, map[string]string{"from": from, "to": to})
}

// One presentation where 3 of 4 reviewers reject is ONE round, not three.
func TestCountRejectedRoundsCountsPresentationsNotRows(t *testing.T) {
	entries := []Entry{
		reject("att_1", "intent", "a"), reject("att_1", "plan", "b"), reject("att_1", "arch", "c"), accept("att_1", "coverage"),
	}
	got := CountRejectedRounds(entries, "backlog", "plan")
	if got.Rounds != 1 {
		t.Fatalf("one round with 3 rejecting reviewers counted %d, want 1", got.Rounds)
	}
	entries = append(entries, reject("att_2", "plan", "still wrong"))
	got = CountRejectedRounds(entries, "backlog", "plan")
	if got.Rounds != 2 || got.LastSkill != "plan" || got.LastNotes != "still wrong" {
		t.Fatalf("two rounds and the newest objection expected, got %+v", got)
	}
}

// An accepted-only presentation is not a rejected round.
func TestCountRejectedRoundsIgnoresAcceptedRoundsAndOtherEdges(t *testing.T) {
	other := reject("att_9", "x", "other edge")
	other.Payload = json.RawMessage(`{"from":"plan","to":"in_progress","attempt":"att_9","skill":"x","notes":"other edge"}`)
	entries := []Entry{accept("att_1", "intent"), other}
	if got := CountRejectedRounds(entries, "backlog", "plan"); got.Rounds != 0 {
		t.Fatalf("no rejected round on this edge, got %+v", got)
	}
}

// Rows written before attempt ids existed each count as a round of their own.
func TestCountRejectedRoundsLegacyRowsCountIndividually(t *testing.T) {
	entries := []Entry{reject("", "a", "1"), reject("", "b", "2")}
	if got := CountRejectedRounds(entries, "backlog", "plan"); got.Rounds != 2 {
		t.Fatalf("legacy rows: %d rounds, want 2", got.Rounds)
	}
}

// A park, a resume or an accepted advance writes a status_transition, and the
// count restarts after it.
func TestCountRejectedRoundsResetsAfterAnyTransition(t *testing.T) {
	entries := []Entry{
		reject("att_1", "a", "1"), reject("att_2", "a", "2"), reject("att_3", "a", "3"),
		transition("backlog", "blocked"), transition("blocked", "backlog"),
		reject("att_4", "a", "4"),
	}
	got := CountRejectedRounds(entries, "backlog", "plan")
	if got.Rounds != 1 || got.LastNotes != "4" {
		t.Fatalf("count must restart after park/resume, got %+v", got)
	}
}

// A round is one attempt however many reviewers reject in it, and a round with
// any reject is rejected even when another reviewer accepted in it.
func TestEdgeRoundsCountsAttemptsNotRows(t *testing.T) {
	entries := []Entry{
		reject("att_1", "intent", "a"), reject("att_1", "plan", "b"), accept("att_1", "arch"),
		reject("att_2", "intent", "a"),
		accept("att_3", "intent"), accept("att_3", "plan"),
	}
	if a, r := EdgeRounds(entries, "backlog", "plan"); a != 1 || r != 2 {
		t.Fatalf("EdgeRounds = %d accepted / %d rejected, want 1 / 2", a, r)
	}
	if a, r := EdgeRounds(entries, "plan", "in_progress"); a != 0 || r != 0 {
		t.Fatalf("another edge = %d/%d, want 0/0", a, r)
	}
}

// Legacy rows carry no attempt id: each counts as a round of its own.
func TestEdgeRoundsLegacyRowsCountIndividually(t *testing.T) {
	entries := []Entry{reject("", "intent", "a"), reject("", "plan", "b"), accept("", "arch")}
	if a, r := EdgeRounds(entries, "backlog", "plan"); a != 1 || r != 2 {
		t.Fatalf("EdgeRounds = %d/%d, want 1/2", a, r)
	}
}

// Unlike CountRejectedRounds the tally survives status transitions: it is the
// edge's whole history in the slice it is given.
func TestEdgeRoundsDoNotResetAtTransitions(t *testing.T) {
	entries := []Entry{
		reject("att_1", "intent", "a"), transition("backlog", "blocked"), transition("blocked", "backlog"),
		reject("att_2", "intent", "a"), accept("att_3", "intent"),
	}
	if a, r := EdgeRounds(entries, "backlog", "plan"); a != 1 || r != 2 {
		t.Fatalf("EdgeRounds = %d/%d, want 1/2 across the park", a, r)
	}
}

// A performer run at a step counts as a re-dispatch only once an exit of that
// step was rejected; the entry dispatch, a run before any reject, a reviewer's
// row and a run at another step do not.
func TestRedispatchesFollowAnExitReject(t *testing.T) {
	invoke := func(agent, from, to string) Entry {
		return row(KindAgentInvocation, map[string]string{"agent": agent, "from": from, "to": to})
	}
	exitReject := row(KindReviewReject, map[string]string{"from": "in_progress", "to": "integration", "skill": "scope"})
	entries := []Entry{
		invoke("coder", "plan", "in_progress"),        // 0: entry dispatch
		invoke("coder", "in_progress", "in_progress"), // 1: before any reject
		exitReject, // 2
		invoke("reviewer", "in_progress", "in_progress"),
		invoke("coder", "plan", "plan"),
		invoke("coder", "in_progress", "in_progress"), // 5: the re-dispatch
	}
	if got := Redispatches(entries); len(got) != 1 || got[0] != 5 {
		t.Fatalf("Redispatches = %v, want [5]", got)
	}
	if !IsExitReject(exitReject) {
		t.Error("a reject of in_progress→integration is an exit reject")
	}
	if IsExitReject(row(KindReviewReject, map[string]string{"from": "in_progress", "to": "in_progress"})) ||
		IsExitReject(row(KindReviewAccept, map[string]string{"from": "in_progress", "to": "integration"})) {
		t.Error("a same-step reject and an accept are not exit rejects")
	}
}

func TestObjectionLineIsOneLine(t *testing.T) {
	r := RejectedRounds{LastSkill: "satelle-story-plan-review", LastNotes: "AC1\n  contradicts   AC2"}
	if got := r.ObjectionLine(); got != "satelle-story-plan-review: AC1 contradicts AC2" || strings.Contains(got, "\n") {
		t.Fatalf("ObjectionLine = %q", got)
	}
}

func TestDefinitionEditedSinceLatestAcceptedEdge(t *testing.T) {
	edit := func(field string) Entry {
		return row(KindDefinitionEdited, map[string]string{"field": field, "before": "a", "after": "b"})
	}
	entries := []Entry{
		edit("acceptance_criteria"), // before the accepted edge: judged by it
		transition("backlog", "plan"),
		edit("title"),
		edit("body"),
	}
	got := DefinitionEditedSince(entries, "backlog", "plan")
	if len(got) != 2 {
		t.Fatalf("2 edits postdate the accepted edge, got %d", len(got))
	}
	// Re-presented and accepted again: the later transition supersedes.
	entries = append(entries, transition("plan", "backlog"), transition("backlog", "plan"))
	if got := DefinitionEditedSince(entries, "backlog", "plan"); len(got) != 0 {
		t.Fatalf("a re-accepted edge clears the stale edits, got %d", len(got))
	}
	// No accepted edge at all: nothing has gone stale.
	if got := DefinitionEditedSince([]Entry{edit("title")}, "backlog", "plan"); got != nil {
		t.Fatalf("no accepted edge means nothing to compare, got %v", got)
	}
}

func TestNewAttemptIDIsUnique(t *testing.T) {
	a, b := NewAttemptID(), NewAttemptID()
	if a == b || !strings.HasPrefix(a, "att_") {
		t.Fatalf("attempt ids must be unique att_ ids, got %q %q", a, b)
	}
}

// sty_216d5205: the latest presentation's per-gate outcome — an earlier round's
// verdicts are not read, a panel's seats fold to reject when any seat rejects,
// and a performer reject is no gate's verdict.
func TestLatestRoundVerdicts(t *testing.T) {
	if got := LatestRoundVerdicts(nil, "backlog", "plan"); got != nil {
		t.Fatalf("no presentation: %v, want nil", got)
	}
	performer := row(KindPerformerReject, map[string]string{"from": "backlog", "to": "plan", "attempt": "att_2", "skill": "planner"})
	entries := []Entry{
		reject("att_1", "intent", "old"), accept("att_1", "arch"),
		accept("att_2", "intent"), accept("att_2", "panel"), reject("att_2", "panel", "seat two"), accept("att_2", "panel"),
		performer,
	}
	got := LatestRoundVerdicts(entries, "backlog", "plan")
	want := map[string]string{"intent": "accept", "panel": "reject"}
	if len(got) != len(want) || got["intent"] != want["intent"] || got["panel"] != want["panel"] {
		t.Fatalf("LatestRoundVerdicts = %v, want %v (arch is the earlier round's)", got, want)
	}
}
