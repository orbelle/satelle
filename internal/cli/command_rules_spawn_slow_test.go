//go:build clislow

package cli

import (
	"os/exec"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// AC5, every git process: against the spawns the same fixture makes with a
// command no rule matches (the no-rule baseline), a matched command that reads
// the seat starts no git process the baseline did not, and none more often than
// the baseline made it. That includes the session worktree (unstamped,
// unmatched and multi-seat sessions all ask for it): it comes from the one
// `--git-common-dir` the call already made, not from its own `--show-toplevel`.
func TestSlowCommitgateMatchedPathSpawnsAgainstNoRuleBaseline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seat    stopcheckSeatState
		session string // "" = an unstamped session
		command string
	}{
		{"stamped, mine, allowed", seatMine, thisSession, "orbctl apply -f x"},
		{"stamped, mine, warned", seatMine, thisSession, "orbctl scale web 2"},
		{"stamped, no seat, warned", seatNone, thisSession, "orbctl scale web 2"},
		{"stamped, sibling's seat (unmatched), warned", seatOther, thisSession, "orbctl scale web 2"},
		{"stamped, sibling's seat (unmatched), denied", seatOther, thisSession, "orbctl apply -f x"},
		{"unstamped, mine, allowed", seatMine, "", "orbctl apply -f x"},
		{"unstamped, mine, warned", seatMine, "", "orbctl scale web 2"},
		{"unstamped, no seat, denied", seatNone, "", "orbctl apply -f x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, _ := ruleRepo(t, tc.seat, exampleRules(t))
			if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, out)
			}
			t.Setenv("SATELLE_CONFIG", "")
			t.Setenv(config.SessionEnv, tc.session)
			spawns := countGitSpawns(t)
			_, _ = commitgateOn(t, "claude", "ls")
			baseline := spawns()
			_, _ = commitgateOn(t, "claude", tc.command)
			matched := spawns()[len(baseline):]

			counts := map[string]int{}
			for _, s := range matched {
				counts[s]++
			}
			for _, s := range baseline {
				counts[s]--
			}
			for s, n := range counts {
				if n > 0 {
					t.Errorf("%q started %d more `git %s` than the no-rule baseline\nbaseline: %q\nmatched:  %q", tc.command, n, s, baseline, matched)
				}
			}
		})
	}
}
