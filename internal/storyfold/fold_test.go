package storyfold

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// chain builds a linked chain of events: each names the one before as its base.
func chain(events ...ledger.Entry) []ledger.Entry {
	for i := range events {
		events[i].ID = fmt.Sprintf("evt_%02d", i)
		events[i].StoryID = "sty_a"
		if i > 0 {
			events[i].BaseID = events[i-1].ID
		}
	}
	return events
}

func ev(kind string, payload any) ledger.Entry {
	b, _ := json.Marshal(payload)
	return ledger.Entry{Kind: kind, Payload: b}
}

func transition(from, to string) ledger.Entry {
	return ev(ledger.KindStatusTransition, map[string]string{"from": from, "to": to})
}

func parks(status string) bool { return status == "blocked" || status == "cancelled" }

// ends is the route's final states: done, and the park that does not resume.
func ends(status string) bool { return status == "done" || status == "cancelled" }

var testRoute = Route{Park: parks, Final: ends}

func created() ledger.Entry {
	return ledger.Entry{Kind: ledger.KindStoryCreated, Payload: CreatedPayloadOf(workitem.Item{
		Kind: workitem.KindStory, Title: "T", Body: "B", AcceptanceCriteria: "1. a", Category: "feature",
		Tags: []string{"x", "workflow:default"}, Status: "backlog",
	}, "")}
}

