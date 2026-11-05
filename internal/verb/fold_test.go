package verb_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/verb"
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

// sty_d84be8bc AC2: the fold of a story's chain equals the row the existing verbs
// wrote, through create, definition edits, tags, transitions, park and resume.
func TestFoldEqualsTheRowTheVerbsWrote(t *testing.T) {
	db := wire(t)
	ctx := context.Background()
	var it workitem.Item
	if err := json.Unmarshal(call(t, "story-create", map[string]any{
		"title": "folded", "body": "b", "acceptance_criteria": "1. x", "category": "feature", "tags": []string{"surface:cli"},
	}), &it); err != nil {
		t.Fatal(err)
	}
	check := func(step string) {
		t.Helper()
		row, err := db.Stories.Get(ctx, it.ID)
		if err != nil {
			t.Fatal(err)
		}
		st, breaks, err := verb.FoldStory(ctx, db.Ledger, row)
		if err != nil || len(breaks) != 0 {
			t.Fatalf("%s: fold: %v %+v", step, err, breaks)
		}
		for _, f := range storyfold.Fields {
			if !st.Defined[f] {
				t.Errorf("%s: the chain defines no %s", step, f)
			}
		}
		if d := storyfold.Compare(row, st); len(d) != 0 {
			t.Errorf("%s: the fold differs from the row the verbs wrote: %+v", step, d)
		}
	}
	check("create")
	call(t, "story-set", map[string]any{"id": it.ID, "title": "folded again", "body": "b2", "acceptance_criteria": "1. y"})
	check("definition edit")
	call(t, "story-set", map[string]any{"id": it.ID, "add_tags": []string{"order:1"}, "remove_tags": []string{"surface:cli"}})
	check("tag change")
	call(t, "story-estimate", map[string]any{"id": it.ID, "fresh_input": 10, "output": 5})
	check("estimate")
	call(t, "story-set", map[string]any{"id": it.ID, "status": "in_progress"})
	check("transition")
	call(t, "story-set", map[string]any{"id": it.ID, "status": "blocked"})
	check("park")
	if row, _ := db.Stories.Get(ctx, it.ID); row.ParkOrigin != "in_progress" {
		t.Errorf("park origin = %q, want in_progress", row.ParkOrigin)
	}
	call(t, "story-set", map[string]any{"id": it.ID, "status": "in_progress"})
	check("resume")
	if row, _ := db.Stories.Get(ctx, it.ID); row.ParkOrigin != "" || row.Status != "in_progress" {
		t.Errorf("after resume: status %q park origin %q", row.Status, row.ParkOrigin)
	}
}
