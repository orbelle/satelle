//go:build clislow

package cli

import (
	"net/http"
	"strings"
	"testing"
)

// AC2: a member, a holder whose tree diverged from the tier, a repo-sourced
// substrate and a failed project lookup are each refused for the team item,
// naming who holds the substrate, and raise no story.
func TestSlowSubstrateReviewRefusesATeamCutOffItsHolder(t *testing.T) {
	for _, c := range []struct {
		name string
		prep func(t *testing.T, repo, server string, h *fakeHolderServer)
		want []string
	}{
		{"member", func(*testing.T, string, string, *fakeHolderServer) {},
			[]string{holderOf(holderID).Email}},
		{"holder with a diverged tree", func(t *testing.T, repo, server string, _ *fakeHolderServer) {
			seedCredAs(t, server, holderID)
			writeRepoFile(t, repo, ".satelle/skills/team-skill.md", tierSkill("diverged"))
		}, []string{holderOf(holderID).Email, "differs from the tier"}},
		{"repo-sourced substrate", func(t *testing.T, _, server string, h *fakeHolderServer) {
			seedCredAs(t, server, holderID)
			h.set(func(h *fakeHolderServer) { h.source, h.repo = "repo", "github.com/acme/substrate" })
		}, []string{"github.com/acme/substrate"}},
		{"lookup failure", func(t *testing.T, _, server string, h *fakeHolderServer) {
			seedCredAs(t, server, holderID)
			h.set(func(h *fakeHolderServer) { h.projectStatus = http.StatusInternalServerError })
		}, []string{"project lookup failed"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo, server, h := teamCutRepo(t)
			c.prep(t, repo, server, h)

			out, err := applyTeamCut(t, server)
			if err == nil {
				t.Fatalf("the team cut was not refused:\n%s", out)
			}
			for _, want := range append([]string{"refused skills/team-skill: origin team"}, c.want...) {
				if !strings.Contains(out, want) {
					t.Errorf("apply output lacks %q:\n%s", want, out)
				}
			}
			if raised := raisedCuts(t); len(raised) != 0 {
				t.Errorf("a refused team cut raised %d stories", len(raised))
			}
		})
	}
}
