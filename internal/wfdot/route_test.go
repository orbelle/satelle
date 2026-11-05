package wfdot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtures(t *testing.T) (done, step string) {
	t.Helper()
	d, err := os.ReadFile(filepath.Join("testdata", "done.toml"))
	if err != nil {
		t.Fatalf("read done.toml: %v", err)
	}
	s, err := os.ReadFile(filepath.Join("testdata", "step.toml"))
	if err != nil {
		t.Fatalf("read step.toml: %v", err)
	}
	return string(d), string(s)
}

func buildFixture(t *testing.T, tags []string) Spec {
	t.Helper()
	done, step := fixtures(t)
	spec, err := ParseRoute(done, step, "feature", tags)
	if err != nil {
		t.Fatalf("ParseRoute: %v", err)
	}
	return spec
}

// TestParseRouteBuildsValidSpec is AC1: two markdown files in, a valid Spec out,
// with no DOT text anywhere in the path.
func TestParseRouteBuildsValidSpec(t *testing.T) {
	spec := buildFixture(t, nil)
	if problems := Validate(spec); len(problems) != 0 {
		t.Fatalf("derived spec does not validate: %v", problems)
	}
	if got := spec.Start(); got != "backlog" {
		t.Errorf("Start() = %q, want backlog", got)
	}
	for _, tc := range []struct {
		from, to string
	}{
		{"backlog", "plan"}, {"plan", "in_progress"}, {"in_progress", "integration"},
		{"integration", "release"}, {"release", "done"},
	} {
		if !spec.HasEdge(tc.from, tc.to) {
			t.Errorf("spine edge %s->%s missing", tc.from, tc.to)
		}
	}
	wantPerforming := []string{"plan", "in_progress", "integration", "release"}
	if got := spec.PerformingStates(); !sameStrings(got, wantPerforming) {
		t.Errorf("PerformingStates() = %v, want %v", got, wantPerforming)
	}
	wantEdit := []string{"in_progress", "integration", "release"}
	if got := spec.EditCapableStates(); !sameStrings(got, wantEdit) {
		t.Errorf("EditCapableStates() = %v, want %v", got, wantEdit)
	}
	if !spec.IsTerminalState("done") {
		t.Error("done must be terminal")
	}
	if !spec.IsParkState("blocked") || !spec.IsParkState("cancelled") {
		t.Error("blocked and cancelled must be park/role states")
	}
	if !spec.IsResumePark("blocked") {
		t.Error("blocked is the from=* resume park")
	}
	if spec.IsResumePark("cancelled") {
		t.Error("cancelled is not a resume park")
	}
}

// TestSynthesisedTopologyValidates is AC2: the author declares no cancel, park or
// backward edge, and all three appear — carrying their role state's own gate.
func TestSynthesisedTopologyValidates(t *testing.T) {
	done, step := fixtures(t)
	for _, forbidden := range []string{"[cancelled]", "[blocked]"} {
		if strings.Contains(step, forbidden) {
			t.Fatalf("step.toml declares %q — topology must be synthesised, not authored", forbidden)
		}
	}
	if strings.Contains(done, "->") {
		t.Fatal("done.toml names an edge — topology must be synthesised, not authored")
	}

	spec := buildFixture(t, nil)
	if problems := Validate(spec); len(problems) != 0 {
		t.Fatalf("synthesised spec does not validate: %v", problems)
	}
	for _, want := range []string{
		"backlog->cancelled", "plan->cancelled", "in_progress->cancelled",
		"integration->cancelled", "release->cancelled", "blocked->cancelled",
		"plan->blocked", "in_progress->blocked", "integration->blocked", "release->blocked",
		"integration->in_progress", "release->in_progress",
	} {
		from, to, _ := strings.Cut(want, "->")
		if !spec.HasEdge(from, to) {
			t.Errorf("synthesised edge %s missing", want)
		}
	}
	// The start state can park (sty_b8a0d062): a wrong premise at ready
	// sets blocked instead of leaving a green backlog pill. A terminal
	// still cannot be abandoned.
	if !spec.HasEdge("backlog", "blocked") {
		t.Error("start state must be able to park as blocked")
	}
	if spec.HasEdge("done", "cancelled") || spec.HasEdge("done", "blocked") {
		t.Error("terminal state must not gain cancel or park edges")
	}
	// Role-state gates ride the synthesised edges, or the reviewers are lost.
	idx := map[string]Transition{}
	for _, tr := range spec.Transitions {
		idx[tr.From+"->"+tr.To] = tr
	}
	if got := idx["in_progress->cancelled"].Skill; got != "satelle-story-cancel-review" {
		t.Errorf("cancel edge gate = %q, want satelle-story-cancel-review", got)
	}
	if got := idx["in_progress->blocked"].Skill; got != "satelle-story-blocked-review" {
		t.Errorf("park edge gate = %q, want satelle-story-blocked-review", got)
	}
	if got := idx["backlog->blocked"].Skill; got != "satelle-story-blocked-review" {
		t.Errorf("start park edge gate = %q, want satelle-story-blocked-review", got)
	}
	// The park advisor is DECLARED (`park.advisor` in done.toml) but is
	// deliberately absent from the Spec: flat dispatch means entry fires nothing,
	// so who to consult is an instruction to the orchestrator carried on the
	// route, not topology (sty_05a5e203).
	if agent, _ := spec.StateAgent("blocked"); agent != "reviewer" {
		t.Errorf("park role agent = %q, want reviewer", agent)
	}
}

