//go:build clislow

package cli

import (
	"strings"
	"testing"
)

// AC2: a substrate-lane story engaged in the tree allows the change on every
// route; a non-substrate story performing refuses it with the holder text; a
// session engaged in nothing is refused even while another session's
// substrate-lane seat is live.
func TestSlowSubstrateLockLaneOnEveryRoute(t *testing.T) {
	for _, c := range lockedChanges() {
		for _, handler := range c.handlers {
			t.Run(handler+"/"+c.name+"/substrate lane allowed", func(t *testing.T) {
				repo, _ := lockRepo(t, "substrate", "")
				if out, err := runRootIn(t, c.event(repo), "hook", handler); err != nil {
					t.Fatalf("a substrate-lane story must be able to change substrate: %v\n%s", err, out)
				}
			})
			t.Run(handler+"/"+c.name+"/non-substrate story refused", func(t *testing.T) {
				repo, id := lockRepo(t, "feature", "")
				out, err := runRootIn(t, c.event(repo), "hook", handler)
				if err == nil {
					t.Fatalf("a non-substrate story must be refused:\n%s", out)
				}
				reason := denyReasonOf(t, out)
				for _, want := range []string{id, "holds a performing seat", "finish or park " + id} {
					if !strings.Contains(reason, want) {
						t.Errorf("deny missing %q: %s", want, reason)
					}
				}
			})
			t.Run(handler+"/"+c.name+"/another session's substrate seat", func(t *testing.T) {
				repo, a, b := holdersRepo(t, "substrate", "substrate")
				out, err := runRootIn(t, c.event(repo), "hook", handler)
				if err == nil {
					t.Fatalf("a session engaged in nothing must be refused:\n%s", out)
				}
				reason := denyReasonOf(t, out)
				if !strings.Contains(reason, noEngagement) || strings.Contains(reason, a.ID) || strings.Contains(reason, b.ID) {
					t.Errorf("want the no-engagement text, naming no holder: %s", reason)
				}
			})
		}
	}
}
