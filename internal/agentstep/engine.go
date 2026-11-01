// Package agentstep is satelle's isolated-agent dispatch engine — the
// quality-management spine that runs a fresh-context agent over a workflow step
// and folds its result back in. It dispatches three kinds of isolated agent, all
// briefed and run through one seam (invoke.go): a GATE REVIEWER judging a status
// transition and returning a verdict, a NAMED EXECUTOR performing a step's work,
// and the STEP SUMMARISER narrating a transition. A reviewer is just one agent
// kind, so the engine (type Engine) is not named for it.
//
// Reviewer path: the active workflow names a reviewer_skill per edge; the skill's
// markdown body rides as the agent's appended system prompt; the work item +
// requested transition go in on stdin; the agent prints one JSON object
// {decision, notes, reasoning} and may include reviewed, the exact words that
// verdict rests on. The decision is parsed strictly into an accept/reject.
// Accept lets the caller enact; reject blocks and pushes the notes back to the
// executor. The binary stores reviewed; it does not compare it.
// LLM gate and named-dispatch runs share Invoke (invoke.go) — one path that
// calls agentcli.Runner.Run (sty_ba860c8a).
//
// The edge is gated only when the workflow names a reviewer_skill AND that
// skill's rubric is installed in the substrate. A named-but-absent rubric (e.g.
// the canonical default referencing a skill not yet embedded) is treated as
// advisory, so gating switches on exactly when the rubrics ship — the gateless
// baseline keeps working until then.
package agentstep

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bobmcallan/satelle/internal/logfile"
	"github.com/bobmcallan/satelle/internal/logsread"

	"github.com/bobmcallan/satelle/internal/agentartifact"
	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/board"
	"github.com/bobmcallan/satelle/internal/compact"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/placement"
	"github.com/bobmcallan/satelle/internal/retrieve"
	"github.com/bobmcallan/satelle/internal/sessionconfig"
	"github.com/bobmcallan/satelle/internal/structure"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/wfhook"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// DocGetter is the read surface the engine needs over the authored-doc index
// (satisfied by *docindex.Store) — listing workflows (to resolve the one active
// for an item's category) and getting the reviewer skills / the baseline.
type DocGetter interface {
	Get(ctx context.Context, kind, name string) (docindex.Doc, error)
	List(ctx context.Context, kind string) ([]docindex.Doc, error)
}

// defaultTools is the reviewer's read-only tool grant. It judges, never MUTATES
// the work tree (Write/Edit/NotebookEdit are also denied by the harness ceiling).
// It needs NO shell: structural conformance is deterministic code (internal/
// structure), and the substrate it reasons about — skills, principles, workflows —
// is materialised as markdown under .satelle (satelle init), so Read/Grep/Glob
// resolve everything. A repo may still widen this in .satelle/workflows/agents.toml
// (transparently, the operator's choice); the default grant is read-only.
const defaultTools = "Read,Grep,Glob"

// defaultCheckTimeout bounds a functional check (deploy/integration can be slow,
// but a hung command must not block a transition forever).
const defaultCheckTimeout = 20 * time.Minute

// Engine judges status transitions against the active workflow's reviewer skills.
// A skill is either an LLM reviewer (its body rides as an isolated agent's system
// prompt) or a functional check (its frontmatter names a deterministic `check:`
// command the gate runs — the command's exit code is the verdict).
type Engine struct {
	runner   agentcli.Runner
	docs     DocGetter
	repoRoot string
	model    string
	tools    string
	// reviewerEnv is the reviewer binding's resolved env (config.ResolveAgentEnvs),
	// layered onto the reviewer AND summariser subprocesses — both are isolated
	// children, so both carry the binding's env (sty_001558ce). A named executor
	// uses its OWN binding.Env instead.
	reviewerEnv  map[string]string
	checkTimeout time.Duration
	// check runs a functional-check command in dir and returns its combined
	// output. The command receives the SAME stdin transition payload an LLM
	// reviewer gets ({story, from, to, review_skill}), so a coded gate can judge
	// the story's tags/edge deterministically (sty_f804caaa). Swappable in
	// tests; defaults to a real `bash -c` exec.
	check func(ctx context.Context, dir, command, payload string) (string, error)
	// children resolves a parent's child stories (id + status) for a container
	// close gate's payload. Nil when unwired (no children injected).
	children func(ctx context.Context, item workitem.Item) []ChildState
	// itemDocs resolves an item's attached documents (name/type/body) so isolated
	// agents judge attachments from the PAYLOAD — no disk path required
	// (sty_58fa970e). Nil-safe: an unwired resolver injects no docs.
	// Named itemDocs to avoid clashing with the docs DocGetter substrate field.
	itemDocs func(ctx context.Context, itemID string) []DocState
	// toolTrace reads the in-loop session's tool calls for the skill-evidence
	// check (sty_23ceeca2); nil makes every in-loop command/tool item unverifiable.
	toolTrace ToolTraceFunc
	// priorVerdicts resolves the verdicts already recorded for THIS item on THIS
	// from→to edge (oldest first), so a re-review judges the delta instead of
	// re-reading the artefact with no memory (sty_0f5e600c). Nil-safe: an unwired
	// resolver injects nothing.
	// ex narrows what marks a verdict stale (sty_59720b8b); the zero value is the
	// unqualified rule.
	priorVerdicts func(ctx context.Context, itemID, from, to string, ex verb.StaleExemption) []PriorVerdict
	// definitionEdits resolves the definition edits recorded for THIS item, oldest
	// first, so an intent reviewer judges an edited AC against the story's purpose
	// (sty_5262592e). Nil-safe: an unwired resolver injects nothing.
	definitionEdits func(ctx context.Context, itemID string) []DefinitionEdit
	// deferrals resolves the objections a rework relay's consult seat deferred
	// to the grant or to a later gate; fillDeferred hands each gate its own.
	// Nil-safe: an unwired resolver injects nothing.
	deferrals func(ctx context.Context, itemID string) []DeferredItem
	// waivers resolves the developer waivers recorded for an item (every gate,
	// oldest first); fillWaivers hands each gate its own. Nil-safe: an unwired
	// resolver injects nothing.
	waivers func(ctx context.Context, itemID string) []WaiverItem
	// findingAnswers reports how the performer of an edge answered the findings it
	// was offered (sty_f948d345); fillFindingAnswers hands the gate the fact.
	// Nil-safe: an unwired resolver injects nothing.
	findingAnswers func(ctx context.Context, itemID, from, to string) (FindingAnswers, bool)
	// exitReject names the exit of a step whose gate rejected most recently, ""
	// when none did, so a performer run at the step it is in is offered that
	// exit's findings. Nil-safe: unwired, such a run is offered none.
	exitReject func(ctx context.Context, itemID, from string) string
	// itemDiff resolves the live engagement slice for a reviewer payload
	// (sty_a125b440). Nil-safe: an unwired resolver injects no Diff. The
	// resolver itself must never error — map missing baseline / git failure to
	// a no-baseline marker so the transition cannot fail on enumeration.
	itemDiff func(ctx context.Context, itemID string) *DiffState
	// citationTree resolves the working tree a gate judges, which fillCitations
	// reads the cited lines from. Nil-safe: an unwired resolver leaves every
	// citation unresolved with the reason.
	citationTree func(ctx context.Context, itemID string) (string, error)
	// itemMessages resolves directed agent messages for a gate/executor payload
	// (sty_2db624d0). Nil-safe: an unwired resolver injects nothing. The
	// resolver itself must never error — skip unreadable rows.
	itemMessages func(ctx context.Context, itemID string, addresses []string) []MessageState
	// messagesDelivered marks the messages a performer's payload carried
	// delivered (sty_73c9aa10). Nil-safe: unwired, nothing is marked. Its error
	// is warned and dropped — messaging never fails the step it rides on.
	messagesDelivered func(ctx context.Context, itemID string, msgIDs []string) error
	// diffCompressor reduces a gate payload's patch (noise-strip then ranked
	// hunk selection, sty_918e2086) before diffPayloadCeiling is ever applied —
	// the compressor is the PRIMARY reducer, the ceiling stays a backstop. Nil
	// leaves fillDiff's patch handling exactly as before this story.
	diffCompressor func(ctx context.Context, itemID, patch string) string
	// diffOffload stores an oversized patch's overflow tail out of line for
	// excerptOffload's backstop marker (sty_918e2086), independent of whether
	// diffCompressor is wired — even an uncompressed patch gets a recoverable
	// marker instead of a bare truncation. Nil falls back to the plain
	// "… [truncated]" suffix excerpt() has always appended.
	diffOffload func(ctx context.Context, itemID string, content []byte) (hash string, err error)
	// checkLogCompressor reduces a failing functional check's captured output
	// into the gate's reject notes (internal/compact.CompressLog,
	// sty_ef930f81), offloading the full raw log behind a retrieval marker.
	// Nil falls back to compact.CompressLog(out, compact.LogConfig{}, nil) —
	// bounded head/tail notes with no marker.
	checkLogCompressor func(ctx context.Context, itemID, log string) string
	// trackingStory resolves the id of an OPEN story already diagnosing a failing
	// authored document, so a refusal caused by that document points at the
	// diagnosis instead of leaving it in backlog (sty_88d40a60). Injected because
	// this package must stay free of the store; nil-safe — an unwired resolver
	// names nothing and the refusal reads exactly as before.
	trackingStory func(ctx context.Context, kind, name string) string
	// injectPrinciples is a cache of the reviewer binding's principle injection
	// (sty_46a40208). Order:2 feeds Invoke from reviewerBinding; this flag remains
	// so SetInjectPrinciples / tests keep working until order:3 retires the scalar.
	injectPrinciples bool
	// reviewerBinding is the agents.toml [reviewer] binding — the SINGLE resolution
	// shape for gate tools/model/env/principles/role (sty_ba860c8a AC3). Scalar
	// SetReviewer* mutators update this binding so existing tests keep working.
	reviewerBinding config.AgentBinding
	// constitution is the project constitution body (frontmatter stripped), injected
	// order-zero whenever principles ≠ none (design §5.3). Wired by the CLI via
	// SetConstitution — the engine must not import cli.
	constitution string
	// attempts bounds how many times an LLM reviewer is retried when it produces NO
	// verdict — a TRANSIENT failure (a rate-limited/killed/empty subprocess under
	// concurrent sessions, sty_d71b0791), distinct from a genuine accept/reject which
	// returns on the first try. Defaults to defaultReviewerAttempts (New sets it).
	attempts int
	// backoff returns the wait before retry N (N ≥ 2) so transient contention can
	// clear. Swappable in tests (return 0 to avoid real waits); defaults to
	// defaultReviewerBackoff.
	backoff func(attempt int) time.Duration
	// logDir is <data_dir>/logs — where a transient reviewer failure (the failing
	// subprocess's own output) is appended to reviewer.log so API contention is
	// REVIEWABLE, not lost (sty_d71b0791). Empty disables logging (tests/unwired).
	// logCfg bounds that log's growth (daily + size + retention, sty_a67e6e8c).
	logDir string
	logCfg logfile.Config
	// progress, when set, receives one-line status messages while a slow gate
	// runs ("running reviewer <skill>…"), so a legitimate multi-minute review is
	// visibly distinct from a hang (sty_6c88ca10). The CLI wires it to stderr;
	// nil (web/tests) disables emission.
	progress func(msg string)
	// warnOut receives the one-line isolation warnings of a reviewer dispatch the
	// binding cannot hold to its grant (sty_2d5e583a). New defaults it to stderr.
	warnOut io.Writer
	// activity, when set, receives structured gate/phase progress so seat can
	// expose which gate is running without the dispatching terminal (sty_598a8e1b).
	// Callback includes the story id so the sink stays stateless.
	activity func(itemID string, a Activity)
	// activityDetail, when set, receives throttled in-flight DISPATCH metadata
	// on real events (sty_752c4ef2) — see SetActivityDetail.
	activityDetail func(itemID string, d ActivityDetail)
	// agentTimeout is an OPTIONAL hard ceiling on EACH nested agent invocation
	// (a reviewer attempt, a named dispatch, or a step summary) with a context
	// deadline (sty_6c88ca10). Zero/negative — the default (sty_752c4ef2) —
	// disables it: the idle-stall detector (idleTimeout) is what actually
	// bounds a dispatch, so a progressing agent is never cut off by elapsed
	// time alone. An operator sets a hard ceiling explicitly (binding
	// timeout=) when they want one anyway.
	agentTimeout time.Duration
	// idleTimeout bounds how long ONE nested agent invocation may go with no
	// REAL event (tool start/end, message, usage — heartbeats excluded)
	// before a Watchdog judges it stalled and cancels it (sty_752c4ef2).
	// Zero/negative disables stall detection (tests). resolveIdleTimeout, when
	// wired, resolves a NAMED binding's own idle_timeout against the shared
	// [defaults] table; unwired dispatches (gate reviewer, step summary) fall
	// back to this engine-wide default.
	idleTimeout        time.Duration
	resolveIdleTimeout func(section string, b config.AgentBinding) (time.Duration, error)
	// busyTimeout / resolveBusyTimeout bound how long CPU-liveness may keep a
	// silent command-transport run alive past idle_timeout (sty_db62a3b9);
	// same wiring shape as idleTimeout. Zero disables CPU liveness.
	busyTimeout        time.Duration
	resolveBusyTimeout func(section string, b config.AgentBinding) (time.Duration, error)
	// namedAgents resolves a NAMED agent binding from the agents layer
	// (.satelle/workflows/agents.toml [<name>] sections) for executor dispatch
	// (sty_fd427546). Nil keeps every step in-loop.
	namedAgents func(name string) (config.AgentBinding, bool)
	// liveNamedAgents resolves a NAMED binding for a LIVE session
	// (OpenSessionAs) as AgentsConfig.LiveBinding — EffectiveBinding(UseLive)
	// over the raw binding, so an unset interface= picks the CLI's best live
	// transport (epic:model-selection child 2). Nil falls back to namedAgents.
	liveNamedAgents func(name string) (config.AgentBinding, bool)
	// oneShotAgents resolves a binding for a ONE-SHOT run of a section the
	// driving session would otherwise perform ([executor]): EffectiveBinding
	// (UseOneShot) over the raw binding, with each role's own baseline default.
	// Nil means no such run can be made (PerformExecutorStep refuses).
	oneShotAgents func(name string) (config.AgentBinding, bool)
	// resolveSecondary returns a fallback binding for rate-limit failover
	// (sty_5bf61f89). Nil disables secondary retry.
	resolveSecondary func(section string, b config.AgentBinding) (config.AgentBinding, string, bool)
	// newRunner builds the runner for a named binding's interface+command —
	// swappable in tests; defaults to agentcli.RunnerFromBinding
	// (epic:agent-dispatch-transport). iface is "command" (default) or "acp".
	newRunner func(iface, command string) (agentcli.Runner, error)
	// newOpener builds a SessionOpener for a live binding (sty_1de7494c).
	// Swappable in tests; defaults to agentcli.OpenerFromBinding.
	newOpener func(iface, command string) (agentcli.SessionOpener, error)
	// telemetry records a structured, queryable dispatch outcome (a reviewer/
	// executor retry, failure, or timeout) that only the binary observes — the
	// verb layer sees just the final result, not each attempt (sty_b73c3236). Nil
	// disables it (tests / no-ledger environments); best-effort like the other
	// engine-owned logging.
	telemetry TelemetryFunc
	// attachArtifact deposits a validated structured step artifact before the
	// transition status commits. Nil refuses a contracted dispatch rather than
	// silently dropping required output.
	attachArtifact func(context.Context, workitem.Item, string, string, string) (string, string, error)
	// sessionModels resolves the latest captured model per role (in-loop,
	// creator) for a story — config.SelectModel's inherited/creator tiers
	// (sty_7069bced). Nil-safe: an unwired resolver leaves every role unknown,
	// so selection falls through those tiers to [model_order] / cli-default.
	sessionModels func(ctx context.Context, storyID string) (inLoop, creator config.SessionModel)
	// modelOrder returns an executable's configured [model_order] list
	// (sty_4fde0a50); the order tier of config.SelectModel.
	modelOrder func(executable string) []config.ModelRank
	// invocationRecorder appends an agent_invocation ledger row directly
	// (sty_7069bced 4.2) — the seam a LIVE session's open/close uses, so it
	// shows up in `satelle story cost` and the web timeline the same way a
	// one-shot dispatch does. Nil-safe: an unwired recorder writes no rows.
	invocationRecorder func(ctx context.Context, itemID string, payload map[string]any) error
	// suiteRecord and suiteLookup are the ledger seam behind check_reuse: tree
	// (sty_dc554488): record appends one check run as a suite_run row, lookup
	// finds a green row for the same story, check, command and tree. Both nil
	// (unwired) means a check always runs and nothing is recorded.
	suiteRecord func(ctx context.Context, storyID string, ev verb.SuiteEvidence) (string, error)
	suiteLookup func(ctx context.Context, storyID, check, commandSHA, tree string) (verb.SuiteEvidence, bool)
	// wiring is the gate-wiring policy a performer dispatch is held to in a
	// linked worktree (sty_f141c77f). Nil means unguarded; the CLI always wires it.
	wiring *WiringGuard
	// leftoverRule is the repo's [dispatch.leftovers] config (sty_e7aaf8b1) —
	// what counts as debris a coder session left in the tree. Zero value
	// (empty Patterns, empty ContentRegex) disables the sweep entirely: the
	// binary ships no opinion about what a leftover looks like.
	leftoverRule config.LeftoverRule
	// cloudPoll / cloudDefaultDeadline pace and bound the wait for a cloud
	// performer's pushed branch (sty_82cffd60): the poll interval, and the
	// deadline a cloud binding gets when it sets no timeout. Tests shorten both.
	cloudPoll            time.Duration
	cloudDefaultDeadline time.Duration
	// cloudDispatch launches a cloud performer's session. Nil means
	// dispatchCloud; SetCloudDispatch replaces it, so a caller that must never
	// reach a cloud (a local test run of a remote definition) installs a refusal
	// instead of relying on every binding being checked first.
	cloudDispatch cloudDispatchFunc
	// remoteChannel pairs a placement = "remote" binding with its performer, and
	// teamRunner wakes a placement = "team:<path>" binding's remote and reads its
	// run (sty_7028d996). Nil refuses the dispatch; there is no local fallback.
	// teamClock is the time the team wait reads (nil: the real clock).
	remoteChannel RemoteChannel
	teamRunner    TeamRunner
	teamClock     teamClock
}

// scrubValues is every value satelle resolved into a performer's environment —
// the binding's env after ${VAR} resolution — keyed by variable name. A
// performer's output is scrubbed of them (verb.OutputTail) before any of it is
// written to the ledger.
func (g *Engine) scrubValues(bindingEnv map[string]string) map[string]string {
	out := make(map[string]string, len(bindingEnv))
	for k, v := range bindingEnv {
		out[k] = v
	}
	return out
}

// cloudDispatchFunc is the shape of dispatchCloud, called as a method
// expression so a replacement receives the engine it is installed on.
type cloudDispatchFunc func(g *Engine, ctx context.Context, item workitem.Item, toStatus, wfName, agent string,
	binding config.AgentBinding, composed []string, skill string, remotePlaced bool) (verb.DispatchResult, error)

// SetLeftoverRule wires the repo's leftover-sweep configuration
// (sty_e7aaf8b1). Unset (the zero value) disables the sweep.
func (g *Engine) SetLeftoverRule(rule config.LeftoverRule) { g.leftoverRule = rule }

// TelemetryFunc records one typed telemetry/quality event for storyID. Callers
// pass the event's outcome/kind and its typed data (never env/secrets — the
// implementation validates). Implemented by verb.AppendTelemetry and wired via
// SetTelemetry; best-effort by contract, so it takes no error return.
type TelemetryFunc func(ctx context.Context, storyID, actor, kind string, data map[string]any)

// SetTelemetry wires the sink the engine uses to record a dispatch-level
// telemetry event (agent-retry/agent-failure/agent-timeout). Pass nil to
// disable (the default) — every call site nil-checks via telemetryEvent.
func (g *Engine) SetTelemetry(fn TelemetryFunc) { g.telemetry = fn }

// telemetryEvent records one telemetry event via the wired sink, if any.
func (g *Engine) telemetryEvent(ctx context.Context, storyID, actor, kind string, data map[string]any) {
	if g.telemetry != nil {
		g.telemetry(ctx, storyID, actor, kind, data)
	}
}

// classifyOutcome names WHY a dispatched agent invocation failed, for the
// structured telemetry payload (AC2, sty_b73c3236): a bounded deadline is a
// timeout, a process killed out from under the caller (e.g. OOM, a session
// cancelling a concurrent claude subprocess) surfaces Go's "signal: killed" in
// the error text, and anything else is a generic error. A nil err (a parsed
// no-verdict output, not a run error) is reported as such by the caller instead.
func classifyOutcome(err error) string {
	if err == nil {
		return "no-verdict"
	}
	if errors.Is(err, agentcli.ErrMaxTurns) {
		return verb.OutcomeMaxTurns
	}
	if asStallError(err) != nil {
		return "stalled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if strings.Contains(err.Error(), "signal: killed") {
		return "signal:killed"
	}
	return "error"
}

// causeOf is the diagnostic text a failed attempt leaves on its telemetry row
// beside the outcome class: the error the transport returned, stripped,
// redacted and bounded by agentcli.SafeText. "" for a nil error.
func causeOf(err error) string {
	if err == nil {
		return ""
	}
	return agentcli.SafeText(err.Error())
}

// withCause adds err's bounded cause to a telemetry data map and returns it.
func withCause(data map[string]any, err error) map[string]any {
	if c := causeOf(err); c != "" {
		data["cause"] = c
	}
	return data
}

// New builds a Engine over the agent runner and doc index. model "" inherits the
// agent's default; the tool grant is read-only.
func New(runner agentcli.Runner, docs DocGetter, repoRoot, model string) *Engine {
	return &Engine{
		runner: runner, docs: docs, repoRoot: repoRoot, model: model, tools: defaultTools,
		checkTimeout: defaultCheckTimeout, check: execCheck, injectPrinciples: true,
		attempts: defaultReviewerAttempts, backoff: defaultReviewerBackoff,
		agentTimeout: defaultHardTimeout, idleTimeout: defaultIdleTimeout,
		busyTimeout: config.DefaultBusyTimeout,
		cloudPoll:   defaultCloudPoll, cloudDefaultDeadline: defaultCloudDeadline,
		newRunner: LookupRunner,
		newOpener: agentcli.OpenerFromBinding,
		warnOut:   os.Stderr,
	}
}

// SetWarnWriter redirects the reviewer isolation warnings (stderr by default);
// nil discards them.
func (g *Engine) SetWarnWriter(w io.Writer) { g.warnOut = w }

// ExecutableNotFoundError is returned by the default runner constructor when a
// binding's program is not on this machine's PATH (sty_01949949 AC3). Local
// resolution is the rule for every binding — a workspace-published one names
// `claude`, and THIS machine decides whether `claude` exists — and a miss is a
// hard refusal, never a silent in-loop downgrade (the failure mode the AC
// names). DispatchExecutor formats it with the workflow/state/agent context.
type ExecutableNotFoundError struct {
	Command string // the command template
	Token   string // the program token that failed to resolve
	Err     error  // the exec.LookPath error
}

func (e *ExecutableNotFoundError) Error() string {
	return fmt.Sprintf("command %q is not executable on this machine (%v)", e.Token, e.Err)
}

func (e *ExecutableNotFoundError) Unwrap() error { return e.Err }

// LookupRunner is the DEFAULT runner constructor: agentcli.RunnerFromBinding
// preceded by local executable resolution. It is the default rather than an
// unconditional step inside DispatchExecutor so tests that inject fake commands
// through the newRunner seam keep their transport-level fakes. Exported so a
// remote node can resolve every binding it was handed before it reports ready,
// and a caller that must prove a binding runnable before dispatching (the remote
// test run's pre-flight) asks the same question DispatchExecutor does.
func LookupRunner(iface, command string) (agentcli.Runner, error) {
	if tok := config.ExecutableToken(command); tok != "" {
		if _, err := exec.LookPath(tok); err != nil {
			return nil, &ExecutableNotFoundError{Command: command, Token: tok, Err: err}
		}
	}
	return agentcli.RunnerFromBinding(iface, command)
}

// defaultHardTimeout is the shipped default for the OPTIONAL hard ceiling on
// one nested agent invocation: unset (sty_752c4ef2). A wall-clock cap killed
// working agents mid-implementation twice (sty_87b86044 at 10m, sty_7069bced
// at 20m, both while tool events were still arriving seconds before the
// cut) — the timeout existed to catch a STUCK process, not to limit a
// progressing one. defaultIdleTimeout now does that job; an operator who
// still wants a hard cap sets a binding's timeout= explicitly.
const defaultHardTimeout = 0

// defaultIdleTimeout is the shipped default idle-stall bound (sty_752c4ef2).
// Per-binding idle_timeout=, or the shared [defaults] table, overrides it
// without a recompile. config.DefaultIdleTimeout is the single source of
// truth — defined there (not here) so verb, which this package imports, can
// use the same default without an import cycle.
const defaultIdleTimeout = config.DefaultIdleTimeout

// DefaultIdleTimeout exports defaultIdleTimeout for callers outside this
// package (the cli package's live-session turn loops — chat, rework relay —
// which resolve a binding's idle-stall bound themselves and need the same
// shipped fallback the engine uses internally).
const DefaultIdleTimeout = defaultIdleTimeout

// SetProgress wires the sink for one-line gate progress messages (the CLI
// prints them to stderr). nil disables emission.
func (g *Engine) SetProgress(fn func(msg string)) { g.progress = fn }

// Activity is structured transition progress: which gate/phase is running,
// its 1-based index, and the size of the set (sty_598a8e1b). Gates marks a gate
// round's stamp: Total is then the round's whole gate list, and Passed and
// Failed count the round's gates that have returned an accept or a reject so far
// (sty_216d5205). Every other phase (a performer dispatch, a summariser) leaves
// Gates false.
type Activity struct {
	Label  string
	Index  int
	Total  int
	Gates  bool
	Passed int
	Failed int
}

// SetActivity wires the structured progress sink (CLI stamps the lease row).
func (g *Engine) SetActivity(fn func(itemID string, a Activity)) { g.activity = fn }

// ActivityDetail is in-flight DISPATCH metadata (sty_752c4ef2), distinct from
// the phase-level Activity above: which agent binding and model are running,
// the last real event's label, and how many real events have been seen. It
// rides the SAME lease row (extended, not duplicated) — the CLI wiring folds
// it into lease.Store.SetActivityDetail.
type ActivityDetail struct {
	Agent      string
	Model      string
	Pid        int
	EventLabel string
	EventAt    time.Time
	EventCount int
}

// SetActivityDetail wires the throttled in-flight dispatch sink (sty_752c4ef2).
// Invoke calls it on real events (tool start/end, message, usage) during an
// ExpectPerform dispatch, throttled to activityDetailThrottle, so a long
// dispatch's lease row — and the mirror push the wired sink performs — stays
// fresh without a write per event. Nil (the default) disables it.
func (g *Engine) SetActivityDetail(fn func(itemID string, d ActivityDetail)) { g.activityDetail = fn }

// activityDetailThrottle bounds how often SetActivityDetail's sink is called
// per dispatch (sty_752c4ef2) — named and documented per the architecture
// review: real-event volume (ACP message chunks especially) must not become a
// write per token. A var, not a const, so a test can shorten it rather than
// running a real 5s span.
var activityDetailThrottle = 5 * time.Second

// emitProgress sends one progress line to the wired sink, if any.
func (g *Engine) emitProgress(format string, a ...any) {
	if g.progress != nil {
		g.progress(fmt.Sprintf(format, a...))
	}
}

// emitActivity sends structured progress for the given story, if wired.
func (g *Engine) emitActivity(itemID, label string, index, total int) {
	g.emit(itemID, Activity{Label: label, Index: index, Total: total})
}

func (g *Engine) emit(itemID string, a Activity) {
	if g.activity != nil && itemID != "" {
		g.activity(itemID, a)
	}
}

// gateTally is ONE gate round's live count, shared by every pass that
// dispatches part of it (a reviewer_carry round's fresh and confirm passes), so
// its stamps always count against the edge's whole enqueued gate list and never
// restart mid-round. Guarded for the parallel loops, which stamp under the lock
// so emissions stay monotonic (sty_216d5205).
type gateTally struct {
	mu                          sync.Mutex
	total, done, passed, failed int
	opened                      bool
}

func newGateTally(total int) *gateTally { return &gateTally{total: total} }

// stamp emits the tally's current counts at index under label. Caller holds mu.
func (g *Engine) stamp(itemID, label string, t *gateTally, index int) {
	g.emit(itemID, Activity{Label: label, Index: index, Total: t.total, Gates: true, Passed: t.passed, Failed: t.failed})
}

// gatesOpened emits the round's opening stamp (nothing returned yet), once per
// round however many passes dispatch it.
func (g *Engine) gatesOpened(itemID, label string, t *gateTally) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.opened {
		return
	}
	t.opened = true
	g.stamp(itemID, label, t, 0)
}

// gateStarted stamps the gate (or bundle) about to be dispatched.
func (g *Engine) gateStarted(itemID, label string, t *gateTally) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g.stamp(itemID, label, t, t.done+1)
}

// gateReturned adds each returned gate's outcome to the tally and stamps it. An
// accept passes and a reject or an error fails; a gate whose rubric is absent
// (advisory, ungated) judged nothing, so it leaves the round's total.
func (g *Engine) gateReturned(itemID, label string, t *gateTally, slots ...gateSlot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range slots {
		switch {
		case s.err != nil:
			t.failed++
		case !s.dec.Gated:
			t.total--
			continue
		case s.dec.Accept:
			t.passed++
		default:
			t.failed++
		}
		t.done++
	}
	g.stamp(itemID, label, t, t.done)
}

// defaultReviewerAttempts is how many times an LLM reviewer is tried before a
// no-verdict transient failure is surfaced. A gated transition must be
// deterministic (advance or a clear error), and a nested reviewer subprocess can
// transiently return no verdict under concurrent load (sty_d71b0791), so it is
// retried a few times before giving up.
const defaultReviewerAttempts = 3

// defaultReviewerBackoff is the wait before retry N (called only for N ≥ 2), a
// short escalating pause so transient contention (e.g. many claude subprocesses
// across sessions) can clear before the next attempt.
func defaultReviewerBackoff(attempt int) time.Duration {
	if attempt <= 2 {
		return 2 * time.Second
	}
	return 5 * time.Second
}

