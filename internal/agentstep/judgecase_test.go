package agentstep

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/retrieve"
	"github.com/bobmcallan/satelle/internal/reviewscore"
	"github.com/bobmcallan/satelle/internal/verb"
)

// stubJudge is a CaseJudge over a stub runner and a stub binding: no agents.toml,
// no agent CLI, no model.
func stubJudge(t *testing.T, out string) (*CaseJudge, *fakeRunner) {
	t.Helper()
	r := &fakeRunner{out: out}
	j, err := NewCaseJudge(CaseJudgeOptions{
		Root:    t.TempDir(),
		Binding: "reviewer",
		Docs:    fakeDocs{skillFound: true, skillBody: "rubric"},
		Resolve: func(name string) (config.AgentBinding, bool) {
			return config.AgentBinding{Role: "reviewer", Command: "fake -p --append-system-prompt {system}"}, name == "reviewer"
		},
		NewRunner: func(string, string) (agentcli.Runner, error) { return r, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return j, r
}

func TestJudgeCaseCorpusCaseSendsTheDiffAndReturnsTheVerdict(t *testing.T) {
	j, r := stubJudge(t, `{"decision":"reject","notes":"the diff drops the retry"}`)
	res, err := j.Judge(context.Background(), reviewscore.Case{
		ID: "c1", Skill: "satelle-code-ac-review", StoryID: "sty_c1", Diff: "--- a/x\n+++ b/x\n+DIFF-MARKER\n",
		ExpectedVerdict: reviewscore.VerdictReject,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != reviewscore.VerdictReject || !strings.Contains(res.Notes, "drops the retry") {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(r.got.Payload, `"diff"`) {
		t.Errorf("a corpus case must send its diff:\n%s", r.got.Payload)
	}
	if res.Usage.Available || !strings.Contains(res.Usage.Reason, "unavailable") {
		t.Errorf("a stub that reports no usage must say so, got %+v", res.Usage)
	}
}

// The scored seat is built exactly as a dispatched one: it receives its
// resident principles whole, never condensed (sty_58a7bdfe AC5).
func TestJudgeCaseSeatReceivesPrinciplesWhole(t *testing.T) {
	principles := []docindex.Doc{seatPrinciple(config.OperatingPrinciple, 12), seatPrinciple("p-two", 10)}
	r := &fakeRunner{out: `{"decision":"accept","notes":"ok"}`}
	j, err := NewCaseJudge(CaseJudgeOptions{
		Root:    t.TempDir(),
		Binding: "reviewer",
		Docs:    fakeDocs{skillFound: true, skillBody: "rubric", extraPrinciples: principles},
		Resolve: func(name string) (config.AgentBinding, bool) {
			return config.AgentBinding{Role: "reviewer", Command: "fake -p --append-system-prompt {system}"}, name == "reviewer"
		},
		NewRunner: func(string, string) (agentcli.Runner, error) { return r, nil },
		Offload:   func(_ context.Context, _ string, c []byte) (string, error) { return retrieve.Hash(c), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Judge(context.Background(), reviewscore.Case{
		ID: "c1", Skill: "satelle-code-ac-review", StoryID: "sty_c1", Diff: "+x\n", ExpectedVerdict: reviewscore.VerdictAccept,
	}); err != nil {
		t.Fatal(err)
	}
	got := r.got.SystemPrompt
	if strings.Contains(got, "Retrieve more") || strings.Contains(got, "OMITTED") {
		t.Errorf("the judging seat's principles must not be condensed or pointered:\n%s", got)
	}
	for _, d := range principles {
		if body := strings.TrimSpace(stripFrontmatter(d.Body)); !strings.Contains(got, body) {
			t.Errorf("principle %s not delivered whole", d.Name)
		}
	}
}

func TestJudgeCaseReplayDocsReachThePayloadThroughTheDocsResolver(t *testing.T) {
	j, r := stubJudge(t, `{"decision":"accept","notes":"ok"}`)
	res, err := j.Judge(context.Background(), reviewscore.Case{
		ID: "sty_r-evt_1", Skill: "satelle-story-plan-review", StoryID: "sty_r", From: "plan", To: "in_progress",
		ExpectedVerdict: reviewscore.VerdictAccept, PayloadSource: reviewscore.PayloadReconstructed,
		Payload: &reviewscore.ReplayPayload{
			Title: "REPLAY-TITLE", Body: "replay body", AcceptanceCriteria: "1. REPLAY-AC",
			Docs: []reviewscore.ReplayDoc{{Name: "plan", Type: "plan", Body: "# replay plan body"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != reviewscore.VerdictAccept {
		t.Fatalf("verdict = %q", res.Verdict)
	}
	for _, want := range []string{"REPLAY-TITLE", "REPLAY-AC", `"name":"plan"`} {
		if !strings.Contains(r.got.Payload, want) {
			t.Errorf("replay payload missing %q:\n%s", want, r.got.Payload)
		}
	}
	if docs := openedDocs(t, r.got.Payload, r.opened); docs["plan"] != "# replay plan body" {
		t.Errorf("replay doc files = %#v", docs)
	}
	if strings.Contains(r.got.Payload, `"diff"`) {
		t.Errorf("a replay case has no diff:\n%s", r.got.Payload)
	}
}

// A replay carries the change its recorded reviewer was shown; the reviewer under
// test must see that change, not an edge with no code to judge.
func TestJudgeCaseReplayPatchReachesTheReviewerAsTheDiff(t *testing.T) {
	j, r := stubJudge(t, `{"decision":"reject","notes":"the patch drops the retry"}`)
	_, err := j.Judge(context.Background(), reviewscore.Case{
		ID: "sty_r-evt_2", Skill: "satelle-code-ac-review", StoryID: "sty_r", From: "in_progress", To: "integration",
		ExpectedVerdict: reviewscore.VerdictReject, PayloadSource: reviewscore.PayloadReconstructed,
		Payload: &reviewscore.ReplayPayload{Title: "T", Patch: "--- a/x\n+++ b/x\n+REPLAYED-PATCH\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.got.Payload, `"diff"`) || !strings.Contains(r.got.Payload, "replay sty_r-evt_2") {
		t.Errorf("the replayed patch must ride as the diff:\n%s", r.got.Payload)
	}
	var carried bool
	for _, b := range r.opened {
		carried = carried || strings.Contains(string(b), "REPLAYED-PATCH")
	}
	if !carried {
		t.Errorf("the reviewer's material files do not hold the replayed patch: %v", r.opened)
	}
}

func TestJudgeCaseReplayCarriesPriorVerdictsAndDefinitionEdits(t *testing.T) {
	j, r := stubJudge(t, `{"decision":"accept","notes":"ok"}`)
	_, err := j.Judge(context.Background(), reviewscore.Case{
		ID: "sty_r-evt_3", Skill: "satelle-story-intent-review", StoryID: "sty_r", From: "backlog", To: "plan",
		ExpectedVerdict: reviewscore.VerdictAccept, PayloadSource: reviewscore.PayloadReconstructed,
		Payload: &reviewscore.ReplayPayload{
			Title: "T", NoPatch: true,
			PriorVerdicts:   []verb.PriorVerdict{{Skill: "satelle-story-intent-review", Decision: "reject", Notes: "PRIOR-NOTE-MARKER"}},
			DefinitionEdits: []verb.DefinitionEdit{{Field: "acceptance_criteria", Old: "", New: "EDIT-NEW-MARKER"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.got.Payload, "prior_verdicts") || !strings.Contains(r.got.Payload, "definition_edits") {
		t.Fatalf("the replayed prior verdicts and edits must ride in the payload:\n%s", r.got.Payload)
	}
	var carried string
	for _, b := range r.opened {
		carried += string(b)
	}
	if !strings.Contains(carried, "PRIOR-NOTE-MARKER") || !strings.Contains(carried, "EDIT-NEW-MARKER") {
		t.Errorf("material files do not hold them: %v", r.opened)
	}
}

func TestJudgeResultKeepsTheAdapterNamedCostReasonWhenTokensCameWithoutCost(t *testing.T) {
	res := judgeResult(verb.ReviewerVerdict{
		Accept: true, UsageAvailable: true, TokensIn: 7, TokensOut: 3, CostUnavailableReason: "pi: unpriced model",
	}, time.Second)
	if !res.Usage.Available || res.Usage.CostUSD != nil || res.Usage.CostReason != "pi: unpriced model" || res.Usage.Reason != "" {
		t.Errorf("usage = %+v", res.Usage)
	}
}

func TestJudgeCaseUnknownBindingIsOneClearError(t *testing.T) {
	_, err := NewCaseJudge(CaseJudgeOptions{
		Root: t.TempDir(), Binding: "nope", Docs: fakeDocs{},
		Resolve: func(string) (config.AgentBinding, bool) { return config.AgentBinding{}, false },
	})
	if err == nil || !strings.Contains(err.Error(), "no [nope] binding") {
		t.Fatalf("err = %v", err)
	}
}
