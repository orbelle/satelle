package web

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wfdot"
)

// routeDocs builds the two halves of a derived route the numbering reads. The
// web layer resolves a lifecycle only through internal/wfdot, so a fixture has
// to be authored in the same grammar the substrate is (sty_085e1a5a) — TOML,
// with a `[meta]` header rather than a `---` block (sty_81bb0dde). A
// category-specific lane is a `[<category>]` TABLE, not a second workflow file.
func routeDocs(done, step string) []docindex.Doc {
	mk := func(name, what, body string) docindex.Doc {
		return docindex.Doc{Kind: "workflows", Name: name,
			Body: "[meta]\nname = \"" + name + "\"\ntype = \"workflow\"\nscope = \"project\"\ndescription = \"" + what + "\"\n\n" + body}
	}
	return []docindex.Doc{
		mk("done", "fixture declaration of done", done),
		mk("step", "fixture step catalogue", step),
	}
}

// TestCategoryStepOf: each item is numbered against the workflow ACTIVE for its
// category — an epic-parent against the parent workflow (done = step 1), a
// wildcard category against the project workflow (done = step 4) — never a single
// hardcoded longest-spine resolver (sty_8dafac0e).
func TestCategoryStepOf(t *testing.T) {
	stepOf := categoryStepOf(routeDocs(
		`["*"]
obligations = ["raised", "coded", "pushed", "committed", "closed"]

[epic-parent]
obligations = ["raised", "children-resolved"]

[parent]
obligations = ["raised", "children-resolved"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[pushed]
status = "commit_push"
agent = "executor"
requires = ["coded"]

[committed]
status = "committed"
agent = "executor"
requires = ["pushed"]

[closed]
status = "done"
terminal = true
requires = ["committed"]

[children-resolved]
status = "done"
terminal = true
requires = ["raised"]
`))

	if got := stepOf("epic-parent", "done"); got != 1 {
		t.Errorf("epic-parent done = %d, want 1 (parent workflow)", got)
	}
	if got := stepOf("feature", "done"); got != 4 {
		t.Errorf("feature done = %d, want 4 (wildcard project workflow)", got)
	}
	// An unknown category with a wildcard present still resolves to the wildcard.
	if got := stepOf("", "done"); got != 4 {
		t.Errorf("empty category done = %d, want 4 (wildcard)", got)
	}
}

