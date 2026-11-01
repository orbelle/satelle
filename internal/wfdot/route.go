// Route construction: an obligation list plus a step catalogue in, a Spec out.
//
// This is now the ONLY constructor onto Spec. BuildRoute reads a declaration of
// done (done.toml) and a step catalogue (step.toml) and DERIVES the lifecycle. Every
// consumer downstream — Validate, the reachability predicates,
// ScopedReviewersSplit, ExecutorSkillsFor, the engine, the seat, the edit gate —
// sees a normal Spec. That is the whole point: the graph stopped being authored
// without the machinery that reads it changing at all (sty_d953c5d8).
//
// What the AUTHOR declares: obligations, steps, gates, and each step's
// prerequisites. What the BINARY owns: order (a topological sort of the
// prerequisites, never an agent's choice) and topology (cancel from every
// non-terminal step, park from anywhere, backward movement) — the boilerplate
// every authored workflow in this repo repeats identically today.
//
// No repo-specific rule belongs in this file. Skill names, category names and
// gate bindings are authored data; this code only arranges them.
package wfdot

import (
	"fmt"
	"sort"
)

// RemoteWait is a step's declared wait on a team remote's run for the story:
// the remote's name, and the status each outcome the run concludes with moves the
// story to. Both are the repo's vocabulary; the binary names neither.
type RemoteWait struct {
	Remote string
	// On maps a reported outcome to the status the story moves to. Every target
	// is an edge the route declares from the waiting step (checked at assembly).
	On map[string]string
}

