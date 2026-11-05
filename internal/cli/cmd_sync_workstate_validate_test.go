package cli

import (
	"testing"
)

// holdOffToml syncs stories and ledger to the probe project with the unpushed-code
// hold off, so a story can only be withheld for the reason this file tests.
const holdOffToml = "[sync]\nhold_unpushed = false\n\n[hosted]\nproject = \"probe\"\n"

func (f *fakeWorkstateServer) resetPosts() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = map[string][]map[string]any{}
}

func corruptRow(t *testing.T, id, column, value string) {
	t.Helper()
	db := openScopeDB(t)
	defer func() { _ = db.Close() }()
	if _, err := db.SQL().Exec(`UPDATE work_items SET `+column+` = ? WHERE id = ?`, value, id); err != nil {
		t.Fatal(err)
	}
}

func countOf(ids []string, id string) int {
	n := 0
	for _, x := range ids {
		if x == id {
			n++
		}
	}
	return n
}
