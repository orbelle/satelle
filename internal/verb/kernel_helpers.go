package verb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/wfroute"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// Kernel helpers kept from files removed in the rebuild (B1).

// governingSpec resolves the workflow governing item and parses its lifecycle.
// ok=false when no workflow resolves or its lifecycle is not parseable — the
// route is then simply unavailable, never wrong.
func governingSpec(ctx context.Context, item workitem.Item) (wfdot.Spec, string, []wfroute.Advisor, bool) {
	d, name, ok := governingRoute(ctx, item)
	return d.Spec, name, d.Advisors, ok
}

// governingRoute is governingSpec's whole answer: the DerivedRoute, so a
// renderer reads the Spec AND the annotations that are not topology (advisors,
// rework loops) off one resolution (sty_8e0b29a0).
func governingRoute(ctx context.Context, item workitem.Item) (wfgovern.DerivedRoute, string, bool) {
	idx, err := requireDocIndex()
	if err != nil {
		return wfgovern.DerivedRoute{}, "", false
	}
	wfs, err := idx.List(ctx, "workflows")
	if err != nil {
		return wfgovern.DerivedRoute{}, "", false
	}
	d, name, err := wfgovern.RouteFor(wfs, item)
	if err != nil {
		return wfgovern.DerivedRoute{}, "", false
	}
	return d, name, true
}

// GoverningRoute is the route governing item — the spine it walks, the loops its
// steps declare — resolved over the wired document index, with the reason when
// none does. A caller that must walk the route (the headless drive) reads it
// here rather than keeping its own copy of the resolution.
func GoverningRoute(ctx context.Context, item workitem.Item) (wfgovern.DerivedRoute, error) {
	idx, err := requireDocIndex()
	if err != nil {
		return wfgovern.DerivedRoute{}, err
	}
	wfs, err := idx.List(ctx, "workflows")
	if err != nil {
		return wfgovern.DerivedRoute{}, err
	}
	d, _, err := wfgovern.RouteFor(wfs, item)
	return d, err
}

// destinationStep resolves the step the story is moving INTO over the declared
// current→to edge. Not found when the route cannot be resolved or declares no
// such edge.
func destinationStep(ctx context.Context, current workitem.Item, to string) (wfdot.State, []wfroute.Advisor, bool) {
	spec, _, advisors, ok := governingSpec(ctx, current)
	if !ok || !spec.HasEdge(current.Status, to) {
		return wfdot.State{}, nil, false
	}
	st, found := spec.StateNamed(to)
	return st, advisors, found
}

// storyLedgerEntries lists every ledger row of a story, oldest first; nil when
// no ledger is wired (an unwired ledger counts no rounds, so nothing refuses).
func storyLedgerEntries(ctx context.Context, storyID string) []ledger.Entry {
	led, err := requireLedger()
	if err != nil || led == nil || strings.TrimSpace(storyID) == "" {
		return nil
	}
	entries, err := led.ListByStory(ctx, storyID, "")
	if err != nil {
		return nil
	}
	return entries
}

// EditorIdentity is the session and executable behind a definition edit — the
// part of "who" the person actor does not say (sty_a8885220). Every field is
// optional so a row written before this identity was recorded still parses; a
// row written after it carries either the pair or IdentityUnavailable, never a
// silent empty.
type EditorIdentity struct {
	SessionID  string `json:"session_id,omitempty"`
	Executable string `json:"executable,omitempty"`
	// IdentityUnavailable names, adapter by adapter, what could not be resolved.
	IdentityUnavailable string `json:"identity_unavailable,omitempty"`
}

