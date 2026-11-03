package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// sty_a28c64dd AC3: a story whose chain reached done but whose stored row was left
// behind it (the row a pull folded from part of its chain) is reported by
// reconcile; --repair rebuilds the row from the chain and records a
// status_reconcile event; the next reconcile is clean.
func TestReconcileRepairsAStoredStatusBehindItsChainsDone(t *testing.T) {
	workstateRepo(t, holdOffToml)
	id := createStoryID(t, "shipped, listed behind")
	ctx, now := context.Background(), time.Now().UTC()
	db := openScopeDB(t)
	for i, tr := range [][2]string{
		{"backlog", "plan"}, {"plan", "in_progress"}, {"in_progress", "integration"},
		{"integration", "release"}, {"release", "done"},
	} {
		if _, err := db.Ledger.Append(ctx, ledger.AppendInput{
			StoryID: id, Kind: ledger.KindStatusTransition, Body: tr[0] + " → " + tr[1],
			Payload: mustJSON(transitionPayload(tr[0], tr[1])),
		}, now.Add(time.Duration(i+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	// The row is written behind its chain, as the paged pull left it.
	if _, err := db.Stories.SetStatus(ctx, id, "plan", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	out, err := runRoot(t, "story", "reconcile")
	if err == nil {
		t.Fatalf("reconcile must exit non-zero on the drifted row:\n%s", out)
	}
	for _, want := range []string{id, "status", `row="plan"`, `chain="done"`} {
		if !strings.Contains(out, want) {
			t.Errorf("reconcile output lacks %q:\n%s", want, out)
		}
	}

	if out, err := runRoot(t, "story", "reconcile", "--repair"); err != nil {
		t.Fatalf("reconcile --repair: %v\n%s", err, out)
	}
	if got := localStatus(t, id); got != "done" {
		t.Fatalf("status after repair = %q, want done", got)
	}
	db = openScopeDB(t)
	chain, err := db.Ledger.StoryChain(ctx, id)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	reconciled := false
	for _, e := range chain {
		reconciled = reconciled || e.Kind == ledger.KindStatusReconcile
	}
	if !reconciled {
		t.Errorf("--repair recorded no %s event on %s", ledger.KindStatusReconcile, id)
	}

	if out, err := runRoot(t, "story", "reconcile"); err != nil || !strings.Contains(out, "status: clean") {
		t.Errorf("reconcile after the repair is not clean: %v\n%s", err, out)
	}
}

// sty_a28c64dd AC4 scope: reconcile --repair --story <id> rebuilds only the named
// story's row; another drifted story is left as it is and still reports drift.
func TestReconcileRepairStoryScopeLeavesOtherDriftAlone(t *testing.T) {
	workstateRepo(t, holdOffToml)
	ctx, now := context.Background(), time.Now().UTC()
	named := createStoryID(t, "named, listed behind")
	other := createStoryID(t, "other, listed behind")
	db := openScopeDB(t)
	for _, id := range []string{named, other} {
		for i, tr := range [][2]string{
			{"backlog", "plan"}, {"plan", "in_progress"}, {"in_progress", "integration"},
			{"integration", "release"}, {"release", "done"},
		} {
			if _, err := db.Ledger.Append(ctx, ledger.AppendInput{
				StoryID: id, Kind: ledger.KindStatusTransition, Body: tr[0] + " → " + tr[1],
				Payload: mustJSON(transitionPayload(tr[0], tr[1])),
			}, now.Add(time.Duration(i+1)*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Stories.SetStatus(ctx, id, "plan", now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	out, err := runRoot(t, "story", "reconcile", "--story", named)
	if err == nil || !strings.Contains(out, named) || strings.Contains(out, other) {
		t.Fatalf("scoped dry run must report only %s and exit non-zero: %v\n%s", named, err, out)
	}

	if out, err := runRoot(t, "story", "reconcile", "--repair", "--story", named); err != nil {
		t.Fatalf("reconcile --repair --story: %v\n%s", err, out)
	} else if strings.Contains(out, other) {
		t.Errorf("scoped repair names the story outside its scope:\n%s", out)
	}
	if got := localStatus(t, named); got != "done" {
		t.Errorf("named story after scoped repair = %q, want done", got)
	}
	if got := localStatus(t, other); got != "plan" {
		t.Errorf("story outside the scope after scoped repair = %q, want it left at plan", got)
	}

	out, err = runRoot(t, "story", "reconcile")
	if err == nil || !strings.Contains(out, other) || !strings.Contains(out, `row="plan"`) {
		t.Errorf("the story outside the scope must still report drift: %v\n%s", err, out)
	}
	if strings.Contains(out, named) {
		t.Errorf("the repaired story still reports drift:\n%s", out)
	}
}
