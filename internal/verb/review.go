package verb

import (
	"context"
	"strings"

	"github.com/bobmcallan/satelle/internal/agentartifact"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// GateDecision is an isolated reviewer's verdict on a requested status
// transition. Gated reports whether a reviewer skill governed the edge at all —
// an ungated edge (no reviewer_skill, or its rubric not installed) is advisory
// and enacts directly, preserving the gateless baseline.
//
// An edge may be judged by MORE THAN ONE reviewer: a transition can name an
// ordered list of reviewers, and an always-on system reviewer layer runs after
// them. Reviewers carries each reviewer's verdict in run order; the top-level
// Accept/Skill/Notes mirror the deciding reviewer (the first reject, or the last
// reviewer when all accept) so single-reviewer callers keep their contract.
type GateDecision struct {
	Gated     bool   // a reviewer skill judged this edge
	Accept    bool   // accept enacts the transition; reject blocks it
	Notes     string // reviewer notes — pushback to the executor on reject
	Reasoning string // optional free-form reasoning (verdict contract; may be empty)
	// Reviewed is the exact words this verdict rests on, as the reviewer quoted
	// them. ReviewedTruncated is set when that quotation was over the ledger
	// ceiling and was omitted rather than shortened. The binary stores both; it
	// does not compare them to the story.
	Reviewed          string
	ReviewedTruncated bool
	// Criteria is the verdict's per-criterion entries (sty_99d3162b). Empty for a
	// verdict that carries none, which is decided by its top-level decision.
	Criteria []CriterionVerdict
	// CriteriaSent records that the reviewer sent a criteria[] at all, even when
	// no entry survived normalising (an unparseable or unjudged number). It is
	// in-process only: it keeps such a verdict deciding from its criteria rather
	// than from its top-level decision.
	CriteriaSent bool
	// ReviewMode is the gate's declared review mode, set only for fail-fast (the
	// engine reads it off the route; empty is full). Coverage is what the
	// reviewer says it covered; nil means it stated nothing. Both are stored on
	// the verdict row so the next presentation can tell a partial reject from a
	// full one (sty_93359e0f).
	ReviewMode string
	Coverage   *Coverage
	// Waived names the checks this verdict honoured a developer waiver for
	// (sty_bef97244). The engine keeps only those the gate's payload carried a
	// waiver for; the binary records them and judges nothing by them.
	Waived    []string
	Skill     string            // the deciding reviewer skill
	Reviewers []ReviewerVerdict // per-reviewer verdicts in run order (empty for the legacy single-reviewer path)
	// Command/Context describe an isolated AGENT invocation (LLM reviewer): the
	// resolved harness command and the injected-context source (skill/rubric file).
	// Empty for a deterministic functional-check gate, which invokes no agent
	// (sty_fb3e0873). They mirror the deciding reviewer for the single-reviewer path.
	Command string
	Context string
	Model   string // the reviewer's configured model alias (sty_a699ad14) — for the cost view
	// ModelResolved is the canonical model id the transport actually ran
	// (sty_87b86044), distinct from Model (the configured alias). Models is
	// every model a multi-model invocation reported. Both are ModelUnavailable/
	// nil respectively when the transport reported no model.
	ModelResolved string
	Models        []ModelUsage
	// ModelSource names why ModelResolved was chosen — binding, step, agent,
	// inherited-in-loop, creator, order, or cli-default
	// (config.SelectModel, sty_7069bced). Empty for a functional-check gate,
	// which invokes no agent and so selects no model.
	ModelSource string
	// TokensIn/Out/Total and DurationMs are the invocation's cost (sty_a699ad14),
	// recorded on the agent_invocation ledger entry so per-gate cost is auditable.
	// Zero for a functional-check gate or a plain-text harness that emits no usage.
	// UsageAvailable distinguishes a transport-reported zero from unreported
	// usage (sty_56aae77a) — false means the numbers are not measured.
	TokensIn       int
	TokensOut      int
	TokensTotal    int
	DurationMs     int64
	UsageAvailable bool
	// TokensInFresh/TokensCacheWrite/TokensCacheRead split TokensIn into its
	// disjoint components (sty_363eaf55) — the same accounting UsageResult
	// carries. Zero on a transport that reports no cache split.
	TokensInFresh    int
	TokensCacheWrite int
	TokensCacheRead  int
	UsageNote
	// RunCounts is the invocation's turns and tool calls, or the adapter-named
	// reason each is unavailable. Bundles carry it on the first verdict only,
	// like usage.
	RunCounts
	// CostUSD/CostUnavailableReason are the invocation's dollar cost (sty_c4df7376),
	// read from the provider's own figure — never a token count times a rate.
	// CostUnavailableReason names the adapter and why when CostUSD is nil.
	CostUSD               *float64
	CostUnavailableReason string
	// SystemPromptBytes/PayloadBytes are the byte lengths of the system prompt
	// and stdin payload satelle sent for this invocation (sty_363eaf55) —
	// lengths only, never content.
	SystemPromptBytes int
	PayloadBytes      int
	// ToolIsolation records what the reviewer's harness offered (sty_ef3efb51).
	ToolIsolation
	// BundleID/BundleSkills mark a verdict that came from ONE reviewer session
	// judging several rubrics (sty_23e10d92): the bundle's id and every skill it
	// judged, in run order. Both are empty for a gate that ran alone. Usage rides
	// the FIRST verdict of a bundle only — it is one measured invocation.
	BundleID     string
	BundleSkills []string
	// Seats is the panel's per-seat record, in declaration order, when this
	// decision is the fold of several seats. Combine names the check skill whose
	// exit code set Accept. Both are empty on a single-seat gate. Command stays
	// empty on a fold so the transition writer does not record the fold as one
	// harness invocation; SeatStamps carries each seat that actually started.
	Seats      []PanelSeat
	Combine    string
	SeatStamps []ReviewerVerdict
	// Unresolved names gate skills this edge DECLARED that do not resolve in the
	// substrate. Those gates degrade to advisory — the edge advances with no
	// reviewer and no verdict — which is deliberate, so a fresh repo works before
	// every gate is authored. It is EVIDENCE OF AN UNGATED ADVANCE, never a
	// verdict: an advance recorded with a non-empty Unresolved was not judged,
	// and without it that is indistinguishable from an edge carrying no gate at
	// all (sty_d59ec6a9).
	Unresolved []string
}

// ReviewerVerdict is one reviewer's verdict within a transition's ordered
// review. Order is its position in the run (workflow-named reviewers first,
// then the always-on system layer); System marks a verdict from that layer.
type ReviewerVerdict struct {
	Skill             string `json:"skill"`
	Order             int    `json:"order"`
	Accept            bool   `json:"accept"`
	Notes             string `json:"notes,omitempty"`
	Reasoning         string `json:"reasoning,omitempty"` // optional free-form reasoning (verdict contract)
	Reviewed          string `json:"reviewed,omitempty"`
	ReviewedTruncated bool   `json:"reviewed_truncated,omitempty"`
	// Criteria is the verdict's per-criterion entries (sty_99d3162b).
	Criteria []CriterionVerdict `json:"criteria,omitempty"`
	// ReviewMode / Coverage mirror GateDecision's (sty_93359e0f): the mode the
	// gate declared (fail-fast only) and what the reviewer says it covered.
	ReviewMode string    `json:"review_mode,omitempty"`
	Coverage   *Coverage `json:"coverage,omitempty"`
	// Waived mirrors GateDecision's: the checks a developer waiver was honoured for.
	Waived []string `json:"waived,omitempty"`
	System bool     `json:"system,omitempty"`
	// CarriedFrom is the 1-based attempt index of the earlier verdict this one
	// re-issues without a dispatch (sty_59720b8b); zero for a fresh judgment. A
	// carried verdict has no Command, so no agent_invocation row is written.
	CarriedFrom int `json:"carried_from,omitempty"`
	// Command/Context name the agent invocation behind an LLM reviewer's verdict —
	// the resolved harness command and the injected skill/rubric file — so the trail
	// records HOW it was judged, not just the outcome. Empty for a functional check.
	Command string `json:"command,omitempty"`
	Context string `json:"context,omitempty"`
	Model   string `json:"model,omitempty"` // reviewer's configured model alias (sty_a699ad14)
	// ModelResolved/Models mirror GateDecision's fields of the same name
	// (sty_87b86044) — stamped directly on this verdict's ledger row so a
	// review_accept/review_reject entry stands alone after compaction, with no
	// join to an agent_invocation row.
	ModelResolved string       `json:"model_resolved,omitempty"`
	Models        []ModelUsage `json:"model_usage,omitempty"`
	// ModelSource mirrors GateDecision.ModelSource (sty_7069bced), stamped
	// directly on this verdict's ledger row for the same reason ModelResolved is.
	ModelSource string `json:"model_source,omitempty"`
	// Token/wall-time cost of this reviewer's invocation (sty_a699ad14), recorded
	// on its agent_invocation entry for the per-gate cost view.
	// UsageAvailable is stamped without omitempty so unreported ≠ measured zero
	// (sty_56aae77a).
	TokensIn       int   `json:"tokens_in,omitempty"`
	TokensOut      int   `json:"tokens_out,omitempty"`
	TokensTotal    int   `json:"tokens_total,omitempty"`
	DurationMs     int64 `json:"duration_ms,omitempty"`
	UsageAvailable bool  `json:"usage_available"`
	// TokensInFresh/TokensCacheWrite/TokensCacheRead split TokensIn (sty_363eaf55).
	TokensInFresh    int `json:"tokens_in_fresh,omitempty"`
	TokensCacheWrite int `json:"tokens_cache_write,omitempty"`
	TokensCacheRead  int `json:"tokens_cache_read,omitempty"`
	UsageNote
	// RunCounts mirrors GateDecision's: the invocation's turns and tool calls.
	RunCounts
	// Outcome is OutcomeUnavailable or OutcomeMaxTurns on the invocation stamp of
	// an attempt that produced no verdict; empty on a verdict, whose accept or
	// reject is the review row's own record.
	Outcome string `json:"outcome,omitempty"`
	// CostUSD/CostUnavailableReason mirror GateDecision's fields of the same
	// name (sty_c4df7376), stamped on this verdict's own ledger row.
	CostUSD               *float64 `json:"cost_usd,omitempty"`
	CostUnavailableReason string   `json:"cost_unavailable_reason,omitempty"`
	// SystemPromptBytes/PayloadBytes are the byte lengths satelle sent — lengths
	// only, never content (sty_363eaf55).
	SystemPromptBytes int `json:"system_prompt_bytes,omitempty"`
	PayloadBytes      int `json:"payload_bytes,omitempty"`
	// ToolIsolation mirrors GateDecision's, stamped on this verdict's ledger row.
	ToolIsolation
	// BundleID/BundleSkills mirror GateDecision's: the verdict came from one
	// bundled session (sty_23e10d92). Only the first verdict of a bundle carries
	// the invocation's usage; the rest carry none.
	BundleID     string   `json:"bundle_id,omitempty"`
	BundleSkills []string `json:"bundle_skills,omitempty"`
	// Seats / Combine are the panel fold recorded on the skill-level verdict.
	// A dissenting seat is a nested record, never its own review row.
	Seats   []PanelSeat `json:"seats,omitempty"`
	Combine string      `json:"combine,omitempty"`
	// Seat is the agents.toml section when this verdict is one seat's invocation
	// stamp rather than the skill-level fold. Empty on the skill row.
	Seat string `json:"seat,omitempty"`
	// SeatStamps are the invocation stamps of seats that started. The transition
	// writer walks them; they are not part of the review-row JSON.
	SeatStamps []ReviewerVerdict `json:"-"`
}

// PanelSeat is one seat's record inside a skill-level panel verdict. Accept
// false with Unavailable true is not an accept — the combine check decides
// whether that counts against the panel.
//
// Reviewed, ReviewedTruncated and Attempt are the seat's own quotation and the
// attempt it was judged on, recorded as a single-seat verdict records them
// (sty_da9e8d0a), so the next presentation of the edge can hand each seat its
// own prior verdict. CarriedFrom is the attempt whose accept a carried seat
// reuses; it is zero for a seat that was judged this round.
type PanelSeat struct {
	Seat        string `json:"seat"`
	Accept      bool   `json:"accept"`
	Unavailable bool   `json:"unavailable,omitempty"`
	// MaxTurns marks an unavailable seat that the harness stopped at its turn
	// cap: the run was cut short, it did not judge. Set together with
	// Unavailable, never instead of it, so a combine check that reads only
	// unavailable keeps working.
	MaxTurns          bool   `json:"max_turns,omitempty"`
	Notes             string `json:"notes,omitempty"`
	Reviewed          string `json:"reviewed,omitempty"`
	ReviewedTruncated bool   `json:"reviewed_truncated,omitempty"`
	Attempt           int    `json:"attempt,omitempty"`
	CarriedFrom       int    `json:"carried_from,omitempty"`
	// Criteria is the seat's own per-criterion entries (sty_99d3162b). Accept is
	// then the seat's effective decision, derived from them (CriteriaAccept).
	Criteria []CriterionVerdict `json:"criteria,omitempty"`
	// Coverage is what this seat says it covered (sty_93359e0f); nil when it
	// stated nothing.
	Coverage *Coverage `json:"coverage,omitempty"`
}

// Coverage is what a reviewer says its verdict covered. Complete means it judged
// the whole artifact; false with StoppedAt means it stopped early at that finding
// (fail-fast) and the remainder was not reviewed. The binary stores it and
// carries it to the next presentation; it never judges it (sty_93359e0f).
type Coverage struct {
	Complete  bool   `json:"complete"`
	StoppedAt string `json:"stopped_at,omitempty"`
}

// Outcomes of a reviewer attempt that produced no verdict, recorded on its
// agent_invocation row (ReviewerVerdict.Outcome). An accept or reject is the
// verdict row's own record, never repeated here.
const (
	// OutcomeUnavailable: the seat could not judge — the run failed, timed out,
	// stalled or its output named no verdict.
	OutcomeUnavailable = "unavailable"
	// OutcomeMaxTurns: the harness ended the run at its turn cap.
	OutcomeMaxTurns = "max-turns"
)

// RunCounts is what one reviewer run measured about its own work, embedded flat
// like UsageNote. Each figure is a pointer so "the harness reported none" (nil,
// with the adapter-named reason beside it) is never confused with a measured
// zero — satelle-agent-agnostic §2.
type RunCounts struct {
	// Turns is the model turns the run reported (the harness's own num_turns).
	Turns                  *int   `json:"turns,omitempty"`
	TurnsUnavailableReason string `json:"turns_unavailable_reason,omitempty"`
	// ToolCalls is the tool calls satelle saw the run start, counted from the
	// transport's tool events; it is set only for a transport that emits them.
	ToolCalls                  *int   `json:"tool_calls,omitempty"`
	ToolCallsUnavailableReason string `json:"tool_calls_unavailable_reason,omitempty"`
}

// named is c with a reason filled in for each figure that is neither measured
// nor already explained, naming the harness binary of command. The engine names
// the adapter before it gets here; this is the row writer's guard that a row from
// any other path still records an absence, never a bare zero.
func (c RunCounts) named(command string) RunCounts {
	binary := "reviewer"
	if fields := strings.Fields(command); len(fields) > 0 {
		binary = fields[0]
	}
	if c.Turns == nil && c.TurnsUnavailableReason == "" {
		c.TurnsUnavailableReason = binary + ": the run reported no turn count"
	}
	if c.ToolCalls == nil && c.ToolCallsUnavailableReason == "" {
		c.ToolCallsUnavailableReason = binary + ": the run reported no tool-call count"
	}
	return c
}

// GateFailure is the error a reviewer gate returns when a seat attempt produced
// no verdict (a failed run, a turn cap, an unparseable output): Err is the cause
// and Stamps are the invocation records of the attempts that started, which the
// round writer appends as agent_invocation rows before it returns the error, so
// a presentation whose only seat failed still leaves its measured usage.
type GateFailure struct {
	Err    error
	Stamps []ReviewerVerdict
}

func (e *GateFailure) Error() string { return e.Err.Error() }
func (e *GateFailure) Unwrap() error { return e.Err }

// ToolIsolation is what a reviewer invocation's harness offered
// (sty_ef3efb51), embedded flat like UsageNote. OfferedToolCount is a pointer
// so "no count is available" (nil, with the reason in OfferedToolsSource) is
// never confused with a measured zero. The count is the number of tools the
// harness offers — from the harness's own report, or the allow-list satelle
// rendered — never the length of the grant.
type ToolIsolation struct {
	OfferedToolCount *int `json:"offered_tool_count,omitempty"`
	// OfferedToolsSource is "flag", "harness", or
	// "unavailable: <adapter reason>".
	OfferedToolsSource string `json:"offered_tools_source,omitempty"`
	// IsolationLimitation is an adapter-named limitation (prompt trim / skills
	// list / tool trim unavailable), empty when there is none.
	IsolationLimitation string `json:"isolation_limitation,omitempty"`
}

// UsageNote is the explicit-unavailable evidence beside a row's token numbers
// (sty_c8d45201). It is embedded in every usage-carrying record so the JSON is
// flat: usage_unavailable_reason names the adapter and why usage was not
// reported (set only when usage_available is false), and
// cache_split_unavailable marks a provider that reported no cache fields —
// its zero split is unreported, not measured. Both are absent on a row whose
// provider reported its own split, and on rows written before this existed.
type UsageNote struct {
	// Adapter names the harness the usage came from (set by agentcli), so a row
	// whose usage was available still says which adapter produced it.
	Adapter                string `json:"adapter,omitempty"`
	UsageUnavailableReason string `json:"usage_unavailable_reason,omitempty"`
	CacheSplitUnavailable  bool   `json:"cache_split_unavailable,omitempty"`
	// AuthMode is the auth mode (subscription, api_key, org, unrecorded) the
	// binding behind the row is declared to run under (config.AgentBinding.Auth).
	// Set by agentstep from the binding, never detected; absent on a row written
	// before it existed.
	AuthMode string `json:"auth_mode,omitempty"`
}

// ModelUsage is one model's token/cost entry from a transport's resolved-model
// report (sty_87b86044). verb owns its own copy rather than importing agentcli
// (review.go deliberately keeps this package free of the agent CLI package).
type ModelUsage struct {
	ID        string `json:"id"`
	TokensIn  int    `json:"tokens_in,omitempty"`
	TokensOut int    `json:"tokens_out,omitempty"`
	// TokensCacheWrite/TokensCacheRead are this model's cache components of
	// TokensIn, when the transport reported them per-model (sty_363eaf55).
	TokensCacheWrite int      `json:"tokens_cache_write,omitempty"`
	TokensCacheRead  int      `json:"tokens_cache_read,omitempty"`
	CostUSD          *float64 `json:"cost_usd,omitempty"`
}

// TransitionGater judges a requested status transition in an isolated,
// fresh-context subprocess. The implementation lives in internal/agentstep; verb
// holds only the seam so the dispatch layer stays free of the agent CLI.
type TransitionGater interface {
	Gate(ctx context.Context, item workitem.Item, toStatus string) (GateDecision, error)
}

// transitionGater is wired once at bootstrap (cli/app openAppForCmd). Nil means
// no reviewer is configured — transitions enact directly (advisory).
var transitionGater TransitionGater

// SetTransitionGater wires the reviewer that gates status transitions. Pass nil
// to disable gating (tests / no-reviewer environments).
func SetTransitionGater(g TransitionGater) { transitionGater = g }

// CreateDraft is a proposed work item handed to the required-structure reviewer
// before it is persisted.
type CreateDraft struct {
	Kind               string   `json:"kind"`
	Title              string   `json:"title"`
	Body               string   `json:"body,omitempty"`
	AcceptanceCriteria string   `json:"acceptance_criteria,omitempty"`
	Priority           string   `json:"priority,omitempty"`
	Category           string   `json:"category,omitempty"`
	Tags               []string `json:"tags,omitempty"`
}

// CreateReviewer judges a draft work item's required structure before creation,
// in an isolated subprocess. Implemented in internal/agentstep.
type CreateReviewer interface {
	ReviewCreate(ctx context.Context, draft CreateDraft) (GateDecision, error)
}

// createReviewer is wired only when a repo opts into create-gating
// (satelle.toml [review] gate_create). Nil means creation is ungated.
var createReviewer CreateReviewer

// SetCreateReviewer wires the required-structure reviewer. Pass nil to disable.
func SetCreateReviewer(r CreateReviewer) { createReviewer = r }

// AmendField is one definition field an amendment proposes to change, with the
// value it holds now and the value proposed — the before/after pair the gate
// judges and the ledger records (sty_81aa4d8f).
type AmendField struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// AmendDraft is a proposed amendment of a story's FROZEN definition fields,
// handed to the amend gate before anything is written. It carries the story as
// it stands (so the reviewer can judge the correction in context), the state the
// story sits in, the per-field before/after set, and the caller's reason.
type AmendDraft struct {
	Item   workitem.Item `json:"item"`
	Status string        `json:"status"`
	Fields []AmendField  `json:"fields"`
	Reason string        `json:"reason"`
}

// AmendReviewer judges an amendment of frozen definition fields, in an isolated
// subprocess, against the skill+agent the active workflow declares for the
// amend_review lifecycle hook. Implemented in internal/agentstep.
//
// A GateDecision with Gated false means the repo declares no amend gate: the
// caller REFUSES the amendment (the freeze holds) rather than allowing it —
// the opposite of create, where an undeclared gate keeps a bare create legal.
type AmendReviewer interface {
	ReviewAmend(ctx context.Context, draft AmendDraft) (GateDecision, error)
}

var amendReviewer AmendReviewer

// SetAmendReviewer wires the amendment gate. Pass nil to disable — with no
// reviewer wired, `story amend` refuses (fail closed).
func SetAmendReviewer(r AmendReviewer) { amendReviewer = r }

// WorkflowResolver names the workflow that governs a story of a given category,
// so the create path can STAMP the choice on the story (sty_3800ac23) and the
// restamp path can re-resolve it mid-flight (sty_ed3386cf). Wired independently
// of create-gating — a story is stamped whenever a workflow governs it.
// Implemented in internal/agentstep.
type WorkflowResolver interface {
	// The error is non-nil only when the authored process cannot be read
	// (wfgovern.ErrAuthoredProcessUnreadable): the caller refuses and stores
	// nothing, rather than stamping the embedded default's name.
	WorkflowNameFor(ctx context.Context, category string) (string, error)
	// WorkflowStates returns the lifecycle states the named workflow declares and
	// whether the workflow resolves at all — the restamp validation seam. An empty
	// state list on a resolved workflow means the lifecycle was not statically
	// parseable; the caller skips the status-compatibility check rather than
	// blocking the restamp. The error is the same unreadable-process refusal.
	WorkflowStates(ctx context.Context, name string) ([]string, bool, error)
}

var workflowResolver WorkflowResolver

// SetWorkflowResolver wires the governing-workflow resolver. Pass nil to disable
// stamping.
func SetWorkflowResolver(r WorkflowResolver) { workflowResolver = r }

// DispatchResult reports a named-agent executor dispatch (sty_fd427546):
// whether the target state was allocated to a named isolated agent and, when it
// was, which agent/harness performed it and under which rubric.
type DispatchResult struct {
	Dispatched bool   `json:"dispatched"`
	Agent      string `json:"agent,omitempty"`
	Command    string `json:"command,omitempty"`
	// RunID is the id of the remote run a team-placed step woke and waited on
	// (placement = "team:<path>", sty_7028d996); empty for every other dispatch.
	RunID string `json:"run_id,omitempty"`
	// Model is the binding's configured model alias ({model}), recorded on the
	// agent_invocation so the ledger shows WHICH model was asked to run a step —
	// the audit signal for per-step model mixing (e.g. a GLM planner vs an opus
	// in-loop session, sty_5d48317b). The model id is not a secret; the binding's
	// env (endpoint + token) is deliberately NEVER recorded. ModelResolved below
	// is what the transport actually ran.
	Model string `json:"model,omitempty"`
	// ModelResolved/Models mirror GateDecision's fields of the same name
	// (sty_87b86044) — the canonical model id the dispatch actually ran, and
	// every model a multi-model invocation reported.
	ModelResolved string       `json:"model_resolved,omitempty"`
	Models        []ModelUsage `json:"model_usage,omitempty"`
	// ModelSource names why ModelResolved was chosen (config.SelectModel,
	// sty_7069bced) — see GateDecision.ModelSource.
	ModelSource string `json:"model_source,omitempty"`
	// Token/wall-time cost of the dispatch (sty_a699ad14), recorded on the
	// agent_invocation entry. Zero for a plain-text harness with no usage envelope.
	// UsageAvailable false means the tokens were not reported (sty_56aae77a).
	TokensIn       int   `json:"-"`
	TokensOut      int   `json:"-"`
	TokensTotal    int   `json:"-"`
	DurationMs     int64 `json:"-"`
	UsageAvailable bool  `json:"-"`
	// TokensInFresh/TokensCacheWrite/TokensCacheRead split TokensIn (sty_363eaf55).
	TokensInFresh    int `json:"-"`
	TokensCacheWrite int `json:"-"`
	TokensCacheRead  int `json:"-"`
	UsageNote        `json:"-"`
	// CostUSD/CostUnavailableReason mirror GateDecision's fields of the same
	// name (sty_c4df7376).
	CostUSD               *float64 `json:"-"`
	CostUnavailableReason string   `json:"-"`
	// SystemPromptBytes/PayloadBytes are the byte lengths satelle sent — lengths
	// only, never content (sty_363eaf55).
	SystemPromptBytes int    `json:"-"`
	PayloadBytes      int    `json:"-"`
	Skill             string `json:"skill,omitempty"`
	// Output is the dispatched agent's captured stdout (sty_890b86cb). For a task
	// EXECUTION run, the verb layer writes it through as an OKF run-output document
	// under the parent task's folder, so a run's evidence is discoverable per task
	// rather than only in the central executor.log.
	Output string `json:"output,omitempty"`
	// LastMessage is the tail of Output that the run's ledger row carries as
	// last_message, already scrubbed of every value satelle resolved into the
	// performer's environment (OutputTail). Output itself is the raw capture and
	// must never be the source of a ledger row.
	LastMessage string `json:"-"`
	// Changes are the story-configuration changes the performer reported in its
	// final response (sty_af0b1761), decoded by the shared agentartifact seam.
	// The verb applies them only on a step that declares apply_changes.
	Changes []agentartifact.Change `json:"-"`
	// FindingsOffered are the ids of the blocking findings the performer's
	// payload asked it to answer, and Answers its per-finding replies decoded
	// from its final response (sty_f948d345). dispatchPayload records both on the
	// run's agent_invocation row so the gates can be told which went unanswered.
	FindingsOffered []string               `json:"-"`
	Answers         []agentartifact.Answer `json:"-"`
	// FindingsEdgeTo is the exit the findings were taken from when the performer
	// ran at the step it is already in (a same-status re-dispatch): the gate of
	// FindingsEdgeTo reads these answers. Empty on a re-presented edge.
	FindingsEdgeTo string `json:"-"`
	// Progress is which of the offered blocking findings the output answers or
	// changes (sty_73224b86); nil when none was offered. An enumeration, computed
	// where the output is final: dispatchPerformer applies the step's
	// answer_progress policy to it.
	Progress *FindingProgress `json:"-"`
	// ArtifactName/Type identify a Satelle-owned structured step artifact
	// attached before the transition committed.
	ArtifactName string `json:"artifact_name,omitempty"`
	ArtifactType string `json:"artifact_type,omitempty"`
	// Turns is the model turns the run reported, or nil with the adapter-named
	// reason it reported none — a measured actual, never a bound.
	Turns                  *int   `json:"turns,omitempty"`
	TurnsUnavailableReason string `json:"turns_unavailable_reason,omitempty"`
	// Cloud is set when the step was performed by a cloud session
	// (interface=cloud, sty_82cffd60): where it ran and what was collected.
	Cloud *CloudDispatch `json:"cloud,omitempty"`
	// Kind is who performs the step when nothing was dispatched: PerformerExecutor
	// or PerformerInLoop mean the driving session does, and Skills/InLoopRubric
	// then describe what satelle delivered to it (sty_23ceeca2).
	Kind PerformerKind `json:"-"`
	// Skills is the manifest of the step's skills as delivered (name, content hash,
	// delivered or not), recorded on the invocation row. A skill that did not
	// resolve is in it as undelivered; it is never silently dropped.
	Skills []SkillDelivery `json:"-"`
	// InLoopRubric is the composed skill text the driving session is shown on
	// entry to an in-loop step; printing it is the delivery the Skills manifest
	// records.
	InLoopRubric string `json:"-"`
	// Evidence is the skill-evidence check's answer for a dispatched step, run
	// against the performer's output and run trace; nil when the step declares
	// nothing to check.
	Evidence *EvidenceReport `json:"-"`
	// StrictSkills carries the workflow's skill_evidence setting for an in-loop
	// delivery: true means an undelivered skill refuses the transition.
	StrictSkills bool `json:"-"`
	// TraceCommands and TraceTools say the in-loop step's skills declare commands or
	// tools as evidence, so a gate hook must record the calls it lets through.
	TraceCommands bool `json:"-"`
	TraceTools    bool `json:"-"`
	// Trace is a headless run's tool calls, recorded with its performance so the
	// step's exit gate judges the same calls the run's own check did.
	Trace *RunTrace `json:"-"`
}

// CloudDispatch is what a cloud-performed step records: the session, the branch
// it pushed and the commit collected into the story worktree. Commit is empty on
// a dispatch that failed before collection; Doc is the story document the commit
// body was attached under, empty when the binding collects none.
type CloudDispatch struct {
	SessionID string `json:"session_id,omitempty"`
	URL       string `json:"url,omitempty"`
	Branch    string `json:"branch"`
	Commit    string `json:"commit,omitempty"`
	Doc       string `json:"doc,omitempty"`
	// Placement is "remote" when the step was sent to the cloud by its
	// remote_agent placement (sty_dde8b6a4); empty when its own agent is the
	// cloud binding.
	Placement string `json:"placement,omitempty"`
}

// ExecutorDispatcher runs the named isolated agent a workflow node allocates a
// step to (agent=<name> — sty_fd427546): agents.toml defines WHO, the workflow
// The route defines WHERE, the binary only RUNS it. Called after the edge's gates
// accept and BEFORE the status is enacted — an error refuses the transition
// (status unchanged). Implemented in internal/agentstep.
type ExecutorDispatcher interface {
	DispatchExecutor(ctx context.Context, item workitem.Item, toStatus string) (DispatchResult, error)
}

// PerformerResolution answers whether entering a step dispatches a named
// performer. Reason says why not when Dispatched is false.
type PerformerResolution struct {
	Agent      string
	Dispatched bool
	Reason     string
	// Kind says which of the engine's answers this is, so a caller that must act
	// on the difference (the headless drive) reads it from the one owner instead
	// of re-deriving it from the step's agent= key.
	Kind PerformerKind
	// OutputName is the document name the performer's skill declares for its
	// artifact (output_name); empty when the skill declares no output contract.
	OutputName string
	// CriteriaSection is the heading the performer's skill declares for the
	// numbered acceptance criteria inside its output document
	// (output_criteria_section); empty when it declares none (sty_99d3162b).
	CriteriaSection string
	// Team, Remote, Channel and PlacementNote are where the step runs, as the
	// engine's placement rule decided (internal/placement.Decide, which verb
	// cannot import): a team remote path, a cloud or channel remote, and the
	// note for a remote declaration that fell back to local. All empty for a
	// local performer.
	Team          string
	Remote        bool
	Channel       bool
	PlacementNote string
	// ExecutorInLoop answers, for Kind == PerformerExecutor, whether the
	// [executor] binding is the in-loop session (the driving session performs)
	// or a headless command. False for every other Kind.
	ExecutorInLoop bool
}

// PerformerKind classifies a PerformerResolution.
type PerformerKind string

const (
	// PerformerDispatched: a named agent whose headless binding performs the step
	// when it is entered.
	PerformerDispatched PerformerKind = "dispatched"
	// PerformerExecutor: the step is allocated to agent=executor, so whoever the
	// [executor] binding names performs it after it is entered.
	PerformerExecutor PerformerKind = "executor"
	// PerformerInLoop: a named agent bound in-loop — the driving session performs.
	PerformerInLoop PerformerKind = "in-loop"
	// PerformerNone: the step has no performer (agent-less, a reviewer step, a
	// non-performer binding, or no workflow governs the item).
	PerformerNone PerformerKind = "none"
	// PerformerUndeclared: the workflow declares no such step.
	PerformerUndeclared PerformerKind = "undeclared"
)

// PerformerResolver is the engine's single rule for who performs a step
// (DispatchExecutor applies it too), exposed so a caller that must know BEFORE
// it runs anything — `satelle story ready` — asks the same rule a presented
// edge uses rather than keeping a second copy (sty_a32bd6a9). The dispatcher
// wired with SetExecutorDispatcher implements it.
type PerformerResolver interface {
	StepPerformer(ctx context.Context, item workitem.Item, toStatus string) (PerformerResolution, error)
}

// executorDispatcher is wired at bootstrap beside the transition gater. Nil
// keeps every step in-loop (today's behaviour).
var executorDispatcher ExecutorDispatcher

// SetExecutorDispatcher wires the named-agent dispatch. Pass nil to disable.
func SetExecutorDispatcher(d ExecutorDispatcher) { executorDispatcher = d }

// SummaryResult is the step summariser's read-only recap plus the cost of the
// isolated agent invocation that produced it (sty_a699ad14): Command/Context/
// Model/tokens/duration mirror a reviewer's ReviewerVerdict so the same
// agent_invocation payload shape covers both. Zero-value cost fields mean the
// summariser produced no billable invocation (e.g. it never ran).
type SummaryResult struct {
	Text           string
	Command        string
	Context        string
	Model          string
	ModelResolved  string // resolved model id the summariser actually ran (sty_87b86044)
	Models         []ModelUsage
	ModelSource    string // why ModelResolved was chosen (config.SelectModel, sty_7069bced)
	TokensIn       int
	TokensOut      int
	TokensTotal    int
	DurationMs     int64
	UsageAvailable bool
	// TokensInFresh/TokensCacheWrite/TokensCacheRead split TokensIn (sty_363eaf55).
	TokensInFresh    int
	TokensCacheWrite int
	TokensCacheRead  int
	UsageNote
	// CostUSD/CostUnavailableReason mirror GateDecision's fields of the same
	// name (sty_c4df7376).
	CostUSD               *float64
	CostUnavailableReason string
	// SystemPromptBytes/PayloadBytes are the byte lengths satelle sent — lengths
	// only, never content (sty_363eaf55).
	SystemPromptBytes int
	PayloadBytes      int
	// ToolIsolation is what the summariser's harness offered (sty_ef3efb51).
	ToolIsolation
}

// FindingProgress is the fact of how a re-presented performer's output stands
// against the blocking findings it was offered (sty_73224b86): Offered lists
// their ids, Progressed those it answered or whose quoted text it changed, and
// Unprogressed the rest. Whether too few progressed is the step's policy.
type FindingProgress struct {
	Offered      []string
	Progressed   []string
	Unprogressed []string
}

// Required is how many offered findings must progress under min_progress: the
// threshold capped at the number offered, so a single finding can always satisfy it.
func (p FindingProgress) Required(minProgress int) int {
	return min(max(minProgress, 1), len(p.Offered))
}