// SetLogDir points the reviewer's transient-failure log at dir (the repo's
// <data_dir>/logs) and bounds it per cfg. When set, each transient reviewer
// failure — the failing subprocess's own output, e.g. a rate-limit message — is
// appended to reviewer.log so cross-session API contention is reviewable
// (sty_d71b0791), rotated daily + by size (sty_a67e6e8c). An empty dir disables
// logging.
func (g *Engine) SetLogDir(dir string, cfg logfile.Config) { g.logDir, g.logCfg = dir, cfg }

// logReviewerFailure appends one no-verdict failure record to
// <logDir>/reviewer.log via the shared rotating writer, so the actual cause —
// a rate-limited nested agent, or a reviewer that answered outside the JSON
// contract — is surfaced for review. The subprocess's FULL output is logged
// (newlines flattened), not a truncated tail: when the reviewer wrote real
// reasons without a parseable verdict, those words must be recoverable by the
// executor (sty_9485d47e). Rotation (daily + size + retention) bounds growth.
// Best-effort: a logging error never affects the gate.
func (g *Engine) logReviewerFailure(skill string, attempt, attempts int, rerr error, out []byte) {
	if g.logDir == "" {
		return
	}
	label := "reviewer subprocess error"
	full := ""
	if s := strings.TrimSpace(string(out)); s != "" {
		label = "no verdict in reviewer output"
		full = " — full output: " + strings.ReplaceAll(s, "\n", "\\n")
	}
	now := time.Now()
	line := fmt.Sprintf("%s\t%s\tattempt %d/%d\t%s: %v%s",
		now.UTC().Format(time.RFC3339), skill, attempt, attempts, label, rerr, full)
	_ = logfile.Append(now, filepath.Join(g.logDir, "reviewer.log"), g.logCfg, line)
}

// logProseFallback records that a reviewer's verdict was recovered from PROSE
// (no JSON decision object) — a normal decision, logged for observability so a
// reviewer drifting off the JSON contract is visible without failing the gate
// (sty_9485d47e). Best-effort.
func (g *Engine) logProseFallback(skill string, accept bool) {
	if g.logDir == "" {
		return
	}
	verdict := "reject"
	if accept {
		verdict = "accept"
	}
	now := time.Now()
	line := fmt.Sprintf("%s\t%s\tprose-verdict fallback: parsed %q from non-JSON reviewer output",
		now.UTC().Format(time.RFC3339), skill, verdict)
	_ = logfile.Append(now, filepath.Join(g.logDir, "reviewer.log"), g.logCfg, line)
}

// SetReviewerTools sets the reviewer's tool grant from the agents layer (the
// resolved `reviewer` binding). It governs every isolated LLM reviewer this Engine
// runs. The default remains the read-only grant; a repo may widen or narrow it in
// .satelle/workflows/agents.toml without touching the workflow. An empty value is ignored
// so callers can pass through an unset binding safely. Also mutates reviewerBinding.
func (g *Engine) SetReviewerTools(tools string) {
	if strings.TrimSpace(tools) != "" {
		g.tools = tools
		g.reviewerBinding.Tools = tools
	}
}

// SetReviewerBinding stores the full resolved [reviewer] binding as the single
// resolution source for Invoke (sty_ba860c8a). Scalar caches are synced from it.
func (g *Engine) SetReviewerBinding(b config.AgentBinding) {
	g.reviewerBinding = b
	if strings.TrimSpace(b.Tools) != "" {
		g.tools = b.Tools
	}
	if strings.TrimSpace(b.Model) != "" {
		g.model = b.Model
	}
	if len(b.Env) > 0 {
		g.reviewerEnv = b.Env
	}
	g.injectPrinciples = b.InjectsPrinciples()
}

// SetConstitution sets the project constitution body injected order-zero into
// isolated agent briefings whenever principles ≠ none (design §5.3).
func (g *Engine) SetConstitution(body string) { g.constitution = strings.TrimSpace(body) }

// SetChildrenResolver wires the resolver that lists a container's member stories
// (id + status) so a container close gate judges the children-resolved rule from
// the payload satelle builds — not an on-disk story mirror. The list is a
// SNAPSHOT taken when the reviewer is prepared: a hint to the reviewer, never the
// close check, which re-reads the set at the commit (sty_9f4f8e12). The same
// snapshot rides the reviewer, retrospective, chat and performer payloads (the
// named-executor dispatch and the cloud prompt, via performerPayload), so a
// performer without a shell is judged on the real children. Nil-safe: an
// unwired resolver simply injects no children.
func (g *Engine) SetChildrenResolver(fn func(ctx context.Context, item workitem.Item) []ChildState) {
	g.children = fn
}

// SetTrackingStoryResolver wires the lookup from a failing document to the open
// story that diagnoses it (sty_88d40a60). Pass nil to disable — a refusal then
// carries no story id, which is the correct silence for a repo with none.
func (g *Engine) SetTrackingStoryResolver(fn func(ctx context.Context, kind, name string) string) {
	g.trackingStory = fn
}

// trackingStoryFor is the nil-safe read of that resolver.
func (g *Engine) trackingStoryFor(ctx context.Context, kind, name string) string {
	if g.trackingStory == nil {
		return ""
	}
	return g.trackingStory(ctx, kind, name)
}

// SetDocsResolver wires the resolver that lists an item's attachments so every
// isolated agent (reviewer, named executor, retrospective) receives plan/step
// summaries in the transition payload — the Bash-less read channel (sty_58fa970e).
// Nil-safe: an unwired resolver injects no docs.
func (g *Engine) SetDocsResolver(fn func(ctx context.Context, itemID string) []DocState) {
	g.itemDocs = fn
}

// SetPriorVerdictsResolver wires the resolver that lists the verdicts already
// recorded for an item on the edge under review, oldest first — the enumeration
// a re-reviewer needs to judge what CHANGED rather than re-deriving a verdict
// from scratch (sty_0f5e600c). Enumeration, not verdict: the binary attaches the
// rows, the reviewer skill decides what to do with them. Nil-safe: an unwired
// resolver injects no prior verdicts.
func (g *Engine) SetPriorVerdictsResolver(fn func(ctx context.Context, itemID, from, to string, ex verb.StaleExemption) []PriorVerdict) {
	g.priorVerdicts = fn
}

// SetDefinitionEditsResolver wires the resolver that lists the definition edits
// recorded for an item, oldest first (sty_5262592e). Enumeration, not verdict:
// the binary attaches the before/after rows, the intent reviewer decides whether
// an edit weakened an acceptance criterion. Nil-safe: an unwired resolver
// injects no edits.
func (g *Engine) SetDefinitionEditsResolver(fn func(ctx context.Context, itemID string) []DefinitionEdit) {
	g.definitionEdits = fn
}

// SetDiffResolver wires the resolver that enumerates the engagement slice for
// a reviewer payload (sty_a125b440). Enumeration, not verdict: the binary
// attaches the facts, the skill decides. Nil-safe: an unwired resolver injects
// no Diff. The callback must not fail the transition — return a no-baseline
// marker on any error.
func (g *Engine) SetDiffResolver(fn func(ctx context.Context, itemID string) *DiffState) {
	g.itemDiff = fn
}

// SetCitationTreeResolver wires the resolver for the working tree a gate judges,
// which a plan review's file:line citations are read from. Unlike the diff it
// needs no engagement baseline. Nil-safe: unwired, every citation is listed as
// unresolved with the reason.
func (g *Engine) SetCitationTreeResolver(fn func(ctx context.Context, itemID string) (string, error)) {
	g.citationTree = fn
}

// SetMessagesResolver wires the resolver that lists directed agent messages
// for a dispatch payload (sty_2db624d0). addresses is the engine-built set
// (binding name + protocol role + implicit *); the resolver matches `to` in
// that set or `to=="*"`. Nil-safe. Never fail a transition.
func (g *Engine) SetMessagesResolver(fn func(ctx context.Context, itemID string, addresses []string) []MessageState) {
	g.itemMessages = fn
}

// SetMessagesDelivered wires the writer of delivered marks (sty_73c9aa10) a
// performer's dispatch calls with the ids of the role messages its payload
// carries. Nil (unwired) injects messages exactly as before and marks nothing.
func (g *Engine) SetMessagesDelivered(fn func(ctx context.Context, itemID string, msgIDs []string) error) {
	g.messagesDelivered = fn
}

// SetDiffCompressor wires the ranked diff compressor (sty_918e2086) fillDiff
// runs on a gate payload's patch before diffPayloadCeiling is applied. Nil
// (unwired) leaves fillDiff's patch handling exactly as it was before this
// story — the 64KiB ceiling is the sole reducer.
func (g *Engine) SetDiffCompressor(fn func(ctx context.Context, itemID, patch string) string) {
	g.diffCompressor = fn
}

// SetDiffOffloader wires the store fillDiff's excerptOffload backstop uses to
// keep an oversized patch's overflow tail recoverable behind a marker
// (sty_918e2086), independent of SetDiffCompressor. Nil keeps the backstop's
// prior plain "… [truncated]" suffix.
func (g *Engine) SetDiffOffloader(fn func(ctx context.Context, itemID string, content []byte) (hash string, err error)) {
	g.diffOffload = fn
}

// SetCheckLogCompressor wires the log compressor (sty_ef930f81) runCheck uses
// to build a failing check's reject notes. Nil falls back to
// compact.CompressLog(out, compact.LogConfig{}, nil) — bounded head/tail
// notes with no retrieval marker.
func (g *Engine) SetCheckLogCompressor(fn func(ctx context.Context, itemID, log string) string) {
	g.checkLogCompressor = fn
}

// SetSessionModelsResolver wires the resolver that returns the latest
// captured model per role (in-loop, creator) for a story (sty_7069bced).
// Nil-safe: an unwired resolver leaves every role unknown, so selectModel
// falls through those tiers to [model_order] / cli-default.
func (g *Engine) SetSessionModelsResolver(fn func(ctx context.Context, storyID string) (inLoop, creator config.SessionModel)) {
	g.sessionModels = fn
}

// SetModelOrder wires the lookup of an executable's [model_order] list
// (config.AgentsConfig.OrderFor, sty_4fde0a50). Nil-safe: unwired, selectModel
// has no order tier and falls through to cli-default.
func (g *Engine) SetModelOrder(fn func(executable string) []config.ModelRank) {
	g.modelOrder = fn
}

// SetInvocationRecorder wires the seam a live session's open/close uses to
// ledger its own agent_invocation rows directly (sty_7069bced 4.2), bypassing
// the transition-gated appendLedgerEntry path a one-shot dispatch runs
// through. Nil-safe: an unwired recorder writes no rows.
func (g *Engine) SetInvocationRecorder(fn func(ctx context.Context, itemID string, payload map[string]any) error) {
	g.invocationRecorder = fn
}

// SetSuiteEvidence wires the seam a check_reuse: tree gate records its runs
// through and finds a reusable one by (sty_dc554488). Nil-safe: unwired, every
// check runs each time.
func (g *Engine) SetSuiteEvidence(
	record func(ctx context.Context, storyID string, ev verb.SuiteEvidence) (string, error),
	lookup func(ctx context.Context, storyID, check, commandSHA, tree string) (verb.SuiteEvidence, bool),
) {
	g.suiteRecord, g.suiteLookup = record, lookup
}

// recordInvocation calls the wired invocation recorder, if any. Best-effort:
// a write failure never blocks the live session it is describing.
func (g *Engine) recordInvocation(ctx context.Context, itemID string, payload map[string]any) {
	if g.invocationRecorder == nil {
		return
	}
	_ = g.invocationRecorder(ctx, itemID, payload)
}

// ModelChoice is one dispatch's already-resolved model selection
// (sty_7069bced 4.1) — the result of a single selectModel call, carried
// separately from config.AgentBinding so a resolved value is never mistaken
// for an explicit agents.toml model= on a later re-selection (an attempt
// loop's repair phase reuses it as-is rather than re-resolving against an
// already-mutated binding).
type ModelChoice struct {
	Model  string
	Source string
}

// selectModel resolves the model for one dispatch (sty_7069bced): binding
// alias first, then a per-dispatch override (a workflow step's model= or a
// dispatching agent's --model flag, named by overrideSource), then the
// in-loop session's model for storyID, then
// the story-creating session's, then the first entry of the executable's
// [model_order] list, else empty (cli-default). It never mutates
// binding — callers apply the result to their own per-dispatch copy.
func (g *Engine) selectModel(ctx context.Context, binding config.AgentBinding, storyID, override, overrideSource string) (model, source string) {
	in := config.SelectInput{
		Binding:           binding.Model,
		DispatchOverride:  override,
		DispatchSource:    overrideSource,
		CommandExecutable: binding.ExecutableToken(),
		HasModelSlot:      config.HasModelSlot(binding.CommandTemplate()),
		ModelViaSession:   binding.IsACP(),
	}
	if g.modelOrder != nil {
		in.Order = g.modelOrder(in.CommandExecutable)
	}
	if g.sessionModels != nil && strings.TrimSpace(storyID) != "" {
		in.InLoop, in.Creator = g.sessionModels(ctx, storyID)
	}
	return config.SelectModel(in)
}

// docsPayloadCeiling bounds how many attachment body bytes ride in one payload
// so a long-lived story with many step summaries does not blow the prompt.
const docsPayloadCeiling = 128 << 10

// priorVerdictCount bounds how many of an edge's verdicts ride in one payload,
// and priorVerdictNotesCeiling bounds each entry's notes — together an upper
// bound of ~10 KiB, deliberately independent of docsPayloadCeiling so a
// paragraph-long verdict can never starve the plan the reviewer must judge.
const (
	priorVerdictCount        = 5
	priorVerdictNotesCeiling = 2 << 10
	// reviewedCeiling is the size bound on the quotation stored with a verdict.
	// Over it, the row records that the citation was truncated and omits the
	// string. It is not a hash and not a comparison of the story.
	reviewedCeiling = 8 << 10
	// maxCriteria caps the per-criterion entries kept from one verdict
	// (sty_99d3162b), so a runaway reviewer cannot grow a ledger row.
	maxCriteria = 64
)

// diffPayloadCeiling bounds the patch body that rides in one reviewer payload,
// and diffFilesCount bounds the files list — together deliberately independent
// of docsPayloadCeiling so a large slice cannot starve the plan the reviewer
// must judge (sty_a125b440). Stat is capped too (small, but bounded).
const (
	diffPayloadCeiling = 64 << 10
	diffFilesCount     = 500
	diffStatCeiling    = 4 << 10
)

// SetReviewerModel sets the reviewer's model from the agents layer (the resolved
// `reviewer` binding's `model`). It rides as `--model` to every isolated reviewer
// this Engine runs, so a repo can review on a different model (e.g. sonnet) without
// touching the executor. An empty value is ignored, keeping the agent CLI's
// default model (no `--model` flag emitted). Also mutates reviewerBinding.
func (g *Engine) SetReviewerModel(model string) {
	if strings.TrimSpace(model) != "" {
		g.model = model
		g.reviewerBinding.Model = model
	}
}

// SetReviewerEnv sets the reviewer binding's resolved env (config.ResolveAgentEnvs),
// applied to every isolated reviewer AND step-summariser subprocess this Engine
// runs — so a repo can point its reviews at an alternate model backend the same
// way a named executor does (sty_001558ce). Absent/empty leaves the child env at
// the inherited process env. Also mutates reviewerBinding.
func (g *Engine) SetReviewerEnv(env map[string]string) {
	g.reviewerEnv = env
	g.reviewerBinding.Env = env
}

// SetInjectPrinciples sets whether the resident principles ride in an isolated
// reviewer's system prompt, from the agents layer's resolved `reviewer` binding
// (sty_46a40208). Defaults ON; a repo disables it with inject_principles = false.
// Also mutates reviewerBinding (deprecated alias + principles selector).
func (g *Engine) SetInjectPrinciples(on bool) {
	g.injectPrinciples = on
	v := on
	g.reviewerBinding.InjectPrinciples = &v
	if on {
		g.reviewerBinding.Principles = config.PrinciplesSession
	} else {
		g.reviewerBinding.Principles = config.PrinciplesNone
	}
}

// SetRunner overrides the reviewer's agent-CLI runner — the agents layer's
// `reviewer` harness binding, resolved to a Runner. A nil runner is ignored,
// keeping the default configured at construction (the global `[agent] cli`).
func (g *Engine) SetRunner(r agentcli.Runner) {
	if r != nil {
		g.runner = r
	}
}

// gateStoryKey / gateInputsKey carry the id of the item a functional check
// judges, and that item's input values, from the gate to execCheck without
// widening the check seam every test double shares.
type gateStoryKey struct{}
type gateInputsKey struct{}

// withGateItem marks ctx as judging item, so execCheck exports its id to the
// check as verb.GateStoryEnv (sty_ab93f9a6) and its input values as
// SATELLE_INPUT_<NAME> variables.
func withGateItem(ctx context.Context, item workitem.Item) context.Context {
	if len(item.InputValues) > 0 {
		ctx = context.WithValue(ctx, gateInputsKey{}, item.InputValues)
	}
	if strings.TrimSpace(item.ID) == "" {
		return ctx
	}
	return context.WithValue(ctx, gateStoryKey{}, item.ID)
}

// gateStoryFrom reports the story withGateItem marked ctx with, or "".
func gateStoryFrom(ctx context.Context) string {
	id, _ := ctx.Value(gateStoryKey{}).(string)
	return id
}

// gateInputEnvFrom reports the SATELLE_INPUT_ variables withGateItem marked ctx with.
func gateInputEnvFrom(ctx context.Context) map[string]string {
	v, _ := ctx.Value(gateInputsKey{}).(map[string]any)
	return workitem.InputEnv(v)
}

// execCheck runs command via `bash -c` in dir, returning combined stdout+stderr.
// bash (not sh) so a multi-line self-contained check embedded in a skill may use
// ordinary shell scripting. dir is the MAIN tree (the process of record); a story
// engaged from a linked worktree is reached through verb.GateStoryEnv, which names
// the story being judged so `satelle story diff` resolves that story's own tree
// instead of refusing the main tree (sty_ab93f9a6).
func execCheck(ctx context.Context, dir, command, payload string) (string, error) {
	c := exec.CommandContext(ctx, "bash", "-c", command)
	c.Dir = dir
	if id := gateStoryFrom(ctx); id != "" {
		c.Env = append(os.Environ(), verb.GateStoryEnv+"="+id)
	}
	if in := gateInputEnvFrom(ctx); len(in) > 0 {
		if c.Env == nil {
			c.Env = os.Environ()
		}
		for k, v := range in {
			c.Env = append(c.Env, k+"="+v)
		}
	}
	c.Stdin = strings.NewReader(payload)
	out, err := c.CombinedOutput()
	return string(out), err
}

// transitionPayload is the JSON delivered to the reviewer on stdin.
type transitionPayload struct {
	Story       workitem.Item `json:"story"`
	From        string        `json:"from"`
	To          string        `json:"to"`
	ReviewSkill string        `json:"review_skill"`
	// ReviewSkills names every rubric a BUNDLED session judges over this one
	// payload (sty_23e10d92); ReviewSkill is empty then. Absent for a gate that
	// runs alone, so its payload is byte-for-byte unchanged.
	ReviewSkills []string `json:"review_skills,omitempty"`
	// ReviewMode names the gate's declared review mode, and is present ONLY for
	// fail-fast (sty_93359e0f): the seat stops at the first blocking finding,
	// rejects, and says in its verdict where it stopped. Absent for the default
	// full review, so that payload is byte-for-byte unchanged.
	ReviewMode string `json:"review_mode,omitempty"`
	// Children carries a container's child stories (id + status) so a parent/epic
	// close gate judges the children-resolved rule from the PAYLOAD — satelle does
	// the context selection — rather than reading any on-disk story mirror. Empty
	// for a non-container or when no resolver is wired.
	Children []ChildState `json:"children,omitempty"`
	// Docs carries the item's attachments (plan, step summaries, …) so a
	// Bash-less reviewer can judge without reading any disk path (sty_58fa970e).
	// Bodies may be Truncated when the cumulative budget is spent.
	Docs []DocState `json:"docs,omitempty"`
	// PlanAttached says whether the document the dispatched step consumes (its
	// apply_criteria doc, normally the plan) rode this payload. It is set, to true
	// or false, only on a performer payload for a step that declares such a
	// document; nil (absent) otherwise, so every other payload is byte-for-byte
	// unchanged. Enumeration, not verdict.
	PlanAttached *bool `json:"plan_attached,omitempty"`
	// RouteDrift is present ONLY when the story's walked lane no longer matches
	// the lane its category derives (sty_6e4f7fd8). Absent on every ordinary
	// transition, so a reviewer that ignores it costs nothing and a gate that
	// judges drift has the enumeration without shelling for it. Enumeration, not
	// verdict — the binary attaches it and decides nothing.
	RouteDrift *wfgovern.RouteDrift `json:"route_drift,omitempty"`
	// PriorVerdicts carries the verdicts already recorded for this story on THIS
	// from→to edge, oldest first (sty_0f5e600c) — so a re-review judges the delta
	// against what it already settled instead of returning a fresh, disjoint
	// finding set each pass. Absent on a first attempt, so a reviewer that ignores
	// it costs nothing. Enumeration, not verdict.
	PriorVerdicts []PriorVerdict `json:"prior_verdicts,omitempty"`
	// DefinitionEdits carries the definition edits recorded for this story while
	// its definition was still editable (sty_5262592e): field, old and new value,
	// actor, oldest first, windowed to the most recent definitionEditCount. Absent
	// when nothing was edited, so a reviewer that ignores it costs nothing.
	// Enumeration, not verdict.
	DefinitionEdits []DefinitionEdit `json:"definition_edits,omitempty"`
	// Amendment is present ONLY on the amend_review gate's payload
	// (sty_81aa4d8f): the before/after of every definition field the amendment
	// proposes, plus the caller's reason. Without it the reviewer would see only
	// the amended story and could not tell a correction from a weakening — the
	// one judgement this gate exists to make. Enumeration, not verdict.
	Amendment *AmendmentState `json:"amendment,omitempty"`
	// Diff is the live engagement slice (files, stat, patch) when a baseline
	// exists (sty_a125b440). Enumeration, not verdict — the binary attaches it
	// and decides nothing. Absent when the resolver is unwired. A no-baseline
	// marker (NoBaseline=true, no patch) is used when the resolver ran and
	// found no baseline, so a skill's fast-accept branch is unambiguous.
	Diff *DiffState `json:"diff,omitempty"`
	// Messages is the engagement-windowed agent_message list for this
	// recipient (sty_2db624d0). Enumeration, not verdict — context, never a
	// verdict input a reviewer must obey. Absent when none qualify or the
	// resolver is unwired (omitempty matches prior_verdicts).
	Messages []MessageState `json:"messages,omitempty"`
	// MeasuredActual is the read-only computed actual (verb.ComputeStoryActual)
	// for the story under review — attached to EVERY gate payload generically,
	// never conditioned on the gate's skill name (sty_8eae81ac): mechanism, not
	// a skill literal in Go. A shipped or repo-authored gate can compare a
	// self-reported estimate tag against it in its own unit. Enumeration, not a
	// verdict — the binary computes it and decides nothing. Nil when the
	// computation fails (unwired store, missing item) — never a fabricated zero.
	MeasuredActual *verb.StoryActual `json:"measured_actual,omitempty"`
	// Rework is present ONLY on the rework relay's consult-seat seed: the coder
	// binding, its authored tool grant, and the later gates that own evidence the
	// coder cannot produce (ConsultSeed). Absent everywhere else.
	Rework *ReworkContext `json:"rework,omitempty"`
	// Deferred carries the objections a rework relay deferred to THIS gate's
	// skill — evidence the gate owns, not a defect the coder could clear. Absent
	// when none, so every other gate's payload is unchanged. Enumeration, not
	// verdict.
	Deferred []DeferredItem `json:"deferred,omitempty"`
	// Waivers carries the developer waivers recorded for THIS story against THIS
	// gate's skill (sty_bef97244). Absent when none, so every other gate's payload
	// is unchanged. Enumeration, not verdict: whether to honour one is the rubric's.
	Waivers []WaiverItem `json:"waivers,omitempty"`
	// Citations carries the file:line citations of a document the gate's rubric
	// names (frontmatter payload_citations), resolved to the lines they name in
	// the judged tree. Absent for every gate whose rubric does not opt in, so
	// its payload is unchanged. Enumeration, not verdict.
	Citations *CitationsState `json:"citations,omitempty"`
	// CriteriaSource names where the criteria this gate judges come from — the
	// performing step's document section ("doc:plan#Acceptance criteria") or
	// "story" — so a reviewer numbers its criteria[] entries by the right lines
	// (sty_99d3162b). Absent when there are no numbered criteria.
	CriteriaSource string `json:"criteria_source,omitempty"`
	// CriteriaState marks each judged criterion settled or open for this seat on a
	// re-presentation. Absent unless the edge carries or an earlier verdict
	// recorded criteria, so an ordinary gate's payload is unchanged.
	CriteriaState []criterionState `json:"criteria_state,omitempty"`
	// PeerFindings are the other panel seats' open findings (never the seat's
	// own). Absent outside a panel and when no peer has one.
	PeerFindings []peerFinding `json:"peer_findings,omitempty"`
	// Findings is present ONLY on the payload of a performer re-presenting an edge
	// whose latest gate round rejected (sty_f948d345): each gate seat's latest
	// verdict on that edge, a panel's seats individually. The performer answers
	// each blocking one — adopted, or rebutted with evidence — in its output.
	// Enumeration, not verdict.
	Findings []Finding `json:"findings_to_answer,omitempty"`
	// FindingsEdgeTo is the exit Findings were taken from when the performer runs
	// at the step the story is already in: the exit gate that rejected it. Empty
	// on a re-presented edge, whose own findings they are.
	FindingsEdgeTo string `json:"findings_edge_to,omitempty"`
	// FindingAnswers is present ONLY on a gate payload whose edge's performer was
	// offered findings: which it answered and which it left unanswered. A fact the
	// gate's rubric weighs; Go never rejects on it.
	FindingAnswers *FindingAnswers `json:"finding_answers,omitempty"`
	// docBodies is every judgeable document with its full body, set on a
	// performer payload only. A local performer is sent these as paths to
	// scratch copies instead of the bodies in Docs (sty_73a2dc68); never
	// marshalled, so the inline and cloud payloads are unchanged.
	docBodies []DocState
}

// MessageState is one agent_message as injected into a transition payload.
type MessageState struct {
	ID            string `json:"id"`
	From          string `json:"from"`
	To            string `json:"to"`
	Body          string `json:"body"`
	CreatedAt     string `json:"created_at"`
	EngagementSHA string `json:"engagement_sha"`
	// Truncated marks a body cut at messageBodyBudget; the body then ends with
	// the command that fetches the whole of it.
	Truncated bool `json:"truncated,omitempty"`
	// State is the message's board state when the resolver read it. It is not
	// part of the payload an agent sees; it lets the engine mark delivered only
	// the messages still waiting.
	State string `json:"-"`
}

// DiffState is the engagement-slice enumeration handed to a reviewer. JSON
// tags match `satelle story diff` so a skill that already knows that shape
// reads the payload unchanged.
type DiffState struct {
	Baseline   string   `json:"baseline_sha,omitempty"`
	Dirty      bool     `json:"baseline_dirty,omitempty"`
	Files      []string `json:"files,omitempty"`
	Stat       string   `json:"stat,omitempty"`
	Patch      string   `json:"patch,omitempty"`
	Truncated  bool     `json:"truncated,omitempty"`
	NoBaseline bool     `json:"no_baseline,omitempty"`
	Note       string   `json:"note,omitempty"`
	Source     string   `json:"source,omitempty"`
}

// AmendmentState is the proposed amendment handed to the amend gate: the state
// the story sits in, the reason given, and each field's old and new value.
type AmendmentState struct {
	Status string            `json:"status"`
	Reason string            `json:"reason"`
	Fields []verb.AmendField `json:"fields"`
}

// PriorVerdict is one verdict already recorded on the edge under review.
// Attempt is 1-based over the edge's FULL history (not the injected window), so
// a reviewer seeing attempt 7 learns the edge is deep even when the window
// starts at 3.
type PriorVerdict struct {
	Skill             string `json:"skill,omitempty"`
	Decision          string `json:"decision"` // "accept" | "reject"
	Notes             string `json:"notes,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"` // RFC3339
	Attempt           int    `json:"attempt"`
	Reviewed          string `json:"reviewed,omitempty"`
	ReviewedTruncated bool   `json:"reviewed_truncated,omitempty"`
	// Criteria is the verdict's per-criterion entries (sty_99d3162b): what the
	// seat said of each criterion, its text then and the quotation it rested on.
	Criteria []verb.CriterionVerdict `json:"criteria,omitempty"`
	// ReviewedStale/EvidenceSince: ledger evidence was recorded after this
	// verdict's cycle ended, so Reviewed is withheld and the gate re-judges.
	ReviewedStale bool                 `json:"reviewed_stale,omitempty"`
	EvidenceSince []verb.EvidenceSince `json:"evidence_since,omitempty"`
	// ReviewMode / Coverage / Partial: what the earlier verdict covered
	// (sty_93359e0f). Partial means the earlier verdict stopped early, so the
	// remainder it did not review is still unjudged.
	ReviewMode string         `json:"review_mode,omitempty"`
	Coverage   *verb.Coverage `json:"coverage,omitempty"`
	Partial    bool           `json:"partial,omitempty"`
	// Unavailable marks a panel seat's own entry for a round it never produced a
	// verdict on (sty_da9e8d0a). It is a reject with no Reviewed, judged in full.
	Unavailable bool `json:"unavailable,omitempty"`
	// Seats is the panel fold of the ledger row this came from. It feeds
	// seatPriorVerdicts and is never serialised: a seat sees its own entry, not
	// the combined row.
	Seats []verb.PanelSeat `json:"-"`
}

// PriorVerdictFrom is the one conversion from a ledger prior verdict to the
// payload record. Attempt stays zero; fillPriorVerdicts assigns it.
func PriorVerdictFrom(v verb.PriorVerdict) PriorVerdict {
	return PriorVerdict{
		Skill:             v.Skill,
		Decision:          v.Decision,
		Notes:             v.Notes,
		CreatedAt:         v.CreatedAt,
		Reviewed:          v.Reviewed,
		ReviewedTruncated: v.ReviewedTruncated,
		Criteria:          v.Criteria,
		ReviewedStale:     v.ReviewedStale,
		EvidenceSince:     v.EvidenceSince,
		ReviewMode:        v.ReviewMode,
		Coverage:          v.Coverage,
		Partial:           v.Partial,
		Seats:             v.Seats,
	}
}

