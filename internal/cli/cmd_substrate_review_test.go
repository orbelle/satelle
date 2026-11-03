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
	return substrateReviewFixtureGate(t, false)
}

// substrateReviewFixtureGate is substrateReviewFixture with the repo's
// [review] gate_create set to gateCreate once the fixture's own story exists.
func substrateReviewFixtureGate(t *testing.T, gateCreate bool) string {
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
	writeCfg := func(gate bool) {
		t.Helper()
		if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("[review]\ngate_create = %t\n\n[hosted]\nproject = \"team-p\"\n", gate)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeCfg(false)
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
	if gateCreate {
		writeCfg(true)
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

// sty_5ed358e0 AC1: with the create gate on, the review's execution is created
// with the fixed run category, so the review runs to its report and ends done.
func TestSubstrateReviewRunsWithCreateGateOn(t *testing.T) {
	substrateReviewFixtureGate(t, true)
	stubSubstrateJudge(t, substratereview.Judgement{})
	report := filepath.Join(t.TempDir(), "report.json")
	out, err := runRoot(t, "substrate", "review", "--out", report)
	if err != nil {
		t.Fatalf("substrate review with the create gate on: %v\n%s", err, out)
	}
	if _, err := os.Stat(report); err != nil {
		t.Fatalf("the JSON report was not written: %v", err)
	}
	r := onlyReviewRun(t)
	if r.item.Status != workitem.StatusDone || r.item.Category != workitem.RunCategory {
		t.Fatalf("the run is %s with category %q, want done with %q", r.item.Status, r.item.Category, workitem.RunCategory)
	}
}

// sty_5ed358e0 AC2: a create-gate rejection of the review's execution fails the
// command with the gate's notes, and no report or execution is left behind.
func TestSubstrateReviewCreateRejectionIsReported(t *testing.T) {
	substrateReviewFixtureGate(t, true)
	prev := substrateReviewCategory
	t.Cleanup(func() { substrateReviewCategory = prev })
	substrateReviewCategory = ""
	stubSubstrateJudge(t, substratereview.Judgement{})
	report := filepath.Join(t.TempDir(), "report.json")
	out, err := runRoot(t, "substrate", "review", "--out", report)
	if err == nil {
		t.Fatalf("a review whose execution the create gate rejects must fail:\n%s", out)
	}
	for _, want := range []string{"create the run's execution", "category is empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if _, serr := os.Stat(report); !os.IsNotExist(serr) {
		t.Errorf("a rejected review wrote a report: %v", serr)
	}
	out, lerr := runRoot(t, "execution", "list", "--parent", substrateReviewTask)
	if lerr != nil {
		t.Fatalf("execution list: %v\n%s", lerr, out)
	}
	var runs []workitem.Item
	if err := json.Unmarshal([]byte(out), &runs); err != nil {
		t.Fatalf("parse execution list: %v\n%s", err, out)
	}
	if len(runs) != 0 {
		t.Errorf("%d executions exist after a rejected create, want 0:\n%s", len(runs), out)
	}
}
