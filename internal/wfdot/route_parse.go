// TOML front end for the route constructor: done.toml and step.toml in, a Spec
// out (sty_81bb0dde).
//
// The route source is a STRUCTURED file — records with terse commentary — and it
// is now written in a structured format. It used to be markdown parsed by a
// hand-rolled key/value reader, which was the wrong shape twice over:
//
//   - The reader was bespoke. Its own header claimed it avoided adding a TOML
//     dependency; `github.com/BurntSushi/toml` had been in go.mod the whole time
//     for satelle.toml and agents.toml. It also skipped only a line STARTING
//     with `<!--`, so a multi-line HTML comment failed on every line after the
//     first — a real defect (sty_0ba83c4e, sty_8a7987de) whose remedy was a
//     prose convention rather than a parser.
//   - The markdown was a costume. A survey of every authored and embedded route
//     source found zero wikilinks and exactly three constructs in use: `---`
//     frontmatter, `##` headings, `<!-- -->` comments. TOML has `[meta]`,
//     `[[table]]` and `#`. The `.md` extension existed so docindex would ingest
//     the file, which is an indexer limitation, not a property of the document.
//
// The AUTHORED grammar is now TOML's. What this file adds is the mapping onto
// the exported Spec-side types, which did NOT change — that is what keeps
// route.go, wfroute, wfgovern, workflow show and the web panel untouched.
//
// Unknown keys are an ERROR, never a silent drop: a typo'd `reviewrs =` that
// parsed as "no reviewers" would lose a gate, and a route that quietly loses a
// gate is the one failure this representation must not have.
package wfdot

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// --- wire structs -----------------------------------------------------------
//
// Deliberately separate from the exported Step/List/RouteGate types. The wire
// shape serves TOML decoding; the Spec-side shape serves the constructor. Fusing
// them would let a decoding concern (a *int for set-vs-unset, say) leak into
// every consumer of a Step.

// roleRef is `{ state = "blocked", gate = "…" }` — a role state and the reviewer
// that admits it. They are a pair; splitting them across keys invites declaring
// one without the other.
type roleRef struct {
	State string `toml:"state"`
	Gate  string `toml:"gate"`
	// Advisor / AdvisorSkill name an agent the ORCHESTRATOR consults on this role
	// state. A declaration, never a dispatch.
	Advisor      string `toml:"advisor"`
	AdvisorSkill string `toml:"advisor_skill"`
}

// recoverRef is `{ step = "in_progress", from = ["integration", "release"] }`.
type recoverRef struct {
	Step string   `toml:"step"`
	From []string `toml:"from"`
}

// reworkRef is a performing step's `rework = { consult = "…", rounds = N }` —
// the consulting binding the orchestrator may converse with before presenting
// this step's exit edge, and the round budget that bounds the conversation.
// Configuration, not a decision: the binary runs the relay and its termination
// rule, and this key says WHO and HOW MANY (sty_8e0b29a0).
type reworkRef struct {
	Consult string `toml:"consult"`
	Rounds  int    `toml:"rounds"`
}

// remoteWaitWire is a step's `remote_wait = { remote = "…", on = { outcome =
// "status", … } }` — the team remote whose run for the story this step waits on,
// and the status each reported outcome moves the story to. Configuration: the
// binary reads the run and takes the mapped edge, the route names both.
type remoteWaitWire struct {
	Remote string            `toml:"remote"`
	On     map[string]string `toml:"on"`
}

// adviseRef is a step's `advise ={ agent = "…", skill = "…" }`.
// applyCriteriaRef is a step's `[apply_criteria] doc = "…" heading = "…"` — the
// story document and heading whose criteria entry to the step applies to the
// story (sty_4d9df9a0). Configuration: the binary copies, the route names.
type applyCriteriaRef struct {
	Doc     string `toml:"doc"`
	Heading string `toml:"heading"`
}

// applyChangesRef is a step's `apply_changes = { ops = […], tags = […] }` — the
// operations a propose step's performer may report as `changes` and the tag
// prefixes it may add (sty_af0b1761). Configuration: the binary applies what the
// route allows, the route names it.
type applyChangesRef struct {
	Ops  []string `toml:"ops"`
	Tags []string `toml:"tags"`
}

// answerProgressRef is a step's `[<step>.answer_progress] enabled = true
// min_progress = N` (sty_73224b86): refuse a re-presentation whose performer
// answers or changes too few of the prior blocking findings, before its gates
// run. Both fields are pointers so an omitted `enabled` (a silent no-op) is
// refused and an omitted `min_progress` defaults to 1. Distinct from the rework
// relay's no_progress, which ends a coder↔consultant conversation.
type answerProgressRef struct {
	Enabled     *bool `toml:"enabled"`
	MinProgress *int  `toml:"min_progress"`
}

type adviseRef struct {
	Agent string `toml:"agent"`
	Skill string `toml:"skill"`
}

// tagObligation is `[[category.tag_obligation]]` — an obligation appended when
// the story carries the tag.
type tagObligation struct {
	Tag        string `toml:"tag"`
	Obligation string `toml:"obligation"`
}

