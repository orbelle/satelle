package agentstep

import (
	"context"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// The verdict rules every judging reviewer obeys are one embedded principle,
// handed to a judging dispatch once. These tests pin who receives it and that a
// repo document of the same name replaces it.

const verdictRulesPhrase = "quotation comparison governs"

// verdictPrinciple is a repo-authored principle of the shared name.
func verdictPrinciple(body string) docindex.Doc {
	return docindex.Doc{
		Kind: "principles", Name: config.ReviewerVerdictPrinciple,
		Body: "---\nname: " + config.ReviewerVerdictPrinciple + "\ntype: principle\n---\n" + body,
	}
}

func verdictDocs(principles ...docindex.Doc) fakeDocs {
	if len(principles) == 0 {
		principles = embeddedPrincipleDocs().extraPrinciples
	}
	return fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true, extraPrinciples: principles}
}

func TestReviewerVerdictDocStripsFrontmatterAndDegradesToEmpty(t *testing.T) {
	ctx := context.Background()
	got := ReviewerVerdictDoc(ctx, verdictDocs())
	if !strings.Contains(got, verdictRulesPhrase) || strings.Contains(got, "name: "+config.ReviewerVerdictPrinciple) {
		t.Errorf("the embedded principle resolves with its frontmatter stripped, got %q", got)
	}
	if got := ReviewerVerdictDoc(ctx, verdictDocs(verdictPrinciple("  \n"))); got != "" {
		t.Errorf("a blank override resolves to nothing, got %q", got)
	}
	if got := ReviewerVerdictDoc(ctx, fakeDocs{workflow: testWorkflow}); got != "" {
		t.Errorf("a missing document resolves to nothing, got %q", got)
	}
	if got := ReviewerVerdictDoc(ctx, nil); got != "" {
		t.Errorf("no store resolves to nothing, got %q", got)
	}
}

func TestSingleGateJudgingPromptCarriesTheVerdictRulesOnce(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`, verdictDocs())
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done"); err != nil {
		t.Fatal(err)
	}
	sp := r.got.SystemPrompt
	assertQuotationRules(t, "single-gate judging prompt", sp)
	if n := strings.Count(sp, verdictRulesPhrase); n != 1 {
		t.Errorf("the verdict rules ride once in a judging prompt, got %d", n)
	}
	if !strings.Contains(sp, "rubric body") {
		t.Errorf("the rubric still rides:\n%s", sp)
	}
	if strings.Index(sp, verdictRulesPhrase) > strings.Index(sp, "rubric body") {
		t.Error("the shared verdict rules come before the rubric that owns only its own verdict shape")
	}
}

func TestBundledGateJudgingPromptCarriesTheVerdictRulesOnce(t *testing.T) {
	docs := fakeDocs{
		workflow:        bundleEdgeWF("rev-a,rev-b", "bundle = true"),
		extraSkills:     bundleSkillDocs("rev-a", "rev-b"),
		extraPrinciples: embeddedPrincipleDocs().extraPrinciples,
	}
	r := &bundleRunner{}
	if _, err := bundleGate(t, New(r, docs, "/repo", "")); err != nil {
		t.Fatal(err)
	}
	if r.calls() != 1 {
		t.Fatalf("a bundle is one session, got %d calls", r.calls())
	}
	sp := r.reqs[0].SystemPrompt
	assertQuotationRules(t, "bundled judging prompt", sp)
	if n := strings.Count(sp, verdictRulesPhrase); n != 1 {
		t.Errorf("a bundle carries the verdict rules once for every rubric, got %d", n)
	}
	for _, n := range []string{"rev-a", "rev-b"} {
		if !strings.Contains(sp, "### Rubric: "+n) {
			t.Errorf("rubric %s is missing from the bundle", n)
		}
	}
}

func TestJudgingPromptTakesTheRepoOverrideOfTheVerdictRules(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`, verdictDocs(verdictPrinciple("# Repo verdict rules\n\nOVERRIDE-VERDICT-RULES")))
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done"); err != nil {
		t.Fatal(err)
	}
	sp := r.got.SystemPrompt
	if !strings.Contains(sp, "OVERRIDE-VERDICT-RULES") {
		t.Errorf("the repo's rules replace the embedded ones:\n%s", sp)
	}
	if strings.Contains(sp, verdictRulesPhrase) {
		t.Errorf("the embedded rules must not ride beside an override:\n%s", sp)
	}
	if strings.Contains(sp, "name: "+config.ReviewerVerdictPrinciple) {
		t.Errorf("the override's frontmatter is stripped:\n%s", sp)
	}
}