// TestRouteOrderFromPrerequisites is AC3: declaration order is scrambled, and the
// route still comes out in prerequisite order.
func TestRouteOrderFromPrerequisites(t *testing.T) {
	step := `[released]
status = "release"
agent = "executor"
requires = ["coded"]

[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]
`
	done := `[feature]
obligations = ["raised", "coded", "released"]
`
	spec, err := ParseRoute(done, step, "feature", nil)
	if err != nil {
		t.Fatalf("ParseRoute: %v", err)
	}
	var names []string
	for _, st := range spec.States {
		names = append(names, st.Name)
	}
	if want := []string{"backlog", "in_progress", "release"}; !sameStrings(names, want) {
		t.Errorf("derived order = %v, want %v", names, want)
	}
}

func TestRouteCycleIsError(t *testing.T) {
	step := `[x]
status = "a"
agent = "executor"
requires = ["y"]

[y]
status = "b"
agent = "executor"
requires = ["x"]
`
	_, err := ParseRoute(`[feature]
obligations = ["x", "y"]
`, step, "feature", nil)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("a prerequisite cycle must be an error naming it, got %v", err)
	}
}

func TestRouteOrphanObligationNamesIt(t *testing.T) {
	step := `[x]
status = "a"
agent = "executor"
`
	_, err := ParseRoute(`[feature]
obligations = ["x", "deployed"]
`, step, "feature", nil)
	if err == nil || !strings.Contains(err.Error(), "deployed") {
		t.Fatalf("an obligation with no discharging step must be NAMED, got %v", err)
	}
}

// TestParseStepsModel (sty_7069bced / epic:model-selection order:3): a step's
// optional model= key parses onto Step.Model and survives into the derived
// Spec's State — the per-dispatch override config.SelectModel's step tier reads.
func TestParseStepsModel(t *testing.T) {
	step := `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "coder"
model = "haiku"
skills = ["coder"]
requires = ["raised"]
`
	cat, err := ParseSteps(step)
	if err != nil {
		t.Fatalf("ParseSteps: %v", err)
	}
	var got string
	for _, st := range cat.Steps {
		if st.Name == "in_progress" {
			got = st.Model
		}
	}
	if got != "haiku" {
		t.Fatalf("Step.Model = %q, want haiku", got)
	}

	done := `["*"]
obligations = ["raised", "coded"]
`
	spec, err := ParseRoute(done, step, "feature", nil)
	if err != nil {
		t.Fatalf("ParseRoute: %v", err)
	}
	found := false
	for _, s := range spec.States {
		if s.Name == "in_progress" {
			found = true
			if s.Model != "haiku" {
				t.Errorf("State.Model = %q, want haiku", s.Model)
			}
		}
	}
	if !found {
		t.Fatalf("in_progress state missing from assembled spec")
	}
}

