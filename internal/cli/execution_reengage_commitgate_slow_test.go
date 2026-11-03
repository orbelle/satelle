//go:build clislow

package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
)

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
