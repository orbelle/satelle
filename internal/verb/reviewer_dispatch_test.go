package verb

import (
	"context"
	"regexp"

	"github.com/bobmcallan/satelle/internal/workitem"
)

// Minimal seam stubs — the predicates only read whether a seam is wired.
type stubGater struct{}

func (stubGater) Gate(context.Context, workitem.Item, string) (GateDecision, error) {
	return GateDecision{}, nil
}

type stubCreateReviewer struct{}

func (stubCreateReviewer) ReviewCreate(context.Context, CreateDraft) (GateDecision, error) {
	return GateDecision{}, nil
}

type stubAmendReviewer struct{}

func (stubAmendReviewer) ReviewAmend(context.Context, AmendDraft) (GateDecision, error) {
	return GateDecision{}, nil
}

// reviewerSeamCall matches a call THROUGH one of the seams that runs a reviewer
// or another isolated agent.
var reviewerSeamCall = regexp.MustCompile(`\b(transitionGater|createReviewer|amendReviewer|executorDispatcher|retrospector|stepSummariser)\.[A-Z]\w*\(`)

// reviewerDispatchSites names, per source file, the verbs whose invocation
// reaches a reviewer seam from it. A file that calls a seam and is not here — or
// a verb named here that does not declare DispatchesReviewer — fails: that is a
// verb an agent-facing caller would sit through unbounded (sty_c4b92c9e AC6).
var reviewerDispatchSites = map[string][]string{
	"workitem.go": {
		"story-create", "task-create", "execution-create",
		"story-set", "task-set", "execution-set",
		"story-resummarise", "story-retrospect",
	},
	"amend.go": {"story-amend"},
	// A readiness round dispatches the step's performer and reviewers; both the
	// presented edge and `story ready` run it (sty_a32bd6a9).
	"readiness_round.go": {"story-set", "task-set", "execution-set", "story-ready"},
	// An entry-state definition edit runs the create gate (sty_a8885220).
	"edit_gate.go": {"story-set", "task-set", "execution-set"},
}