func TestJudgingPromptWithABlankOverrideCarriesNoVerdictRules(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`, verdictDocs(verdictPrinciple("\n")))
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done"); err != nil {
		t.Fatal(err)
	}
	sp := r.got.SystemPrompt
	if strings.Contains(sp, verdictRulesPhrase) || strings.Contains(sp, config.ReviewerVerdictPrinciple) {
		t.Errorf("a blank override writes nothing:\n%s", sp)
	}
	if !strings.Contains(sp, "rubric body") {
		t.Errorf("the rubric still rides:\n%s", sp)
	}
}

// TestNonJudgingDispatchesDoNotReceiveTheVerdictRules: an executor, a consultant
// and the always-on summary seat are dispatched with the verdict document
// available and are not handed it.
func TestNonJudgingDispatchesDoNotReceiveTheVerdictRules(t *testing.T) {
	g := New(&fakeRunner{}, verdictDocs(), "/repo", "")
	for name, inv := range map[string]invocation{
		"performer": {charter: executorCharter("coder", "in_progress", "default"), rubric: "RUBRIC", principles: config.PrinciplesNone, payload: map[string]string{}},
		"consult":   {charter: consultCharter("reviewer", "consultant", "in_progress"), rubric: "RUBRIC", principles: config.PrinciplesNone, payload: map[string]string{}},
		"summary":   {rubric: "RUBRIC", principles: config.PrinciplesNone, payload: map[string]string{}, scratch: "/tmp/satelle/x"},
	} {
		req, err := g.buildRequest(context.Background(), inv)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(req.SystemPrompt, verdictRulesPhrase) || strings.Contains(req.SystemPrompt, "is required on every judgment") {
			t.Errorf("%s prompt carries the verdict rules:\n%s", name, req.SystemPrompt)
		}
	}
}

// TestSeatPayloadForKeysOnJudgingNotKind: the injected-size estimate delivers the
// verdict rules, and the judging briefing, to a seat that judges and to no other
// — a reviewer-kind seat that does not judge is estimated without them.
func TestSeatPayloadForKeysOnJudgingNotKind(t *testing.T) {
	verdict := ReviewerVerdictDoc(context.Background(), verdictDocs())
	in := SeatPromptInput{Kind: SeatReviewer, Section: "reviewer", Rubric: "RUBRIC", Verdict: verdict}

	idle := SeatPayloadFor(in)
	if strings.Contains(idle, verdictRulesPhrase) || strings.Contains(idle, judgingContextBriefing(false)) {
		t.Errorf("a reviewer-kind seat that does not judge is estimated as a non-judging dispatch:\n%s", idle)
	}

	in.Judging = true
	judging := SeatPayloadFor(in)
	if n := strings.Count(judging, verdictRulesPhrase); n != 1 {
		t.Errorf("a judging seat is estimated with the verdict rules once, got %d", n)
	}
	if len(judging) <= len(idle) {
		t.Errorf("a judging estimate is larger by the rules (%d <= %d)", len(judging), len(idle))
	}

	in.Verdict = ""
	if bare := SeatPayloadFor(in); strings.Contains(bare, verdictRulesPhrase) {
		t.Error("no verdict document, no rules in the estimate")
	}

	in.Kind, in.Judging, in.Verdict = SeatPerformer, true, verdict
	if got := SeatPayloadFor(in); strings.Count(got, verdictRulesPhrase) != 1 {
		t.Error("the estimate keys on Judging, whatever the kind")
	}
}

// TestJudgingSeatNeverGetsTheVerdictRulesTwice: a selector that also picks the
// embedded principle up as a resident document (system, all) must not deliver
// it a second time.
func TestJudgingSeatNeverGetsTheVerdictRulesTwice(t *testing.T) {
	docs := verdictDocs()
	resident := ResidentPrincipleDocs(context.Background(), docs, config.PrinciplesAll)
	var carried bool
	for _, d := range resident {
		carried = carried || d.Name == config.ReviewerVerdictPrinciple
	}
	if !carried {
		t.Fatal("fixture: the all selector must pick the verdict principle up as a resident document")
	}
	verdict := ReviewerVerdictDoc(context.Background(), docs)
	got := BuildSeatPayload(SeatPayloadInput{
		Principles: resident, Charter: "CHARTER", Rubric: "RUBRIC", Judging: true, Verdict: verdict,
	})
	if n := strings.Count(got, verdictRulesPhrase); n != 1 {
		t.Errorf("verdict rules delivered %d times, want once", n)
	}
	// A non-judging seat on the same selector keeps it as an ordinary resident principle.
	idle := BuildSeatPayload(SeatPayloadInput{Principles: resident, Charter: "CHARTER", Rubric: "RUBRIC"})
	if strings.Count(idle, verdictRulesPhrase) != 1 {
		t.Error("a non-judging seat on selector all keeps the principle as an ordinary resident document")
	}
}
