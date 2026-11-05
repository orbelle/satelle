package verb_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// TestAttachRootResolvesByKind proves the attachment root resolves by KIND
// (sty_890b86cb): attaching to a TASK materialises under .satelle/tasks/<tsk_id>/,
// never under .satelle/stories/, while a story attaches under stories/ as before.
func TestAttachRootResolvesByKind(t *testing.T) {
	withWiring(t)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "satelle.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	storyDir := filepath.Join(dir, ".satelle", "stories")
	taskDir := filepath.Join(dir, ".satelle", "tasks")
	verb.SetWorkItemStore(db.Stories)
	verb.SetLedgerStore(db.Ledger)
	verb.SetTxRunner(db.InTx)
	verb.SetStoryDir(storyDir)
	verb.SetTaskDir(taskDir)
	t.Cleanup(func() {
		db.Close()
	})

	var task workitem.Item
	json.Unmarshal(call(t, "task-create", map[string]any{"title": "T"}), &task)
	var story workitem.Item
	json.Unmarshal(call(t, "story-create", map[string]any{"title": "S"}), &story)

	// Attach to the task → lands under .satelle/tasks/<tsk_id>/, NOT stories/.
	call(t, "story-doc-attach", map[string]any{"story_id": task.ID, "name": "plan", "type": "plan", "body": "task plan"})
	if _, err := os.Stat(filepath.Join(taskDir, task.ID, "plan.md")); err != nil {
		t.Errorf("task attachment did not land under .satelle/tasks/<tsk_id>/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storyDir, task.ID)); !os.IsNotExist(err) {
		t.Error("task attachment wrongly materialised under .satelle/stories/")
	}

	// Attach to the story → lands under .satelle/stories/<sty_id>/ as before.
	call(t, "story-doc-attach", map[string]any{"story_id": story.ID, "name": "plan", "type": "plan", "body": "story plan"})
	if _, err := os.Stat(filepath.Join(storyDir, story.ID, "plan.md")); err != nil {
		t.Errorf("story attachment did not land under .satelle/stories/<sty_id>/: %v", err)
	}

	// story-doc-list is kind-aware too: it lists the task's attachment.
	var docs []struct{ Name, Type string }
	json.Unmarshal(call(t, "story-doc-list", map[string]any{"story_id": task.ID}), &docs)
	if len(docs) != 1 || docs[0].Name != "plan" {
		t.Errorf("kind-aware doc-list did not return the task attachment: %+v", docs)
	}
}
