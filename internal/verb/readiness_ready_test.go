package verb_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_a32bd6a9: `story ready` readies backlog stories and epics without engaging
// them. The fixture route declares the readiness knobs (propose, mark,
// reject_budget on a story's step; waits_on_children on a container's) and the
// behaviour under test is the binary reading them, not this repo's step names.

const readyDoneToml = `["*"]
obligations = ["raised", "readied", "coded", "closed"]
park = { state = "blocked", gate = "park-gate" }
recover = { step = "backlog", from = ["plan"] }

[epic-parent]
obligations = ["raised", "ready", "epic-coded", "epic-closed"]
park = { state = "blocked", gate = "park-gate" }
`

// readyStepToml is the step catalogue; mark is the key the readiness steps carry
// (empty omits it) and reviewers the story readiness step's reviewer list.
func readyStepToml(mark string, reviewers string) string {
	markLine := ""
	if mark != "" {
		markLine = `mark = "` + mark + `"` + "\n"
	}
	return `[raised]
status = "backlog"
start = true

[ready]
status = "ready"
agent = "ready-reviewer"
skills = ["ready-review"]
waits_on_children = true
schedule = "parallel"
requires = ["raised"]
` + markLine + `
[readied]
status = "plan"
agent = "planner"
skills = ["plan"]
propose = true
reject_budget = 3
` + reviewers + markLine + `requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
freeze = true
requires = ["readied"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[epic-coded]
status = "in_progress"
agent = "executor"
requires = ["ready"]

[epic-closed]
status = "done"
terminal = true
requires = ["epic-coded"]
`
}

const readyReviewers = `reviewers = ["gate-a", "gate-b"]` + "\n"

// roundDispatcher is the wired performer: it records every dispatch, resolves who
// performs a step under a script, and attaches a plan the way a planner does.
type roundDispatcher struct {
	t       *testing.T
	mu      sync.Mutex
	calls   map[string]int
	resolve func(to string) verb.PerformerResolution
	perform func(it workitem.Item, to string) (verb.DispatchResult, error)
	// docName is the output_name the performer's skill declares: the resolver
	// reports it and the default dispatch attaches its artifact under it.
	docName string
}

func (d *roundDispatcher) total() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		n += c
	}
	return n
}

func (d *roundDispatcher) StepPerformer(_ context.Context, _ workitem.Item, to string) (verb.PerformerResolution, error) {
	if d.resolve != nil {
		return d.resolve(to), nil
	}
	return verb.PerformerResolution{Agent: "performer", Dispatched: true, OutputName: d.docName}, nil
}

func (d *roundDispatcher) DispatchExecutor(_ context.Context, it workitem.Item, to string) (verb.DispatchResult, error) {
	d.mu.Lock()
	d.calls[it.ID]++
	d.mu.Unlock()
	if d.perform != nil {
		return d.perform(it, to)
	}
	if to == "plan" {
		call(d.t, "story-doc-attach", map[string]any{"story_id": it.ID, "name": d.docName, "type": d.docName, "body": "the plan"})
	}
	return verb.DispatchResult{Dispatched: true, Agent: "performer", Skill: "plan"}, nil
}

// roundGater is the wired gate: verd decides each (item, step) round.
type roundGater struct {
	mu    sync.Mutex
	calls map[string]int
	verd  func(it workitem.Item, to string) verb.GateDecision
}

func (g *roundGater) total() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.calls {
		n += c
	}
	return n
}

func (g *roundGater) Gate(_ context.Context, it workitem.Item, to string) (verb.GateDecision, error) {
	g.mu.Lock()
	g.calls[it.ID]++
	g.mu.Unlock()
	return g.verd(it, to), nil
}

type readyRig struct {
	d *roundDispatcher
	g *roundGater
}

// activeRig is the rig of the running test, for the helpers that read its
// declared plan document name.
var activeRig *readyRig

// newReadyRig wires the route and a dispatcher and gater that accept everything.
func newReadyRig(t *testing.T, mark, reviewers string) *readyRig {
	t.Helper()
	return newRigFor(t, readyStepToml(mark, reviewers))
}

// newRigFor is newReadyRig over an arbitrary step catalogue.
func newRigFor(t *testing.T, stepToml string) *readyRig {
	t.Helper()
	return newRigWith(t, readyDoneToml, stepToml)
}

// newRigWith is newRigFor over an arbitrary lifecycle declaration too.
func newRigWith(t *testing.T, doneToml, stepToml string) *readyRig {
	t.Helper()
	r, _ := newRigWithStore(t, doneToml, stepToml)
	return r
}

// newRigWithStore is newRigWith that also returns the store, for a fixture that
// writes a record directly the way legacy data was produced.
func newRigWithStore(t *testing.T, doneToml, stepToml string) (*readyRig, *store.DB) {
	t.Helper()
	db := wireWithWorkflowsStore(t, routeHalves(doneToml, stepToml))
	verb.SetStoryDir(t.TempDir())
	r := &readyRig{
		d: &roundDispatcher{t: t, calls: map[string]int{}, docName: "plan"},
		g: &roundGater{calls: map[string]int{}},
	}
	r.g.verd = func(workitem.Item, string) verb.GateDecision { return acceptAll() }
	verb.SetExecutorDispatcher(r.d)
	verb.SetTransitionGater(r.g)
	activeRig = r
	return r, db
}

