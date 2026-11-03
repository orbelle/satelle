//go:build clislow

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/substratereview"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func TestSlowSubstrateReviewReportsEveryItemAndRuleAndAppliesChosenCuts(t *testing.T) {
	repo := substrateReviewFixture(t)
	input := stubSubstrateJudge(t, substratereview.Judgement{
		Items: []substratereview.ItemJudgement{
			{Item: "skills/satelle-panel-majority", Disposition: "remove", Reason: "the embedded default covers it", Evidence: "skills/satelle-panel-majority (embedded)"},
			{Item: "skills/repo-cut", Disposition: "reduce", KeptLines: []string{"keep me"}, Reason: "rejected once for length", Evidence: "repo-cut.md:9", Replacement: "# repo cut\n\nkeep me\n"},
			{Item: "skills/satelle-story-cancel-review", Disposition: "keep", Rule: "names the cancel reason", Reason: "no default names it", Evidence: "satelle-story-cancel-review.md:3"},
			{Item: "agents/executor", Disposition: "conflict", Rule: "satelle-agent-agnostic", Reason: "names a vendor", Evidence: "agents.toml:2"},
			// skills/team-skill is omitted: it must read "not judged".
		},
		Constitution: []substratereview.RuleFinding{
			{Rule: "rule-a", File: "internal/x.go", Line: 7, Note: "names a vendor"},
			{Rule: "rule-b", None: true},
			// rule-c is omitted: it must read "not judged".
		},
	})
	scratch := t.TempDir()
	report := filepath.Join(scratch, "report.json")

	// AC1 + AC3: one verb, a report, no file in the tree changed.
	before := treeHashes(t, repo)
	out, err := runRoot(t, "substrate", "review", "--out", report)
	if err != nil {
		t.Fatalf("substrate review: %v\n%s", err, out)
	}
	assertTreeUnchanged(t, "substrate review", before, treeHashes(t, repo))

	origins := map[string]string{}
	for _, it := range input.Items {
		origins[it.Key] = it.Origin
	}
	for key, want := range map[string]string{
		"skills/satelle-panel-majority":      substratereview.OriginSeeded,
		"skills/repo-cut":                    substratereview.OriginRepo,
		"skills/satelle-story-cancel-review": substratereview.OriginEmbedded,
		"skills/team-skill":                  substratereview.OriginTeam,
	} {
		if origins[key] != want {
			t.Errorf("origin of %s = %q, want %q", key, origins[key], want)
		}
	}
	for _, kind := range []string{"principles/", "skills/", "workflows/default/", "agents/"} {
		found := false
		for key := range origins {
			found = found || strings.HasPrefix(key, kind)
		}
		if !found {
			t.Errorf("the inventory holds no %s item", kind)
		}
	}
	for _, want := range []string{
		"### skills/satelle-panel-majority — remove", "- origin: seeded",
		"### skills/repo-cut — reduce", "- kept: keep me", "- rejected: too long", "model M1: 0 invocation(s), 0 accept(s), 1 rejection(s)",
		"### skills/satelle-story-cancel-review — keep", "- rule: names the cancel reason", "- origin: embedded",
		"### agents/executor — conflict", "- rule: satelle-agent-agnostic",
		"### skills/team-skill — not judged", "- origin: team",
		"- rule-a (constitution:3): finding internal/x.go:7 — names a vendor",
		"- rule-b (constitution:4): no finding",
		"- rule-c (constitution:5): not judged",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(report); err != nil {
		t.Fatalf("the JSON report was not written: %v", err)
	}

	// --out inside the tree is refused, so the report cannot change the tree.
	if out, err := runRoot(t, "substrate", "review", "--out", filepath.Join(repo, "report.json")); err == nil || !strings.Contains(err.Error()+out, "inside the repo tree") {
		t.Errorf("--out inside the tree should be refused: %v\n%s", err, out)
	}

	// AC4: apply the chosen cuts from the saved report.
	before = treeHashes(t, repo)
	out, err = runRoot(t, "substrate", "review", "--from", report,
		"--apply", "skills/satelle-panel-majority", "--apply", "skills/repo-cut",
		"--apply", "skills/satelle-story-cancel-review", "--apply", "skills/team-skill")
	if err == nil {
		t.Errorf("apply with refused items should fail:\n%s", out)
	}
	assertTreeUnchanged(t, "substrate review --apply", before, treeHashes(t, repo))
	for _, want := range []string{
		"for skills/satelle-panel-majority (remove, origin seeded)",
		"for skills/repo-cut (reduce, origin repo)",
		"refused skills/satelle-story-cancel-review: origin embedded",
		"refused skills/team-skill: origin team",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("apply output lacks %q:\n%s", want, out)
		}
	}

	out, err = runRoot(t, "story", "list", "--tag", substratereview.ApplyTag)
	if err != nil {
		t.Fatalf("story list: %v\n%s", err, out)
	}
	var raised []workitem.Item
	if err := json.Unmarshal([]byte(out), &raised); err != nil {
		t.Fatalf("parse story list: %v\n%s", err, out)
	}
	if len(raised) != 2 {
		t.Fatalf("raised %d stories, want 2 (seeded and repo only):\n%s", len(raised), out)
	}
	bodies := map[string]workitem.Item{}
	for _, it := range raised {
		if it.Category != substratereview.ApplyCategory {
			t.Errorf("%s category = %q, want substrate", it.ID, it.Category)
		}
		for _, key := range []string{"skills/satelle-panel-majority", "skills/repo-cut"} {
			if strings.Contains(it.Title, key) {
				bodies[key] = it
			}
		}
	}
	seeded, repoCut := bodies["skills/satelle-panel-majority"], bodies["skills/repo-cut"]
	for _, c := range []struct {
		it   workitem.Item
		want []string
	}{
		{seeded, []string{"- origin: seeded", "- disposition: remove", "## Evidence", "dissent: not recorded"}},
		{repoCut, []string{"- origin: repo", "- disposition: reduce", "- kept: keep me", "- rejected: too long"}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.it.Body, w) {
				t.Errorf("story %q body lacks %q:\n%s", c.it.Title, w, c.it.Body)
			}
		}
	}
	if !strings.Contains(repoCut.AcceptanceCriteria, `keeps only these lines: "keep me"`) || !strings.Contains(repoCut.AcceptanceCriteria, "1 rejection(s)") {
		t.Errorf("repo-cut criteria should carry the kept lines and the evidence:\n%s", repoCut.AcceptanceCriteria)
	}
	if !strings.Contains(seeded.AcceptanceCriteria, "skills/satelle-panel-majority is removed") {
		t.Errorf("seeded criteria should carry the cut:\n%s", seeded.AcceptanceCriteria)
	}
}