type categoryWire struct {
	Obligations []string `toml:"obligations"`
	Park        *roleRef `toml:"park"`
	Cancel      *roleRef `toml:"cancel"`
	// Recover is a table (one declared backward movement) or an array of tables
	// (several). A Primitive so ParseDone can accept either shape while the
	// strict unknown-key pass still covers the keys inside it.
	Recover        *toml.Primitive `toml:"recover"`
	TagObligations []tagObligation `toml:"tag_obligation"`
}

type stepWire struct {
	// Status is the STAGE NAME — the status an item holds while in this step.
	// It is a field, not the key, because stage names repeat by design: this
	// repo's catalogue declares `done` four times and `in_progress` three, one
	// per route family. The KEY is the obligation, which is the step's actual
	// identity and is unique by construction — BuildRoute already refuses two
	// steps providing the same obligation in one route (sty_81bb0dde).
	Status string `toml:"status"`
	Agent  string `toml:"agent"`
	// Model names a model for THIS step's dispatch (sty_7069bced /
	// epic:model-selection order:3) — one precedence tier below the
	// agents.toml binding's own model=, above the inherited/creator session
	// tiers. Distinct from the retired DOT edge model= (sty_a476a2f8, which
	// stays retired): that was a GATE override; this is a per-dispatch
	// instruction for the step's PERFORMER, resolved by config.SelectModel
	// alongside the binding, never baked into a Go branch.
	Model         string   `toml:"model"`
	Skills        []string `toml:"skills"`
	Reviewers     []string `toml:"reviewers"`
	ReviewerAgent string   `toml:"reviewer_agent"`
	// Panel is the ordered list of agents.toml sections that judge this step's
	// entry reviewers. Combine names the functional-check skill that folds their
	// verdicts. Both empty means today's single seat.
	Panel   []string `toml:"panel"`
	Combine string   `toml:"combine"`
	// Parallel is a POINTER on purpose: TOML's zero value is indistinguishable
	// from an absent key, and an authored `parallel = 0` (serial) must not read
	// as "unset" (which defaults to DefaultParallelCap concurrent). This is the
	// old ParallelSet bool, preserved deliberately.
	Parallel *int `toml:"parallel"`
	Bundle   bool `toml:"bundle"`
	// ReviewerCarry is a closed enum (sty_59720b8b): absent dispatches every
	// reviewer every round; "until_accept" carries an unchanged accept.
	ReviewerCarry string `toml:"reviewer_carry"`
	// FailFast names the entry reviewers (edge-named skills and panel skills)
	// that run in fail-fast review mode. Absent means every reviewer runs full.
	FailFast  []string   `toml:"fail_fast"`
	Provides  string     `toml:"provides"`
	Requires  []string   `toml:"requires"`
	AppliesTo []string   `toml:"applies_to"`
	Advise    *adviseRef `toml:"advise"`
	// Rework is a POINTER for the same reason Parallel is: an absent key must be
	// distinguishable from an authored zero, because absent means "no loop" and
	// `rounds = 0` is a mis-authored budget this parser refuses.
	Rework *reworkRef `toml:"rework"`
	// ApplyCriteria is a POINTER for the same reason: absent means the step applies
	// nothing, and a half-authored one is refused rather than ignored.
	ApplyCriteria *applyCriteriaRef `toml:"apply_criteria"`
	// ApplyChanges is a POINTER for the same reason as ApplyCriteria.
	ApplyChanges *applyChangesRef `toml:"apply_changes"`
	// Propose, Freeze and RejectBudget are the step's declared transition knobs
	// (sty_5262592e). RejectBudget is a POINTER for the same reason Rework is: an
	// absent key means "no budget" and an authored zero is a mis-authored one this
	// parser refuses.
	Propose      bool `toml:"propose"`
	Freeze       bool `toml:"freeze"`
	RejectBudget *int `toml:"reject_budget"`
	// AnswerProgress is a POINTER like ApplyCriteria: absent means the rule is
	// off, and a half-authored or misplaced table is refused rather than ignored.
	AnswerProgress *answerProgressRef `toml:"answer_progress"`
	// Mark is the tag an accepted readiness round puts on a story
	// (sty_a32bd6a9). Optional: absent means the step cannot be readied without
	// engagement, never a default tag name.
	Mark string `toml:"mark"`
	// UntrustedInput declares that the text this step's seats read is untrusted
	// (an external raiser's story): validate then refuses a seat on the step that
	// is not held read-only.
	UntrustedInput bool `toml:"untrusted_input"`
	// PRStep marks the step that raises the pull request: `satelle stats` splits
	// a finished item's cycle time at its first entry here.
	PRStep bool `toml:"pr_step"`
	// WaitsOnChildren marks a step at which a container idles while its child
	// stories are driven: it holds the status, but nothing performs on the
	// container itself (sty_7f3e6fd3).
	WaitsOnChildren bool `toml:"waits_on_children"`
	// ReadyChildren declares whether `satelle story ready` on a container also
	// readies each of its children before it marks the container. A pointer so
	// an absent key stays today's behaviour (true); legal only on a
	// waits_on_children step. Read through Step.ReadiesChildren.
	ReadyChildren *bool `toml:"ready_children"`
	// Schedule is the DEPRECATED container-step child schedule key: SchedParallel
	// or SchedSequential. A plain string, so an absent key stays "". It keeps its
	// meaning for existing repos; validate names it deprecated (LegacyScheduleSteps).
	// Distinct from Parallel above, which is the reviewer fan-out.
	Schedule string `toml:"schedule"`
	// Sequential is the user's override of the parallel default: a waits_on_children
	// step with sequential = true runs its container's children one at a time.
	// Read through ChildSchedule.
	Sequential bool `toml:"sequential"`
	// AfterChildren names the obligation every child must have discharged on its
	// own route before a container may enter this step. A plain string, so an
	// absent key stays "" and container engagement is unchanged.
	AfterChildren string `toml:"after_children"`
	// RemoteAgent and LocalTags declare where a parallel epic child's step runs:
	// the cloud binding that performs it, and the tags that pin a child to the
	// step's own agent (sty_dde8b6a4).
	RemoteAgent string   `toml:"remote_agent"`
	LocalTags   []string `toml:"local_tags"`
	// PlacementTag is the tag prefix a child's placement pin is written under;
	// DeliveryHeading is the readiness-document heading that records the reason
	// for each child's delivery choice (sty_c931cd90).
	// LaneTag is the tag prefix of a child's landing lane, which wave prints.
	PlacementTag    string `toml:"placement_tag"`
	DeliveryHeading string `toml:"delivery_heading"`
	LaneTag         string `toml:"lane_tag"`
	// RemoteWait declares that the story waits here on a team remote's run for
	// it, and maps each outcome the run concludes with to the status it moves
	// the story to. A POINTER so an absent key stays distinguishable from an
	// authored empty table, which is refused.
	RemoteWait *remoteWaitWire `toml:"remote_wait"`
	// Wait marks a step at which a story rests between performing steps: it is
	// neither parked nor terminal, holds no seat or tree, and nothing performs
	// on it until a move takes it onward.
	Wait     bool `toml:"wait"`
	Start    bool `toml:"start"`
	Terminal bool `toml:"terminal"`
	// ContextBudget and TurnBudget once set the step's spend bounds. Satelle
	// manages no budget now (sty_58a7bdfe), so either key is refused by name,
	// ahead of the generic unknown-key pass, until satelle init removes it.
	ContextBudget removedStepContextBudget `toml:"context_budget"`
	TurnBudget    removedStepTurnBudget    `toml:"turn_budget"`
}