// Step is one entry in the catalogue. It is deliberately flat — stage name,
// agent, skills, reviewers — per the epic's "a step is agents, skills,
// reviewers". Order is NOT a field: it is derived from Requires/Provides, so the
// orchestrator chooses the SET and never the SEQUENCE.
type Step struct {
	// Name is the stage name — the status a story holds while in this step.
	Name string
	// Agent allocates the performer: executor (in-loop), a named isolated agent
	// (planner, coder), or reviewer for a non-performing role state.
	Agent string
	// Model names a model for this step's dispatch — one precedence tier below
	// the allocated binding's own agents.toml model=, above the inherited/
	// creator session tiers (config.SelectModel, sty_7069bced). Empty means no
	// step-level override. Distinct from the retired DOT edge model=
	// (sty_a476a2f8): that was a gate override on an edge and stays retired;
	// this is a per-dispatch instruction for the step's PERFORMER.
	Model string
	// Skills are the executor rubrics this step performs. A spine step carries
	// one; declaring more is a construction error naming the step.
	Skills []string
	// Reviewers are the gates on ENTRY to this step, all-must-accept.
	Reviewers []string
	// ReviewerAgent is the agents.toml binding the entry gates run under.
	// Empty means [reviewer].
	ReviewerAgent string
	// Panel is the ordered list of agents.toml sections that judge this step's
	// entry reviewers, one cold run each. Empty means the single ReviewerAgent
	// seat. Combine names the functional-check skill that folds those runs; a
	// one-seat panel may omit it.
	Panel   []string
	Combine string
	// Parallel is the entry gates' concurrency cap. Unset with two or more
	// reviewers defaults to DefaultParallelCap: concurrency is the shape's rule,
	// not an authored opt-in. An authored value always wins.
	Parallel int
	// ParallelSet distinguishes an authored parallel: 0 from an absent one.
	ParallelSet bool
	// Bundle asks that the entry gates which share a binding, model, effort and
	// tool grant run as ONE reviewer session returning one verdict per rubric
	// (sty_23e10d92). Absent or false leaves every gate its own session.
	Bundle bool
	// ReviewerCarry is the step's entry-gate carry policy (sty_59720b8b). Empty
	// is today's behaviour: every reviewer is dispatched every round.
	// CarryUntilAccept lets a reviewer whose earlier accept still quotes the
	// current material go undispatched while another reviewer rejects; the round
	// that would advance the edge re-dispatches every carried reviewer.
	ReviewerCarry string
	// FailFast names the entry reviewers (a subset of Reviewers and Panel
	// skills) that run in fail-fast review mode: stop at the first blocking
	// finding and record where. Every reviewer not named runs full.
	FailFast []string
	// Provides is the obligation this step discharges. A step with no Provides
	// is a role state (start or terminal marker), not an obligation-bearer.
	Provides string
	// Requires are the obligations that must be discharged before this step.
	Requires []string
	// AppliesTo scopes the step to stories carrying a matching tag.
	AppliesTo []string
	// Advisor is a named agent the ORCHESTRATOR may consult on this step, and the
	// rubric it consults under. It is a declaration, never a dispatch: under flat
	// dispatch the orchestrator is the sole scheduler, so an advisor advises the
	// orchestrator and is never fired by entry to a state (sty_05a5e203). It is
	// deliberately absent from the emitted Spec — Spec is topology; who to consult
	// is an instruction to the orchestrator, carried on the route.
	Advisor string
	// AdvisorSkill is the rubric Advisor is consulted under.
	AdvisorSkill string
	// ReworkConsult / ReworkRounds declare the step's bounded rework loop: the
	// binding the orchestrator may converse with about this slice, and how many
	// rounds that conversation may run before it must stop (sty_8e0b29a0). Like
	// Advisor it is a DECLARATION, never a dispatch — entering the state fires
	// nothing; the orchestrator opens `satelle story rework` and the relay's
	// outcome is a signal, never a verdict. Empty ReworkConsult means no loop.
	// Also deliberately absent from the emitted Spec: Spec is topology, and a
	// consult budget is an instruction to the orchestrator.
	ReworkConsult string
	ReworkRounds  int
	// Propose runs this step's performer BEFORE its entry gates, on the source
	// status, so the gates judge the artifact the performer produced (a plan
	// that must exist before the readiness reviewers read it). Without it the
	// gates run first and the performer after they accept. Mechanism only: it
	// reorders two calls the binary already makes and decides no verdict
	// (sty_5262592e).
	Propose bool
	// Mark is the tag a readiness round that was accepted puts on the story
	// (`satelle story ready`): a display marker only, the ledger decides whether
	// readiness holds. The binary compiles in no tag name; a readiness step that
	// declares none cannot be readied without engagement (sty_a32bd6a9).
	Mark string
	// Freeze makes entry to this step the point where a story's definition (title,
	// body, acceptance criteria, category) freezes. Without any step declaring it
	// the legacy rule applies: the definition freezes when the story leaves the
	// entry state.
	Freeze bool
	// UntrustedInput declares that the text this step's seats read is untrusted
	// (a story raised by an external party). It has no runtime effect: validate
	// refuses a performer or reviewer on the step whose grant is not read-only.
	// The step's author declares it; the binary never keys it on a step name.
	UntrustedInput bool
	// PRStep marks the step that raises the pull request. It splits a finished
	// item's cycle time into the time to reach it and the time from it to the
	// terminal state; a route that marks none has no second phase.
	PRStep bool
	// ApplyCriteriaDoc / ApplyCriteriaHeading declare that entry to this step
	// applies the acceptance criteria authored under "## <heading>" in the story
	// document <doc> to the story. Mechanism only: the binary copies the section
	// the route names; which document and heading is configuration. Both empty
	// means the step applies nothing (sty_4d9df9a0).
	ApplyCriteriaDoc     string
	ApplyCriteriaHeading string
	// ApplyChangeOps / ApplyChangeTags declare the changes a propose step's
	// performer may report and the tag prefixes it may add (sty_af0b1761).
	ApplyChangeOps  []string
	ApplyChangeTags []string
	// RejectBudget is the most rejected presentations of this step's entry edge
	// before the orchestrator must park the story and ask the developer. Zero
	// means no declared budget. The unit is a PRESENTATION (one gate run),
	// however many reviewers it fanned out to.
	RejectBudget int
	// AnswerProgress turns on the unanswered-findings refusal for a propose step
	// (sty_73224b86): a re-presentation whose performer answers or changes fewer
	// than MinProgress of the prior blocking findings (capped at the number
	// offered) is refused before its gates run. Off when the table is absent or
	// enabled = false. MinProgress is always >= 1 once set.
	AnswerProgress bool
	MinProgress    int
	// WaitsOnChildren marks a step at which a container idles while its child
	// stories are driven. The seat holder there is not performing anything, so
	// the edit gate and Stop hook do not count it while children are open
	// (sty_7f3e6fd3). Authored in step.toml; the binary never names the status.
	WaitsOnChildren bool
	// ReadyChildren is the authored `ready_children` key; nil when absent. Read
	// through ReadiesChildren, never this field.
	ReadyChildren *bool
	// Schedule is the DEPRECATED authored child-schedule key (SchedParallel or
	// SchedSequential), kept as written so validate can name it
	// (LegacyScheduleSteps). Empty when absent. Read the resolved schedule
	// through ChildSchedule, never this field.
	Schedule string
	// Sequential is the authored `sequential = true` override of the parallel
	// default, legal only on a waits_on_children step.
	Sequential bool
	// AfterChildren is the obligation every child must have discharged on its own
	// route before a container may enter this step. The step may name a performer
	// like any other; the binary only gates entry and the seat. Empty means the
	// step has no such wait.
	AfterChildren string
	// RemoteAgent names the agents.toml cloud binding that performs this step for
	// a child placed remote, and LocalTags are the tags that pin a child to the
	// step's own Agent (sty_dde8b6a4). Declarations only: the placement rule is
	// mechanism, the tag vocabulary is the repo's. Empty RemoteAgent means the
	// step never runs remotely.
	RemoteAgent string
	LocalTags   []string
	// PlacementTag is the tag prefix under which a child carries its placement
	// pin ("in-loop", "local" or "team:<path>"), chosen by the orchestrator at
	// readiness. Empty means the step honours no pin. The prefix is the repo's
	// vocabulary; the binary only reads the value after it.
	PlacementTag string
	// DeliveryHeading is the heading, in the readiness document, under which the
	// performer records the orchestrator's reason for each child's delivery
	// choice. Empty means the step records none.
	DeliveryHeading string
	// LaneTag is the tag prefix under which a child carries its landing lane;
	// `satelle story wave` prints the value after it. The prefix is the repo's
	// vocabulary, so the binary names none. Empty means no lane is printed.
	LaneTag string
	// RemoteWait declares that a story holding this step waits on a team
	// remote's run for it and maps the run's outcomes to edges. Nil when the step
	// waits on no remote.
	RemoteWait *RemoteWait
	// Wait marks a step at which a story rests between performing steps — for
	// example finished work waiting for its release. It is neither parked nor
	// terminal: entering it frees the seat and the tree and records a queue
	// entry, and nothing performs there. The step names no agent.
	Wait bool
	// Start marks the entry state; Terminal marks a terminal success state.
	Start    bool
	Terminal bool
}

