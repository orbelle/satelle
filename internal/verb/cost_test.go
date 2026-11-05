package verb_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func hasActualUSD(tags []string) bool {
	for _, t := range tags {
		if t == "actual-usd" || strings.HasPrefix(t, "actual-usd:") {
			return true
		}
	}
	return false
}

func actualCostBands(tags []string) []string {
	var out []string
	for _, t := range tags {
		if strings.HasPrefix(t, "actual-cost:") {
			out = append(out, t)
		}
	}
	return out
}

func appendInvocationRow(t *testing.T, db *store.DB, storyID string, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{
		StoryID: storyID, Kind: ledger.KindAgentInvocation, Actor: "coder", Payload: raw,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestStoryEstimateRequiresAValue(t *testing.T) {
	wire(t)
	var it workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "x"}), &it)
	if _, err := dispatchRaw(t, "story-estimate", map[string]any{"id": it.ID}); err == nil {
		t.Error("estimate with neither tokens nor time should error")
	}
}
