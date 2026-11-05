package verb_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func createFolded(t *testing.T, req map[string]any) workitem.Item {
	t.Helper()
	var it workitem.Item
	if err := json.Unmarshal(call(t, "story-create", req), &it); err != nil {
		t.Fatal(err)
	}
	return it
}

// sty_d84be8bc AC4: story get, the transition edge and story wave read a story's
// status and definition from the fold of its chain. A row corrupted directly in
// SQL does not change what they answer; listing, which reads the cache, shows it.
func TestReadersAnswerFromTheChainNotTheRow(t *testing.T) {
	db := wire(t)
	ctx := context.Background()
	a := createFolded(t, map[string]any{"title": "authoritative", "body": "the chain's body", "category": "feature"})
	if _, err := db.SQL().Exec(`UPDATE work_items SET status = 'done', body = 'corrupt body', title = 'corrupt title' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}

	// story get: the chain's status, body and title.
	got := getItem(t, a.ID)
	if got.Status != "backlog" || got.Body != "the chain's body" || got.Title != "authoritative" {
		t.Errorf("story get = status %q body %q title %q, want the chain's backlog / %q / authoritative", got.Status, got.Body, got.Title, "the chain's body")
	}
	// Listing still reads the row, the cache.
	var listed []workitem.Item
	if err := json.Unmarshal(call(t, "story-list", map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(listed, func(it workitem.Item) bool { return it.ID == a.ID }); i < 0 || listed[i].Status != "done" || listed[i].Body != "corrupt body" {
		t.Errorf("listing = %+v, want the cached row as it stands", listed)
	}

	// The transition edge: from the chain's backlog, in_progress is declared; from
	// the row's done (a terminal state) it would be refused.
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": a.ID, "status": "in_progress"}); err != nil {
		t.Fatalf("the edge was judged from the corrupted row: %v", err)
	}
	// A mutation ends by rebuilding the cache from the fold, so the row is whole again.
	row, err := db.Stories.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "in_progress" || row.Body != "the chain's body" || row.Title != "authoritative" {
		t.Errorf("row after the mutation = status %q body %q title %q, want it rebuilt from the chain", row.Status, row.Body, row.Title)
	}
}

// A task's file is its source of truth and its row is read as before: the fold
// owns stories only.
func TestATaskIsStillReadFromItsRow(t *testing.T) {
	db := wire(t)
	var task workitem.Item
	if err := json.Unmarshal(call(t, "task-create", map[string]any{"title": "a task"}), &task); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`UPDATE work_items SET status = 'in_progress', body = 'from the file' WHERE id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if got := getItem(t, task.ID); got.Status != "in_progress" || got.Body != "from the file" {
		t.Errorf("a task read through get = status %q body %q, want its row's", got.Status, got.Body)
	}
	if st, _, err := verb.FoldStory(context.Background(), db.Ledger, task); err != nil {
		t.Fatal(err)
	} else if st.Defined[storyfold.FieldTitle] {
		t.Errorf("a task's chain defined a title: %+v", st)
	}
}

func sortedStrings(s ...string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}