func newStoryOf(t *testing.T, category, title string, tags ...string) workitem.Item {
	t.Helper()
	var it workitem.Item
	req := map[string]any{"title": title, "body": "B", "acceptance_criteria": "1. one", "category": category}
	if len(tags) > 0 {
		req["tags"] = tags
	}
	if err := json.Unmarshal(call(t, "story-create", req), &it); err != nil {
		t.Fatal(err)
	}
	return it
}

func readyCall(t *testing.T, ids ...string) []verb.ReadyResult {
	t.Helper()
	var out []verb.ReadyResult
	if err := json.Unmarshal(call(t, "story-ready", map[string]any{"ids": ids}), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func allRows(t *testing.T, id string) []ledger.Entry {
	t.Helper()
	var all []ledger.Entry
	if err := json.Unmarshal(call(t, "ledger-list", map[string]any{"story_id": id}), &all); err != nil {
		t.Fatal(err)
	}
	return all
}

func hasMark(t *testing.T, id string) bool {
	t.Helper()
	for _, tag := range statusOf(t, id).Tags {
		if tag == "ready" {
			return true
		}
	}
	return false
}

func holds(t *testing.T, id string) bool {
	t.Helper()
	_, ok := ledger.ReadinessHolds(allRows(t, id), "backlog", "plan", activeRig.d.docName)
	return ok
}

// seatList is the seat listing with its time-derived fields (every *_age)
// dropped, so two reads compare equal whenever the seats themselves do.
func seatList(t *testing.T) string {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(call(t, "story-seat-list", map[string]any{}), &rows); err != nil {
		t.Fatalf("decode seat list: %v", err)
	}
	for _, row := range rows {
		for k := range row {
			if strings.HasSuffix(k, "_age") {
				delete(row, k)
			}
		}
	}
	out, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("encode seat list: %v", err)
	}
	return string(out)
}

// AC1 + AC3 + AC5: two backlog stories are readied in one call while a third
// story holds the seat; each round ran, no status moved, the seat holder is
// unchanged, every row of a round shares one attempt id and the story is marked.
func TestStoryReadyReadiesWithoutTheSeat(t *testing.T) {
	r := newReadyRig(t, "ready", readyReviewers)
	holder := newStoryOf(t, "feature", "holder")
	a := newStoryOf(t, "feature", "a")
	b := newStoryOf(t, "feature", "b")
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": holder.ID, "status": "plan"}); err != nil {
		t.Fatalf("engage the holder: %v", err)
	}
	seatBefore := seatList(t)
	if !strings.Contains(seatBefore, holder.ID) {
		t.Fatalf("the holder must hold the seat: %s", seatBefore)
	}
	dispatchesBefore, gatesBefore := r.d.total(), r.g.total()

	res := readyCall(t, a.ID, b.ID)
	for i, id := range []string{a.ID, b.ID} {
		if res[i].ID != id || res[i].Outcome != verb.ReadyMarked {
			t.Fatalf("result %d = %+v, want %s marked", i, res[i], id)
		}
		if got := statusOf(t, id).Status; got != "backlog" {
			t.Errorf("%s status = %q, ready must not change it", id, got)
		}
		if !hasMark(t, id) {
			t.Errorf("%s must carry the mark", id)
		}
		if r.d.calls[id] != 1 || r.g.calls[id] != 1 {
			t.Errorf("%s: %d dispatches, %d gate runs, want 1 and 1", id, r.d.calls[id], r.g.calls[id])
		}
	}
	if r.d.total()-dispatchesBefore != 2 || r.g.total()-gatesBefore != 2 {
		t.Errorf("both rounds must have run")
	}
	if got := seatList(t); got != seatBefore {
		t.Errorf("the seat must be untouched\nbefore: %s\nafter:  %s", seatBefore, got)
	}

	// One attempt id covers the round: dispatch row, every reviewer row, accept row.
	rows := allRows(t, a.ID)
	var dispatch, accepts, readiness []ledger.Entry
	for _, e := range rows {
		switch {
		case e.Kind == ledger.KindAgentInvocation && strings.Contains(e.Body, "dispatched step"):
			dispatch = append(dispatch, e)
		case e.Kind == ledger.KindReviewAccept:
			accepts = append(accepts, e)
		case e.Kind == ledger.KindReadinessAccept:
			readiness = append(readiness, e)
		}
	}
	if len(dispatch) != 1 || len(accepts) != 2 || len(readiness) != 1 {
		t.Fatalf("rows = %d dispatch, %d review accepts, %d readiness accepts; want 1, 2, 1", len(dispatch), len(accepts), len(readiness))
	}
	attempt := attemptOf(t, readiness[0])
	if attempt == "" || attempt != res[0].Attempt {
		t.Fatalf("readiness accept attempt %q, result names %q", attempt, res[0].Attempt)
	}
	for _, e := range append(append(dispatch, accepts...), readiness...) {
		if got := attemptOf(t, e); got != attempt {
			t.Errorf("%s row carries attempt %q, want the round's %q", e.Kind, got, attempt)
		}
	}
	var doc struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(call(t, "story-doc-get", map[string]any{"story_id": a.ID, "name": "plan"}), &doc); err != nil || !strings.Contains(doc.Body, "the plan") {
		t.Errorf("the performer's artifact must be attached as the plan doc: %v %+v", err, doc)
	}
}

