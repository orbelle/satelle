package verb_test

import (
	"encoding/json"
	"testing"

	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func TestCreateStampsAssigneeFromResolver(t *testing.T) {
	withWiring(t)
	wire(t)
	verb.SetAssigneeResolver(func() string { return "P" })

	var story workitem.Item
	if err := json.Unmarshal(call(t, "story-create", map[string]any{"title": "S"}), &story); err != nil {
		t.Fatal(err)
	}
	if story.Assignee != "P" {
		t.Fatalf("story assignee = %q, want P", story.Assignee)
	}
	var task workitem.Item
	if err := json.Unmarshal(call(t, "task-create", map[string]any{"title": "T"}), &task); err != nil {
		t.Fatal(err)
	}
	if task.Assignee != "P" {
		t.Fatalf("task assignee = %q, want P", task.Assignee)
	}
}

func TestNoCredentialCreateAndEngageLeaveEmpty(t *testing.T) {
	withWiring(t)
	wire(t)
	verb.ClearAssigneeResolver()

	var story workitem.Item
	if err := json.Unmarshal(call(t, "story-create", map[string]any{"title": "Local"}), &story); err != nil {
		t.Fatal(err)
	}
	if story.Assignee != "" {
		t.Fatalf("create assignee = %q", story.Assignee)
	}
	var got workitem.Item
	if err := json.Unmarshal(call(t, "story-set", map[string]any{"id": story.ID, "status": "in_progress"}), &got); err != nil {
		t.Fatal(err)
	}
	if got.Assignee != "" {
		t.Fatalf("engaged assignee = %q, want empty", got.Assignee)
	}
}
