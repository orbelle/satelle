package verb_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// containerWaitWF is a route whose container lane idles at a step declaring
// waits_on_children (sty_56648ae5), between a performing step and the close.
// Working stories take the ordinary `["*"]` lane.
var containerWaitWF = routeHalves(
	`["*"]
obligations = ["raised", "planned", "coded", "closed"]
park = { state = "blocked" }

[container]
obligations = ["raised", "planned", "ready", "wrapped"]
`,
	`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "executor"
requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["planned"]

[ready]
status = "ready"
agent = "ready-reviewer"
waits_on_children = true
requires = ["planned"]

[wrapped]
status = "in_review"
agent = "executor"
requires = ["ready"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)

func createContainer(t *testing.T) workitem.Item {
	t.Helper()
	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "Container", "category": "container"}), &it)
	return it
}

func moveTo(t *testing.T, id, status string) workitem.Item {
	t.Helper()
	var it workitem.Item
	json.Unmarshal(call(t, "story-set", map[string]any{"id": id, "status": status}), &it)
	if it.Status != status {
		t.Fatalf("%s → %s: got %q", id, status, it.Status)
	}
	return it
}

func leaseRow(t *testing.T, db *store.DB, id string) (lease.Lease, bool) {
	t.Helper()
	l, err := db.Leases.Get(context.Background(), id)
	if err != nil {
		return lease.Lease{}, false
	}
	return l, true
}