func TestUnresolvedObligations(t *testing.T) {
	step := `[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "planner"
requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["planned"]
`
	cat, err := ParseSteps(step)
	if err != nil {
		t.Fatalf("ParseSteps: %v", err)
	}

	t.Run("fail: obligation is a status not a table key", func(t *testing.T) {
		lists, err := ParseDone(`["*"]
obligations = ["raised", "in_progress", "coded"]
`)
		if err != nil {
			t.Fatalf("ParseDone: %v", err)
		}
		got := UnresolvedObligations(lists, cat)
		if len(got) != 1 {
			t.Fatalf("want 1 problem, got %v", got)
		}
		if !strings.Contains(got[0], "in_progress") || !strings.Contains(got[0], "*") || !strings.Contains(got[0], "has no discharging step") {
			t.Errorf("problem must name category, obligation, and why: %q", got[0])
		}
	})

	t.Run("fail: unresolved tag_obligation", func(t *testing.T) {
		lists, err := ParseDone(`["*"]
obligations = ["raised", "coded"]
[["*".tag_obligation]]
tag = "surface:ui"
obligation = "styled"
`)
		if err != nil {
			t.Fatalf("ParseDone: %v", err)
		}
		got := UnresolvedObligations(lists, cat)
		if len(got) != 1 || !strings.Contains(got[0], "styled") {
			t.Fatalf("want unresolved tag obligation styled, got %v", got)
		}
	})

	t.Run("pass: every obligation is a table key", func(t *testing.T) {
		lists, err := ParseDone(`["*"]
obligations = ["raised", "planned", "coded"]
`)
		if err != nil {
			t.Fatalf("ParseDone: %v", err)
		}
		if got := UnresolvedObligations(lists, cat); len(got) != 0 {
			t.Fatalf("legal route must report nothing, got %v", got)
		}
	})

	t.Run("pass: seeded testdata fixtures", func(t *testing.T) {
		done, stepBody := fixtures(t)
		lists, err := ParseDone(done)
		if err != nil {
			t.Fatalf("ParseDone testdata: %v", err)
		}
		seedCat, err := ParseSteps(stepBody)
		if err != nil {
			t.Fatalf("ParseSteps testdata: %v", err)
		}
		if got := UnresolvedObligations(lists, seedCat); len(got) != 0 {
			t.Fatalf("seeded testdata must resolve, got %v", got)
		}
	})

	t.Run("parity with SelectSteps", func(t *testing.T) {
		lists, err := ParseDone(`[feature]
obligations = ["raised", "in_progress"]
`)
		if err != nil {
			t.Fatalf("ParseDone: %v", err)
		}
		got := UnresolvedObligations(lists, cat)
		if len(got) != 1 {
			t.Fatalf("want 1 problem, got %v", got)
		}
		_, selErr := SelectSteps(lists[0], cat, nil)
		if selErr == nil {
			t.Fatal("SelectSteps must fail the same fixture")
		}
		if got[0] != selErr.Error() {
			t.Errorf("checker %q != SelectSteps %q", got[0], selErr.Error())
		}
	})
}

func TestRouteUnknownCategoryIsError(t *testing.T) {
	done, step := fixtures(t)
	_, err := ParseRoute(done, step, "nonesuch", nil)
	if err == nil || !strings.Contains(err.Error(), "nonesuch") {
		t.Fatalf("an unknown category must be an error naming it, got %v", err)
	}
}

// TestStepReviewersAreConcurrentAndComplete is AC4: every declared reviewer
// survives into the transition, and two or more run concurrently by default.
func TestStepReviewersAreConcurrentAndComplete(t *testing.T) {
	spec := buildFixture(t, nil)
	idx := map[string]Transition{}
	for _, tr := range spec.Transitions {
		idx[tr.From+"->"+tr.To] = tr
	}

	plan := idx["plan->in_progress"]
	want := []string{
		"satelle-story-plan-review",
		"satelle-story-architecture-review",
		"satelle-story-integration-coverage-review",
	}
	if !sameStrings(plan.Skills, want) {
		t.Errorf("plan gates = %v, want %v (order preserved, none dropped)", plan.Skills, want)
	}
	if plan.Skill != want[0] {
		t.Errorf("legacy Skill = %q, want %q", plan.Skill, want[0])
	}
	if plan.Parallel != DefaultParallelCap {
		t.Errorf("three unset reviewers must default to concurrent, got parallel=%d", plan.Parallel)
	}
	if single := idx["integration->release"]; single.Parallel != 0 {
		t.Errorf("a single reviewer needs no concurrency, got parallel=%d", single.Parallel)
	}
	// An authored value wins, including an explicit 0 on a multi-reviewer step.
	if authored := idx["in_progress->integration"]; authored.Parallel != 0 {
		t.Errorf("authored parallel: 0 must win, got %d", authored.Parallel)
	}
	if got := len(idx["in_progress->integration"].Skills); got != 3 {
		t.Errorf("authored parallel must not drop reviewers, got %d", got)
	}
}