// removedStepContextBudget / removedStepTurnBudget refuse the removed step
// budget keys when a step.toml still carries one.
type (
	removedStepContextBudget struct{}
	removedStepTurnBudget    struct{}
)

func (*removedStepContextBudget) UnmarshalTOML(any) error {
	return removedStepBudgetError("context_budget")
}

func (*removedStepTurnBudget) UnmarshalTOML(any) error { return removedStepBudgetError("turn_budget") }

func removedStepBudgetError(key string) error {
	return fmt.Errorf("%s is removed — satelle manages no context or turn budget (each agent CLI manages its own); run \"satelle init\" to remove it", key)
}

type gateWire struct {
	Skill     string   `toml:"skill"`
	Agent     string   `toml:"agent"`
	On        []string `toml:"on"`
	AppliesTo []string `toml:"applies_to"`
	For       []string `toml:"for"`
	Mandatory bool     `toml:"mandatory"`
	// When names a functional-check skill the engine runs before it enqueues the
	// gate — an enqueue precondition, never a verdict.
	When string `toml:"when"`
	// Panel / Combine are this gate's own seats and fold check. They apply only
	// to this skill, and they do not overwrite a step-level panel.
	Panel   []string `toml:"panel"`
	Combine string   `toml:"combine"`
	// ReviewMode is the gate's review mode ("full" or "fail-fast"). It covers the
	// gate and every seat of its panel. Absent means full.
	ReviewMode string `toml:"review_mode"`
}

// recordsOf splits a route-source body into its top-level tables: the reserved
// ones (meta, and gate in a step catalogue) and everything else, which IS the
// record set — keyed by the record's own identity, the way agents.toml keys a
// binding by its section name.
//
// Two-pass, because that keying is what the format is FOR: the key is data, so
// no fixed struct can name it. Pass one takes every top-level table as an
// undecoded Primitive; pass two decodes each into its wire struct. BurntSushi
// tracks decoded keys across PrimitiveDecode, so Undecoded() at the end still
// catches an unknown key INSIDE a record — the strictness that stops a typo'd
// `reviewrs =` from silently costing a gate.
func recordsOf(body, role string, reserved ...string) (recs, resv map[string]toml.Primitive, md toml.MetaData, err error) {
	var raw map[string]toml.Primitive
	md, err = toml.Decode(body, &raw)
	if err != nil {
		return nil, nil, md, fmt.Errorf("%s: %w", role, err)
	}
	skip := map[string]bool{metaKey: true}
	for _, r := range reserved {
		skip[r] = true
	}
	recs = make(map[string]toml.Primitive, len(raw))
	resv = make(map[string]toml.Primitive, len(skip))
	for k, v := range raw {
		if skip[k] {
			resv[k] = v
			continue
		}
		recs[k] = v
	}
	return recs, resv, md, nil
}

// metaKey is the document header table. It is not a record: its keys vary by
// kind and the frontmatter reader owns them, so the strict unknown-key pass must
// never report on it.
const metaKey = "meta"

