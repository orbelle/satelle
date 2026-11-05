package verb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/agentartifact"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// refuseSkippedStep refuses a status change that is not a declared DOT edge of
// the item's governing workflow (sty_ebd3d666). Agents must walk declared edges
// (including recovery/park/cancel when the graph names them) — skipping a
// required step (e.g. backlog→in_progress when only backlog→plan exists) is a
// hard refuse naming the expected successor(s).
//
// Park resume (sty_f75286dc): when ParkOrigin is set, leaving a park node is
// allowed only to that origin (resume) or via an explicit non-performing exit
// edge (e.g. cancelled). Resume-to-origin is not an N×2 edge explosion — it is
// enforced from the work_items.park_origin column.
//
// Fail-open when the governing workflow cannot be resolved or has no DOT: those
// deployments are owned by structure/engage checks, not this fence. Stories and
// tasks with a resolvable DOT are fenced; executions follow their own paths.
func refuseSkippedStep(ctx context.Context, current workitem.Item, toStatus string) error {
	if current.Kind != workitem.KindStory && current.Kind != workitem.KindTask {
		return nil
	}
	from := current.Status
	if from == toStatus {
		return nil
	}
	idx, err := requireDocIndex()
	if err != nil {
		return nil
	}
	wfs, err := idx.List(ctx, "workflows")
	if err != nil {
		return nil
	}
	spec, wfName, _, serr := wfgovern.SpecFor(wfs, current)
	ok := serr == nil
	if !ok {
		// A broken authored route source, or an authored workflows dir that cannot
		// be read, must fail closed. Other resolution errors (fresh repo /
		// unconverted graph) stay the existing fail-open owned by the gate engine.
		if errors.Is(serr, wfgovern.ErrAuthoredProcessUnreadable) {
			return wfgovern.Refusal{
				Rule: wfgovern.RuleStructureGuard, Item: current.ID,
				From: from, To: toStatus,
				Why:    serr.Error(),
				Remedy: "restore the workflows dir (it must be a readable directory) — no transition is legal until it can be read",
				Err:    serr,
			}
		}
		if errors.Is(serr, wfgovern.ErrRouteSourceBroken) {
			// Point at the diagnosis (sty_88d40a60). The indexer already raised a
			// story naming the exact file and key when the route source stopped
			// parsing; a refusal that does not name it sends the operator to hunt
			// the symptom instead. Wrapped, so errors.Is(ErrRouteSourceBroken)
			// keeps matching and the refusal stays fail-closed either way.
			return wfgovern.Refusal{
				Rule: wfgovern.RuleStructureGuard, Item: current.ID,
				From: from, To: toStatus,
				Why: serr.Error(),
				Err: serr,
			}
		}
		return nil
	}

	// Park resume-to-origin (sty_f75286dc): origin is authoritative state.
	// An empty column is not "cancel only" — derive from the last transition
	// into this park when the workstate wire has wiped the stamp (sty_524f091d).
	if spec.IsParkState(from) {
		origin := ParkOrigin(current, spec, parkOriginEntries(ctx, current.ID))
		if origin != "" {
			if toStatus == origin {
				return nil // resume — always legal
			}
			// Explicit non-performing exits (cancelled, etc.) still use declared edges.
			if spec.HasEdge(from, toStatus) && !spec.IsPerformingState(toStatus) {
				return nil
			}
			return wfgovern.Refusal{
				Rule: wfgovern.RuleParkResume, Item: current.ID, Workflow: wfName,
				From: from, To: toStatus,
				Why: fmt.Sprintf(
					"a parked story resumes to the state it parked from (%q), so the gates already passed to reach it are not re-run and none are skipped",
					origin),
				Alternatives: append([]string{origin}, declaredExits(spec, from)...),
				Remedy:       "cancelled and other declared non-performing exits remain open",
			}
		}
		// Parked with no origin: do not fall through to skipped-step (that
		// names cancelled as the only next step and looks like a missing
		// recover edge). Non-performing declared exits stay open.
		if spec.HasEdge(from, toStatus) && !spec.IsPerformingState(toStatus) {
			return nil
		}
		return wfgovern.Refusal{
			Rule: wfgovern.RuleParkResume, Item: current.ID, Workflow: wfName,
			From: from, To: toStatus,
			Why:          "this park has no recorded origin — the column is empty and the ledger has no transition into this park — so resume is not admitted",
			Alternatives: declaredExits(spec, from),
			Remedy:       "cancelled and other declared non-performing exits remain open",
		}
	}

	if spec.HasEdge(from, toStatus) {
		return nil
	}
	// Unknown from (legacy status not in graph): still refuse if to is not a
	// free-form — name successors of from when any, else generic.
	next := spec.Successors(from)
	if len(next) == 0 {
		return wfgovern.Refusal{
			Rule: wfgovern.RuleSkippedStep, Item: current.ID, Workflow: wfName,
			From: from, To: toStatus,
			Why:    fmt.Sprintf("the route declares no step at all after %s, so there is nothing to move to from here", from),
			Remedy: "open a session on a legal path, or fix the workflow's declaration of done",
		}
	}
	return wfgovern.Refusal{
		Rule: wfgovern.RuleSkippedStep, Item: current.ID, Workflow: wfName,
		From: from, To: toStatus,
		Why: fmt.Sprintf(
			"the route puts %s after %s; a step may not be skipped because its gates are the only place the work is judged",
			strings.Join(next, " or "), from),
		Alternatives: next,
	}
}