// AC2: a step whose performer is not dispatched is refused naming the step, under
// the engine's rule (here scripted), and nothing runs.
func TestStoryReadyRefusesAStepNobodyDispatches(t *testing.T) {
	for _, reason := range []string{"agent is empty", "allocated to agent=executor", "allocated to agent=reviewer", "bound in-loop"} {
		t.Run(reason, func(t *testing.T) {
			r := newReadyRig(t, "ready", readyReviewers)
			r.d.resolve = func(string) verb.PerformerResolution { return verb.PerformerResolution{Reason: reason} }
			a := newStoryOf(t, "feature", "a")
			res := readyCall(t, a.ID)[0]
			if res.Outcome != verb.ReadyRefused || !strings.Contains(res.Reason, `"readied"`) || !strings.Contains(res.Reason, reason) {
				t.Fatalf("want a refusal naming the step and %q, got %+v", reason, res)
			}
			if r.d.total() != 0 || r.g.total() != 0 {
				t.Errorf("a refused story must dispatch nothing: %d dispatches, %d gates", r.d.total(), r.g.total())
			}
		})
	}
}

// A dispatcher that cannot say who performs a step is not one `story ready` can run.
func TestStoryReadyRefusesWithoutAPerformerResolver(t *testing.T) {
	newReadyRig(t, "ready", readyReviewers)
	verb.SetExecutorDispatcher(dispatcherFunc(func(context.Context, workitem.Item, string) (verb.DispatchResult, error) {
		t.Fatal("must not dispatch")
		return verb.DispatchResult{}, nil
	}))
	a := newStoryOf(t, "feature", "a")
	if res := readyCall(t, a.ID)[0]; res.Outcome != verb.ReadyRefused || !strings.Contains(res.Reason, `"readied"`) {
		t.Fatalf("want a refusal naming the step, got %+v", res)
	}
}

// AC3: a performer that rejects under `story ready` is a rejected round with its
// own attempt id; the status is unchanged and no accept or mark is written.
func TestStoryReadyPerformerRejectIsARejectedRound(t *testing.T) {
	r := newReadyRig(t, "ready", readyReviewers)
	r.d.perform = func(workitem.Item, string) (verb.DispatchResult, error) {
		return verb.DispatchResult{Dispatched: true, Agent: "performer", Skill: "plan"}, &verb.PerformerReject{Notes: "premise wrong: foo.go has no such function"}
	}
	a := newStoryOf(t, "feature", "a")
	res := readyCall(t, a.ID)[0]
	if res.Outcome != verb.ReadyNotAccepted || !strings.Contains(res.Reason, "premise wrong") || res.Attempt == "" {
		t.Fatalf("want a not-accepted round with the notes and an attempt, got %+v", res)
	}
	if statusOf(t, a.ID).Status != "backlog" || hasMark(t, a.ID) {
		t.Errorf("status must be unchanged and the story unmarked")
	}
	if r.g.total() != 0 {
		t.Errorf("the gate must not run after a performer reject")
	}
	rejects := ledgerRows(t, a.ID, ledger.KindPerformerReject)
	if len(rejects) != 1 || attemptOf(t, rejects[0]) != res.Attempt || !strings.Contains(rejects[0].Body, "premise wrong") {
		t.Fatalf("one performer_reject row with the round's attempt and the notes expected: %+v", rejects)
	}
	if got := ledgerRows(t, a.ID, ledger.KindReadinessAccept); len(got) != 0 {
		t.Errorf("a rejected round writes no readiness accept: %+v", got)
	}
	failure := false
	for _, e := range ledgerRows(t, a.ID, ledger.KindAgentInvocation) {
		if strings.Contains(e.Body, "dispatch failed") && attemptOf(t, e) == res.Attempt {
			failure = true
		}
	}
	if !failure {
		t.Errorf("the dispatch failure row must carry the round's attempt")
	}
}

// AC3 (engaged route): a performer reject parks the story, and the reject row,
// the park comment and the park transition all name the same attempt.
func TestEngagedPerformerRejectAndItsParkShareOneAttempt(t *testing.T) {
	r := newReadyRig(t, "ready", readyReviewers)
	r.d.perform = func(_ workitem.Item, to string) (verb.DispatchResult, error) {
		if to != "plan" {
			return verb.DispatchResult{}, nil // the park edge dispatches nobody
		}
		return verb.DispatchResult{Dispatched: true, Agent: "performer", Skill: "plan"}, &verb.PerformerReject{Notes: "premise wrong"}
	}
	a := newStoryOf(t, "feature", "a")
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}); err != nil {
		t.Fatalf("a premise reject must park, not refuse: %v", err)
	}
	if got := statusOf(t, a.ID).Status; got != "blocked" {
		t.Fatalf("status = %q, want blocked", got)
	}
	rejects := ledgerRows(t, a.ID, ledger.KindPerformerReject)
	if len(rejects) != 1 {
		t.Fatalf("want one performer_reject row, got %+v", rejects)
	}
	attempt := attemptOf(t, rejects[0])
	if attempt == "" {
		t.Fatal("the performer reject must carry an attempt")
	}
	comment := false
	for _, e := range ledgerRows(t, a.ID, ledger.KindComment) {
		if strings.Contains(e.Body, "premise wrong") && attemptOf(t, e) == attempt {
			comment = true
		}
	}
	park := false
	for _, e := range ledgerRows(t, a.ID, ledger.KindStatusTransition) {
		var p struct{ From, To string }
		_ = json.Unmarshal(e.Payload, &p)
		if p.To == "blocked" && attemptOf(t, e) == attempt {
			park = true
		}
	}
	if !comment || !park {
		t.Errorf("park comment (%v) and park transition (%v) must carry the reject's attempt %s", comment, park, attempt)
	}
	if r.g.calls[a.ID] != 1 { // only the park edge's gate, never readiness reviewers
		t.Errorf("gate runs = %d, want only the park edge", r.g.calls[a.ID])
	}
}

