//go:build clislow

package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/syncstate"
)

// AC4: once the code is on the remote the next push sends every held row, with
// no new transition and no --full.
func TestSlowWorkstatePushPublishesHeldRowsOnceCodeIsPushed(t *testing.T) {
	w := newHoldWorld(t, holdSyncToml)
	w.push(t)
	w.assertPublished(t, w.dirty, false)
	w.assertPublished(t, w.unpushed, false)

	// Still held while nothing has changed.
	w.push(t)
	w.assertPublished(t, w.dirty, false)

	gitIn(t, w.repo, "add", "-A")
	gitIn(t, w.repo, "commit", "-m", "feature")
	gitIn(t, w.repo, "push", "origin", "HEAD:main")
	gitIn(t, w.unpushedTree, "push", "origin", "HEAD:refs/heads/side")

	out := w.push(t)
	if strings.Contains(out, "held ") {
		t.Errorf("nothing should be held once the code is pushed:\n%s", out)
	}
	w.assertPublished(t, w.dirty, true)
	w.assertPublished(t, w.unpushed, true)
}

// sty_c203fd62 AC1: a push whose only unpublished stories are held exits zero,
// records no failure reason and records the held ids with their reasons apart
// from it; once the code is on the remote the next push clears them.
func TestSlowWorkstatePushRecordsHeldStoriesApartFromFailure(t *testing.T) {
	w := newHoldWorld(t, holdSyncToml)
	w.push(t) // fails the test on a non-zero exit

	st, ok, err := syncstate.Load(config.GlobalDir(), w.repo)
	if err != nil || !ok {
		t.Fatalf("no sync state recorded: ok=%v err=%v", ok, err)
	}
	if st.PushReason != "" {
		t.Errorf("a push that only held stories recorded a failure reason: %q", st.PushReason)
	}
	if st.PushLastSuccess.IsZero() {
		t.Error("a push that only held stories must record success")
	}
	if got := st.PushHeld[w.dirty]; got != "uncommitted changes in "+w.repo {
		t.Errorf("held reason for %s = %q, want uncommitted changes in %s", w.dirty, got, w.repo)
	}
	head := gitIn(t, w.unpushedTree, "rev-parse", "HEAD")[:8]
	if got := st.PushHeld[w.unpushed]; got != "head "+head+" is not on any remote branch" {
		t.Errorf("held reason for %s = %q", w.unpushed, got)
	}
	if len(st.PushHeld) != 2 {
		t.Errorf("only the two held stories may be recorded: %+v", st.PushHeld)
	}

	gitIn(t, w.repo, "add", "-A")
	gitIn(t, w.repo, "commit", "-m", "feature")
	gitIn(t, w.repo, "push", "origin", "HEAD:main")
	gitIn(t, w.unpushedTree, "push", "origin", "HEAD:refs/heads/side")
	w.push(t)
	if st, _, _ := syncstate.Load(config.GlobalDir(), w.repo); len(st.PushHeld) != 0 {
		t.Errorf("held stories must clear once their code is pushed: %+v", st.PushHeld)
	}
}

// A held story only holds its own rows back: a later row of another story is
// still sent, and the held rows survive until the story's code is pushed.
func TestSlowWorkstatePushHeldRowsDoNotBlockLaterRows(t *testing.T) {
	w := newHoldWorld(t, holdSyncToml)
	w.push(t)
	later := createStoryID(t, "created after the hold")
	w.push(t)
	w.assertPublished(t, later, true)
	w.assertPublished(t, w.dirty, false)
}