// sortedKeys keeps record order deterministic. Route ORDER is derived from
// requires/provides, never from declaration order, so this is about stable
// output and reproducible errors — not about the route.
func sortedKeys(m map[string]toml.Primitive) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// undecodedErr reports any key no wire struct claimed, across the whole file —
// except under [meta].
//
// The [meta] exemption is by KEY PATH, not by decoding the table into a sink.
// The sink was the first attempt and it silently half-worked: decoding into a
// `map[string]any` leaves the elements of an array-of-tables unmarked, so the
// moment a route source declared its create hook the block way — `[[meta.hooks]]`
// — every field inside it was reported as an unknown key and the whole file
// failed to parse (sty_81bb0dde). A route source that declares a lifecycle hook
// is ordinary substrate, so that was a refusal to read a legal file.
func undecodedErr(md toml.MetaData, role string) error {
	un := md.Undecoded()
	keys := make([]string, 0, len(un))
	for _, k := range un {
		if len(k) > 0 && k[0] == metaKey {
			continue
		}
		keys = append(keys, k.String())
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return fmt.Errorf("%s: unknown key(s): %s", role, strings.Join(keys, ", "))
}

// ParseDone reads a declaration of done. One TABLE per story category, keyed by
// the category name — `["*"]` for the wildcard, quoted because `*` is not a bare
// key:
//
//	[meta]
//	name = "done"
//
//	[feature]
//	obligations = ["raised", "planned", "coded"]
//	park    = { state = "blocked", gate = "satelle-story-blocked-review" }
//	cancel  = { state = "cancelled", gate = "satelle-story-cancel-review" }
//	recover = { step = "in_progress", from = ["integration"] }
//
//	[[feature.tag_obligation]]
//	tag = "surface:ui"
//	obligation = "design-reviewed"
//
// A duplicate category is a TOML decode error rather than something Validate has
// to catch, which is the point of keying by identity.
func ParseDone(body string) ([]List, error) {
	recs, _, md, err := recordsOf(body, "done.toml")
	if err != nil {
		return nil, err
	}
	out := make([]List, 0, len(recs))
	for _, name := range sortedKeys(recs) {
		var c categoryWire
		if err := md.PrimitiveDecode(recs[name], &c); err != nil {
			return nil, fmt.Errorf("done.toml: category %q: %w", name, err)
		}
		l := List{Category: name, Obligations: c.Obligations}
		if c.Park != nil {
			l.Park, l.ParkGate = c.Park.State, c.Park.Gate
			l.ParkAdvisor, l.ParkAdvisorSkill = c.Park.Advisor, c.Park.AdvisorSkill
		}
		if c.Cancel != nil {
			l.Cancel, l.CancelGate = c.Cancel.State, c.Cancel.Gate
		}
		recovers, rerr := decodeRecovers(md, c.Recover)
		if rerr != nil {
			return nil, fmt.Errorf("done.toml: category %q: recover: %w", name, rerr)
		}
		for _, r := range recovers {
			l.Recovers = append(l.Recovers, Recover{Step: r.Step, From: r.From})
		}
		for _, t := range c.TagObligations {
			if t.Tag == "" || t.Obligation == "" {
				return nil, fmt.Errorf("done.toml: [[%s.tag_obligation]] needs both tag and obligation", name)
			}
			if l.TagObligations == nil {
				l.TagObligations = map[string][]string{}
			}
			l.TagObligations[t.Tag] = append(l.TagObligations[t.Tag], t.Obligation)
		}
		out = append(out, l)
	}
	if err := undecodedErr(md, "done.toml"); err != nil {
		return nil, err
	}
	return out, nil
}

// decodeRecovers reads a category's `recover` as either one table or an array of
// tables. The single-table form is the original grammar and stays valid; the
// array form lets a route declare a second backward edge (sty_5262592e).
func decodeRecovers(md toml.MetaData, p *toml.Primitive) ([]recoverRef, error) {
	if p == nil {
		return nil, nil
	}
	var many []recoverRef
	if err := md.PrimitiveDecode(*p, &many); err == nil {
		return many, nil
	}
	var one recoverRef
	if err := md.PrimitiveDecode(*p, &one); err != nil {
		return nil, err
	}
	return []recoverRef{one}, nil
}

// ParseSteps reads a step catalogue. One TABLE per step, keyed by the OBLIGATION
// it discharges, plus `[[gate]]` entries for the always-on gates:
//
//	[coded]
//	status = "in_progress"
//	agent = "executor"
//	skills = ["code"]
//	requires = ["planned"]
//
//	[[gate]]
//	skill = "satelle-build-unit-check"
//	on = ["integration"]
//
// trimPanel drops surrounding space and refuses an empty seat name. An empty
// name would be a binding that cannot resolve, recorded as a silent gap rather
// than the typo it is.
func trimPanel(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("panel contains an empty seat")
		}
		out = append(out, s)
	}
	return out, nil
}

// ChangeOps is the closed set of operations a step's apply_changes may allow a
// performer to report (sty_af0b1761).
var ChangeOps = []string{"add-tags", "create-child"}

