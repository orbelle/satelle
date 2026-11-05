package verb_test

import (
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/workitem"
)

func getItem(t *testing.T, id string) workitem.Item {
	t.Helper()
	var it workitem.Item
	if err := json.Unmarshal(call(t, "story-get", map[string]any{"id": id}), &it); err != nil {
		t.Fatal(err)
	}
	return it
}