// sty_c356ed4f AC3: a cut applied from a review, or from its saved report, is a
// substrate story naming the execution it came from.
func TestSlowSubstrateReviewAppliedCutNamesItsExecution(t *testing.T) {
	substrateReviewFixture(t)
	stubSubstrateJudge(t, substratereview.Judgement{Items: []substratereview.ItemJudgement{
		{Item: "skills/satelle-panel-majority", Disposition: "remove", Reason: "the default covers it", Evidence: "skills/satelle-panel-majority (embedded)"},
		{Item: "skills/repo-cut", Disposition: "reduce", KeptLines: []string{"keep me"}, Reason: "too long", Evidence: "repo-cut.md:9"},
	}})
	report := filepath.Join(t.TempDir(), "report.json")
	if out, err := runRoot(t, "substrate", "review", "--out", report, "--apply", "skills/repo-cut"); err != nil {
		t.Fatalf("substrate review --apply: %v\n%s", err, out)
	}
	exe := onlyReviewRun(t).item.ID
	if out, err := runRoot(t, "substrate", "review", "--from", report, "--apply", "skills/satelle-panel-majority"); err != nil {
		t.Fatalf("substrate review --from --apply: %v\n%s", err, out)
	}
	out, err := runRoot(t, "story", "list", "--tag", "execution:"+exe)
	if err != nil {
		t.Fatalf("story list: %v\n%s", err, out)
	}
	var raised []workitem.Item
	if err := json.Unmarshal([]byte(out), &raised); err != nil {
		t.Fatalf("parse story list: %v\n%s", err, out)
	}
	if len(raised) != 2 {
		t.Fatalf("%d stories name execution %s, want 2 (one per apply path):\n%s", len(raised), exe, out)
	}
	for _, it := range raised {
		if it.Category != substratereview.ApplyCategory || !strings.Contains(it.Body, "From execution "+exe) {
			t.Errorf("%s: category %q, body %q — want a substrate story naming %s", it.ID, it.Category, it.Body, exe)
		}
	}
}

// sty_c356ed4f AC2: a close the route's gate refuses ends the execution
// cancelled with the refusal as its reason. The gate is a coded check, so no
// reviewer agent runs.
func TestSlowSubstrateReviewRefusedCloseCancelsItsExecution(t *testing.T) {
	repo := substrateReviewFixture(t)
	writeRepoFile(t, repo, filepath.Join(config.DefaultDataDir, "skills", "refuse-run.md"),
		"---\nname: refuse-run\ntype: skill\ntags: [type:skill, type:reviewer, type:functional-check]\ndescription: refuses every run close\n---\n# refuse run\n\n```check\necho \"the run left no evidence\"\nexit 1\n```\n")
	rel := filepath.Join(config.DefaultDataDir, "workflows", "step.toml")
	step, err := os.ReadFile(filepath.Join(repo, rel))
	if err != nil {
		t.Fatal(err)
	}
	const verified = "[run-verified]\n"
	if strings.Count(string(step), verified) != 1 {
		t.Fatal("the step catalogue declares no run-verified step")
	}
	writeRepoFile(t, repo, rel, strings.Replace(string(step), verified, verified+`reviewers = ["refuse-run"]`+"\n", 1))
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	stubSubstrateJudge(t, substratereview.Judgement{})
	if out, err := runRoot(t, "substrate", "review", "--out", filepath.Join(t.TempDir(), "report.json")); err == nil {
		t.Fatalf("a review whose close is refused must fail:\n%s", out)
	}
	r := onlyReviewRun(t)
	if r.item.Status != workitem.StatusCancelled {
		t.Fatalf("the refused run is %s, want cancelled", r.item.Status)
	}
	if strings.Join(r.moves, ",") != "backlog→in_progress,in_progress→cancelled" {
		t.Errorf("moves = %v", r.moves)
	}
	last := r.stages[len(r.stages)-1]
	if last["stage"] != workitem.StatusCancelled || !strings.Contains(fmt.Sprint(last["reason"]), "refuse-run") {
		t.Errorf("the cancel does not record the refusal: %v", last)
	}
	if r.report == "" {
		t.Error("the refused run keeps no report attachment")
	}
}
