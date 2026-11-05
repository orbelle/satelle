package verb

// sty_36d6f155: the seat a story-set takes is stamped with the id the driving
// shell resolves, never one a dispatched performer's hooks left in the publish
// directory, and a same-status re-set re-stamps a seat nobody live speaks for.

import (
	"context"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/store"
)

func clearSessionEnv(t *testing.T) {
	t.Helper()
	t.Setenv(config.SessionEnv, "")
	t.Setenv("CURSOR_CONVERSATION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
}

func seatStamp(t *testing.T, db *store.DB, id string) string {
	t.Helper()
	l, err := db.Leases.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("lease %s: %v", id, err)
	}
	return l.SessionID
}