// RouteGate is an always-on gate: a reviewer that judges entry to named steps
// rather than occupying a stage of its own — a `## gate <skill>` section with an
// `on:` list.
type RouteGate struct {
	// Skill is the reviewer rubric.
	Skill string
	// Agent is the agents.toml binding; empty means [reviewer]. A coded check
	// names none — the engine returns before gate binding.
	Agent string
	// On names the steps this gate fires on entry to. "*" means every step.
	On []string
	// AppliesTo scopes the gate to stories carrying a matching tag.
	AppliesTo []string
	// For scopes the gate to named declaration-of-done SECTIONS — the categories
	// whose route it belongs to. Empty means every route in the catalogue.
	//
	// The catalogue is shared, so without this a deployment gate on `done` would
	// fire on every category's terminal step, including the routes that
	// deliberately have no release to verify. AppliesTo cannot express it: that
	// axis is the story's TAGS, this one is which route the gate is part of
	// (sty_9835070d). A section is named by its category, so the wildcard section
	// is named `*` here like anywhere else.
	For []string
	// Mandatory marks a gate whose failure must be surfaced, not swallowed.
	Mandatory bool
	// When names a functional-check skill whose ```check the engine runs, in the
	// engaged worktree, before enqueuing this gate. Exit 0 runs the gate, exit 1
	// skips it, anything else runs it: a broken precondition never drops a gate.
	// It decides whether a judgement is worth its cost, never the judgement, and
	// the rule lives entirely in that skill's script.
	When string
	// Panel / Combine are this gate's own seats and fold check. They apply only
	// to this skill.
	Panel   []string
	Combine string
	// ReviewMode is this gate's review mode: empty (full) or ReviewModeFailFast.
	// It covers the gate and every seat of its panel.
	ReviewMode string
}

