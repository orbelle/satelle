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