// sty_d84be8bc AC2: the fold returns status, title, body, acceptance criteria,
// category, tags and park origin from the events that set them.
func TestFoldTable(t *testing.T) {
	cases := []struct {
		name   string
		events []ledger.Entry
		want   State
	}{
		{"create", chain(created()), State{
			Status: "backlog", Title: "T", Body: "B", AcceptanceCriteria: "1. a", Category: "feature", Tags: []string{"x", "workflow:default"}}},
		{"definition edit", chain(created(),
			ev(ledger.KindDefinitionEdited, map[string]string{"field": "title", "old": "T", "new": "T2"}),
			ev(ledger.KindDefinitionEdited, map[string]string{"field": "acceptance_criteria", "old": "1. a", "new": "1. b"})),
			State{Status: "backlog", Title: "T2", Body: "B", AcceptanceCriteria: "1. b", Category: "feature", Tags: []string{"x", "workflow:default"}}},
		{"amend", chain(created(),
			ev(ledger.KindDefinitionAmended, map[string]any{"reason": "r", "fields": []FieldChange{
				{Field: "body", Old: "B", New: "B2"}, {Field: "category", Old: "feature", New: "fix"}}})),
			State{Status: "backlog", Title: "T", Body: "B2", AcceptanceCriteria: "1. a", Category: "fix", Tags: []string{"x", "workflow:default"}}},
		{"story set field edit", chain(created(),
			ev(ledger.KindStoryUpdated, map[string]any{"fields": []FieldChange{{Field: "body", Old: "B", New: "B3"}}})),
			State{Status: "backlog", Title: "T", Body: "B3", AcceptanceCriteria: "1. a", Category: "feature", Tags: []string{"x", "workflow:default"}}},
		{"tag change", chain(created(),
			ev(ledger.KindStoryUpdated, map[string]any{"tags": TagsChange{Before: []string{"x", "workflow:default"}, After: []string{"workflow:default", "ready"}}})),
			State{Status: "backlog", Title: "T", Body: "B", AcceptanceCriteria: "1. a", Category: "feature", Tags: []string{"workflow:default", "ready"}}},
		{"transition", chain(created(), transition("backlog", "plan"), transition("plan", "in_progress")),
			State{Status: "in_progress", Title: "T", Body: "B", AcceptanceCriteria: "1. a", Category: "feature", Tags: []string{"x", "workflow:default"}}},
		{"park", chain(created(), transition("backlog", "plan"), transition("plan", "blocked")),
			State{Status: "blocked", ParkOrigin: "plan", Title: "T", Body: "B", AcceptanceCriteria: "1. a", Category: "feature", Tags: []string{"x", "workflow:default"}}},
		{"park then resume", chain(created(), transition("backlog", "plan"), transition("plan", "blocked"), transition("blocked", "plan")),
			State{Status: "plan", ParkOrigin: "", Title: "T", Body: "B", AcceptanceCriteria: "1. a", Category: "feature", Tags: []string{"x", "workflow:default"}}},
		{"a chain merge record is no change", chain(created(), transition("backlog", "plan"),
			ev(ledger.KindChainMerge, map[string]any{"fork_base": "evt_00", "merged": []string{"evt_01"},
				"excluded": []map[string]string{{"id": "evt_x", "from": "backlog", "to": "done"}}})),
			State{Status: "plan", Title: "T", Body: "B", AcceptanceCriteria: "1. a", Category: "feature", Tags: []string{"x", "workflow:default"}}},
		{"repair rows are not changes", chain(created(), transition("backlog", "plan"),
			ev(ledger.KindStatusReconcile, map[string]any{"fields": []map[string]string{{"field": "status", "row": "x", "chain": "plan"}}})),
			State{Status: "plan", Title: "T", Body: "B", AcceptanceCriteria: "1. a", Category: "feature", Tags: []string{"x", "workflow:default"}}},
		{"snapshot sets the whole state", chain(
			ev(ledger.KindStoryCreated, map[string]string{"source": "legacy"}),
			transition("backlog", "plan"),
			ledger.Entry{Kind: ledger.KindDefinitionSnapshot, Payload: SnapshotPayloadOf(workitem.Item{
				Title: "S", Body: "SB", Category: "fix", Tags: []string{"t"}, Status: "blocked", ParkOrigin: "plan"}, true)}),
			State{Status: "blocked", ParkOrigin: "plan", Title: "S", Body: "SB", Category: "fix", Tags: []string{"t"}}},
		{"a snapshot overrides older transitions and later ones follow it", chain(
			ev(ledger.KindStoryCreated, map[string]string{"source": "legacy"}),
			transition("plan", "done"),
			ledger.Entry{Kind: ledger.KindDefinitionSnapshot, Payload: SnapshotPayloadOf(workitem.Item{
				Title: "S", Status: "in_progress"}, true)},
			transition("in_progress", "blocked")),
			State{Status: "blocked", ParkOrigin: "in_progress", Title: "S", Tags: []string{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, breaks := Fold(c.events, testRoute)
			if len(breaks) != 0 {
				t.Fatalf("breaks = %+v", breaks)
			}
			if got.Status != c.want.Status || got.Title != c.want.Title || got.Body != c.want.Body ||
				got.AcceptanceCriteria != c.want.AcceptanceCriteria || got.Category != c.want.Category ||
				got.ParkOrigin != c.want.ParkOrigin || !slices.Equal(got.Tags, c.want.Tags) {
				t.Errorf("fold = %+v\nwant   %+v", got, c.want)
			}
			if got.Head != c.events[len(c.events)-1].ID {
				t.Errorf("head = %q, want the last event %q", got.Head, c.events[len(c.events)-1].ID)
			}
		})
	}
}

// A field no event set is not empty, it is unknown: Compare leaves it alone and
// Overlay keeps the row's value.
func TestFoldLeavesUndefinedFieldsToTheRow(t *testing.T) {
	st, _ := Fold(chain(transition("backlog", "plan")), Route{})
	if len(st.Defined) != 1 || !st.Defined[FieldStatus] {
		t.Fatalf("defined = %v, want only status", st.Defined)
	}
	row := workitem.Item{ID: "sty_a", Title: "kept", Body: "kept body", Status: "backlog", Tags: []string{"a"}}
	if d := Compare(row, st); len(d) != 1 || d[0].Field != FieldStatus || d[0].Chain != "plan" || d[0].Row != "backlog" {
		t.Errorf("compare = %+v, want the status alone", d)
	}
	got := Overlay(row, st)
	if got.Status != "plan" || got.Title != "kept" || got.Body != "kept body" || !slices.Equal(got.Tags, []string{"a"}) {
		t.Errorf("overlay = %+v", got)
	}
}

// The fold follows the chain's base links, so events listed out of insertion
// order (a pull that delivered them late) still fold in the order they were made.
func TestFoldFollowsTheChainNotTheListing(t *testing.T) {
	in := chain(created(), transition("backlog", "plan"), transition("plan", "in_progress"))
	shuffled := []ledger.Entry{in[2], in[0], in[1]}
	got, _ := Fold(shuffled, Route{})
	if got.Status != "in_progress" {
		t.Errorf("status = %q, want in_progress from the last link of the chain", got.Status)
	}
}

// A fork is reported, never merged by guessing: the fold falls back to the given
// order and returns the ledger's own break. The forked transition's from is not
// the status the listing had reached: it is reported as stale, and as the story
// had not ended it applies out of order.
func TestFoldReportsAForkAndFallsBackToListingOrder(t *testing.T) {
	in := chain(created(), transition("backlog", "plan"))
	fork := transition("backlog", "cancelled")
	fork.ID, fork.StoryID, fork.BaseID = "evt_fork", "sty_a", in[0].ID
	in = append(in, fork)
	got, breaks := Fold(in, testRoute)
	if len(breaks) != 1 || breaks[0].Kind != ledger.BreakFork {
		t.Fatalf("breaks = %+v, want one fork", breaks)
	}
	if len(got.StaleTransitions) != 1 || got.StaleTransitions[0].EventID != "evt_fork" || got.StaleTransitions[0].Skipped {
		t.Errorf("stale = %+v, want the forked event, applied", got.StaleTransitions)
	}
	if got.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled: plan is not final, so the forked event applies", got.Status)
	}
}

