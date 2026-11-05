package verb_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workitem"
)

// twoWorktrees builds a real git repo with a real second working tree — the
// shape the mode exists for. Returns both toplevels; the caller chdirs between
// them. Both trees share one git common dir, which is exactly why they already
// share one satelle DB and one seat (config.RepoKey).
func twoWorktrees(t *testing.T) (treeA, treeB string) {
	t.Helper()
	treeA = t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in %s: %v\n%s", args, dir, err, out)
		}
	}
	run(treeA, "git", "init")
	run(treeA, "git", "config", "user.email", "t@t")
	run(treeA, "git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(treeA, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(treeA, "git", "add", "a.txt")
	run(treeA, "git", "commit", "-m", "init")
	// Sibling tree beside the first so neither is nested inside the other.
	treeB = filepath.Join(t.TempDir(), "tree-b")
	run(treeA, "git", "worktree", "add", "-b", "sibling", treeB)
	// git resolves symlinked temp dirs (macOS /var → /private/var); ask git for
	// the canonical toplevel so the test compares what the code compares.
	top := func(dir string) string {
		out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
		if err != nil {
			t.Fatalf("toplevel %s: %v", dir, err)
		}
		return strings.TrimSpace(string(out))
	}
	return top(treeA), top(treeB)
}

// engagementModeTestWF is singleStoryWF's shape under a fresh name so these
// cases are independent of the fixtures the single-occupancy tests pin.
var engagementModeTestWF = routeHalves(
	`["*"]
obligations = ["raised", "planned", "coded", "closed"]
park = { state = "blocked" }
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

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)

func createChild(t *testing.T, title, parent string) workitem.Item {
	t.Helper()
	req := map[string]any{"title": title, "category": "feature"}
	if parent != "" {
		req["parent_id"] = parent
	}
	var it workitem.Item
	json.Unmarshal(call(t, "story-create", req), &it)
	return it
}