// ChildState is one child story's id and status, injected into a parent/epic
// close payload.
type ChildState struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// DocState is one story attachment injected into the transition payload.
// Binary attachments carry Binary=true with empty Body and must never consume
// docsPayloadCeiling (sty_40e5a305); fillPayloadDocs skips them entirely.
type DocState struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Body        string `json:"body,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Binary      bool   `json:"binary,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	// Path is the persistent file the body was read from. It never rides a
	// payload; the dispatch log writes it in place of the body (sty_2b89feab).
	Path string `json:"-"`
}

// judgeableDoc reports whether an attachment rides the material a gate judges:
// every document but the three kinds below. fillPayloadDocs and the
// answer_progress material (progressMaterial) both read it.
func judgeableDoc(d DocState) bool {
	// Binary attachments are disk retention only (sty_40e5a305): a screenshot
	// larger than the ceiling would starve plan/step-summary. Never inline
	// bodies or count them against the budget.
	// Pull via satelle story doc <id> <name> --out <path>.
	if d.Binary {
		return false
	}
	// type:change patches are disk retention for on-demand review; they must
	// not ride the cumulative payload ceiling or starve plan/step-summary
	// (sty_948ad5df). Pull via satelle story doc / story diff --recorded.
	if strings.EqualFold(d.Type, "change") {
		return false
	}
	// A configuration bundle (internal/sessionconfig) is the whole substrate
	// a past run used: a record for `satelle story session`, not a document
	// to judge, and it would starve the plan of the payload budget.
	if strings.EqualFold(d.Type, sessionconfig.DocType) {
		return false
	}
	// The route document is the OPERATOR's artifact (sty_39e2d9df): it grows by
	// one block per step, and injecting it into the gate that is about to write
	// the NEXT block is both circular and quadratic in tokens. A reviewer already
	// receives the plan and the step summaries. Pull via satelle story route.
	return !strings.EqualFold(d.Type, verb.RouteDocName)
}

// fillPayloadDocs attaches resolved docs under the cumulative body budget.
// After the binary, type:change, and route skips, every remaining doc is kept
// with its full body on mat. The ceiling applies only to tp.Docs, which is
// the functional-check payload.
func (g *Engine) fillPayloadDocs(ctx context.Context, itemID string, tp *transitionPayload, mat *reviewerMaterial) {
	if g.itemDocs == nil || itemID == "" {
		return
	}
	all := g.itemDocs(ctx, itemID)
	if len(all) == 0 {
		return
	}
	var used int
	out := make([]DocState, 0, len(all))
	var retained []DocState
	for _, d := range all {
		if !judgeableDoc(d) {
			continue
		}
		retained = append(retained, d)
		if used >= docsPayloadCeiling {
			out = append(out, DocState{Name: d.Name, Type: d.Type, Truncated: true})
			continue
		}
		body := d.Body
		if used+len(body) > docsPayloadCeiling {
			// Prefer shipping a truncated marker over a partial body that could
			// mislead a judge into thinking the document is complete.
			out = append(out, DocState{Name: d.Name, Type: d.Type, Truncated: true})
			used = docsPayloadCeiling
			continue
		}
		out = append(out, d)
		used += len(body)
	}
	if mat != nil {
		mat.Docs = retained
	}
	tp.Docs = out
}

// fillPriorVerdicts attaches the edge's earlier verdicts, numbered from 1 over
// the FULL resolved history, then windowed to the most recent
// priorVerdictCount with each note capped. The two budgets stay separate on
// purpose: this runs AFTER fillPayloadDocs and never touches its counter, so
// prior verdicts provably cannot consume docsPayloadCeiling.
func (g *Engine) fillPriorVerdicts(ctx context.Context, itemID, from, to string, ex verb.StaleExemption, tp *transitionPayload, mat *reviewerMaterial) {
	if g.priorVerdicts == nil || itemID == "" {
		return
	}
	all := g.priorVerdicts(ctx, itemID, from, to, ex)
	if len(all) == 0 {
		return
	}
	numberAttempts(all)
	// Snapshot before the window. The copy does not share the backing array,
	// so the note excerpts below cannot shorten the retained bodies.
	if mat != nil {
		mat.PriorVerdicts = append([]PriorVerdict(nil), all...)
	}
	tp.PriorVerdicts = windowPriorVerdicts(all)
}

// windowPriorVerdicts is the most recent priorVerdictCount of all with each
// note capped — what rides the payload inline. all is numbered over the full
// history, so a window still reports how deep the edge is. A panel seat's own
// priors take the same window.
func windowPriorVerdicts(all []PriorVerdict) []PriorVerdict {
	window := all
	if len(window) > priorVerdictCount {
		window = window[len(window)-priorVerdictCount:]
	}
	out := make([]PriorVerdict, 0, len(window))
	for _, v := range window {
		v.Notes = excerpt(v.Notes, priorVerdictNotesCeiling)
		// Reviewed is the reviewer's quotation. It is copied whole onto this
		// window and onto the material snapshot. It is not excerpted:
		// a shortened quotation is not a citation.
		out = append(out, v)
	}
	return out
}

// DefinitionEdit is one recorded edit to a story's definition while it was still
// editable: which field, its value before and after, and who edited it.
type DefinitionEdit struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
	Actor string `json:"actor,omitempty"`
	At    string `json:"at,omitempty"` // RFC3339
}

// definitionEditCount bounds how many edits ride in one payload and
// definitionEditValueCeiling bounds each before/after value; together an upper
// bound on the block, separate from the docs, prior-verdict and diff budgets.
const (
	definitionEditCount        = 20
	definitionEditValueCeiling = 4 << 10
)

// fillDefinitionEdits attaches the story's definition edits, most recent
// definitionEditCount, each value capped. Gate payload only, like prior verdicts:
// the reader is a reviewer judging the definition, not a performer.
func (g *Engine) fillDefinitionEdits(ctx context.Context, itemID string, tp *transitionPayload, mat *reviewerMaterial) {
	if g.definitionEdits == nil || itemID == "" {
		return
	}
	all := g.definitionEdits(ctx, itemID)
	if len(all) == 0 {
		return
	}
	if mat != nil {
		mat.DefinitionEdits = append([]DefinitionEdit(nil), all...)
	}
	window := all
	if len(window) > definitionEditCount {
		window = window[len(window)-definitionEditCount:]
	}
	out := make([]DefinitionEdit, 0, len(window))
	for _, e := range window {
		e.Old = excerpt(e.Old, definitionEditValueCeiling)
		e.New = excerpt(e.New, definitionEditValueCeiling)
		out = append(out, e)
	}
	tp.DefinitionEdits = out
}

// fillDiff attaches the engagement slice under its own ceiling. Runs AFTER
// fillPayloadDocs and never touches that counter. Called unconditionally on
// the gate payload — no status-name switch (sty_a125b440 AC2). Gate payload
// only: executor and retrospective payloads deliberately go without the diff (an
// executor modelling its own slice is the failure mode). They go without the
// full prior_verdicts history too; a performer re-presenting an edge gets only
// each seat's latest verdict, as findings_to_answer (sty_f948d345).
func (g *Engine) fillDiff(ctx context.Context, itemID string, tp *transitionPayload, mat *reviewerMaterial) {
	if g.itemDiff == nil || itemID == "" {
		return
	}
	d := g.itemDiff(ctx, itemID)
	if d == nil {
		return
	}
	cp := *d
	if d.Files != nil {
		cp.Files = append([]string(nil), d.Files...)
	}
	// The compressor runs once, before the file, stat, and patch ceilings.
	// The retained copy is that post-compressor slice. The ceilings still
	// apply to tp.Diff, which is the functional-check payload.
	if g.diffCompressor != nil && cp.Patch != "" {
		compressed := g.diffCompressor(ctx, itemID, cp.Patch)
		if retrieve.MarkerRE.MatchString(compressed) {
			cp.Truncated = true
		}
		cp.Patch = compressed
	}
	if mat != nil {
		snap := cp
		if cp.Files != nil {
			snap.Files = append([]string(nil), cp.Files...)
		}
		mat.Diff = &snap
	}
	if len(cp.Files) > diffFilesCount {
		cp.Files = append([]string(nil), cp.Files[:diffFilesCount]...)
		cp.Truncated = true
	}
	if len(cp.Stat) > diffStatCeiling {
		cp.Stat = excerpt(cp.Stat, diffStatCeiling)
		cp.Truncated = true
	}
	// diffPayloadCeiling stays only as a backstop on the check payload
	// (sty_918e2086). The judging path keeps the post-compressor text.
	if len(cp.Patch) > diffPayloadCeiling {
		cp.Patch = g.excerptOffload(ctx, itemID, cp.Patch, diffPayloadCeiling)
		cp.Truncated = true
	}
	if cp.NoBaseline {
		cp.Files = nil
		cp.Stat = ""
		cp.Patch = ""
	}
	tp.Diff = &cp
}

// fillMeasuredActual attaches the read-only computed actual (verb.
// ComputeStoryActual) to the gate payload, generically for every gate —
// mechanism, never conditioned on the skill under way (sty_8eae81ac
// architecture note: the measured_actual block must not depend on the
// review_skill name, or it becomes a skill literal in Go). Best-effort: a
// computation failure (unwired store, missing item) leaves MeasuredActual nil
// rather than failing the gate.
func (g *Engine) fillMeasuredActual(ctx context.Context, itemID string, tp *transitionPayload) {
	if itemID == "" {
		return
	}
	actual, err := verb.ComputeStoryActual(ctx, itemID)
	if err != nil {
		return
	}
	tp.MeasuredActual = &actual
}

const (
	messagesCount = 20
	// messageBodyBudget is sized so a consolidated decision message reaches the
	// performer whole; only a longer body is cut and marked Truncated.
	messageBodyBudget = 8 << 10
)

// performerPayload builds the payload a step's performer is dispatched with —
// the named-executor dispatch and the cloud prompt both go through it, so the
// two cannot drift. It carries the story, its attached documents, the container's
// children (when a resolver is wired and the item has any) and the messages
// addressed to the agent, "executor" and its resolved role. Provider-neutral: the
// children ride the payload, so a performer with read-only tools needs no shell.
//
// consumes names the document the step consumes (its apply_criteria doc); empty
// when it declares none. When set, PlanAttached reports whether that document
// rode the payload, and the document itself is returned (full body, before any
// ceiling trimming) for the caller to record as consumed; nil when absent.
func (g *Engine) performerPayload(ctx context.Context, item workitem.Item, toStatus, skill, agent string, binding config.AgentBinding, consumes string) (transitionPayload, *DocState) {
	tp := transitionPayload{Story: item, From: item.Status, To: toStatus, ReviewSkill: skill}
	var mat reviewerMaterial
	g.fillPayloadDocs(ctx, item.ID, &tp, &mat)
	tp.docBodies = mat.Docs
	var consumed *DocState
	if consumes != "" {
		for i := range mat.Docs {
			if mat.Docs[i].Name == consumes {
				consumed = &mat.Docs[i]
				break
			}
		}
		attached := consumed != nil
		tp.PlanAttached = &attached
	}
	g.fillFindings(ctx, item, toStatus, &tp)
	if g.children != nil {
		tp.Children = g.children(ctx, item)
	}
	addrs := []string{agent, "executor"}
	if role := config.ResolvedRole(agent, binding); role != "" {
		addrs = append(addrs, role)
	}
	g.fillMessages(ctx, item.ID, addrs, &tp, nil)
	g.markDelivered(ctx, item.ID, addrs, tp.Messages)
	return tp, consumed
}

// markDelivered records the delivery of the messages a performer's payload
// carries: each one still waiting and addressed to the performer's role (or to
// everyone) is marked delivered (sty_73c9aa10). Only the performer's own step
// does this — a reviewer's payload and a chat preview read messages without
// being the addressee receiving them. Best-effort: a mark that cannot be
// written is warned and dropped.
func (g *Engine) markDelivered(ctx context.Context, itemID string, addrs []string, msgs []MessageState) {
	if g.messagesDelivered == nil {
		return
	}
	want := map[string]bool{"*": true}
	for _, a := range addrs {
		if a = strings.TrimSpace(a); a != "" {
			want[a] = true
		}
	}
	var ids []string
	for _, m := range msgs {
		if m.ID != "" && m.State == string(board.Waiting) && want[m.To] {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	if err := g.messagesDelivered(ctx, itemID, ids); err != nil {
		g.warnf("satelle: warning: marking %d message(s) delivered on %s: %v", len(ids), itemID, err)
	}
}

// recordDocConsumed writes the plan-consumed ledger row for one dispatch attempt:
// the document the performer's payload carried, its content hash and the attempt.
// It runs immediately before the performer is invoked, so the record exists
// whether or not the performer says anything about the plan. source=dispatch marks
// it as the engine's own row (verb.IsEvidenceRow), distinct from a driver's
// `story log`. The hash carries a "sha256:" prefix: a bare 64-hex value is
// credential-shaped to the telemetry writer's secret check and would be replaced
// with a refusal marker.
func (g *Engine) recordDocConsumed(ctx context.Context, itemID, agent, step string, doc DocState, attempt int) {
	sum := doc.SHA256
	if sum == "" {
		raw := sha256.Sum256([]byte(doc.Body))
		sum = hex.EncodeToString(raw[:])
	}
	g.telemetryEvent(ctx, itemID, agent, verb.TelemetryPlanConsumed, map[string]any{
		"attachment":   doc.Name,
		"content_hash": "sha256:" + sum,
		"attempt":      attempt,
		"step":         step,
		"source":       verb.TelemetrySourceDispatch,
	})
}

// fillMessages attaches engagement-windowed agent messages for recipient.
// Runs AFTER fillPayloadDocs / fillDiff and never touches those counters.
// Newest 20 kept; each body cut at messageBodyBudget with a fetch hint.
// Nil-safe; never errors.
func (g *Engine) fillMessages(ctx context.Context, itemID string, addresses []string, tp *transitionPayload, mat *reviewerMaterial) {
	if g.itemMessages == nil || itemID == "" {
		return
	}
	msgs := g.itemMessages(ctx, itemID, addresses)
	if len(msgs) == 0 {
		return
	}
	// The file keeps resolver order and the full bodies. The check payload
	// stays the newest messagesCount (the resolver lists oldest first, so the tail
	// is the newest — a relayed rejection is the last thing said, and the one that
	// must not be dropped), each body cut at messageBodyBudget.
	if mat != nil {
		mat.Messages = append([]MessageState(nil), msgs...)
	}
	window := msgs
	if len(window) > messagesCount {
		window = window[len(window)-messagesCount:]
	}
	out := make([]MessageState, len(window))
	for i := range window {
		out[i] = budgetMessage(itemID, window[i])
	}
	tp.Messages = out
}

// budgetMessage cuts a message body longer than messageBodyBudget, sets
// Truncated, and names the command that fetches the whole body. A body within
// the budget is returned untouched.
func budgetMessage(itemID string, m MessageState) MessageState {
	if len(m.Body) <= messageBodyBudget {
		return m
	}
	fetch := "satelle story messages " + itemID
	if m.To != "" {
		fetch += " --to " + m.To
	}
	if m.CreatedAt != "" {
		fetch += " --since " + m.CreatedAt
	}
	m.Body = excerpt(m.Body, messageBodyBudget) + " — fetch the full body: " + fetch
	m.Truncated = true
	return m
}

// excerpt cuts s to at most limit bytes on a rune boundary, marking the cut so
// a reviewer never mistakes a truncated verdict for the whole of one.
func excerpt(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "… [truncated]"
}

// excerptOffload is fillDiff's ceiling backstop for the patch field
// (sty_918e2086): like excerpt, it cuts s to limit bytes on a rune boundary,
// but offloads the overflow tail behind a retrieve marker via g.diffOffload
// instead of dropping it — so even a patch the compressor left oversized (or
// one with no compressor wired at all) stays recoverable. g.diffOffload nil,
// or a failed Put, falls back to excerpt's plain "… [truncated]" suffix.
func (g *Engine) excerptOffload(ctx context.Context, itemID, s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	overflow := s[len(cut):]
	if g.diffOffload == nil {
		return cut + "… [truncated]"
	}
	hash, err := g.diffOffload(ctx, itemID, []byte(overflow))
	if err != nil {
		return cut + "… [truncated]"
	}
	return cut + "… [truncated " + retrieve.MarkerKind(hash, "patch", len(overflow)) + "]"
}

// alwaysPrinciples returns the bodies of the SESSION-resident (principles:session)
// principles — the SAME set the SessionStart injector gives the in-loop session
// (sty_46a40208), read via the SAME residency marker so the two never diverge —
// plus the seat-only (principles:seat) principles a cold-start seat needs and the
// in-loop session does not carry, frontmatter stripped and joined in a stable (name-sorted) order, so an isolated
// reviewer judges with the session guardrails the executor also sees. The
// operating principle (config.OperatingPrinciple) is guaranteed
// even when it is embedded-only on a fresh repo, via Get's embedded fallback that
// List lacks. Empty when none resolve; injection is additive and must never break
// a gate.
func (g *Engine) alwaysPrinciples(ctx context.Context) string {
	return sessionPrinciples(ctx, g.docs)
}

// sessionPrinciples is alwaysPrinciples over any doc source, shared with
// ResidentPrinciples.
func sessionPrinciples(ctx context.Context, docs DocGetter) string {
	return joinPrincipleBodies(sessionPrincipleDocs(ctx, docs))
}

// joinPrincipleBodies is the text a seat receives for a resolved principle set:
// each body without its frontmatter, blank ones dropped, a blank line between.
func joinPrincipleBodies(picked []docindex.Doc) string {
	var bodies []string
	for _, d := range picked {
		if body := strings.TrimSpace(stripFrontmatter(d.Body)); body != "" {
			bodies = append(bodies, body)
		}
	}
	return strings.Join(bodies, "\n\n")
}

// sessionPrincipleDocs is the session-resident principle documents, in the order
// a seat receives them.
func sessionPrincipleDocs(ctx context.Context, docs DocGetter) []docindex.Doc {
	seen := map[string]bool{}
	var picked []docindex.Doc
	add := func(d docindex.Doc) {
		if seen[d.Name] {
			return
		}
		seen[d.Name] = true
		picked = append(picked, d)
	}
	if listed, err := docs.List(ctx, "principles"); err == nil {
		sort.Slice(listed, func(i, j int) bool { return listed[i].Name < listed[j].Name })
		for _, d := range listed {
			if isSeatResident(d.Body) {
				add(d)
			}
		}
	}
	// Guarantee the operating principle even when it is embedded-only (not yet
	// materialised on disk) — Get carries the embedded fallback List does not.
	if !seen[config.OperatingPrinciple] {
		if d, err := docs.Get(ctx, "principles", config.OperatingPrinciple); err == nil {
			add(d)
		}
	}
	return picked
}

// hasSessionTag reports whether a doc's FRONTMATTER carries the principles:session
// residency marker — checked only within the leading `---`…`---` block so prose
// mentioning the tag never counts.
func hasSessionTag(body string) bool {
	return frontmatterContains(body, config.SessionResidencyTag)
}

// isSeatResident reports whether a seat with the session selector is handed the
// doc: it carries principles:session, or principles:seat (seat-only residency,
// never rendered at SessionStart).
func isSeatResident(body string) bool {
	return hasSessionTag(body) || frontmatterContains(body, config.SeatResidencyTag)
}

// frontmatterContains reports whether the leading `---`…`---` block contains s.
func frontmatterContains(body, s string) bool {
	t := strings.TrimLeft(body, "\n")
	if !strings.HasPrefix(t, "---") {
		return false
	}
	rest := t[len("---"):]
	i := strings.Index(rest, "\n---")
	if i < 0 {
		return false
	}
	return strings.Contains(rest[:i], s)
}

// stripFrontmatter drops a leading `---`…`---` YAML block, returning the markdown
// body. Returns body unchanged when there is no frontmatter.
func stripFrontmatter(body string) string {
	s := strings.TrimLeft(body, "\n")
	if !strings.HasPrefix(s, "---") {
		return body
	}
	rest := s[len("---"):]
	i := strings.Index(rest, "\n---")
	if i < 0 {
		return body
	}
	after := rest[i+len("\n---"):]
	if nl := strings.IndexByte(after, '\n'); nl >= 0 {
		return strings.TrimLeft(after[nl+1:], "\n")
	}
	return ""
}

// Gate judges item's transition to toStatus against every reviewer governing the
// edge — the workflow-named reviewers (one, or an ordered list) followed by the
// always-on system reviewer layer. Each reviewer runs in order and ALL must
// accept; the first reject short-circuits and blocks the edge. It returns the
// per-reviewer verdicts in run order plus a top-level verdict mirroring the
// deciding reviewer (the first reject, or the last when all accept), so
// single-reviewer callers keep their contract. Gated=false (enact directly)
// when no reviewer governs the edge.
func (g *Engine) Gate(ctx context.Context, item workitem.Item, toStatus string) (verb.GateDecision, error) {
	dec, err := g.gateEdge(ctx, item, toStatus)
	if err != nil {
		return dec, err
	}
	return g.foldSkillEvidence(ctx, item, toStatus, dec), nil
}

// foldSkillEvidence runs the always-on skill-evidence check on a transition that
// LEAVES a step the driving session performed, and folds its verdict into the
// edge's decision (sty_23ceeca2). A dispatched step's check already ran when its
// performer returned, against its output and event stream; this is the in-loop
// counterpart, against the documents attached and the tool calls the gate hooks
// recorded since the step was entered. Under the workflow's skill_evidence =
// reject a gap refuses the edge; under the default it is only recorded. The
// check never judges a back-edge or a move into a park or cancel state, which is
// not the step's completion, and never fails the gate for its own errors.
func (g *Engine) foldSkillEvidence(ctx context.Context, item workitem.Item, toStatus string, dec verb.GateDecision) verb.GateDecision {
	if toStatus == item.Status || (dec.Gated && !dec.Accept) {
		return dec // not a departure, or the edge is already refused
	}
	perf, err := g.resolvePerformer(ctx, item, item.Status, false)
	if err != nil || (perf.Kind != verb.PerformerExecutor && perf.Kind != verb.PerformerInLoop) || perf.target == nil {
		return dec
	}
	// Only the step's FORWARD exit is its completion: a back-edge (rework to an
	// earlier step), a park and a cancel are not, and must never be refused for
	// evidence the step has not had the chance to leave yet.
	if to := g.stateNamed(perf.spec, toStatus); to == nil || to.Agent == "reviewer" || !perf.spec.IsForwardEdge(item.Status, toStatus) {
		return dec
	}
	names := perf.spec.ExecutorSkillsFor(item.Status, item.Tags)
	if len(names) == 0 && perf.target.Skill != "" {
		names = []string{perf.target.Skill}
	}
	if len(names) == 0 {
		return dec
	}
	report, err := g.inLoopEvidence(ctx, item, names)
	if err != nil || report == nil {
		return dec
	}
	verdict := report.Verdict(len(dec.Reviewers))
	dec.Reviewers = append(dec.Reviewers, verdict)
	if !dec.Gated {
		dec.Gated, dec.Accept = true, true
	}
	if !verdict.Accept {
		dec.Accept = false
		dec.Skill = verb.SkillEvidenceCheck
		dec.Notes = strings.TrimSpace(dec.Notes + "\n" + report.Notes())
	}
	return dec
}

// stateNamed returns the spec's state called name, or nil.
func (g *Engine) stateNamed(spec wfdot.Spec, name string) *wfdot.State {
	for i := range spec.States {
		if spec.States[i].Name == name {
			return &spec.States[i]
		}
	}
	return nil
}

// gateEdge is Gate without the skill-evidence fold: the reviewers that govern the
// edge, in order, then the always-on system layer.
func (g *Engine) gateEdge(ctx context.Context, item workitem.Item, toStatus string) (verb.GateDecision, error) {
	// Broken substrate refuses to run (sty_d0d6bb67): a governing workflow that
	// fails its deterministic structure check must never gate work — refuse the
	// transition with the problems, instead of silently proceeding under a broken
	// definition.
	if err := g.guardWorkflowStructure(ctx, item, toStatus); err != nil {
		return verb.GateDecision{}, err
	}
	// Park resume-to-origin (sty_f75286dc): when ParkOrigin is set and the target
	// is that origin, the edge is declared and ungated — resume must not re-run
	// gates already passed to reach the origin. Wrong resume targets are refused
	// here (and by refuseSkippedStep) so park cannot wormhole around gates.
	if resume, refuse := g.parkResume(ctx, item, toStatus); refuse != nil {
		return verb.GateDecision{}, refuse
	} else if resume {
		return verb.GateDecision{Gated: false}, nil
	}
	skills, edgeAgent, parallelCap, declared, edgePanel, edgeCombine, err := g.reviewerSkills(ctx, item, item.Status, toStatus)
	if err != nil {
		return verb.GateDecision{}, err
	}
	if !declared {
		// The active workflow does not declare this edge — it is not a legal move.
		// Refuse it (the caller blocks the transition), so a story cannot skip a
		// gate by jumping across an edge the workflow never declared (sty_ebd3d666).
		// Structured (sty_39e2d9df): with the graph derived there is no file to open,
		// so the refusal itself carries the rule, why it applied here, and where the
		// story may go instead.
		next := g.successorsOf(ctx, item, item.Status)
		// Route drift first, when that is the actual cause: a status that is not a
		// state of the lane its category derives NOW has no legal edge, and the
		// generic undeclared-edge text ("no step after X") sends the reader to fix
		// a workflow that is not broken. Structural, not a verdict on the drift.
		if d, drifted := g.routeDriftFor(ctx, item); drifted && !d.StatusOnRoute {
			entry := ""
			if spec, _, serr := g.activeSpec(ctx, item); serr == nil {
				entry = spec.Start()
			}
			return verb.GateDecision{}, wfgovern.Refusal{
				Rule: wfgovern.RuleRouteDrift, Item: item.ID,
				From: item.Status, To: toStatus, Alternatives: next,
				Why:    wfgovern.DriftWhy(d),
				Remedy: wfgovern.DriftRemedy(d, entry),
			}
		}
		ref := wfgovern.Refusal{
			Rule: wfgovern.RuleUndeclaredEdge, Item: item.ID,
			From: item.Status, To: toStatus, Alternatives: next,
		}
		if len(next) > 0 {
			ref.Why = fmt.Sprintf(
				"the route puts %s after %s; entry to %s is gated, and an undeclared edge would reach it with no reviewer",
				strings.Join(next, " or "), item.Status, toStatus)
		} else {
			ref.Why = fmt.Sprintf("the route declares no step after %s", item.Status)
			ref.Remedy = "fix the workflow's declaration of done, or move the story to a declared state"
		}
		return verb.GateDecision{}, ref
	}
	// Before a story is IMPLEMENTED, guard against engaging it into a workflow that
	// cannot complete. On the ENGAGEMENT edge, deterministically (no agent) resolve
	// every EXECUTOR-step skill on the path to done: an executor step whose rubric
	// is missing leaves that step unperformable (the wasted-work trap — e.g. a
	// removed commit/push skill). Reject engagement up front, naming the gap. This is the
	// fast, in-process complement to the LLM satelle-workflow-review, which judges
	// the workflow's full structure + actionability at create/update. Reviewer-gate
	// skills are NOT required here — a missing reviewer rubric degrades to advisory
	// by design, so fresh repos keep working.
	if dec, blocked, gerr := g.guardEngagementExecutorSkills(ctx, item, toStatus); gerr != nil {
		return verb.GateDecision{}, gerr
	} else if blocked {
		return dec, nil
	}
	// Append the workflow's DECLARED scoped reviewers (edge-less reviewer nodes
	// whose on= includes this target, or "*") AFTER the edge-named ones — they run
	// last. Skills already named on the edge are not duplicated. The DOT is the sole
	// gating authority: there is no skill-tag scan that injects gates the workflow
	// never declared (the reviewer:always layer was removed — sty_ca9f675f).
	sys, err := g.scopedReviewers(ctx, item, toStatus, skills)
	if err != nil {
		return verb.GateDecision{}, err
	}
	// Build ordered gate list; edge skills share the edge's agent= binding
	// (sty_a476a2f8). Scoped nodes carry their own agent=.
	var ordered []reviewerRef
	failFast := g.edgeFailFast(ctx, item, item.Status, toStatus)
	for _, sk := range skills {
		ordered = append(ordered, reviewerRef{
			skill: sk, agent: edgeAgent, panel: edgePanel, combine: edgeCombine, parallel: parallelCap,
			mode: failFastModeOf(failFast, sk),
		})
	}
	sysStart := len(ordered)
	for i := range sys {
		sys[i].parallel = parallelCap
	}
	ordered = append(ordered, sys...)

	// A panel and a bundle cannot share a session: runGateBundled calls
	// runReviewer once per rubric inside one session, which would drop the
	// seats. Refuse before that branch, naming the edge, so a skipped validate
	// cannot enter it either.
	if g.edgeBundle(ctx, item, item.Status, toStatus) {
		for _, ref := range ordered {
			if len(ref.panel) == 0 {
				continue
			}
			return verb.GateDecision{}, fmt.Errorf(
				"gate refused: edge %s→%s is bundled and %q declares panel %v — a panel runs one cold session per seat and cannot share a bundled session",
				item.Status, toStatus, ref.skill, ref.panel)
		}
	}

	// reviewer_carry (sty_59720b8b): a step that declares it may leave an
	// unchanged earlier accept undispatched. Absent — the default — never enters
	// this branch, so every reviewer is dispatched every round.
	//
	// One tally counts the round against the whole ordered list, whichever path
	// dispatches it: a carried round that is not handled dispatched nothing, so
	// the plain path below starts the same tally fresh.
	tally := newGateTally(len(ordered))
	if g.edgeCarry(ctx, item, item.Status, toStatus) == wfdot.CarryUntilAccept {
		if dec, handled, cerr := g.gateCarrying(ctx, item, toStatus, ordered, sysStart, parallelCap, tally); handled {
			return dec, cerr
		}
	}
	all := make([]int, len(ordered))
	for i := range all {
		all[i] = i
	}
	return g.scheduleGate(ctx, item, toStatus, ordered, all, sysStart, parallelCap, tally)
}

// runScheduled dispatches ordered under the edge's bundle, parallel or serial
// scheduling and assembles the decision. Verdict Order is the position in
// ordered; scheduleGate maps it back when ordered is a subset of the edge.
// tally is the round's live count, which every loop stamps as gates return.
func (g *Engine) runScheduled(ctx context.Context, item workitem.Item, toStatus string, ordered []reviewerRef, sysStart, parallelCap int, tally *gateTally) (verb.GateDecision, error) {
	// Parallel opt-in (sty_4f0a15db): edge parallel=N|true runs reviewers
	// concurrently (no short-circuit). Absent/0 or a single reviewer keeps the
	// sequential first-reject loop byte-for-byte.
	// Bundle opt-in (sty_23e10d92): an edge that declares `bundle` runs the gates
	// that share a session's shape as ONE reviewer session with a verdict per
	// rubric. Absent — the default — never enters this branch, so gates run as
	// separate sessions exactly as before.
	if len(ordered) >= 2 && g.edgeBundle(ctx, item, item.Status, toStatus) {
		if dec, handled, berr := g.runGateBundled(ctx, item, toStatus, ordered, sysStart, parallelCap, tally); handled {
			return dec, berr
		}
	}
	if parallelCap > 0 && len(ordered) >= 2 {
		return g.runGateParallel(ctx, item, toStatus, ordered, sysStart, parallelCap, tally)
	}

	var result verb.GateDecision
	g.gatesOpened(item.ID, "gates", tally)
	for i, ref := range ordered {
		skill := ref.skill
		if skill == "" {
			continue
		}
		g.gateStarted(item.ID, skill, tally)
		dec, rerr := g.runReviewerRef(ctx, item, toStatus, ref, nil)
		g.gateReturned(item.ID, skill, tally, gateSlot{dec: dec, err: rerr})
		if rerr != nil {
			return dec, rerr
		}
		if !dec.Gated {
			// Declared but this reviewer's rubric is absent — advisory, skip it.
			// Carry WHICH skill was skipped so the advance is recorded as ungated
			// rather than looking like an edge that never had a gate (sty_d59ec6a9).
			result.Unresolved = append(result.Unresolved, dec.Unresolved...)
			continue
		}
		result.Gated = true
		result.Skill = dec.Skill
		result.Accept = dec.Accept
		result.Notes = dec.Notes
		result.Reasoning = dec.Reasoning
		result.Reviewed = dec.Reviewed
		result.ReviewedTruncated = dec.ReviewedTruncated
		result.ReviewMode, result.Coverage, result.Waived = dec.ReviewMode, dec.Coverage, dec.Waived
		result.Turns, result.TurnsUnavailableReason = dec.Turns, dec.TurnsUnavailableReason
		result.Command = dec.Command
		result.Context = dec.Context
		result.Model = dec.Model
		result.ModelResolved, result.Models = dec.ModelResolved, dec.Models
		result.ModelSource = dec.ModelSource
		result.TokensIn, result.TokensOut, result.TokensTotal = dec.TokensIn, dec.TokensOut, dec.TokensTotal
		result.TokensInFresh, result.TokensCacheWrite, result.TokensCacheRead = dec.TokensInFresh, dec.TokensCacheWrite, dec.TokensCacheRead
		result.UsageNote = dec.UsageNote
		result.RunCounts = dec.RunCounts
		result.CostUSD, result.CostUnavailableReason = dec.CostUSD, dec.CostUnavailableReason
		result.SystemPromptBytes, result.PayloadBytes = dec.SystemPromptBytes, dec.PayloadBytes
		result.ToolIsolation = dec.ToolIsolation
		result.DurationMs = dec.DurationMs
		result.UsageAvailable = dec.UsageAvailable
		result.Reviewers = append(result.Reviewers, reviewerVerdictOf(skill, i, i >= sysStart, dec))
		if !dec.Accept {
			return result, nil // a reject blocks the edge — do not run later reviewers
		}
	}
	return result, nil
}

// runGateParallel runs every reviewer in ordered concurrently with a bounded
// semaphore of size cap (sty_4f0a15db). Collects ALL verdicts (no short-circuit);
// order of result.Reviewers is the input index order. A reviewer ERROR (not a
// reject) returns the lowest-index error after all have finished.
func (g *Engine) runGateParallel(ctx context.Context, item workitem.Item, toStatus string, ordered []reviewerRef, sysStart, cap int, tally *gateTally) (verb.GateDecision, error) {
	if cap < 1 {
		cap = 1
	}
	if cap > len(ordered) {
		cap = len(ordered)
	}
	results := make([]gateSlot, len(ordered))
	sem := make(chan struct{}, cap)
	var wg sync.WaitGroup
	g.gatesOpened(item.ID, "gates (parallel)", tally)
	for i, ref := range ordered {
		if ref.skill == "" {
			continue
		}
		wg.Add(1)
		go func(i int, ref reviewerRef) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i].err = ctx.Err()
				return
			}
			// Approximate progress: most-recent start + completed count (sty_598a8e1b).
			g.gateStarted(item.ID, ref.skill+" (parallel)", tally)
			dec, rerr := g.runReviewerRef(ctx, item, toStatus, ref, nil)
			results[i] = gateSlot{dec: dec, err: rerr}
			g.gateReturned(item.ID, ref.skill+" (parallel)", tally, results[i])
		}(i, ref)
	}
	wg.Wait()
	return assembleGate(ordered, results, sysStart)
}