// AC4: the one rule — an accept, a one-reviewer reject, a performer reject, a
// performer failure and a performer-only accept — as `story ready` records them,
// counted by both counters, with the accept row present only for an accepted round.
func TestStoryReadyAcceptRuleAcrossRounds(t *testing.T) {
	oneReject := func(workitem.Item, string) verb.GateDecision {
		return verb.GateDecision{Gated: true, Skill: "gate-a", Reviewers: []verb.ReviewerVerdict{
			{Skill: "gate-a", Accept: true}, {Skill: "gate-b", Accept: false, Notes: "AC2 is untestable"},
		}}
	}
	cases := []struct {
		name      string
		reviewers string
		verd      func(workitem.Item, string) verb.GateDecision
		perform   func(workitem.Item, string) (verb.DispatchResult, error)
		outcome   string
		accepted  int
		rejected  int
	}{
		{"accept", readyReviewers, nil, nil, verb.ReadyMarked, 1, 0},
		{"one reviewer rejects", readyReviewers, oneReject, nil, verb.ReadyNotAccepted, 0, 1},
		{"performer rejects", readyReviewers, nil, func(workitem.Item, string) (verb.DispatchResult, error) {
			return verb.DispatchResult{Agent: "performer"}, &verb.PerformerReject{Notes: "no"}
		}, verb.ReadyNotAccepted, 0, 1},
		{"performer fails", readyReviewers, nil, func(workitem.Item, string) (verb.DispatchResult, error) {
			return verb.DispatchResult{Agent: "performer"}, errors.New("agent timed out")
		}, verb.ReadyNotAccepted, 0, 1},
		{"performer-only accept", "", func(workitem.Item, string) verb.GateDecision { return verb.GateDecision{} }, nil, verb.ReadyMarked, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newReadyRig(t, "ready", tc.reviewers)
			if tc.verd != nil {
				r.g.verd = tc.verd
			}
			r.d.perform = tc.perform
			a := newStoryOf(t, "feature", "a")
			res := readyCall(t, a.ID)[0]
			if res.Outcome != tc.outcome {
				t.Fatalf("outcome = %+v, want %s", res, tc.outcome)
			}
			rows := allRows(t, a.ID)
			acc, rej := ledger.EdgeRounds(rows, "backlog", "plan")
			if acc != tc.accepted || rej != tc.rejected {
				t.Errorf("edge rounds = (%d, %d), want (%d, %d)", acc, rej, tc.accepted, tc.rejected)
			}
			if got := ledger.CountRejectedRounds(rows, "backlog", "plan").Rounds; got != tc.rejected {
				t.Errorf("rejected-rounds count = %d, want %d", got, tc.rejected)
			}
			accepts := ledgerRows(t, a.ID, ledger.KindReadinessAccept)
			if (len(accepts) == 1) != (tc.outcome == verb.ReadyMarked) {
				t.Errorf("readiness accept rows = %d for outcome %s", len(accepts), tc.outcome)
			}
			if tc.outcome != verb.ReadyMarked && res.Reason == "" {
				t.Errorf("a rejected round is reported with its notes")
			}
			if statusOf(t, a.ID).Status != "backlog" {
				t.Errorf("status must be unchanged")
			}
		})
	}
}

// AC4: an accepted round followed by a performer-rejected round is one accepted
// and one rejected, by both counters; the rejected round voids the mark.
func TestAcceptedThenPerformerRejectedRoundCountsOnceEach(t *testing.T) {
	r := newReadyRig(t, "ready", readyReviewers)
	a := newStoryOf(t, "feature", "a")
	if res := readyCall(t, a.ID)[0]; res.Outcome != verb.ReadyMarked {
		t.Fatalf("first round: %+v", res)
	}
	call(t, "story-set", map[string]any{"id": a.ID, "body": "B2"}) // a definition edit, so the round runs again
	r.d.perform = func(workitem.Item, string) (verb.DispatchResult, error) {
		return verb.DispatchResult{Agent: "performer"}, &verb.PerformerReject{Notes: "gone wrong"}
	}
	if res := readyCall(t, a.ID)[0]; res.Outcome != verb.ReadyNotAccepted {
		t.Fatalf("second round: %+v", res)
	}
	rows := allRows(t, a.ID)
	if acc, rej := ledger.EdgeRounds(rows, "backlog", "plan"); acc != 1 || rej != 1 {
		t.Errorf("edge rounds = (%d, %d), want (1, 1)", acc, rej)
	}
	if got := ledger.CountRejectedRounds(rows, "backlog", "plan").Rounds; got != 1 {
		t.Errorf("rejected-rounds count = %d, want 1", got)
	}
	if holds(t, a.ID) || hasMark(t, a.ID) {
		t.Errorf("a later rejected round voids readiness and removes the mark")
	}
}

// A readiness step that declares no mark still runs the round and records it on
// the ledger; only the display tag is absent.
func TestNoMarkReadinessRecordsRound(t *testing.T) {
	r := newReadyRig(t, "", readyReviewers)
	a := newStoryOf(t, "feature", "a")
	res := readyCall(t, a.ID)[0]
	if res.Outcome != verb.ReadyMarked || !res.NoMark {
		t.Fatalf("want a round that holds with no mark configured, got %+v", res)
	}
	if !strings.Contains(res.Line(), "no mark configured") {
		t.Errorf("the line must say no mark is configured: %q", res.Line())
	}
	if got := ledgerRows(t, a.ID, ledger.KindReadinessAccept); len(got) != 1 {
		t.Errorf("want one accepted readiness round on the ledger, got %d", len(got))
	}
	if !holds(t, a.ID) {
		t.Error("readiness must hold from the ledger")
	}
	if len(statusOf(t, a.ID).Tags) != len(a.Tags) {
		t.Errorf("no tag may be applied: %v -> %v", a.Tags, statusOf(t, a.ID).Tags)
	}
	if r.d.total() == 0 {
		t.Error("the round must have been dispatched")
	}
}