// TestTagObligationsAppend covers the "tags append obligations" rule end to end.
func TestTagObligationsAppend(t *testing.T) {
	done := `[feature]
obligations = ["raised", "coded"]

[[feature.tag_obligation]]
tag = "surface:ui"
obligation = "styled"
`
	step := `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[styled]
status = "style"
agent = "executor"
requires = ["coded"]
`

	plain, err := ParseRoute(done, step, "feature", nil)
	if err != nil {
		t.Fatalf("ParseRoute (untagged): %v", err)
	}
	if plain.HasEdge("in_progress", "style") {
		t.Error("untagged story must not get the tag-appended step")
	}
	tagged, err := ParseRoute(done, step, "feature", []string{"surface:ui"})
	if err != nil {
		t.Fatalf("ParseRoute (tagged): %v", err)
	}
	if !tagged.HasEdge("in_progress", "style") {
		t.Error("surface:ui story must get the tag-appended step")
	}
}

// A key no wire struct claims is an ERROR, never a silent drop: a typo'd
// `reviewrs =` that parsed as "no reviewers" would lose a gate, and a route that
// quietly loses a gate is the one failure this representation must not have. The
// message names the FILE and the offending key path, because that is what an
// author needs to fix it.
func TestParseErrorsNameTheKey(t *testing.T) {
	const doneOK = "[feature]\nobligations = [\"x\"]\n"
	const stepOK = "[x]\nstatus = \"in_progress\"\n"
	for _, tc := range []struct {
		name, done, step string
		want             []string
	}{
		{
			name: "unknown done key",
			done: "[feature]\nobligations = [\"x\"]\nwibble = \"y\"\n", step: stepOK,
			want: []string{"done.toml", "unknown key", "feature.wibble"},
		},
		{
			name: "unknown step key",
			done: doneOK, step: "[x]\nstatus = \"in_progress\"\nreviewrs = [\"gate-a\"]\n",
			want: []string{"step.toml", "unknown key", "x.reviewrs"},
		},
		{
			name: "non-numeric parallel",
			done: doneOK, step: "[x]\nstatus = \"in_progress\"\nparallel = \"lots\"\n",
			want: []string{"step.toml", "parallel"},
		},
		{
			// A step with no status has no stage name for an item to hold, so the
			// route has nowhere to put it.
			name: "step without a status",
			done: doneOK, step: "[x]\nagent = \"executor\"\n",
			want: []string{"step.toml", `step "x"`, "no status"},
		},
		{
			// Malformed TOML is reported by the real parser, with a line number —
			// the whole point of retiring the hand-rolled reader.
			name: "malformed toml",
			done: "[feature\nobligations = [\"x\"]\n", step: stepOK,
			want: []string{"done.toml", "line 2", "end table name"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRoute(tc.done, tc.step, "feature", nil)
			if err == nil {
				t.Fatalf("want an error, got none")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error must contain %q, got: %v", want, err)
				}
			}
		})
	}
}

func TestMultiSkillStepIsError(t *testing.T) {
	step := `[raised]
status = "backlog"
start = true

[x]
status = "a"
agent = "executor"
skills = ["one", "two"]
requires = ["raised"]
`
	_, err := ParseRoute(`[feature]
obligations = ["raised", "x"]
`, step, "feature", nil)
	if err == nil || !strings.Contains(err.Error(), "one rubric") {
		t.Fatalf("a multi-skill spine step must be an error, got %v", err)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
