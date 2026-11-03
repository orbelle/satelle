//go:build clislow

package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSlowWorktreeDivergenceIsReportedOnEverySurface(t *testing.T) {
	main, wt := linkedDivergenceFixture(t)
	copyProcessInto(t, main, wt)
	// One edited file and one extra file in the worktree's copy.
	writeFile(t, wt, divergenceDataDir+"/skills/wt-place-check.md", placeCheckSkill+"\n# edited in the worktree\n")
	writeFile(t, wt, divergenceDataDir+"/skills/extra.md", alwaysPassSkill("extra"))

	t.Chdir(wt)
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	id := createStoryJSON(t, "driven from a divergent worktree")

	// story route, live-render path (the story has not transitioned).
	live := storyRoute(t, id)
	if !strings.Contains(live, "## Process of record") {
		t.Errorf("live route lacks the Process of record section:\n%s", live)
	}
	wantDivergenceReport(t, "story route (live)", live)

	// A transition refusal names the divergence as well: the operator told that
	// an edge is not declared is also told the worktree's copy is not in force.
	out, err := runRoot(t, "story", "set", id, "--status", "in_progress")
	if err == nil {
		t.Fatalf("backlog → in_progress must be refused\n%s", out)
	}
	if !strings.Contains(err.Error()+out, "skipped-step") {
		t.Errorf("the refusal must still be the skipped-step refusal: %v\n%s", err, out)
	}
	wantDivergenceReport(t, "transition refusal", err.Error()+"\n"+out)

	// engage: the main tree's route governs and the transition succeeds; the
	// report is on stderr and stdout is still the story JSON.
	var stdout string
	var serr error
	stderr := captureStderr(t, func() {
		stdout, _, serr = runRootSplit(t, "", "story", "set", id, "--status", "plan")
	})
	if serr != nil {
		t.Fatalf("plan transition: %v\n%s", serr, stdout)
	}
	wantDivergenceReport(t, "engage stderr", stderr)
	if !json.Valid([]byte(stdout)) || strings.Contains(stdout, "worktree's copy") {
		t.Errorf("stdout must stay JSON and carry no report:\n%s", stdout)
	}

	// story route, stored-doc path: the transition wrote a route document.
	stored := storyRoute(t, id)
	if !strings.Contains(stored, "**plan**") || !strings.Contains(stored, "## Process of record") {
		t.Errorf("stored route must carry the main tree's lane and the Process of record section:\n%s", stored)
	}
	wantDivergenceReport(t, "story route (stored)", stored)

	// The check that ran is the main tree's copy, run in the worktree: had the
	// worktree's edited copy governed, nothing here could tell — but the place
	// file proves the gate ran and wrote beside the worktree, not the main tree.
	assertPlaceFile(t, wt)

	// doctor: a warning that lists the same files.
	doc, _ := runRoot(t, "doctor")
	wantDivergenceReport(t, "doctor", doc)
}

// A main tree has no worktree to diverge from, and a worktree whose hand-copied
// process matches the main tree's has nothing to report: neither gets a
// divergence report on any surface.
func TestSlowNoDivergenceReportWithoutDivergence(t *testing.T) {
	check := func(t *testing.T, where string) {
		t.Helper()
		t.Chdir(where)
		if out, err := runRoot(t, "reindex"); err != nil {
			t.Fatalf("reindex: %v\n%s", err, out)
		}
		id := createStoryJSON(t, "no divergence")
		live := storyRoute(t, id)
		wantNoDivergenceReport(t, "story route (live)", live)
		if strings.Contains(live, "## Process of record") {
			t.Errorf("a healthy repo's route output must be unchanged:\n%s", live)
		}
		var stdout string
		var serr error
		stderr := captureStderr(t, func() {
			stdout, _, serr = runRootSplit(t, "", "story", "set", id, "--status", "plan")
		})
		if serr != nil {
			t.Fatalf("plan transition: %v\n%s", serr, stdout)
		}
		wantNoDivergenceReport(t, "engage stderr", stderr)
		stored := storyRoute(t, id)
		wantNoDivergenceReport(t, "story route (stored)", stored)
		if strings.Contains(stored, "## Process of record") {
			t.Errorf("a healthy repo's route output must be unchanged:\n%s", stored)
		}
		doc, _ := runRoot(t, "doctor")
		wantNoDivergenceReport(t, "doctor", doc)
		if strings.Contains(doc, "process.divergent") {
			t.Errorf("doctor reported a divergence:\n%s", doc)
		}
	}

	t.Run("main tree", func(t *testing.T) {
		main, _ := linkedDivergenceFixture(t)
		check(t, main)
	})
	t.Run("worktree with an identical copy", func(t *testing.T) {
		main, wt := linkedDivergenceFixture(t)
		copyProcessInto(t, main, wt)
		check(t, wt)
	})
}