// gateSlot is one gate's outcome awaiting assembly into the edge's decision.
type gateSlot struct {
	dec verb.GateDecision
	err error
}

// reviewerVerdictOf is one gate's decision as the verdict row the ledger writes —
// the single place that maps a GateDecision onto a ReviewerVerdict, shared by
// the serial, parallel and bundled paths.
func reviewerVerdictOf(skill string, order int, system bool, dec verb.GateDecision) verb.ReviewerVerdict {
	return verb.ReviewerVerdict{
		Skill: skill, Order: order, Accept: dec.Accept, Notes: dec.Notes, Reasoning: dec.Reasoning,
		Reviewed: dec.Reviewed, ReviewedTruncated: dec.ReviewedTruncated, Criteria: dec.Criteria, System: system,
		ReviewMode: dec.ReviewMode, Coverage: dec.Coverage, Waived: dec.Waived,
		Command: dec.Command, Context: dec.Context, Model: dec.Model,
		ModelResolved: dec.ModelResolved, Models: dec.Models, ModelSource: dec.ModelSource,
		TokensIn: dec.TokensIn, TokensOut: dec.TokensOut, TokensTotal: dec.TokensTotal, DurationMs: dec.DurationMs,
		UsageAvailable: dec.UsageAvailable,
		TokensInFresh:  dec.TokensInFresh, TokensCacheWrite: dec.TokensCacheWrite, TokensCacheRead: dec.TokensCacheRead, UsageNote: dec.UsageNote,
		RunCounts: dec.RunCounts,
		CostUSD:   dec.CostUSD, CostUnavailableReason: dec.CostUnavailableReason,
		SystemPromptBytes: dec.SystemPromptBytes, PayloadBytes: dec.PayloadBytes,
		ToolIsolation: dec.ToolIsolation,
		BundleID:      dec.BundleID, BundleSkills: dec.BundleSkills,
		Seats: dec.Seats, Combine: dec.Combine, SeatStamps: dec.SeatStamps,
	}
}

// assembleGate folds every gate's outcome, in ordered's index order, into the
// edge's decision: no short-circuit, the lowest-index ERROR wins (a reviewer
// error is not a reject), and the top-level fields mirror the first reject, or
// the last gated verdict when all accept. Shared by the parallel and bundled
// paths.
func assembleGate(ordered []reviewerRef, results []gateSlot, sysStart int) (verb.GateDecision, error) {
	// Prefer lowest-index non-nil error (reviewer ERROR ≠ reject).
	for i := range results {
		if results[i].err != nil {
			return results[i].dec, results[i].err
		}
	}

	var result verb.GateDecision
	var firstReject *verb.GateDecision
	var lastGated *verb.GateDecision
	for i, ref := range ordered {
		if ref.skill == "" {
			continue
		}
		dec := results[i].dec
		if !dec.Gated {
			// Same advisory carry as the serial path — without it the parallel
			// edge silently loses the record of an ungated advance (sty_d59ec6a9).
			result.Unresolved = append(result.Unresolved, dec.Unresolved...)
			continue
		}
		result.Gated = true
		result.Reviewers = append(result.Reviewers, reviewerVerdictOf(ref.skill, i, i >= sysStart, dec))
		d := dec
		lastGated = &d
		if !dec.Accept && firstReject == nil {
			firstReject = &d
		}
	}
	// Top-level fields: first reject if any, else last gated (mirrors serial path).
	pick := lastGated
	if firstReject != nil {
		pick = firstReject
		result.Accept = false
	} else if pick != nil {
		result.Accept = true
	}
	if pick != nil {
		result.Skill = pick.Skill
		result.Notes = pick.Notes
		result.Reasoning = pick.Reasoning
		result.Reviewed = pick.Reviewed
		result.ReviewedTruncated = pick.ReviewedTruncated
		result.ReviewMode, result.Coverage, result.Waived = pick.ReviewMode, pick.Coverage, pick.Waived
		result.Turns, result.TurnsUnavailableReason = pick.Turns, pick.TurnsUnavailableReason
		result.Command = pick.Command
		result.Context = pick.Context
		result.Model = pick.Model
		result.ModelResolved, result.Models = pick.ModelResolved, pick.Models
		result.ModelSource = pick.ModelSource
		result.TokensIn, result.TokensOut, result.TokensTotal = pick.TokensIn, pick.TokensOut, pick.TokensTotal
		result.TokensInFresh, result.TokensCacheWrite, result.TokensCacheRead = pick.TokensInFresh, pick.TokensCacheWrite, pick.TokensCacheRead
		result.UsageNote = pick.UsageNote
		result.RunCounts = pick.RunCounts
		result.CostUSD, result.CostUnavailableReason = pick.CostUSD, pick.CostUnavailableReason
		result.SystemPromptBytes, result.PayloadBytes = pick.SystemPromptBytes, pick.PayloadBytes
		result.ToolIsolation = pick.ToolIsolation
		result.DurationMs = pick.DurationMs
		result.UsageAvailable = pick.UsageAvailable
		if firstReject == nil {
			result.Accept = pick.Accept
		}
	}
	return result, nil
}

// guardWorkflowStructure refuses a gate whose governing DEFINITION is broken
// (sty_d0d6bb67): the active workflow must pass its deterministic structure
// check (internal/structure) before it may gate a transition. The substrate
// executes as defined or refuses — a broken workflow never silently governs.
// Embedded canonical defaults are the binary's own bytes (validated by satelle's
// tests), so only authored (non-embedded) substrate is judged here; no resolvable
// workflow at all keeps the gateless path working.
func (g *Engine) guardWorkflowStructure(ctx context.Context, item workitem.Item, toStatus string) error {
	// A lifecycle is a DERIVED ROUTE, so the doc to judge is whichever half is
	// malformed (sty_d953c5d8). Judging the route source is the whole point of
	// this guard now — activeWorkflow deliberately does not return route halves,
	// so without this the guard would pass every broken route silently.
	if workflows, lerr := g.docs.List(ctx, "workflows"); lerr == nil {
		// An unreadable authored workflows dir refuses BEFORE the activeWorkflow
		// fallback below: that fallback would resolve the embedded default and let
		// the story be gated by a process that is not the repository's
		// (sty_d6e209aa).
		if path, reason, ok := docindex.UnreadableOf(workflows); ok {
			return wfgovern.Refusal{
				Rule: wfgovern.RuleStructureGuard, Item: item.ID,
				From: item.Status, To: toStatus,
				Why:    wfgovern.UnreadableMessage(path, reason),
				Remedy: "restore the workflows dir (it must be a readable directory) — no transition is legal until it can be read",
				Err:    wfgovern.ErrAuthoredProcessUnreadable,
			}
		}
		// Judge authored route halves regardless of whether RouteGovernsErr
		// currently resolves: a parse failure makes it false, which
		// is exactly when this guard must still fire (sty_5b88aa1b).
		for _, w := range workflows {
			if !wfgovern.IsRouteSource(w.Name) || w.Embedded {
				continue
			}
			if problems := structure.Doc("workflows", w.Name, w.Body, nil); len(problems) > 0 {
				return wfgovern.Refusal{
					Rule: wfgovern.RuleStructureGuard, Item: item.ID, Workflow: w.Name,
					From: item.Status, To: toStatus,
					TrackingStory: g.trackingStoryFor(ctx, "workflows", w.Name),
					Why: fmt.Sprintf("the governing workflow fails structure validation (%s), so no gate under it can be trusted to judge; %s",
						strings.Join(problems, "; "), wfgovern.EmbeddedLaneNotice()),
					Remedy: fmt.Sprintf("fix the substrate (`satelle workflow validate %s`) — if the file on disk already passes, the index is stale: run `satelle reindex`", w.Name),
				}
			}
		}
		if _, governs, _ := wfgovern.RouteGovernsErr(workflows, workflowCategory(item)); governs {
			return nil
		}
	}
	doc, err := g.activeWorkflowPreferring(ctx, workflowCategory(item), stampedWorkflowName(item))
	if err != nil {
		if errors.Is(err, docindex.ErrNotFound) {
			return nil
		}
		return err
	}
	if doc.Embedded {
		return nil
	}
	// resolveSkill is nil DELIBERATELY: executor-skill actionability is the
	// engagement guard's territory (guardEngagementExecutorSkills — a recorded,
	// edge-scoped DECISION), not a hard structural error on every edge. This
	// guard judges only whether the workflow DEFINITION itself is well-formed.
	if problems := structure.Doc("workflows", doc.Name, doc.Body, nil); len(problems) > 0 {
		// No alternatives on purpose: a workflow that fails structure validation
		// governs NO legal move, so the only honest answer is the remedy.
		return wfgovern.Refusal{
			Rule: wfgovern.RuleStructureGuard, Item: item.ID, Workflow: doc.Name,
			From: item.Status, To: toStatus,
			TrackingStory: g.trackingStoryFor(ctx, "workflows", doc.Name),
			Why: fmt.Sprintf("the governing workflow fails structure validation (%s), so no gate under it can be trusted to judge; %s",
				strings.Join(problems, "; "), wfgovern.EmbeddedLaneNotice()),
			Remedy: fmt.Sprintf("fix the substrate (`satelle workflow validate %s`) — no transition is legal until it passes", doc.Name),
		}
	}
	return nil
}

// SetNamedAgents wires the resolver for NAMED agent bindings from the agents
// layer (.satelle/workflows/agents.toml [<name>] sections) — the WHO of a workflow node's
// agent=<name> allocation (sty_fd427546). Nil keeps every step in-loop.
func (g *Engine) SetNamedAgents(fn func(name string) (config.AgentBinding, bool)) { g.namedAgents = fn }

// SetLiveNamedAgents wires the resolver OpenSessionAs uses to open a LIVE
// session: name's binding through AgentsConfig.LiveBinding, i.e.
// EffectiveBinding(UseLive) over the RAW (undefaulted) binding — an unset
// interface= resolves to the binding CLI's best live transport instead of
// always command (epic:model-selection child 2). Nil falls back to
// namedAgents, so a caller that never wires this resolves exactly as before
// (an unset interface stays command and OpenSessionAs refuses it).
func (g *Engine) SetLiveNamedAgents(fn func(name string) (config.AgentBinding, bool)) {
	g.liveNamedAgents = fn
}

// SetArtifactAttacher wires the verb-owned typed document writer used by
// structured step output contracts.
func (g *Engine) SetArtifactAttacher(fn func(context.Context, workitem.Item, string, string, string) (string, string, error)) {
	g.attachArtifact = fn
}

// SetSecondaryResolver wires rate-limit failover (sty_5bf61f89). The resolver
// returns (binding, name, ok) for a primary section + binding.
func (g *Engine) SetSecondaryResolver(fn func(section string, b config.AgentBinding) (config.AgentBinding, string, bool)) {
	g.resolveSecondary = fn
}

// SetIdleTimeoutResolver wires the resolver a NAMED binding's idle-stall bound
// is resolved through (sty_752c4ef2) — normally config.AgentsConfig.
// ResolveIdleTimeout, so a binding's own idle_timeout= wins over the shared
// [defaults] table, which wins over the shipped default. Nil (the zero value)
// falls back to binding.IdleTimeoutDuration(g.idleTimeout) — binding-level
// only, no [defaults] table — so a caller that never wires this still
// resolves sanely.
func (g *Engine) SetIdleTimeoutResolver(fn func(section string, b config.AgentBinding) (time.Duration, error)) {
	g.resolveIdleTimeout = fn
}

// idleTimeoutFor resolves section/b's idle-stall bound: through the wired
// resolver when set, else the binding alone against the engine-wide default.
func (g *Engine) idleTimeoutFor(section string, b config.AgentBinding) (time.Duration, error) {
	if g.resolveIdleTimeout != nil {
		return g.resolveIdleTimeout(section, b)
	}
	return b.IdleTimeoutDuration(g.idleTimeout)
}

// SetBusyTimeoutResolver wires the resolver a binding's busy_timeout (the CPU
// liveness cap for a silent command-transport run, sty_db62a3b9) is resolved
// through — normally config.AgentsConfig.ResolveBusyTimeout. Nil falls back to
// the binding alone against the engine default.
func (g *Engine) SetBusyTimeoutResolver(fn func(section string, b config.AgentBinding) (time.Duration, error)) {
	g.resolveBusyTimeout = fn
}

// busyTimeoutFor resolves section/b's busy cap; 0 means CPU liveness is off.
func (g *Engine) busyTimeoutFor(section string, b config.AgentBinding) (time.Duration, error) {
	if g.resolveBusyTimeout != nil {
		return g.resolveBusyTimeout(section, b)
	}
	return b.BusyTimeoutDuration(g.busyTimeout)
}

// performerResolution is resolvePerformer's whole answer: the verb-facing
// verdict plus the resolved route, placement and binding DispatchExecutor goes on
// to run.
type performerResolution struct {
	verb.PerformerResolution
	spec    wfdot.Spec
	wfName  string
	target  *wfdot.State
	placed  placement.Decision
	binding config.AgentBinding
}

// resolvePerformer is the engine's ONE rule for who performs a step: the TARGET
// state's named spine agent= performer, and nothing else. Flat dispatch
// (sty_05a5e203): the orchestrator is the sole scheduler, so entering a state
// never fires an agent of its own. An agent-less, executor or reviewer state, a
// role=reviewer binding that is a judge and not a performer, and a binding whose
// command is the in-loop session all dispatch nothing — the driving session
// performs, or an ADVISOR named on the route is consulted by the orchestrator at
// a moment it chooses. A missing binding, or a role=reviewer binding allocated on
// a performing node, is an ERROR: a broken definition never silently falls back
// in-loop (sty_d0d6bb67). DispatchExecutor and StepPerformer both ask it, so a
// presented edge and `story ready` cannot disagree (sty_a32bd6a9).
//
// emit says whether to record the placement telemetry event; only the dispatch
// itself does.
func (g *Engine) resolvePerformer(ctx context.Context, item workitem.Item, toStatus string, emit bool) (performerResolution, error) {
	answer := func(kind verb.PerformerKind, reason string) (performerResolution, error) {
		return performerResolution{PerformerResolution: verb.PerformerResolution{Reason: reason, Kind: kind}}, nil
	}
	none := func(reason string) (performerResolution, error) { return answer(verb.PerformerNone, reason) }
	spec, wfName, err := g.activeSpec(ctx, item)
	if err != nil {
		if ungoverned(err) {
			return none("no workflow governs this item")
		}
		return performerResolution{}, err
	}
	var target *wfdot.State
	for i := range spec.States {
		if spec.States[i].Name == toStatus {
			target = &spec.States[i]
			break
		}
	}
	if target == nil {
		return answer(verb.PerformerUndeclared, fmt.Sprintf("the workflow declares no step %q", toStatus))
	}
	switch target.Agent {
	case "":
		return none(fmt.Sprintf("step %q allocates no performer (agent is empty)", toStatus))
	case "executor":
		return g.resolveExecutorStep(ctx, item, toStatus, spec, wfName, target)
	case "reviewer":
		return none(fmt.Sprintf("step %q is allocated to agent=%s, which is not a dispatched performer", toStatus, target.Agent))
	}
	// Placement (sty_dde8b6a4): a parallel epic child's step may be declared to
	// run in a cloud session (step.toml remote_agent). Otherwise, or when the
	// session is not signed in, the step's own agent performs, unchanged.
	//
	// A binding's own placement (sty_7028d996) is read through the same lookup the
	// dispatch uses, resolved once per name.
	var lookup placement.Bindings
	if g.namedAgents != nil {
		type found struct {
			b  config.AgentBinding
			ok bool
		}
		seen := map[string]found{}
		lookup = func(name string) (config.AgentBinding, bool) {
			f, hit := seen[name]
			if !hit {
				f.b, f.ok = g.namedAgents(name)
				seen[name] = f
			}
			return f.b, f.ok
		}
	}
	placed := placement.Decide(ctx, item, *target, lookup)
	if placed.PinErr != nil {
		return performerResolution{}, fmt.Errorf("step %q: %w", toStatus, placed.PinErr)
	}
	dispatchAgent := placed.Agent
	if placed.Note != "" && emit {
		g.telemetryEvent(ctx, item.ID, "executor", "placement", map[string]any{
			"placement": placed.Note, "step": toStatus, "agent": dispatchAgent,
		})
	}
	if g.namedAgents == nil {
		return performerResolution{}, fmt.Errorf(
			"workflow %q allocates state %q to named agent %q but no agents layer is wired", wfName, toStatus, dispatchAgent)
	}
	binding, found := lookup(dispatchAgent)
	if !found {
		return performerResolution{}, fmt.Errorf(
			"workflow %q allocates state %q to agent %q but .satelle/workflows/agents.toml defines no [%s] binding — define it, or reassign the step",
			wfName, toStatus, dispatchAgent, dispatchAgent)
	}
	// Design §9 (a): when the resolved binding is role=reviewer, it is a judge
	// not a performer — do not dispatch as ExpectPerform (isNamedPerformer).
	// Fail loud when a role=reviewer binding is allocated on a performing node.
	if config.ResolvedRole(dispatchAgent, binding) == config.RoleReviewer {
		return performerResolution{}, fmt.Errorf(
			"workflow %q allocates performing state %q to agent %q with role=reviewer — judges advance status only via gates; use a gated edge or scoped on= node",
			wfName, toStatus, dispatchAgent)
	}
	// A team placement (sty_7028d996) wakes a remote rather than running a
	// command, so its binding needs none: it is dispatched before the named-
	// performer and in-loop checks, which read the command.
	dispatched := func() performerResolution {
		return performerResolution{
			PerformerResolution: verb.PerformerResolution{
				Agent: dispatchAgent, Dispatched: true, Kind: verb.PerformerDispatched,
				Team: placed.Team, Remote: placed.Remote, Channel: placed.Channel, PlacementNote: placed.Note,
			},
			spec: spec, wfName: wfName, target: target, placed: placed, binding: binding,
		}
	}
	inLoop := func(why string) performerResolution {
		pr, _ := answer(verb.PerformerInLoop, fmt.Sprintf("step %q agent %q %s: the driving session performs it", toStatus, dispatchAgent, why))
		pr.Agent = dispatchAgent
		pr.spec, pr.wfName, pr.target = spec, wfName, target
		return pr
	}
	if placed.Team != "" {
		return dispatched(), nil
	}
	// A child the orchestrator pinned in-loop (sty_c931cd90) is performed by the
	// driving session whatever its binding's command is.
	if placed.InLoop {
		return inLoop("is pinned in-loop"), nil
	}
	if !isNamedPerformer(dispatchAgent, binding) {
		return none(fmt.Sprintf("step %q agent %q is not a named performer", toStatus, dispatchAgent))
	}
	if config.IsInLoopCommand(binding.CommandTemplate()) {
		if placed.Pinned {
			return performerResolution{}, fmt.Errorf(
				"step %q is pinned local but the [%s] command in .satelle/workflows/agents.toml is in-loop: bind a headless CLI, or pin the child in-loop", toStatus, dispatchAgent)
		}
		return inLoop("is bound in-loop"), nil
	}
	return dispatched(), nil
}

// StepPerformer implements verb.PerformerResolver: whether entering toStatus
// dispatches a named performer, under the rule DispatchExecutor itself uses.
//
// A dispatched performer's skill may declare an output contract; its output_name
// is the document the step attaches, reported as OutputName so a caller that must
// recognise that document (the readiness check) reads it from the skill rather
// than naming one itself.
func (g *Engine) StepPerformer(ctx context.Context, item workitem.Item, toStatus string) (verb.PerformerResolution, error) {
	perf, err := g.resolvePerformer(ctx, item, toStatus, false)
	if err != nil || !perf.Dispatched {
		return perf.PerformerResolution, err
	}
	skill := firstStr(perf.spec.ExecutorSkillsFor(toStatus, item.Tags))
	if skill == "" {
		skill = perf.target.Skill
	}
	if skill == "" {
		return perf.PerformerResolution, nil
	}
	body, serr := g.skillBody(ctx, skill)
	if errors.Is(serr, docindex.ErrNotFound) {
		return perf.PerformerResolution, nil
	}
	if serr != nil {
		return perf.PerformerResolution, serr
	}
	contract, cerr := agentartifact.ParseContract(body)
	if cerr != nil {
		return perf.PerformerResolution, fmt.Errorf("skill %q output contract: %w", skill, cerr)
	}
	perf.OutputName = contract.Name
	perf.CriteriaSection = contract.CriteriaSection
	return perf.PerformerResolution, nil
}