// TestCategoryStepOfActiveWorkflowWins reproduces the double-"1" bug (sty_1b548d7e):
// when BOTH the embedded system baseline (backlog→in_progress→done) and the repo
// project workflow (…→integration→commit_push→committed→done) carry applies_to
// ["*"], a wildcard category must be numbered against the ACTIVE (repo) project
// workflow — not whichever wildcard appears first in doc order. With the baseline
// winning, integration was off-spine (step 0) and collided with in_progress at
// step 1 (rendering ①①②③④). Baseline is listed FIRST here — the order that
// triggered the bug.
func TestCategoryStepOfActiveWorkflowWins(t *testing.T) {
	// The repo's own route outranks the shipped one, so the numbering follows the
	// authored spine (sty_3795e7f6).
	authored := routeDocs(
		`["*"]
obligations = ["raised", "coded", "integrated", "pushed", "committed", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[integrated]
status = "integration"
agent = "executor"
requires = ["coded"]

[pushed]
status = "commit_push"
agent = "executor"
requires = ["integrated"]

[committed]
status = "committed"
agent = "executor"
requires = ["pushed"]

[closed]
status = "done"
terminal = true
requires = ["committed"]
`)
	shipped := routeDocs(`["*"]
obligations = ["raised", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	for i := range shipped {
		shipped[i].Embedded = true
	}
	stepOf := categoryStepOf(authored)
	_ = shipped
	for state, want := range map[string]int{"in_progress": 1, "integration": 2, "commit_push": 3, "committed": 4, "done": 5} {
		if got := stepOf("chore", state); got != want {
			t.Errorf("chore %q = %d, want %d (active project workflow must beat the embedded baseline)", state, got, want)
		}
	}
}

// ev builds a ledger entry with a {from,to} payload.
func ev(kind, from, to string) ledger.Entry {
	p, _ := json.Marshal(lightPayload{From: from, To: to})
	return ledger.Entry{Kind: kind, Payload: p}
}

// evA is ev with the attempt id the gate run stamped on the row.
func evA(kind, from, to, attempt string) ledger.Entry {
	p, _ := json.Marshal(map[string]string{"from": from, "to": to, "attempt": attempt})
	return ledger.Entry{Kind: kind, Payload: p}
}

// names lists the stage names in order.
func names(ss []stageVM) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}

// stageNamed returns the stage called name, or the zero value.
func stageNamed(ss []stageVM, name string) stageVM {
	for _, s := range ss {
		if s.Name == name {
			return s
		}
	}
	return stageVM{}
}

// testStep is the step resolver for the simple test lifecycle
// open(0) → in_progress(1) → done(2).
func testStep(s string) int { return map[string]int{"in_progress": 1, "done": 2}[s] }

// noStep is a category that resolved no route spine: every state is depth 0.
func noStep(string) int { return 0 }

func TestBuildStagesRetriedEdgeKeepsItsRounds(t *testing.T) {
	chrono := []ledger.Entry{
		ev(ledger.KindReviewAccept, "open", "in_progress"),
		ev(ledger.KindStatusTransition, "open", "in_progress"),
		ev(ledger.KindReviewReject, "in_progress", "done"),
		ev(ledger.KindReviewAccept, "in_progress", "done"),
		ev(ledger.KindStatusTransition, "in_progress", "done"),
	}
	stages := buildStages(chrono, "done", false, testStep, wfdot.Spec{}, nil, nil)
	if got, want := names(stages), []string{"in_progress", "done"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
	if s := stages[0]; s.accepted != 1 || s.rejected != 0 {
		t.Errorf("in_progress = %+v, want 1 accepted 0 rejected", s)
	}
	if s := stages[1]; s.accepted != 1 || s.rejected != 1 {
		t.Errorf("done = %+v, want 1 accepted 1 rejected (the reject is not turned green by the later accept)", s)
	}
	for _, s := range stages {
		if s.State == "current" {
			t.Errorf("a done story must show no current stage: %+v", stages)
		}
	}
}

// A skill-level accept whose nested seats include a dissent is not a rejection:
// buildStages keys off the row kind; it does not re-vote the seats.
func TestBuildStagesPanelDissentIsNotARejection(t *testing.T) {
	p, err := json.Marshal(map[string]any{
		"from": "open", "to": "in_progress", "accept": true,
		"seats": []map[string]any{
			{"seat": "a", "accept": true},
			{"seat": "b", "accept": false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := []ledger.Entry{
		{Kind: ledger.KindReviewAccept, Payload: p},
		ev(ledger.KindStatusTransition, "open", "in_progress"),
	}
	stages := buildStages(entries, "in_progress", false, testStep, wfdot.Spec{}, nil, nil)
	if s := stageNamed(stages, "in_progress"); s.rejected != 0 || s.accepted != 1 {
		t.Fatalf("nested dissent must not count as a rejected round: %+v", stages)
	}
}

func TestBuildStagesCurrentStagePulses(t *testing.T) {
	// A story sitting IN in_progress: the entry is the current stage, not a
	// completed one.
	chrono := []ledger.Entry{
		ev(ledger.KindReviewAccept, "open", "in_progress"),
		ev(ledger.KindStatusTransition, "open", "in_progress"),
	}
	stages := buildStages(chrono, "in_progress", false, testStep, wfdot.Spec{}, nil, nil)
	if len(stages) != 1 || stages[0].State != "current" || stages[0].Name != "in_progress" {
		t.Fatalf("want [current in_progress], got %+v", stages)
	}
}

func TestBuildStagesPriorStageDoneCurrentPulses(t *testing.T) {
	chrono := []ledger.Entry{
		ev(ledger.KindReviewAccept, "open", "in_progress"),
		ev(ledger.KindStatusTransition, "open", "in_progress"),
		ev(ledger.KindReviewAccept, "in_progress", "release"),
		ev(ledger.KindStatusTransition, "in_progress", "release"),
	}
	stages := buildStages(chrono, "release", false, noStep, wfdot.Spec{}, nil, nil)
	if got, want := names(stages), []string{"in_progress", "release"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
	if stages[0].State != "done" || stages[1].State != "current" {
		t.Errorf("states = %s,%s, want done,current", stages[0].State, stages[1].State)
	}
}

func TestBuildStagesUngatedStageHasNoCounts(t *testing.T) {
	chrono := []ledger.Entry{ev(ledger.KindStatusTransition, "open", "in_progress")}
	stages := buildStages(chrono, "done", false, testStep, wfdot.Spec{}, nil, nil)
	if len(stages) != 2 || stages[0].Name != "in_progress" || stages[0].accepted+stages[0].rejected != 0 {
		t.Fatalf("want an ungated in_progress stage with no counts, got %+v", stages)
	}
	for _, s := range stages {
		if s.out != nil {
			t.Errorf("no review rows must show no pending gate, got %+v", s)
		}
	}
}

func TestBuildStagesUnstartedHasNoStage(t *testing.T) {
	// A freshly-created item at its initial state shows nothing — no phantom stage.
	if got := buildStages(nil, "open", false, testStep, wfdot.Spec{}, nil, nil); len(got) != 0 {
		t.Fatalf("unstarted open item should have no stages, got %v", got)
	}
	if got := buildStages([]ledger.Entry{ev(ledger.KindStoryCreated, "", "")}, "open", false, testStep, wfdot.Spec{}, nil, nil); len(got) != 0 {
		t.Fatalf("created-only item should have no stages, got %v", got)
	}
}

// TestBuildStagesStartingSeat: a pre-transition seat emits a single current chip
// titled "starting"; without a seat the cell stays blank; once a transition lands
// the real current stage takes over (sty_e1314fe3).
func TestBuildStagesStartingSeat(t *testing.T) {
	stages := buildStages(nil, "backlog", true, projStep, wfdot.Spec{}, nil, nil)
	if len(stages) != 1 || stages[0].State != "current" || stages[0].Title != "starting" {
		t.Fatalf("seat-held unentered = %+v, want one current chip titled starting", stages)
	}
	if got := buildStages(nil, "backlog", false, projStep, wfdot.Spec{}, nil, nil); len(got) != 0 {
		t.Fatalf("no-seat backlog: want no stages, got %v", got)
	}
	chrono := []ledger.Entry{
		ev(ledger.KindReviewAccept, "backlog", "in_progress"),
		ev(ledger.KindStatusTransition, "backlog", "in_progress"),
	}
	stages = buildStages(chrono, "in_progress", true, projStep, wfdot.Spec{}, nil, nil)
	if len(stages) != 1 || stages[0].Name != "in_progress" || stages[0].Title == "starting" {
		t.Fatalf("entered with seat: want one real in_progress stage, got %+v", stages)
	}
}

func TestBuildStagesFollowTheLedgerNotRouteDepth(t *testing.T) {
	// A ledger recording a deeper step before a shallower one: stages are in the
	// order they were reached, never re-sorted by depth.
	chrono := []ledger.Entry{
		ev(ledger.KindStatusTransition, "in_progress", "done"),
		ev(ledger.KindStatusTransition, "open", "in_progress"),
	}
	stages := buildStages(chrono, "done", false, testStep, wfdot.Spec{}, nil, nil)
	if got, want := names(stages), []string{"done", "in_progress"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
}

func TestBuildStagesRetriedStageKeepsOneEntry(t *testing.T) {
	chrono := []ledger.Entry{
		ev(ledger.KindReviewReject, "open", "in_progress"),
		ev(ledger.KindReviewAccept, "open", "in_progress"),
		ev(ledger.KindStatusTransition, "open", "in_progress"),
	}
	stages := buildStages(chrono, "in_progress", false, testStep, wfdot.Spec{}, nil, nil)
	if len(stages) != 1 {
		t.Fatalf("want one stage, got %+v", stages)
	}
	if s := stages[0]; s.State != "current" || s.accepted != 1 || s.rejected != 1 {
		t.Errorf("stage = %+v, want current with 1 accepted 1 rejected", s)
	}
}

func TestBuildStagesChronologicalWithRetry(t *testing.T) {
	chrono := []ledger.Entry{
		ev(ledger.KindStatusTransition, "a", "b"),
		ev(ledger.KindStatusTransition, "b", "c"),
		ev(ledger.KindReviewReject, "c", "d"),
		ev(ledger.KindStatusTransition, "c", "d"),
		ev(ledger.KindStatusTransition, "d", "e"),
		ev(ledger.KindStatusTransition, "e", "f"),
	}
	stages := buildStages(chrono, "f", false, noStep, wfdot.Spec{}, nil, nil)
	if got, want := names(stages), []string{"b", "c", "d", "e", "f"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
	if stageNamed(stages, "d").rejected != 1 {
		t.Errorf("d = %+v, want 1 rejected", stageNamed(stages, "d"))
	}
	if last := stages[len(stages)-1]; last.State != "current" {
		t.Errorf("the last stage must be the current one, got %q", last.State)
	}
}

// TestSpineDepths: the route's spine numbering still drives which statuses are
// on-route, and an off-route exit (a park state) is never numbered. Stated as a
// Spec literal now that the DOT front end is retired (sty_d953c5d8).
func TestSpineDepths(t *testing.T) {
	spec := wfdot.Spec{
		States: []wfdot.State{
			{Name: "open", Shape: "Mdiamond"},
			{Name: "planned"},
			{Name: "in_progress", Agent: "executor"},
			{Name: "blocked", Agent: "reviewer"},
			{Name: "reviewed"},
			{Name: "done", Shape: "Msquare"},
		},
		Transitions: []wfdot.Transition{
			{From: "open", To: "planned", Skill: "a"},
			{From: "planned", To: "in_progress", Skill: "b"},
			{From: "in_progress", To: "blocked"},
			{From: "blocked", To: "in_progress"},
			{From: "in_progress", To: "reviewed", Skill: "c"},
			{From: "reviewed", To: "done", Skill: "d"},
		},
	}
	d := spineDepths(spec)
	for st, want := range map[string]int{"planned": 1, "in_progress": 2, "reviewed": 3, "done": 4} {
		if d[st] != want {
			t.Errorf("depth[%s] = %d, want %d", st, d[st], want)
		}
	}
	if _, ok := d["blocked"]; ok {
		t.Error("a park state must not be numbered — it is off-route")
	}
}

// projSpec mirrors the project workflow: executor steps (in_progress, commit_push)
// are NOT gated, a recovery back-edge (committed→in_progress) and a cancelled
// detour exist.
func projSpec() wfdot.Spec {
	return wfdot.Spec{
		States: []wfdot.State{
			{Name: "backlog"}, {Name: "in_progress", Agent: "executor"},
			{Name: "commit_push", Agent: "executor"}, {Name: "committed", Agent: "reviewer"},
			{Name: "done", Agent: "reviewer", Terminal: true, Shape: "Msquare"},
			{Name: "cancelled", Agent: "reviewer", Terminal: true},
		},
		Transitions: []wfdot.Transition{
			{From: "backlog", To: "in_progress"},
			{From: "in_progress", To: "commit_push"},
			{From: "commit_push", To: "committed"},
			{From: "committed", To: "done"},
			{From: "committed", To: "in_progress"}, // recovery back-edge
			{From: "backlog", To: "cancelled"},
			{From: "in_progress", To: "cancelled"},
		},
	}
}

func TestSpineDepthsProjectShape(t *testing.T) {
	d := spineDepths(projSpec())
	for st, want := range map[string]int{"in_progress": 1, "commit_push": 2, "committed": 3, "done": 4} {
		if d[st] != want {
			t.Errorf("spineDepths[%s] = %d, want %d (full=%v)", st, d[st], want, d)
		}
	}
	if _, ok := d["cancelled"]; ok {
		t.Errorf("cancelled must be off the spine, got %d", d["cancelled"])
	}
	if _, ok := d["backlog"]; ok {
		t.Errorf("backlog (start) must be omitted, got %d", d["backlog"])
	}
}

// projStep is the step resolver derived from the project spine.
func projStep(s string) int { return spineDepths(projSpec())[s] }

func TestBuildStagesFullSpineEachOnce(t *testing.T) {
	chrono := []ledger.Entry{
		ev(ledger.KindReviewAccept, "backlog", "in_progress"),
		ev(ledger.KindStatusTransition, "backlog", "in_progress"),
		ev(ledger.KindStatusTransition, "in_progress", "commit_push"),
		ev(ledger.KindReviewAccept, "commit_push", "committed"),
		ev(ledger.KindStatusTransition, "commit_push", "committed"),
		ev(ledger.KindReviewAccept, "committed", "done"),
		ev(ledger.KindStatusTransition, "committed", "done"),
	}
	stages := buildStages(chrono, "done", false, projStep, wfdot.Spec{}, nil, nil)
	if got, want := names(stages), []string{"in_progress", "commit_push", "committed", "done"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
}

// A done-review reject, then the committed→in_progress recovery loop: a real
// backward edge. The stages that repeat keep ONE entry each, in the order first
// reached, and the reject stays a red round on done.
func TestBuildStagesRecoveryLoopListsEachStageOnce(t *testing.T) {
	chrono := []ledger.Entry{
		ev(ledger.KindStatusTransition, "backlog", "in_progress"),
		ev(ledger.KindStatusTransition, "in_progress", "commit_push"),
		ev(ledger.KindReviewAccept, "commit_push", "committed"),
		ev(ledger.KindStatusTransition, "commit_push", "committed"),
		ev(ledger.KindReviewReject, "committed", "done"),
		ev(ledger.KindStatusTransition, "committed", "in_progress"),
		ev(ledger.KindStatusTransition, "in_progress", "commit_push"),
		ev(ledger.KindReviewAccept, "commit_push", "committed"),
		ev(ledger.KindStatusTransition, "commit_push", "committed"),
		ev(ledger.KindReviewAccept, "committed", "done"),
		ev(ledger.KindStatusTransition, "committed", "done"),
	}
	stages := buildStages(chrono, "done", false, projStep, wfdot.Spec{}, nil, nil)
	if got, want := names(stages), []string{"in_progress", "commit_push", "committed", "done"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %v, want %v (no stage twice)", got, want)
	}
	if s := stageNamed(stages, "done"); s.rejected != 1 || s.accepted != 1 {
		t.Errorf("done = %+v, want 1 accepted 1 rejected", s)
	}
	if s := stageNamed(stages, "committed"); s.accepted != 2 {
		t.Errorf("committed = %+v, want 2 accepted rounds across the loop", s)
	}
}

// A story AT release that attempted release→done and was rejected: release is
// the current stage, done has not been reached, and the rejected outgoing round
// shows as the pending gate on the release chip — never as a stage.
func TestBuildStagesRejectPastCurrentIsPending(t *testing.T) {
	chrono := []ledger.Entry{
		ev(ledger.KindStatusTransition, "a", "b"),
		ev(ledger.KindStatusTransition, "b", "c"),
		ev(ledger.KindStatusTransition, "c", "d"),
		ev(ledger.KindStatusTransition, "d", "release"),
		ev(ledger.KindReviewReject, "release", "done"),
	}
	stages := buildStages(chrono, "release", false, noStep, wfdot.Spec{}, nil, nil)
	if got, want := names(stages), []string{"b", "c", "d", "release"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
	if last := stages[3]; last.State != "current" || last.rejected != 0 {
		t.Errorf("release = %+v, want current with no counts of its own", last)
	}
	if p := stages[3].out; p == nil || p.rejected != 1 || p.accepted != 0 || p.Edge != "release → done" {
		t.Errorf("pending = %+v, want 1 rejected (the outgoing release→done round)", p)
	}
}

// TestBuildStagesStatusAloneCurrentStage: empty ledger + on-route status yields a
// current stage independent of seat (sty_c5065d05 AC4/AC5).
func TestBuildStagesStatusAloneCurrentStage(t *testing.T) {
	noSeat := buildStages(nil, "in_progress", false, projStep, wfdot.Spec{}, nil, nil)
	withSeat := buildStages(nil, "in_progress", true, projStep, wfdot.Spec{}, nil, nil)
	if len(noSeat) != 1 || noSeat[0].State != "current" || noSeat[0].Name != "in_progress" {
		t.Fatalf("status alone: want one current in_progress, got %v", noSeat)
	}
	if !reflect.DeepEqual(noSeat, withSeat) {
		t.Fatalf("seat flicker: noSeat=%v withSeat=%v", noSeat, withSeat)
	}
}
