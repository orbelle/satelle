package verb

// sty_58a7bdfe AC1: satelle manages no context budget, so engaging a story gives
// no fresh-session advice and records no session_advisory row — not even when
// the driving session already drove another story, the case the removed
// advisory used to fire on.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

type engageHarness struct {
	t     *testing.T
	db    *store.DB
	lines []string
}

func (h *engageHarness) dispatch(name string, req map[string]any) workitem.Item {
	h.t.Helper()
	b, _ := json.Marshal(req)
	resp, err := Dispatch(context.Background(), name, b)
	if err != nil {
		h.t.Fatalf("%s %v: %v", name, req, err)
	}
	var it workitem.Item
	if err := json.Unmarshal(resp, &it); err != nil {
		h.t.Fatal(err)
	}
	return it
}

func (h *engageHarness) create(title string) workitem.Item {
	return h.dispatch("story-create", map[string]any{"title": title, "status": "backlog",
		"tags": []string{"estimate-minutes:10", "estimate-tokens:1000"}})
}

func (h *engageHarness) set(id, status string) workitem.Item {
	it := h.dispatch("story-set", map[string]any{"id": id, "status": status})
	if it.Status != status {
		h.t.Fatalf("story-set %s → %s: status = %q", id, status, it.Status)
	}
	return it
}
