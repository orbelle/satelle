package verb_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
)

// sty_9ece9394 AC2: the machine set counts this machine's rows written without a
// location — what a story worktree engagement left before it named the main
// tree's location — and not a row naming another location.
func TestMachineSetCountsRowsWithNoLocation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	for id, loc := range map[string]string{"sty_here": "loc_here_000", "sty_worktree": "", "sty_elsewhere": "loc_other_000"} {
		p, _ := json.Marshal(verb.WorkingOnPayload{Location: loc, State: "in_progress"})
		if _, err := db.Ledger.Append(ctx, ledger.AppendInput{StoryID: id, Kind: ledger.KindWorkingOn, Payload: p}, now); err != nil {
			t.Fatal(err)
		}
	}
	got, err := verb.MachineSet(ctx, db.Ledger, "loc_here_000")
	if err != nil {
		t.Fatal(err)
	}
	if !got["sty_here"] || !got["sty_worktree"] || got["sty_elsewhere"] || len(got) != 2 {
		t.Errorf("MachineSet = %v, want sty_here and sty_worktree only", got)
	}
}