// sty_6f952dd0 AC1/AC2: an earlier attempt's events linked after the story's done
// carry a plan → in_progress whose from is not the status the chain had reached.
// It never becomes the latest status; it is recorded as skipped, and Head still
// advances (the sty_41f61669 shape).
func TestFoldSkipsAStaleTransitionAfterDone(t *testing.T) {
	in := chain(created(), transition("backlog", "plan"), transition("plan", "in_progress"), transition("in_progress", "done"),
		transition("plan", "in_progress"), transition("in_progress", "integration"))
	got, _ := Fold(in, testRoute)
	if got.Status != "done" {
		t.Fatalf("status = %q, want done", got.Status)
	}
	if got.StatusEvent != in[3].ID {
		t.Errorf("status event = %q, want the done transition %q", got.StatusEvent, in[3].ID)
	}
	if got.Head != in[5].ID {
		t.Errorf("head = %q, want the last event %q", got.Head, in[5].ID)
	}
	if len(got.StaleTransitions) != 2 || got.StaleTransitions[0].EventID != in[4].ID || got.StaleTransitions[1].EventID != in[5].ID {
		t.Fatalf("stale = %+v, want both late events", got.StaleTransitions)
	}
	if r := got.StaleTransitions[0]; r.Reason != ReplayStaleFrom || r.From != "plan" || r.To != "in_progress" || !r.Skipped {
		t.Errorf("stale[0] = %+v", r)
	}
}

// sty_5c9b295d AC1: a story cancelled at the place of a stale transition is skipped
// too — cancelled is final on the route, as done is.
func TestFoldSkipsAStaleTransitionAfterCancelled(t *testing.T) {
	in := chain(created(), transition("backlog", "plan"), transition("plan", "cancelled"), transition("plan", "in_progress"))
	got, _ := Fold(in, testRoute)
	if got.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
	if len(got.StaleTransitions) != 1 || !got.StaleTransitions[0].Skipped {
		t.Errorf("stale = %+v, want the late event skipped", got.StaleTransitions)
	}
}

// sty_5c9b295d AC1: a legacy chain linked once in insertion order (the
// sty_df21b2db shape) holds in_progress → integration before the blocked →
// in_progress that precedes it in time. The stale from is at a place where the
// story was parked, which is not final, so the transitions apply and the story
// folds to done; the breaks are still reported, none of them skipped.
func TestFoldAppliesOutOfOrderTransitionsOfALegacyChain(t *testing.T) {
	in := chain(created(), transition("backlog", "plan"), transition("plan", "in_progress"), transition("in_progress", "blocked"),
		transition("in_progress", "integration"), transition("blocked", "in_progress"),
		transition("integration", "release"), transition("release", "done"))
	got, _ := Fold(in, testRoute)
	if got.Status != "done" {
		t.Fatalf("status = %q, want done", got.Status)
	}
	if got.StatusEvent != in[7].ID {
		t.Errorf("status event = %q, want the done transition %q", got.StatusEvent, in[7].ID)
	}
	if len(got.StaleTransitions) == 0 {
		t.Fatal("the out-of-order transitions must still be reported")
	}
	for _, r := range got.StaleTransitions {
		if r.Reason != ReplayStaleFrom || r.Skipped {
			t.Errorf("stale = %+v, want a stale_from that applied", r)
		}
	}
	// With no route resolved nothing is known to be final, so nothing is skipped.
	if bare, _ := Fold(in, Route{}); bare.Status != got.Status {
		t.Errorf("no-route status = %q, want %q", bare.Status, got.Status)
	}
}