// resolveEditorIdentity names the session making this edit and the harness it
// runs under. What cannot be resolved is stated, not left empty.
func resolveEditorIdentity() EditorIdentity {
	sid := config.ResolveSession()
	exe := agentcli.HarnessUnknown
	if sid != "" {
		exe = driverSessionHarness(sid)
	} else if h, ok := agentcli.InLoopHarnessFromEnv(os.Environ()); ok {
		exe = h
	}
	id := EditorIdentity{SessionID: sid}
	var gaps []string
	if sid == "" {
		gaps = append(gaps, "session: no session id in SATELLE_SESSION, the harness environment or a published session")
	}
	if exe == agentcli.HarnessUnknown {
		gaps = append(gaps, fmt.Sprintf("executable: no adapter (%s, %s, %s) recognised the session",
			agentcli.HarnessClaude, agentcli.HarnessGrok, agentcli.HarnessPi))
	} else {
		id.Executable = exe
	}
	id.IdentityUnavailable = strings.Join(gaps, "; ")
	return id
}

// storyUpdatedPayload is a story_updated row's payload: the editor and, for a
// definition edit, the before and after of each changed field. A row written
// before this payload existed has none and decodes to the zero value.
type storyUpdatedPayload struct {
	EditorIdentity
	Fields []AmendField `json:"fields,omitempty"`
	// Tags names the story's tags before and after when the set changed them
	// (sty_d84be8bc): the shape every tag writer records, so the fold reads one.
	Tags *storyfold.TagsChange `json:"tags,omitempty"`
	// Source names the writer when it is not a story-set (a forced pull).
	Source string `json:"source,omitempty"`
	// Reason is the operator's stated reason for an archive or unarchive.
	Reason string `json:"reason,omitempty"`
}

// storyUpdatedPayloadFor renders the story_updated payload for one committed
// story-set. It is always present, so even a tag or priority edit names its editor.
func storyUpdatedPayloadFor(id EditorIdentity, edits []AmendField, tags *storyfold.TagsChange) json.RawMessage {
	b, err := json.Marshal(storyUpdatedPayload{EditorIdentity: id, Fields: edits, Tags: tags})
	if err != nil {
		return nil
	}
	return b
}

// reconcilePayload is a status_reconcile event's payload: each field the row
// held and the chain's value it was rebuilt to. The fold ignores the event, since
// it records a repair of the cache, not a change to the story.
func reconcilePayload(drifts []StoryDrift) json.RawMessage {
	type field struct {
		Field string `json:"field"`
		Row   string `json:"row"`
		Chain string `json:"chain"`
	}
	fields := make([]field, 0, len(drifts))
	for _, d := range drifts {
		fields = append(fields, field{Field: d.Field, Row: d.Row, Chain: d.Chain})
	}
	b, _ := json.Marshal(map[string]any{"fields": fields})
	return b
}

// driverSessionHarness names the harness whose record holds sessionID's usage.
// The session's own published in-loop row wins: its hooks name their harness
// (sty_719c4a7b), while the environment can carry a parent agent's markers —
// a grok session started from a Claude Code shell inherits CLAUDECODE=1.
// The environment is the fallback when the session published nothing.
func driverSessionHarness(sessionID string) string {
	switch _, exe, _ := config.ResolveSessionModel(sessionID, SessionModelRoleInLoop); exe {
	case agentcli.HarnessClaude, agentcli.HarnessGrok, agentcli.HarnessPi:
		return exe
	}
	if h, ok := agentcli.InLoopHarnessFromEnv(os.Environ()); ok {
		return h
	}
	return agentcli.HarnessUnknown
}

// seatPolicy is what a claim hands the lease: its arbitration key and the two
// co-hold flags lease.CoHolds reads. A seat is the item's own id: there is no
// concurrency cap (REBUILD Q-multiseat).
type seatPolicy = lease.SeatClaim

func seatPolicyFor(_ context.Context, item workitem.Item) seatPolicy {
	return seatPolicy{Key: item.ID}
}

// engagementWorktree resolves the git working tree this process is engaging
// from — the anchor recorded on the lease. Empty when git cannot answer.
func engagementWorktree() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return gitToplevel(dir)
}

// gitToplevel resolves dir's git working-tree root. Empty when dir is not in a
// working tree.
func gitToplevel(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