// A markless epic's own round opens the gate for its children.
func TestMarklessEpicReadyAdmitsChild(t *testing.T) {
	_, epic, kids := epicRig(t, 1, "")
	err := setStatus(t, kids[0].ID, "plan")
	if err == nil || !strings.Contains(err.Error(), "has not been readied") {
		t.Fatalf("before the epic is readied the child is refused: %v", err)
	}
	res := readyCall(t, epic.ID)[0]
	if res.Outcome != verb.ReadyMarked || !res.NoMark {
		t.Fatalf("epic: %+v", res)
	}
	if got := ledgerRows(t, epic.ID, ledger.KindReadinessAccept); len(got) == 0 {
		t.Error("the epic's accepted round must be on the ledger")
	}
	if hasMark(t, epic.ID) {
		t.Error("no mark is configured, so none may be applied")
	}
	if err := setStatus(t, kids[0].ID, "plan"); err != nil && strings.Contains(err.Error(), "has not been readied") {
		t.Errorf("a child of the readied epic must pass the gate: %v", err)
	}
}

// When `story ready` can run, the gate sends the caller to it.
func TestUnreadiedEpicGateAdvisesStoryReadyWhenRunnable(t *testing.T) {
	_, epic, kids := epicRig(t, 1, "")
	err := setStatus(t, kids[0].ID, "plan")
	if err == nil || !strings.Contains(err.Error(), "run satelle story ready "+epic.ID) || strings.Contains(err.Error(), "cannot run") {
		t.Fatalf("want the runnable advice, got %v", err)
	}
}

// AC5: the tag is whatever the route says.
func TestStoryReadyMarksWithTheConfiguredTag(t *testing.T) {
	newReadyRig(t, "triaged", readyReviewers)
	a := newStoryOf(t, "feature", "a")
	if res := readyCall(t, a.ID)[0]; res.Outcome != verb.ReadyMarked {
		t.Fatalf("%+v", res)
	}
	found := false
	for _, tag := range statusOf(t, a.ID).Tags {
		if tag == "triaged" {
			found = true
		}
		if tag == "ready" {
			t.Errorf("no tag name is compiled in: got %v", statusOf(t, a.ID).Tags)
		}
	}
	if !found {
		t.Errorf("the configured tag must be set: %v", statusOf(t, a.ID).Tags)
	}
}

// AC6: readiness is read from the ledger; the tag is only a display.
func TestReadinessHoldsFromTheLedgerAlone(t *testing.T) {
	t.Run("a hand-set mark with no accept does not hold", func(t *testing.T) {
		r := newReadyRig(t, "ready", readyReviewers)
		a := newStoryOf(t, "feature", "a")
		// sty_6e3ffa2f: the hand-set mark is refused, not stored.
		if _, err := dispatchRaw(t, "story-set", map[string]any{"id": a.ID, "add_tags": []string{"ready"}}); err == nil {
			t.Fatal("a hand-set mark with no accepted readiness must be refused")
		}
		if hasMark(t, a.ID) || holds(t, a.ID) {
			t.Fatal("a hand-set tag is not an accepted readiness")
		}
		// ...and it never skips readiness: presenting the edge runs the round.
		if _, err := dispatchRaw(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}); err != nil {
			t.Fatal(err)
		}
		if r.d.total() != 1 || r.g.total() != 1 {
			t.Errorf("readiness must run in full: %d dispatches, %d gates", r.d.total(), r.g.total())
		}
	})
	t.Run("accept then a body edit", func(t *testing.T) {
		newReadyRig(t, "ready", readyReviewers)
		a := newStoryOf(t, "feature", "a")
		readyCall(t, a.ID)
		if !holds(t, a.ID) || !hasMark(t, a.ID) {
			t.Fatal("an accepted round holds and marks")
		}
		call(t, "story-set", map[string]any{"id": a.ID, "body": "changed"})
		if holds(t, a.ID) || hasMark(t, a.ID) {
			t.Errorf("a definition edit after the accept voids readiness and removes the mark")
		}
	})
	t.Run("accept then a rejected round", func(t *testing.T) {
		r := newReadyRig(t, "ready", readyReviewers)
		a := newStoryOf(t, "feature", "a")
		readyCall(t, a.ID)
		call(t, "story-set", map[string]any{"id": a.ID, "body": "changed"})
		r.g.verd = func(workitem.Item, string) verb.GateDecision { return fourReviewers("no") }
		readyCall(t, a.ID)
		if holds(t, a.ID) || hasMark(t, a.ID) {
			t.Errorf("a rejected round after an accept voids readiness")
		}
	})
	t.Run("accept then a plan attached through the story-doc-attach verb", func(t *testing.T) {
		newReadyRig(t, "ready", readyReviewers)
		a := newStoryOf(t, "feature", "a")
		readyCall(t, a.ID)
		if !hasMark(t, a.ID) {
			t.Fatal("marked after the accept")
		}
		call(t, "story-doc-attach", map[string]any{"story_id": a.ID, "name": "plan", "type": "plan", "body": "a different plan"})
		if holds(t, a.ID) || hasMark(t, a.ID) {
			t.Errorf("a plan attached after the accept voids readiness and removes the mark")
		}
	})
	// The plan document's name is the performer skill's output_name, not a name
	// the binary knows: a repo whose plan skill declares "design" is voided by a
	// re-attached design, and a document merely named "plan" is unrelated.
	t.Run("the plan document is the one the skill declares", func(t *testing.T) {
		r := newReadyRig(t, "ready", readyReviewers)
		r.d.docName = "design"
		a := newStoryOf(t, "feature", "a")
		readyCall(t, a.ID)
		if !holds(t, a.ID) || !hasMark(t, a.ID) {
			t.Fatal("an accepted round holds and marks (its own design attach is not after its accept)")
		}
		call(t, "story-doc-attach", map[string]any{"story_id": a.ID, "name": "plan", "type": "plan", "body": "an unrelated doc"})
		if !holds(t, a.ID) || !hasMark(t, a.ID) {
			t.Fatal("a document named plan is not the declared plan document and must not void readiness")
		}
		call(t, "story-doc-attach", map[string]any{"story_id": a.ID, "name": "design", "type": "design", "body": "a different design"})
		if holds(t, a.ID) || hasMark(t, a.ID) {
			t.Errorf("a re-attached design voids readiness and removes the mark")
		}
	})
	t.Run("a skill that declares no document is voided by no attach", func(t *testing.T) {
		r := newReadyRig(t, "ready", readyReviewers)
		r.d.docName = ""
		r.d.perform = func(workitem.Item, string) (verb.DispatchResult, error) {
			return verb.DispatchResult{Dispatched: true, Agent: "performer", Skill: "plan"}, nil
		}
		a := newStoryOf(t, "feature", "a")
		readyCall(t, a.ID)
		call(t, "story-doc-attach", map[string]any{"story_id": a.ID, "name": "plan", "type": "plan", "body": "p"})
		if !holds(t, a.ID) || !hasMark(t, a.ID) {
			t.Errorf("with no declared output_name the binary names no plan document")
		}
	})
	t.Run("a document that is not the plan leaves it holding", func(t *testing.T) {
		newReadyRig(t, "ready", readyReviewers)
		a := newStoryOf(t, "feature", "a")
		readyCall(t, a.ID)
		call(t, "story-doc-attach", map[string]any{"story_id": a.ID, "name": "notes", "type": "notes", "body": "n"})
		if !holds(t, a.ID) || !hasMark(t, a.ID) {
			t.Errorf("an unrelated document must not void readiness")
		}
	})
}

