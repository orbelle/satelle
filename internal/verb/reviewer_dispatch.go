package verb

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// A gate's verdict line has two destinations. It is shown on stderr — the caller
// at a terminal reads each decision as it lands — and, in a detached gate run, it
// is also recorded (SetVerdictRecorder), so the verdict and only the verdict can
// be handed to the driving session afterwards (sty_c4b92c9e).
var verdictRecord = func(string) {}

// SetVerdictRecorder records every verdict line in addition to showing it; nil
// stops recording.
func SetVerdictRecorder(f func(string)) {
	if f == nil {
		f = func(string) {}
	}
	verdictRecord = f
}

// EmitVerdict shows one gate decision on stderr and records it.
func EmitVerdict(line string) {
	fmt.Fprintln(os.Stderr, line)
	verdictRecord(line)
}

// RecordVerdict records a gate decision without showing it: an accepted create
// or amend has always been silent at a terminal, and stays so, but a detached
// run still has to hand its verdict to the session that started it.
func RecordVerdict(line string) { verdictRecord(line) }

// DispatchesReviewer reports whether invoking verb `name` with req will run a
// reviewer or another isolated agent — the seam an agent-facing caller uses to
// decide whether to hand the call to a detached run (sty_c4b92c9e). An unknown
// verb, or one that declares no predicate, never dispatches.
//
// This is the single choke point for "does this call run a gate": every verb
// that reaches transitionGater, createReviewer, amendReviewer,
// executorDispatcher, retrospector or stepSummariser declares itself here, and
// reviewer_dispatch_test.go fails if a new call site appears in a verb that does
// not, so a new verb cannot slip past the contract.
func DispatchesReviewer(name string, req json.RawMessage) bool {
	v := Get(name)
	if v == nil || v.DispatchesReviewer == nil {
		return false
	}
	return v.DispatchesReviewer(req)
}

// requestsStatus reports whether a set request carries a status change — the
// only thing that reaches the transition gate, the named-agent dispatch or the
// step summariser. A set of a title or a tag runs no reviewer.
func requestsStatus(req json.RawMessage) bool {
	var r struct {
		Status *string `json:"status"`
	}
	if json.Unmarshal(req, &r) != nil || r.Status == nil {
		return false
	}
	return strings.TrimSpace(*r.Status) != ""
}

// dispatchesOnStatusChange is the predicate of a <kind>-set verb: a status
// change with any reviewer-side seam wired.
func dispatchesOnStatusChange(req json.RawMessage) bool {
	if requestsDefinitionEdit(req) && createReviewer != nil {
		// An entry-state definition edit runs the create gate (runEditCreateGate).
		// Whether the story is still in its entry state is not known from the
		// request, so this errs towards handing the call to a detached run.
		return true
	}
	if !requestsStatus(req) {
		return false
	}
	return transitionGater != nil || executorDispatcher != nil
}

// requestsDefinitionEdit reports whether a set request names a definition field.
func requestsDefinitionEdit(req json.RawMessage) bool {
	var r struct {
		Title              *string `json:"title"`
		Body               *string `json:"body"`
		AcceptanceCriteria *string `json:"acceptance_criteria"`
		Category           *string `json:"category"`
	}
	if json.Unmarshal(req, &r) != nil {
		return false
	}
	return r.Title != nil || r.Body != nil || r.AcceptanceCriteria != nil || r.Category != nil
}

// dispatchesOnGatedCreate is the predicate of a <kind>-create verb: a repo that
// opted into create-gating (satelle.toml [review] gate_create).
func dispatchesOnGatedCreate(json.RawMessage) bool { return createReviewer != nil }

// dispatchesOnAmend is story-amend's predicate: an amendment is always judged.
func dispatchesOnAmend(json.RawMessage) bool { return amendReviewer != nil }