// declaredExits returns the declared NON-PERFORMING targets of from — the exits a
// parked story may still take (cancel and the like), so a park-resume refusal
// names every legal move rather than only the origin.
func declaredExits(spec wfdot.Spec, from string) []string {
	var out []string
	for _, to := range spec.Successors(from) {
		if !spec.IsPerformingState(to) {
			out = append(out, to)
		}
	}
	return out
}

// parkOriginForTransition returns the park_origin value to stamp on a status
// transition (sty_f75286dc). Entering a park node stores the prior status;
// leaving a park node clears it. A stay-parked row with an empty column is
// healed from the ledger when a performing origin can be derived (sty_524f091d).
// Nil means leave the column unchanged. This is the only write seam for the
// column on a transition — ParkOrigin itself is a pure derivation.
func parkOriginForTransition(ctx context.Context, current workitem.Item, toStatus string) *string {
	idx, err := requireDocIndex()
	if err != nil {
		return nil
	}
	wfs, err := idx.List(ctx, "workflows")
	if err != nil {
		return nil
	}
	spec, _, _, serr := wfgovern.SpecFor(wfs, current)
	ok := serr == nil
	if !ok {
		return nil
	}
	empty := ""
	if spec.IsParkState(toStatus) && !spec.IsParkState(current.Status) {
		// Enter park: origin is the prior performing (or other) status.
		o := current.Status
		return &o
	}
	if spec.IsParkState(current.Status) && !spec.IsParkState(toStatus) {
		// Leave park (resume or cancel): clear origin.
		return &empty
	}
	if spec.IsParkState(current.Status) && strings.TrimSpace(current.ParkOrigin) == "" {
		if o := ParkOrigin(current, spec, parkOriginEntries(ctx, current.ID)); o != "" {
			return &o
		}
	}
	return nil
}

// ParkOrigin is the single resume-origin computation (sty_524f091d). The
// stamped column wins; otherwise the last status_transition into the current
// park status is used, and only when that from-state is a declared performing
// state of spec. Pure: no store, no write.
func ParkOrigin(item workitem.Item, spec wfdot.Spec, entries []ledger.Entry) string {
	if o := strings.TrimSpace(item.ParkOrigin); o != "" {
		return o
	}
	if !spec.IsParkState(item.Status) {
		return ""
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if TransitionTo(entries[i]) != item.Status {
			continue
		}
		from := TransitionFrom(entries[i])
		if from == "" {
			continue
		}
		// The start state is a legal origin: a wrong premise at ready parks
		// straight from backlog and resumes there (sty_b8a0d062).
		if spec.IsPerformingState(from) || from == spec.Start() {
			return from
		}
		return ""
	}
	return ""
}

// ParkOriginOf is ParkOrigin with the wired ledger. Callers that already have
// entries should call ParkOrigin directly so tests stay store-free.
func ParkOriginOf(ctx context.Context, item workitem.Item, spec wfdot.Spec) string {
	return ParkOrigin(item, spec, parkOriginEntries(ctx, item.ID))
}

func parkOriginEntries(ctx context.Context, storyID string) []ledger.Entry {
	led, err := requireLedger()
	if err != nil || led == nil || strings.TrimSpace(storyID) == "" {
		return nil
	}
	entries, err := led.ListByStory(ctx, storyID, ledger.KindStatusTransition)
	if err != nil {
		return nil
	}
	return entries
}

// PerformerReject is a ready performer that judged the premise wrong.
// Notes are the incorrect content for the operator. The transition parks
// the story as blocked instead of leaving it in backlog (sty_b8a0d062).
// Any other dispatch error stays a refusal with status unchanged.
type PerformerReject struct {
	Notes string
	// Changes are the story-configuration changes the performer reported beside
	// its reject (sty_af0b1761); applied within the step's apply_changes limits.
	Changes []agentartifact.Change
}

func (e *PerformerReject) Error() string {
	if e == nil || strings.TrimSpace(e.Notes) == "" {
		return "performer rejected the premise"
	}
	return e.Notes
}

// ResumeParkName is the synthesised from="*" park state for this item's
// lane, or "" when the lane has none.
func ResumeParkName(ctx context.Context, current workitem.Item) string {
	idx, err := requireDocIndex()
	if err != nil {
		return ""
	}
	wfs, err := idx.List(ctx, "workflows")
	if err != nil {
		return ""
	}
	spec, _, _, serr := wfgovern.SpecFor(wfs, current)
	if serr != nil {
		return ""
	}
	for _, st := range spec.States {
		if spec.IsResumePark(st.Name) {
			return st.Name
		}
	}
	return ""
}