// AC7: a presented readiness edge whose readiness still holds runs nothing and
// records a reuse naming the accepted attempt.
func TestPresentedReadinessEdgeReusesAnAcceptedRound(t *testing.T) {
	r := newReadyRig(t, "ready", readyReviewers)
	a := newStoryOf(t, "feature", "a")
	first := readyCall(t, a.ID)[0]
	dispatches, gates := r.d.total(), r.g.total()

	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}); err != nil {
		t.Fatalf("presenting the readiness edge: %v", err)
	}
	if statusOf(t, a.ID).Status != "plan" {
		t.Fatal("the story must have been engaged")
	}
	if r.d.total() != dispatches || r.g.total() != gates {
		t.Errorf("a reused readiness runs neither performer nor reviewers: %d dispatches, %d gates (were %d, %d)", r.d.total(), r.g.total(), dispatches, gates)
	}
	reuse := ledgerRows(t, a.ID, ledger.KindReadinessReuse)
	if len(reuse) != 1 || attemptOf(t, reuse[0]) != first.Attempt {
		t.Fatalf("one reuse row naming attempt %s expected: %+v", first.Attempt, reuse)
	}
}

func TestPresentedReadinessEdgeRunsInFullAfterARejectedRound(t *testing.T) {
	r := newReadyRig(t, "ready", readyReviewers)
	a := newStoryOf(t, "feature", "a")
	readyCall(t, a.ID)
	call(t, "story-set", map[string]any{"id": a.ID, "body": "changed"})
	r.g.verd = func(workitem.Item, string) verb.GateDecision { return fourReviewers("no") }
	readyCall(t, a.ID)
	r.g.verd = func(workitem.Item, string) verb.GateDecision { return acceptAll() }
	dispatches := r.d.total()

	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": a.ID, "status": "plan"}); err != nil {
		t.Fatal(err)
	}
	if r.d.total() != dispatches+1 {
		t.Errorf("readiness must run in full: dispatches %d → %d", dispatches, r.d.total())
	}
	if got := ledgerRows(t, a.ID, ledger.KindReadinessReuse); len(got) != 0 {
		t.Errorf("nothing was reused: %+v", got)
	}
}

// AC7: an epic's ready step runs whenever the epic enters it.
func TestPresentedEpicReadyStepAlwaysRuns(t *testing.T) {
	r := newReadyRig(t, "ready", readyReviewers)
	epic := newStoryOf(t, "epic-parent", "epic", "epic:rr")
	r.g.verd = func(workitem.Item, string) verb.GateDecision { return verb.GateDecision{} }
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": epic.ID, "status": "ready"}); err != nil {
		t.Fatal(err)
	}
	if r.d.calls[epic.ID] != 1 {
		t.Errorf("the epic's ready step must run: %d dispatches", r.d.calls[epic.ID])
	}
}