// DispatchExecutor implements verb.ExecutorDispatcher: when the TARGET state of
// an accepted transition is allocated to a NAMED agent (agent=<name>, neither
// "executor" nor "reviewer"), the binding's harness performs the step
// synchronously — prompt assembled from the item (title, body, acceptance
// criteria on stdin) plus the node's @skill rubric, tools/model/principles from
// the binding, nothing hardcoded (sty_fd427546). A missing binding or a failed
// run is an ERROR — the caller refuses the transition (broken definition never
// silently falls back in-loop, consistent with sty_d0d6bb67). agent=executor,
// agent-less and reviewer states dispatch nothing; a named binding whose harness
// is explicitly "in-loop" also stays with the orchestrator.
//
// Flat dispatch (sty_05a5e203): this is the ONLY dispatch entering a state can
// cause, and it happens because the SPINE allocates the step — not because the
// state fires an agent of its own. Entry dispatch (on_enter_agent) is retired:
// steps never call steps, so an advisor is consulted by the orchestrator at a
// moment it chooses, and the route names which advisor that is.
func (g *Engine) DispatchExecutor(ctx context.Context, item workitem.Item, toStatus string) (verb.DispatchResult, error) {
	perf, err := g.resolvePerformer(ctx, item, toStatus, true)
	if err != nil {
		return verb.DispatchResult{}, err
	}
	if !perf.Dispatched {
		return g.deliverInLoopSkills(ctx, item, toStatus, perf)
	}
	target, wfName, spec, placed := perf.target, perf.wfName, perf.spec, perf.placed
	dispatchAgent, binding := perf.Agent, perf.binding
	composed := spec.ExecutorSkillsFor(toStatus, item.Tags)
	dispatchSkill := firstStr(composed)
	if dispatchSkill == "" {
		dispatchSkill = target.Skill
	}
	// A team placement wakes its remote and waits on the run: no performer is
	// dispatched, so none of the below applies (sty_7028d996).
	if placed.Team != "" {
		return g.dispatchTeam(ctx, item, toStatus, dispatchAgent, dispatchSkill, placed.Team, binding)
	}
	// Model selection (sty_7069bced): binding.Model wins outright when set;
	// otherwise the step's own model= (target.Model) is a per-dispatch
	// override, then the inherited/creator session tiers, else cli-default.
	// This is distinct from the RETIRED DOT edge model= (sty_a476a2f8, still
	// retired) — that was a gate override on an edge; target.Model is a
	// per-dispatch instruction for THIS state's performer.
	//
	// binding itself is NEVER mutated with the resolved value (4.1 fix): a
	// mutated binding.Model would be misread by a later re-selection (inside
	// runArtifactAttempts) as an explicit agents.toml model=, poisoning every
	// attempt's recorded source to "binding". modelChoice carries the result;
	// callers that need it applied build their OWN per-use copy.
	modelResolved, modelSource := g.selectModel(ctx, binding, item.ID, target.Model, config.ModelSourceStep)
	modelChoice := ModelChoice{Model: modelResolved, Source: modelSource}
	// A cloud performer returns a pushed branch, not stdout: it runs and is
	// collected on its own path, which never reaches the runner below.
	if binding.ResolvedInterface() == config.InterfaceCloud {
		launch := g.cloudDispatch
		if launch == nil {
			launch = (*Engine).dispatchCloud
		}
		return launch(g, ctx, item, toStatus, wfName, dispatchAgent, binding, composed, dispatchSkill, placed.Remote)
	}
	if placed.Remote && !placed.Channel {
		return verb.DispatchResult{}, fmt.Errorf(
			"workflow %q places state %q remote on agent %q but its [%s] binding is not interface=cloud — fix remote_agent in step.toml",
			wfName, toStatus, dispatchAgent, dispatchAgent)
	}
	var (
		outputContract agentartifact.Contract
		attemptPolicy  agentartifact.AttemptPolicy
	)
	if dispatchSkill != "" {
		skillBody, serr := g.skillBody(ctx, dispatchSkill)
		if serr != nil && !errors.Is(serr, docindex.ErrNotFound) {
			return verb.DispatchResult{}, serr
		}
		if serr == nil {
			var cerr error
			outputContract, cerr = agentartifact.ParseContract(skillBody)
			if cerr != nil {
				return verb.DispatchResult{}, fmt.Errorf("skill %q output contract: %w", dispatchSkill, cerr)
			}
			attemptPolicy, cerr = agentartifact.ParseAttemptPolicy(skillBody)
			if cerr != nil {
				return verb.DispatchResult{}, fmt.Errorf("skill %q attempt policy: %w", dispatchSkill, cerr)
			}
			if attemptPolicy.Active() && !outputContract.Active() {
				return verb.DispatchResult{}, fmt.Errorf(
					"skill %q declares an attempt policy without an output_* artifact contract", dispatchSkill)
			}
		}
	}
	// Engagement lease (sty_8426b9c0) is acquired for the TARGET engaging state
	// BEFORE this dispatch runs (verb/workitem.go acquire-at-start). Edit/commit
	// gates read the lease, not committed FROM status — so a code-writing named
	// agent may edit during dispatch without the FROM state itself being
	// performing. The prior FROM-performing band-aid (sty_f5bd176f) is removed.
	var runner agentcli.Runner
	if placed.Channel {
		// A remote-placed binding (sty_7028d996): the same dispatch with its
		// transport swapped for the paired remote channel. The performer is the
		// node's own binding of this name, so nothing below that reads the local
		// command, worktree wiring or tool grant applies.
		rr, rerr := g.remoteRunnerFor(dispatchAgent, binding, item.ID)
		if rerr != nil {
			return verb.DispatchResult{}, fmt.Errorf("workflow %q places state %q remote: %w", wfName, toStatus, rerr)
		}
		defer func() { _ = rr.Close() }()
		runner = rr
		g.telemetryEvent(ctx, item.ID, "executor", "remote_dispatch", map[string]any{
			"agent": dispatchAgent, "step": toStatus, "placement": config.PlacementRemote, "remote": binding.Remote,
		})
	} else {
		r, rerr := g.newRunner(binding.ResolvedInterface(), binding.CommandTemplate())
		if rerr != nil {
			var enf *ExecutableNotFoundError
			if errors.As(rerr, &enf) {
				// Local resolution refused (sty_01949949 AC3): same voice as the
				// missing-binding refusal above, and the same posture — the caller
				// rejects the transition; nothing is performed in-loop instead.
				return verb.DispatchResult{}, fmt.Errorf(
					"workflow %q allocates state %q to agent %q but its command %q is not executable on this machine (%v) — install it or rebind the agent",
					wfName, toStatus, dispatchAgent, enf.Token, enf.Err)
			}
			return verb.DispatchResult{}, fmt.Errorf("named agent %q: broken command in .satelle/workflows/agents.toml: %w", dispatchAgent, rerr)
		}
		if r == nil {
			return verb.DispatchResult{}, nil // command "in-loop": the orchestrator performs the step
		}
		runner = r
		// A performer that would run in a linked worktree without its gate wiring
		// is refused before it starts (or visibly allowed under fail-open).
		if err := g.wiringGuard(ctx, dispatchAgent, binding.CommandTemplate(), item.ID); err != nil {
			return verb.DispatchResult{}, err
		}
	}
	// A dispatched executor starts fresh and reconstructs its context by PULLING the
	// story, its documents, and the ledger — either via the read-only satelle CLI
	// (the pull-context call-to-action, sty_47d31300) or via disk reads under the
	// home-keyed runtime stories dir when the binding has a file-read tool but no
	// shell (sty_565a0202 grok coder; path: sty_58fa970e / sty_4660bbe1). Without a
	// context channel the agent is silently context-starved. Refuse the dispatch
	// with an actionable fix rather than run a blind agent — the no-silent-fallback
	// style the engine uses for a missing binding.
	if !placed.Channel && config.NeedsContextChannel(binding) && !config.BindingGrantsContextChannel(binding) {
		return verb.DispatchResult{}, fmt.Errorf(
			"named agent %q cannot perform step %q: its .satelle/workflows/agents.toml [%s] tools grant has no context channel (add `Bash(satelle:*)` for the satelle CLI, or %s under ~/.satelle/<repo-key>/stories/<id>/)",
			dispatchAgent, toStatus, dispatchAgent, agentcli.ContextChannelHint(agentcli.AdapterName(binding.CommandTemplate())))
	}
	// Composed rubrics: spine skill first, then matching augmentations in order
	// (sty_8225d8a5). Absent skills stay advisory here — the engagement guard
	// already hard-blocks missing executor-path skills. LLM run goes through
	// shared Invoke (sty_ba860c8a).
	delivered, rerr := g.composeSkills(ctx, composed, false)
	if rerr != nil {
		return verb.DispatchResult{}, rerr
	}
	rubric := delivered.Rubric
	// A declared skill that does not resolve is recorded as undelivered. Under the
	// workflow's skill_evidence = reject the step is refused; otherwise it runs and
	// the gap is on the row and in front of the driver (sty_23ceeca2).
	if rerr := g.undeliveredRefusal(ctx, delivered, wfName, toStatus, dispatchAgent); rerr != nil {
		return verb.DispatchResult{Skills: delivered.Manifest, Agent: dispatchAgent, Skill: dispatchSkill}, rerr
	}
	// The hard ceiling falls back to g.agentTimeout (unset by default,
	// sty_752c4ef2), NOT g.checkTimeout (defaultCheckTimeout, a functional-
	// check bound out of this story's scope) — a dispatched performer like the
	// coder is a whole implementation, not a check, and used to inherit
	// checkTimeout's 20m by accident (the exact cap that killed sty_87b86044's
	// and sty_7069bced's coder dispatches).
	timeout, terr := binding.TimeoutDuration(g.agentTimeout)
	if terr != nil {
		return verb.DispatchResult{}, fmt.Errorf("named agent %q: invalid timeout in .satelle/workflows/agents.toml [%s]: %w", dispatchAgent, dispatchAgent, terr)
	}
	idle, ierr := g.idleTimeoutFor(dispatchAgent, binding)
	if ierr != nil {
		return verb.DispatchResult{}, fmt.Errorf("named agent %q: invalid idle_timeout in .satelle/workflows/agents.toml [%s]: %w", dispatchAgent, dispatchAgent, ierr)
	}
	dlog, sinkPath, closeLog := g.openDispatchLog(dispatchAgent, item.ID, binding)
	defer closeLog()
	rawSink, closeRaw := g.dispatchRawSink(sinkPath)
	if closeRaw != nil {
		defer closeRaw()
	}
	trace := &runTrace{}
	onEvent := trace.handler(dlog.handler())
	g.emitActivity(item.ID, "dispatch:"+toStatus, 1, 1)
	if sinkPath != "" {
		g.emitProgress("dispatching step %s to named agent %s (may take several minutes)… live output: %s", toStatus, dispatchAgent, sinkPath)
	} else {
		g.emitProgress("dispatching step %s to named agent %s (may take several minutes)…", toStatus, dispatchAgent)
	}
	execPayload, consumed := g.performerPayload(ctx, item, toStatus, dispatchSkill, dispatchAgent, binding, target.ApplyCriteriaDoc)
	recordConsumed := func(attempt int) {
		if consumed != nil {
			g.recordDocConsumed(ctx, item.ID, dispatchAgent, toStatus, *consumed, attempt)
		}
	}
	charter := executorCharter(dispatchAgent, toStatus, wfName)
	var finalArtifact *agentartifact.Artifact
	var invRes InvokeResult
	if attemptPolicy.Active() {
		var attemptErr error
		invRes, finalArtifact, attemptErr = g.runArtifactAttempts(
			ctx, item, toStatus, dispatchSkill, dispatchAgent, rubric,
			execPayload, charter, binding, modelChoice, target.Model, config.ModelSourceStep,
			runner, timeout, idle, rawSink, onEvent, dlog,
			outputContract, attemptPolicy, recordConsumed)
		if attemptErr != nil {
			invRes.Err = attemptErr
		}
	} else {
		invokeBinding := binding
		invokeBinding.Model = modelChoice.Model
		recordConsumed(1)
		invRes = g.Invoke(ctx, InvokeRequest{
			Binding:     invokeBinding,
			Section:     dispatchAgent,
			Rubric:      rubric,
			Payload:     execPayload,
			Charter:     charter,
			Expect:      ExpectPerform,
			Timeout:     timeout,
			IdleTimeout: idle,
			Runner:      runner,
			Sink:        rawSink,
			OnEvent:     onEvent,
			Log:         dlog,
			StoryID:     item.ID,
			Step:        toStatus,
			Skill:       dispatchSkill,
			Actor:       "executor",
		})
	}
	res := g.performanceResult(dispatchAgent, dispatchSkill, binding, modelChoice, invRes)
	res.Skills = delivered.Manifest
	g.logExecutorRun(dispatchAgent, item.ID, toStatus, invRes.Stdout, invRes.Err)
	if invRes.Err != nil {
		failData := map[string]any{
			"agent": dispatchAgent, "step": toStatus, "outcome": classifyOutcome(invRes.Err),
			"duration_ms": res.DurationMs, "usage_available": res.UsageAvailable,
		}
		if res.UsageAvailable {
			failData["tokens_total"] = res.TokensTotal
		}
		g.telemetryEvent(ctx, item.ID, "executor", "agent-failure", withCause(failData, invRes.Err))
		return res, fmt.Errorf("named agent %q failed performing step %q: %w", dispatchAgent, toStatus, invRes.Err)
	}
	// A performer reject is not a crash. The verb parks the story as blocked
	// and puts the notes on the timeline (sty_b8a0d062). No verdict is a
	// normal performer. An accept proceeds.
	//
	// A performer that cannot write may report the configuration changes it wants
	// beside its verdict or artifact; the verb applies them on the record
	// (sty_af0b1761). The decode is the shared one, so every adapter reports alike.
	changes, _, cerr := agentartifact.DecodeChanges(invRes.Stdout)
	if cerr != nil {
		return res, fmt.Errorf("named agent %q reported changes for step %q that cannot be read: %w", dispatchAgent, toStatus, cerr)
	}
	res.Changes = changes
	if aerr := RecordFindingAnswers(&res, execPayload.Findings, execPayload.FindingsEdgeTo, invRes.Stdout); aerr != nil {
		return res, fmt.Errorf("named agent %q reported answers for step %q that cannot be read: %w", dispatchAgent, toStatus, aerr)
	}
	if dec, derr := parseDecision(invRes.Stdout); derr == nil && !dec.Accept {
		return res, &verb.PerformerReject{Notes: dec.Notes, Changes: changes}
	}
	// The run is over: compare what it produced and did with the evidence each
	// delivered skill declares (sty_23ceeca2). The artifact body is read at the
	// return, once the contract below has decoded it.
	mode := g.skillEvidenceMode(ctx)
	done := func() (verb.DispatchResult, error) {
		artifactBody := ""
		if finalArtifact != nil {
			artifactBody = finalArtifact.Body
		}
		res.Evidence = g.dispatchedEvidence(ctx, delivered, binding, string(invRes.Stdout), artifactBody, trace, mode)
		return res, nil
	}
	if outputContract.Active() && !attemptPolicy.Active() {
		artifact, derr := agentartifact.Decode(invRes.Stdout)
		switch {
		case derr == nil:
			validated, verr := agentartifact.Validate(artifact, outputContract, item.AcceptanceCriteria)
			if verr != nil {
				return res, fmt.Errorf("named agent %q structured output for step %q: %w", dispatchAgent, toStatus, verr)
			}
			finalArtifact = &validated
		case !outputContract.Required && errors.Is(derr, agentartifact.ErrNoArtifact):
			// An optional artifact the performer did not return: finalArtifact stays nil.
		default:
			return res, fmt.Errorf("named agent %q structured output for step %q: %w", dispatchAgent, toStatus, derr)
		}
	}
	// The output is final here on every path (an attempt policy resolved it
	// above, a plain contract just now), so this is where the performer's
	// standing against the findings it was offered is read (sty_73224b86).
	g.recordFindingProgress(ctx, &res, item, execPayload.Findings, finalArtifact)
	if outputContract.Active() {
		if finalArtifact == nil {
			return done()
		}
		if g.attachArtifact == nil {
			return res, fmt.Errorf("named agent %q produced contracted output for step %q but no artifact attachment writer is configured", dispatchAgent, toStatus)
		}
		name, typ, aerr := g.attachArtifact(ctx, item, finalArtifact.Name, finalArtifact.Type, finalArtifact.Body)
		if aerr != nil {
			return res, fmt.Errorf("named agent %q artifact attachment for step %q: %w", dispatchAgent, toStatus, aerr)
		}
		res.ArtifactName, res.ArtifactType = name, typ
	}
	return done()
}

// performanceResult is the record of one performer run: who ran, on what
// command and model, at what cost, and what it printed. DispatchExecutor and
// PerformExecutorStep both build it, so a dispatched step and an executor step
// are recorded alike.
func (g *Engine) performanceResult(agent, skill string, binding config.AgentBinding, mc ModelChoice, invRes InvokeResult) verb.DispatchResult {
	modelResolved, models := toVerbModels(invRes.Usage)
	res := verb.DispatchResult{
		Dispatched: true, Agent: agent, Command: invRes.Command, Model: invRes.RanModel(mc.Model), Skill: skill,
		ModelResolved: modelResolved, Models: models, ModelSource: mc.Source,
		TokensIn: invRes.Usage.InputTokens, TokensOut: invRes.Usage.OutputTokens, TokensTotal: invRes.Usage.TotalTokens,
		DurationMs: invRes.Usage.Duration.Milliseconds(), UsageAvailable: invRes.Usage.Available,
		TokensInFresh: invRes.Usage.FreshInputTokens, TokensCacheWrite: invRes.Usage.CacheCreationInputTokens, TokensCacheRead: invRes.Usage.CacheReadInputTokens, UsageNote: usageNoteFor(invRes.Usage, binding),
		CostUSD: invRes.Usage.CostUSD, CostUnavailableReason: invRes.Usage.CostUnavailableReason,
		SystemPromptBytes: invRes.SystemPromptBytes, PayloadBytes: invRes.PayloadBytes,
		Output: string(invRes.Stdout), LastMessage: verb.OutputTail(string(invRes.Stdout), g.scrubValues(binding.Env)),
	}
	if invRes.Err == nil {
		res.Turns, res.TurnsUnavailableReason = turnsOf(invRes.Usage, binding.ResolvedInterface(), binding.CommandTemplate())
	}
	return res
}

// dispatchRawSink creates a sibling raw transport trace only when explicitly
// requested. Hidden reasoning is still filtered and obvious credentials are
// redacted by agentcli before bytes reach this writer.
func (g *Engine) dispatchRawSink(normalizedPath string) (io.Writer, func()) {
	if normalizedPath == "" {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SATELLE_AGENT_TRACE_RAW"))) {
	case "1", "true", "yes", "on":
	default:
		return nil, nil
	}
	path := strings.TrimSuffix(normalizedPath, ".log") + "-raw.log"
	f, err := os.Create(path)
	if err != nil {
		return nil, nil
	}
	return f, func() { _ = f.Close() }
}

// retrospectAgent / retrospectSkill name the post-story improvement step: an
// isolated named agent that reads a finished story and proposes improvements
// (sty_b53730e2). It is a dispatched EXECUTOR (it creates backlog proposals via
// the CLI), never a read-only reviewer — the constitution keeps reviewers judging
// only, so a step that MUTATES is a named agent.
const (
	retrospectAgent = "retrospective"
	retrospectSkill = "satelle-retrospective"
)

// Retrospect dispatches the retrospective agent over a finished story or
// container: it pulls the story + its plan/summary/ledger by id (a container's
// payload also carries its children, so each child's ledger can be pulled) and
// files improvement PROPOSALS as backlog stories (its Bash(satelle:*) grant).
// Invoked by `satelle story retrospect`, which the orchestrator relays when the
// route declares an `advise` at the closing step — nothing fires on entering done
// (flat dispatch). Returns the dispatch result (with captured output +
// token/wall-time cost) so the verb layer can record an agent_invocation for the
// cost view. It changes no item's status.
//
// spec.Agent / spec.Skill are the route-declared advisor; empty falls back to the
// built-in retrospective. spec.Model is `satelle story retrospect --model`
// (sty_7069bced): a per-dispatch instruction recorded with source=agent, one tier
// below the binding's own model= and above the inherited/creator session tiers.
func (g *Engine) Retrospect(ctx context.Context, item workitem.Item, spec verb.RetrospectSpec) (verb.DispatchResult, error) {
	modelOverride := spec.Model
	retrospectAgent := cmp.Or(spec.Agent, retrospectAgent)
	retrospectSkill := cmp.Or(spec.Skill, retrospectSkill)
	if g.namedAgents == nil {
		return verb.DispatchResult{}, fmt.Errorf("no agents layer is wired — cannot dispatch the %q agent", retrospectAgent)
	}
	binding, found := g.namedAgents(retrospectAgent)
	if !found {
		return verb.DispatchResult{}, fmt.Errorf(
			"no [%s] binding in .satelle/workflows/agents.toml — define it (with Bash(satelle:*) so it can file proposals) to run the retrospective", retrospectAgent)
	}
	runner, err := g.newRunner(binding.ResolvedInterface(), binding.CommandTemplate())
	if err != nil {
		return verb.DispatchResult{}, fmt.Errorf("%s agent: broken command: %w", retrospectAgent, err)
	}
	if runner == nil {
		return verb.DispatchResult{}, fmt.Errorf("%s agent harness is in-loop; set a real harness to dispatch it", retrospectAgent)
	}
	if !config.BindingGrantsContextChannel(binding) {
		return verb.DispatchResult{}, fmt.Errorf(
			"[%s] tools grant has no context channel (add `Bash(satelle:*)` for the satelle CLI, or %s) — it needs a channel to pull the story and file proposal stories",
			retrospectAgent, agentcli.ContextChannelHint(agentcli.AdapterName(binding.CommandTemplate())))
	}
	rubric := ""
	if body, rerr := g.skillBody(ctx, retrospectSkill); rerr == nil {
		rubric = body
	} else if !errors.Is(rerr, docindex.ErrNotFound) {
		return verb.DispatchResult{}, rerr
	}
	// Hard ceiling falls back to g.agentTimeout (unset by default), NOT
	// g.checkTimeout (defaultCheckTimeout, a functional-check bound out of
	// this story's scope) — the retrospective is a dispatched executor like
	// the coder, and used to inherit checkTimeout's 20m by accident
	// (sty_752c4ef2).
	retroTimeout, rterr := binding.TimeoutDuration(g.agentTimeout)
	if rterr != nil {
		return verb.DispatchResult{}, fmt.Errorf("named agent %q: invalid timeout in .satelle/workflows/agents.toml [%s]: %w", retrospectAgent, retrospectAgent, rterr)
	}
	retroIdle, rierr := g.idleTimeoutFor(retrospectAgent, binding)
	if rierr != nil {
		return verb.DispatchResult{}, fmt.Errorf("named agent %q: invalid idle_timeout in .satelle/workflows/agents.toml [%s]: %w", retrospectAgent, retrospectAgent, rierr)
	}
	g.emitActivity(item.ID, "retrospective", 1, 1)
	g.emitProgress("running retrospective on %s (may take a few minutes)…", item.ID)
	retroPayload := transitionPayload{Story: item, From: item.Status, To: item.Status, ReviewSkill: retrospectSkill}
	if g.children != nil {
		retroPayload.Children = g.children(ctx, item)
	}
	g.fillPayloadDocs(ctx, item.ID, &retroPayload, nil)
	// Model selection (sty_7069bced): binding.Model wins outright when set;
	// otherwise modelOverride (--model) is a per-dispatch instruction, then the
	// inherited/creator session tiers apply, else cli-default.
	retroModelResolvedInput, retroModelSource := g.selectModel(ctx, binding, item.ID, modelOverride, config.ModelSourceAgent)
	binding.Model = retroModelResolvedInput
	dlog, _, closeLog := g.openDispatchLog(retrospectAgent, item.ID, binding)
	defer closeLog()
	invRes := g.Invoke(ctx, InvokeRequest{
		Binding:     binding,
		Section:     retrospectAgent,
		Rubric:      rubric,
		Payload:     retroPayload,
		Charter:     executorCharter(retrospectAgent, "retrospect", "post-story retrospective"),
		Expect:      ExpectPerform,
		Timeout:     retroTimeout,
		IdleTimeout: retroIdle,
		Runner:      runner,
		OnEvent:     dlog.handler(),
		Log:         dlog,
		StoryID:     item.ID,
		Step:        "retrospect",
		Skill:       retrospectSkill,
		Actor:       "executor",
	})
	retroModelResolved, retroModels := toVerbModels(invRes.Usage)
	res := verb.DispatchResult{
		Dispatched: true, Agent: retrospectAgent, Command: invRes.Command, Model: invRes.RanModel(binding.Model), Skill: retrospectSkill,
		ModelResolved: retroModelResolved, Models: retroModels, ModelSource: retroModelSource,
		TokensIn: invRes.Usage.InputTokens, TokensOut: invRes.Usage.OutputTokens, TokensTotal: invRes.Usage.TotalTokens,
		DurationMs: invRes.Usage.Duration.Milliseconds(), UsageAvailable: invRes.Usage.Available,
		TokensInFresh: invRes.Usage.FreshInputTokens, TokensCacheWrite: invRes.Usage.CacheCreationInputTokens, TokensCacheRead: invRes.Usage.CacheReadInputTokens, UsageNote: usageNoteFor(invRes.Usage, binding),
		CostUSD: invRes.Usage.CostUSD, CostUnavailableReason: invRes.Usage.CostUnavailableReason,
		SystemPromptBytes: invRes.SystemPromptBytes, PayloadBytes: invRes.PayloadBytes,
		Output: string(invRes.Stdout), LastMessage: verb.OutputTail(string(invRes.Stdout), g.scrubValues(binding.Env)),
	}
	g.logExecutorRun(retrospectAgent, item.ID, "retrospect", invRes.Stdout, invRes.Err)
	if invRes.Err != nil {
		return res, fmt.Errorf("%s agent failed on %s: %w", retrospectAgent, item.ID, invRes.Err)
	}
	return res, nil
}

// ChatPayload is the first-turn payload for a hand-opened live session: the
// same transitionPayload shape as a gate/executor dispatch, with from == to ==
// current status, plus messages[] for the chosen binding's address set —
// messages addressed to that role, or to "*" (verb.MessagesSince unions the
// wildcard). A message to another role is not delivered (sty_a0372443).
func (g *Engine) ChatPayload(ctx context.Context, item workitem.Item, binding string) (transitionPayload, error) {
	tp := transitionPayload{Story: item, From: item.Status, To: item.Status}
	if g.children != nil {
		tp.Children = g.children(ctx, item)
	}
	var mat reviewerMaterial
	g.fillPayloadDocs(ctx, item.ID, &tp, &mat)
	tp.docBodies = mat.Docs
	g.fillMessages(ctx, item.ID, []string{strings.TrimSpace(binding)}, &tp, nil)
	return tp, nil
}

// SessionSeed is the story payload a hand-opened live session should be given
// on its FIRST turn, as JSON — the same shape a dispatch receives (story, ACs,
// docs, children, and the messages addressed to that binding). A live session's
// command template need not carry {payload}, so a caller that must guarantee
// the agent saw the acceptance criteria sends this rather than assuming the
// transport did (sty_8e0b29a0).
func (g *Engine) SessionSeed(ctx context.Context, item workitem.Item, binding string) (string, error) {
	tp, err := g.ChatPayload(ctx, item, binding)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(tp)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// SessionRole says which CHARTER a live session opens under. It is a parameter
// rather than an inference from the binding name because the two are not the
// same question: a repo may name its coder binding anything, and a coder
// driving a rework relay must not be handed the consultant's "your reply is
// not a verdict" charter — nor the reverse (sty_8e0b29a0).
type SessionRole int

const (
	// SessionRoleDriving is a session that performs: the executor charter.
	SessionRoleDriving SessionRole = iota
	// SessionRoleConsult is a binding opened to be ASKED: its reply is context,
	// never a verdict ([[satelle-agent-consultation]]), and it does not move
	// status. Tools stay the binding's own grant, so a consulted reviewer keeps
	// its read-only grant (sty_a0372443).
	SessionRoleConsult
)

// OpenSessionAs opens a live Session for a named binding under the given
// charter role. in-loop / command / missing bindings return a clear error;
// they do not fall back to a guessed spawn (sty_1de7494c). onEvent, when
// non-nil, is installed as the request's OnEvent: transports call it inline
// from their reader goroutine BEFORE the lossy Events() fan-out, so a caller
// that must record every tool boundary (the relay transcript) hangs its ledger
// writer here, not on the drained channel.
func (g *Engine) OpenSessionAs(ctx context.Context, name string, role SessionRole, item workitem.Item, pol agentcli.PermissionPolicy, onEvent agentcli.EventHandler) (agentcli.Session, error) {
	return g.OpenSessionAsWithModel(ctx, name, role, item, pol, onEvent, "")
}

// OpenSessionAsWithModel is OpenSessionAs with an explicit per-open model
// override (sty_7069bced) — what `satelle story rework --model` feeds
// through, recorded with source=agent. Empty
// modelOverride behaves exactly as OpenSessionAs.
func (g *Engine) OpenSessionAsWithModel(ctx context.Context, name string, role SessionRole, item workitem.Item, pol agentcli.PermissionPolicy, onEvent agentcli.EventHandler, modelOverride string) (agentcli.Session, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("satelle: cannot open a live session: no binding named")
	}
	if g.namedAgents == nil {
		return nil, fmt.Errorf("no agents layer is wired — cannot open the %q session", name)
	}
	resolve := g.liveNamedAgents
	if resolve == nil {
		resolve = g.namedAgents
	}
	binding, found := resolve(name)
	if !found {
		return nil, fmt.Errorf("no [%s] binding in .satelle/workflows/agents.toml — define interface=acp or stream to open a live session", name)
	}
	if strings.TrimSpace(binding.Command) == "" {
		return nil, config.LiveCommandRequiredError(name)
	}
	if config.IsInLoopCommand(binding.CommandTemplate()) {
		return nil, fmt.Errorf("satelle: cannot open a live session: [%s] is in-loop — the hook channel remains the orchestrator; set interface=acp or stream to open a live session", name)
	}
	if binding.ResolvedInterface() == config.InterfaceCloud {
		return nil, fmt.Errorf("satelle: cannot open a live session: [%s] interface=cloud runs one-shot in a cloud session — rework and consult need interface=acp or stream", name)
	}
	// A driving session performs, so it is held to the same wiring guard a
	// one-shot performer dispatch is; a consult session is read-only.
	if role == SessionRoleDriving {
		if err := g.wiringGuard(ctx, name, binding.CommandTemplate(), item.ID); err != nil {
			return nil, err
		}
	}
	openerFn := g.newOpener
	if openerFn == nil {
		openerFn = agentcli.OpenerFromBinding
	}
	opener, err := openerFn(binding.ResolvedInterface(), binding.CommandTemplate())
	if err != nil {
		if errors.Is(err, agentcli.ErrNotLiveCapable) {
			return nil, fmt.Errorf("satelle: cannot open a live session: [%s] interface=%s is not live-capable — set interface=acp or stream", name, binding.ResolvedInterface())
		}
		return nil, err
	}
	payload, err := g.ChatPayload(ctx, item, name)
	if err != nil {
		return nil, err
	}
	charter := executorCharter(name, item.Status, "live session")
	if role == SessionRoleConsult {
		charter = consultCharter(name, config.ResolvedRole(name, binding), item.Status)
	}
	// Model selection (sty_7069bced): binding.Model wins outright when set;
	// otherwise modelOverride (an explicit --model flag on this open) is a
	// per-dispatch instruction, then the inherited/creator session tiers,
	// else cli-default.
	modelResolved, modelSource := g.selectModel(ctx, binding, item.ID, modelOverride, config.ModelSourceAgent)
	binding.Model = modelResolved
	// Every live session gets its own scratch dir (sty_e7aaf8b1 AC1), exactly
	// like a one-shot Invoke dispatch.
	scratchDir, screrr := newScratch(g.repoRoot, item.ID)
	if screrr != nil {
		return nil, screrr
	}
	// A live session runs on this machine, so a grant that can open a file is
	// sent the documents as paths into its scratch (sty_73a2dc68).
	var sent any = payload
	if readsDocsByPath(payload, binding.Tools) {
		p, derr := docsByPath(scratchDir, payload)
		if derr != nil {
			finishScratch(scratchDir, false)
			return nil, derr
		}
		sent = p
	}
	req, err := g.buildRequest(ctx, invocation{
		charter:    charter,
		payload:    sent,
		tools:      binding.Tools,
		model:      binding.Model,
		effort:     binding.Effort,
		settings:   binding.Settings,
		env:        binding.Env,
		principles: binding.ResolvedPrinciples(),
		scratch:    scratchDir,
	})
	if err != nil {
		finishScratch(scratchDir, false)
		return nil, err
	}
	// Reviewer tool isolation (sty_ef3efb51): a live session bound to a
	// role=reviewer binding (the rework consultant) takes the same
	// warn / trim / describe path as a gate verdict, in addition to the
	// caller's own policy, before any process starts.
	var isolation verb.ToolIsolation
	if config.ResolvedRole(name, binding) == config.RoleReviewer {
		runner, rerr := agentcli.RunnerFromBinding(binding.ResolvedInterface(), binding.CommandTemplate())
		if rerr != nil {
			finishScratch(scratchDir, false)
			return nil, rerr
		}
		isolation, _ = g.isolateReviewer(ctx, reviewerDispatch{
			StoryID: item.ID, Actor: name, Skill: "live-session", Step: item.Status, Section: name,
		}, runner, binding.OperatorAttested(), &req)
		pol = agentcli.ReviewerSessionPolicy(req.AllowedTools, pol)
	}
	// A live session ledgers its own open as an agent_invocation row
	// (sty_7069bced 4.2): the model chosen and why, so a live dispatch is as
	// inspectable in `satelle story cost` and the web timeline as a one-shot
	// one. model_resolved is "unavailable" at open — nothing has run yet; the
	// close row (liveModelSession.Close) reports what actually happened.
	openRow := map[string]any{
		"agent": name, "phase": "open", "model": binding.Model, "model_source": modelSource,
		"model_resolved": agentcli.ModelUnavailable, "usage_available": false,
		"system_prompt_bytes": len(req.SystemPrompt), "payload_bytes": len(req.Payload),
	}
	isolationFields(openRow, isolation)
	g.recordInvocation(ctx, item.ID, openRow)
	if req.Env == nil {
		req.Env = map[string]string{}
	}
	if sid := strings.TrimSpace(os.Getenv(config.SessionEnv)); sid != "" {
		req.Env[config.SessionEnv] = sid
	}
	// Every live session satelle itself opens — the rework relay's coder AND
	// consultant alike — is a dispatch, not the in-loop driver, even though it
	// inherits the driver's SATELLE_SESSION stamp above (sty_719c4a7b AC4): a
	// live session's own hooks otherwise see that inherited id with no
	// dispatch marker, take isDispatchedProcess() as false, and publish role
	// in-loop over the driver's own file. Marking it here — the one path both
	// SessionRoleDriving and SessionRoleConsult opens go through — matches
	// what agentstep.Invoke already does for a command/ACP ExpectPerform or
	// ExpectVerdict child.
	req.Env[config.DispatchAgentEnv] = name
	req.Env[config.DispatchStepEnv] = item.Status
	req.Env[config.DispatchItemEnv] = item.ID
	req.Env = overlayScratchEnv(req.Env, scratchDir)
	// A DRIVING session (the coder rework seat) may edit the tree,
	// so it is the one that can leave debris; snapshot the untracked files at
	// open so Close can tell what THIS session created (sty_e7aaf8b1 AC5).
	// Consult-role sessions are read-only and are never swept.
	var leftoverBefore map[string]bool
	sweepConfigured := len(g.leftoverRule.Patterns) > 0 || strings.TrimSpace(g.leftoverRule.ContentRegex) != ""
	if role == SessionRoleDriving && sweepConfigured {
		leftoverBefore, _ = untrackedSnapshot(g.repoRoot)
	}
	// usageTracker captures the session's LAST EventUsage (sty_7069bced) — the
	// same signal runOneShotUsage reads for a one-shot dispatch — so the close
	// row below can report what the live session actually resolved/spent, not
	// just what open intended. The caller's own onEvent still fires unchanged.
	tracker := &liveUsageTracker{scrub: g.scrubValues(binding.Env)}
	// A denied ask-the-user is ledgered as it happens (not at Close, so a
	// killed session keeps the row), exactly as a one-shot dispatch does
	// (sty_ff50f788). A consult session acts as its own binding; a driving one
	// as the executor.
	askActor := ""
	if role == SessionRoleConsult {
		askActor = name
	}
	req.OnEvent = tracker.wrap(func(ev agentcli.Event) {
		if ev.Kind == agentcli.EventInteractiveDenied {
			g.ledgerInteractiveDeniedFor(ctx, item.ID, askActor, name, "", ev)
		}
		if onEvent != nil {
			onEvent(ev)
		}
	})
	sess, err := opener(ctx, req, pol)
	if err != nil {
		finishScratch(scratchDir, false)
		return nil, err
	}
	return &liveModelSession{Session: sess, engine: g, ctx: ctx, storyID: item.ID, agent: name,
		model: binding.Model, modelSource: modelSource, tracker: tracker, performer: role == SessionRoleDriving,
		scratch: scratchDir, repoRoot: g.repoRoot, sweepBefore: leftoverBefore}, nil
}