// List is one category's declaration of done: the obligations that must be
// discharged, in order, plus the role states the binary synthesises rather than
// having them authored once per workflow.
type List struct {
	// Category is the story category this list applies to.
	Category string
	// Obligations are the things that must be true before the work is done.
	Obligations []string
	// TagObligations append to Obligations when the story carries the tag.
	TagObligations map[string][]string
	// Park is the park state synthesised from every non-terminal step.
	Park string
	// ParkGate is the reviewer that judges entry to Park.
	ParkGate string
	// ParkAdvisor / ParkAdvisorSkill name the advisor the orchestrator consults
	// when it parks a story. Declared, never dispatched (see Step.Advisor).
	ParkAdvisor      string
	ParkAdvisorSkill string
	// Cancel is the cancel sink synthesised from every non-terminal step.
	Cancel string
	// CancelGate is the reviewer that judges entry to Cancel.
	CancelGate string
	// Recovers are the backward movements the binary owns rather than the author
	// drawing them: each names a step later steps return to. A route may declare
	// more than one (release→in_progress, plan→backlog).
	Recovers []Recover
}

// Recover is one declared backward movement: Step is the step a later step
// returns to. From names the steps that may move back to it; empty means every
// spine step after Step.
type Recover struct {
	Step string
	From []string
}

// Catalogue is the step catalogue: every step and gate available to any category.
// A category's route selects from it by obligation.
type Catalogue struct {
	Steps []Step
	Gates []RouteGate
}

// ObligationsFor returns the category's obligations plus any appended by the
// story's tags, in declaration order with tag-appended obligations last.
func (l List) ObligationsFor(tags []string) []string {
	out := append([]string(nil), l.Obligations...)
	seen := map[string]bool{}
	for _, o := range out {
		seen[o] = true
	}
	// Sorted keys: map iteration order must not reach the route.
	var keys []string
	for k := range l.TagObligations {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !tagsMatchAppliesTo([]string{k}, tags) {
			continue
		}
		for _, o := range l.TagObligations[k] {
			if !seen[o] {
				seen[o], out = true, append(out, o)
			}
		}
	}
	return out
}

// BuildRoute derives a Spec for one category from its declaration of done and
// the step catalogue, for a story carrying tags.
//
// Errors are construction failures the author must fix, and each NAMES the thing
// at fault: an obligation nothing discharges, a prerequisite nothing provides, a
// cycle. A route that cannot be built must never degrade into a partial one —
// that would silently drop gates, which is exactly the authority this
// representation exists to preserve.
func BuildRoute(l List, cat Catalogue, tags []string) (Spec, error) {
	ordered, err := SelectSteps(l, cat, tags)
	if err != nil {
		return Spec{}, err
	}
	if err := checkAfterChildrenRoute(l.Category, ordered, cat); err != nil {
		return Spec{}, err
	}
	return assemble(ordered, cat.Gates, l)
}

// unresolvedMsg is the ONE wording for "this obligation has no discharging
// step". SelectSteps (derivation) and UnresolvedObligations (validate) both
// use it so an author sees one diagnosis rather than two (sty_5d712bc5).
func unresolvedMsg(obligation, category string) string {
	return fmt.Sprintf("obligation %q in category %q has no discharging step", obligation, category)
}

func providedSet(cat Catalogue) map[string]bool {
	provided := map[string]bool{}
	for _, st := range cat.Steps {
		if st.Provides != "" {
			provided[st.Provides] = true
		}
	}
	return provided
}

// UnresolvedObligations reports every declarable obligation across lists that
// no step in cat discharges, in the same words route derivation uses.
//
// This is a SUPERSET of SelectSteps: it checks l.Obligations and every
// TagObligations value unconditionally. SelectSteps resolves the tag-filtered
// ObligationsFor(tags) for one story; this checker must not call SelectSteps
// (that would reintroduce tag-conditional blindness). Resolve against
// Step.Provides (the table key), never Step.Name (the status).
func UnresolvedObligations(lists []List, cat Catalogue) []string {
	provided := providedSet(cat)
	var out []string
	seen := map[string]bool{}
	report := func(o, category string) {
		msg := unresolvedMsg(o, category)
		if seen[msg] {
			return
		}
		seen[msg] = true
		out = append(out, msg)
	}
	for _, l := range lists {
		for _, o := range l.Obligations {
			if !provided[o] {
				report(o, l.Category)
			}
		}
		var keys []string
		for k := range l.TagObligations {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, o := range l.TagObligations[k] {
				if !provided[o] {
					report(o, l.Category)
				}
			}
		}
	}
	return out
}

