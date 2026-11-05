package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/wfroute"
)

// TestEditPermissionDenyReasonCoderStep pins the coder-step refusal text
// (sty_a7914904): the relay is named only on a step that declares a rework
// loop; otherwise the one-shot coder dispatch is named. An executor step keeps
// permitting the driver's edit.
func TestEditPermissionDenyReasonCoderStep(t *testing.T) {
	now := time.Now().UTC()
	coder := seatInfo{
		ItemID: "sty_x", State: "in_progress", StoryStatus: "in_progress",
		StateAgent: "coder", Engaged: true, EditCapable: false,
		EditStates: []string{"integration", "release"},
	}

	withRework := coder
	withRework.StateRework = true
	got := editPermissionDenyReason(withRework, nil, "", now)
	for _, want := range []string{"sty_x", `"coder"`, "story rework sty_x", "Do not edit in-loop"} {
		if !strings.Contains(got, want) {
			t.Errorf("rework step reason missing %q: %s", want, got)
		}
	}

	got = editPermissionDenyReason(coder, nil, "", now)
	for _, want := range []string{"one-shot coder dispatch", `"coder"`, "Do not edit in-loop"} {
		if !strings.Contains(got, want) {
			t.Errorf("non-rework step reason missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "story rework") {
		t.Errorf("non-rework step must not name `story rework`: %s", got)
	}

	exec := seatInfo{
		ItemID: "sty_x", State: "integration", StoryStatus: "integration",
		StateAgent: "executor", Engaged: true, EditCapable: true,
		EditStates: []string{"integration", "release"},
	}
	if !editPermitted(exec, dispatchMarker{}) {
		t.Errorf("executor step must still permit the driver's edit")
	}
}

// codedStepWFs is a route whose implement step is allocated to a dispatched
// coder, with or without a rework loop on that step.
func codedStepWFs(reworkLine string) []docindex.Doc {
	return routeWFs(
		`["*"]
obligations = ["raised", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "coder"
requires = ["raised"]
`+reworkLine+`

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
}

func TestStepDeclaresRework(t *testing.T) {
	rw := []wfroute.Rework{{Step: "in_progress", Consult: "reviewer-consult", Rounds: 5}}
	if !stepDeclaresRework(rw, "in_progress") {
		t.Errorf("in_progress declares rework")
	}
	if stepDeclaresRework(rw, "plan") || stepDeclaresRework(nil, "in_progress") {
		t.Errorf("only the declared step carries rework")
	}
}
