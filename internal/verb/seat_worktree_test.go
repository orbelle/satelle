package verb_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// addWorktree opens one more linked tree off treeA and returns its toplevel.
func addWorktree(t *testing.T, treeA, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("git", "worktree", "add", "-b", name, dir)
	cmd.Dir = treeA
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add %s: %v\n%s", name, err, out)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("toplevel %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}
