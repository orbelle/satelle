//go:build clislow

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

func TestSlowWorktreeDrivesMainRouteAndKeepsEditsInTheWorktree(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv(config.SessionEnv, "")
	_ = os.Unsetenv(config.SessionEnv)
	t.Setenv("SATELLE_CONFIG", "")
	_ = os.Unsetenv("SATELLE_CONFIG")
	t.Setenv("SATELLE_SERVER_ENDPOINT", "none")

	base := t.TempDir()
	main := filepath.Join(base, "proj")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "-q")
	gitIn(t, main, "config", "user.email", "t@example.com")
	gitIn(t, main, "config", "user.name", "t")
	gitIn(t, main, "add", "-A")
	gitIn(t, main, "commit", "-q", "-m", "init")
	wt := filepath.Join(base, "wt", "sty_driven")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "worktree", "add", "-q", "-b", "sty_driven", wt, "HEAD")

	wf := filepath.Join(main, ".satelle", "workflows")
	writeFile(t, main, ".satelle/satelle.toml", "[review]\ngate_create = false\n")
	writeFile(t, main, ".satelle/workflows/done.toml", featureLaneDone)
	writeFile(t, main, ".satelle/workflows/step.toml", featureLaneStep)
	writeFile(t, main, ".satelle/skills/wt-place-check.md", placeCheckSkill)
	if _, err := os.Stat(wf); err != nil {
		t.Fatal(err)
	}

	t.Chdir(wt)
	if _, err := os.Stat(filepath.Join(wt, ".satelle")); !os.IsNotExist(err) {
		t.Fatalf("worktree already has a data dir: %v", err)
	}
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	id := createFeatureStory(t, "driven from the worktree")

	wtRoute := storyRoute(t, id)
	if !strings.Contains(wtRoute, "**plan**") || !strings.Contains(wtRoute, "wt-place-check") {
		t.Fatalf("worktree route is not the main tree's plan lane:\n%s", wtRoute)
	}
	t.Chdir(main)
	mainRoute := storyRoute(t, id)
	if wtRoute != mainRoute {
		t.Fatalf("routes differ before any transition\nworktree:\n%s\nmain:\n%s", wtRoute, mainRoute)
	}

	t.Chdir(wt)
	wtSkip, wtErr := runRoot(t, "story", "set", id, "--status", "in_progress")
	if wtErr == nil {
		t.Fatalf("backlog → in_progress must be refused\n%s", wtSkip)
	}
	if !strings.Contains(wtErr.Error()+wtSkip, "skipped-step") || !strings.Contains(wtErr.Error()+wtSkip, "plan") {
		t.Fatalf("worktree refusal must name skipped-step and plan: %v\n%s", wtErr, wtSkip)
	}
	t.Chdir(main)
	mainSkip, mainErr := runRoot(t, "story", "set", id, "--status", "in_progress")
	if mainErr == nil {
		t.Fatalf("main tree must refuse the same jump\n%s", mainSkip)
	}
	if !strings.Contains(mainErr.Error()+mainSkip, "skipped-step") || !strings.Contains(mainErr.Error()+mainSkip, "plan") {
		t.Fatalf("main refusal must name skipped-step and plan: %v\n%s", mainErr, mainSkip)
	}

	t.Chdir(wt)
	if out, err := runRoot(t, "story", "set", id, "--status", "plan"); err != nil {
		t.Fatalf("worktree plan transition: %v\n%s", err, out)
	}
	assertPlaceFile(t, wt)
	if _, err := os.Stat(filepath.Join(main, "wt-driven-place")); !os.IsNotExist(err) {
		t.Fatalf("worktree gate wrote into the main tree: %v", err)
	}
	// plan is a performing state, so this story keeps the one engagement seat.
	// Release it so a second story can walk the same gate from the main tree.
	if out, err := runRoot(t, "story", "seat", "release", id); err != nil {
		t.Fatalf("release seat: %v\n%s", err, out)
	}

	t.Chdir(main)
	mainID := createFeatureStory(t, "driven from the main tree")
	if out, err := runRoot(t, "story", "set", mainID, "--status", "in_progress"); err == nil {
		t.Fatalf("main backlog → in_progress must be refused\n%s", out)
	} else if !strings.Contains(err.Error()+out, "skipped-step") || !strings.Contains(err.Error()+out, "plan") {
		t.Fatalf("main refusal must name skipped-step and plan: %v\n%s", err, out)
	}
	if out, err := runRoot(t, "story", "set", mainID, "--status", "plan"); err != nil {
		t.Fatalf("main plan transition: %v\n%s", err, out)
	}
	assertPlaceFile(t, main)

	if _, err := os.Stat(filepath.Join(wt, ".satelle")); !os.IsNotExist(err) {
		t.Fatalf("driving the story created a data dir in the worktree: %v", err)
	}
}
