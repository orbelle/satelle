//go:build clislow

package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
)

// TestSlowExecutionAtInProgressPassesCommitGate (sty_931026b3 AC2/AC3): a release
// run's execution moved into in_progress is the only engaged item in the tree,
// and the branch-delete and tag pushes its action declares pass the commit gate.
// With no story or execution performing — before the move and after its close —
// the same pushes are refused as unengaged.
func TestSlowExecutionAtInProgressPassesCommitGate(t *testing.T) {
	repo := tempRepo(t)
	t.Chdir(repo)
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"), execRouteDone, execRouteStep)
	writeFile(t, repo, ".satelle/satelle.toml",
		"[review]\ngate_create = false\n\n[gate.command_allow]\npush = [\"in_progress\"]\n")
	gitInitCommit(t, repo)
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	t.Setenv(config.SessionEnv, execRunSession)
	stubWorktree(t, repo)

	out, err := runRoot(t, "task", "create", "--title", "release")
	if err != nil {
		t.Fatalf("task create: %v\n%s", err, out)
	}
	task := idOf(t, out)
	out, err = runRoot(t, "execution", "create", "--title", "release run", "--parent", task)
	if err != nil {
		t.Fatalf("execution create: %v\n%s", err, out)
	}
	exe := idOf(t, out)

	pushes := []string{"git push origin --delete story/sty_x", "git push origin refs/tags/v0.0.1"}
	refused := func(when string) {
		t.Helper()
		for _, push := range pushes {
			out, err := runRootIn(t, execRunBash(push), "hook", "commitgate")
			if err == nil || !strings.Contains(out, "refusing to commit/push with no engaged story") {
				t.Fatalf("%s: %q must be refused as unengaged: %v\n%s", when, push, err, out)
			}
		}
	}

	refused("control, execution at backlog")

	if out, err := runRoot(t, "execution", "set", exe, "--status", "in_progress"); err != nil {
		t.Fatalf("execution engage: %v\n%s", err, out)
	}
	for _, push := range pushes {
		if out, err := runRootIn(t, execRunBash(push), "hook", "commitgate"); err != nil || strings.Contains(out, "deny") {
			t.Fatalf("with %s at in_progress, %q must be admitted: %v\n%s", exe, push, err, out)
		}
	}

	// The tree's uncommitted work is the worked story's; it never holds the run's close.
	writeFile(t, repo, "wip.go", "package wip\n")
	if out, err := runRoot(t, "execution", "set", exe, "--status", "done"); err != nil {
		t.Fatalf("execution close: %v\n%s", err, out)
	}
	refused("after the execution's close")
}

// TestSlowReengagedExecutionPushAttributedToItOverASameTreeStory (AC1/AC2): the run
// seats a story in the main tree (at plan), then its execution — whose seat was
// dropped — is re-engaged with `story set <exe> --status in_progress` from the
// same tree and session. The push is admitted and attributed to the execution:
// push is allowed only at in_progress, so attributing it to the story (at plan)
// would refuse it.
func TestSlowReengagedExecutionPushAttributedToItOverASameTreeStory(t *testing.T) {
	repo := tempRepo(t)
	t.Chdir(repo)
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"), execRouteDone, execRouteStep)
	writeFile(t, repo, ".satelle/satelle.toml",
		"[review]\ngate_create = false\n\n[gate.command_allow]\npush = [\"in_progress\"]\n")
	gitInitCommit(t, repo)
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	t.Setenv(config.SessionEnv, execRunSession)
	stubWorktree(t, repo)

	// The run's execution, moved into its performing state, then its seat dropped.
	out, err := runRoot(t, "task", "create", "--title", "release")
	if err != nil {
		t.Fatalf("task create: %v\n%s", err, out)
	}
	task := idOf(t, out)
	out, err = runRoot(t, "execution", "create", "--title", "release run", "--parent", task)
	if err != nil {
		t.Fatalf("execution create: %v\n%s", err, out)
	}
	exe := idOf(t, out)
	if out, err := runRoot(t, "execution", "set", exe, "--status", "in_progress"); err != nil {
		t.Fatalf("execution engage: %v\n%s", err, out)
	}
	forceRelease(t, exe)

	// The run then moves a story into a performing state from the same tree.
	out, err = runRoot(t, "story", "create", "--title", "released story", "--body", "goal",
		"--acceptance", "1. ok", "--category", "feature")
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	story := idOf(t, out)
	if out, err := runRoot(t, "story", "set", story, "--status", "plan"); err != nil {
		t.Fatalf("story engage: %v\n%s", err, out)
	}
	reseatUnderEpicKey(t, story, "plan", repo)

	// With only the story seated, the push is the story's, and it is at plan.
	if out, err := runRootIn(t, execRunBash("git push origin main"), "hook", "commitgate"); err == nil {
		t.Fatalf("control: with only the plan story seated the push must be refused:\n%s", out)
	}

	time.Sleep(5 * time.Millisecond) // time-subject: the re-engage is acquired after the story's seat
	if out, err := runRoot(t, "story", "set", exe, "--status", "in_progress"); err != nil {
		t.Fatalf("re-engage the execution: %v\n%s", err, out)
	}
	seats, err := runRoot(t, "story", "seat")
	if err != nil || !strings.Contains(seats, exe) {
		t.Fatalf("satelle story seat must list %s after the re-engage: %v\n%s", exe, err, seats)
	}

	info, engaged, live, err := resolveSeatsFor(false, execRunSession, repo)
	if len(live) != 2 || live[0].ItemID != story {
		t.Fatalf("fixture: the store must list the story's seat first, got %+v", live)
	}
	if err != nil || !engaged || info.ItemID != exe {
		t.Fatalf("push from the main tree attributed to %q (engaged=%v err=%v), want %s, not %s", info.ItemID, engaged, err, exe, story)
	}
	if out, err := runRootIn(t, execRunBash("git push origin main"), "hook", "commitgate"); err != nil {
		t.Fatalf("the re-engaged execution is at in_progress, so the push must be admitted: %v\n%s", err, out)
	}
}
