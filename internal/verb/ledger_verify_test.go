package verb_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/verb"
)

// sty_41f61669 AC6: ledger-verify is a registered, read-only verb that reports
// every chain break of one story or of all of them.
func TestLedgerVerifyVerbReportsBreaksAndModifiesNothing(t *testing.T) {
	db := wire(t)
	ctx := context.Background()

	var a []ledger.Entry
	for i := 0; i < 3; i++ {
		e, err := db.Ledger.Append(ctx, ledger.AppendInput{StoryID: "sty_a", Kind: ledger.KindComment}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		a = append(a, e)
	}
	if _, err := db.Ledger.Append(ctx, ledger.AppendInput{StoryID: "sty_b", Kind: ledger.KindComment}, time.Now()); err != nil {
		t.Fatal(err)
	}

	verify := func(req map[string]any) ledger.ChainReport {
		t.Helper()
		var rep ledger.ChainReport
		if err := json.Unmarshal(call(t, "ledger-verify", req), &rep); err != nil {
			t.Fatal(err)
		}
		return rep
	}

	if rep := verify(nil); len(rep.Breaks) != 0 || rep.Stories != 2 || rep.Events != 4 {
		t.Fatalf("a sound ledger: %+v", rep)
	}

	if _, err := db.SQL().Exec(`UPDATE evidence SET base_id = 'evt_ghost' WHERE id = ?`, a[1].ID); err != nil {
		t.Fatal(err)
	}
	before, err := db.Ledger.ListByStory(ctx, "sty_a", "")
	if err != nil {
		t.Fatal(err)
	}

	rep := verify(map[string]any{"story_id": "sty_a"})
	if len(rep.Breaks) != 1 || rep.Breaks[0].Kind != ledger.BreakMissingBase || rep.Breaks[0].EventID != a[1].ID {
		t.Fatalf("one story: breaks = %+v, want one missing_base on %s", rep.Breaks, a[1].ID)
	}
	if rep := verify(map[string]any{"story_id": "sty_b"}); len(rep.Breaks) != 0 {
		t.Errorf("the other story is sound: %+v", rep)
	}
	if rep := verify(nil); len(rep.Breaks) != 1 {
		t.Errorf("all stories: breaks = %+v, want the one", rep.Breaks)
	}

	after, err := db.Ledger.ListByStory(ctx, "sty_a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("verify changed the row count: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i].BaseID != after[i].BaseID || before[i].ID != after[i].ID {
			t.Errorf("verify repaired entry %s: base %q -> %q", before[i].ID, before[i].BaseID, after[i].BaseID)
		}
	}
}

// sty_6f952dd0 AC2: a status transition whose from is not the status its chain had
// reached at its place — an earlier attempt's event linked after the story's done —
// is reported by ledger-verify as a stale_from replay break, and the fold stays done.
func TestLedgerVerifyReportsAStaleTransitionAsABreak(t *testing.T) {
	db := wire(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var stale ledger.Entry
	for i, tr := range [][2]string{{"backlog", "in_progress"}, {"in_progress", "done"}, {"plan", "in_progress"}} {
		payload, _ := json.Marshal(map[string]string{"from": tr[0], "to": tr[1]})
		e, err := db.Ledger.Append(ctx, ledger.AppendInput{
			StoryID: "sty_late", Kind: ledger.KindStatusTransition, Body: tr[0] + " → " + tr[1], Payload: payload,
		}, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		stale = e
	}

	var rep verb.LedgerVerifyReport
	if err := json.Unmarshal(call(t, "ledger-verify", map[string]any{"story_id": "sty_late"}), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.ReplayBreaks) != 1 || rep.ReplayBreaks[0].Reason != storyfold.ReplayStaleFrom ||
		rep.ReplayBreaks[0].EventID != stale.ID || rep.ReplayBreaks[0].From != "plan" {
		t.Fatalf("replay_breaks = %+v, want one stale_from on %s", rep.ReplayBreaks, stale.ID)
	}
	if len(rep.Breaks) != 0 {
		t.Errorf("the chain's links are sound, got breaks %+v", rep.Breaks)
	}
	chain, _ := db.Ledger.StoryChain(ctx, "sty_late")
	ends := func(s string) bool { return s == "done" }
	if st, _ := storyfold.Fold(chain, storyfold.Route{Final: ends}); st.Status != "done" {
		t.Errorf("fold = %q, want done", st.Status)
	}
}

// sty_5c9b295d AC1: a transition out of order at a place where the story had not
// ended (the sty_df21b2db shape) is still reported by ledger-verify as a stale_from
// break; it applies, so it is not marked skipped.
func TestLedgerVerifyStillReportsAnOutOfOrderTransitionTheFoldApplies(t *testing.T) {
	db := wire(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var outOfOrder ledger.Entry
	for i, tr := range [][2]string{
		{"backlog", "in_progress"}, {"in_progress", "blocked"}, {"in_progress", "integration"},
		{"blocked", "in_progress"}, {"integration", "done"},
	} {
		payload, _ := json.Marshal(map[string]string{"from": tr[0], "to": tr[1]})
		e, err := db.Ledger.Append(ctx, ledger.AppendInput{
			StoryID: "sty_legacy", Kind: ledger.KindStatusTransition, Body: tr[0] + " → " + tr[1], Payload: payload,
		}, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			outOfOrder = e
		}
	}

	var rep verb.LedgerVerifyReport
	if err := json.Unmarshal(call(t, "ledger-verify", map[string]any{"story_id": "sty_legacy"}), &rep); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, b := range rep.ReplayBreaks {
		if b.Reason == storyfold.ReplayStaleFrom && b.EventID == outOfOrder.ID {
			found = true
			if b.Skipped {
				t.Errorf("break = %+v, want it applied out of order: blocked is not final", b)
			}
		}
	}
	if !found {
		t.Fatalf("replay_breaks = %+v, want a stale_from on %s", rep.ReplayBreaks, outOfOrder.ID)
	}
	chain, _ := db.Ledger.StoryChain(ctx, "sty_legacy")
	ends := func(s string) bool { return s == "done" }
	if st, _ := storyfold.Fold(chain, storyfold.Route{Final: ends}); st.Status != "done" {
		t.Errorf("fold = %q, want done", st.Status)
	}
}
