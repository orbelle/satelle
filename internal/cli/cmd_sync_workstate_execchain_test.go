package cli

import (
	"encoding/json"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// sty_937fd6cc: an execution's chain is published by the same compare-and-append
// as a task's, so a pull must read it as it reads a task's, and rechain must
// repair one whose hosted chain carries no status (exe_317cdf90).

// cancelEvent is the status_transition a local backlog → cancelled writes.
func cancelEvent(id string) ledger.Entry {
	return ledger.Entry{
		ID: ledger.NewID(), StoryID: id, Kind: ledger.KindStatusTransition, Actor: "bob",
		Body: "backlog → cancelled", Payload: json.RawMessage(`{"from":"backlog","to":"cancelled"}`),
	}
}