// liveUsageTracker accumulates every EventUsage a live session reports across
// its turns, so its close row carries the SUM of the session — a Claude
// stream-json `result` usage is per turn, not cumulative, so keeping only the
// last turn understates a multi-turn session (sty_363eaf55 AC2). Guarded:
// EventHandler may be called from concurrent stdout/stderr reader goroutines.
type liveUsageTracker struct {
	mu    sync.Mutex
	usage agentcli.UsageResult
	turns int
	// splitMissing latches when any turn's provider reported no cache split.
	splitMissing bool
	// last is the tail of the most recent EventMessage's text: what the session
	// last said, stamped on its close row as last_message.
	last string
	// scrub holds the values satelle resolved into the session's environment,
	// which last never carries (verb.OutputTail).
	scrub map[string]string
}

// wrap returns an EventHandler that folds each EventUsage into the running
// total and then forwards to next (which may be nil) unchanged — the
// caller's own handler still sees every event.
func (t *liveUsageTracker) wrap(next agentcli.EventHandler) agentcli.EventHandler {
	return func(ev agentcli.Event) {
		if ev.Kind == agentcli.EventMessage && ev.Text != "" {
			t.mu.Lock()
			t.last = verb.OutputTail(ev.Text, t.scrub)
			t.mu.Unlock()
		}
		if ev.Kind == agentcli.EventUsage && ev.Usage != nil {
			t.mu.Lock()
			t.turns++
			u := ev.Usage
			if u.Adapter != "" {
				t.usage.Adapter = u.Adapter
			}
			t.usage.Available = t.usage.Available || u.Available
			t.usage.InputTokens += u.InputTokens
			t.usage.OutputTokens += u.OutputTokens
			t.usage.TotalTokens += u.TotalTokens
			t.usage.FreshInputTokens += u.FreshInputTokens
			t.usage.CacheCreationInputTokens += u.CacheCreationInputTokens
			t.usage.CacheReadInputTokens += u.CacheReadInputTokens
			// The split is a measurement only while every turn reported one.
			if !u.CacheSplitAvailable {
				t.splitMissing = true
			}
			t.usage.CacheSplitAvailable = !t.splitMissing
			if u.UnavailableReason != "" {
				t.usage.UnavailableReason = u.UnavailableReason
			}
			t.usage.Duration += u.Duration
			if u.ModelResolved != "" {
				t.usage.ModelResolved = agentcli.KeepModel(t.usage.ModelResolved, u.ModelResolved)
			}
			// Cost sums only the turns that reported one; an all-nil sum stays
			// nil rather than becoming a measured zero (mirrors addUsage).
			if u.CostUSD != nil {
				sum := *u.CostUSD
				if t.usage.CostUSD != nil {
					sum += *t.usage.CostUSD
				}
				t.usage.CostUSD = &sum
				t.usage.CostUnavailableReason = ""
			} else if t.usage.CostUSD == nil {
				t.usage.CostUnavailableReason = u.CostUnavailableReason
			}
			t.mergeModelsLocked(u.Models)
			t.mu.Unlock()
		}
		if next != nil {
			next(ev)
		}
	}
}

// mergeModelsLocked folds one turn's per-model usage into the running total,
// merged by model id in a deterministic (sorted) order — same determinism
// rule as agentcli.parseModelUsage, so Go's randomized map order can never
// affect the recorded result. Caller holds t.mu.
func (t *liveUsageTracker) mergeModelsLocked(models []agentcli.ModelUsage) {
	if len(models) == 0 {
		return
	}
	byID := make(map[string]agentcli.ModelUsage, len(t.usage.Models)+len(models))
	for _, m := range t.usage.Models {
		byID[m.ID] = m
	}
	for _, m := range models {
		cur := byID[m.ID]
		cur.ID = m.ID
		cur.InputTokens += m.InputTokens
		cur.OutputTokens += m.OutputTokens
		cur.CacheCreationInputTokens += m.CacheCreationInputTokens
		cur.CacheReadInputTokens += m.CacheReadInputTokens
		if m.CostUSD != nil {
			sum := *m.CostUSD
			if cur.CostUSD != nil {
				sum += *cur.CostUSD
			}
			cur.CostUSD = &sum
		}
		byID[m.ID] = cur
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	merged := make([]agentcli.ModelUsage, 0, len(ids))
	for _, id := range ids {
		merged = append(merged, byID[id])
	}
	t.usage.Models = merged
}

// total returns the accumulated usage across every turn and the turn count.
func (t *liveUsageTracker) total() (agentcli.UsageResult, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usage, t.turns
}

// lastMessage is the tail of the session's most recent message ("" when it sent none).
func (t *liveUsageTracker) lastMessage() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}

// liveModelSession decorates agentcli.Session to ledger a close row
// (sty_7069bced): what the live session actually resolved/spent, mirroring
// the open row OpenSessionAsWithModel already writes. Every other method
// delegates unchanged via the embedded Session.
type liveModelSession struct {
	agentcli.Session
	engine             *Engine
	ctx                context.Context
	storyID, agent     string
	model, modelSource string
	tracker            *liveUsageTracker
	performer          bool // a driving (coder) seat, not a consultant
	closeOnce          sync.Once
	// scratch/repoRoot/sweepBefore back the scratch-dir lifecycle and leftover
	// sweep this session owns at Close (sty_e7aaf8b1). sweepBefore is nil for a
	// consult-role session or when no leftover rule is configured — either way
	// Close sweeps nothing.
	scratch     string
	repoRoot    string
	sweepBefore map[string]bool
}

func (s *liveModelSession) Close() error {
	err := s.Session.Close()
	s.closeOnce.Do(func() {
		u, turns := s.tracker.total()
		modelResolved, models := toVerbModels(u)
		data := map[string]any{
			"agent": s.agent, "phase": "close", "model": s.model, "model_source": s.modelSource,
			"model_resolved": modelResolved, "usage_available": u.Available,
			"tokens_in": u.InputTokens, "tokens_out": u.OutputTokens, "tokens_total": u.TotalTokens,
			"tokens_in_fresh": u.FreshInputTokens, "tokens_cache_write": u.CacheCreationInputTokens, "tokens_cache_read": u.CacheReadInputTokens,
			"duration_ms": u.Duration.Milliseconds(),
			"turns":       turns,
		}
		if s.performer {
			// The relay's coder seat is a performer: a drive that parks over its
			// relay quotes this as the performer's last message. A consultant's
			// close row is a reviewer's and carries none.
			data["last_message"] = s.tracker.lastMessage()
		}
		if !u.Available && u.UnavailableReason == "" {
			u.UnavailableReason = s.agent + " adapter: session reported no usage event"
		}
		addUsageNote(data, u)
		addCostNote(data, u)
		if len(models) > 0 {
			data["model_usage"] = models
		}
		s.engine.recordInvocation(s.ctx, s.storyID, data)

		var swept []string
		var sweepFailed bool
		if s.sweepBefore != nil {
			files, serr := SweepLeftovers(s.repoRoot, s.scratch, s.sweepBefore, s.engine.leftoverRule)
			if serr != nil {
				// A partial move before this error must not be lost: SweepLeftovers
				// still reports every matched file it attempted, and the scratch dir
				// below is kept — never deleted — on this path (sty_e7aaf8b1 AC5).
				sweepFailed = true
				s.engine.recordInvocation(s.ctx, s.storyID, map[string]any{
					"phase": "leftovers_failed", "scratch_dir": s.scratch, "agent": s.agent,
					"files": files, "error": serr.Error(),
				})
			} else if len(files) > 0 {
				swept = files
				s.engine.ledgerLeftovers(s.ctx, s.storyID, s.scratch, s.engine.leftoverRule.ResolveAction(), files)
			}
		}
		keep := err != nil || sweepFailed || (len(swept) > 0 && s.engine.leftoverRule.ResolveAction() == config.LeftoverActionMove)
		if err != nil {
			s.engine.recordInvocation(s.ctx, s.storyID, map[string]any{
				"phase": "scratch_kept", "scratch_dir": s.scratch, "agent": s.agent,
			})
		}
		finishScratch(s.scratch, keep)
	})
	return err
}

// setDecisionUsage copies an invocation's token/wall-time cost onto a gate
// decision so the verb layer can record it on the agent_invocation entry — the
// per-gate cost the observability view rolls up (sty_a699ad14). model is the
// effective model used for this run (binding/override), not only the engine cache.
func (g *Engine) setDecisionUsage(d *verb.GateDecision, u agentcli.UsageResult, model string) {
	d.TokensIn, d.TokensOut, d.TokensTotal = u.InputTokens, u.OutputTokens, u.TotalTokens
	d.TokensInFresh, d.TokensCacheWrite, d.TokensCacheRead =
		u.FreshInputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens
	d.DurationMs = u.Duration.Milliseconds()
	d.UsageAvailable = u.Available
	d.UsageNote = usageNote(u)
	d.CostUSD, d.CostUnavailableReason = u.CostUSD, u.CostUnavailableReason
	if model != "" {
		d.Model = model
	} else {
		d.Model = g.model
	}
	d.ModelResolved, d.Models = toVerbModels(u)
}

// usageNote derives the explicit-unavailable evidence for one invocation's
// usage (sty_c8d45201): the adapter-named reason when usage is unavailable, and
// the unreported-cache-split marker when the provider named no cache fields.
// An unavailable usage with no adapter reason (a path that bypassed
// runOnceBusy) still records a reason — never a bare unexplained false.
func usageNote(u agentcli.UsageResult) verb.UsageNote {
	n := verb.UsageNote{Adapter: u.Adapter, CacheSplitUnavailable: !u.CacheSplitAvailable}
	if !u.Available {
		n.UsageUnavailableReason = u.UnavailableReason
		if n.UsageUnavailableReason == "" {
			n.UsageUnavailableReason = "transport reported no usage"
		}
	}
	return n
}

// usageNoteFor is usageNote plus the auth mode the binding behind the run
// declares. The auth is the binding's configuration, not something the run
// measured, so it comes from the binding and never from the usage.
func usageNoteFor(u agentcli.UsageResult, b config.AgentBinding) verb.UsageNote {
	n := usageNote(u)
	n.AuthMode = b.ResolvedAuth()
	return n
}

// addUsageNote stamps usageNote's fields onto a map-shaped ledger row.
func addUsageNote(data map[string]any, u agentcli.UsageResult) {
	n := usageNote(u)
	if n.Adapter != "" {
		data["adapter"] = n.Adapter
	}
	if n.UsageUnavailableReason != "" {
		data["usage_unavailable_reason"] = n.UsageUnavailableReason
	}
	if n.CacheSplitUnavailable {
		data["cache_split_unavailable"] = true
	}
}

// addCostNote stamps a map-shaped ledger row with the invocation's dollar
// cost when the provider reported one, else the adapter-named reason it
// did not — never a zero (sty_c4df7376).
func addCostNote(data map[string]any, u agentcli.UsageResult) {
	if u.CostUSD != nil {
		data["cost_usd"] = *u.CostUSD
		return
	}
	if u.CostUnavailableReason != "" {
		data["cost_unavailable_reason"] = u.CostUnavailableReason
	}
}

// toVerbModels converts a transport's resolved-model usage into the verb
// package's own types, normalizing an unreported resolution to
// agentcli.ModelUnavailable HERE — the single point where a new write can
// never land empty (sty_87b86044): every population site (gate decisions,
// dispatch results, the summariser) funnels through this one helper.
func toVerbModels(u agentcli.UsageResult) (string, []verb.ModelUsage) {
	resolved := u.ModelResolved
	if resolved == "" {
		resolved = agentcli.ModelUnavailable
	}
	if len(u.Models) == 0 {
		return resolved, nil
	}
	models := make([]verb.ModelUsage, len(u.Models))
	for i, m := range u.Models {
		models[i] = verb.ModelUsage{
			ID: m.ID, TokensIn: m.InputTokens, TokensOut: m.OutputTokens,
			TokensCacheWrite: m.CacheCreationInputTokens, TokensCacheRead: m.CacheReadInputTokens,
			CostUSD: m.CostUSD,
		}
	}
	return resolved, models
}

// isNamedPerformer reports whether a workflow node agent=<name> should run as a
// performing dispatch (ExpectPerform). The built-in DSL tokens are filtered
// before this is called. A named binding whose resolved role is reviewer is a
// judge, not a performer (design §9 (a) / sty_e21cbc08).
func isNamedPerformer(section string, b config.AgentBinding) bool {
	if section == "" || section == "executor" || section == "reviewer" {
		return false
	}
	return config.ResolvedRole(section, b) != config.RoleReviewer
}

// dispatchSink opens a per-dispatch normalized event log under
// <data_dir>/logs/dispatch/ so an operator can `tail -f` provider-neutral,
// sanitized progress while it runs. Returns a nil writer, empty path, and nil
// closer when no log dir is configured (most engine tests) or the file cannot be
// created, so streaming degrades to a no-op rather than failing the dispatch —
// best-effort, like the executor/reviewer logs. The caller must invoke the
// returned closer (when non-nil) once the dispatch finishes.
func (g *Engine) dispatchSink(agent, itemID string) (io.Writer, string, func()) {
	if g.logDir == "" {
		return nil, "", nil
	}
	dir := filepath.Join(g.logDir, "dispatch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", nil
	}
	path := filepath.Join(dir, logsread.FormatDispatchName(agent, itemID, time.Now().UnixNano()))
	f, err := os.Create(path)
	if err != nil {
		return nil, "", nil
	}
	return f, path, func() { _ = f.Close() }
}

// dispatchLog is one dispatch's log under <data_dir>/logs/dispatch/: the run's
// events as they arrive, and around them what the engine writes itself — the
// input it sent before the run, a closing summary after it (sty_2b89feab).
// Every line is scrubbed of the secrets resolved into the dispatch's
// environment before it is written. A nil *dispatchLog writes nothing.
type dispatchLog struct {
	mu      sync.Mutex
	w       io.Writer
	secrets map[string]string
}

// openDispatchLog opens the log of agent's dispatch for itemID, scrubbing the
// values binding resolved into its environment. With no log dir, or a file that
// cannot be created, the log is nil and the dispatch runs unchanged. The closer
// is never nil.
func (g *Engine) openDispatchLog(agent, itemID string, binding config.AgentBinding) (*dispatchLog, string, func()) {
	w, path, closeFn := g.dispatchSink(agent, itemID)
	if w == nil {
		return nil, "", func() {}
	}
	return &dispatchLog{w: w, secrets: g.scrubValues(binding.Env)}, path, closeFn
}

// addSecrets widens the scrub to env's values — the binding a secondary
// failover runs under, or the reviewer env a binding without its own falls back to.
func (l *dispatchLog) addSecrets(env map[string]string) {
	if l == nil || len(env) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, v := range env {
		if _, ok := l.secrets[k]; !ok {
			l.secrets[k] = v
		}
	}
}

// write formats ev and appends it, scrubbed.
func (l *dispatchLog) write(ev agentcli.Event) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ev.Text = verb.RedactEnv(ev.Text, l.secrets)
	ev.Error = verb.RedactEnv(ev.Error, l.secrets)
	line := agentcli.RedactSecrets(verb.RedactEnv(agentcli.FormatEvent(ev), l.secrets))
	_, _ = io.WriteString(l.w, line)
}

// handler is the OnEvent that writes the run's events to the log; nil for a nil log.
func (l *dispatchLog) handler() agentcli.EventHandler {
	if l == nil {
		return nil
	}
	return l.write
}

// input writes one named block of what the dispatch was sent.
func (l *dispatchLog) input(block, text string) {
	l.write(agentcli.Event{Kind: agentcli.EventInput, Text: text, Meta: map[string]string{agentcli.EventMetaBlock: block}})
}

// summary closes the log with the run's progress and final answer.
func (l *dispatchLog) summary(s dispatchSummary) {
	l.write(agentcli.Event{Kind: agentcli.EventSummary, Text: s.String()})
}

// logExecutorRun appends a named-agent run's output (or failure) to
// <data_dir>/logs/executor.log — the run log that makes an isolated executor's
// work reviewable (sty_fd427546). Best-effort, like the reviewer log.
func (g *Engine) logExecutorRun(agent, itemID, state string, out []byte, runErr error) {
	if g.logDir == "" {
		return
	}
	now := time.Now()
	status := "ok"
	if runErr != nil {
		status = "error: " + runErr.Error()
	}
	line := fmt.Sprintf("%s\t%s\t%s\tstate %s\t%s — output: %s",
		now.UTC().Format(time.RFC3339), agent, itemID, state, status,
		strings.ReplaceAll(strings.TrimSpace(string(out)), "\n", "\\n"))
	_ = logfile.Append(now, filepath.Join(g.logDir, "executor.log"), g.logCfg, line)
}

// engagementSkillCheck is the synthetic reviewer name recorded when the
// deterministic engagement guard blocks because the active workflow's path to
// done has an executor step whose skill does not resolve.
const engagementSkillCheck = "satelle-workflow-skill-check"

// guardEngagementExecutorSkills is the fast, agent-free completability guard run
// on the ENGAGEMENT edge (leaving the workflow's start state for a non-cancel
// target). It resolves every EXECUTOR-step skill on the active workflow's path to
// done and returns blocked=true, naming any that do not resolve in the substrate
// (embedded ∪ project). It returns blocked=false to proceed: off the engagement
// edge, when the workflow is not parseable DOT, or when every executor skill
// resolves. A docs lookup error other than not-found is surfaced.
func (g *Engine) guardEngagementExecutorSkills(ctx context.Context, item workitem.Item, toStatus string) (verb.GateDecision, bool, error) {
	spec, _, err := g.activeSpec(ctx, item)
	if err != nil {
		if ungoverned(err) {
			return verb.GateDecision{}, false, nil
		}
		return verb.GateDecision{}, false, err
	}
	if item.Status != spec.Start() || toStatus == "cancelled" {
		return verb.GateDecision{}, false, nil // not the engagement edge
	}
	// Tag-filtered: a missing surface-scoped augmentation blocks only stories
	// whose tags match it (sty_8225d8a5); structure validate still requires all.
	var missing []string
	for _, name := range spec.ExecutorPathToDoneSkillsFor(item.Tags) {
		if _, gerr := g.docs.Get(ctx, "skills", name); gerr != nil {
			if errors.Is(gerr, docindex.ErrNotFound) {
				missing = append(missing, name)
				continue
			}
			return verb.GateDecision{}, false, gerr
		}
	}
	if len(missing) == 0 {
		return verb.GateDecision{}, false, nil
	}
	notes := fmt.Sprintf(
		"cannot engage: the active workflow's path to done has an executor step whose skill does not resolve in the substrate — %s. Author it under .satelle/skills (or embed it), or remove the step, before starting.",
		strings.Join(missing, ", "))
	dec := verb.GateDecision{Gated: true, Accept: false, Skill: engagementSkillCheck, Notes: notes}
	dec.Reviewers = append(dec.Reviewers, verb.ReviewerVerdict{
		Skill: engagementSkillCheck, Order: 0, Accept: false, Notes: notes, System: true,
	})
	return dec, true, nil
}

// retryWait backs off before a retry (attempt > 1) so transient contention can
// clear, aborting if the context is cancelled. Returns ctx.Err() on cancellation,
// else nil. The single source of the bounded-backoff step (sty_d71b0791), shared by
// the reviewer gate loop and the step summariser so both retry a transient the same
// way. attempt == 1 waits not at all.
func (g *Engine) retryWait(ctx context.Context, attempt int) error {
	if attempt <= 1 {
		return nil
	}
	wait := time.Duration(0)
	if g.backoff != nil {
		wait = g.backoff(attempt)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// runReviewer runs ONE reviewer skill over item's transition and returns its
// verdict. A skill carrying a functional check runs deterministically; otherwise
// the skill body rides as an isolated LLM reviewer's system prompt. Gated=false
// when the skill's rubric is not installed (advisory — keeps fresh repos working).
// gateAgent is the agents.toml section that runs this gate (agent=<name>); empty
// or "reviewer" resolves [reviewer] (sty_a476a2f8).

// gateBinding resolves the agents.toml section that runs a gate (sty_a476a2f8).
// Empty or "reviewer" → g.reviewerBinding. Any other name requires namedAgents
// and role=reviewer (enforced by the caller).
func (g *Engine) gateBinding(section string) (config.AgentBinding, string, error) {
	section = strings.TrimSpace(section)
	if section == "" || section == "reviewer" {
		return g.reviewerBinding, "reviewer", nil
	}
	if g.namedAgents == nil {
		return config.AgentBinding{}, section, fmt.Errorf(
			"gate refused: agent=%s but no named-agent resolver is configured", section)
	}
	b, ok := g.namedAgents(section)
	if !ok {
		return config.AgentBinding{}, section, fmt.Errorf(
			"gate refused: agent=%s has no [%s] binding in agents.toml", section, section)
	}
	return b, section, nil
}

func (g *Engine) runReviewer(ctx context.Context, item workitem.Item, toStatus, skill, gateAgent string) (verb.GateDecision, error) {
	return g.runReviewerWith(ctx, item, toStatus, skill, gateAgent, nil)
}

// runReviewerWith is runReviewer with an optional payload decorator, for a gate
// that judges something the item alone does not carry — today the amendment's
// before/after (sty_81aa4d8f). Everything else about the run is identical, so a
// decorated gate is not a second reviewer path. Hook call sites pass no panel
// and stay single-seat.
func (g *Engine) runReviewerWith(ctx context.Context, item workitem.Item, toStatus, skill, gateAgent string, decorate func(*transitionPayload)) (verb.GateDecision, error) {
	return g.runReviewerRef(ctx, item, toStatus, reviewerRef{skill: skill, agent: gateAgent}, decorate)
}

// runReviewerRef runs one skill. An empty panel, and a one-seat panel with no
// combine, are the single-seat path. A panel of two or more seats, or a panel
// that names a combine skill, runs the skill once per seat and folds through
// that check. The fold returns one verdict for the skill.
func (g *Engine) runReviewerRef(ctx context.Context, item workitem.Item, toStatus string, ref reviewerRef, decorate func(*transitionPayload)) (verb.GateDecision, error) {
	skill := ref.skill
	agent := ref.agent
	fold := panelFolds(ref)
	if len(ref.panel) == 1 && !fold {
		agent = ref.panel[0]
	}
	prep, err := g.prepareReviewer(ctx, item, toStatus, skill, agent, withReviewMode(ref.mode, decorate))
	if err != nil {
		return verb.GateDecision{}, err
	}
	if prep.unresolved {
		return verb.GateDecision{Gated: false, Skill: skill, Unresolved: []string{skill}}, nil
	}
	if !fold {
		dec, derr := g.decideReviewer(ctx, item, toStatus, skill, agent, prep)
		if dec.Gated {
			dec.ReviewMode = ref.mode
		}
		return dec, derr
	}
	if command := skillCheck(prep.body); command != "" {
		return verb.GateDecision{Gated: true, Skill: skill}, fmt.Errorf(
			"gate refused: reviewer skill %q carries a functional check and also sits on panel %v — the check is that skill's one decision", skill, ref.panel)
	}
	command, cerr := g.combineCommand(ctx, ref.combine)
	if cerr != nil {
		return verb.GateDecision{Gated: true, Skill: skill}, cerr
	}
	dec, ferr := g.runPanel(ctx, item, toStatus, skill, ref, command, prep)
	if dec.Gated {
		dec.ReviewMode = ref.mode
	}
	return dec, ferr
}

// panelFolds reports whether this ref expands into seat runs and a combine
// check. A one-seat panel with combine omitted is the single-binding shape.
func panelFolds(ref reviewerRef) bool {
	if len(ref.panel) == 0 {
		return false
	}
	if len(ref.panel) == 1 && strings.TrimSpace(ref.combine) == "" {
		return false
	}
	return true
}

// decideReviewer is the single-seat path: a check fence runs once through
// runCheck, otherwise one reviewerSeatFor plus one Invoke with ExpectVerdict.
func (g *Engine) decideReviewer(ctx context.Context, item workitem.Item, toStatus, skill, gateAgent string, prep reviewerPrep) (verb.GateDecision, error) {
	// Functional-check gate: when the skill carries a check — an embedded ```check
	// script block in its body, or a single-line `check:` in frontmatter — the
	// gate is deterministic. The check is SELF-CONTAINED in the skill (it never
	// references an external script); satelle runs it in the repo root with the
	// transition payload on stdin, exit 0 accepts, non-zero rejects with the
	// output tail as notes. No LLM (the command IS the decision). This is the
	// constitution's "skill + functional check" gate. Stays OUTSIDE Invoke (design §4.2).
	if command := skillCheck(prep.body); command != "" {
		return g.runCheck(ctx, item, skill, command, string(prep.payload), structure.CheckReuse(prep.body)), nil
	}
	// LLM path: shared Invoke (sty_ba860c8a / sty_e21cbc08). Pre-flight (skill,
	// structure, functional-check, missing-rubric advisory) stays above.
	seat, serr := g.reviewerSeatFor(ctx, item, toStatus, skill, gateAgent)
	if serr != nil {
		return verb.GateDecision{Gated: true, Skill: skill}, serr
	}
	res := g.invokeReviewerSeat(ctx, item, toStatus, skill, prep, seat)
	if res.Err != nil {
		return verb.GateDecision{Gated: true, Skill: skill}, g.gateFailure(skill, res, seat)
	}
	if res.Decision == nil {
		return verb.GateDecision{Gated: true, Skill: skill}, fmt.Errorf(
			"reviewer: %s produced no decision", skill)
	}
	stampInvocation(res.Decision, res, seat)
	prep.criteria.apply(res.Decision)
	prep.keepWaived(res.Decision)
	return *res.Decision, nil
}

// invokeReviewerSeat is the cold one-shot Invoke shared by the single-seat path
// and each panel seat. The payload is the prepared transition, never another
// seat's stdout.
func (g *Engine) invokeReviewerSeat(ctx context.Context, item workitem.Item, toStatus, skill string, prep reviewerPrep, seat reviewerSeat) InvokeResult {
	defer closeSeat(seat)
	dlog, _, closeLog := g.openDispatchLog(seat.section, item.ID, seat.binding)
	defer closeLog()
	return g.Invoke(ctx, InvokeRequest{
		Binding:     seat.binding,
		Section:     seat.section,
		Rubric:      prep.body,
		Payload:     prep.tp,
		Material:    &prep.material,
		Charter:     reviewerCharter(),
		Expect:      ExpectVerdict,
		Timeout:     g.agentTimeout,
		IdleTimeout: seat.idle,
		Runner:      seat.runner,
		OnEvent:     dlog.handler(),
		Log:         dlog,
		Attempts:    g.attempts,
		StoryID:     item.ID,
		Step:        toStatus,
		Skill:       skill,
		Actor:       seat.section,
	})
}

// panelSeatRun is one declared seat's record plus the invocation stamp, if the
// process started. A seat that never started has an empty Command.
type panelSeatRun struct {
	record verb.PanelSeat
	stamp  verb.ReviewerVerdict
}

// combineCommand resolves the fold check. A missing skill or a skill with no
// check command is a skill-level error, never an accept.
func (g *Engine) combineCommand(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("panel combine skill is empty")
	}
	body, err := g.skillBody(ctx, name)
	if err != nil {
		return "", fmt.Errorf("panel combine skill %q: %w", name, err)
	}
	command := skillCheck(body)
	if command == "" {
		return "", fmt.Errorf("panel combine skill %q has no check command", name)
	}
	return command, nil
}

// runPanelSeats runs the reviewer once per declared section, in order, under the
// edge's parallel cap. A seat that cannot start or whose output does not parse
// is an unavailable record; the loop continues. It is not a skill-level error.
func (g *Engine) runPanelSeats(ctx context.Context, item workitem.Item, toStatus, skill string, panel []string, prep reviewerPrep, cap int) []panelSeatRun {
	runs := make([]panelSeatRun, len(panel))
	// attempt is the edge's next verdict number over the FULL history, the scale
	// fillPriorVerdicts numbers priors on; every seat of this round shares it.
	attempt := len(prep.material.PriorVerdicts) + 1
	one := func(i int, section string) {
		defer func() { runs[i].record.Attempt = attempt }()
		if ctx.Err() != nil {
			runs[i] = panelSeatRun{record: verb.PanelSeat{Seat: section, Accept: false, Unavailable: true, Notes: ctx.Err().Error()}}
			return
		}
		seat, serr := g.reviewerSeatFor(ctx, item, toStatus, skill, section)
		if serr != nil {
			runs[i] = panelSeatRun{record: verb.PanelSeat{Seat: section, Accept: false, Unavailable: true, Notes: serr.Error()}}
			return
		}
		sp := seatPrep(prep, skill, section)
		res := g.invokeReviewerSeat(ctx, item, toStatus, skill, sp, seat)
		if res.Err != nil || res.Decision == nil {
			notes := "reviewer produced no decision"
			if res.Err != nil {
				notes = res.Err.Error()
			}
			runs[i] = panelSeatRun{
				record: verb.PanelSeat{
					Seat: section, Accept: false, Unavailable: true,
					MaxTurns: errors.Is(res.Err, agentcli.ErrMaxTurns), Notes: notes,
				},
				stamp: g.seatStamp(skill, section, res, nil, seat),
			}
			return
		}
		stampInvocation(res.Decision, res, seat)
		sp.criteria.apply(res.Decision)
		runs[i] = panelSeatRun{
			record: verb.PanelSeat{
				Seat: section, Accept: res.Decision.Accept, Notes: res.Decision.Notes,
				Reviewed: res.Decision.Reviewed, ReviewedTruncated: res.Decision.ReviewedTruncated,
				Criteria: res.Decision.Criteria,
				Coverage: res.Decision.Coverage,
			},
			stamp: g.seatStamp(skill, section, res, res.Decision, seat),
		}
	}
	if cap <= 1 {
		for i, section := range panel {
			one(i, section)
		}
		return runs
	}
	if cap > len(panel) {
		cap = len(panel)
	}
	sem := make(chan struct{}, cap)
	var wg sync.WaitGroup
	for i, section := range panel {
		wg.Add(1)
		go func(i int, section string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				runs[i] = panelSeatRun{record: verb.PanelSeat{Seat: section, Accept: false, Unavailable: true, Notes: ctx.Err().Error(), Attempt: attempt}}
				return
			}
			one(i, section)
		}(i, section)
	}
	wg.Wait()
	return runs
}

