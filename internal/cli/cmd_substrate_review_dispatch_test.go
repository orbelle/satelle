package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/reviewscore"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/substratereview"
)

// judgeDispatch is one request the fake judge runner received, with the
// manifest it read through the payload's document path.
type judgeDispatch struct {
	req      agentcli.Request
	manifest substratereview.Manifest
}

// fakeJudgeRunner stands in for the judging agent CLI on the real
// newSubstrateJudge → CaseJudge → Engine path: it records every assembled
// request and the (iface, command) each runner was built from, reads the
// manifest the way a judge with a read grant would, and answers with answer.
type fakeJudgeRunner struct {
	mu       sync.Mutex
	built    [][2]string
	sent     []judgeDispatch
	readErrs []string
	answer   func(m substratereview.Manifest) string
}

func (r *fakeJudgeRunner) Name() string    { return "fake" }
func (r *fakeJudgeRunner) Command() string { return "fake" }

func (r *fakeJudgeRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	var top struct {
		Docs []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"docs"`
	}
	var m substratereview.Manifest
	_ = json.Unmarshal([]byte(req.Payload), &top)
	for _, d := range top.Docs {
		if d.Name != substrateManifestDoc {
			continue
		}
		b, err := os.ReadFile(d.Path)
		if err == nil {
			err = json.Unmarshal(b, &m)
		}
		if err != nil {
			r.mu.Lock()
			r.readErrs = append(r.readErrs, err.Error())
			r.mu.Unlock()
		}
	}
	r.mu.Lock()
	r.sent = append(r.sent, judgeDispatch{req: req, manifest: m})
	r.mu.Unlock()
	return []byte(verdictJSON(r.answer(m))), nil
}

// isolateSubstrateScratch keeps the review's scratch files in the test's temp
// dir.
func isolateSubstrateScratch(t *testing.T) {
	t.Helper()
	prev := substrateScratchRoot
	substrateScratchRoot = t.TempDir()
	t.Cleanup(func() { substrateScratchRoot = prev })
}

// verdictJSON is the accept verdict a judge returns with notes as its text.
func verdictJSON(notes string) string {
	b, _ := json.Marshal(map[string]string{"decision": "accept", "notes": notes})
	return string(b)
}

// installFakeJudge routes every case judge's runner to a fake answering with
// answer.
func installFakeJudge(t *testing.T, answer func(m substratereview.Manifest) string) *fakeJudgeRunner {
	t.Helper()
	r := &fakeJudgeRunner{answer: answer}
	prev := caseJudgeRunner
	t.Cleanup(func() { caseJudgeRunner = prev })
	caseJudgeRunner = func(iface, command string) (agentcli.Runner, error) {
		r.mu.Lock()
		r.built = append(r.built, [2]string{iface, command})
		r.mu.Unlock()
		return r, nil
	}
	return r
}

// keepAll judges every manifest item keep, with a reason and evidence, and
// every rule "none".
func keepAll(m substratereview.Manifest) string {
	return judgementFor(m, nil)
}

// judgementFor judges every item of m: the disposition in want, else keep;
// every rule none.
func judgementFor(m substratereview.Manifest, want map[string]substratereview.ItemJudgement) string {
	var j substratereview.Judgement
	for _, it := range m.Items {
		ij, ok := want[it.Item]
		if !ok {
			ij = substratereview.ItemJudgement{Disposition: "keep", Rule: "repo-specific", Reason: "no default carries it", Evidence: it.Path + ":1"}
		}
		ij.Item = it.Item
		j.Items = append(j.Items, ij)
	}
	for _, r := range m.Rules {
		j.Constitution = append(j.Constitution, substratereview.RuleFinding{Rule: r.Name, None: true})
	}
	b, _ := json.Marshal(j)
	return string(b)
}

// appendLedgerRow writes one ledger row on story.
func appendLedgerRow(t *testing.T, story, kind string, payload map[string]any) {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b, _ := json.Marshal(payload)
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{StoryID: story, Kind: kind, Actor: "test", Body: kind, Payload: b}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// recordInLoop records an in-loop session_model row naming agent and model.
func recordInLoop(t *testing.T, story, agent, model string) {
	t.Helper()
	appendLedgerRow(t, story, ledger.KindSessionModel, map[string]any{"role": "in-loop", "agent": agent, "model": model})
}

// setConfig rewrites the repo's satelle.toml.
func setConfig(t *testing.T, repo, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "satelle.toml"), []byte("[review]\ngate_create = false\n\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	bodySentinel      = "BODY-SENTINEL-7f3a"
	rejectionSentinel = "REJECTION-SENTINEL-91c2"
)

// manifestOnlyFixture is a repo holding a 200 KB principle, a repo skill with a
// rejection whose notes carry a sentinel, and an in-loop session_model row.
func manifestOnlyFixture(t *testing.T) (repo, story string) {
	t.Helper()
	repo = tempRepo(t)
	isolateSubstrateScratch(t)
	runValidatorsDropped(t, repo)
	big := "---\nname: huge\ntype: principle\ndescription: huge\n---\n# huge\n\n" + bodySentinel + "\n" + strings.Repeat("filler line of the principle body.\n", 200*1024/35)
	writeRepoFile(t, repo, ".satelle/principles/huge.md", big)
	writeRepoFile(t, repo, ".satelle/skills/repo-cut.md", "---\ntype: skill\nname: repo-cut\ndescription: a repo skill\n---\n# repo cut\n")
	writeRepoFile(t, repo, ".satelle/constitution.md", "# constitution\n\n- [[huge]]\n")
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	story = createStory(t, "History")
	appendLedgerRow(t, story, ledger.KindReviewReject, map[string]any{"from": "backlog", "to": "plan", "skill": "repo-cut", "attempt": "att_x", "notes": rejectionSentinel, "model_resolved": "M1"})
	recordInLoop(t, story, "claude", "claude-opus-5-5")
	return repo, story
}

// AC1: every dispatch carries the instruction and a manifest of pointers — no
// item body, no rejection text — one per area, never split.
func TestSubstrateReviewDispatchCarriesManifestOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"default judge", nil},
		{"--binding reviewer", []string{"--binding", "reviewer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, story := manifestOnlyFixture(t)
			r := installFakeJudge(t, keepAll)
			report := filepath.Join(t.TempDir(), "report.json")
			out, err := runRoot(t, append([]string{"substrate", "review", "--out", report}, tc.args...)...)
			if err != nil {
				t.Fatalf("substrate review: %v\n%s", err, out)
			}
			if len(r.readErrs) > 0 {
				t.Fatalf("the judge could not read its manifest: %v", r.readErrs)
			}
			areas := map[string]int{}
			for _, d := range r.sent {
				areas[d.manifest.Area]++
				whole := d.req.SystemPrompt + d.req.Payload
				for _, s := range []string{bodySentinel, rejectionSentinel} {
					if strings.Contains(whole, s) {
						t.Errorf("the %s dispatch carries %s", d.manifest.Area, s)
					}
				}
				raw, _ := json.Marshal(d.manifest)
				var generic map[string]any
				_ = json.Unmarshal(raw, &generic)
				walkStrings(generic, func(v string) {
					if strings.HasPrefix(v, "satelle ") {
						t.Errorf("the manifest carries a command pointer %q", v)
					}
				})
				for _, it := range d.manifest.Items {
					if it.Stories == nil {
						t.Errorf("%s has no stories field", it.Item)
					}
					if it.Path == "" || it.HistoryPath == "" {
						t.Errorf("%s lacks a path (path %q, history_path %q)", it.Item, it.Path, it.HistoryPath)
					}
					for _, p := range []string{it.Path, it.HistoryPath, it.DefaultPath} {
						if p == "" {
							continue
						}
						if !filepath.IsAbs(p) {
							t.Errorf("%s path %s is not absolute", it.Item, p)
						}
						if _, err := os.ReadFile(p); err != nil {
							t.Errorf("%s path %s is not readable: %v", it.Item, p, err)
						}
					}
					if it.Item == "skills/repo-cut" {
						b, _ := os.ReadFile(it.HistoryPath)
						if !strings.Contains(string(b), rejectionSentinel) {
							t.Errorf("the repo-cut history file lacks the rejection:\n%s", b)
						}
						found := false
						for _, s := range it.Stories {
							found = found || s == story
						}
						if !found {
							t.Errorf("repo-cut stories = %v, want %s", it.Stories, story)
						}
					}
					if it.Item == "principles/huge" {
						b, _ := os.ReadFile(it.Path)
						if !strings.Contains(string(b), bodySentinel) {
							t.Errorf("the huge principle's path does not hold its body")
						}
					}
				}
			}
			for _, a := range substratereview.Areas {
				if areas[a] != 1 {
					t.Errorf("area %s dispatched %d time(s), want 1 (all: %v)", a, areas[a], areas)
				}
			}
			if strings.Contains(out, "over budget") || strings.Contains(out, "returned no judgement") {
				t.Errorf("a dispatch was refused:\n%s", out)
			}
		})
	}

	// sty_58a7bdfe AC4: no dispatch is refused against a budget — the same seat
	// handed the 200 KB body in its payload runs and receives it whole.
	t.Run("embedded body is sent whole", func(t *testing.T) {
		manifestOnlyFixture(t)
		r := installFakeJudge(t, keepAll)
		a, err := app.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		j, err := newCaseJudge(a, "reviewer", nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := os.ReadFile(filepath.Join(a.RepoRoot, ".satelle", "principles", "huge.md"))
		res, err := j.Judge(context.Background(), reviewscore.Case{
			ID: "embedded", Skill: substrateReviewSkill, StoryID: "substrate-review", From: "backlog", To: "plan",
			Payload: &reviewscore.ReplayPayload{Title: "embedded", Body: string(body), NoPatch: true},
		})
		if err != nil || strings.Contains(res.Notes, "over budget") {
			t.Fatalf("an embedded body must not be refused: %v %s", err, res.Notes)
		}
		if len(r.sent) != 1 || !strings.Contains(r.sent[0].req.Payload, bodySentinel) {
			t.Errorf("the dispatch must reach the runner carrying the body, sent %d", len(r.sent))
		}
	})
}

// walkStrings calls f on every string value in v.
func walkStrings(v any, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case []any:
		for _, e := range x {
			walkStrings(e, f)
		}
	case map[string]any:
		for _, e := range x {
			walkStrings(e, f)
		}
	}
}

// AC5: with no --binding the judge is the in-loop model on the reviewer seat,
// through the in-loop agent's preset command; --binding reaches the configured
// seat unchanged; no recorded in-loop model, or an agent with no preset, stops
// before any dispatch.
func TestSubstrateReviewDefaultJudgeIsOrchestratorModel(t *testing.T) {
	const cursorCmd = "cursor-agent -p --output-format json --model {model}"
	fixture := func(t *testing.T, agent string) {
		t.Helper()
		repo := tempRepo(t)
		isolateSubstrateScratch(t)
		runValidatorsDropped(t, repo)
		writeRepoFile(t, repo, ".satelle/agents.toml", "[executor]\nharness = \"in-loop\"\n\n[reviewer]\ninterface = \"command\"\ncommand = \""+cursorCmd+"\"\nmodel = \"cursor/x\"\n")
		if _, err := config.LoadAgents(filepath.Join(repo, config.DefaultDataDir)); err != nil {
			t.Fatalf("the fixture agents.toml does not load: %v", err)
		}
		if out, err := runRoot(t, "reindex"); err != nil {
			t.Fatalf("reindex: %v\n%s", err, out)
		}
		if agent != "" {
			recordInLoop(t, createStory(t, "s"), agent, "claude-opus-5-5")
		}
	}
	run := func(t *testing.T, args ...string) (*fakeJudgeRunner, string, error) {
		t.Helper()
		r := installFakeJudge(t, keepAll)
		out, err := runRoot(t, append([]string{"substrate", "review", "--out", filepath.Join(t.TempDir(), "r.json")}, args...)...)
		return r, out, err
	}
	check := func(t *testing.T, r *fakeJudgeRunner, iface, command, model string) {
		t.Helper()
		if len(r.sent) == 0 {
			t.Fatal("no dispatch reached the runner")
		}
		for _, b := range r.built {
			if b[0] != iface || b[1] != command {
				t.Errorf("runner built from (%q, %q), want (%q, %q)", b[0], b[1], iface, command)
			}
		}
		for _, d := range r.sent {
			if d.req.Model != model {
				t.Errorf("dispatched model %q, want %q", d.req.Model, model)
			}
			if d.req.AllowedTools != config.DefaultReviewerTools {
				t.Errorf("dispatched tools %q, want %q", d.req.AllowedTools, config.DefaultReviewerTools)
			}
		}
		if _, err := agentcli.RunnerFromBinding(iface, command); err != nil {
			t.Errorf("the resolved binding (%q, %q) is not runnable: %v", iface, command, err)
		}
	}

	t.Run("default is the in-loop claude model", func(t *testing.T) {
		fixture(t, "claude")
		r, out, err := run(t)
		if err != nil {
			t.Fatalf("substrate review: %v\n%s", err, out)
		}
		if strings.Contains(out, "want role=reviewer") {
			t.Errorf("the gate refused the seat:\n%s", out)
		}
		check(t, r, config.InterfaceCommand, agentcli.DefaultClaudeCommand, "claude-opus-5-5")
		if !strings.Contains(out, "Judged by binding: orchestrator:claude/claude-opus-5-5") {
			t.Errorf("the report does not name the default judge:\n%s", out)
		}
	})
	t.Run("default is the in-loop grok model", func(t *testing.T) {
		fixture(t, "grok")
		r, out, err := run(t)
		if err != nil {
			t.Fatalf("substrate review: %v\n%s", err, out)
		}
		check(t, r, config.InterfaceCommand, agentcli.DefaultGrokCommand, "claude-opus-5-5")
	})
	t.Run("--binding reviewer reaches the configured seat", func(t *testing.T) {
		fixture(t, "claude")
		r, out, err := run(t, "--binding", "reviewer")
		if err != nil {
			t.Fatalf("substrate review: %v\n%s", err, out)
		}
		check(t, r, config.InterfaceCommand, cursorCmd, "cursor/x")
	})
	for _, agent := range []string{"", "pi"} {
		t.Run("no one-shot judge for agent "+agent, func(t *testing.T) {
			fixture(t, agent)
			r, out, err := run(t)
			if err == nil || !strings.Contains(err.Error()+out, "pass --binding") {
				t.Errorf("want a stop naming --binding, got %v\n%s", err, out)
			}
			if len(r.sent) != 0 || len(r.built) != 0 {
				t.Errorf("the runner was called: built %v, sent %d", r.built, len(r.sent))
			}
		})
	}
}

// AC4: one item of each kind gets its expected disposition in the report.
func TestSubstrateReviewFixtureDispositions(t *testing.T) {
	repo := tempRepo(t)
	isolateSubstrateScratch(t)
	runValidatorsDropped(t, repo)
	startSmall, _ := os.ReadFile(filepath.Join("..", "config", "substrate", "principles", "satelle-start-small.md"))
	writeRepoFile(t, repo, ".satelle/principles/start-small-copy.md", strings.Replace(string(startSmall), "name: satelle-start-small", "name: start-small-copy", 1))
	writeRepoFile(t, repo, ".satelle/skills/orphan-skill.md", "---\ntype: skill\nname: orphan-skill\ndescription: nothing reads it\n---\n# orphan\n")
	writeRepoFile(t, repo, ".satelle/principles/history-narrator.md", "---\nname: history-narrator\ntype: principle\ndescription: narrates\n---\nWe used to gate every edit; sty_1 then sty_2 changed it. Keep edits small.\n")
	writeRepoFile(t, repo, ".satelle/skills/methodology.md", "---\ntype: skill\nname: methodology\ndescription: describes BDD\n---\nBDD is a methodology in which teams describe behaviour. Judge the AC.\n")
	writeRepoFile(t, repo, ".satelle/principles/flag-claim.md", "---\nname: flag-claim\ntype: principle\ndescription: claims a flag\n---\nRun `tool --fast`.\n")
	writeRepoFile(t, repo, "cmd/tool.go", "package main\n\n// tool has no --fast flag.\nfunc main() {}\n")
	writeRepoFile(t, repo, ".satelle/tasks/tsk_repo-audit.md", "---\ntype: task\nname: tsk_repo-audit\ndescription: audits this repo's vendored schema\n---\nCheck schema/v3 against the vendored copy.\n")
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	recordInLoop(t, createStory(t, "s"), "claude", "claude-opus-5-5")

	want := map[string]substratereview.ItemJudgement{
		"principles/start-small-copy": {Disposition: "remove", Reason: "the embedded default says the same", Evidence: "principles/satelle-start-small"},
		"skills/orphan-skill":         {Disposition: "remove", Reason: "nothing reads it", Evidence: "grep -r orphan-skill: no reader"},
		"principles/history-narrator": {Disposition: "reduce", Reason: "narrates history", Evidence: "history-narrator.md:5", Replacement: "Keep edits small.\n"},
		"skills/methodology":          {Disposition: "reduce", Reason: "describes a methodology", Evidence: "methodology.md:5", Replacement: "Judge the AC.\n"},
		"principles/flag-claim":       {Disposition: "conflict", Reason: "the flag does not exist", Evidence: "cmd/tool.go:3"},
		"tasks/tsk_repo-audit":        {Disposition: "keep", Rule: "audits the vendored schema", Reason: "repo-specific", Evidence: "tsk_repo-audit.md:5"},
	}
	installFakeJudge(t, func(m substratereview.Manifest) string { return judgementFor(m, want) })
	report := filepath.Join(t.TempDir(), "r.json")
	out, err := runRoot(t, "substrate", "review", "--out", report)
	if err != nil {
		t.Fatalf("substrate review: %v\n%s", err, out)
	}
	rep, err := substratereview.ReadReport(report)
	if err != nil {
		t.Fatal(err)
	}
	for key, w := range want {
		it, ok := rep.Find(key)
		if !ok {
			t.Errorf("the report holds no %s", key)
			continue
		}
		if it.Disposition != w.Disposition || it.Citation != w.Evidence || it.Replacement != w.Replacement {
			t.Errorf("%s = %s (evidence %q, replacement %q), want %s", key, it.Disposition, it.Citation, it.Replacement, w.Disposition)
		}
		if !strings.Contains(out, "### "+key+" — "+w.Disposition) {
			t.Errorf("the rendered report lacks %s — %s", key, w.Disposition)
		}
	}
}

// AC7: an edited copy of the default step catalogue is judged against the
// current embedded default, and each improvement the copy lacks is reported.
func TestEditedItemJudgedAgainstCurrentDefault(t *testing.T) {
	repo := tempRepo(t)
	isolateSubstrateScratch(t)
	if out, err := runRoot(t, "substrate", "edit", "workflows", "step"); err != nil {
		t.Fatalf("substrate edit: %v\n%s", err, out)
	}
	stepPath := filepath.Join(repo, ".satelle", "workflows", "step.toml")
	b, err := os.ReadFile(stepPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	dropped := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "# ") {
			dropped = i
			break
		}
	}
	if dropped < 0 {
		t.Fatal("the default step catalogue has no comment line to drop")
	}
	if err := os.WriteFile(stepPath, []byte(strings.Join(append(lines[:dropped:dropped], lines[dropped+1:]...), "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	runValidatorsDropped(t, repo)
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	recordInLoop(t, createStory(t, "s"), "claude", "claude-opus-5-5")

	var embedded string
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "workflows" && d.Name == "step" {
			embedded = d.Body
		}
	}
	const missed = "the default's comment on step identity"
	var target string
	var defaultBodies []string
	r := installFakeJudge(t, func(m substratereview.Manifest) string {
		want := map[string]substratereview.ItemJudgement{}
		for _, it := range m.Items {
			if it.Kind != substratereview.KindStep || it.Origin != substratereview.OriginEdited {
				continue
			}
			db, _ := os.ReadFile(it.DefaultPath)
			defaultBodies = append(defaultBodies, string(db))
			if target == "" {
				target = it.Item
				want[it.Item] = substratereview.ItemJudgement{Disposition: "keep", Rule: "r", Reason: "edited copy", Evidence: it.Path + ":1", MissedDefaults: []string{missed}}
			}
		}
		return judgementFor(m, want)
	})
	report := filepath.Join(t.TempDir(), "r.json")
	out, err := runRoot(t, "substrate", "review", "--out", report)
	if err != nil {
		t.Fatalf("substrate review: %v\n%s", err, out)
	}
	if len(r.sent) == 0 || target == "" {
		t.Fatalf("no edited workflow step reached the judge:\n%s", out)
	}
	for _, db := range defaultBodies {
		if db != embedded {
			t.Errorf("default_path does not hold the current embedded step catalogue")
		}
	}
	if !strings.Contains(out, "- default improvement not carried: "+missed) {
		t.Errorf("the rendered report does not name the missed default:\n%s", out)
	}
	rep, err := substratereview.ReadReport(report)
	if err != nil {
		t.Fatal(err)
	}
	if it, _ := rep.Find(target); len(it.MissedDefaults) != 1 || it.MissedDefaults[0] != missed {
		t.Errorf("the JSON report's %s missed_defaults = %v", target, it.MissedDefaults)
	}
}

// AC8: a batch whose answer does not parse keeps the raw answer beside the
// report and reads not judged; every other batch is still reported.
func TestSubstrateReviewMalformedBatch(t *testing.T) {
	repo := tempRepo(t)
	isolateSubstrateScratch(t)
	runValidatorsDropped(t, repo)
	writeRepoFile(t, repo, ".satelle/skills/repo-cut.md", "---\ntype: skill\nname: repo-cut\ndescription: a repo skill\n---\n# repo cut\n")
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	recordInLoop(t, createStory(t, "s"), "claude", "claude-opus-5-5")
	installFakeJudge(t, func(m substratereview.Manifest) string {
		if m.Area == substratereview.AreaSkills {
			return "not json"
		}
		return keepAll(m)
	})
	report := filepath.Join(t.TempDir(), "r.json")
	out, err := runRoot(t, "substrate", "review", "--out", report)
	if err != nil {
		t.Fatalf("one malformed batch must not fail the review: %v\n%s", err, out)
	}
	raw := report + ".raw-skills.txt"
	b, err := os.ReadFile(raw)
	if err != nil || !strings.Contains(string(b), "not json") {
		t.Fatalf("the raw answer was not kept at %s: %v %q", raw, err, b)
	}
	if !strings.Contains(out, raw) {
		t.Errorf("stderr does not name %s:\n%s", raw, out)
	}
	rep, err := substratereview.ReadReport(report)
	if err != nil {
		t.Fatalf("the report was not written: %v", err)
	}
	var skills, judged int
	for _, it := range rep.Items {
		switch {
		case it.Kind == substratereview.KindSkill:
			skills++
			if it.Disposition != substratereview.NotJudged {
				t.Errorf("%s = %s, want not judged", it.Key, it.Disposition)
			}
		case it.Disposition == substratereview.DispKeep:
			judged++
		default:
			t.Errorf("%s = %s, want keep", it.Key, it.Disposition)
		}
	}
	if skills == 0 || judged == 0 {
		t.Errorf("want skills not judged and the rest judged: %d skills, %d judged", skills, judged)
	}
	if len(rep.Failures) != 1 || rep.Failures[0].Area != substratereview.AreaSkills || rep.Failures[0].RawPath != raw {
		t.Errorf("failures = %+v", rep.Failures)
	}
}

// AC2/AC4: `satelle review score --replay` scores the substrate-review cases
// by their dispositions through the real judge path: a judge returning the
// right disposition for each item has no disposition miss, a keep-everything
// judge misses every case that expects a cut.
func TestReviewScoreSubstrateReplayCases(t *testing.T) {
	right := map[string]substratereview.ItemJudgement{
		"principles/satelle-start-small":  {Disposition: "remove", Reason: "identical to the default", Evidence: "default_path"},
		"skills/orphan-greeting-review":   {Disposition: "remove", Reason: "nothing reads it", Evidence: "grep: no reader"},
		"principles/edit-small":           {Disposition: "reduce", Reason: "narrates history", Evidence: "edit-small.md:7", Replacement: "Keep each commit under 400 changed lines.\n"},
		"skills/ac-judge":                 {Disposition: "reduce", Reason: "describes BDD", Evidence: "ac-judge.md:8", Replacement: "Reject the change when any acceptance criterion has no test that proves it.\n"},
		"principles/fast-builds":          {Disposition: "conflict", Reason: "no --fast flag", Evidence: "buildtool.go:5"},
		"tasks/tsk_vendored-schema-audit": {Disposition: "keep", Rule: "vendored schema", Reason: "repo-specific", Evidence: "tsk_vendored-schema-audit.md:6"},
	}
	replay, err := filepath.Abs(filepath.Join("..", "substratereview", "testdata", "replay"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		answer func(substratereview.Manifest) string
		misses int
	}{
		{"right dispositions", func(m substratereview.Manifest) string { return judgementFor(m, right) }, 0},
		{"keep everything", keepAll, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempRepo(t)
			r := installFakeJudge(t, tc.answer)
			report := filepath.Join(t.TempDir(), "score.json")
			out, err := runRoot(t, "review", "score", "--replay", replay, "--binding", "reviewer", "--out", report)
			if err != nil {
				t.Fatalf("review score: %v\n%s", err, out)
			}
			if len(r.readErrs) > 0 || len(r.sent) != 7 {
				t.Fatalf("the judge read %d manifest(s), errors %v", len(r.sent), r.readErrs)
			}
			b, err := os.ReadFile(report)
			if err != nil {
				t.Fatal(err)
			}
			var rep reviewscore.Report
			if err := json.Unmarshal(b, &rep); err != nil {
				t.Fatal(err)
			}
			if rep.Total.DispositionMisses != tc.misses || rep.Total.Rows != 7 {
				t.Errorf("disposition misses = %d over %d row(s), want %d over 7:\n%s", rep.Total.DispositionMisses, rep.Total.Rows, tc.misses, out)
			}
		})
	}
}