// RouteErrors assembles every category's route and reports each construction
// refusal once, as "<category>: <reason>". A route is built for a story's tags,
// so a refusal that only a tag-appended step provokes is found by also building
// with each tag a category appends obligations for, and with all of them. It
// skips a category whose obligations do not resolve — UnresolvedObligations
// already names that, in the same words.
func RouteErrors(lists []List, cat Catalogue) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		tagSets := [][]string{nil}
		var all []string
		for k := range l.TagObligations {
			all = append(all, k)
		}
		sort.Strings(all)
		for _, k := range all {
			tagSets = append(tagSets, []string{k})
		}
		if len(all) > 1 {
			tagSets = append(tagSets, all)
		}
		for _, tags := range tagSets {
			if _, err := BuildRoute(l, cat, tags); err != nil {
				msg := l.Category + ": " + err.Error()
				if !seen[msg] {
					seen[msg] = true
					out = append(out, msg)
				}
			}
		}
	}
	return out
}

// SelectSteps returns the steps a category's route walks, topologically ordered.
//
// This is the ONE answer to "which steps did this category ask for". The
// catalogue is SHARED and stage names repeat across route families by design, so
// anything keying the catalogue by step name attaches to routes that never
// selected that section — the defect class behind sty_ee0f4ae6 and sty_a7316b06.
// A consumer that needs per-route step data takes this slice; it must never walk
// Catalogue.Steps itself.
//
// Exported rather than duplicated because a second selection rule is a second
// answer, and the two would drift.
func SelectSteps(l List, cat Catalogue, tags []string) ([]Step, error) {
	want := l.ObligationsFor(tags)
	wanted := map[string]bool{}
	for _, o := range want {
		wanted[o] = true
	}

	provided := providedSet(cat)
	for _, o := range want {
		if !provided[o] {
			return nil, fmt.Errorf("%s", unresolvedMsg(o, l.Category))
		}
	}

	// Select: obligation-bearing steps the category asked for, plus the role
	// states (start / terminal) that carry no obligation of their own.
	var selected []Step
	for _, st := range cat.Steps {
		if st.Provides == "" || wanted[st.Provides] {
			selected = append(selected, st)
		}
	}

	// Refuse an ambiguous selection BEFORE the sort. topoSortSteps keys its maps
	// by step name, so a duplicate would resolve last-wins and drop a step — with
	// its reviewers — out of the route silently. Checking afterwards would mean
	// reporting on already-corrupted state.
	if err := checkSelectionUnambiguous(selected, l.Category); err != nil {
		return nil, err
	}
	return topoSortSteps(selected)
}

// checkSelectionUnambiguous refuses a selected step set that cannot be turned
// into a route without picking a silent winner.
//
// Two collisions matter, and both are one authored done.toml edit away — a
// category listing two obligations whose steps share a stage name, or two steps
// declaring the same `provides:`. Downstream, each resolves last-wins: a step
// vanishes from the route carrying its reviewers with it, which is exactly the
// "route that quietly loses a gate" this representation exists to prevent.
//
// The check is on the SELECTED set, never the catalogue. The catalogue
// legitimately holds several steps per stage name — four `## done` sections is
// the normal shape, one per route family — and only their co-selection is the
// defect. Errors name the offending SECTIONS (name plus provides) because the
// stage name alone cannot identify which two of them to fix.
func checkSelectionUnambiguous(selected []Step, category string) error {
	firstByName := map[string]Step{}
	for _, st := range selected {
		if prev, dup := firstByName[st.Name]; dup {
			return fmt.Errorf(
				"route for category %q selects two steps named %q (provides %s and %s); "+
					"a route must select at most one step per stage name",
				category, st.Name, quoteProvides(prev), quoteProvides(st))
		}
		firstByName[st.Name] = st
	}
	firstByProvides := map[string]Step{}
	for _, st := range selected {
		if st.Provides == "" {
			continue
		}
		if prev, dup := firstByProvides[st.Provides]; dup {
			return fmt.Errorf(
				"route for category %q selects two steps providing obligation %q (steps %q and %q); "+
					"an obligation must have exactly one discharging step on a route",
				category, st.Provides, prev.Name, st.Name)
		}
		firstByProvides[st.Provides] = st
	}
	// At most one step of a route may declare the freeze point: two would leave
	// "where does the definition freeze" with a silent winner.
	var freezeAt string
	for _, st := range selected {
		if !st.Freeze {
			continue
		}
		if freezeAt != "" {
			return fmt.Errorf(
				"route for category %q selects two steps declaring freeze (%q and %q); "+
					"a route freezes the definition at exactly one step",
				category, freezeAt, st.Name)
		}
		freezeAt = st.Name
	}
	return nil
}

