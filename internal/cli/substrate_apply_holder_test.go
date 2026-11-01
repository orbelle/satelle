package cli

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/substratereview"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// teamCutRepo is holderTierRepo whose deployed tier carries the step catalogue
// with no validator on the execution run, so a review runs with no reviewer
// agent, and whose judge returns a remove for the team-owned skill. The stored
// credential is the member's.
func teamCutRepo(t *testing.T) (string, string, *fakeHolderServer) {
	t.Helper()
	prev := substrateScratchRoot
	substrateScratchRoot = t.TempDir()
	t.Cleanup(func() { substrateScratchRoot = prev })
	repo, ts, h := holderTierRepoWith(t, func(t *testing.T, repo string) { runValidatorsDropped(t, repo) })
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	stubSubstrateJudge(t, substratereview.Judgement{Items: []substratereview.ItemJudgement{
		{Item: "skills/team-skill", Disposition: "remove", Reason: "the default covers it", Evidence: "team-skill.md:1"},
	}})
	return repo, ts.URL, h
}

// applyTeamCut runs the review and applies the team-owned skill's cut.
func applyTeamCut(t *testing.T, server string) (string, error) {
	t.Helper()
	return runRoot(t, "substrate", "review", "--out", filepath.Join(t.TempDir(), "report.json"),
		"--apply", "skills/team-skill", "--server", server)
}

// raisedCuts is every story --apply raised.
func raisedCuts(t *testing.T) []workitem.Item {
	t.Helper()
	out, err := runRoot(t, "story", "list", "--tag", substratereview.ApplyTag)
	if err != nil {
		t.Fatalf("story list: %v\n%s", err, out)
	}
	var raised []workitem.Item
	if err := json.Unmarshal([]byte(out), &raised); err != nil {
		t.Fatalf("parse story list: %v\n%s", err, out)
	}
	return raised
}

// AC1: the substrate holder, whose tree's copy is the tier's, raises a
// team-owned item's cut in this repo.
func TestSubstrateReviewAppliesATeamCutAtItsHolder(t *testing.T) {
	_, server, _ := teamCutRepo(t)
	seedCredAs(t, server, holderID)

	out, err := applyTeamCut(t, server)
	if err != nil {
		t.Fatalf("the holder's apply failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "for skills/team-skill (remove, origin team)") {
		t.Errorf("apply output lacks the raised team cut:\n%s", out)
	}
	raised := raisedCuts(t)
	if len(raised) != 1 {
		t.Fatalf("raised %d stories, want 1", len(raised))
	}
	if raised[0].Category != substratereview.ApplyCategory || !strings.Contains(raised[0].Body, "- origin: team") {
		t.Errorf("raised %q (category %q), want a substrate story for the team cut:\n%s", raised[0].Title, raised[0].Category, raised[0].Body)
	}
}

// AC2: a member, a holder whose tree diverged from the tier, a repo-sourced
// substrate and a failed project lookup are each refused for the team item,
// naming who holds the substrate, and raise no story.
func TestSubstrateReviewRefusesATeamCutOffItsHolder(t *testing.T) {
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