// AC8: epics.
func epicRig(t *testing.T, kids int, mark string) (*readyRig, workitem.Item, []workitem.Item) {
	t.Helper()
	r := newReadyRig(t, mark, readyReviewers)
	epic := newStoryOf(t, "epic-parent", "epic", "epic:rr")
	var children []workitem.Item
	for i := 0; i < kids; i++ {
		children = append(children, newStoryOf(t, "feature", "child"+string(rune('a'+i)), "epic:rr"))
	}
	r.g.verd = func(it workitem.Item, to string) verb.GateDecision {
		if to == "ready" {
			return verb.GateDecision{} // the epic's ready step has no reviewers
		}
		return acceptAll()
	}
	return r, epic, children
}

func TestReadyEpicReadiesChildrenThenMarksIt(t *testing.T) {
	r, epic, kids := epicRig(t, 2, "ready")
	res := readyCall(t, epic.ID)[0]
	if res.Outcome != verb.ReadyMarked || res.NoMark {
		t.Fatalf("epic: %+v", res)
	}
	if statusOf(t, epic.ID).Status != "backlog" || !hasMark(t, epic.ID) {
		t.Errorf("the epic stays in backlog and is marked: %+v", statusOf(t, epic.ID))
	}
	for _, k := range kids {
		if !hasMark(t, k.ID) || r.d.calls[k.ID] != 1 {
			t.Errorf("child %s must be readied once and marked", k.ID)
		}
	}
	if r.d.calls[epic.ID] != 1 {
		t.Errorf("the epic's own step runs once: %d", r.d.calls[epic.ID])
	}
	if len(res.Children) != 2 {
		t.Errorf("both child results are reported: %+v", res.Children)
	}
}

// sty_4cbe00aa: a child on a route with no readiness step has nothing to ready, so
// it does not hold its epic back; it is reported no-readiness-step, not refused.
func TestReadyEpicMarksWithAChildOnARouteWithNoReadinessStep(t *testing.T) {
	noStepLane := `
[substrate]
obligations = ["raised", "sub-coded", "sub-closed"]
`
	noStepSteps := `
[sub-coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[sub-closed]
status = "done"
terminal = true
requires = ["sub-coded"]
`
	r := newRigWith(t, readyDoneToml+noStepLane, readyStepToml("ready", readyReviewers)+noStepSteps)
	epic := newStoryOf(t, "epic-parent", "epic", "epic:rr")
	story := newStoryOf(t, "feature", "story", "epic:rr")
	sub := newStoryOf(t, "substrate", "sub", "epic:rr")
	r.g.verd = func(it workitem.Item, to string) verb.GateDecision {
		if to == "ready" {
			return verb.GateDecision{}
		}
		return acceptAll()
	}
	res := readyCall(t, epic.ID)[0]
	if res.Outcome != verb.ReadyMarked || !res.Marked() || !hasMark(t, epic.ID) {
		t.Fatalf("the epic is marked despite the child with no readiness step: %+v", res)
	}
	if strings.Contains(res.Reason, sub.ID) {
		t.Errorf("the child with no readiness step must not hold the epic back: %q", res.Reason)
	}
	byID := map[string]verb.ReadyResult{}
	for _, c := range res.Children {
		byID[c.ID] = c
	}
	if c := byID[story.ID]; c.Outcome != verb.ReadyMarked {
		t.Errorf("the child with a readiness step is readied: %+v", c)
	}
	c := byID[sub.ID]
	if c.Outcome != verb.ReadyNoStep || !strings.Contains(c.Reason, "declares no readiness step") {
		t.Errorf("the substrate child is reported no-readiness-step: %+v", c)
	}
	if hasMark(t, sub.ID) || r.d.calls[sub.ID] != 0 {
		t.Errorf("nothing runs for a child with no readiness step")
	}
}

func TestReadyResultMarkedIgnoresNoReadinessStepChildren(t *testing.T) {
	res := verb.ReadyResult{ID: "e", Outcome: verb.ReadyMarked, Children: []verb.ReadyResult{
		{ID: "a", Outcome: verb.ReadyMarked},
		{ID: "b", Outcome: verb.ReadyNoStep},
	}}
	if !res.Marked() {
		t.Error("a no-readiness-step child counts as ready")
	}
	res.Children[1].Outcome = verb.ReadyRefused
	if res.Marked() {
		t.Error("a refused child still holds the epic back")
	}
}

// sty_a476b913: an epic's own readiness round, which readies its children, is not
// refused for the epic being un-readied; once it ran, its children may engage.
func TestStoryReadyOnUnreadiedEpicWithChildrenNotRefused(t *testing.T) {
	_, epic, kids := epicRig(t, 2, "ready")
	res := readyCall(t, epic.ID)[0]
	if res.Outcome != verb.ReadyMarked || strings.Contains(res.Reason, "has not been readied") {
		t.Fatalf("the epic's own round must not be refused: %+v", res)
	}
	for _, c := range res.Children {
		if c.Outcome != verb.ReadyMarked || strings.Contains(c.Reason, "has not been readied") {
			t.Errorf("child %s must be readied by the round: %+v", c.ID, c)
		}
	}
	if !hasMark(t, epic.ID) {
		t.Error("the epic carries the mark")
	}
	if err := setStatus(t, kids[0].ID, "plan"); err != nil && strings.Contains(err.Error(), "has not been readied") {
		t.Errorf("a child of the readied epic must pass the readiness check: %v", err)
	}
}