// seatStamp is the invocation evidence for a seat whose process started.
// Command empty means it never started, and the transition writer skips it.
func (g *Engine) seatStamp(skill, section string, res InvokeResult, dec *verb.GateDecision, seat reviewerSeat) verb.ReviewerVerdict {
	if res.Command == "" && (dec == nil || dec.Command == "") {
		return verb.ReviewerVerdict{}
	}
	var rv verb.ReviewerVerdict
	if dec != nil {
		rv = reviewerVerdictOf(skill, 0, false, *dec)
	} else {
		// A started seat that produced no verdict: its measured usage, turns and
		// tool calls, and why it ended (turn cap or otherwise).
		rv = g.failureStamp(skill, res, seat)
	}
	rv.Seat = section
	rv.SeatStamps = nil
	rv.Seats = nil
	rv.Combine = ""
	return rv
}

// gateFailure is the error a single-seat gate returns for an attempt that
// produced no verdict. An attempt that started carries its invocation stamp, so
// the round writer can record what it measured; one that never started is the
// bare error.
func (g *Engine) gateFailure(skill string, res InvokeResult, seat reviewerSeat) error {
	if res.Command == "" {
		return res.Err
	}
	return &verb.GateFailure{Err: res.Err, Stamps: []verb.ReviewerVerdict{g.failureStamp(skill, res, seat)}}
}

// foldPanel runs the combine check over the seat records and returns one
// skill-level verdict. It does not use runCheck: that wrapper would replace the
// check's own notes. Command stays empty so the fold is not a harness invocation.
func (g *Engine) foldPanel(ctx context.Context, skill, combine, command string, panel []string, runs []panelSeatRun) (verb.GateDecision, error) {
	type combineSeat struct {
		Seat        string `json:"seat"`
		Accept      bool   `json:"accept"`
		Unavailable bool   `json:"unavailable"`
		Notes       string `json:"notes"`
		// Criteria is the seat's per-criterion entries when it recorded any;
		// Accept is then its effective decision, derived from them.
		Criteria []verb.CriterionVerdict `json:"criteria,omitempty"`
	}
	seats := make([]combineSeat, 0, len(panel))
	records := make([]verb.PanelSeat, 0, len(panel))
	var stamps []verb.ReviewerVerdict
	var perSeat [][]verb.CriterionVerdict
	for _, run := range runs {
		seats = append(seats, combineSeat{
			Seat: run.record.Seat, Accept: run.record.Accept,
			Unavailable: run.record.Unavailable, Notes: run.record.Notes,
			Criteria: run.record.Criteria,
		})
		perSeat = append(perSeat, run.record.Criteria)
		records = append(records, run.record)
		if run.stamp.Command != "" {
			stamps = append(stamps, run.stamp)
		}
	}
	payload, err := json.Marshal(struct {
		Seats    []combineSeat `json:"seats"`
		Declared int           `json:"declared"`
		// CriteriaCombined tallies the seats' entries per criterion; absent when
		// no seat recorded any.
		CriteriaCombined []criterionCombine `json:"criteria_combined,omitempty"`
	}{Seats: seats, Declared: len(panel), CriteriaCombined: combineCriteria(perSeat)})
	if err != nil {
		return verb.GateDecision{Gated: true, Skill: skill}, err
	}
	timeout := g.checkTimeout
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, cerr := g.check(cctx, g.repoRoot, command, string(payload))
	notes := strings.TrimSpace(out)
	accept := cerr == nil
	if !accept && notes == "" {
		notes = cerr.Error()
	}
	return verb.GateDecision{
		Gated: true, Skill: skill, Accept: accept, Notes: notes,
		Combine: combine, Seats: records, SeatStamps: stamps,
	}, nil
}

// outputTail returns a short, trimmed tail of a reviewer's last output for an
// error message — empty when there was none, so a runner error (no output) and a
// no-verdict output are both reported clearly.
func outputTail(out []byte) string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return ""
	}
	const max = 300
	if len(s) > max {
		s = "…" + s[len(s)-max:]
	}
	return " — last output: " + s
}

