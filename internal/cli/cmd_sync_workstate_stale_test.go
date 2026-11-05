package cli

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func transitionPayload(from, to string) map[string]any {
	return map[string]any{"from": from, "to": to}
}

func localStatus(t *testing.T, id string) string {
	t.Helper()
	db := openScopeDB(t)
	defer func() { _ = db.Close() }()
	row, err := db.Stories.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row.Status
}

// sty_6f952dd0 AC1: an earlier attempt's events reach this database after the story
// is done, without a base, and are linked to its tail. The one that carries a
// plan → in_progress does not become the story's status: it folds to done, the row
// stays done, and the chain reports the late events as stale.
func TestPullOfALateEarlierAttemptDoesNotReopenADoneStory(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, holdOffToml)
	at := time.Now().Add(time.Hour).UTC()
	const id = "sty_late01"
	seedHostedFull(f, id, "Shipped", "B", "1. x", "done", []string{"x"}, at)
	seedHostedLedger(f, "led_c", id, ledger.KindStoryCreated, map[string]any{"source": "legacy"}, at)
	seedHostedLedger(f, "led_t1", id, ledger.KindStatusTransition, transitionPayload("backlog", "in_progress"), at.Add(time.Second))
	seedHostedLedger(f, "led_t2", id, ledger.KindStatusTransition, transitionPayload("in_progress", "done"), at.Add(2*time.Second))
	if out, err := runRoot(t, "sync", "workstate", "pull", "--server", ts.URL); err != nil {
		t.Fatalf("first pull: %v\n%s", err, out)
	}
	if got := localStatus(t, id); got != "done" {
		t.Fatalf("status after the first pull = %q, want done", got)
	}

	// The earlier, killed attempt: made before the done, arriving after it.
	seedHostedLedger(f, "led_e1", id, ledger.KindStatusTransition, transitionPayload("backlog", "plan"), at.Add(-time.Hour))
	seedHostedLedger(f, "led_e2", id, ledger.KindStatusTransition, transitionPayload("plan", "in_progress"), at.Add(-30*time.Minute))
	out, err := runRoot(t, "sync", "workstate", "pull", "--server", ts.URL)
	if err != nil {
		t.Fatalf("second pull: %v\n%s", err, out)
	}
	if strings.Contains(out, "the pull moved it") {
		t.Errorf("a done story that stayed done was reported as moved:\n%s", out)
	}

	db := openScopeDB(t)
	defer func() { _ = db.Close() }()
	// The fold chooses its stale-transition rule from the story's route, which the
	// verb layer reads from the doc index.
	withVerbWiring(t)
	verb.SetDocIndexStore(db.DocIndex)
	row, err := db.Stories.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := verb.FoldStory(context.Background(), db.Ledger, row)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "done" || row.Status != "done" {
		t.Errorf("fold = %q, row = %q; want both done", st.Status, row.Status)
	}
	var late []string
	for _, b := range st.StaleTransitions {
		late = append(late, b.EventID)
	}
	slices.Sort(late)
	if !slices.Equal(late, []string{"led_e1", "led_e2"}) {
		t.Errorf("stale transitions = %v, want the earlier attempt's two events", late)
	}
	if d, _ := verb.StoryDrifts(context.Background(), db.Ledger, row); len(d) != 0 {
		t.Errorf("drift after the pull: %+v", d)
	}
}

// sty_6f952dd0 AC3: a pull that rebuilds a done or cancelled story's row to a
// non-terminal status names the story, both statuses and the event that caused it,
// on a plain pull and on --force alike (UpsertForce replaces the status before the
// rebuild reads the row, so the prior status is read ahead of both).
func TestPullNamesATerminalStoryItMovesToANonTerminalStatus(t *testing.T) {
	for _, tc := range []struct {
		prior string
		force bool
	}{{"done", false}, {"done", true}, {"cancelled", false}, {"cancelled", true}} {
		name := tc.prior + "/plain"
		if tc.force {
			name = tc.prior + "/force"
		}
		t.Run(name, func(t *testing.T) {
			ts, f := newFakeWorkstateServer(t)
			seedCred(t, ts.URL)
			workstateRepo(t, holdOffToml)
			at := time.Now().Add(time.Hour).UTC()
			const id = "sty_move01"
			seedHostedFull(f, id, "Reopened", "B", "1. x", "in_progress", []string{"x"}, at)
			// A chain that defines the story itself, so no seed decides its status: the
			// pull appends none (sty_199ad82b).
			created := storyfold.CreatedPayloadOf(workitem.Item{Title: "Reopened", Body: "B", AcceptanceCriteria: "1. x",
				Category: "feature", Tags: []string{"x"}, Status: "backlog"}, "cli")
			seedHostedLedger(f, "led_c", id, ledger.KindStoryCreated, created, at)
			seedHostedLedger(f, "led_t1", id, ledger.KindStatusTransition, transitionPayload("backlog", "in_progress"), at.Add(time.Second))
			if out, err := runRoot(t, "sync", "workstate", "pull", "--server", ts.URL); err != nil {
				t.Fatalf("first pull: %v\n%s", err, out)
			}
			// Close the story locally, as an earlier session left it.
			corruptRow(t, id, "status", tc.prior)

			args := []string{"sync", "workstate", "pull", "--server", ts.URL}
			if tc.force {
				args = append(args, "--force")
			}
			out, err := runRoot(t, args...)
			if err != nil {
				t.Fatalf("pull: %v\n%s", err, out)
			}
			for _, want := range []string{id, "was " + tc.prior, "moved it to in_progress", "event led_t1"} {
				if !strings.Contains(out, want) {
					t.Errorf("output does not name %q:\n%s", want, out)
				}
			}
			if got := localStatus(t, id); got != "in_progress" {
				t.Errorf("status = %q, want the chain's in_progress", got)
			}
		})
	}
}
