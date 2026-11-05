package agentstep

import (
	"context"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// amendWF declares the amend gate through the `amend_review` shorthand on its
// declaration of done — the same seam create_review uses (sty_81aa4d8f).
var amendWF = func() string {
	base := spineWF("", "", "", "in_progress|executor", "done")
	return strings.Replace(base, "scope = \"system\"\n",
		"scope = \"system\"\namend_review = \"my-amend-review\"\n", 1)
}()

// amendDraft is a well-formed correction of one acceptance criterion.
var amendDraft = verb.AmendDraft{
	Item: workitem.Item{
		Kind: workitem.KindStory, ID: "sty_amend", Title: "Add X",
		Body: "Make the thing do X", AcceptanceCriteria: "1. it does X\n2. false claim",
		Category: "feature", Status: "in_progress",
	},
	Status: "in_progress",
	Reason: "AC2 asserted behaviour the system does not have",
	Fields: []verb.AmendField{{
		Field: "acceptance_criteria",
		Old:   "1. it does X\n2. false claim",
		New:   "1. it does X\n2. corrected claim",
	}},
}

// A workflow that declares no amend_review hook yields an UNGATED decision, which
// the verb treats as a refusal. This inverts create's fail-open deliberately:
// nothing may pierce the definition freeze unjudged.
func TestReviewAmendWithoutADeclaredHookIsUngated(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`,
		fakeDocs{workflow: plainWF, skillBody: "rubric", skillFound: true})
	dec, err := g.ReviewAmend(context.Background(), amendDraft)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Gated {
		t.Fatalf("an undeclared amend gate must not judge: %+v", dec)
	}
	if r.got.SystemPrompt != "" {
		t.Error("no reviewer should have run")
	}
}

// An amendment may correct a definition but never leave the story structurally
// invalid: the deterministic check pre-empts the reviewer, exactly as on create.
// Empty acceptance criteria are not structurally invalid — the amend skill judges
// a dropped criterion — so the pre-empt case is an emptied body.
func TestReviewAmendRejectsAStructurallyInvalidResult(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`,
		fakeDocs{workflow: amendWF, skillBody: "amend rubric", skillFound: true})
	draft := amendDraft
	draft.Fields = []verb.AmendField{{Field: "body", Old: "Make the thing do X", New: ""}}
	dec, err := g.ReviewAmend(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || dec.Accept || dec.Notes == "" {
		t.Fatalf("want a structural reject, got %+v", dec)
	}
	if r.got.SystemPrompt != "" {
		t.Error("the content reviewer must not be reached on a structurally invalid amendment")
	}
}

// Dropping acceptance criteria is not a structural pre-empt. The amend reviewer
// is reached; it, not Go, judges the weakening.
func TestReviewAmendEmptyCriteriaReachesReviewer(t *testing.T) {
	g, r := newEngine(t, `{"decision":"reject","notes":"dropped a criterion"}`,
		fakeDocs{workflow: amendWF, skillBody: "amend rubric", skillFound: true})
	draft := amendDraft
	draft.Fields = []verb.AmendField{{Field: "acceptance_criteria", Old: "1. it does X", New: ""}}
	dec, err := g.ReviewAmend(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || dec.Accept || dec.Skill != "my-amend-review" {
		t.Fatalf("empty criteria must reach the amend reviewer, got %+v", dec)
	}
	if r.got.SystemPrompt == "" {
		t.Fatal("amend reviewer must run when criteria are dropped")
	}
}