// trimmedAll trims every entry of in and drops the empty ones.
func trimmedAll(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// unknownChangeOp returns the first op in ops outside ChangeOps, or "".
func unknownChangeOp(ops []string) string {
	for _, op := range ops {
		known := false
		for _, k := range ChangeOps {
			known = known || op == k
		}
		if !known {
			return op
		}
	}
	return ""
}

// parseReviewMode validates a gate's declared review_mode against the closed
// set and returns the canonical stored form: "" for full, ReviewModeFailFast.
func parseReviewMode(declared, where string) (string, error) {
	switch mode := strings.TrimSpace(declared); mode {
	case "", ReviewModeFull:
		return "", nil
	case ReviewModeFailFast:
		return mode, nil
	default:
		return "", fmt.Errorf("%s: review_mode %q is invalid — it must be one of: %s, %s (omit the key for %s)", where, mode, ReviewModeFull, ReviewModeFailFast, ReviewModeFull)
	}
}

// refusePanelShape is the parse refusal for a panel declaration, by key name.
// A one-seat panel is the same shape as a single binding and may omit combine.
// Two or more seats need a combine skill. combine without a panel, and panel
// together with the single-binding key, are silent no-ops if accepted — so
// they are refused here rather than dropped later.
func refusePanelShape(panel []string, combine, other, otherKey, where string) error {
	combine = strings.TrimSpace(combine)
	switch {
	case strings.TrimSpace(other) != "" && len(panel) > 0:
		return fmt.Errorf("%s: panel together with %s — name the seats on panel, or one binding on %s, not both", where, otherKey, otherKey)
	case combine != "" && len(panel) == 0:
		return fmt.Errorf("%s: combine %q without panel — combine names the check that folds a panel", where, combine)
	case len(panel) >= 2 && combine == "":
		return fmt.Errorf("%s: panel of %d seats without combine — name the check skill that folds them", where, len(panel))
	}
	return nil
}

// Keyed by obligation, NOT by stage name: `done` is four different steps in this
// repo's catalogue and `in_progress` is three, because different route families
// reach the same status by discharging different obligations. The obligation is
// the identity, and it is unique by construction.
//
// Gates stay an ARRAY, and that is not an inconsistency: the same skill
// legitimately repeats under different scope — `satelle-step-summary` runs on
// one binding for the spine and another for the container lanes. A gate has no
// unique identity to key on.
func ParseSteps(body string) (Catalogue, error) {
	recs, resv, md, err := recordsOf(body, "step.toml", "gate")
	if err != nil {
		return Catalogue{}, err
	}
	var cat Catalogue
	reworkDeclared := map[string]bool{}
	budgetDeclared := map[string]bool{}
	criteriaDeclared := map[string]bool{}
	changesDeclared := map[string]bool{}
	for _, provides := range sortedKeys(recs) {
		var s stepWire
		if err := md.PrimitiveDecode(recs[provides], &s); err != nil {
			return Catalogue{}, fmt.Errorf("step.toml: step %q: %w", provides, err)
		}
		if strings.TrimSpace(s.Status) == "" {
			return Catalogue{}, fmt.Errorf("step.toml: step %q has no status (the stage name an item holds here)", provides)
		}
		panel, perr := trimPanel(s.Panel)
		if perr != nil {
			return Catalogue{}, fmt.Errorf("step.toml: step %q: %w", provides, perr)
		}
		if err := refusePanelShape(panel, s.Combine, s.ReviewerAgent, "reviewer_agent", fmt.Sprintf("step.toml: step %q", provides)); err != nil {
			return Catalogue{}, err
		}
		st := Step{
			Name:          s.Status,
			Provides:      provides,
			Agent:         s.Agent,
			Model:         s.Model,
			Skills:        s.Skills,
			Reviewers:     s.Reviewers,
			ReviewerAgent: s.ReviewerAgent,
			Panel:         panel,
			Combine:       strings.TrimSpace(s.Combine),
			Requires:      s.Requires,
			AppliesTo:     s.AppliesTo,
			Start:         s.Start,
			Terminal:      s.Terminal,
			Propose:       s.Propose,
			Mark:          strings.TrimSpace(s.Mark),
			Freeze:        s.Freeze,

			UntrustedInput:  s.UntrustedInput,
			PRStep:          s.PRStep,
			WaitsOnChildren: s.WaitsOnChildren,
			ReadyChildren:   s.ReadyChildren,
			Schedule:        s.Schedule,
			Sequential:      s.Sequential,
			AfterChildren:   strings.TrimSpace(s.AfterChildren),
			RemoteAgent:     strings.TrimSpace(s.RemoteAgent),
			LocalTags:       s.LocalTags,
			Wait:            s.Wait,
			PlacementTag:    strings.TrimSpace(s.PlacementTag),
			DeliveryHeading: strings.TrimSpace(s.DeliveryHeading),
			LaneTag:         strings.TrimSpace(s.LaneTag),
		}
		if s.RemoteWait != nil {
			rw, err := parseRemoteWait(provides, s.RemoteWait, s.Agent)
			if err != nil {
				return Catalogue{}, err
			}
			st.RemoteWait = rw
		}
		if s.RejectBudget != nil {
			st.RejectBudget = *s.RejectBudget
			budgetDeclared[provides] = true
		}
		if ap := s.AnswerProgress; ap != nil {
			if ap.Enabled == nil {
				return Catalogue{}, fmt.Errorf("step.toml: step %q: answer_progress needs enabled = true|false — a table without it does nothing", provides)
			}
			if !st.Propose {
				return Catalogue{}, fmt.Errorf("step.toml: step %q: answer_progress requires propose = true (only a propose step runs its performer before its gates, so there is nothing to refuse before them)", provides)
			}
			st.MinProgress = 1
			if ap.MinProgress != nil {
				if *ap.MinProgress < 1 {
					return Catalogue{}, fmt.Errorf("step.toml: step %q: answer_progress min_progress must be >= 1 (got %d)", provides, *ap.MinProgress)
				}
				st.MinProgress = *ap.MinProgress
			}
			st.AnswerProgress = *ap.Enabled
		}
		if s.Parallel != nil {
			st.Parallel, st.ParallelSet = *s.Parallel, true
		}
		st.Bundle = s.Bundle
		st.ReviewerCarry = strings.TrimSpace(s.ReviewerCarry)
		st.FailFast = trimmedAll(s.FailFast)
		if s.Advise != nil {
			st.Advisor, st.AdvisorSkill = s.Advise.Agent, s.Advise.Skill
		}
		if s.Rework != nil {
			// Recorded here, VALIDATED after the unknown-key pass below. Order
			// matters: a typo'd `round = 2` decodes as no rounds at all, and
			// "rounds must be > 0" would be a confusing answer to a misspelling
			// the strict pass can name exactly (sty_8e0b29a0).
			st.ReworkConsult, st.ReworkRounds = strings.TrimSpace(s.Rework.Consult), s.Rework.Rounds
			reworkDeclared[provides] = true
		}
		if s.ApplyCriteria != nil {
			st.ApplyCriteriaDoc = strings.TrimSpace(s.ApplyCriteria.Doc)
			st.ApplyCriteriaHeading = strings.TrimSpace(s.ApplyCriteria.Heading)
			criteriaDeclared[provides] = true
		}
		if s.ApplyChanges != nil {
			st.ApplyChangeOps = trimmedAll(s.ApplyChanges.Ops)
			st.ApplyChangeTags = trimmedAll(s.ApplyChanges.Tags)
			changesDeclared[provides] = true
		}
		cat.Steps = append(cat.Steps, st)
	}
	var gates []gateWire
	if g, ok := resv["gate"]; ok {
		if err := md.PrimitiveDecode(g, &gates); err != nil {
			return Catalogue{}, fmt.Errorf("step.toml: [[gate]]: %w", err)
		}
	}
	for i, g := range gates {
		if strings.TrimSpace(g.Skill) == "" {
			return Catalogue{}, fmt.Errorf("step.toml: [[gate]] #%d has no skill", i+1)
		}
		panel, perr := trimPanel(g.Panel)
		if perr != nil {
			return Catalogue{}, fmt.Errorf("step.toml: [[gate]] %q: %w", g.Skill, perr)
		}
		if err := refusePanelShape(panel, g.Combine, g.Agent, "agent", fmt.Sprintf("step.toml: [[gate]] %q", g.Skill)); err != nil {
			return Catalogue{}, err
		}
		mode, merr := parseReviewMode(g.ReviewMode, fmt.Sprintf("step.toml: [[gate]] %q", g.Skill))
		if merr != nil {
			return Catalogue{}, merr
		}
		cat.Gates = append(cat.Gates, RouteGate{
			Skill: g.Skill, Agent: g.Agent, On: g.On,
			AppliesTo: g.AppliesTo, For: g.For, Mandatory: g.Mandatory,
			When:       strings.TrimSpace(g.When),
			Panel:      panel,
			Combine:    strings.TrimSpace(g.Combine),
			ReviewMode: mode,
		})
	}
	if err := undecodedErr(md, "step.toml"); err != nil {
		return Catalogue{}, err
	}
	// Rework validation LAST, so a misspelled key inside the sub-table is
	// reported as the unknown key it is rather than as its consequence. Each of
	// the three refusals exists because the alternative is a silent no-op, and a
	// rework key that does nothing is indistinguishable from a route that never
	// declared one (sty_8e0b29a0).
	for _, st := range cat.Steps {
		if !reworkDeclared[st.Provides] {
			continue
		}
		switch {
		case st.ReworkConsult == "":
			return Catalogue{}, fmt.Errorf("step.toml: step %q: rework needs consult = \"<binding>\"", st.Provides)
		case st.ReworkRounds <= 0:
			return Catalogue{}, fmt.Errorf("step.toml: step %q: rework rounds must be > 0 (a zero budget is a loop that never runs)", st.Provides)
		case strings.TrimSpace(st.Agent) == "":
			return Catalogue{}, fmt.Errorf("step.toml: step %q: rework needs the step to allocate a performer (agent = …) — there is nobody to code with", st.Provides)
		}
	}
	// propose and reject_budget refusals, for the same reason as rework's: each is
	// a silent no-op when mis-authored (sty_5262592e).
	for _, st := range cat.Steps {
		switch {
		case st.Propose && (strings.TrimSpace(st.Agent) == "" || len(st.Skills) == 0):
			return Catalogue{}, fmt.Errorf("step.toml: step %q: propose needs the step to allocate a performer (agent = …) and its skills — there is nothing to run before the gates", st.Provides)
		case criteriaDeclared[st.Provides] && (st.ApplyCriteriaDoc == "" || st.ApplyCriteriaHeading == ""):
			return Catalogue{}, fmt.Errorf("step.toml: step %q: apply_criteria needs both doc = \"<document>\" and heading = \"<heading>\" — half a declaration applies nothing", st.Provides)
		case changesDeclared[st.Provides] && (len(st.ApplyChangeOps) == 0 || len(st.ApplyChangeTags) == 0):
			return Catalogue{}, fmt.Errorf("step.toml: step %q: apply_changes needs ops = [...] and tags = [...] — half a declaration applies nothing", st.Provides)
		case changesDeclared[st.Provides] && !st.Propose:
			return Catalogue{}, fmt.Errorf("step.toml: step %q: apply_changes requires propose = true (the performer must run before the step's gates, so they judge the repaired record)", st.Provides)
		case changesDeclared[st.Provides] && unknownChangeOp(st.ApplyChangeOps) != "":
			return Catalogue{}, fmt.Errorf("step.toml: step %q: apply_changes op %q is not one of: %s", st.Provides, unknownChangeOp(st.ApplyChangeOps), strings.Join(ChangeOps, ", "))
		case budgetDeclared[st.Provides] && st.RejectBudget < 1:
			return Catalogue{}, fmt.Errorf("step.toml: step %q: reject_budget must be >= 1 (a zero budget refuses the first presentation)", st.Provides)
		}
	}
	for _, st := range cat.Steps {
		if st.ReviewerCarry != "" && st.ReviewerCarry != CarryUntilAccept {
			return Catalogue{}, fmt.Errorf("step.toml: step %q: reviewer_carry %q is invalid — the only value is %q (omit the key to dispatch every reviewer every round)", st.Provides, st.ReviewerCarry, CarryUntilAccept)
		}
	}
	// fail_fast names edge reviewers; an entry that is neither a reviewer nor a
	// panel skill of its step would be a silent no-op (sty_93359e0f).
	for _, st := range cat.Steps {
		for _, sk := range st.FailFast {
			if !containsStr(st.Reviewers, sk) {
				return Catalogue{}, fmt.Errorf("step.toml: step %q: fail_fast names %q, which is not one of the step's reviewers %v — fail_fast selects reviewers that run in %s review mode", st.Provides, sk, st.Reviewers, ReviewModeFailFast)
			}
		}
	}
	// schedule refusals: a value outside the closed set, or on a step that does
	// not wait on children, would be a silent no-op; sequential = true beside the
	// deprecated schedule = "parallel" is a contradiction.
	for _, st := range cat.Steps {
		switch {
		case st.Schedule != "" && st.Schedule != SchedParallel && st.Schedule != SchedSequential:
			return Catalogue{}, fmt.Errorf("step.toml: step %q: schedule %q is invalid — schedule must be one of: %s, %s", st.Provides, st.Schedule, SchedParallel, SchedSequential)
		case st.Schedule != "" && !st.WaitsOnChildren:
			return Catalogue{}, fmt.Errorf("step.toml: step %q: schedule is a container declaration — it needs waits_on_children = true", st.Provides)
		case st.ReadyChildren != nil && !st.WaitsOnChildren:
			return Catalogue{}, fmt.Errorf("step.toml: step %q: ready_children is a container declaration — it needs waits_on_children = true", st.Provides)
		case st.Sequential && !st.WaitsOnChildren:
			return Catalogue{}, fmt.Errorf("step.toml: step %q: sequential is a container declaration — it needs waits_on_children = true", st.Provides)
		case st.Sequential && st.Schedule == SchedParallel:
			return Catalogue{}, fmt.Errorf("step.toml: step %q: sequential = true contradicts schedule = %q — drop the deprecated schedule key (parallel is the default)", st.Provides, SchedParallel)
		}
	}
	for _, st := range cat.Steps {
		if err := checkWaitStep(st); err != nil {
			return Catalogue{}, err
		}
	}
	if err := checkAfterChildren(cat); err != nil {
		return Catalogue{}, err
	}
	return cat, nil
}

// checkWaitStep refuses a wait step that would contradict itself: one that is
// also the start or a terminal, a container's wait or a remote's wait, or one
// that names a performer — a story resting there frees its agent, so there is
// nobody to run. Whether the step has a way onward is decided per route, at
// assembly, where its edges exist.
func checkWaitStep(st Step) error {
	if !st.Wait {
		return nil
	}
	clash := ""
	switch {
	case st.Start:
		clash = "start"
	case st.Terminal:
		clash = "terminal"
	case st.WaitsOnChildren:
		clash = "waits_on_children"
	case st.RemoteWait != nil:
		clash = "remote_wait"
	}
	if clash != "" {
		return fmt.Errorf("step.toml: step %q: wait and %s are contradictory — a wait step is a rest between performing steps, neither the route's entry, its end, nor another kind of wait", st.Provides, clash)
	}
	if agent := strings.TrimSpace(st.Agent); agent != "" {
		return fmt.Errorf("step.toml: step %q: wait and agent = %q are contradictory — a story resting at a wait step frees its agent, so the step has no performer", st.Provides, agent)
	}
	return nil
}

// parseRemoteWait shapes a step's remote_wait declaration, refusing the forms
// that would be silent no-ops: no remote, no outcomes, a blank outcome or
// target, or a step that also names a performer (a waiting step has none). Whether
// each target is an edge the route declares is decided at assembly, where the
// recover, park and cancel edges exist.
func parseRemoteWait(step string, w *remoteWaitWire, agent string) (*RemoteWait, error) {
	remote := strings.TrimSpace(w.Remote)
	switch {
	case remote == "":
		return nil, fmt.Errorf("step.toml: step %q: remote_wait needs remote = \"<team remote name>\" — a wait with no remote reads no run", step)
	case len(w.On) == 0:
		return nil, fmt.Errorf("step.toml: step %q: remote_wait needs on = { <outcome> = \"<status>\", … } — a wait that maps no outcome never moves the story", step)
	case strings.TrimSpace(agent) != "":
		return nil, fmt.Errorf("step.toml: step %q: remote_wait and agent = %q are contradictory — a step that waits on a remote run has no performer", step, agent)
	}
	on := make(map[string]string, len(w.On))
	for outcome, target := range w.On {
		outcome, target = strings.TrimSpace(outcome), strings.TrimSpace(target)
		if outcome == "" || target == "" {
			return nil, fmt.Errorf("step.toml: step %q: remote_wait on needs a non-empty outcome and a non-empty status (got %q → %q)", step, outcome, target)
		}
		on[outcome] = target
	}
	return &RemoteWait{Remote: remote, On: on}, nil
}

// checkAfterChildren refuses the after_children declarations that would be
// silent no-ops whichever route lists the step: a name no step provides, a step
// that is itself the wait, and a step that names its own obligation. That a
// waits_on_children step precedes it is judged per route, by
// checkAfterChildrenRoute: a repo overlay that restates the readiness step
// without the wait still inherits the shipped after_children step by name
// (mergeRouteHalf), and is refused only for a category whose route lists it —
// an inherited container route does, so such a repo restores the wait or
// restates that category in its own done.toml.
func checkAfterChildren(cat Catalogue) error {
	provided := providedSet(cat)
	for _, st := range cat.Steps {
		if st.AfterChildren == "" {
			continue
		}
		switch {
		case st.WaitsOnChildren:
			return fmt.Errorf("step.toml: step %q: after_children and waits_on_children are two different container steps — the wait is the step before the one that names after_children", st.Provides)
		case !provided[st.AfterChildren]:
			return fmt.Errorf("step.toml: step %q: after_children %q names an obligation no step provides", st.Provides, st.AfterChildren)
		case st.AfterChildren == st.Provides:
			return fmt.Errorf("step.toml: step %q: after_children names the step's own obligation", st.Provides)
		}
	}
	return nil
}

// checkAfterChildrenRoute refuses a route that lists an after_children step no
// waits_on_children step precedes — not a container route, so the declaration
// would be a silent no-op. The precedence test is the step's own requires chain,
// so it is decided on the catalogue and never on a category or status name.
func checkAfterChildrenRoute(category string, ordered []Step, cat Catalogue) error {
	byProvides := map[string]Step{}
	for _, st := range cat.Steps {
		if st.Provides != "" {
			byProvides[st.Provides] = st
		}
	}
	for _, st := range ordered {
		if st.AfterChildren != "" && !requiresWait(st, byProvides, map[string]bool{}) {
			return fmt.Errorf("step.toml: step %q: after_children is a container declaration — the step must require, directly or through other steps, a waits_on_children step (category %q lists it without one)", st.Provides, category)
		}
	}
	return nil
}

// requiresWait reports whether st's requires chain reaches a waits_on_children
// step.
func requiresWait(st Step, byProvides map[string]Step, seen map[string]bool) bool {
	for _, req := range st.Requires {
		if seen[req] {
			continue
		}
		seen[req] = true
		p, ok := byProvides[req]
		if ok && (p.WaitsOnChildren || requiresWait(p, byProvides, seen)) {
			return true
		}
	}
	return false
}

// ParseRoute is the single entry point: the two authored bodies plus a category
// and the story's tags, in; a Spec, out. An unknown category is an error rather
// than an empty route — silently producing a workflow with no gates is the worst
// available failure.
func ParseRoute(doneBody, stepBody, category string, tags []string) (Spec, error) {
	lists, err := ParseDone(doneBody)
	if err != nil {
		return Spec{}, err
	}
	cat, err := ParseSteps(stepBody)
	if err != nil {
		return Spec{}, err
	}
	l, err := ListFor(lists, category)
	if err != nil {
		return Spec{}, err
	}
	return BuildRoute(l, cat, tags)
}

// WildcardCategory is the declaration of done that governs any category with no
// section of its own — the route-source spelling of a workflow's
// `applies_to: ["*"]`.
const WildcardCategory = "*"

// ListFor picks the declaration of done governing a category: an exact entry
// first, then the wildcard. That is the same precedence a workflow's applies_to
// already gets (a category-specific workflow beats a wildcard one), so the two
// front doors agree rather than each having a rule of its own.
//
// A category with neither is an ERROR, not an empty route. Enumerating every
// category a repo will ever use is brittle — the next new one would silently
// resolve to a workflow with no gates, which is the worst failure available. The
// wildcard is how a repo says "this route governs everything else"; its absence
// is how it says "refuse".
func ListFor(lists []List, category string) (List, error) {
	var wild *List
	for i := range lists {
		switch lists[i].Category {
		case category:
			return lists[i], nil
		case WildcardCategory:
			wild = &lists[i]
		}
	}
	if wild != nil {
		return *wild, nil
	}
	var known []string
	for _, l := range lists {
		known = append(known, l.Category)
	}
	return List{}, fmt.Errorf("no declaration of done for category %q and no %q section (have: %s)",
		category, WildcardCategory, strings.Join(known, ", "))
}