// scopedReviewers returns the active workflow's DECLARED scoped reviewers for the
// transition into toStatus — edge-less reviewer nodes whose `on=` attribute lists
// toStatus (or "*"), excluding any already named on the edge. Each entry carries
// the skill and optional node model= override (sty_19456622). This replaces the
// old reviewer:always skill-tag scan: the scope is declared in the workflow DOT,
// not inferred from a skill's frontmatter tag, so the workflow is the SOLE gating
// authority (sty_ca9f675f). A workflow that is not parseable DOT (the inline-YAML
// grammar) has no scoped-node concept and contributes none. A resolution failure
// degrades to none — scoped reviewers are additive and must never break the
// workflow's own edge gating.
func (g *Engine) scopedReviewers(ctx context.Context, item workitem.Item, toStatus string, exclude []string) ([]reviewerRef, error) {
	spec, _, err := g.activeSpec(ctx, item)
	if err != nil {
		if ungoverned(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []reviewerRef
	// item.Tags decide whether a surface-scoped node is ENQUEUED (sty_c6d093c8).
	// Skipped applies_to filters leave a telemetry artifact so a silent skip is
	// not identical to "no such surface gate" (sty_dcce86d5).
	enqueued, skipped := spec.ScopedReviewersSplit(toStatus, item.Tags)
	for _, s := range enqueued {
		if containsStr(exclude, s.Skill) {
			continue
		}
		if s.When != "" && !g.whenAllows(ctx, item, toStatus, s) {
			continue
		}
		out = append(out, reviewerRef{skill: s.Skill, agent: s.Agent, panel: s.Panel, combine: s.Combine, mode: s.ReviewMode})
	}
	for _, s := range skipped {
		g.telemetryEvent(ctx, item.ID, "reviewer", "scoped-gate-skipped", map[string]any{
			"skill": s.Skill, "to": toStatus, "reason": "applies_to",
			"tags": item.Tags,
		})
	}
	return out, nil
}

// whenAllows evaluates a scoped gate's `when` precondition: it runs the named
// functional-check skill's ```check in the repo root with the transition payload
// on stdin, exactly as a functional-check gate would. Exit 0 runs the gate and
// exit 1 is the ONE skip code (telemetry reason "when"). Every other outcome —
// another exit code, a timeout, an exec failure, a missing skill or one with no
// check — runs the gate (telemetry reason "when-error"): a broken precondition
// must never cost a gate. The engine decides nothing about paths or words; the
// whole rule is in the script, and this is an enqueue filter, not a verdict.
func (g *Engine) whenAllows(ctx context.Context, item workitem.Item, toStatus string, s wfdot.ScopedReviewer) bool {
	event := func(reason, detail string) {
		g.telemetryEvent(ctx, item.ID, "reviewer", "scoped-gate-skipped", map[string]any{
			"skill": s.Skill, "to": toStatus, "reason": reason, "when": s.When, "detail": detail,
		})
	}
	body, err := g.skillBody(ctx, s.When)
	if err != nil {
		event("when-error", "when skill "+s.When+": "+err.Error())
		return true
	}
	command := skillCheck(body)
	if command == "" {
		event("when-error", "when skill "+s.When+" carries no ```check")
		return true
	}
	payload, err := json.Marshal(transitionPayload{Story: item, From: item.Status, To: toStatus, ReviewSkill: s.Skill})
	if err != nil {
		event("when-error", err.Error())
		return true
	}
	timeout := g.checkTimeout
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := g.check(withGateItem(cctx, item), g.repoRoot, command, string(payload))
	if err == nil {
		return true
	}
	var ee interface{ ExitCode() int }
	if cctx.Err() == nil && errors.As(err, &ee) && ee.ExitCode() == whenSkipExit {
		event("when", strings.TrimSpace(out))
		return false
	}
	event("when-error", fmt.Sprintf("%v: %s", err, strings.TrimSpace(out)))
	return true
}

// whenSkipExit is the only exit code of a `when` check that skips its gate.
const whenSkipExit = 1

// reviewerRef is one gate to run: skill name + agents.toml binding section.
// agent empty means [reviewer] (sty_a476a2f8). panel is the ordered seat list
// for this skill; combine is the check skill that folds those seats. Both empty
// means the single agent seat. parallel is the edge's concurrency cap, which
// also bounds the seat fan-out.
type reviewerRef struct {
	skill, agent string
	panel        []string
	combine      string
	parallel     int
	// mode is the gate's declared review mode: wfdot.ReviewModeFailFast or empty
	// (full). A panel's seats inherit it (sty_93359e0f).
	mode string
}

// structureSkill is the required-structure reviewer that judges a draft work
// item at creation. Embedded by default; overridable under .satelle/skills.
const structureSkill = "satelle-story-review"

// summariserSkill recaps an enacted transition. Embedded by default; overridable.
const summariserSkill = "satelle-step-summary"

// summaryPayload is the JSON handed to the summariser on stdin.
type summaryPayload struct {
	Story workitem.Item `json:"story"`
	From  string        `json:"from"`
	To    string        `json:"to"`
}

// Summarise runs the read-only summariser over an enacted transition and returns
// its prose recap (empty when no summariser rubric is installed). The binding
// named on the step node (agent=<name>, default [reviewer]) supplies the harness
// and model so a cheap summariser can narrate without burning the deep judgment
// model. Grant stays read-only (observes, never mutates).
//
// principles stays PrinciplesNone regardless of the binding's principles=
// (deliberate): the summariser is a rubric-only narrator; honouring session
// principles on every transition would inject the constitution into every recap
// and defeat a cheap high-frequency path (sty_8ee40f94).
//
// TODO(sty_ba860c8a): fold onto Invoke once a soft-fail/empty-retry expect mode
// exists without ballooning ExpectVerdict/ExpectPerform. Today it still uses
// buildRequest+runOnce directly (AC1 carve-out).
func (g *Engine) Summarise(ctx context.Context, item workitem.Item, from, to string) (result verb.SummaryResult, err error) {
	// The summariser runs ONLY when the active workflow DECLARES a step-summary
	// node (transparent opt-in via the DOT) — there is no hidden always-on
	// summariser (sty_9a139c78). A non-declaring workflow records nothing.
	agentSec, declared, mandatory := g.stepSummaryDeclared(ctx, item)
	if !declared {
		return verb.SummaryResult{}, nil
	}
	// soft returns a zero result on a non-mandatory failure (best-effort) and the
	// error when the step node is mandatory, so the caller can surface the gap.
	soft := func(format string, a ...any) (verb.SummaryResult, error) {
		if mandatory {
			return verb.SummaryResult{}, fmt.Errorf(format, a...)
		}
		return verb.SummaryResult{}, nil
	}
	body, err := g.skillBody(ctx, summariserSkill)
	if err != nil {
		if errors.Is(err, docindex.ErrNotFound) {
			return soft("step summary is mandatory but the %s skill is not installed", summariserSkill)
		}
		return verb.SummaryResult{}, err
	}
	binding, section, berr := g.gateBinding(agentSec)
	if berr != nil {
		return soft("step summary binding: %v", berr)
	}
	if config.ResolvedRole(section, binding) != config.RoleReviewer {
		return soft("step summary agent=%s has role=%q (want role=reviewer)",
			section, config.ResolvedRole(section, binding))
	}
	// Default [reviewer] reuses the engine bootstrap runner; a named binding
	// builds its own harness (same rule as gated edges — sty_68dafd5f).
	var runner agentcli.Runner
	tools, model, env := binding.Tools, binding.Model, binding.Env
	if section == "reviewer" {
		if g.runner == nil {
			return soft("step summary is mandatory but no agent runner is configured")
		}
		runner = g.runner
		if tools == "" {
			tools = g.tools
		}
		if model == "" {
			model = g.model
		}
		if len(env) == 0 {
			env = g.reviewerEnv
		}
	} else {
		r, rerr := g.newRunner(binding.ResolvedInterface(), binding.CommandTemplate())
		if rerr != nil {
			return soft("step summary agent=%s: %v", section, rerr)
		}
		runner = r
	}
	// Model selection (sty_7069bced): model already carries the explicit
	// configured value or the g.model fallback above, so this only descends
	// the ladder (inherited/creator/cli-default) when both are empty.
	selectBinding := binding
	selectBinding.Model = model
	model, modelSource := g.selectModel(ctx, selectBinding, item.ID, "", "")
	// The summariser dispatch gets its own scratch dir exactly like any other
	// isolated agent (sty_e7aaf8b1 AC1) — it is still a subprocess that can be
	// tempted to write evidence/debris somewhere, and the charter briefing
	// below is generated from inv.scratch, not a skill/principle edit.
	scratchDir, screrr := newScratch(g.repoRoot, item.ID)
	if screrr != nil {
		return verb.SummaryResult{}, screrr
	}
	// Removed when this call returns a summary; kept — and ledgered — when it
	// returns an error, exactly like a one-shot Invoke dispatch (sty_e7aaf8b1
	// AC3). A non-mandatory soft() failure returns err==nil, so it is disposed
	// like a success: the caller already has no visibility into it either way.
	defer func() {
		if err == nil {
			finishScratch(scratchDir, false)
			return
		}
		g.recordInvocation(ctx, item.ID, map[string]any{
			"phase": "scratch_kept", "scratch_dir": scratchDir, "agent": section,
		})
		finishScratch(scratchDir, true)
	}()
	scratchedEnv := overlayScratchEnv(env, scratchDir)
	// The summariser prompt is rubric-only (no charter, principles=none) so it
	// stays a plain narrator — buildRequest omits empty sections. Grant is read-only.
	req, err := g.buildRequest(ctx, invocation{
		rubric:     body,
		principles: config.PrinciplesNone,
		payload:    summaryPayload{Story: item, From: from, To: to},
		tools:      tools,
		model:      model,
		effort:     binding.Effort,
		settings:   binding.Settings,
		env:        scratchedEnv,
		scratch:    scratchDir,
	})
	if err != nil {
		return verb.SummaryResult{}, err
	}
	// Reviewer tool isolation (sty_ef3efb51): the summariser is a role=reviewer
	// dispatch, so it takes the same warn / trim / describe path as a gate verdict.
	isolation, _ := g.isolateReviewer(ctx, reviewerDispatch{
		StoryID: item.ID, Actor: section, Skill: summariserSkill, Step: to, Section: section,
	}, runner, binding.OperatorAttested(), &req)
	g.emitActivity(item.ID, "summary", 1, 1)
	g.emitProgress("summarising step %s→%s via [%s] (may take a minute)…", from, to, section)
	// Retry the SAME transient a reviewer retries (a rate-limited/killed/empty
	// subprocess under concurrent sessions — sty_d71b0791, sty_a1151fb0): a single
	// runOnce permanently LOST the summary on a transient kill, silently holing the
	// pull-context chain. Bounded by g.attempts with g.backoff; fail fast on a
	// deadline (a bound, not contention — retrying just re-blocks a full window).
	attempts := g.attempts
	if attempts < 1 {
		attempts = 1
	}
	idle, ierr := g.idleTimeoutFor(section, binding)
	if ierr != nil {
		return soft("step summary invalid idle_timeout in .satelle/workflows/agents.toml [%s]: %v", section, ierr)
	}
	busy, berr := g.busyTimeoutFor(section, binding)
	if berr != nil {
		return soft("step summary invalid busy_timeout in .satelle/workflows/agents.toml [%s]: %v", section, berr)
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if werr := g.retryWait(ctx, attempt); werr != nil {
			lastErr = werr
			break
		}
		out, usage, rerr := g.runOnceBusy(ctx, runner, req, g.agentTimeout, idle, busy)
		if rerr != nil {
			var se *StallError
			if errors.As(rerr, &se) {
				g.telemetryEvent(ctx, item.ID, section, "agent-stalled", map[string]any{
					"skill": summariserSkill, "step": to, "attempt": attempt, "attempts": attempts,
					"idle": se.Idle.String(), "last_event": se.LastEvent,
				})
				return soft("mandatory step summary stalled: no activity for %s (last event: %s)", se.Idle.Round(time.Second), se.LastEvent)
			}
			if errors.Is(rerr, context.DeadlineExceeded) && ctx.Err() == nil {
				g.telemetryEvent(ctx, item.ID, section, "agent-timeout", map[string]any{
					"skill": summariserSkill, "step": to, "attempt": attempt, "attempts": attempts,
					"cause": causeOf(rerr),
				})
				return soft("mandatory step summary timed out after %s", g.agentTimeout)
			}
			lastErr = rerr
			g.logReviewerFailure(summariserSkill, attempt, attempts, rerr, nil)
			g.telemetryEvent(ctx, item.ID, section, "agent-retry", map[string]any{
				"skill": summariserSkill, "step": to, "attempt": attempt, "attempts": attempts, "outcome": classifyOutcome(rerr),
				"cause": causeOf(rerr),
			})
			continue // transient — retry
		}
		if s := strings.TrimSpace(string(out)); s != "" {
			// The summariser's own token/wall-time cost (sty_a699ad14, a documented
			// gap now closed): the verb layer folds this into an agent_invocation row
			// alongside the step_summary text, so `satelle story cost` sees it too.
			summaryModelResolved, summaryModels := toVerbModels(usage)
			return verb.SummaryResult{
				Text: s, Command: runner.Command(), Context: summariserSkill, Model: model,
				ModelResolved: summaryModelResolved, Models: summaryModels, ModelSource: modelSource,
				TokensIn: usage.InputTokens, TokensOut: usage.OutputTokens, TokensTotal: usage.TotalTokens,
				DurationMs: usage.Duration.Milliseconds(), UsageAvailable: usage.Available,
				TokensInFresh: usage.FreshInputTokens, TokensCacheWrite: usage.CacheCreationInputTokens, TokensCacheRead: usage.CacheReadInputTokens, UsageNote: usageNoteFor(usage, binding),
				CostUSD: usage.CostUSD, CostUnavailableReason: usage.CostUnavailableReason,
				SystemPromptBytes: len(req.SystemPrompt), PayloadBytes: len(req.Payload),
				ToolIsolation: isolation,
			}, nil
		}
		lastErr = fmt.Errorf("empty summary output")
		g.logReviewerFailure(summariserSkill, attempt, attempts, lastErr, out)
		g.telemetryEvent(ctx, item.ID, section, "agent-retry", map[string]any{
			"skill": summariserSkill, "step": to, "attempt": attempt, "attempts": attempts, "outcome": "empty-output",
			"cause": causeOf(lastErr),
		})
	}
	g.telemetryEvent(ctx, item.ID, section, "agent-failure", withCause(map[string]any{
		"skill": summariserSkill, "step": to, "attempts": attempts, "outcome": classifyOutcome(lastErr),
	}, lastErr))
	return soft("mandatory step summary failed after %d attempts: %v", attempts, lastErr)
}

// MandatorySummary reports whether item's active workflow declares a MANDATORY
// step-summary node — used to gate the done-time missing-summary surfacing
// (sty_a1151fb0). Implements verb.StepSummariser.
func (g *Engine) MandatorySummary(ctx context.Context, item workitem.Item) bool {
	_, _, mandatory := g.stepSummaryDeclared(ctx, item)
	return mandatory
}

// stepSummaryDeclared reports whether the workflow active for category declares a
// step-summary node (wfdot StepSummary), its agent= section (empty → [reviewer]),
// and whether it is mandatory.
func (g *Engine) stepSummaryDeclared(ctx context.Context, item workitem.Item) (agent string, declared, mandatory bool) {
	spec, _, err := g.activeSpec(ctx, item)
	if err != nil {
		return "", false, false
	}
	return spec.StepSummaryBinding()
}

// ReviewCreate judges a draft work item's required structure before it is
// persisted, DETERMINISTICALLY (internal/structure) — a specific title, a goal
// body, and a non-empty category. Acceptance criteria are not part of that
// check. No LLM, no agent CLI: the contract is code, so it is
// harness-independent and never flaky. Always Gated (the structure is the one
// thing satelle enforces on creation).
func (g *Engine) ReviewCreate(ctx context.Context, draft verb.CreateDraft) (verb.GateDecision, error) {
	// 1. Deterministic structural check FIRST — the one thing satelle always
	// enforces on creation. A structural failure pre-empts: the content reviewer
	// is never reached on a malformed draft.
	if problems := structure.Story(draft.Title, draft.Body, draft.AcceptanceCriteria, draft.Category); len(problems) > 0 {
		return verb.GateDecision{Gated: true, Accept: false, Skill: structureSkill, Notes: strings.Join(problems, "; ")}, nil
	}
	// 2. Optional content/alignment review — the reviewer skill AND the logical
	// agent that runs it are DECLARED by the active workflow's create-review hook
	// (selected by the draft's category), not hardcoded. Absent a declaration (or
	// the skill does not resolve), creation stays deterministic-only.
	hook, declared, herr := g.createReviewHook(ctx, draft.Category)
	if herr != nil {
		// The authored process cannot be read: refuse rather than create under the
		// embedded default's "no gate declared" (sty_d6e209aa).
		return verb.GateDecision{}, herr
	}
	if !declared || hook.Skill == "" {
		return verb.GateDecision{Gated: true, Accept: true, Skill: structureSkill}, nil
	}
	draftItem := workitem.Item{
		Title:              draft.Title,
		Body:               draft.Body,
		AcceptanceCriteria: draft.AcceptanceCriteria,
		Category:           draft.Category,
		Priority:           draft.Priority,
		Tags:               draft.Tags,
		Status:             "backlog",
	}
	// The hook's DECLARED agent, not an empty selector: an omitted agent is
	// wfhook.DefaultAgent ("reviewer") — the same binding as before, but chosen by
	// the substrate and inspectable, rather than fallen into inside gateBinding.
	dec, err := g.runReviewer(ctx, draftItem, "backlog", hook.Skill, hook.Agent)
	if err != nil {
		return verb.GateDecision{}, err
	}
	if dec.Gated {
		return dec, nil // the declared content reviewer accepted or rejected
	}
	// The workflow declared a skill but it does not resolve — accept on structure
	// alone rather than blocking creation on a misconfigured binding.
	return verb.GateDecision{Gated: true, Accept: true, Skill: structureSkill}, nil
}

// createReviewHook resolves the create-review LIFECYCLE HOOK declared by the
// workflow active for the category — its `hooks:` entry or the `create_review:`
// shorthand. Not declared when no workflow governs the category or the workflow
// declares none, so creation stays deterministic-only (the binding is
// configuration, never a hardcoded filename).
//
// The hook carries both the skill and the logical agent; the engine does not
// choose either. Declaration DEFECTS (an unknown operation, a missing skill) are
// deliberately not judged here — that is validation's job, surfaced by
// `satelle agent validate` / `satelle workflow validate` before anything runs.
// The engine keeps only its mechanism-level refusals at dispatch (role, in-loop).
func (g *Engine) createReviewHook(ctx context.Context, category string) (wfhook.Hook, bool, error) {
	return g.lifecycleHook(ctx, category, wfhook.OpCreateReview)
}

// ReviewAmend judges a proposed amendment of a story's FROZEN definition fields
// (sty_81aa4d8f) against the skill+agent the active workflow declares for the
// amend_review lifecycle hook. The reviewer is handed the AMENDED story plus the
// per-field before/after and the caller's reason, because the question it exists
// to answer — correction or self-serving weakening? — cannot be read off the
// amended text alone.
//
// An UNDECLARED hook (or one whose skill does not resolve) returns Gated false,
// which the verb treats as a refusal: piercing the freeze without a judge is not
// on offer. That inverts ReviewCreate's fail-open, deliberately — a bare create
// must stay legal, an unjudged amendment must not.
func (g *Engine) ReviewAmend(ctx context.Context, draft verb.AmendDraft) (verb.GateDecision, error) {
	amended := applyAmendment(draft)
	// Deterministic structure first, exactly as create does: an amendment may
	// correct a definition but never leave the story structurally invalid.
	if problems := structure.Story(amended.Title, amended.Body, amended.AcceptanceCriteria, amended.Category); len(problems) > 0 {
		return verb.GateDecision{Gated: true, Accept: false, Skill: structureSkill, Notes: strings.Join(problems, "; ")}, nil
	}
	hook, declared, herr := g.lifecycleHook(ctx, amended.Category, wfhook.OpAmendReview)
	if herr != nil {
		return verb.GateDecision{}, herr
	}
	if !declared || hook.Skill == "" {
		return verb.GateDecision{}, nil // no gate declared — the verb refuses
	}
	dec, err := g.runReviewerWith(ctx, amended, amended.Status, hook.Skill, hook.Agent,
		func(tp *transitionPayload) {
			tp.Amendment = &AmendmentState{
				Status: draft.Status,
				Reason: draft.Reason,
				Fields: draft.Fields,
			}
		})
	if err != nil {
		return verb.GateDecision{}, err
	}
	// dec.Gated false here means the declared rubric does not resolve. For a
	// transition that degrades to advisory; for an amendment it stays a refusal,
	// carried back as-is so the caller reports the unresolved skill.
	return dec, nil
}

// applyAmendment returns the story as the amendment would leave it — what the
// reviewer judges and what the structure check runs against.
func applyAmendment(draft verb.AmendDraft) workitem.Item {
	out := draft.Item
	for _, f := range draft.Fields {
		switch f.Field {
		case "title":
			out.Title = f.New
		case "body":
			out.Body = f.New
		case "acceptance_criteria":
			out.AcceptanceCriteria = f.New
		case "category":
			out.Category = f.New
		}
	}
	return out
}

// lifecycleHook resolves one lifecycle hook declared by the workflow active for
// the category — its `hooks:` entry or the `<operation>:` shorthand. Not
// declared when no workflow governs the category or the workflow declares none;
// what that ABSENCE means is the caller's decision, not this resolver's.
func (g *Engine) lifecycleHook(ctx context.Context, category, operation string) (wfhook.Hook, bool, error) {
	// A lifecycle hook is workflow FRONTMATTER, and a derived route's frontmatter
	// lives on its declaration of done — the half that says what this repo means
	// by finished, which is where a create gate belongs. Read it first, so a
	// converted repo keeps the gate its graphs used to declare (sty_9835070d).
	//
	// Only when the route GOVERNS the category, though. The doc index overlays the
	// shipped done.toml wherever a repo has no file of that name, so reading it
	// unconditionally would let the default's create gate shadow the one an
	// authored workflow declares — the same precedence hole RouteGovernsErr closes
	// for the lifecycle itself (sty_3795e7f6).
	//
	// An UNREADABLE authored workflows dir is an error, never "no hook declared":
	// the caller would otherwise create or amend under no gate at all
	// (sty_d6e209aa).
	if workflows, err := g.docs.List(ctx, "workflows"); err == nil {
		rs, ok, rerr := wfgovern.RouteGovernsErr(workflows, category)
		if rerr != nil {
			return wfhook.Hook{}, false, rerr
		}
		if ok {
			if h, hooked := wfhook.For(rs.Done, operation); hooked {
				return h, true, nil
			}
		}
	}
	doc, err := g.activeWorkflow(ctx, category)
	if err != nil {
		return wfhook.Hook{}, false, nil
	}
	h, hooked := wfhook.For(doc.Body, operation)
	return h, hooked, nil
}

// reviewerSkills resolves the ordered reviewer skills governing the (from→to)
// edge from the workflow active for the item's category, the edge's model=
// override (empty = inherit binding), and whether the edge is a DECLARED
// transition of that workflow. An absent workflow means no governance at all —
// every edge is allowed and ungated (declared=true, no skills), so fresh repos
// and the baseline keep working.
func (g *Engine) reviewerSkills(ctx context.Context, item workitem.Item, from, to string) (skills []string, model string, parallel int, declared bool, panel []string, combine string, err error) {
	spec, _, err := g.activeSpec(ctx, item)
	if ungoverned(err) {
		return nil, "", 0, true, nil, "", nil
	}
	if err != nil {
		// A lifecycle EXISTS and does not resolve. Refusing here is the point:
		// falling through as "ungated but declared" would advance the story past
		// every gate the route declares (sty_9835070d).
		return nil, "", 0, false, nil, "", err
	}
	skills, model, parallel, declared, panel, combine = specReviewerSkills(spec, from, to)
	return skills, model, parallel, declared, panel, combine, nil
}

// successorsOf returns declared DOT successors of from for agent-facing refuse
// messages (sty_ebd3d666). Empty when no workflow/DOT resolves.
func (g *Engine) successorsOf(ctx context.Context, item workitem.Item, from string) []string {
	spec, _, err := g.activeSpec(ctx, item)
	if err != nil {
		return nil
	}
	return spec.Successors(from)
}

// parkResume reports whether item→toStatus is a park resume-to-origin (resume
// true, ungated) or an illegal resume (refuse non-nil). Origin is
// verb.ParkOriginOf — the stamped column, else the last transition into this
// park (sty_524f091d). An origin-less park refuses performing targets instead
// of falling through to undeclared-edge.
func (g *Engine) parkResume(ctx context.Context, item workitem.Item, toStatus string) (resume bool, refuse error) {
	spec, _, err := g.activeSpec(ctx, item)
	if err != nil || !spec.IsParkState(item.Status) {
		return false, nil
	}
	origin := verb.ParkOriginOf(ctx, item, spec)
	if origin == "" {
		// Origin-less park: do not fall through to undeclared-edge (that
		// names cancelled as the only successor). Non-performing exits
		// stay on the ordinary gate path; performing targets refuse.
		if spec.HasEdge(item.Status, toStatus) && !spec.IsPerformingState(toStatus) {
			return false, nil
		}
		if spec.IsPerformingState(toStatus) || !spec.HasEdge(item.Status, toStatus) {
			var exits []string
			for _, to := range spec.Successors(item.Status) {
				if !spec.IsPerformingState(to) {
					exits = append(exits, to)
				}
			}
			return false, wfgovern.Refusal{
				Rule: wfgovern.RuleParkResume, Item: item.ID,
				From: item.Status, To: toStatus,
				Why:          "this park has no recorded origin — the column is empty and the ledger has no transition into this park — so resume is not admitted",
				Alternatives: exits,
				Remedy:       "cancelled and other declared non-performing exits remain open",
			}
		}
		return false, nil
	}
	if toStatus == origin {
		return true, nil
	}
	// Explicit non-performing exits (cancelled) stay on ordinary gate path.
	if spec.HasEdge(item.Status, toStatus) && !spec.IsPerformingState(toStatus) {
		return false, nil
	}
	// Performing targets other than origin, or undeclared exits: refuse so park
	// cannot wormhole around gates (e.g. park from in_progress → release).
	if spec.IsPerformingState(toStatus) || !spec.HasEdge(item.Status, toStatus) {
		var exits []string
		for _, to := range spec.Successors(item.Status) {
			if !spec.IsPerformingState(to) {
				exits = append(exits, to)
			}
		}
		return false, wfgovern.Refusal{
			Rule: wfgovern.RuleParkResume, Item: item.ID,
			From: item.Status, To: toStatus,
			Why: fmt.Sprintf(
				"a parked story resumes to the state it parked from (%q); resuming elsewhere would re-enter the route past gates it never passed",
				origin),
			Alternatives: append([]string{origin}, exits...),
		}
	}
	return false, nil
}

// activeWorkflow returns the authored WORKFLOW doc governing an item of the
// given category. Selection matches the item's category against each indexed
// workflow's `applies_to` frontmatter: a workflow listing the category wins, a
// wildcard (`applies_to: ["*"]`) workflow is the next-best. This is the
// configuration-over-code path — a repo adds a category-specific workflow as
// substrate and it takes effect with no binary change.
//
// It knows nothing about the derived route: the lifecycle front door is
// activeSpec / wfgovern.SpecFor, and the order-zero fallback is now the route
// the binary ships rather than a graph resolved by name (sty_3795e7f6). No
// applicable workflow is ErrNotFound, which every caller already treats as "no
// authored workflow governs this".
func (g *Engine) activeWorkflow(ctx context.Context, category string) (docindex.Doc, error) {
	workflows, err := g.docs.List(ctx, "workflows")
	if err != nil {
		return docindex.Doc{}, err
	}
	if ordered := wfgovern.OrderedWorkflows(wfgovern.LifecycleWorkflows(workflows), category); len(ordered) > 0 {
		return ordered[0], nil // the highest-priority applicable workflow
	}
	return docindex.Doc{}, docindex.ErrNotFound
}

// WorkflowStampPrefix re-exports the stamp tag prefix (owned by wfgovern).
const WorkflowStampPrefix = wfgovern.WorkflowStampPrefix

// workflowCategory / stampedWorkflowName thin-wrap wfgovern for in-package call sites.
func workflowCategory(item workitem.Item) string { return wfgovern.WorkflowCategory(item) }
func stampedWorkflowName(item workitem.Item) string {
	return wfgovern.StampedWorkflowName(item)
}

// OrderedWorkflows re-exports wfgovern.OrderedWorkflows for callers that still
// import agentstep (web, CLI). Prefer wfgovern directly for new code.
func OrderedWorkflows(workflows []docindex.Doc, category string) []docindex.Doc {
	return wfgovern.OrderedWorkflows(workflows, category)
}

// GoverningWorkflow re-exports wfgovern.GoverningWorkflow.
func GoverningWorkflow(workflows []docindex.Doc, item workitem.Item) (docindex.Doc, bool) {
	return wfgovern.GoverningWorkflow(workflows, item)
}

// activeWorkflowPreferring resolves the governing workflow, preferring the item's
// STAMPED workflow when present (deterministic after create); it falls back to
// category selection when un-stamped or the stamped workflow no longer resolves.
func (g *Engine) activeWorkflowPreferring(ctx context.Context, category, stamped string) (docindex.Doc, error) {
	if stamped != "" {
		if doc, err := g.docs.Get(ctx, "workflows", stamped); err == nil {
			return doc, nil
		}
		// The stamped workflow is gone — fall back to category selection rather
		// than losing governance.
	}
	return g.activeWorkflow(ctx, category)
}

// activeSpec resolves the LIFECYCLE governing item through the one front door
// (wfgovern.SpecFor): the derived route the substrate carries, and a named
// refusal when a workflows doc claims the category but declares no route. It
// replaces the resolve-then-parse pair every gating path used to repeat, so a
// repo's conversion needs no per-call-site change (sty_9835070d).
//
// The error is not collapsed. wfgovern.ErrNoWorkflow means nothing governs the
// item at all — a fresh repo, the case every caller below already treats as
// ungoverned. Any OTHER error means a lifecycle exists and does not resolve, and
// that must never read the same way: a route that fails to build would otherwise
// silently drop every gate it declares.
func (g *Engine) activeSpec(ctx context.Context, item workitem.Item) (wfdot.Spec, string, error) {
	workflows, err := g.docs.List(ctx, "workflows")
	if err != nil {
		return wfdot.Spec{}, "", err
	}
	spec, name, _, err := wfgovern.SpecFor(workflows, item)
	if err == nil {
		return spec, name, nil
	}
	// There is no second fallback to reach for: the order-zero lifecycle is the
	// route the binary ships, and the doc index overlays it into `workflows`
	// wherever the repo has no half of its own, so SpecFor has already considered
	// it (sty_3795e7f6). ErrNoWorkflow here means the substrate genuinely governs
	// nothing — a repo that deleted the shipped route.
	return wfdot.Spec{}, name, err
}

// ungoverned reports whether err from activeSpec means "nothing governs this
// item" — the fresh-repo case a caller may treat as no governance. A route that
// exists but does not build is NOT ungoverned and must not take this path.
func ungoverned(err error) bool {
	return errors.Is(err, wfgovern.ErrNoWorkflow) || errors.Is(err, docindex.ErrNotFound)
}

// WorkflowNameFor returns the name of the workflow that governs a story of the
// given category — the value stamped on the story at create. Empty when no
// workflow governs the category. Used by the create path to record the choice.
//
// The error is non-nil only when the authored workflows dir is unreadable: the
// caller refuses, so no story is stamped (and so created) under a process that is
// not the repository's (sty_d6e209aa).
func (g *Engine) WorkflowNameFor(ctx context.Context, category string) (string, error) {
	// A story is stamped with what will GOVERN it. When a derived route claims
	// the category, that is the route — stamping an authored workflow the engine
	// will not consult would be a lie on every story created after a conversion
	// (sty_9835070d).
	if workflows, err := g.docs.List(ctx, "workflows"); err == nil {
		_, ok, rerr := wfgovern.RouteGovernsErr(workflows, category)
		if rerr != nil {
			return "", rerr
		}
		if ok {
			return wfgovern.DerivedRouteName, nil
		}
	}
	doc, err := g.activeWorkflow(ctx, category)
	if err != nil {
		return "", nil
	}
	return doc.Name, nil
}

// WorkflowStates returns the lifecycle states the named workflow declares — the
// nodes on its TRANSITIONS (an edge-less declared reviewer node like estimate/
// step is a gate declaration, not a lifecycle state) — and whether the workflow
// resolves at all. The restamp validation seam (sty_ed3386cf): a story may only
// be re-stamped onto a workflow that declares its current status.
//
// A lifecycle is a DERIVED ROUTE now, and a route's states depend on the story's
// category — which a name alone does not carry. So a name that resolves returns
// no states, and the caller skips the status check rather than stranding the
// story: the same contract an unparseable lifecycle always had here
// (sty_d953c5d8).
//
// The derived route resolves by NAME, not by doc lookup (sty_81bb0dde). It is
// the one lifecycle that has no document of its own — it is built from the two
// route-source halves — so asking the index for a doc called "default" always
// misses, and `satelle story restamp` refused to stamp the very name the create
// path assigns. That is the exact round trip a stamp has to support: whatever
// WorkflowNameFor hands out, restamp must accept back.
func (g *Engine) WorkflowStates(ctx context.Context, name string) ([]string, bool, error) {
	if name == wfgovern.DerivedRouteName {
		present, err := g.routeSourcePresent(ctx)
		return nil, present, err
	}
	if _, err := g.docs.Get(ctx, "workflows", name); err != nil {
		return nil, false, nil
	}
	return nil, true, nil
}

// routeSourcePresent reports whether a derived route exists to be stamped onto —
// both halves, authored or shipped. Without this check the name would resolve in
// a repo that has no route at all, which is the one case a stamp must refuse.
// An unreadable authored workflows dir is an error rather than "absent", so the
// refusal names it (sty_d6e209aa).
func (g *Engine) routeSourcePresent(ctx context.Context) (bool, error) {
	workflows, err := g.docs.List(ctx, "workflows")
	if err != nil {
		return false, nil
	}
	rs := wfgovern.RouteSourceOf(workflows)
	if err := rs.Err(); err != nil {
		return false, err
	}
	return rs.Present(), nil
}

// WorkflowConsistency reports cross-workflow inconsistencies an agent should
// advise the user about (sty_4c0c7246): (1) OVER-CONFIGURATION — two or more REPO
// workflows claim the same category (or the wildcard) at the same precedence, so
// the tiebreak is arbitrary; (2) a workflow that REFERENCES a skill (an edge gate
// or a node @skill: prompt) which does not resolve in the substrate. Empty when
// the workflow set is consistent. resolve may be nil to skip the skill check.
func WorkflowConsistency(workflows []docindex.Doc, resolve func(skill string) bool) []string {
	var problems []string
	workflows = docindex.WithoutUnreadable(workflows) // a sentinel is a state, never a workflow

	// (1) Ambiguous applies_to among REPO workflows (the embedded defaults are the
	// single canonical source, so a tie there is not the user's misconfiguration).
	cats := map[string]bool{}
	for _, w := range workflows {
		for _, c := range wfgovern.FrontmatterList(w.Body, "applies_to") {
			cats[c] = true
		}
	}
	for c := range cats {
		var repo []string
		for _, w := range workflows {
			if !w.Embedded && containsStr(wfgovern.FrontmatterList(w.Body, "applies_to"), c) {
				repo = append(repo, w.Name)
			}
		}
		if len(repo) >= 2 {
			sort.Strings(repo)
			label := c
			if c == "*" {
				label = "* (wildcard)"
			}
			problems = append(problems, fmt.Sprintf(
				"category %s: workflows %s apply at the same precedence — give them distinct applies_to or remove the duplicate", label, strings.Join(repo, ", ")))
		}
	}

	// (2) Referenced skills that do not resolve — per workflow, so the same
	// definition serves the whole-set callers and the single-doc authoring paths
	// (sty_d59ec6a9). The two cannot drift.
	if resolve != nil {
		for _, w := range workflows {
			problems = append(problems, WorkflowSkillProblems(w, resolve)...)
		}
	}
	sort.Strings(problems)
	return problems
}

// WorkflowSkillProblems reports, for ONE workflow, every skill it names that
// does not resolve in the substrate — edge gates, node @skill: prompts, and
// declared lifecycle hooks — plus any hook declaration defect.
//
// This is the half of WorkflowConsistency that is meaningful per document. The
// ambiguity check is deliberately NOT here: it compares repo workflows against
// each other, so it is whole-set by nature and firing it on a single doc would
// misreport (sty_d59ec6a9 AC3).
//
// Callers: WorkflowConsistency (whole set, where these are FAILs), and the
// authoring paths `workflow validate <name>` / `workflow create` (where they are
// WARNs — a repo mid-authoring writes the workflow before it writes the gate
// skills, so blocking there would make the ordinary sequence impossible).
func WorkflowSkillProblems(w docindex.Doc, resolve func(skill string) bool) []string {
	if resolve == nil {
		return nil
	}
	var problems []string
	// A declared lifecycle hook's skill must resolve too (sty_51ad783b,
	// generalised in sty_ede16f51): an unresolved one silently degrades the
	// operation, which is exactly the misconfiguration to surface here.
	// Declaration defects (unknown operation, malformed entry) surface with
	// it, so one check covers the whole hook grammar.
	hooks, hookProblems := wfhook.Parse(w.Body)
	for _, p := range hookProblems {
		problems = append(problems, fmt.Sprintf("workflow %s %s", w.Name, p))
	}
	for _, h := range hooks {
		if !resolve(h.Skill) {
			problems = append(problems, fmt.Sprintf(
				"workflow %s declares %s %q which does not resolve in the substrate", w.Name, h.Operation, h.Skill))
		}
	}
	// A lifecycle is a derived route, so the skills a workflow-kind doc names come
	// from the route grammar rather than a graph (sty_d953c5d8).
	for _, s := range referencedWorkflowSkills(w.Body) {
		if !resolve(s) {
			// Message text is deliberately IDENTICAL to what the whole-set callers
			// have always printed — AC3 requires their output unchanged. The
			// authoring paths add the "will advance ungated" context around the
			// WARN they wrap it in, rather than editing this shared string.
			problems = append(problems, fmt.Sprintf(
				"workflow %s references skill %q which does not resolve in the substrate", w.Name, s))
		}
	}
	return problems
}

// ReferencedWorkflowSkills is referencedWorkflowSkills for a caller outside the
// package: every skill one half of a derived route names, which is the set a
// route WIRES. The context audit asks it which skills anything references.
func ReferencedWorkflowSkills(body string) []string { return referencedWorkflowSkills(body) }

// referencedWorkflowSkills lists every skill one half of a derived route names —
// a step's executor rubrics and entry reviewers, an always-on gate, a park or
// cancel gate — deduped and sorted. A body that is not route grammar names
// nothing, which is the honest answer for a doc that carries no lifecycle
// (sty_d953c5d8).
func referencedWorkflowSkills(body string) []string {
	set := map[string]bool{}
	if lists, err := wfdot.ParseDone(body); err == nil {
		for _, l := range lists {
			for _, sk := range []string{l.ParkGate, l.CancelGate, l.ParkAdvisorSkill} {
				if sk != "" {
					set[sk] = true
				}
			}
		}
	}
	if cat, err := wfdot.ParseSteps(body); err == nil {
		// Catalogue-wide DELIBERATELY: this wants the UNION of every skill any
		// section names, so a skill referenced by any route resolves. Narrowing to
		// one category's selected steps (wfdot.SelectSteps) would report skills the
		// other route families use as unreferenced (sty_a7316b06).
		for _, st := range cat.Steps {
			for _, sk := range append(append([]string(nil), st.Skills...), st.Reviewers...) {
				if sk != "" {
					set[sk] = true
				}
			}
			if st.AdvisorSkill != "" {
				set[st.AdvisorSkill] = true
			}
		}
		for _, g := range cat.Gates {
			// A gate's `when` skill is a route reference like its own skill: unresolved,
			// the gate would run unconditionally (when-error), so surface it.
			for _, sk := range []string{g.Skill, g.When} {
				if sk != "" {
					set[sk] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runCheck runs a skill's functional-check command and returns a deterministic
// verdict: exit 0 accepts, any non-zero (or a run error / timeout) rejects
// with the command's output — reduced by the wired log compressor
// (sty_ef930f81) — as actionable notes.
//
// reuse is the skill's check_reuse value. "tree" (with the evidence seam wired
// and a git tree to hash) records every run as a suite_run row and accepts
// without running when a green row already proves the same story, check,
// command and tree (sty_dc554488). Any other value runs the command every time.
func (g *Engine) runCheck(ctx context.Context, item workitem.Item, skill, command, payload, reuse string) verb.GateDecision {
	var tree, head, cmdSHA string
	if reuse == "tree" && g.suiteRecord != nil && g.suiteLookup != nil {
		var terr error
		if tree, head, terr = verb.GitContentTree(g.repoRoot); terr != nil {
			tree = "" // no tree to prove: run, record nothing
		} else {
			cmdSHA = verb.CommandDigest(command)
			if ev, ok := g.suiteLookup(ctx, item.ID, skill, cmdSHA, tree); ok {
				return verb.GateDecision{Gated: true, Skill: skill, Accept: true, Notes: fmt.Sprintf(
					"functional check passed (%s) — reused suite_run %s: tree %s proven %s (%s), not re-run",
					skill, ev.ID, verb.ShortTree(tree), ev.RecordedAt.UTC().Format(time.RFC3339),
					(time.Duration(ev.DurationMS) * time.Millisecond).String())}
			}
		}
	}
	timeout := g.checkTimeout
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	out, err := g.check(withGateItem(cctx, item), g.repoRoot, command, payload)
	if tree != "" {
		outcome := verb.SuiteGreen
		if err != nil {
			outcome = verb.SuiteRed
		}
		_, _ = g.suiteRecord(ctx, item.ID, verb.SuiteEvidence{
			Check: skill, Tree: tree, SHA: head, Command: command, CommandSHA: cmdSHA,
			Outcome: outcome, DurationMS: time.Since(started).Milliseconds(),
		})
	}
	dec := verb.GateDecision{Gated: true, Skill: skill}
	if err != nil {
		dec.Accept = false
		var notes string
		if g.checkLogCompressor != nil {
			notes = g.checkLogCompressor(ctx, item.ID, out)
		} else {
			notes = compact.CompressLog(out, compact.LogConfig{}, nil)
		}
		// The label names the skill, never the script: the script stays readable
		// in the skill file, and echoing it into every verdict floods the
		// driver's context (sty_e2b78a8d).
		label := "functional check failed (" + skill
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			label += fmt.Sprintf(", exit %d", ee.ExitCode())
		}
		dec.Notes = fmt.Sprintf("%s): %v\n%s", label, err, notes)
		return dec
	}
	dec.Accept = true
	dec.Notes = "functional check passed (" + skill + ")"
	// A passing check's own result lines ride along, bounded by the default log
	// compression; the wired compressor stays failure-only.
	if res := strings.TrimSpace(compact.CompressLog(out, compact.LogConfig{}, nil)); res != "" {
		dec.Notes += "\n" + res
	}
	return dec
}

// skillCheck returns a functional-check skill's command — the SELF-CONTAINED
// check carried inside the skill artifact. Delegates to structure.CheckCommand,
// the single definition every caller shares (sty_4cebc624 / sty_6830e78e).
// Empty when the skill carries no check (an LLM reviewer).
func skillCheck(body string) string {
	return structure.CheckCommand(body)
}

// frontmatterList parses a list-valued key from a markdown frontmatter block,
// handling both the inline flow form (`applies_to: ["*", "web"]`) and the block
// list form (`applies_to:` then `- web` lines). Returns nil when absent.
func frontmatterList(body, key string) []string {
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil
	}
	end := -1
	for j := 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "---" {
			end = j
			break
		}
	}
	if end < 0 {
		return nil
	}
	for i := 1; i < end; i++ {
		t := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(t, key+":") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(t, key+":"))
		if strings.HasPrefix(rest, "[") { // inline flow form
			rest = strings.TrimSuffix(strings.TrimPrefix(rest, "["), "]")
			return splitTrimList(rest)
		}
		var out []string // block list form
		for j := i + 1; j < end; j++ {
			l2 := strings.TrimSpace(lines[j])
			if l2 == "" {
				continue
			}
			if strings.HasPrefix(l2, "- ") {
				out = append(out, strings.Trim(strings.TrimSpace(l2[2:]), `"'`))
				continue
			}
			break
		}
		return out
	}
	return nil
}

// splitTrimList splits a comma-separated inline list, trimming whitespace and
// surrounding quotes, dropping empties.
func splitTrimList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		v := strings.Trim(strings.TrimSpace(p), `"'`)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// skillBody returns the reviewer skill's markdown body from the substrate.
func (g *Engine) skillBody(ctx context.Context, name string) (string, error) {
	doc, err := g.docs.Get(ctx, "skills", name)
	if err != nil {
		return "", err
	}
	return doc.Body, nil
}

// firstStr returns the first non-empty string in ss, or "".
func firstStr(ss []string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// reviewerSkillsFor scans a workflow body's transition lines for the (from→to)
// edge. It returns the edge's ordered reviewer skills (nil when the edge is
// declared but ungated), the edge agent= binding section (empty → reviewer),
// the parallel concurrency cap (0 = sequential; sty_4f0a15db), and whether the
// edge is DECLARED at all.
func reviewerSkillsFor(body, from, to string) (skills []string, agent string, parallel int, declared bool) {
	// The inline `- {from:, to:}` grammar, which some fixtures and legacy bodies
	// still carry. A DERIVED route never reaches here — the front door hands its
	// callers a Spec, and specReviewerSkills answers off that (sty_d953c5d8).
	for _, line := range strings.Split(body, "\n") {
		l := strings.TrimSpace(line)
		if !strings.HasPrefix(l, "- {") || !strings.Contains(l, "from:") || !strings.Contains(l, "to:") {
			continue
		}
		if inlineField(l, "from") == from && inlineField(l, "to") == to {
			if list := inlineListField(l, "reviewer_skills"); len(list) > 0 {
				return list, "", 0, true
			}
			if s := inlineField(l, "reviewer_skill"); s != "" {
				return []string{s}, "", 0, true
			}
			return nil, "", 0, true
		}
	}
	return nil, "", 0, false
}

// specReviewerSkills resolves an edge's gate off an already-built Spec. It is
// the front-door form: whichever representation produced the Spec — an authored
// DOT or a derived route — the edge answers the same way (sty_9835070d).
func specReviewerSkills(spec wfdot.Spec, from, to string) (skills []string, agent string, parallel int, declared bool, panel []string, combine string) {
	for _, tr := range spec.Transitions {
		if tr.From == from && tr.To == to {
			if len(tr.Skills) > 0 {
				return tr.Skills, tr.Agent, tr.Parallel, true, tr.Panel, tr.Combine
			}
			return nil, tr.Agent, tr.Parallel, true, tr.Panel, tr.Combine
		}
	}
	return nil, "", 0, false, nil, ""
}

// inlineField extracts key's value from an inline-map line, trimming quotes. The
// value runs to the next comma or closing brace.
func inlineField(line, key string) string {
	i := strings.Index(line, key+":")
	if i < 0 {
		return ""
	}
	rest := strings.TrimLeft(line[i+len(key)+1:], " ")
	if end := strings.IndexAny(rest, ",}"); end >= 0 {
		rest = rest[:end]
	}
	return strings.Trim(strings.TrimSpace(rest), `"`)
}

// inlineListField extracts a bracketed list value (`key: [a, b, c]`) from an
// inline-map line, trimming whitespace and quotes per element. Returns nil when
// the key is absent or carries no bracketed list — so a single-valued field
// falls through to inlineField.
func inlineListField(line, key string) []string {
	i := strings.Index(line, key+":")
	if i < 0 {
		return nil
	}
	rest := line[i+len(key)+1:]
	open := strings.Index(rest, "[")
	if open < 0 {
		return nil
	}
	closeAt := strings.Index(rest[open:], "]")
	if closeAt < 0 {
		return nil
	}
	return splitTrimList(rest[open+1 : open+closeAt])
}

// rawDecision is the reviewer's JSON contract: {decision, notes, reasoning}.
// reasoning is optional for back-compat with notes-only output (design §6.1).
// reviewed is the exact words this verdict rests on. reviewed_truncated is
// accepted on the wire and then ignored: the ceiling below decides the flag.
type rawDecision struct {
	Decision          string `json:"decision"`
	Notes             string `json:"notes"`
	Reasoning         string `json:"reasoning"`
	Reviewed          string `json:"reviewed,omitempty"`
	ReviewedTruncated bool   `json:"reviewed_truncated,omitempty"`
	// Criteria is the optional per-criterion verdict (sty_99d3162b).
	Criteria []rawCriterion `json:"criteria,omitempty"`
	// Coverage is what the reviewer says the verdict covered (sty_93359e0f):
	// complete, or stopped early at a named finding. Optional; absent is stored
	// as absent, never as complete.
	Coverage json.RawMessage `json:"coverage,omitempty"`
	// Waived names the checks the reviewer honoured a payload waiver for
	// (sty_bef97244). Optional; the engine drops any the payload carried no
	// waiver for (prep.keepWaived).
	Waived []string `json:"waived,omitempty"`
}

// applyCoverage reads a reviewer's stated coverage, bounding stopped_at to
// reviewedCeiling like the quotation it travels with. It is decoded apart from
// the verdict so a malformed coverage costs the coverage, never the verdict:
// absent or unreadable stays nil, which is stored as "stated nothing".
func applyCoverage(raw json.RawMessage) *verb.Coverage {
	var c struct {
		Complete  bool   `json:"complete"`
		StoppedAt string `json:"stopped_at"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &c) != nil {
		return nil
	}
	stopped := strings.TrimSpace(c.StoppedAt)
	if len(stopped) > reviewedCeiling {
		stopped = stopped[:reviewedCeiling]
	}
	return &verb.Coverage{Complete: c.Complete, StoppedAt: stopped}
}

// applyReviewed bounds a quotation to reviewedCeiling before the decision is
// copied onward. Over the ceiling the string is dropped and the flag is set.
// At or under the ceiling the full string is kept and the flag stays unset.
// A model-sent flag does not override that. This is a size bound, not a
// comparison and not a shortened citation.
func applyReviewed(s string) (string, bool) {
	if len(s) > reviewedCeiling {
		return "", true
	}
	return s, false
}

// parseDecision finds the reviewer's verdict in the agent's stdout — lenient on
// surrounding prose, extra wrapping braces, and example objects, but strict on
// shape. It scans every balanced {…} candidate and returns the LAST that yields a
// decision in {accept, reject}: a model reasons then concludes, so its final
// verdict wins over any format example it echoed earlier.
func parseDecision(out []byte) (verb.GateDecision, error) {
	var found *verb.GateDecision
	for _, obj := range jsonObjectCandidates(out) {
		var rd rawDecision
		if err := json.Unmarshal(obj, &rd); err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(rd.Decision)) {
		case "accept", "reject":
			reviewed, truncated := applyReviewed(rd.Reviewed)
			d := verb.GateDecision{
				Accept:            strings.ToLower(strings.TrimSpace(rd.Decision)) == "accept",
				Notes:             rd.Notes,
				Reasoning:         rd.Reasoning,
				Reviewed:          reviewed,
				ReviewedTruncated: truncated,
				Criteria:          parseCriteria(rd.Criteria),
				CriteriaSent:      len(rd.Criteria) > 0,
				Coverage:          applyCoverage(rd.Coverage),
				Waived:            rd.Waived,
			}
			found = &d
		}
	}
	if found != nil {
		return *found, nil
	}
	return verb.GateDecision{}, fmt.Errorf("no {\"decision\": \"accept\"|\"reject\"} object in reviewer output")
}

// proseVerdict matches an explicit prose decision statement — the word
// "verdict" or "decision" followed (allowing punctuation/markdown/an "is") by
// accept or reject, e.g. `Verdict: **reject**.` or `decision is accept`. The
// marker word is REQUIRED so rubric prose the reviewer echoes ("Reject when a
// criterion is unmet…") can never read as a verdict.
var proseVerdict = regexp.MustCompile(`(?i)\b(?:verdict|decision)\b(?:\s+is)?[^a-zA-Z0-9]{0,12}(accept|reject)`)

// parseProseDecision recovers a verdict from reviewer output that carries no
// parseable JSON decision object but states its conclusion in prose
// (sty_9485d47e). Every prose-verdict marker in the output must agree: one or
// more consistent markers yield that decision (with the full trimmed output as
// the notes — the reviewer's reasons ARE the prose); conflicting markers or none
// at all yield ok=false, leaving the caller's no-verdict handling in place.
func parseProseDecision(out []byte) (verb.GateDecision, bool) {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return verb.GateDecision{}, false
	}
	matches := proseVerdict.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return verb.GateDecision{}, false
	}
	verdict := strings.ToLower(matches[0][1])
	for _, m := range matches[1:] {
		if strings.ToLower(m[1]) != verdict {
			return verb.GateDecision{}, false // conflicting statements — genuinely ambiguous
		}
	}
	notes := text
	const maxNotes = 4000
	if len(notes) > maxNotes {
		notes = "…" + notes[len(notes)-maxNotes:]
	}
	return verb.GateDecision{Accept: verdict == "accept", Notes: notes}, true
}

// jsonObjectCandidates returns every balanced {…} substring, trying each '{'
// start so wrapping braces (e.g. {{…}}), prose, or a code-fenced example don't
// defeat extraction. Brace counting is string-aware so a '{' inside the notes
// text does not unbalance it.
func jsonObjectCandidates(b []byte) [][]byte {
	var out [][]byte
	for i := 0; i < len(b); i++ {
		if b[i] == '{' {
			if end := balancedEnd(b, i); end > i {
				out = append(out, b[i:end+1])
			}
		}
	}
	return out
}

// balancedEnd returns the index of the '}' that closes the '{' at i, ignoring
// braces inside JSON strings, or -1 if unbalanced.
func balancedEnd(b []byte, i int) int {
	depth, inStr, esc := 0, false, false
	for j := i; j < len(b); j++ {
		c := b[j]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}
