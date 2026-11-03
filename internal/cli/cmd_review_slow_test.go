//go:build clislow

package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

// The store lists at most 2000 rows oldest first, so on a long story the rows that
// say what changed since the review are the ones a single list would drop: a
// plan re-attached after 2000 later rows must still leave the case unfaithful.
func TestSlowReviewCaptureSeesAReattachPastTheFirstLedgerPage(t *testing.T) {
	tempRepo(t)
	out, err := runRoot(t, "story", "create", "--title", "Long story", "--body", "Body", "--status", "backlog")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("parse create: %v\n%s", err, out)
	}
	attach := func(body string) {
		t.Helper()
		if out, err := runRoot(t, "story", "attach", created.ID, "--name", "plan", "--type", "plan", "--body", body); err != nil {
			t.Fatalf("attach: %v\n%s", err, out)
		}
	}
	attach("# the plan as reviewed")
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	add := func(kind string, payload map[string]any) ledger.Entry {
		b, _ := json.Marshal(payload)
		e, err := db.Ledger.Append(context.Background(), ledger.AppendInput{StoryID: created.ID, Kind: kind, Actor: "reviewer", Body: kind, Payload: b}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	rej := add(ledger.KindReviewReject, map[string]any{
		"from": "backlog", "to": "plan", "skill": "satelle-story-plan-review", "notes": "the plan is thin", "accept": false,
	})
	for i := 0; i < ledgerPage+100; i++ {
		add(ledger.KindComment, map[string]any{"n": i})
	}
	db.Close()
	attach("# the plan, rewritten after the review")

	replay := filepath.Join(t.TempDir(), "replay")
	out, err = runRoot(t, "review", "capture", created.ID, "--ledger", rej.ID, "--expect", "reject", "--no-patch", "--out", replay)
	if err != nil {
		t.Fatalf("capture: %v\n%s", err, out)
	}
	if !strings.Contains(out, "UNFAITHFUL") || !strings.Contains(out, `"plan" was re-attached after the review`) {
		t.Fatalf("a re-attach past the first ledger page must be seen:\n%s", out)
	}
}