// quoteProvides renders a step's obligation for an error message. A role state
// carries none, and an empty %q would read as a step that provides the empty
// string rather than one that provides nothing.
func quoteProvides(st Step) string {
	if st.Provides == "" {
		return "(no provides)"
	}
	return fmt.Sprintf("%q", st.Provides)
}

// assemble turns ordered steps, gates and the list's role-state declarations into
// a Spec. Split from BuildRoute so the selection rules and the emission rules
// stay separately readable.
func assemble(ordered []Step, gates []RouteGate, l List) (Spec, error) {
	spec := Spec{}
	for _, st := range ordered {
		shape := ""
		switch {
		case st.Start:
			shape = "Mdiamond"
		case st.Terminal:
			shape = "Msquare"
		}
		if len(st.Skills) > 1 {
			return Spec{}, fmt.Errorf(
				"step %q declares %d skills; a spine step carries one rubric — declare the rest as gates",
				st.Name, len(st.Skills))
		}
		spec.States = append(spec.States, State{
			Name:  st.Name,
			Agent: st.Agent,
			Model: st.Model,
			Skill: firstOf(st.Skills),

			Obligation: st.Provides,
			Shape:      shape,
			AppliesTo:  st.AppliesTo,

			Propose:      st.Propose,
			Mark:         st.Mark,
			Freeze:       st.Freeze,
			RejectBudget: st.RejectBudget,

			AnswerProgress: st.AnswerProgress,
			MinProgress:    st.MinProgress,

			UntrustedInput: st.UntrustedInput,
			PRStep:         st.PRStep,

			ApplyCriteriaDoc:     st.ApplyCriteriaDoc,
			ApplyCriteriaHeading: st.ApplyCriteriaHeading,
			ApplyChangeOps:       st.ApplyChangeOps,
			ApplyChangeTags:      st.ApplyChangeTags,

			WaitsOnChildren: st.WaitsOnChildren,
			ReadyChildren:   st.ReadiesChildren(),
			Schedule:        st.ChildSchedule(),
			AfterChildren:   st.AfterChildren,
			RemoteAgent:     st.RemoteAgent,
			LocalTags:       st.LocalTags,
			PlacementTag:    st.PlacementTag,
			DeliveryHeading: st.DeliveryHeading,
			LaneTag:         st.LaneTag,
			RemoteWait:      st.RemoteWait,
			Wait:            st.Wait,
		})
	}

	// Gates become edge-less nodes. Their node name carries no behavioural
	// contract — a gate is identified by its skill — so it is derived. A gate
	// scoped to other sections is not part of THIS route and emits nothing.
	for _, g := range gates {
		if !GateInRoute(g, l.Category) {
			continue
		}
		spec.States = append(spec.States, State{
			Name:       "gate_" + g.Skill,
			Agent:      g.Agent,
			Skill:      g.Skill,
			On:         g.On,
			AppliesTo:  g.AppliesTo,
			Mandatory:  g.Mandatory,
			When:       g.When,
			Panel:      g.Panel,
			Combine:    g.Combine,
			ReviewMode: g.ReviewMode,
		})
	}

	// Spine edges: consecutive steps, gated on entry by the TARGET step's
	// reviewers. Gates belong to the step they admit, not to the edge — that is
	// the shape change, and it is why a step definition is self-sufficient.
	var spine []Step
	for _, st := range ordered {
		if st.Agent == "reviewer" && !st.Terminal {
			continue // role state, not on the spine
		}
		spine = append(spine, st)
	}
	for i := 0; i+1 < len(spine); i++ {
		from, to := spine[i], spine[i+1]
		spec.Transitions = append(spec.Transitions, Transition{
			From:          from.Name,
			To:            to.Name,
			Skill:         firstOf(to.Reviewers),
			Skills:        to.Reviewers,
			Agent:         to.ReviewerAgent,
			Parallel:      parallelFor(to),
			Bundle:        to.Bundle && len(to.Reviewers) > 1,
			ReviewerCarry: to.ReviewerCarry,
			Panel:         to.Panel,
			Combine:       to.Combine,
			FailFast:      to.FailFast,
		})
	}

	// Synthesised topology — the edges the binary owns rather than the author
	// drawing them once per workflow. A role state's own gate admits entry to it,
	// so the synthesised edges carry that skill: the same propagation Parse
	// performs for an authored agent=reviewer node. Synthesising the edge without
	// its gate would silently drop the cancel and park reviewers.
	if l.Cancel != "" {
		spec.States = append(spec.States, State{
			Name: l.Cancel, Agent: "reviewer", Skill: l.CancelGate,
		})
		for _, st := range spine {
			if st.Terminal {
				continue
			}
			spec.Transitions = append(spec.Transitions, roleEdge(st.Name, l.Cancel, l.CancelGate))
		}
	}
	if l.Park != "" {
		spec.States = append(spec.States, State{
			Name: l.Park, Agent: "reviewer", Skill: l.ParkGate, From: []string{"*"},
		})
		// Park-from-anywhere: From holds the wildcard, but every consumer reads
		// explicit inbound edges, so the constructor expands it here. The start
		// state is included so a wrong premise at ready can park as blocked
		// (sty_b8a0d062). A terminal still cannot be abandoned.
		for _, st := range spine {
			if st.Terminal {
				continue
			}
			spec.Transitions = append(spec.Transitions, roleEdge(st.Name, l.Park, l.ParkGate))
		}
		if l.Cancel != "" {
			spec.Transitions = append(spec.Transitions, roleEdge(l.Park, l.Cancel, l.CancelGate))
		}
	}
	for _, r := range l.Recovers {
		for _, name := range recoverSources(r, spine) {
			spec.Transitions = append(spec.Transitions, Transition{From: name, To: r.Step})
		}
	}
	if err := checkRemoteWaits(spec, ordered); err != nil {
		return Spec{}, err
	}
	if err := checkWaitExits(spec, ordered, l); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

// checkWaitExits refuses a wait step with no way onward on this route. Park and
// cancel abandon the wait rather than end it, so they do not count: a story
// resting at the step must have an edge that takes it on.
func checkWaitExits(spec Spec, ordered []Step, l List) error {
	for _, st := range ordered {
		if !st.Wait {
			continue
		}
		onward := false
		for _, to := range spec.Successors(st.Name) {
			if to != l.Park && to != l.Cancel {
				onward = true
			}
		}
		if !onward {
			return fmt.Errorf("step %q: wait = true, but the route declares no edge onward from it (declared from %s: %v) — a story resting there could only be parked or cancelled", st.Name, st.Name, spec.Successors(st.Name))
		}
	}
	return nil
}

// checkRemoteWaits refuses a remote_wait outcome mapped to a status the route
// does not declare an edge to from the waiting step. It runs once every edge
// exists — spine, cancel, park and recover — and asks the spec's own edge table,
// so a map can name a recovery or park edge but never invent a move.
func checkRemoteWaits(spec Spec, ordered []Step) error {
	for _, st := range ordered {
		if st.RemoteWait == nil {
			continue
		}
		outcomes := make([]string, 0, len(st.RemoteWait.On))
		for o := range st.RemoteWait.On {
			outcomes = append(outcomes, o)
		}
		sort.Strings(outcomes)
		for _, o := range outcomes {
			target := st.RemoteWait.On[o]
			if !spec.Declares(st.Name, target) {
				return fmt.Errorf(
					"step %q: remote_wait maps outcome %q to %q, but the route declares no edge %s → %s (a remote_wait outcome may only take a spine, recover, park or cancel edge; declared from %s: %v)",
					st.Name, o, target, st.Name, target, st.Name, spec.Successors(st.Name))
			}
		}
	}
	return nil
}

// parallelFor is the concurrency cap for a step's entry gates. Two or more
// reviewers run concurrently by default: all-must-accept with no short-circuit is
// the shape's rule, so a route cannot accidentally serialise a gate set by
// forgetting to opt in. An authored value always wins, including an explicit 0.
func parallelFor(st Step) int {
	if st.ParallelSet {
		return st.Parallel
	}
	if len(st.Reviewers) > 1 {
		return DefaultParallelCap
	}
	return 0
}

// roleEdge is an edge into a role state, carrying that state's own gate. Agent is
// left empty because empty means [reviewer] — the same binding an authored
// role-state edge carries, and naming it explicitly would read as divergence.
func roleEdge(from, to, gate string) Transition {
	tr := Transition{From: from, To: to}
	if gate != "" {
		tr.Skill, tr.Skills = gate, []string{gate}
	}
	return tr
}

// recoverSources returns the steps that may move backward to r.Step: the
// explicit From list, or every spine step after Step.
func recoverSources(r Recover, spine []Step) []string {
	if len(r.From) > 0 {
		return r.From
	}
	var out []string
	seen := false
	for _, st := range spine {
		if st.Name == r.Step {
			seen = true
			continue
		}
		if seen && !st.Terminal {
			out = append(out, st.Name)
		}
	}
	return out
}

// topoSortSteps orders steps by their prerequisite obligations — the rule that
// order is derived, never chosen by an agent. Declaration order breaks ties, so
// the result is deterministic.
// Its name- and obligation-keyed maps would resolve last-wins on a duplicate;
// that branch is unreachable because BuildRoute establishes the invariant first
// via checkSelectionUnambiguous.
func topoSortSteps(steps []Step) ([]Step, error) {
	provider := map[string]string{} // obligation -> step name
	for _, st := range steps {
		if st.Provides != "" {
			provider[st.Provides] = st.Name
		}
	}
	index := map[string]int{}
	for i, st := range steps {
		index[st.Name] = i
	}
	deps := map[string][]string{}
	for _, st := range steps {
		for _, req := range st.Requires {
			p, ok := provider[req]
			if !ok {
				return nil, fmt.Errorf(
					"step %q requires obligation %q which no selected step provides", st.Name, req)
			}
			deps[st.Name] = append(deps[st.Name], p)
		}
	}
	var out []Step
	state := map[string]int{} // 0 unvisited, 1 visiting, 2 done
	var failure error
	var visit func(name string) bool
	visit = func(name string) bool {
		switch state[name] {
		case 2:
			return true
		case 1:
			failure = fmt.Errorf("prerequisite cycle through step %q", name)
			return false
		}
		state[name] = 1
		prereqs := append([]string(nil), deps[name]...)
		sort.Slice(prereqs, func(i, j int) bool { return index[prereqs[i]] < index[prereqs[j]] })
		for _, p := range prereqs {
			if !visit(p) {
				return false
			}
		}
		state[name] = 2
		out = append(out, steps[index[name]])
		return true
	}
	for _, st := range steps {
		if !visit(st.Name) {
			return nil, failure
		}
	}
	return out, nil
}

func firstOf(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

// GateInRoute reports whether a gate belongs to the route being built for
// category. A gate with no `for:` belongs to every route — that is the common
// case and stays unstated. A gate that names sections belongs only to those,
// matched against the section's own name (the wildcard section is `*`).
//
// Exported so a RENDERER can name the gates a `for:` excluded (sty_a989764d).
// An excluded gate is deliberately absent from the Spec, so the only honest way
// to report it is to ask this same predicate rather than re-derive the rule.
func GateInRoute(g RouteGate, category string) bool {
	if len(g.For) == 0 {
		return true
	}
	for _, c := range g.For {
		if c == category {
			return true
		}
	}
	return false
}
