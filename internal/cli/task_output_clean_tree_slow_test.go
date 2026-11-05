//go:build clislow

package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/testutil"
)

// TestSlowTaskOutputCleanTree (sty_9e85f8fe AC2): in a repo satelle init has
// healed, with its substrate tracked and main level with origin, recording a
// task execution's run output leaves `git status --porcelain` empty and
// `story worktree --base main` proceeds. Without the run-output ignore line the
// same doc shows as untracked.
func TestSlowTaskOutputCleanTree(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo := tempRepo(t)
	r := testutil.NewTrunkRepos(t).Adopt(t, repo)
	// The substrate is tracked here, not excluded the way Adopt leaves it.
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	isolateUserHome(t)
	if err := runInit(io.Discard, repo, false, nil); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfg := filepath.Join(repo, ".satelle", "satelle.toml")
	body, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, ".satelle/satelle.toml", string(body)+"\n[worktree]\nbranch = \"work/{id}\"\npath = \"../trees/{id}\"\n")
	t.Chdir(repo)
	publish := func(msg string) {
		t.Helper()
		r.Git(t, repo, "add", "-A")
		r.Git(t, repo, "commit", "--quiet", "-m", msg)
		r.Git(t, repo, "push", "--quiet", "origin", "main")
	}
	publish("satelle init")

	out, err := runRoot(t, "task", "create", "--title", "nightly sweep")
	if err != nil {
		t.Fatalf("task create: %v\n%s", err, out)
	}
	task := idOf(t, out)
	if st := r.Git(t, repo, "status", "--porcelain"); st != "" {
		// The authored task header is tracked substrate: commit it like any other.
		publish("add " + task)
	}
	out, err = runRoot(t, "execution", "create", "--title", "sweep run", "--parent", task)
	if err != nil {
		t.Fatalf("execution create: %v\n%s", err, out)
	}
	exe := idOf(t, out)
	if out, err := runRoot(t, "execution", "record", exe, "--output", "swept the lot", "--outcome", "accept"); err != nil {
		t.Fatalf("execution record: %v\n%s", err, out)
	}

	doc := filepath.Join(repo, ".satelle", "tasks", task, "output-"+exe+".md")
	if _, err := os.Stat(doc); err != nil {
		t.Fatalf("test premise: execution record writes the run-output doc: %v", err)
	}
	if st := r.Git(t, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("recording a run output dirtied the tree:\n%s", st)
	}

	id := createStoryJSON(t, "after a recorded run")
	stdout, stderr, err := runRootSplit(t, "", "story", "worktree", id, "--base", "main")
	if err != nil || strings.Contains(stdout+stderr, "trunk dirty tree on main") {
		t.Fatalf("story worktree after a recorded run: %v\n%s\n%s", err, stdout, stderr)
	}

	// Control: without the line the run-output doc is what dirties the tree.
	gi := filepath.Join(repo, ".gitignore")
	raw, _ := os.ReadFile(gi)
	writeFile(t, repo, ".gitignore", withoutLine(t, string(raw), ".satelle/tasks/*/output-exe_*.md"))
	if st := r.Git(t, repo, "status", "--porcelain", "--untracked-files=all"); !strings.Contains(st, "output-"+exe+".md") {
		t.Errorf("without the ignore line the run-output doc must show as untracked:\n%s", st)
	}
}
