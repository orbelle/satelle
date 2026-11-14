package syncstate

import (
	"os"
	"testing"
	"time"
)

// TestRecordPushHeld: the held set is replaced wholesale, kept apart from the
// failure reason, and an empty set clears it (sty_c203fd62).
func TestRecordPushHeld(t *testing.T) {
	t.Run("a replace drops ids no longer held and leaves the reason alone", func(t *testing.T) {
		home, repo := t.TempDir(), t.TempDir()
		if err := RecordPass(home, repo, Pass{Store: "s", Reason: "refused: archived"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := RecordPushHeld(home, repo, map[string]string{"sty_a": "uncommitted changes in /x", "sty_b": "head 1234 is not on any remote branch"}); err != nil {
			t.Fatal(err)
		}
		if err := RecordPushHeld(home, repo, map[string]string{"sty_b": "head 1234 is not on any remote branch"}); err != nil {
			t.Fatal(err)
		}
		s, _, _ := Load(home, repo)
		if len(s.PushHeld) != 1 || s.PushHeld["sty_b"] == "" {
			t.Errorf("held must be exactly sty_b: %+v", s.PushHeld)
		}
		if s.SyncReason != "refused: archived" {
			t.Errorf("recording held must not touch the failure reason: %q", s.SyncReason)
		}
	})

	t.Run("an empty set clears the held stories", func(t *testing.T) {
		home, repo := t.TempDir(), t.TempDir()
		if err := RecordPushHeld(home, repo, map[string]string{"sty_a": "uncommitted changes in /x"}); err != nil {
			t.Fatal(err)
		}
		if err := RecordPushHeld(home, repo, nil); err != nil {
			t.Fatal(err)
		}
		if s, _, _ := Load(home, repo); s.PushHeld != nil {
			t.Errorf("an empty set must clear held: %+v", s.PushHeld)
		}
	})

	t.Run("nothing held on a repo with no state writes no file", func(t *testing.T) {
		home, repo := t.TempDir(), t.TempDir()
		if err := RecordPushHeld(home, repo, map[string]string{}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(pathFor(home, repo)); !os.IsNotExist(err) {
			t.Errorf("no state file expected, stat err = %v", err)
		}
	})
}
