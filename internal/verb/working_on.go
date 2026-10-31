package verb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// WorkingOnPayload is the payload of a KindWorkingOn ledger row (sty_40853d02):
// which location, session and working tree moved the story into a performing
// state, and which state (sty_9c782f0e). Location and Label are empty for an
// unbound repo. It is advisory presence: displays show it, a stale-base refusal
// names it, and in hosted mode it decides which stories keep their history
// cached (storyhistory.go); nothing reads it to refuse an action.
type WorkingOnPayload struct {
	Location string `json:"location,omitempty"`
	Label    string `json:"label,omitempty"`
	Session  string `json:"session,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	State    string `json:"state"`
}

// locationResolver names this machine's hosted location (id, label), wired from
// the CLI so verb never imports hosted — same seam as assigneeResolver. Nil
// (unwired) or empty means an unbound repo.
var locationResolver func() (id, label string)

// SetLocationResolver wires how the working_on row names this location. Pass nil
// to clear (tests / unbound).
func SetLocationResolver(f func() (id, label string)) {
	locationResolver = f
}

// ClearLocationResolver unsets the resolver (tests).
func ClearLocationResolver() {
	locationResolver = nil
}

// recordWorkingOn appends the working_on row for a move of item into state at
// this location (sty_40853d02, sty_9c782f0e), naming the session and working tree
// the move was made from. It writes one row for every move into a performing
// state — a non-terminal engaging state of the item's route that is not a
// container waiting on open children — whether or not the move took a seat, and
// none for any other state or an unresolved route. Callers write it once the
// move has committed, so a refused or rolled-back move leaves no row.
// Best-effort like every ledger row; only a story carries one, so a task or
// execution never does.
func recordWorkingOn(ctx context.Context, item workitem.Item, state string, now time.Time) {
	if item.Kind != workitem.KindStory || item.ID == "" {
		return
	}
	if engaging, ok := storyStatusIsEngaging(ctx, item, state); !ok || !engaging || waitsOnOpenChildrenAt(ctx, item, state) {
		return
	}
	p := WorkingOnPayload{Session: config.ResolveSession(), Worktree: engagementWorktree(), State: state}
	if locationResolver != nil {
		p.Location, p.Label = locationResolver()
	}
	payload, _ := json.Marshal(p)
	appendLedgerEntry(ctx, item.ID, ledger.KindWorkingOn, "executor", WorkingOnText(p), payload, now)
}

// WorkingOnText is the one human line for a working_on row, shared by the row's
// own body and the displays that cite it.
func WorkingOnText(p WorkingOnPayload) string {
	where := p.Location
	if where == "" {
		where = "local"
	}
	if p.Label != "" {
		where += " (" + p.Label + ")"
	}
	s := "working on at " + where
	if p.Session != "" {
		s += ", session " + p.Session
	}
	if p.Worktree != "" {
		s += ", tree " + p.Worktree
	}
	return fmt.Sprintf("%s [%s]", s, p.State)
}