func TestReadyEpicWithARejectedChildIsNotMarked(t *testing.T) {
	r, epic, kids := epicRig(t, 2, "ready")
	bad := kids[1].ID
	r.g.verd = func(it workitem.Item, to string) verb.GateDecision {
		switch {
		case to == "ready":
			return verb.GateDecision{}
		case it.ID == bad:
			return fourReviewers("no")
		}
		return acceptAll()
	}
	res := readyCall(t, epic.ID)[0]
	if res.Outcome != verb.ReadyNotAccepted || !strings.Contains(res.Reason, bad) {
		t.Fatalf("epic must not be accepted, naming %s: %+v", bad, res)
	}
	if hasMark(t, epic.ID) || !hasMark(t, kids[0].ID) || hasMark(t, bad) {
		t.Errorf("only the accepted child is marked")
	}
	if r.d.calls[bad] != 2 {
		t.Errorf("a rejected child is re-checked once after the epic judgement: %d dispatches", r.d.calls[bad])
	}
	if r.d.calls[kids[0].ID] != 1 {
		t.Errorf("an accepted child is not run again: %d", r.d.calls[kids[0].ID])
	}
	if r.d.calls[epic.ID] != 1 {
		t.Errorf("the epic-level judgement still runs: %d", r.d.calls[epic.ID])
	}
}

// A child rejected in the first pass that the epic-level judgement fixed is
// accepted on the one re-check, and the epic is then marked.
func TestReadyEpicMarksWhenARejectedChildIsAcceptedOnTheRecheck(t *testing.T) {
	r, epic, kids := epicRig(t, 2, "ready")
	bad := kids[1].ID
	r.g.verd = func(it workitem.Item, to string) verb.GateDecision {
		switch {
		case to == "ready":
			return verb.GateDecision{}
		case it.ID == bad && r.d.calls[epic.ID] == 0: // before the epic judgement ran
			return fourReviewers("no")
		}
		return acceptAll()
	}
	res := readyCall(t, epic.ID)[0]
	if res.Outcome != verb.ReadyMarked {
		t.Fatalf("the epic is marked once the child is accepted on the re-check: %+v", res)
	}
	if r.d.calls[bad] != 2 || !hasMark(t, bad) || !hasMark(t, epic.ID) {
		t.Errorf("child %s: %d dispatches, marked=%v; epic marked=%v", bad, r.d.calls[bad], hasMark(t, bad), hasMark(t, epic.ID))
	}
	// One result per child, the latest: the epic reads as marked end to end, so
	// `story ready` prints each child once and exits zero.
	if len(res.Children) != 2 || !res.Marked() {
		t.Errorf("want one result per child and the epic Marked(), got %d children, Marked=%v: %+v", len(res.Children), res.Marked(), res.Children)
	}
	seen := map[string]int{}
	for _, c := range res.Children {
		seen[c.ID]++
	}
	if seen[bad] != 1 || seen[kids[0].ID] != 1 {
		t.Errorf("each child is reported once, got %v", seen)
	}
}

func TestReadyEpicReadiesAChildCreatedDuringTheEpicJudgement(t *testing.T) {
	r, epic, _ := epicRig(t, 1, "ready")
	var created workitem.Item
	r.d.perform = func(it workitem.Item, to string) (verb.DispatchResult, error) {
		if it.ID == epic.ID && created.ID == "" {
			created = newStoryOf(t, "feature", "filed by ready-review", "epic:rr")
		}
		if to == "plan" {
			call(t, "story-doc-attach", map[string]any{"story_id": it.ID, "name": "plan", "type": "plan", "body": "the plan"})
		}
		return verb.DispatchResult{Dispatched: true, Agent: "performer", Skill: "plan"}, nil
	}
	res := readyCall(t, epic.ID)[0]
	if created.ID == "" {
		t.Fatal("the judgement must have filed a child")
	}
	if res.Outcome != verb.ReadyMarked || !hasMark(t, created.ID) || !hasMark(t, epic.ID) {
		t.Fatalf("the new child is readied before the epic is marked: %+v", res)
	}
}

// AC9: a story not at the start status, or over its round budget, is refused with
// the reason while the others in the call still run.
func TestStoryReadyRefusesPerStoryAndRunsTheRest(t *testing.T) {
	r := newReadyRig(t, "ready", readyReviewers)
	started := newStoryOf(t, "feature", "started")
	spent := newStoryOf(t, "feature", "spent")
	good := newStoryOf(t, "feature", "good")
	if _, err := dispatchRaw(t, "story-set", map[string]any{"id": started.ID, "status": "plan"}); err != nil {
		t.Fatal(err)
	}
	r.g.verd = func(it workitem.Item, to string) verb.GateDecision {
		if it.ID == spent.ID || to != "plan" {
			return fourReviewers("no")
		}
		return acceptAll()
	}
	for i := 0; i < 3; i++ { // reject_budget = 3 rejected rounds
		if res := readyCall(t, spent.ID)[0]; res.Outcome != verb.ReadyNotAccepted {
			t.Fatalf("round %d: %+v", i+1, res)
		}
	}
	res := readyCall(t, started.ID, spent.ID, good.ID, "sty_missing")
	want := []string{verb.ReadyRefused, verb.ReadyRefused, verb.ReadyMarked, verb.ReadyRefused}
	for i, w := range want {
		if res[i].Outcome != w {
			t.Fatalf("result %d = %+v, want %s", i, res[i], w)
		}
	}
	if !strings.Contains(res[0].Reason, "start status") {
		t.Errorf("not-at-start reason: %q", res[0].Reason)
	}
	if !strings.Contains(res[1].Reason, "reject budget") {
		t.Errorf("spent-budget reason: %q", res[1].Reason)
	}
	if !hasMark(t, good.ID) {
		t.Errorf("the story after the refused ones still ran")
	}
}
