package verb_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_d4886a11: an epic that reached its ready state before the readiness-round
// mark existed carries no accepted round. `story ready` must run that round on
// it, as the backlog→ready edge, so its children can start.

// legacyReadyEpic is an epic with one backlog child, put at "ready" directly in
// the store with no readiness rows — the way the legacy data was produced. The
// gater resolves its verdict from the presented from-status, as the production
// gater resolves reviewers from item.Status: only a backlog presentation is
// accepted, so a round presented on ready→ready is rejected.
func legacyReadyEpic(t *testing.T) (*readyRig, workitem.Item, workitem.Item) {
	t.Helper()
	r, db := newRigWithStore(t, readyDoneToml, readyStepToml("ready", readyReviewers))
	epic := newStoryOf(t, "epic-parent", "epic", "epic:lr")
	child := newStoryOf(t, "feature", "child", "epic:lr")
	r.g.verd = func(it workitem.Item, to string) verb.GateDecision {
		if it.Status != "backlog" {
			return verb.GateDecision{Gated: true, Skill: "gate-a", Notes: "no edge " + it.Status + "→" + to,
				Reviewers: []verb.ReviewerVerdict{{Skill: "gate-a", Notes: "no edge " + it.Status + "→" + to}}}
		}
		return acceptAll()
	}
	// The legacy move: a backlog→ready transition on the chain (the status is its
	// fold) and the cached row, with no readiness round and no mark.
	ctx, now := context.Background(), time.Now()
	if _, err := db.Ledger.Append(ctx, ledger.AppendInput{
		StoryID: epic.ID, Kind: ledger.KindStatusTransition, Actor: "executor",
		Body: "backlog → ready", Payload: json.RawMessage(`{"from":"backlog","to":"ready"}`),
	}, now); err != nil {
		t.Fatalf("append the legacy transition: %v", err)
	}
	ready := "ready"
	if _, err := db.Stories.Update(ctx, epic.ID, workitem.UpdateInput{Status: &ready}, now); err != nil {
		t.Fatalf("put the epic at ready: %v", err)
	}
	if got := statusOf(t, epic.ID); got.Status != "ready" || hasMark(t, epic.ID) {
		t.Fatalf("fixture: want a ready epic with no mark, got %+v", got)
	}
	if rows := ledgerRows(t, epic.ID, ledger.KindReadinessAccept); len(rows) != 0 {
		t.Fatalf("fixture: the legacy epic must have no readiness rows: %+v", rows)
	}
	return r, epic, child
}

// AC1 + AC2: the round runs on the legacy-ready epic, presented and recorded as
// backlog→ready, marks it without moving its status, and the child then starts.
func TestStoryReadyRunsOnALegacyReadyEpic(t *testing.T) {
	r, epic, child := legacyReadyEpic(t)

	res := readyCall(t, epic.ID)[0]
	if res.Outcome != verb.ReadyMarked {
		t.Fatalf("epic: %+v", res)
	}
	if !hasMark(t, epic.ID) {
		t.Error("the accepted round must mark the epic")
	}
	if got := statusOf(t, epic.ID).Status; got != "ready" {
		t.Errorf("epic status = %q, the round must not change it", got)
	}
	if r.d.calls[epic.ID] != 1 || r.g.calls[epic.ID] != 1 {
		t.Errorf("the epic's round runs once: %d dispatches, %d gate runs", r.d.calls[epic.ID], r.g.calls[epic.ID])
	}

	var accepts, readiness int
	for _, e := range allRows(t, epic.ID) {
		var p struct {
			From    string `json:"from"`
			To      string `json:"to"`
			Attempt string `json:"attempt"`
		}
		if len(e.Payload) == 0 || json.Unmarshal(e.Payload, &p) != nil {
			continue
		}
		if p.From == "ready" && p.To == "ready" {
			t.Errorf("%s row recorded on ready→ready: %s", e.Kind, e.Body)
		}
		if p.Attempt != res.Attempt {
			continue
		}
		switch e.Kind {
		case ledger.KindReviewAccept, ledger.KindReadinessAccept:
			if p.From != "backlog" || p.To != "ready" {
				t.Errorf("%s row on %s→%s, want backlog→ready", e.Kind, p.From, p.To)
			}
			if e.Kind == ledger.KindReviewAccept {
				accepts++
			} else {
				readiness++
			}
		}
	}
	if accepts != 2 || readiness != 1 {
		t.Errorf("round %s: %d review accepts, %d readiness accepts; want 2 and 1", res.Attempt, accepts, readiness)
	}

	if err := setStatus(t, child.ID, "plan"); err != nil {
		t.Fatalf("a child of the readied legacy epic must move backlog → plan: %v", err)
	}
	if got := statusOf(t, child.ID).Status; got != "plan" {
		t.Errorf("child status = %q, want plan", got)
	}
}

// AC3: before any round, the child's refusal sends the caller to the command that
// now runs on the epic's current status, not to one that refuses.
func TestLegacyReadyEpicRefusalNamesStoryReady(t *testing.T) {
	_, epic, child := legacyReadyEpic(t)
	err := setStatus(t, child.ID, "plan")
	if err == nil {
		t.Fatal("a child of an unreadied epic must be refused")
	}
	if !strings.Contains(err.Error(), "run satelle story ready "+epic.ID+" first") || strings.Contains(err.Error(), "cannot run") {
		t.Fatalf("want the runnable story ready advice, got %v", err)
	}
}
