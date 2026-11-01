package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/substratereview"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// treeHashes is every file under repo with its sha256 — the tree a command
// must leave unchanged.
func treeHashes(t *testing.T, repo string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[strings.TrimPrefix(p, repo)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertTreeUnchanged fails on any file stage changed or created in the tree,
// except the review's own run record: its execution and that execution's report
// attachment, under the task's folder (sty_c356ed4f).
func assertTreeUnchanged(t *testing.T, stage string, before, after map[string]string) {
	t.Helper()
	runRecord := string(filepath.Separator) + filepath.Join(config.DefaultDataDir, "tasks", substrateReviewTask) + string(filepath.Separator)
	for p, h := range before {
		if after[p] != h && !strings.HasPrefix(p, runRecord) {
			t.Errorf("%s changed %s in the tree", stage, p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok && !strings.HasPrefix(p, runRecord) {
			t.Errorf("%s created %s in the tree", stage, p)
		}
	}
}

// runValidatorsDropped authors repo's step catalogue (its own copy, else the
// embedded default) with no validator on the execution run: its two task-validate
// reviewers removed and the instruction-change gate scoped off the task-run
// categories, so a review's execution starts and closes with no reviewer agent.
// The shipped before-gate would also shell out to whatever satelle binary is on
// PATH. Call it before the fixture's reindex. Every other step and gate is the
// shipped one.
func runValidatorsDropped(t *testing.T, repo string) {
	t.Helper()
	rel := filepath.Join(config.DefaultDataDir, "workflows", "step.toml")
	body, err := os.ReadFile(filepath.Join(repo, rel))
	if err != nil {
		for _, d := range config.EmbeddedDefaults() {
			if d.Kind == "workflows" && d.Name == "step" {
				body = []byte(d.Body)
			}
		}
	}
	s := string(body)
	for line, with := range map[string]string{
		`reviewers = ["satelle-task-validate-before-review"]` + "\n": "",
		`reviewers = ["satelle-task-validate-after-review"]` + "\n":  "",
		`for = ["*", "docs", "execution", "task"]` + "\n":            `for = ["*", "docs"]` + "\n",
	} {
		if strings.Count(s, line) != 1 {
			t.Fatalf("the step catalogue does not hold %q once", line)
		}
		s = strings.Replace(s, line, with, 1)
	}
	writeRepoFile(t, repo, rel, s)
}

// substrateReviewFixture lays a repo holding one item of each origin the apply
// path distinguishes: a seeded copy of an embedded skill, a repo-authored skill,
// an embedded-only skill, and a skill the project tier owns. The constitution
// links three rules, and one story's ledger holds a rejection by the repo skill.
func substrateReviewFixture(t *testing.T) string {
	t.Helper()
	repo := tempRepo(t)
	runValidatorsDropped(t, repo)
	writeRepoFile(t, repo, ".satelle/skills/repo-cut.md", "---\ntype: skill\nname: repo-cut\ndescription: a repo skill\n---\n# repo cut\n\nkeep me\n\ndrop me\n")
	writeRepoFile(t, repo, ".satelle/constitution.md", "# constitution\n\n- first: [[rule-a]]\n- second: [[rule-b]]\n- third: [[rule-c]]\n")
	if out, err := runRoot(t, "substrate", "edit", "skills", "satelle-panel-majority"); err != nil {
		t.Fatalf("substrate edit: %v\n%s", err, out)
	}

	// The project tier owns skills/team-skill.md.
	cfgPath := filepath.Join(repo, ".satelle", "satelle.toml")
	if err := os.WriteFile(cfgPath, []byte("[review]\ngate_create = false\n\n[hosted]\nproject = \"team-p\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	tier := config.ProjectTierDir(cfg.ResolveRuntimeDir(repo).Dir)
	writeRepoFile(t, tier, "skills/team-skill.md", "---\ntype: skill\nname: team-skill\ndescription: team skill\n---\n# team skill\n")
	stamp, _ := json.Marshal(config.ProjectTierStamp{Project: "team-p", TeamPaths: []string{"skills/team-skill.md"}, Files: []string{"skills/team-skill.md"}, DeployedAt: time.Now()})
	writeRepoFile(t, tier, config.ProjectTierStampName, string(stamp))

	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	out, err := runRoot(t, "story", "create", "--title", "History", "--body", "b", "--status", "backlog")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created workitem.Item
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("parse create: %v\n%s", err, out)
	}
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b, _ := json.Marshal(map[string]any{"from": "backlog", "to": "plan", "skill": "repo-cut", "attempt": "att_x", "notes": "too long", "model_resolved": "M1"})
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{StoryID: created.ID, Kind: ledger.KindReviewReject, Actor: "reviewer", Body: "reject", Payload: b}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return repo
}

// stubSubstrateJudge swaps the judge for one returning j for every batch, and
// records the items of every batch it was handed.
func stubSubstrateJudge(t *testing.T, j substratereview.Judgement) *substratereview.JudgeInput {
	t.Helper()
	prev := newSubstrateJudge
	t.Cleanup(func() { newSubstrateJudge = prev })
	var got substratereview.JudgeInput
	newSubstrateJudge = func(*cobra.Command, string, substratereview.History) (substrateJudge, error) {
		return func(_ context.Context, in substratereview.JudgeInput) (substratereview.Judgement, string, error) {
			got.Items = append(got.Items, in.Items...)
			return j, "", nil
		}, nil
	}
	return &got
}

func TestSubstrateReviewReportsEveryItemAndRuleAndAppliesChosenCuts(t *testing.T) {
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

// reviewRun is a review's execution as the CLI reads it back: the record, its
// status transitions and stage rows in ledger order, and its report attachment.
type reviewRun struct {
	item   workitem.Item
	moves  []string         // "from→to", oldest first
	stages []map[string]any // each stage row's data, oldest first
	order  []string         // "move:<to>" / "stage:<stage>", oldest first
	report string
}

// onlyReviewRun reads the one execution of the review task through the CLI.
func onlyReviewRun(t *testing.T) reviewRun {
	t.Helper()
	out, err := runRoot(t, "execution", "list", "--parent", substrateReviewTask)
	if err != nil {
		t.Fatalf("execution list: %v\n%s", err, out)
	}
	var runs []workitem.Item
	if err := json.Unmarshal([]byte(out), &runs); err != nil {
		t.Fatalf("parse execution list: %v\n%s", err, out)
	}
	if len(runs) != 1 {
		t.Fatalf("%d executions of %s, want 1:\n%s", len(runs), substrateReviewTask, out)
	}
	r := reviewRun{item: runs[0]}
	out, err = runRoot(t, "ledger", "list", "--story", r.item.ID)
	if err != nil {
		t.Fatalf("ledger list: %v\n%s", err, out)
	}
	var rows []ledger.Entry
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("parse ledger list: %v\n%s", err, out)
	}
	ledger.SortEntries(rows)
	for _, e := range rows {
		switch e.Kind {
		case ledger.KindStatusTransition:
			var p struct{ From, To string }
			_ = json.Unmarshal(e.Payload, &p)
			r.moves = append(r.moves, p.From+"→"+p.To)
			r.order = append(r.order, "move:"+p.To)
		case ledger.KindTelemetryEvent:
			var env struct {
				Kind string         `json:"kind"`
				Data map[string]any `json:"data"`
			}
			if json.Unmarshal(e.Payload, &env) == nil && env.Kind == substrateReviewStageKind {
				r.stages = append(r.stages, env.Data)
				r.order = append(r.order, fmt.Sprint("stage:", env.Data["stage"]))
			}
		}
	}
	if out, err := runRoot(t, "story", "doc", r.item.ID, "substrate-review-report-"+r.item.ID); err == nil {
		var doc struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal([]byte(out), &doc)
		r.report = doc.Body
	}
	return r
}

// sty_c356ed4f AC2: a review is an execution of the embedded substrate-review
// task, in progress through the execution route before anything is judged, with
// each stage on its ledger and its report attached, closed done through the
// route's gate.
func TestSubstrateReviewRunsAsAnExecutionOfItsTask(t *testing.T) {
	substrateReviewFixture(t)
	stubSubstrateJudge(t, substratereview.Judgement{})
	report := filepath.Join(t.TempDir(), "report.json")
	out, err := runRoot(t, "substrate", "review", "--out", report)
	if err != nil {
		t.Fatalf("substrate review: %v\n%s", err, out)
	}
	r := onlyReviewRun(t)
	if r.item.Kind != workitem.KindExecution || r.item.ParentID != substrateReviewTask || r.item.Status != workitem.StatusDone {
		t.Fatalf("the run is %s %s of %q, want a done execution of %s", r.item.Status, r.item.Kind, r.item.ParentID, substrateReviewTask)
	}
	if !strings.Contains(r.item.Body, "embedded:tasks/"+substrateReviewTask+".md") {
		t.Errorf("the run does not name the embedded task it ran: %q", r.item.Body)
	}
	if strings.Join(r.moves, ",") != "backlog→in_progress,in_progress→done" {
		t.Errorf("moves = %v, want backlog→in_progress then in_progress→done", r.moves)
	}
	rep, err := substratereview.ReadReport(report)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"move:in_progress", "stage:gather"}
	for i := 0; i < rep.Batches; i++ {
		want = append(want, "stage:judge")
	}
	want = append(want, "stage:report", "move:done")
	if strings.Join(r.order, ",") != strings.Join(want, ",") {
		t.Errorf("run ledger = %v, want %v", r.order, want)
	}
	if rep.Batches == 0 || r.stages[1]["batch"] != fmt.Sprintf("1/%d", rep.Batches) {
		t.Errorf("the first judge stage = %v over %d batches", r.stages[1], rep.Batches)
	}
	if fmt.Sprint(r.stages[0]["items"]) != fmt.Sprint(len(rep.Items)) {
		t.Errorf("gather stage = %v, want %d items", r.stages[0], len(rep.Items))
	}
	if rep.ExecutionID != r.item.ID {
		t.Errorf("the report names execution %q, want %s", rep.ExecutionID, r.item.ID)
	}
	saved, _ := os.ReadFile(report)
	if !strings.Contains(r.report, strings.TrimSpace(string(saved))) {
		t.Errorf("the run's attachment is not the JSON report:\n%s", r.report)
	}
}

// sty_c356ed4f AC2: a review that fails ends its execution cancelled, with the
// reason as the last stage on its ledger, so it leaves the Activity tab.
func TestSubstrateReviewFailureCancelsItsExecution(t *testing.T) {
	substrateReviewFixture(t)
	prev := newSubstrateJudge
	t.Cleanup(func() { newSubstrateJudge = prev })
	newSubstrateJudge = func(*cobra.Command, string, substratereview.History) (substrateJudge, error) {
		return func(context.Context, substratereview.JudgeInput) (substratereview.Judgement, string, error) {
			return substratereview.Judgement{}, "garbled", errors.New("the judge broke")
		}, nil
	}
	out, err := runRoot(t, "substrate", "review", "--out", filepath.Join(t.TempDir(), "report.json"))
	if err == nil {
		t.Fatalf("a review whose every batch fails must fail:\n%s", out)
	}
	r := onlyReviewRun(t)
	if r.item.Status != workitem.StatusCancelled {
		t.Fatalf("the failed run is %s, want cancelled", r.item.Status)
	}
	if got := r.moves[len(r.moves)-1]; got != "in_progress→cancelled" {
		t.Errorf("last move = %s, want in_progress→cancelled", got)
	}
	last := r.stages[len(r.stages)-1]
	if last["stage"] != workitem.StatusCancelled || !strings.Contains(fmt.Sprint(last["reason"]), "returned no judgement") {
		t.Errorf("the cancel does not record the reason: %v", last)
	}
}

// sty_c356ed4f AC2: a repo's own tsk_substrate-review overrides the embedded
// default, and the review runs as an execution of that copy.
func TestSubstrateReviewRunsTheRepoOverrideOfItsTask(t *testing.T) {
	repo := substrateReviewFixture(t)
	rel := filepath.Join(config.DefaultDataDir, "tasks", substrateReviewTask+".md")
	writeRepoFile(t, repo, rel, "---\nid: "+substrateReviewTask+"\ntype: task\nstatus: done\n---\n\n# Our substrate review\n\n## Action\nRun it.\n\n## Verification\nThe report is attached.\n")
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	stubSubstrateJudge(t, substratereview.Judgement{})
	if out, err := runRoot(t, "substrate", "review", "--out", filepath.Join(t.TempDir(), "report.json")); err != nil {
		t.Fatalf("substrate review: %v\n%s", err, out)
	}
	r := onlyReviewRun(t)
	if r.item.ParentID != substrateReviewTask || r.item.Status != workitem.StatusDone {
		t.Fatalf("the run is %s of %q", r.item.Status, r.item.ParentID)
	}
	if !strings.Contains(r.item.Body, filepath.Join(repo, rel)) {
		t.Errorf("the run does not name the repo's copy of the task: %q", r.item.Body)
	}
}

// sty_c356ed4f AC3: a cut applied from a review, or from its saved report, is a
// substrate story naming the execution it came from.
func TestSubstrateReviewAppliedCutNamesItsExecution(t *testing.T) {
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
func TestSubstrateReviewRefusedCloseCancelsItsExecution(t *testing.T) {
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
