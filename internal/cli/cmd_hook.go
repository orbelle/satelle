// `satelle hook` carries the Claude Code hook handlers. Currently one:
// `satelle hook context` — the SessionStart always-context injector
// (sty_e3922598).
//
// At session start it fetches every `principles:session`-flagged authored doc
// and injects their bodies as session context, followed by the standing "pull
// the rest on demand" instruction. This keeps the minimal SYSTEM residency set
// in front of the agent without auto-injecting an unbounded list — the bodies
// are bounded by a byte ceiling, and an overflow is reported on stderr (never
// silently dropped). It FAILS OPEN: an unconfigured repo or any read error
// injects nothing and never blocks the session.
//
// Residency taxonomy (sty_1278fdd9 / satelle-residency): ONE classifier —
//
//	system   = carries the principles:session tag → injected every session
//	ondemand = no marker (default) → pulled only when a skill/workflow references it
//
// Frontmatter scope: on principles is NOT an injection axis (removed; inert if reintroduced).
// Ownership (embedded_sha) is orthogonal to residency.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/wfroute"
	"github.com/bobmcallan/satelle/internal/workitem"
	"github.com/bobmcallan/satelle/internal/worktree"
)

// sessionTag is the sole system-residency marker (taxonomy: system|ondemand).
// A principle carrying it is system-resident — injected every SessionStart.
// A principle WITHOUT it is ondemand — resolvable substrate pulled only when a
// skill or workflow references it, never auto-injected. No other frontmatter
// field (including legacy scope:) participates in injection.
const sessionTag = "principles:session"

// alwaysIndexInstruction is the standing "pull, don't preload" directive
// appended to every injection — the pivot of the session-context model: the
// session set is pushed, everything else is on-demand. On-demand substrate is
// pulled when a skill or workflow REFERENCES it (recall on reference), not by
// browsing — `satelle doc list` is the quality-management / authoring browse
// surface, not the session-context path.
const alwaysIndexInstruction = "The session set above is everything auto-loaded. Other principles and documents are on-demand: pull one with `satelle doc get <kind> <name>` when a skill or workflow references it — do not preload. (`satelle doc list` is the quality-management browse surface for authoring/curating substrate, not a step in the work loop.)"

func init() {
	hook := &cobra.Command{
		Use:   "hook",
		Short: "Claude Code hook handlers (SessionStart context injection, …)",
		Long: `The handlers a harness calls on satelle's behalf: session context injection, the
edit and commit gates, the prompt reminder.

You do not run these by hand — the harness does, on its own events, and they read
their payload from stdin. Read them to understand what satelle enforces in a
session; install or remove the wiring with satelle agents.`,
	}
	context := &cobra.Command{
		Use:   "context",
		Short: "Session-context injector — inject principles:session docs + the on-demand pointer",
		Long: `context injects every principles:session authored doc (the SESSION set —
the minimal operating principle) as session context, then the standing
instruction that the rest is on-demand: pulled via ` + "`satelle doc get`" + ` only
when a skill or workflow references it. The emitting event is the harness's
session_context_event; a payload that names a different event emits nothing,
and a payload with no event field keeps the diagnostic SessionStart contract.
Bounded by that event's budget (overflow noted on stderr); fails open so it
never blocks a session.`,
		Args: cobra.NoArgs,
		// No store annotation: this command opens the store itself, defensively,
		// so any bootstrap failure fails OPEN (exit 0, inject nothing) rather than
		// blocking the session.
		RunE: func(cmd *cobra.Command, args []string) error {
			// Bind identity from the SessionStart payload so a later
			// `satelle story set` in this harness tree can stamp the same id.
			raw, _ := io.ReadAll(cmd.InOrStdin())
			_ = bindSessionID(raw)
			return runHookContext(cmd.OutOrStdout(), cmd.ErrOrStderr(),
				resolveContextHarness(hookHarnessFlag, raw, os.Environ()), raw)
		},
	}
	context.Flags().StringVar(&hookHarnessFlag, "harness", "", "claude|grok|pi —selects the injection limit and output shape (default: sniff event, then environment)")
	gate := &cobra.Command{
		Use:   "gate",
		Short: "PreToolUse edit gate — block code edits unless a story is engaged",
		Long: `gate is the PreToolUse handler for Edit|Write|MultiEdit|NotebookEdit|
search_replace|write. It returns a deny unless a story is ENGAGED — in one of the
active workflow's non-terminal engaging states. On deny it
emits one harness-correct JSON shape on stdout, named by --harness or detected
from the event envelope: Claude hookSpecificOutput.permissionDecision=deny;
Grok decision=deny + reason; cursor permission=deny + user_message +
agent_message. The encodings live in internal/agentcli. Emitting two shapes in
one blob fails Claude's schema and silently unblocks the tool.
"Engaged" is authored substrate: it reads the route's start/terminal markers
(Mdiamond=start, Msquare=terminal), never hardcoded state names.

The installed satelle-hook.sh wrapper passes a deny it recognises through
unchanged, exit 0; any other result becomes that harness's infrastructure deny.
commitgate fails open for non-mutating shell.

The edit target is resolved to an ABSOLUTE path against the repo root before any
containment test: Claude sends an absolute path, Grok a relative one.

Edits landing in ANOTHER git working tree are REFUSED — open a
session in THAT repo. Temp, scratchpad and non-repo paths are allowed.

Exemption is CONFIGURATION, not code. An edit is exempt when its target falls
under a [gate] edit_exempt_paths prefix (repo-root-relative or absolute) or
matches an edit_exempt_globs filename pattern. 'satelle init' SEEDS .satelle/,
the footprint it deploys (.gitignore, harness scaffolds) and story-dump names;
the operator owns the lists.

Lock precedence over edit_exempt_paths: see 'satelle help substrate'. A path
under a [gate] lock_substrate_paths prefix (default .satelle/ when the key is
absent) is judged before the exemption is consulted, so no edit can rewrite the
substrate that judges the work.
One harness-neutral predicate decides it for Claude, Grok and pi. The deny names
each holder (status, category), the path, the condition and fix, and lands on
that ledger. A substrate-lane story (category "substrate", judged by
satelle-workflow-change-review) may change substrate while siblings perform;
they get substrate_change_note. Never locked:
temp dirs, edit_exempt_globs matches, and the deployed footprint (.gitignore,
.claude/, .grok/, .pi/). lock_substrate_paths = [] opts out; 'satelle doctor' reports it.

The lock covers Bash, here and in commitgate, with the same text and ledger
row: a redirect, tee, rm/mv/cp/sed -i or git -C target in locked substrate, or
an interpreter (python, node, perl, ruby) whose text names a locked path or the
bare root (.satelle) — even if it only READS. A shell -c program is classified
as shell unless not plain ('satelle help substrate'). Unseen, so allowed:
python3 tool.py, '.sat'+'elle'.

Fails closed: a store or listing error, unresolvable workflow, or
workflow declaring no route blocks the edit.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, _ := io.ReadAll(cmd.InOrStdin())
			defer config.MemoGitIdentity()()
			gs := &gateStore{load: true}
			defer gs.close()
			sid := bindSessionID(raw)
			p := filePathFromEvent(raw)
			command := agentcli.CommandFromHookEvent(raw)
			// A session holding seats in several worktrees is attributed by the
			// tree the edit targets; a shell command's first mutation target's tree
			// stands in for its path.
			attribution := treeOf(p)
			if p == "" && command != "" {
				attribution = bashAttributionTree(command, sessionAnchor())
			}
			info, engaged, live, engErr := gs.seatsFor(true, sid, attribution)
			if p != "" {
				// Foreign-tree fence: refuse edits that land in ANOTHER git working
				// tree unless the operator opts in. Non-repo paths are not fenced.
				if !allowOutsideTreeEdits() {
					if foreignRoot, foreign := editTargetForeign(p); foreign {
						return denyPreToolUse(cmd, raw, outsideRepoEditReason(p, foreignRoot))
					}
				}
				// Exemption is configuration ([gate] edit_exempt_paths/globs) plus
				// the temp-dir containment rule.
				if exemptTarget(p) {
					return nil
				}
			} else if command != "" {
				if !allowOutsideTreeEdits() {
					_, foreign := bashMutationTargets(command, sessionAnchor())
					if path, foreignRoot, ok := foreignTreeTarget(sessionAnchor(), foreign); ok {
						return denyPreToolUse(cmd, raw, outsideAnchorBashReason(path, foreignRoot))
					}
				}
				if !bashMutatesTree(command, sessionAnchor()) {
					return nil
				}
			}
			// A broken deployment blocks the edit with a clear message rather than
			// silently allowing it.
			if engErr != nil {
				return denyPreToolUse(cmd, raw, "satelle: "+engErr.Error())
			}
			_ = engaged
			dm, rm := currentDispatchMarker(), currentRelayMarker()
			if hookEditPermitted(info, dm, rm) {
				return nil
			}
			if reason, ok := sessionSeatsUnmatched(engaged, live, sid, dm, rm); ok {
				return denyPreToolUse(cmd, raw, reason)
			}
			return denyPreToolUse(cmd, raw, hookDenyReason(info, live, dm, rm, sid, time.Now().UTC()))
		},
	}

	prompt := &cobra.Command{
		Use:   "prompt",
		Short: "UserPromptSubmit reinforcement — re-inject the edits-require-a-story rule + a gate-liveness self-check",
		Long: `prompt is the UserPromptSubmit handler. With no live engagement seat it
re-injects a CONCISE reminder that tree edits require an engaged story (the full
principle rides at SessionStart). With a LIVE seat that has a forward route
advance, that reminder is REPLACED by the engaged form — story id, status, next
gate(s) and the satelle story set command — derived from the governing route
(sty_e16a2cd7). It ALSO runs a gate-liveness SELF-CHECK: it reads the repo's
committed hook settings (.claude/settings.json / .grok/hooks/satelle.json) and,
when it can confidently see that NO PreToolUse Edit-matcher hook invokes
'satelle hook gate', it PREPENDS a LOUD warning that enforcement is not wired
(sty_949e8739). Fails OPEN: any read/resolve error injects only the reminder and
never blocks the prompt. Output is the Claude-shaped hookSpecificOutput/
additionalContext envelope for both harnesses (AC7 finding on sty_e16a2cd7).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, _ := io.ReadAll(cmd.InOrStdin())
			_ = bindSessionID(raw)
			return runHookPromptWith(cmd.OutOrStdout(), false)
		},
	}
	stopcheck := &cobra.Command{
		Use:   "stopcheck",
		Short: "Stop post-hoc detector — block finishing when the tree was edited ungated (no engaged story)",
		Long: `stopcheck is the Stop handler. It catches the incident the PreToolUse gate is
meant to prevent even if that gate never fired this session: on Stop, if the tree
has uncommitted, NON-EXEMPT changes while NO live seat exists anywhere in this
repo, it emits a Stop block ({"decision":"block","reason":…}) naming the ungated
files (sty_949e8739; top-level decision/reason, sty_5e4bc568 AC6).

The dirty check is repo-wide, so the seat question is too (sty_211d8419): a live
seat held by a SIBLING session attributes the dirty tree to that holder, and
stopcheck allows the stop with a systemMessage naming the holding story and
session. It honours stop_hook_active (never re-blocks its own block) and fails
OPEN when git is absent, the tree is clean, only exempt paths changed, or this
session holds the seat.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, _ := io.ReadAll(cmd.InOrStdin())
			// Bind identity (and, unless this process is a dispatch, publish the
			// in-loop model) before the stop question — the Stop event is one of
			// the three the scaffold now names --harness on (sty_719c4a7b AC1/AC2),
			// and stays fail-open: bindSessionID never errors.
			_ = bindSessionID(raw)
			return runHookStopcheck(raw, cmd.OutOrStdout())
		},
	}

	// Explicit harness for deny shape (sty_9e86f407): wrapper forwards
	// --harness claude|grok|pi; empty falls back to harnessFromEvent. pi
	// takes the claude deny envelope (agentcli.PreToolUseDeny gives every harness
	// without its own shape that).
	gate.Flags().StringVar(&hookHarnessFlag, "harness", "", "claude|grok|pi —deny envelope (default: sniff event)")
	// Same explicit --harness on prompt/stopcheck (sty_719c4a7b AC2): the
	// installed hook names its own harness rather than relying only on the
	// event sniff, so bindSessionID's in-loop publish stays correct even if a
	// future payload shape changes.
	prompt.Flags().StringVar(&hookHarnessFlag, "harness", "", "claude|grok|pi —in-loop publish (default: sniff event)")
	stopcheck.Flags().StringVar(&hookHarnessFlag, "harness", "", "claude|grok|pi —in-loop publish and Stop output shape (default: sniff event)")
	hook.AddCommand(context, gate, prompt, stopcheck)
	register(hook)
}

// hookHarnessFlag is set by gate/commitgate --harness (wrapper forwards the
// scaffold harness token). Empty → harnessFromEvent fallback.
var hookHarnessFlag string

// hookNoWakeFlag is set by stopcheck --no-wake: the harness adapter says this
// run ends at settle, where nothing can wake it, so the Stop hook waits for no
// gate and records a limitation for each undelivered one instead.
var hookNoWakeFlag bool

// seatInfo is a single engagement-lease view for gate denials and session inject
// (sty_1738f973). Empty ItemID means no lease row was relevant.
type seatInfo struct {
	ItemID      string
	State       string // lease target / in-flight target (messaging)
	TargetState string // immutable lease target used to authenticate a dispatched performer
	StoryStatus string // committed work-item status (step policy; sty_c21490cc)
	StateAgent  string // agent allocated to lease target / State
	// StateRework is true when the route step StateAgent performs declares a
	// rework loop (rework = { consult, rounds }). It decides whether the deny
	// text may name `satelle story rework` (sty_a7914904).
	StateRework bool
	Owner       string
	// Worktree is the git working tree the lease was engaged from, when
	// recorded (sty_c098dc2d). It is how a session picks ITS seat out of
	// several: the default owner is pid-less local@host, so co-held sibling
	// leases are indistinguishable by owner.
	Worktree    string
	SessionID   string
	Mine        bool
	AcquiredAt  time.Time
	HeartbeatAt time.Time
	Stale       bool
	InFlight    bool
	// Waiting is true for a container idling at a route-declared
	// waits_on_children step with open children (sty_7f3e6fd3). evaluateSeat
	// never returns such a seat as live or as the naming pick; the flag is set
	// for callers that build a descriptor themselves.
	Waiting bool
	// EditCapable is true only when the committed status is a spine performing
	// step the route allocates to agent=executor. It is intentionally narrower
	// than Engaged, which also includes isolated-agent planning states.
	EditCapable bool
	EditStates  []string
	// DispatchAgents is the route-authored performer identity by target state.
	// Lease.State intentionally remains the last committed state across a new
	// transition, so an in-flight child marker must be checked against the spec,
	// not mistaken for that prior state.
	DispatchAgents map[string][]string
	// Engaged is true when this row qualifies as live engagement (performing
	// committed status, or fresh in-flight mid-transition).
	Engaged bool
	// Advance are route-declared forward transitions from the seat's committed
	// status — terminal, park, and back-edges excluded (sty_e16a2cd7). Empty when
	// the seat is live only via InFlight, or when no forward target survives.
	Advance []wfdot.Advance
}

// storyEngaged reports whether a LIVE engagement seat is active (sty_8426b9c0 /
// sty_1738f973). A bare engagement_lease row is not enough: stale heartbeats and
// leases whose committed story is not in a performing state do NOT open the gate.
// Fails CLOSED on store open/query errors. Does NOT heartbeat (Stop is out of
// scope for sty_3bb1d8be).
func storyEngaged() (bool, error) {
	_, engaged, err := currentSeat()
	return engaged, err
}

// currentSeat resolves the engagement seat for gate/session surfaces without
// refreshing the lease heartbeat. Prefer currentSeatTouch from hook handlers
// that observe ongoing work (gate, commitgate, prompt).
func currentSeat() (info seatInfo, engaged bool, err error) {
	return resolveSeat(false, config.ResolveSession())
}

// currentSeatTouch is currentSeat plus a fail-open heartbeat of a live
// owner-held seat (sty_3bb1d8be). Activity keeps the seat alive; the write
// never changes a verdict.
func currentSeatTouch() (info seatInfo, engaged bool, err error) {
	return resolveSeat(true, config.ResolveSession())
}

func sessionIDFromHook(raw []byte) string {
	var ev struct {
		SessionID      string `json:"session_id"`
		SessionIDCamel string `json:"sessionId"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return ""
	}
	for _, s := range []string{ev.SessionID, ev.SessionIDCamel} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// bindSessionID is the hook identity: prefer SATELLE_SESSION (dispatch and
// operator stamp) so a performer inherits the lease stamp; otherwise take the
// harness payload and publish it for a later Acquire in this process tree.
//
// The identity is always bound — a dispatched performer still needs the lease
// stamp — but the in-loop MODEL publish is skipped for a dispatched process
// (sty_719c4a7b AC4): SATELLE_SESSION is inherited from the driver that
// dispatched it, so an unguarded publish here would overwrite the DRIVER's own
// in-loop file with the dispatch's harness/model instead of recording
// anything about the dispatch itself.
func bindSessionID(raw []byte) string {
	dispatched := isDispatchedProcess()
	if id := config.SessionFromEnv(); id != "" {
		config.PublishSession(id)
		if !dispatched {
			publishInLoopModel(raw, id)
		}
		return id
	}
	if id := sessionIDFromHook(raw); id != "" {
		config.PublishSession(id)
		if !dispatched {
			publishInLoopModel(raw, id)
		}
		return id
	}
	return config.ResolveSession()
}

// isDispatchedProcess reports whether this process is a satelle-dispatched
// agent/reviewer/live-session/step-summary rather than the in-loop driving
// session (sty_719c4a7b AC4): agentstep.Invoke and OpenSessionAsWithModel set
// SATELLE_DISPATCH_AGENT/STEP/ITEM on every child they spawn, and every
// request satelle's buildRequest assembles — including a step-summary run,
// which carries no agent/step/item three-tuple of its own — also carries
// SATELLE_DISPATCH_SPAWN (config.SpawnEnv). Either marker sits alongside the
// inherited SATELLE_SESSION stamp bindSessionID otherwise treats as this
// process's own identity.
func isDispatchedProcess() bool {
	for _, k := range []string{config.DispatchAgentEnv, config.DispatchStepEnv, config.DispatchItemEnv, config.SpawnEnv} {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			return true
		}
	}
	return false
}

// publishInLoopModel captures this hook invocation's caller model as the
// in-loop session tier config.SelectModel's inherited/creator resolution
// reads (sty_7069bced / epic:model-selection order:3). Runs on every hook
// invocation that (re)binds a session identity — unconditional, not gated
// behind gate.no_implement_models the way resolveCaller's ONLY other caller is.
//
// Harness is derived the same way a PreToolUse deny already picks one
// (hookHarnessFlag, else harnessFromEvent sniffing the event envelope) —
// never hardcoded to "claude", so a wrapper or a non-Claude harness is not
// misreported as Claude's own executable (architecture review advisory,
// sty_7069bced). Best-effort and fail-open: an unresolvable model publishes
// "unknown" (plus an adapter-named reason, sty_719c4a7b AC6) rather than
// skipping the write, so the resolver sees a definite "nothing reported"
// instead of stale/absent data.
func publishInLoopModel(raw []byte, sessionID string) {
	h := hookHarnessFlag
	if h == "" {
		h = harnessFromEvent(raw)
	}
	model, reason := resolveInLoopModel(raw, h)
	config.PublishSessionModel(sessionID, verb.SessionModelRoleInLoop, model, h, reason)
}

// resolveInLoopModel is publishInLoopModel's pure half: the caller's reported
// model when resolveCaller finds one, else the explicit "unknown" marker plus
// the adapter-named reason from agentcli's capability table for harness
// (sty_719c4a7b AC6) — never a silent zero, never a Claude default
// (satelle-agent-agnostic §2).
func resolveInLoopModel(raw []byte, harness string) (model, reason string) {
	if m := strings.TrimSpace(resolveCaller(raw, osCallerFS{}).Model); m != "" {
		return m, ""
	}
	return "unknown", agentcli.ReasonForNoModel(harness)
}

// resolveSeat is the shared seat lookup. When touch is true and a seat is
// bound (stamped-other returns empty), heartbeat_at is refreshed (fail-open)
// so unstamped tree-routed seats still stay alive.
//
// This is the SESSION-scoped answer every gate uses. Do not widen it: a
// repo-wide allow in runHookGate would let any session edit under a sibling's
// story. The repo-wide set is exposed separately by resolveSeats for the ONE
// surface whose question is repo-wide — stopcheck (sty_211d8419).
func resolveSeat(touch bool, sessionID string) (info seatInfo, engaged bool, err error) {
	info, engaged, _, err = resolveSeats(touch, sessionID)
	return info, engaged, err
}

// resolveSeats is resolveSeat plus the repo-wide LIVE seat set the same
// computation already produced (sty_211d8419): every qualifying seat in this
// repo, whoever holds it, before pickSessionSeat narrows to this session. live
// is nil on the derived-status fallback and the ungoverned-repo early return.
func resolveSeats(touch bool, sessionID string) (info seatInfo, engaged bool, live []seatInfo, err error) {
	return resolveSeatsFor(touch, sessionID, "")
}

// resolveSeatsFor is resolveSeats for an event with a target: when the session
// holds seats in several worktrees, targetTree (the git root of the path being
// edited, "" when it has none) picks among them. It is the ONE body behind every
// seat lookup, so no caller attributes by store order (sty_42231b74).
func resolveSeatsFor(touch bool, sessionID, targetTree string) (info seatInfo, engaged bool, live []seatInfo, err error) {
	a, openErr := app.Open()
	if openErr != nil {
		return seatOpenFailed(openErr)
	}
	defer func() { _ = a.Close() }()
	return resolveSeatsOn(a, touch, sessionID, targetTree)
}

// seatOpenFailed is the seat answer for a store that would not open.
func seatOpenFailed(openErr error) (info seatInfo, engaged bool, live []seatInfo, err error) {
	// An ungoverned repo has no seat to determine — a session opened in an
	// ordinary non-satelle directory must go INERT, not error (sty_20a7824c).
	// This is strictly quieter than before: previously such a session paid
	// for a materialised runtime plane and then got a "fix config and retry"
	// it could not act on. Any other open failure still reports.
	if errors.Is(openErr, app.ErrNotInitialised) {
		return seatInfo{}, false, nil, nil
	}
	return seatInfo{}, false, nil, fmt.Errorf("cannot determine engagement (store open failed: %w) — fix config and retry", openErr)
}

// resolveSeatsOn is resolveSeatsFor on an App the caller opened and closes.
func resolveSeatsOn(a *app.App, touch bool, sessionID, targetTree string) (info seatInfo, engaged bool, live []seatInfo, err error) {
	ctx := context.Background()
	wfs, werr := a.Store.DocIndex.List(ctx, "workflows")
	if werr != nil {
		return seatInfo{}, false, nil, fmt.Errorf("cannot determine engagement (workflow list failed: %w) — fix config and retry", werr)
	}
	items, lerr := a.Store.Stories.List(ctx, workitem.ListFilter{})
	if lerr != nil {
		return seatInfo{}, false, nil, fmt.Errorf("cannot determine engagement (story list failed: %w) — fix config and retry", lerr)
	}
	if a.Store.Leases == nil {
		// Pre-migration or incomplete bootstrap: fall back to derived status scan.
		info, engaged, err = derivedSeat(items, wfs)
		return info, engaged, nil, err
	}
	leases, qerr := a.Store.Leases.List(ctx)
	if qerr != nil {
		return seatInfo{}, false, nil, fmt.Errorf("cannot determine engagement (lease query failed: %w) — fix config and retry", qerr)
	}
	live, other, eerr := evaluateSeat(leases, items, wfs, time.Now().UTC())
	if eerr != nil {
		return seatInfo{}, false, nil, eerr
	}
	if len(live) > 0 {
		// The seatless performing stories only matter to an unstamped session
		// (sty_fbbb4aee AC2): a stamped one resolves by id or worktree alone.
		var dropped []seatInfo
		if strings.TrimSpace(sessionID) == "" {
			dropped = droppedSeatsFrom(items, wfs, leases, time.Now().UTC())
		}
		// A dispatched performer is attributed to its own dispatch (sty_8d7d1c45):
		// the item its marker names, before any session or worktree guess.
		pick, mine := dispatchSeat(live, currentDispatchMarker())
		if pick.ItemID == "" {
			pick, mine = pickSessionSeatFor(live, dropped, sessionID, targetTree)
		}
		if pick.ItemID == "" {
			return other, false, live, nil
		}
		pick.Mine = mine
		if touch {
			touchSeat(ctx, a.Store.Leases, pick)
		}
		return pick, true, live, nil
	}
	return other, false, live, nil
}

// stopcheckSeat is the repo-wide engagement answer stopcheck compares against
// its repo-wide dirty check (sty_211d8419): mine when THIS session holds a live
// seat (engaged, today's allow); otherwise the first live seat held elsewhere
// in this repo with the count of any further ones; otherwise none. It never
// touches a heartbeat — Stop observes, it does not work.
func stopcheckSeat() (mine bool, other seatInfo, extra int, err error) {
	_, engaged, live, err := resolveSeats(false, config.ResolveSession())
	if err != nil {
		return false, seatInfo{}, 0, err
	}
	if engaged {
		return true, seatInfo{}, 0, nil
	}
	if len(live) == 0 {
		return false, seatInfo{}, 0, nil
	}
	return false, live[0], len(live) - 1, nil
}

// dispatchSeat selects the live seat a dispatched performer is running under,
// by the one identity the dispatch carries: the item it was spawned for. It
// inherits the driver's session id, so pickSessionSeat can hand it a sibling's
// seat or — for an unstamped ambiguous session — none at all; the marker names
// the item outright, so there is nothing to disambiguate. It only SELECTS the
// performer's own lease: permission still needs that lease in flight on exactly
// this item and a route allocation to the marked agent (editPermitted). No
// marker, or no live seat for its item, returns the zero seat and the caller
// falls through to the session pick.
func dispatchSeat(live []seatInfo, marker dispatchMarker) (seatInfo, bool) {
	if marker.Item == "" {
		return seatInfo{}, false
	}
	for _, s := range live {
		if s.ItemID == marker.Item {
			return s, false
		}
	}
	return seatInfo{}, false
}

// pickSessionSeat chooses which live seat is THIS session's. A matching
// SessionID is identity (mine=true). A different non-empty SessionID is
// skipped. Unstamped seats still tree-route with mine=false — that is
// today's permission path, not ownership. An UNSTAMPED session that no
// worktree binds, facing more than one performing story — live seats plus the
// performing stories that hold none (dropped) — gets no pick at all
// (sty_fbbb4aee AC2): live[0] would attribute the edit to whichever seat the
// store listed first, so the caller reports the ambiguity instead. dropped may
// be nil where the caller has no seatless set to offer.
func pickSessionSeat(live, dropped []seatInfo, sessionID string) (seatInfo, bool) {
	return pickSessionSeatFor(live, dropped, sessionID, "")
}

// pickSessionSeatFor is pickSessionSeat for an event with a target tree. One
// session may hold stamped seats in several worktrees (one per epic child); the
// store lists them in no meaningful order, so with two or more the seat is chosen
// by tree, never by position: the seat whose worktree is targetTree (when the
// target sits in a git tree), else the seat whose worktree is the session's own
// tree, else none — the caller reports the session's seats instead of guessing
// (sty_42231b74). Exactly one stamped seat resolves as it always did.
//
// One session may also hold several seats in the SAME tree — a task run that
// moved a story into a performing state, then re-engaged its own execution
// (sty_7f93ae45). Then the most recently acquired of that tree's seats is the
// engagement the session works under now; an exact tie on AcquiredAt is no
// pick, for the same reason position never is.
func pickSessionSeatFor(live, dropped []seatInfo, sessionID, targetTree string) (seatInfo, bool) {
	if len(live) == 0 {
		return seatInfo{}, false
	}
	id := strings.TrimSpace(sessionID)
	if id != "" {
		switch mine := sessionSeats(live, id); len(mine) {
		case 0:
		case 1:
			return mine[0], true
		default:
			for _, tree := range []string{targetTree, sessionWorktree()} {
				if tree == "" {
					continue
				}
				var inTree []seatInfo
				for _, s := range mine {
					if sameTree(s.Worktree, tree) {
						inTree = append(inTree, s)
					}
				}
				if len(inTree) > 0 {
					if s, ok := latestAcquired(inTree); ok {
						return s, true
					}
					return seatInfo{}, false
				}
			}
			return seatInfo{}, false
		}
		tree := sessionWorktree()
		for _, s := range live {
			if s.SessionID != "" && s.SessionID != id {
				continue
			}
			if tree != "" && s.Worktree == tree {
				return s, false
			}
		}
		return seatInfo{}, false
	}
	if tree := sessionWorktree(); tree != "" {
		for _, s := range live {
			if s.Worktree == tree {
				return s, false
			}
		}
	}
	if unstampedAmbiguous(live, dropped, sessionID) {
		return seatInfo{}, false
	}
	return live[0], false
}

// latestAcquired is the seat with the latest AcquiredAt, or none when two seats
// share that latest instant (sty_7f93ae45). One seat is returned as it is.
func latestAcquired(seats []seatInfo) (seatInfo, bool) {
	if len(seats) == 0 {
		return seatInfo{}, false
	}
	best, tied := seats[0], false
	for _, s := range seats[1:] {
		switch {
		case s.AcquiredAt.After(best.AcquiredAt):
			best, tied = s, false
		case s.AcquiredAt.Equal(best.AcquiredAt):
			tied = true
		}
	}
	if tied {
		return seatInfo{}, false
	}
	return best, true
}

// sessionSeats is the live seats stamped with this session id, in store order.
func sessionSeats(live []seatInfo, sessionID string) []seatInfo {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return nil
	}
	var mine []seatInfo
	for _, s := range live {
		if s.SessionID == id {
			mine = append(mine, s)
		}
	}
	return mine
}

// pushBranchTree is the working tree an unmoved `git push <remote> <branch>`
// publishes from: the worktree of base's repository that has the pushed branch
// checked out, so a session holding seats in several worktrees attributes the push
// to the seat of the one it publishes. "" when the push names no branch, several
// that sit in different trees, or one no worktree has checked out, so the tree the
// hook runs in decides as it always did.
func pushBranchTree(command, base string) string {
	branches := gitPushBranches(command)
	if len(branches) == 0 || strings.TrimSpace(base) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tree := ""
	for _, ref := range branches {
		co := worktree.CheckoutOf(ctx, base, ref)
		if co == "" || (tree != "" && !sameTree(tree, co)) {
			return ""
		}
		tree = co
	}
	return treeOf(tree)
}

// sameTree reports whether two git working-tree paths name one directory.
func sameTree(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// sessionSeatsUnmatched is the deny for a session that holds two or more live
// seats when none of them is in the tree the event targets or the tree the
// session runs in: it names every seat instead of attributing the event to one
// (sty_42231b74 AC3). ok is false when the session was attributed, holds fewer
// than two seats, or the event belongs to a dispatched performer or a rework
// relay (their own denials already name their identity).
func sessionSeatsUnmatched(engaged bool, live []seatInfo, sessionID string, dm dispatchMarker, rm relayMarker) (string, bool) {
	if engaged || dm.Item != "" || rm.Binding != "" {
		return "", false
	}
	mine := sessionSeats(live, sessionID)
	if len(mine) < 2 {
		return "", false
	}
	return sessionSeatsAmbiguousReason(mine, sessionWorktree()), true
}

// sessionSeatsAmbiguousReason names each of a session's seats with its status and
// worktree, and the tree the event was attributed against.
func sessionSeatsAmbiguousReason(seats []seatInfo, tree string) string {
	parts := make([]string, 0, len(seats))
	for _, s := range seats {
		parts = append(parts, fmt.Sprintf("%s (status %q) in worktree %q", s.ItemID, s.StoryStatus, s.Worktree))
	}
	return fmt.Sprintf(
		"satelle: this session holds %d seats — %s — and none is in the tree this event runs in (%q), so it is not attributed to any of them; act from the worktree of the seat you mean. Inspect with `satelle story seat`.",
		len(seats), strings.Join(parts, "; "), tree)
}

// unstampedAmbiguous reports whether nothing binds this session to one
// performing story: no session id, no live seat for this worktree, and more than
// one performing story (live plus seatless). One definition shared by the
// chooser and every deny that must report the ambiguity rather than a pick
// (sty_fbbb4aee AC2).
func unstampedAmbiguous(live, dropped []seatInfo, sessionID string) bool {
	if strings.TrimSpace(sessionID) != "" {
		return false
	}
	if tree := sessionWorktree(); tree != "" {
		for _, s := range live {
			if s.Worktree == tree {
				return false
			}
		}
	}
	return len(live)+len(dropped) > 1
}

// sessionWorktreeMemo holds sessionWorktree's answer for one hook call. The
// seat read asks for it several times (unstamped, unmatched or multi-seat
// sessions), and each ask would otherwise start `git rev-parse --show-toplevel`.
// Off unless held, so nothing outside a hook call sees a stale answer.
var sessionWorktreeMemo struct {
	mu      sync.Mutex
	holders int
	set     bool
	tree    string
}

// holdSessionWorktree memoises sessionWorktree until the returned release is
// called. A hook call does not change directory, so the answer cannot move.
func holdSessionWorktree() (release func()) {
	sessionWorktreeMemo.mu.Lock()
	sessionWorktreeMemo.holders++
	sessionWorktreeMemo.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			sessionWorktreeMemo.mu.Lock()
			defer sessionWorktreeMemo.mu.Unlock()
			if sessionWorktreeMemo.holders--; sessionWorktreeMemo.holders == 0 {
				sessionWorktreeMemo.set, sessionWorktreeMemo.tree = false, ""
			}
		})
	}
}

// sessionWorktree resolves the git working tree this hook process runs in.
// Empty when git cannot answer — callers then fall back rather than guess.
var sessionWorktree = func() string {
	sessionWorktreeMemo.mu.Lock()
	held, set, tree := sessionWorktreeMemo.holders > 0, sessionWorktreeMemo.set, sessionWorktreeMemo.tree
	sessionWorktreeMemo.mu.Unlock()
	if set {
		return tree
	}
	tree = sessionWorktreeUncached()
	if held {
		sessionWorktreeMemo.mu.Lock()
		if sessionWorktreeMemo.holders > 0 {
			sessionWorktreeMemo.set, sessionWorktreeMemo.tree = true, tree
		}
		sessionWorktreeMemo.mu.Unlock()
	}
	return tree
}

func sessionWorktreeUncached() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	// Inside a hook call the repo identity is already resolved by the one
	// `git rev-parse --git-common-dir`; the toplevel comes from it and the
	// `.git` entry above dir, so the seat read starts no process of its own. Only
	// a tree that identity cannot confirm asks git.
	if tree, ok := config.WorktreeOfMemoised(dir); ok {
		return tree
	}
	out, gerr := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if gerr != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// touchSeat refreshes heartbeat_at for a live, owner-held seat (sty_3bb1d8be).
// Activity keeps the seat alive; the write is a side effect of observation.
// Never revives a stale lease (callers pass evaluateSeat's live pick, which is
// non-stale by construction) and never touches another owner's row. Fail-open
// by construction: every error is discarded.
func touchSeat(ctx context.Context, ls *lease.Store, info seatInfo) {
	if ls == nil || info.ItemID == "" || info.Stale {
		return
	}
	owner := lease.ResolveOwner()
	if info.Owner != owner {
		return
	}
	_ = seatHeartbeat(ctx, ls, info.ItemID, owner)
}

// seatHeartbeat is the heartbeat write, injectable so the fail-open path is
// testable (same idiom as lease.PidAlive / healthzOK).
var seatHeartbeat = func(ctx context.Context, ls *lease.Store, itemID, owner string) error {
	return ls.Heartbeat(ctx, itemID, owner)
}

// evaluateSeat is the pure engagement predicate (sty_1738f973 AC2). A lease L is
// a live seat iff lease.Alive(L) AND (committed status is a NonTerminalEngaging
// state of the item's governing workflow OR (L.InFlight AND non-stale)). The
// InFlight+fresh disjunct preserves the acquire-at-start window for a legit
// first-transition dispatch; the residual sub-TTL orphan gap is the documented
// cost (Acquire steals after HeartbeatTTL).
//
// Returns (live, other, err): live is EVERY qualifying seat (Engaged=true), in
// lease order — a project may hold more than one under a shared arbitration key
// (sty_c098dc2d), and "is work in flight?" is len(live) > 0 whether that is one
// lease or five. other is the most relevant non-qualifying lease for messaging
// (stale first, else any row), with Engaged=false. err is non-nil when a leased
// item has no resolving workflow (fail-closed, same as anyEngaged).
func evaluateSeat(leases []lease.Lease, items []workitem.Item, wfs []docindex.Doc, now time.Time) (live []seatInfo, other seatInfo, err error) {
	byID := make(map[string]workitem.Item, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	// The front door resolves the lifecycle per item, because a DERIVED route
	// depends on the item's category and tags, not only on a workflow name — a
	// name-keyed cache would hand one story another's route (sty_9835070d).
	var otherPick seatInfo
	for _, l := range leases {
		stale := !lease.Alive(l, now)
		info := seatInfo{
			ItemID:      l.ItemID,
			State:       l.State,
			Owner:       l.Owner,
			AcquiredAt:  l.AcquiredAt,
			HeartbeatAt: l.HeartbeatAt,
			Stale:       stale,
			// EffectiveInFlight ages stuck flags; raw InFlight would wedge the
			// edit gate forever when hooks keep the heartbeat fresh (sty_bf797fa9).
			InFlight: lease.EffectiveInFlight(l, now),
		}
		info.Worktree = l.Worktree
		info.SessionID = l.SessionID
		if stale {
			// Prefer naming a stale holder in deny/session text.
			if otherPick.ItemID == "" || (!otherPick.Stale && stale) {
				otherPick = info
			}
			continue
		}
		it, ok := byID[l.ItemID]
		if !ok {
			// Leased id not in stories/tasks list — treat as non-performing residue.
			if otherPick.ItemID == "" {
				otherPick = info
			}
			continue
		}
		// Prefer the committed item status for the performing check; fall back to
		// the lease's last-committed state when the item row is somehow missing a
		// status (should not happen for real rows).
		status := it.Status
		if status == "" {
			status = l.State
		}
		info.StoryStatus = status
		// Surface lease.State (last committed engaging target / in-flight target)
		// in the seat descriptor when more informative than committed backlog.
		if l.State != "" {
			info.State = l.State
		}
		// Also expose the committed status when it differs (e.g. lease state=plan
		// while story is still backlog mid-transition) by preferring lease state
		// for messaging, but judging on committed status.
		route, wfName, serr := wfgovern.RouteFor(wfs, it)
		if serr != nil {
			return nil, seatInfo{}, fmt.Errorf("item %s: %w — cannot determine engagement", it.ID, serr)
		}
		spec := route.Spec
		if _, known := spec.StateAgent(status); !known {
			return nil, seatInfo{}, fmt.Errorf(
				"item %s status %q is not declared by workflow %s — cannot classify edit permission",
				it.ID, status, wfName)
		}
		info.EditCapable = spec.IsEditCapableState(status)
		info.EditStates = spec.EditCapableStates()
		info.DispatchAgents = dispatchAgents(spec)
		target := info.State
		if target == "" {
			target = status
		}
		info.TargetState = target
		var targetKnown bool
		info.StateAgent, targetKnown = spec.StateAgent(target)
		info.StateRework = stepDeclaresRework(route.Reworks, target)
		if !targetKnown {
			return nil, seatInfo{}, fmt.Errorf(
				"lease for item %s targets state %q not declared by workflow %s — cannot classify edit permission",
				it.ID, target, wfName)
		}
		engaging := map[string]bool{}
		for _, s := range spec.NonTerminalEngagingStates() {
			engaging[s] = true
		}
		// A container waiting on its children is not performing: leave it out of
		// live AND out of the deny/session naming (sty_7f3e6fd3). A transition in
		// flight on it is real work, so that case still counts.
		if !info.InFlight && waitsOnOpenChildren(it, status, spec, items, wfs) {
			info.Waiting = true
			continue
		}
		// Live seat: committed status is performing, OR effective in-flight mid-transition.
		// Use info.InFlight (EffectiveInFlight) not raw l.InFlight — a dead
		// transitioning pid must not keep the seat "engaged" via residue
		// (sty_bf797fa9 AC3).
		// Effective in-flight covers the acquire-at-start window when status is
		// still the start state (backlog) — without this, mid-dispatch edits
		// would be denied.
		if engaging[status] || info.InFlight {
			info.Engaged = true
			// Prefer the committed status in the descriptor when it is performing;
			// otherwise keep lease.State (target of the in-flight transition).
			if engaging[status] {
				info.State = status
				// Advance only when committed status is genuinely performing —
				// in-flight-only seats sit at a not-yet-committed state (sty_e16a2cd7).
				info.Advance = spec.AdvanceOptions(status)
			}
			live = append(live, info)
			continue
		}
		if otherPick.ItemID == "" {
			otherPick = info
		}
	}
	return live, otherPick, nil
}

// dispatchMarker identifies an isolated performer spawned by the workflow.
// Environment markers are deliberately an honest-posture boundary rather than
// a sandbox: the harness hook is the enforcement point, and bypassing/spoofing
// that hook remains visible operator misconduct.
type dispatchMarker struct {
	Agent string
	Step  string
	Item  string
}

func currentDispatchMarker() dispatchMarker {
	return dispatchMarker{
		Agent: os.Getenv(config.DispatchAgentEnv),
		Step:  os.Getenv(config.DispatchStepEnv),
		Item:  os.Getenv(config.DispatchItemEnv),
	}
}

// relayMarker identifies a rework-relay coder spawn to the PreToolUse hooks
// (sty_7567f047). Distinct from dispatchMarker: that branch requires InFlight,
// and the relay runs at a committed status. Honest-posture boundary — same as
// SATELLE_DISPATCH_*: a process that spoofs the env is outside the contract.
type relayMarker struct {
	Binding string
	Item    string
}

func currentRelayMarker() relayMarker {
	return relayMarker{
		Binding: os.Getenv(config.RelayBindingEnv),
		Item:    os.Getenv(config.RelayItemEnv),
	}
}

// hookEditPermitted is the PreToolUse permission predicate for gate and
// commitgate. A non-empty relay marker takes the allocated-binding rule at a
// committed status (dispatchedPerformerPermitted); otherwise today's
// editPermitted branches apply unchanged for the driving session and for
// in-flight dispatch.
func hookEditPermitted(info seatInfo, dm dispatchMarker, rm relayMarker) bool {
	if rm.Binding != "" {
		return info.ItemID == rm.Item && dispatchedPerformerPermitted(info, rm.Binding)
	}
	return editPermitted(info, dm)
}

// editPermitted separates lease engagement from source-edit authorization.
// A dispatched performer is allowed only for the exact in-flight item/target
// and authored agent allocation. The driving session is allowed only in a
// committed route step allocated to the in-loop executor, never mid-transition.
func editPermitted(info seatInfo, marker dispatchMarker) bool {
	if info.ItemID == "" || info.Stale || !info.Engaged {
		return false
	}
	if marker.Agent != "" || marker.Step != "" || marker.Item != "" {
		agents := info.DispatchAgents[marker.Step]
		if len(agents) == 0 {
			// Compatibility for legacy/derived pure callers that do not carry a
			// parsed-spec dispatch map.
			target := info.TargetState
			if target == "" {
				target = info.State
			}
			if marker.Step == target {
				agents = []string{info.StateAgent}
			}
		}
		if marker.Item != info.ItemID {
			return false
		}
		if info.InFlight {
			return slices.Contains(agents, marker.Agent)
		}
		// Not mid-transition: the dispatch entered its step and the status has
		// committed (or the in-flight mark aged out under a long run). The seat is
		// correct, so the performer the route allocates to that committed step may
		// edit — the same rule the rework relay's coder gets (sty_7f3e6fd3).
		status := info.StoryStatus
		if status == "" {
			status = info.State
		}
		return marker.Step == status && dispatchedPerformerPermitted(info, marker.Agent)
	}
	return !info.InFlight && info.EditCapable
}

// dispatchedPerformerPermitted reports whether BINDING may mutate the tree from
// a live session satelle itself opened on the seat — the rework relay's coder
// (sty_8e0b29a0). It is a third branch of the same policy, not a fork of it.
//
// editPermitted cannot answer this question. With an empty marker it takes the
// driving-session branch, whose EditCapable is Spec.IsEditCapableState — true
// only for agent="executor" — and OpenSession refuses an in-loop binding, so a
// relay coder is ALWAYS a named live binding and would be denied every mutator
// in exactly the configuration the relay exists for. With a marker it requires
// InFlight, and the relay runs at a committed status, not mid-transition.
//
// So the rule is stated directly: a live, non-stale seat that is NOT
// mid-transition, whose COMMITTED status the route allocates to that binding.
// The allocation is read from info.DispatchAgents — route-authored, never
// compiled in — and for binding "executor" it agrees with EditCapable by
// construction, so the in-loop policy is unchanged.
func dispatchedPerformerPermitted(info seatInfo, binding string) bool {
	if binding == "" || info.ItemID == "" || info.Stale || !info.Engaged || info.InFlight {
		return false
	}
	status := info.StoryStatus
	if status == "" {
		status = info.State
	}
	agents := info.DispatchAgents[status]
	if len(agents) == 0 && status == info.State {
		// Same compatibility path editPermitted takes for legacy/derived pure
		// callers that carry no parsed-spec dispatch map.
		agents = []string{info.StateAgent}
	}
	return slices.Contains(agents, binding)
}

// waitsOnOpenChildren reports whether it is a container idling at a step its
// route declares `waits_on_children` while at least one child is still open
// (sty_7f3e6fd3). Such a story holds a status but performs nothing, so it is
// not a performing seat holder: no edit is allowed under it, and its seat must
// not stop an unrelated session, nor be named in a deny message.
//
// A child is resolved when its OWN route says terminal, or that it sits in a
// cancel sink; a parked child (blocked) is still open. A child whose workflow
// cannot be resolved does not keep the container waiting — the container then
// counts as performing, exactly as it did before this rule. Which step waits is
// the route's declaration; no status or category name appears here.
func waitsOnOpenChildren(it workitem.Item, status string, spec wfdot.Spec, items []workitem.Item, wfs []docindex.Doc) bool {
	return false
}

func dispatchAgents(spec wfdot.Spec) map[string][]string {
	out := make(map[string][]string, len(spec.States))
	for _, state := range spec.States {
		// Only the SPINE allocation dispatches (sty_05a5e203): entry no longer
		// fires an agent, so a state allocates at most one dispatch target.
		if state.Agent != "" && !slices.Contains(out[state.Name], state.Agent) {
			out[state.Name] = append(out[state.Name], state.Agent)
		}
	}
	return out
}

// seatSuffix appends a discoverable seat descriptor to a gate deny reason when
// a non-live lease row exists (sty_1738f973 AC6). Empty when no row to name.
func seatSuffix(info seatInfo, now time.Time) string {
	if info.ItemID == "" {
		return ""
	}
	return " — " + formatSeat(info, now) + "; inspect with `satelle story seat`, release with `satelle story seat release " + info.ItemID + "`"
}

// formatSeat renders a one-line seat descriptor for deny reasons and session inject.
func formatSeat(info seatInfo, now time.Time) string {
	if info.ItemID == "" {
		return ""
	}
	state := info.State
	if state == "" {
		state = "?"
	}
	age := formatSeatAge(now.Sub(info.HeartbeatAt))
	if info.Stale {
		return fmt.Sprintf("seat held by %s (%s, stale %s)", info.ItemID, state, age)
	}
	acq := formatSeatAge(now.Sub(info.AcquiredAt))
	return fmt.Sprintf("seat held by %s (%s, owner %s, acquired %s, heartbeat %s)",
		info.ItemID, state, info.Owner, acq, age)
}

// formatSeatAge rounds a duration to a short human age for seat messaging.
func formatSeatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dd", int(d.Hours())/24)
}

// seatModeLine states the ACTIVE seat concurrency mode as mechanism: what the
// mode is, and — for a grouped seat — the two preconditions a second engagement
// must satisfy. An agent that only meets the mode in a refusal has already lost a
// turn to it. Nothing here says when a repo should choose a mode: that opinion is
// the repo's own substrate, and satelle polices no sibling contention.
func seatModeLine(mode string) string {
	if mode == config.ParallelWorktree {
		return "seat mode: worktree — any story may engage alongside the holders, from a DISTINCT git working tree; a second engagement from a tree that already holds a seat is refused (the children of a sequential epic still go one at a time)."
	}
	if mode == config.ParallelEpic {
		return "seat mode: epic — a sibling under the SAME parent may engage alongside the holder, from a DISTINCT git working tree; a story under another parent (or none) is refused while that seat is held."
	}
	return "seat mode: none — one performing story at a time."
}

// scheduleLine names the child schedule of an epic container and the
// command that answers who may start. It formats a string only: it never reads
// children, never derives a wave and never names a child — which child starts is
// `satelle story wave`'s answer, not this line's. The worktree clause is stated
// only for the parallel schedule, where it applies.
func scheduleLine(id, schedule string) string {
	if schedule == "" {
		return ""
	}
	line := fmt.Sprintf("epic %s schedule: %s — the set that may be engaged is `satelle story wave %s`; a non-zero wave is a stop, not a licence to choose a child.", id, schedule, id)
	if schedule == wfdot.SchedParallel {
		line += " A parallel wave is one worktree per child; same-tree engagement is refused."
	}
	return line
}

// seatScheduleLines names the schedule of every scheduled epic container in
// context: a seat holder that is itself an epic-parent, or whose parent is one.
// A container that declares nothing is parallel by default and is named so.
// Silent (empty) when no holder is in an epic container.
func seatScheduleLines(holders []string, items []workitem.Item, wfs []docindex.Doc) string {
	return ""
}

// formatSeatBlock is the SessionStart / prompt multi-line seat inject body.
func formatSeatBlock(info seatInfo, now time.Time, mode string) string {
	if info.ItemID == "" {
		return ""
	}
	return "## Engagement seat\n" + formatSeat(info, now) + "\n" + seatModeLine(mode) +
		"\nInspect: `satelle story seat` · Release: `satelle story seat release " + info.ItemID + "`"
}

// appendSeatToContext merges a seat block into SessionStart content when room
// remains under ceiling (sty_1738f973 AC6). Pure — unit-tested so removing the
// inject path fails a test rather than leaving the suite green.
func appendSeatToContext(content, seatBlock string, ceiling int) string {
	if strings.TrimSpace(seatBlock) == "" {
		return content
	}
	if content == "" {
		return seatBlock
	}
	if ceiling > 0 && len(content)+2+len(seatBlock) > ceiling {
		return content // prefer principles over a truncated seat line
	}
	return content + "\n\n" + seatBlock
}

// appendSeatToPrompt appends a one-line seat descriptor to the UserPromptSubmit
// reminder when a lease exists (sty_1738f973 AC6). Pure for unit tests.
func appendSeatToPrompt(msg string, info seatInfo, now time.Time) string {
	if info.ItemID == "" {
		return msg
	}
	return msg + "\n" + formatSeat(info, now) + " — inspect: `satelle story seat`"
}

// anyEngaged reports whether any work item sits in a non-terminal engaging state
// of the workflow that governs IT — the stamped workflow, else its category-selected
// one (wfgovern.GoverningWorkflow). A "non-terminal engaging state" is one that
// is neither start (Shape Mdiamond) nor terminal (Shape Msquare) nor cancel/exception
// (agent=reviewer with no outgoing edges) — read from the Spec's own markers,
// not hardcoded (sty_f3d5d4b8).
//
// Returns (engaged, err): err is non-nil when an item has NO resolving workflow
// or the workflow does not yield a Spec — fail-closed, not a silent allow. Pure
// core, split for testing.
func anyEngaged(items []workitem.Item, wfs []docindex.Doc) (bool, error) {
	for _, it := range items {
		spec, _, _, serr := wfgovern.SpecFor(wfs, it)
		if serr != nil {
			return false, fmt.Errorf("item %s: %w — cannot determine engagement", it.ID, serr)
		}
		engaging := map[string]bool{}
		for _, s := range spec.NonTerminalEngagingStates() {
			engaging[s] = true
		}
		if engaging[it.Status] {
			return true, nil
		}
	}
	return false, nil
}

// derivedSeat is the pre-lease-store compatibility path. It derives both the
// broad engagement predicate and the narrower edit-capable predicate from the
// governing route, so an old store never re-opens planning/reviewer states merely
// because they are non-terminal.
func derivedSeat(items []workitem.Item, wfs []docindex.Doc) (seatInfo, bool, error) {
	var other seatInfo
	for _, it := range items {
		route, wfName, serr := wfgovern.RouteFor(wfs, it)
		if serr != nil {
			return seatInfo{}, false, fmt.Errorf("item %s: %w — cannot determine engagement", it.ID, serr)
		}
		spec := route.Spec
		agent, known := spec.StateAgent(it.Status)
		if !known {
			return seatInfo{}, false, fmt.Errorf(
				"item %s status %q is not declared by workflow %s — cannot classify edit permission",
				it.ID, it.Status, wfName)
		}
		info := seatInfo{
			ItemID: it.ID, State: it.Status, StoryStatus: it.Status,
			StateAgent: agent, EditCapable: spec.IsEditCapableState(it.Status),
			StateRework: stepDeclaresRework(route.Reworks, it.Status),
			EditStates:  spec.EditCapableStates(),
		}
		engaging := false
		for _, state := range spec.NonTerminalEngagingStates() {
			if state == it.Status {
				engaging = true
				break
			}
		}
		if engaging && waitsOnOpenChildren(it, it.Status, spec, items, wfs) {
			continue // a container waiting on its children is neither a seat nor a name to cite
		}
		if engaging {
			info.Engaged = true
			return info, true, nil
		}
		if other.ItemID == "" {
			other = info
		}
	}
	return other, false, nil
}

// filePathFromEvent pulls the edit target out of a PreToolUse edit event.
// Claude Code: tool_input.file_path / tool_input.notebook_path.
// Grok: toolInput.file_path | filePath | path | notebook_path | notebookPath.
// Returns "" when none is present. Prefer Claude snake_case, then Grok aliases.
func filePathFromEvent(raw []byte) string {
	var ev struct {
		ToolInputSnake struct {
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
			// Grok may nest camelCase aliases under tool_input too; accept them.
			FilePathCamel     string `json:"filePath"`
			Path              string `json:"path"`
			NotebookPathCamel string `json:"notebookPath"`
		} `json:"tool_input"`
		ToolInputCamel struct {
			FilePath          string `json:"file_path"`
			FilePathCamel     string `json:"filePath"`
			Path              string `json:"path"`
			NotebookPath      string `json:"notebook_path"`
			NotebookPathCamel string `json:"notebookPath"`
		} `json:"toolInput"`
	}
	_ = json.Unmarshal(raw, &ev)
	for _, p := range []string{
		ev.ToolInputSnake.FilePath,
		ev.ToolInputSnake.NotebookPath,
		ev.ToolInputSnake.FilePathCamel,
		ev.ToolInputSnake.Path,
		ev.ToolInputSnake.NotebookPathCamel,
		ev.ToolInputCamel.FilePath,
		ev.ToolInputCamel.FilePathCamel,
		ev.ToolInputCamel.Path,
		ev.ToolInputCamel.NotebookPath,
		ev.ToolInputCamel.NotebookPathCamel,
	} {
		if p != "" {
			return p
		}
	}
	return ""
}

// sessionAnchor returns the pinned session-home repo root for containment and
// edit-gate path checks (sty_aadd4d6c). Precedence: SATELLE_PROJECT_DIR, then
// CLAUDE_PROJECT_DIR (harness pin), then RepoRootFromConfigPath of config.Load.
// Never uses live shell CWD alone — CWD moves with the shell.
func sessionAnchor() string {
	cfgRoot := ""
	if _, cfgPath, err := config.Load(""); err == nil {
		cfgRoot = config.RepoRootFromConfigPath(cfgPath)
	}
	return anchorFrom(os.Getenv, cfgRoot)
}

// anchorFromEnv is the env-pinned part of the anchor, "" when no pin is set. It
// is the one answer to "which repo is this session anchored in", independent of
// the working directory: a gate hand-off uses it to tell the repo whose hooks
// serve the session from the repo the command happens to act on (sty_8f10499d).
func anchorFromEnv(getenv func(string) string) string {
	for _, key := range []string{"SATELLE_PROJECT_DIR", "CLAUDE_PROJECT_DIR"} {
		if p := strings.TrimSpace(getenv(key)); p != "" {
			if abs, err := filepath.Abs(p); err == nil {
				return filepath.Clean(abs)
			}
			return filepath.Clean(p)
		}
	}
	return ""
}

// anchorFrom is the pure resolver for sessionAnchor. getenv is injected for tests.
// An env pin wins over cfgRoot because config.Load walks up from CWD and is not
// trustworthy alone once a persistent shell has cd'd.
func anchorFrom(getenv func(string) string, cfgRoot string) string {
	if p := anchorFromEnv(getenv); p != "" {
		return p
	}
	if strings.TrimSpace(cfgRoot) == "" {
		return ""
	}
	if abs, err := filepath.Abs(cfgRoot); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(cfgRoot)
}

// commandAllowRestrictsWith reports whether any git subcommand is listed in the
// opt-in [gate.command_allow] policy (even if not commit/push).
func commandAllowRestrictsWith(policy map[string][]string, subs []string) bool {
	if len(policy) == 0 {
		return false
	}
	for _, sub := range subs {
		if _, ok := policy[strings.ToLower(sub)]; ok {
			return true
		}
	}
	return false
}

// commandAllowDenyWith returns a deny reason when a restricted subcommand is not
// permitted at the engaged story's current status. deny=false when allowed or
// unconfigured.
func commandAllowDenyWith(policy map[string][]string, subs []string, storyStatus string) (reason string, deny bool) {
	if len(policy) == 0 {
		return "", false
	}
	status := strings.ToLower(strings.TrimSpace(storyStatus))
	for _, sub := range subs {
		key := strings.ToLower(sub)
		allowed, restricted := policy[key]
		if !restricted {
			continue
		}
		if len(allowed) == 0 {
			// Key present with empty list = never allowed while policy is on.
			return fmt.Sprintf(
				"satelle: refusing git %s — [gate.command_allow] lists %q with no allowed states (remove the key or name permitted story statuses, e.g. push = [\"release\"])",
				sub, key), true
		}
		ok := false
		for _, a := range allowed {
			if strings.ToLower(strings.TrimSpace(a)) == status {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Sprintf(
				"satelle: refusing git %s while engaged story is at %q — [gate.command_allow] permits it only at: %s",
				sub, storyStatus, strings.Join(allowed, ", ")), true
		}
	}
	return "", false
}

// gateProcess is one load of the invoking process config. LoadInvokingProcess
// resolves the canonical repo root through `git rev-parse`, so a hook call loads
// it once and every check in that call reads this value (sty_a74551d6 AC5).
type gateProcess struct {
	proc     config.Config
	invoking string
	path     string
	err      error
	// loc, locPath and procRoot are the other halves of the same load, so the
	// store can open from it (gateStore) instead of loading the config again.
	loc      config.Config
	locPath  string
	procRoot string
}

// loadGateProcess is config.LoadInvokingProcess, keeping the location config
// and the process root it computed on the way.
func loadGateProcess() gateProcess {
	loc, locPath, lerr := config.Load("")
	if lerr != nil && !errors.Is(lerr, config.ErrNotFound) {
		return gateProcess{path: locPath, err: lerr}
	}
	proc, procRoot, path, err := config.LoadProcess(loc, locPath)
	if err != nil {
		return gateProcess{path: path, err: err}
	}
	return gateProcess{
		proc: proc, invoking: config.InvokingRoot(locPath), path: path,
		loc: loc, locPath: locPath, procRoot: procRoot,
	}
}

// gateStore is the one store a commitgate call opens for command rules, from the
// gateProcess already loaded: the seat read and the ledger row share it, so a
// matched command adds store I/O and no second config load or process.
type gateStore struct {
	gp gateProcess
	// load makes the first open load the process config itself (a call that may
	// never need the store, such as the edit gate's skipped tools, pays nothing).
	load   bool
	a      *app.App
	err    error
	opened bool
}

func (g *gateStore) open() (*app.App, error) {
	if !g.opened {
		g.opened = true
		if g.load {
			g.gp = loadGateProcess()
		}
		if g.gp.err != nil {
			g.err = g.gp.err
		} else {
			g.a, g.err = app.OpenLoaded(g.gp.loc, g.gp.locPath, g.gp.proc, g.gp.procRoot)
		}
	}
	return g.a, g.err
}

func (g *gateStore) close() {
	if g.a != nil {
		_ = g.a.Close()
		g.a = nil
	}
}

// seatsFor is resolveSeatsFor on the shared store.
func (g *gateStore) seatsFor(touch bool, sessionID, targetTree string) (info seatInfo, engaged bool, live []seatInfo, err error) {
	a, oerr := g.open()
	if oerr != nil {
		return seatOpenFailed(oerr)
	}
	return resolveSeatsOn(a, touch, sessionID, targetTree)
}

// seat is resolveSeats(false, ...) on the shared store.
func (g *gateStore) seat(sessionID string) (seatInfo, bool, error) {
	a, err := g.open()
	if err != nil {
		info, engaged, _, serr := seatOpenFailed(err)
		return info, engaged, serr
	}
	info, engaged, _, serr := resolveSeatsOn(a, false, sessionID, "")
	return info, engaged, serr
}

// allowOutsideTreeEdits reports whether [gate] allow_outside_tree_edits is true.
// Default false = deny mutations in another repo's working tree (sty_a8454d10).
// Non-repo paths are never fenced. On config load failure, returns false
// (containment stays on).
func allowOutsideTreeEdits() bool {
	return allowOutsideTreeEditsFrom(loadGateProcess())
}

func allowOutsideTreeEditsFrom(gp gateProcess) bool {
	if gp.err != nil {
		return false
	}
	return gp.proc.Gate.AllowOutsideTreeEdits
}

// outsideAnchorBashReason is the agent-facing deny when a Bash mutation target
// lands in another repo's working tree (sty_a8454d10 / sty_aadd4d6c). Names the
// path and the foreign root; prescribes opening a session in THAT repo.
func outsideAnchorBashReason(path, foreignRoot string) string {
	return fmt.Sprintf(
		"satelle: refusing Bash mutation in another repo's tree (%s, root %s) — open a session in THAT repo to action it there (create stories cross-repo is fine; progressing/mutating is not). Temp/non-repo paths are not fenced. Opt-in only for a deliberate multi-repo install: [gate] allow_outside_tree_edits = true",
		path, foreignRoot)
}

// noEngagedStoryEditReason is the canonical agent-facing deny for product-code
// edits without a performing story (sty_e4902c51). Both Claude and Grok must
// surface this text — not a bare "hook denied (exit 2)".
const noEngagedStoryEditReason = "satelle: you're mutating the tree without a performing story, or you have used the wrong tool for reading. " +
	"Open a story session before editing code: satelle story create …, then satelle story set <id> --status plan. " +
	"That session stays open through your edits until the story reaches a terminal or parked state (done, cancelled, or blocked) — finishing an edit does NOT close it. " +
	"For research, use read tools (Read/read_file/grep/Glob) — not Edit/Write/search_replace."

// droppedSeatEditReason is the deny when a story is still in a performing
// status but its engagement lease is missing (sty_4f74d01f). Distinct from
// noEngagedStoryEditReason so agents re-acquire instead of creating a new story.
func droppedSeatEditReason(id, status string) string {
	return fmt.Sprintf(
		"satelle: story %s is performing (status %q, seat: none) but its engagement seat was dropped — re-acquire with `satelle story set %s --status %s` (same status is intentional; it grants a seat without inventing a new step), then retry the edit. Inspect with `satelle story seat`.",
		id, status, id, status)
}

// seatToken names a performing story's seat in one phrase for deny text
// (sty_fbbb4aee AC3): "stale", "none" (no lease — a dropped seat), or the live
// lease's session and worktree.
func seatToken(s seatInfo) string {
	switch {
	case s.Stale:
		return "stale"
	case s.Engaged:
		sess, tree := s.SessionID, s.Worktree
		if sess == "" {
			sess = "unstamped"
		}
		if tree == "" {
			tree = "unrecorded"
		}
		return fmt.Sprintf("live (session %s, worktree %s)", sess, tree)
	}
	return "none"
}

// seatMismatchEditReason is the deny when the story that would hold this edit
// has a live seat this session is not bound to (sty_fbbb4aee AC1/AC3). It names
// that story, its state and its seat, and points at the stamp or worktree that
// binds the session — never at re-acquiring a seat that is already held.
func seatMismatchEditReason(s seatInfo, sessionID, tree string) string {
	sess := strings.TrimSpace(sessionID)
	if sess == "" {
		sess = "unstamped"
	}
	seatSess := s.SessionID
	if seatSess == "" {
		seatSess = "unstamped"
	}
	var hint string
	held := " The seat is held; it does not need re-acquiring."
	switch {
	case tree == s.Worktree:
		// Already in the seat's worktree: only the id can differ.
		hint = fmt.Sprintf("this session is already in that worktree; its session id (%s) does not match the seat's (%s)", sess, seatSess)
		switch {
		case s.SessionID == "":
		case strings.TrimSpace(sessionID) == "":
			// No id resolved for the caller, so there is nothing to re-stamp the
			// seat to: the stamp has to come from an environment the hook reads.
			hint += fmt.Sprintf(" — stamp SATELLE_SESSION=%s", s.SessionID)
		case config.SessionLive(s.SessionID):
			hint += " — another live session holds the seat; inspect with `satelle story seat`, and use `satelle story stop-request` to preempt it"
		case s.Owner == lease.ResolveOwner():
			// A ghost stamp: no live session publishes it, and this caller drove
			// the story. The hook cannot see an exported SATELLE_SESSION, so the
			// remedy is the command that re-stamps the seat to the caller
			// (sty_36d6f155).
			hint += fmt.Sprintf(" and no live session holds it — re-stamp the seat to this session with `satelle story set %s --status %s` (same status), then retry the edit", s.ItemID, s.StoryStatus)
			held = ""
		default:
			hint += " and no live session holds it, but this caller did not engage the story; inspect with `satelle story seat`"
		}
	case s.SessionID != "":
		hint = fmt.Sprintf("stamp SATELLE_SESSION=%s or run from that seat's worktree", s.SessionID)
	default:
		hint = "run from that seat's worktree"
	}
	return fmt.Sprintf(
		"satelle: story %s (status %q) holds the live engagement seat — seat: %s — but this session is %s in worktree %q, so the edit is not attributed to it; %s.%s",
		s.ItemID, s.StoryStatus, seatToken(s), sess, tree, hint, held)
}

// ambiguousPerformingReason is the deny when several stories are performing and
// nothing binds this session to one of them (sty_fbbb4aee AC2): it says so and
// lists each with its state and seat, instead of picking one.
func ambiguousPerformingReason(performing []seatInfo, sessionID string) string {
	lead := "more than one performing story, no session stamped"
	if sess := strings.TrimSpace(sessionID); sess != "" {
		lead = fmt.Sprintf("more than one performing story, none holds a seat for session %s", sess)
	}
	parts := make([]string, 0, len(performing))
	for _, s := range performing {
		parts = append(parts, fmt.Sprintf("%s (status %q, seat: %s)", s.ItemID, s.StoryStatus, seatToken(s)))
	}
	return fmt.Sprintf(
		"satelle: %s — %s; stamp the session (SATELLE_SESSION=<the seat's session id>) or run from the seat's worktree. Inspect with `satelle story seat`.",
		lead, strings.Join(parts, ", "))
}

// attributedDenyReason is the pure half of editGateDenyReason: given the live
// seats and the performing stories that hold none, it names the story the edit
// would have been attributed to (sty_fbbb4aee). ok is false when nothing is
// performing. Order: the live seat for this worktree; else the only performing
// story; else the ambiguity report. It never prefers a seatless story over a
// seated one for this worktree.
func attributedDenyReason(live, dropped []seatInfo, sessionID, tree string) (reason string, ok bool) {
	if tree != "" {
		for _, s := range live {
			if s.Worktree == tree {
				return seatMismatchEditReason(s, sessionID, tree), true
			}
		}
	}
	performing := make([]seatInfo, 0, len(live)+len(dropped))
	performing = append(performing, live...)
	performing = append(performing, dropped...)
	switch len(performing) {
	case 0:
		return "", false
	case 1:
		if performing[0].Engaged {
			return seatMismatchEditReason(performing[0], sessionID, tree), true
		}
		return droppedSeatEditReason(performing[0].ItemID, performing[0].StoryStatus), true
	}
	return ambiguousPerformingReason(performing, sessionID), true
}

// editGateDenyReason picks the agent-facing text when this session resolved no
// engaged seat (sty_4f74d01f): name the performing story the edit belongs to —
// seated for this worktree first, seatless only when it is the sole candidate —
// over the generic "open a story" message.
func editGateDenyReason(info seatInfo, live []seatInfo, sessionID string, now time.Time) string {
	if reason, ok := attributedDenyReason(live, droppedPerformingSeats(), sessionID, sessionWorktree()); ok {
		return reason
	}
	return noEngagedStoryEditReason + seatSuffix(info, now)
}

const readOnlyPreflightBase = "Read-only preflight remains available: use Read/read_file/Grep/Glob or non-mutating shell commands, and record approved context with `satelle story attach` or `satelle story log`; advance through the workflow gate before editing."

// readOnlyPreflightReason names only drafting locations the live [gate]
// config actually allows. Config load failure or an empty exemption set
// promises nothing.
func readOnlyPreflightReason() string {
	return readOnlyPreflightReasonFrom(func() (config.Config, string, error) {
		proc, invoking, _, _, err := config.LoadInvokingProcess()
		if err != nil {
			return config.Config{}, "", err
		}
		// The seam derives the root with RepoRootFromConfigPath, which walks
		// two directories up. A path under the invoking tree joins exemptions
		// there. The config itself is the process of record.
		return proc, filepath.Join(invoking, config.DefaultDataDir, config.ConfigName), nil
	})
}

func readOnlyPreflightReasonFrom(load func() (config.Config, string, error)) string {
	cfg, cfgPath, err := load()
	if err != nil {
		return readOnlyPreflightBase + " No [gate] exemptions could be read — every in-repo path is gated."
	}
	root := config.RepoRootFromConfigPath(cfgPath)
	var locs []string
	seen := map[string]bool{}
	addLoc := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if root != "" && withinRoot(root, p) {
			return
		}
		seen[p] = true
		locs = append(locs, p)
	}
	for _, p := range cfg.ResolveEditExemptPaths(root) {
		addLoc(p)
	}
	// Process temp is a mechanism drafting location, independent of the
	// authored list — keep the deny advice truthful when a custom list omits /tmp/.
	for _, p := range tempDraftRoots() {
		addLoc(p)
	}
	locs = append(locs, cfg.ResolveEditExemptGlobs()...)
	if len(locs) == 0 {
		return readOnlyPreflightBase + " No [gate] exemptions are authored in this repo — every in-repo path is gated."
	}
	return readOnlyPreflightBase + " Story-reference copies belong in " + strings.Join(locs, ", ") + "."
}

func editPermissionDenyReason(info seatInfo, live []seatInfo, sessionID string, now time.Time) string {
	pre := readOnlyPreflightReason()
	if info.ItemID == "" || !info.Engaged || info.Stale {
		return editGateDenyReason(info, live, sessionID, now) + " " + pre
	}
	if info.InFlight {
		target := info.State
		if target == "" {
			target = "the next state"
		}
		if info.Mine {
			return fmt.Sprintf(
				"satelle: story %s has a transition to %q IN FLIGHT (planner/reviewer or dispatched step running); the driving session cannot edit until the transition commits. %s",
				info.ItemID, target, pre)
		}
		return fmt.Sprintf(
			"satelle: story %s has a transition to %q IN FLIGHT (planner/reviewer or dispatched step running). %s",
			info.ItemID, target, pre)
	}
	agent := info.StateAgent
	states := strings.Join(info.EditStates, ", ")
	if states == "" {
		states = "(none declared)"
	}
	if agent == "" {
		return fmt.Sprintf(
			"satelle: story %s is at %q (seat: %s), which its workflow allocates to %q; source edits are permitted only in route steps allocated to agent=executor (%s). Do not work ahead. %s",
			info.ItemID, info.StoryStatus, seatToken(info), "no in-loop executor", states, pre)
	}
	// A named performer owns this step: the driver's own edit is refused, and
	// the text names the one path that reaches that performer. The relay is
	// named only when the step declares a rework loop — naming it on a step
	// with none sends the driver at a command that refuses (sty_a7914904).
	path := fmt.Sprintf("the one-shot coder dispatch of %q performs this step — advance the edge (`satelle story set %s --status <next>`) to dispatch it", agent, info.ItemID)
	if info.StateRework {
		path = fmt.Sprintf("relay the change through the dispatched coder %q with `satelle story rework %s`", agent, info.ItemID)
	}
	return fmt.Sprintf(
		"satelle: story %s is at %q (seat: %s), which its workflow allocates to %q; source edits are permitted only in route steps allocated to agent=executor (%s). Do not edit in-loop — %s. Do not work ahead. %s",
		info.ItemID, info.StoryStatus, seatToken(info), agent, states, path, pre)
}

// stepDeclaresRework reports whether the derived route declares a rework loop
// on step.
func stepDeclaresRework(reworks []wfroute.Rework, step string) bool {
	for _, w := range reworks {
		if w.Step == step {
			return true
		}
	}
	return false
}

// hookDenyReason selects the agent-facing deny text for gate/commitgate.
// A marked relay coder gets relayDenyReason; unmarked sessions keep
// editPermissionDenyReason byte-for-byte (sty_7567f047 AC5).
func hookDenyReason(info seatInfo, live []seatInfo, dm dispatchMarker, rm relayMarker, sessionID string, now time.Time) string {
	if rm.Binding != "" {
		return relayDenyReason(info, live, rm, sessionID, now)
	}
	// A dispatched performer whose item holds no live seat is refused for THAT,
	// not for whichever other story the session happens to resolve
	// (sty_8d7d1c45). It never borrows a sibling's permission.
	if dm.Item != "" && info.ItemID != dm.Item {
		return dispatchDenyReason(dm)
	}
	return editPermissionDenyReason(info, live, sessionID, now)
}

// dispatchDenyReason names a dispatched performer whose item holds no live
// in-flight lease: the dispatch is the identity, so the reason names it.
func dispatchDenyReason(dm dispatchMarker) string {
	return fmt.Sprintf(
		"satelle: dispatch %s/%s (agent %q) has no live in-flight lease — a dispatched performer may edit only under the seat of the item it was dispatched for, and only while that transition is in flight. %s",
		dm.Item, dm.Step, dm.Agent, readOnlyPreflightReason())
}

// relayDenyReason names why a marked rework-relay coder was refused. It never
// uses the unmarked "without a performing story" or "allocated to agent=executor"
// wording — those mis-diagnose the 23:04 / 23:08 auctelle failures.
func relayDenyReason(info seatInfo, live []seatInfo, rm relayMarker, sessionID string, now time.Time) string {
	pre := readOnlyPreflightReason()
	item := strings.TrimSpace(rm.Item)
	binding := strings.TrimSpace(rm.Binding)
	sid := strings.TrimSpace(sessionID)

	if item != "" && info.ItemID != "" && info.ItemID != item {
		return fmt.Sprintf(
			"satelle: rework relay marker names story %s but this session resolved seat %s — the marker must match the live seat the relay is driving. %s",
			item, info.ItemID, pre)
	}

	if info.ItemID == "" || !info.Engaged {
		// Prefer naming a live seat for the marked item that this session did
		// not bind (the 23:04 pickSessionSeat mismatch) over the generic
		// "without a performing story" text.
		for _, s := range live {
			if s.ItemID == item && !s.Stale {
				leaseSID := strings.TrimSpace(s.SessionID)
				return fmt.Sprintf(
					"satelle: rework relay for story %s did not bind the live seat (lease session %q, this session %q) — the rework relay exports the lease's session id to its coder so pickSessionSeat can select it. %s",
					item, leaseSID, sid, pre)
			}
		}
		return fmt.Sprintf(
			"satelle: rework relay for story %s requires a live, non-stale seat at a committed status allocated to the binding — none is bound for this session. %s",
			item, pre)
	}
	if info.Stale {
		return fmt.Sprintf(
			"satelle: rework relay for story %s requires a live, non-stale seat at a committed status allocated to the binding — the seat is stale. %s%s",
			info.ItemID, pre, seatSuffix(info, now))
	}
	if info.InFlight {
		target := info.State
		if target == "" {
			target = "the next state"
		}
		return fmt.Sprintf(
			"satelle: rework relay coder cannot edit while story %s has a transition to %q in flight. %s",
			info.ItemID, target, pre)
	}

	status := info.StoryStatus
	if status == "" {
		status = info.State
	}
	agents := info.DispatchAgents[status]
	if len(agents) == 0 && status == info.State && info.StateAgent != "" {
		agents = []string{info.StateAgent}
	}
	allocated := strings.Join(agents, ", ")
	if allocated == "" {
		allocated = "(none)"
	}
	if !slices.Contains(agents, binding) {
		return fmt.Sprintf(
			"satelle: story %s is at %q, which its route allocates to [%s]; the rework relay permits edits only from the binding allocated to the committed status (this session is marked %q). %s",
			info.ItemID, status, allocated, binding, pre)
	}
	// Seat resolved and binding allocated — caller should have allowed. Keep a
	// precise fallback that still names the relay rule.
	return fmt.Sprintf(
		"satelle: rework relay for story %s refused a mutator under the live, non-stale, committed-status allocation rule for binding %q. %s",
		info.ItemID, binding, pre)
}

// droppedPerformingSeats finds every story/task whose committed status is
// performing but that has no live (non-stale) engagement lease, in store order
// (sty_4f74d01f). It returns all of them, not the first: which one an edit
// belongs to is attributedDenyReason's decision (sty_fbbb4aee).
func droppedPerformingSeats() []seatInfo {
	a, err := app.Open()
	if err != nil {
		return nil
	}
	defer func() { _ = a.Close() }()
	ctx := context.Background()
	wfs, err := a.Store.DocIndex.List(ctx, "workflows")
	if err != nil {
		return nil
	}
	items, err := a.Store.Stories.List(ctx, workitem.ListFilter{})
	if err != nil {
		return nil
	}
	var leases []lease.Lease
	if a.Store.Leases != nil {
		if ls, lerr := a.Store.Leases.List(ctx); lerr == nil {
			leases = ls
		}
	}
	return droppedSeatsFrom(items, wfs, leases, time.Now().UTC())
}

// droppedSeatsFrom is droppedPerformingSeats over an already-read store view, so
// resolveSeats can count the seatless performing stories without a second open.
func droppedSeatsFrom(items []workitem.Item, wfs []docindex.Doc, leases []lease.Lease, now time.Time) []seatInfo {
	leased := map[string]bool{}
	for _, l := range leases {
		if lease.Alive(l, now) {
			leased[l.ItemID] = true
		}
	}
	var dropped []seatInfo
	for _, it := range items {
		if leased[it.ID] {
			continue
		}
		spec, _, _, serr := wfgovern.SpecFor(wfs, it)
		if serr != nil {
			continue
		}
		engaging := false
		for _, s := range spec.NonTerminalEngagingStates() {
			if s == it.Status {
				engaging = true
				break
			}
		}
		if engaging && !waitsOnOpenChildren(it, it.Status, spec, items, wfs) {
			dropped = append(dropped, seatInfo{ItemID: it.ID, StoryStatus: it.Status, State: it.Status})
		}
	}
	return dropped
}

// noEngagedStoryCommitReason is the agent-facing deny for git commit/push without
// an engaged story. Same harness-specific emission as the edit gate. States
// PreToolUse pre-execution semantics so agents do not retry a fused engage+commit
// in one tool call (sty_577d292f / session-trace-workflow-review-followups).
const noEngagedStoryCommitReason = "satelle: refusing to commit/push with no engaged story. " +
	"This gate runs BEFORE the command executes — an engage line inside the same tool call cannot pass it. " +
	"Engage in a SEPARATE, PRIOR tool call (satelle story set <id> --status plan or --status in_progress, per the governing workflow), then run git commit/push in a later call."

// fusedEngageAndCommitReason is the deny when the payload both tries to engage
// a story and run git commit/push. Still a deny (behavior unchanged); the text
// explicitly says to split into two tool calls (sty_577d292f optional variant).
const fusedEngageAndCommitReason = "satelle: refusing fused engage+commit/push in one tool call. " +
	"commitgate evaluates BEFORE any line runs, so a same-command 'satelle story set … --status …' cannot engage for this commit/push. " +
	"Split into TWO tool calls: (1) engage alone (status plan or in_progress per the governing workflow); (2) git commit/push only after engagement succeeds."

// commitDenyReason picks the agent-facing deny text for a blocked commit/push.
// Pure string selection — gate behavior is always deny when nothing is engaged.
func commitDenyReason(command string) string {
	if isFusedEngageAndCommit(command) {
		return fusedEngageAndCommitReason
	}
	return noEngagedStoryCommitReason
}

// outsideRepoEditReason is the agent-facing refusal when a PreToolUse edit
// targets a path inside another repo's working tree (sty_a8454d10). Kept as a
// pure string so harness-specific deny emission and unit tests share one stable
// message. foreignRoot is the git root of the denied tree.
func outsideRepoEditReason(path, foreignRoot string) string {
	return fmt.Sprintf(
		"satelle: refusing edit in another repo's tree (%s, root %s) — open a session in THAT repo and create/engage the story there: satelle story create … then satelle story set <id> --status plan. Temp/non-repo paths are not fenced; opt-in multi-repo: [gate] allow_outside_tree_edits = true",
		path, foreignRoot)
}

// outsideRepoEditErr wraps outsideRepoEditReason as an error for tests that
// still assert .Error() on the refusal helper.
func outsideRepoEditErr(path, foreignRoot string) error {
	return fmt.Errorf("%s", outsideRepoEditReason(path, foreignRoot))
}

// denyPreToolUse emits a harness-specific deny payload on stdout and returns an
// error, so direct invocation has both structured stdout and a plain stderr
// reason. The installed wrapper consumes that result and normalises a usable
// deny to structured stdout + handler exit 0; it never discards stderr and then
// exits 2. The harness is detected from the PreToolUse event envelope (raw).
func denyPreToolUse(cmd *cobra.Command, raw []byte, reason string) error {
	_ = emitPreToolUseDeny(cmd.OutOrStdout(), commandHarness(raw), reason)
	return fmt.Errorf("%s", reason)
}

// commandHarness is the harness a PreToolUse event came from: the wrapper's
// --harness, else sniffed from the event envelope.
func commandHarness(raw []byte) string {
	if hookHarnessFlag != "" {
		return hookHarnessFlag
	}
	return harnessFromEvent(raw)
}

// harnessFromEvent classifies a hook event envelope as claude, grok or
// unknown (sty_5e4bc568, sty_37fd5470). The per-provider fingerprints live in
// agentcli.HarnessFromHookEvent. Unrecognised input is "unknown", never
// "claude"; agentcli.PreToolUseDeny still gives every harness without its own
// shape (unknown included) the strict hookSpecificOutput shape, so the deny
// stays effective and the sty_5e4bc568 inert-gate bug is not reopened.
func harnessFromEvent(raw []byte) string {
	return agentcli.HarnessFromHookEvent(raw)
}

// claudePreToolUseDenyOut aliases the adapter's Claude deny shape so a test can
// decode what the verb printed; the wire struct itself lives in agentcli.
type claudePreToolUseDenyOut = agentcli.ClaudePreToolUseDenyOut

// emitPreToolUseDeny writes one harness-correct deny JSON line to out. The
// per-harness encodings (claude, grok, cursor; everything else gets Claude's)
// live in agentcli.PreToolUseDeny.
func emitPreToolUseDeny(out io.Writer, harness, reason string) error {
	_, err := fmt.Fprintf(out, "%s\n", agentcli.PreToolUseDeny(harness, reason))
	return err
}

// hookInfraUnavailableReason is the model-visible text when the scaffolded
// PreToolUse wrapper cannot run satelle at all (sty_c75c73ed). It must never
// look like a policy denial — the agent needs to diagnose PATH/binary and run
// `satelle init` to heal, which requires Bash not to be bricked.
const hookInfraUnavailableReason = "satelle unavailable in this hook shell env — INFRASTRUCTURE failure, NOT a policy denial. " +
	"The satelle binary could not be resolved or did not produce a decision. " +
	"Try: which satelle; satelle version; satelle init. Non-mutating bash stays allowed so you can diagnose."

// infraDenyJSON returns the harness-correct PreToolUse deny JSON for an
// infrastructure failure (same shape as a real satelle deny — sty_5e4bc568).
func infraDenyJSON(harness string) string {
	var buf strings.Builder
	_ = emitPreToolUseDeny(&buf, harness, hookInfraUnavailableReason)
	return strings.TrimSpace(buf.String())
}

// exemptTarget reports whether an edit to target is exempt from the engaged-story
// gate. Exemption is configuration plus one mechanism rule: a write under the
// process temp directory is not an in-repo product edit, so it is never gated
// (sty_e33f78fe). Authored exemptions remain [gate] edit_exempt_paths prefixes
// and [gate] edit_exempt_globs filename patterns. The binary does not special-
// case the data dir or managed paths; `satelle init` seeds .satelle/ and the
// footprint it deploys itself (.gitignore block, harness scaffolds) into
// edit_exempt_paths, and story-dump names into edit_exempt_globs, so authored
// substrate and satelle-written output stay editable OOTB, but the operator
// owns those lists (sty_8c3d345c / sty_f115e6bf / sty_926cfcdc / sty_fefc88cd).
// The target is resolved to absolute against the repo root FIRST, so a
// repo-relative path (as Grok sends) is classified correctly. Returns false if
// the config/root cannot be resolved, so the gate stays conservative (still
// applies) on any resolution failure — including a temp-dir target, so a
// broken config does not fail open.
func exemptTarget(target string) bool {
	proc, invoking, _, _, err := config.LoadInvokingProcess()
	if err != nil {
		return false
	}
	abs := resolveAbsTarget(invoking, target)
	if tempDraftTarget(invoking, abs) {
		return true
	}
	if editExempt(proc.ResolveEditExemptPaths(invoking), invoking, abs) {
		return true
	}
	return editExemptPattern(proc.ResolveEditExemptGlobs(), invoking, abs)
}

// tempDraftRoots is the process temp directory (os.TempDir already honours
// TMPDIR) plus a literal /tmp when that is not already covered. A session may
// run with TMPDIR pointed elsewhere while the agent still drafts to /tmp.
func tempDraftRoots() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "" || p == "." || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add(os.TempDir())
	add("/tmp")
	return out
}

// tempDraftTarget reports whether target is an out-of-repo write under the
// process temp directory. A target inside repoRoot is never a temp draft — a
// repo that lives under the temp dir still gates its own tree (same
// containment as editExempt). Pure: no config, no extra filesystem. Containment
// is checked before the temp-prefix match so a t.TempDir repo cannot exempt
// its whole tree.
func tempDraftTarget(repoRoot, target string) bool {
	if repoRoot != "" && withinRoot(repoRoot, target) {
		return false
	}
	for _, r := range containmentTempRoots() {
		if withinRoot(r, target) {
			return true
		}
	}
	return false
}

// editExempt is the pure classification the edit-gate exemption rests on: target
// is exempt when it resolves under any configured exempt prefix. A prefix that
// itself sits outside repoRoot (e.g. /tmp/) matches only targets also outside
// repoRoot — it never exempts in-tree product code, even when the repo lives
// under that prefix. Kept pure (no config/filesystem) so the path classification
// is unit-tested directly. Callers pass an already-absolute target (see
// resolveAbsTarget) and absolute prefixes (ResolveEditExemptPaths); blank
// prefixes are skipped — a blank prefix would make withinRoot fail open TOWARD
// inside and exempt everything, so this is the guard. An empty repoRoot
// disables the outside-tree restriction (tests of prefix-only matching).
func editExempt(exemptRoots []string, repoRoot, target string) bool {
	inRepo := repoRoot != "" && withinRoot(repoRoot, target)
	for _, r := range exemptRoots {
		if strings.TrimSpace(r) == "" {
			continue
		}
		if repoRoot != "" && !withinRoot(repoRoot, r) && inRepo {
			continue
		}
		if withinRoot(r, target) {
			return true
		}
	}
	return false
}

// editExemptPattern reports whether target matches an authored filename glob.
// Basename match unless the pattern contains `/` (then repo-relative, slash-
// normalized). Blank and unparseable patterns are skipped — never fail-open
// toward exempt-everything (sty_fefc88cd). Kept pure so classification is
// unit-tested directly.
func editExemptPattern(patterns []string, root, target string) bool {
	for _, p := range patterns {
		s := strings.TrimSpace(p)
		if s == "" {
			continue
		}
		subject := filepath.Base(target)
		if strings.ContainsRune(s, '/') {
			if strings.TrimSpace(root) == "" {
				continue
			}
			rel, err := filepath.Rel(root, target)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			subject = filepath.ToSlash(rel)
		}
		ok, err := filepath.Match(s, subject)
		if err != nil {
			continue
		}
		if ok {
			return true
		}
	}
	return false
}

// resolveAbsTarget makes target absolute against root (the repo root the hook runs
// in). A blank target passes through; an already-absolute target is cleaned and
// returned; a relative target is joined under root. This is the single point that
// pins a repo-relative edit path (as Grok sends) to the repo root BEFORE any
// containment test, so a relative target is never nested under a narrower tested
// root (e.g. the data dir) and mis-classed as inside it (sty_8c3d345c). On a
// resolution error the raw target is returned unchanged (withinRoot stays
// conservative from there).
func resolveAbsTarget(root, target string) string {
	if strings.TrimSpace(target) == "" {
		return target
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return target
	}
	return filepath.Clean(filepath.Join(absRoot, target))
}

// withinRoot reports whether target resolves to a path inside root. A relative
// target is taken relative to root (the hook runs in the repo cwd). Pure, so the
// path classification is unit-tested without touching the filesystem; any
// resolution failure returns true (treat as in-repo) so the gate never opens by
// accident.
func withinRoot(root, target string) bool {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(target) == "" {
		return true
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return true
	}
	t := target
	if !filepath.IsAbs(t) {
		t = filepath.Join(absRoot, t)
	}
	rel, err := filepath.Rel(absRoot, filepath.Clean(t))
	if err != nil {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// runHookContext assembles and emits the session principle set. It fails open:
// any error opening the store or listing docs injects nothing and returns nil.
// The emitting event is the harness's configured session_context_event when the
// payload names that event; a payload with no event field keeps today's
// diagnostic contract (emit, rendered event name SessionStart). A payload that
// names a different event emits nothing. Delivery on a matching channel is once
// per session|harness|channel.
func runHookContext(out, stderr io.Writer, harness string, raw []byte) error {
	a, err := app.Open()
	if err != nil {
		return nil // fail open — unconfigured repo / unopenable db blocks nothing
	}
	defer func() { _ = a.Close() }()

	channel := a.Config.SessionContextEvent(harness)
	snake, camel := hookEventNames(raw)
	present, match := eventMatchesChannel(snake, camel, channel)
	// An event is present and neither field equals the configured channel
	// (including an empty channel): emit nothing. No event field keeps the
	// diagnostic contract.
	if present && !match {
		return nil
	}
	emitEvent := "SessionStart"
	unit := "bytes"
	limit := a.Config.ContextLimit(harness)
	if match {
		emitEvent = channel
		// A harness spells its session-start event its own way (cursor sends
		// sessionStart); every spelling is the SessionStart byte budget.
		if !strings.EqualFold(channel, "SessionStart") {
			unit = "characters"
			limit = a.Config.ToolContextLimitChars(harness)
		}
	}
	sessionID := sessionContextSessionID(raw)
	marker := ""
	if match && sessionID != "" {
		marker = sessionContextMarkerPath(a, sessionID, harness, channel)
		if sessionContextMarkerExists(marker) {
			return nil
		}
	}
	content, omitted := assembleSessionContext(a, harness, limit, unit)
	if len(omitted) > 0 {
		fmt.Fprintf(stderr,
			"satelle hook context: %s exceeded the %s harness limit (%d %s) — indexed with a read instruction: %s\n",
			"always-content", harness, limit, contextUnitName(unit), strings.Join(omitted, ", "))
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	fmt.Fprintln(out, string(agentcli.SessionContextOutput(harness, emitEvent, content, "")))
	// The marker is written only after a channel-matching emit of non-empty
	// additionalContext. A non-matching event (including SessionStart when the
	// channel is elsewhere) writes nothing. Marker errors are swallowed.
	if match && marker != "" {
		writeSessionContextMarker(marker)
	}
	return nil
}

// assembleSessionContext renders the session principle set — constitution,
// principles:session docs, advisories, seat — under the budget the caller
// resolved. unit is "bytes" (len, the SessionStart path) or "characters"
// (runes, a tool-event clip). versionLine is the version-stamp drift advisory,
// placed beside the scaffold block; teamLine is the project-tier write-down
// warning; releaseLine is the release-currency advisory
// ("" when none), placed beside the version-drift line; substrateLine is the
// substrate-currency advisory, likewise, and storyLine the story-sync advisory
// (held and conflicted stories), and policyLine the subscribed command-policy
// advisory, and driftLine the captured-CLI-version advisory. Fail-open: a list
// error returns empty content.
func assembleSessionContext(a *app.App, harness string, limit int, unit string) (string, []string) {
	if a == nil || a.Store == nil {
		return "", nil
	}
	// Principles and the constitution left Go (REBUILD Q-principles): the
	// session context is the web availability line and the engagement seat.
	content := prependContextLine("", probeWebAvailability().hookLine())
	return appendSeatToContext(content, sessionSeatBlock(a), 0), nil
}

// hookEventNames reads both event fields a harness may send. Captured grok
// stdin puts the PascalCase name on hook_event_name and the snake_case twin on
// hookEventName. Matching is exact equality with the configured channel — the
// snake_case twin is not case-folded into a match.
func hookEventNames(raw []byte) (snake, camel string) {
	if len(raw) == 0 {
		return "", ""
	}
	var ev struct {
		Snake string `json:"hook_event_name"`
		Camel string `json:"hookEventName"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return "", ""
	}
	return ev.Snake, ev.Camel
}

// eventMatchesChannel reports whether an event field is present and whether
// either field equals channel exactly. An empty channel never matches.
func eventMatchesChannel(snake, camel, channel string) (present, match bool) {
	present = snake != "" || camel != ""
	if channel == "" {
		return present, false
	}
	return present, snake == channel || camel == channel
}

// sessionContextSessionID prefers the hook payload's session id, then the
// SATELLE_SESSION stamp. Empty means the once-marker cannot key a delivery, so
// the caller delivers.
func sessionContextSessionID(raw []byte) string {
	if id := sessionIDFromHook(raw); id != "" {
		return id
	}
	return strings.TrimSpace(config.SessionFromEnv())
}

func sessionContextMarkerPath(a *app.App, session, harness, channel string) string {
	if a == nil || strings.TrimSpace(session) == "" || strings.TrimSpace(channel) == "" {
		return ""
	}
	dir := filepath.Join(a.Config.ResolveRuntimeDir(a.RepoRoot).Dir, "session-context")
	name := session + "|" + harness + "|" + channel
	name = strings.ReplaceAll(strings.ReplaceAll(name, "/", "_"), "\\", "_")
	return filepath.Join(dir, name)
}

func sessionContextMarkerExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// writeSessionContextMarker records a delivery. Any error — including an
// unwritable runtime dir — is swallowed so the emit already written still stands.
func writeSessionContextMarker(path string) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte("1\n"), 0o644)
}

func contextMeasure(unit string) func(string) int {
	if unit == "characters" || unit == "runes" {
		return utf8.RuneCountInString
	}
	return func(s string) int { return len(s) }
}

func contextUnitName(unit string) string {
	if unit == "" {
		return "bytes"
	}
	return unit
}

// resolveContextHarness names the in-loop harness a SessionStart injection is
// for: the explicit --harness flag, else the hook payload's fingerprint, else
// the session markers in the environment. Anything unrecognised is "unknown" —
// never assumed to be Claude — and takes the neutral limit.
func resolveContextHarness(flag string, raw []byte, environ []string) string {
	if h := strings.ToLower(strings.TrimSpace(flag)); h != "" {
		return h
	}
	if h := agentcli.HarnessFromHookEvent(raw); h != agentcli.HarnessUnknown {
		return h
	}
	if h, ok := agentcli.InLoopHarnessFromEnv(environ); ok {
		return h
	}
	return agentcli.HarnessUnknown
}

func prependContextLine(content, line string) string {
	if strings.TrimSpace(line) == "" {
		return content
	}
	if strings.TrimSpace(content) == "" {
		return line
	}
	return line + "\n\n" + content
}

// sessionSeatBlock returns the SessionStart seat inject for a, or "" when no
// lease exists / store is incomplete. Fail-open helper for runHookContext.
func sessionSeatBlock(a *app.App) string {
	if a == nil || a.Store == nil || a.Store.Leases == nil {
		return ""
	}
	ctx := context.Background()
	mode := a.PlaneConfig().ResolveEngagementParallel()
	leases, err := a.Store.Leases.List(ctx)
	if err != nil || len(leases) == 0 {
		// No seat to describe. Under the default mode that is the whole story and
		// the inject stays silent; under a NON-default mode the agent still has to
		// know the rule it will be engaging under, and a refusal is too late to
		// learn it.
		if mode != config.ParallelNone {
			return "## Engagement seat\nseat free.\n" + seatModeLine(mode)
		}
		return ""
	}
	wfs, _ := a.Store.DocIndex.List(ctx, "workflows")
	items, _ := a.Store.Stories.List(ctx, workitem.ListFilter{})
	now := time.Now().UTC()
	live, other, eerr := evaluateSeat(leases, items, wfs, now)
	// An unreadable authored process is said, not read as "no seat to describe":
	// the seat's route cannot be resolved while the process cannot be read
	// (sty_d6e209aa).
	if errors.Is(eerr, wfgovern.ErrAuthoredProcessUnreadable) {
		return "## Engagement seat\n" + eerr.Error()
	}
	// Several live seats: lead with this session's own (by working tree) but
	// render them ALL — an operator joining a project where work is in flight
	// under a shared key must see every holder, not just one.
	if len(live) > 0 {
		holders := make([]string, 0, len(live))
		for _, s := range live {
			holders = append(holders, s.ItemID)
		}
		block := renderSeatBlocks(live, now, mode)
		if sched := seatScheduleLines(holders, items, wfs); sched != "" {
			block += "\n\n" + sched
		}
		return block
	}
	info := other
	// No evaluateSeat pick (e.g. empty items+wfs): still name the first raw row.
	if info.ItemID == "" {
		l := leases[0]
		info = seatInfo{
			ItemID: l.ItemID, State: l.State, Owner: l.Owner, Worktree: l.Worktree,
			AcquiredAt: l.AcquiredAt, HeartbeatAt: l.HeartbeatAt,
			Stale: !lease.Alive(l, now), InFlight: lease.EffectiveInFlight(l, now),
		}
	}
	return formatSeatBlock(info, now, mode)
}

// renderSeatBlocks formats every live seat for the SessionStart inject, leading
// with this session's own (sty_c098dc2d). A project may hold more than one, and
// an operator joining that work must see all of it — the old leases[0] pick
// would have shown one and hidden the rest.
func renderSeatBlocks(live []seatInfo, now time.Time, mode string) string {
	if len(live) == 0 {
		return ""
	}
	mine, _ := pickSessionSeat(live, nil, config.ResolveSession())
	if mine.ItemID == "" {
		mine = live[0]
	}
	blocks := []string{formatSeatBlock(mine, now, mode)}
	for _, s := range live {
		if s.ItemID != mine.ItemID {
			blocks = append(blocks, formatSeatBlock(s, now, mode))
		}
	}
	return strings.Join(blocks, "\n\n")
}

// readConstitution returns the project constitution body (frontmatter stripped),
// or "" when absent or unreadable — the order-zero session context injected every
// session (epic:session-context). Fails open: a missing constitution injects
// nothing and never blocks the session.
func readConstitution(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(stripFrontmatter(string(b)))
}

// docHasTag reports whether the markdown's frontmatter `tags:` includes tag.
func docHasTag(body, tag string) bool {
	for _, t := range frontmatterTags(body) {
		if t == tag {
			return true
		}
	}
	return false
}

// frontmatterTags parses the `tags:` value from a markdown frontmatter block.
// It handles both the inline flow form (`tags: [a, b]`) and the block list form
// (`tags:` followed by `- a` lines). Returns nil when there is no frontmatter or
// no tags key.
func frontmatterTags(body string) []string {
	fm := frontmatter(body)
	if fm == "" {
		return nil
	}
	lines := strings.Split(fm, "\n")
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, "tags:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(t, "tags:"))
		if strings.HasPrefix(rest, "[") { // inline flow form
			rest = strings.TrimSuffix(strings.TrimPrefix(rest, "["), "]")
			return splitTrimTags(rest)
		}
		// block list form: gather subsequent "- item" lines
		var out []string
		for j := i + 1; j < len(lines); j++ {
			l2 := strings.TrimSpace(lines[j])
			if l2 == "" {
				continue
			}
			if strings.HasPrefix(l2, "- ") {
				out = append(out, strings.Trim(strings.TrimSpace(l2[2:]), `"'`))
				continue
			}
			break // next key — end of the tags list
		}
		return out
	}
	return nil
}

// splitTrimTags splits a comma-separated inline tag list, trimming whitespace
// and surrounding quotes from each item, dropping empties.
func splitTrimTags(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		v := strings.Trim(strings.TrimSpace(p), `"'`)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// frontmatter returns the YAML frontmatter block (between the leading `---` and
// the next `---`), or "" when the body has none.
func frontmatter(body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for j := 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "---" {
			return strings.Join(lines[1:j], "\n")
		}
	}
	return ""
}

// stripFrontmatter returns the body with any leading YAML frontmatter block
// removed, so the injected content is clean markdown.
func stripFrontmatter(body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return body
	}
	for j := 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "---" {
			return strings.TrimLeft(strings.Join(lines[j+1:], "\n"), "\n")
		}
	}
	return body
}

// emitAdditionalContext writes the hook JSON that adds advisory context. event is
// the hook event name; context is the body the model reads (a system reminder for
// SessionStart; beside the tool result for PreToolUse). permissionDecision is
// optional — "" omits it (SessionStart, which makes no permission decision); a
// PreToolUse allow-with-nudge sets "allow" so the edit proceeds while the
// additionalContext advisory rides alongside (the only model-visible channel on an
// ALLOWED edit — bare stderr is transcript-only on exit 0). One emitter for both
// callers (sty_f5f351d1).
//
// UserPromptSubmit shape (sty_e16a2cd7 AC7): the same Claude-shaped
// hookSpecificOutput/additionalContext envelope is used for both Claude and Grok
// harnesses. Grok documents additionalContext as a working channel (Stop non-error
// feedback; Claude-compat vocabulary) and does not define a distinct
// UserPromptSubmit output envelope. PreToolUse deny still splits harnesses because
// Claude rejects top-level decision/reason — a real schema conflict that context
// injection does not share. Finding recorded on sty_e16a2cd7 as grok-prompt-contract.
//
// PreToolUse with no permission decision is a warn (sty_a74551d6): the command
// runs and the agent is told why it was flagged. Whether a harness carries that
// to the model is a per-harness fact (agentcli HarnessFacts.PreToolUseWarn), so
// harness picks the encoding and a harness that cannot deliver one gets nothing
// on stdout. Every other event keeps the one Claude-shaped envelope.
func emitAdditionalContext(out io.Writer, harness, event, permissionDecision, context string) error {
	if event == "PreToolUse" && permissionDecision == "" {
		if b := agentcli.PreToolUseWarn(harness, context); b != nil {
			fmt.Fprintln(out, string(b))
		}
		return nil
	}
	fmt.Fprintln(out, string(agentcli.HookEnvelope(event, permissionDecision, context, "")))
	return nil
}

// hookPromptReminder is the CONCISE standing nudge the UserPromptSubmit hook
// re-injects every turn (the full rule is the satelle-edits-require-a-story
// principle injected at SessionStart). It keeps the engaged-story discipline in
// front of the agent between session starts — a single PreToolUse gate is not the
// only line of defence (sty_949e8739).
const hookPromptReminder = "satelle: edits require an ENGAGED story. Before any Edit/Write/create/delete, engage a story in a performing state — `satelle story create …` then `satelle story set <id> --status plan` — and drive it through its workflow. Research uses read tools (Read/grep/Glob), never Edit/Write. The edit gate enforces this; never route around it. " + gateWaitNote

// gateWaitNote is the one sentence every agent-facing surface carries about a
// gate that outlasts the call (sty_c4b92c9e): the command returns a handle, the
// verdict arrives by the harness's own hook, and the driver waits for it instead
// of asking. It rides the standing reminder and the engaged form so an agent
// reads it on every turn it could be tempted to poll on, and the help topic
// carries the same two phrases (gate_wait_text_test.go asserts both).
const gateWaitNote = "A slow gate returns a handle and its verdict is delivered into this session as a notification — never poll, sleep-loop, or re-run a backgrounded command."

// gateNotWiredWarning is the LOUD banner the UserPromptSubmit self-check prepends
// when it can confidently see the PreToolUse edit gate is NOT wired into the
// repo's committed hook settings — the countermeasure to a silently inert gate
// (the incident this story fixes): without a wired gate, code edits are ungated
// with no signal.
const gateNotWiredWarning = "⚠️ satelle: the edit gate is NOT wired into this repo's hooks — code edits are currently UNGATED. Do NOT edit code until enforcement is live: run `satelle init` to (re)install the PreToolUse gate, then restart the session so the harness loads it."

// promptEngagedCeiling is the byte budget for the live-seat replacement text.
// The engaged form replaces the static reminder, so it must never be longer
// (sty_e16a2cd7 AC4).
var promptEngagedCeiling = len(hookPromptReminder)

// formatEngagedPrompt builds the live-seat UserPromptSubmit body: story id,
// status, forward target(s) + gates from the route, and the story set
// command (sty_e16a2cd7). Returns "" when there is nothing useful to say
// (no id, no advance options) so the caller keeps today's reminder+seat path.
// Never includes the static create-and-engage reminder — it REPLACES it.
func formatEngagedPrompt(info seatInfo, now time.Time) string {
	if info.ItemID == "" || len(info.Advance) == 0 {
		return ""
	}
	state := info.State
	if state == "" {
		state = "?"
	}
	hb := formatSeatAge(now.Sub(info.HeartbeatAt))

	// Build advance clauses and gate lists; degrade under the ceiling.
	type targetView struct {
		to    string
		gates []string
	}
	views := make([]targetView, 0, len(info.Advance))
	for _, a := range info.Advance {
		views = append(views, targetView{to: a.To, gates: append([]string(nil), a.Gates...)})
	}

	build := func(capGates int, withGates, withSeat bool) string {
		var advParts []string
		var gateParts []string
		for _, v := range views {
			advParts = append(advParts, fmt.Sprintf("`satelle story set %s --status %s`", info.ItemID, v.to))
			if !withGates || len(v.gates) == 0 {
				continue
			}
			g := v.gates
			suffix := ""
			if capGates > 0 && len(g) > capGates {
				suffix = fmt.Sprintf("+%d", len(g)-capGates)
				g = g[:capGates]
			}
			gateParts = append(gateParts, strings.Join(g, ", ")+suffix)
		}
		msg := fmt.Sprintf("satelle: %s ENGAGED (%s, hb %s) — advance: %s",
			info.ItemID, state, hb, strings.Join(advParts, " | "))
		if withGates && len(gateParts) > 0 {
			msg += "; gates: " + strings.Join(gateParts, " | ")
		}
		if withSeat {
			msg += ". Seat: `satelle story seat`"
		}
		return msg + ". " + gateWaitNote
	}

	// Degradation ladder: full → cap gates at 3 → drop gates → drop seat → give up.
	for _, try := range []struct {
		capGates  int
		withGates bool
		withSeat  bool
	}{
		{0, true, true},  // full (0 = no cap)
		{3, true, true},  // cap gates
		{0, false, true}, // drop gates
		{0, false, false},
	} {
		out := build(try.capGates, try.withGates, try.withSeat)
		if len(out) <= promptEngagedCeiling {
			return out
		}
	}
	return ""
}

// runHookPrompt is the UserPromptSubmit handler: it re-injects the concise
// edits-require-a-story reminder and, when a gate-liveness self-check confidently
// finds no wired edit gate, prepends the LOUD not-wired warning. With a LIVE
// seat that has forward route advances, the reminder is REPLACED by the engaged
// form (id, status, next gate, story set) (sty_e16a2cd7). Otherwise a seat line
// is appended as before (sty_1738f973 AC6). Fails open — a resolve/read failure
// injects only the reminder.
func runHookPrompt(out io.Writer) error {
	return runHookPromptWith(out, true)
}

// runHookPromptWith is runHookPrompt that can leave a finished gate unclaimed.
// A harness whose resume wake owns delivery (resumeWakeFor) discards this hook's
// additionalContext, so claiming a verdict here would lose it: it stays for the
// resume (sty_eac9b28d).
func runHookPromptWith(out io.Writer, gatesInContext bool) error {
	body := hookPromptReminder
	now := time.Now().UTC()
	// Seat + heartbeat: fail-open (resolve error leaves the static reminder;
	// heartbeat write failures never block the prompt) (sty_3bb1d8be, sty_e16a2cd7 AC6).
	if info, engaged, err := currentSeatTouch(); err == nil {
		if engaged {
			if eng := formatEngagedPrompt(info, now); eng != "" {
				body = eng // REPLACE the create-and-engage reminder
			} else {
				// Live seat but no forward advance (e.g. release after AC5, or a
				// baseline workflow whose only next is terminal): keep today's
				// reminder + seat line so the turn still names the holder
				// (architecture note on sty_e16a2cd7 plan).
				body = appendSeatToPrompt(body, info, now)
			}
		} else {
			body = appendSeatToPrompt(body, info, now)
		}
	}
	if root, ok := repoRootForHook(); ok {
		if wired, checked := gateWiredInSettings(root); checked && !wired {
			// Warning is orthogonal to the reminder/engaged body and does not
			// count against promptEngagedCeiling.
			body = gateNotWiredWarning + "\n\n" + body
		}
	}
	return emitAdditionalContext(out, "", "UserPromptSubmit", "", body)
}

// runHookStopcheck is the Stop handler: a post-hoc detector for the exact
// incident the PreToolUse gate prevents. It blocks finishing when the tree has
// uncommitted non-exempt in-repo changes while NO live seat exists anywhere in
// this repo — so an ungated edit cannot be silently finished even if the
// PreToolUse hook never fired. Honours stop_hook_active (never re-blocks its own
// block) and fails open (git absent, clean tree, only exempt changes, or a story
// engaged by this session → allow).
//
// The dirty check is repo-wide, so the engagement question must be too
// (sty_211d8419): a live seat held by a SIBLING session attributes the dirty
// tree to that holder — the edits were gated, in that session — so this session
// is allowed to stop and told who holds the seat, instead of being blocked with
// a demand it cannot satisfy (engaging would claim another session's work;
// reverting would destroy it). Branch order is a decision: `mine` short-circuits
// before the git call (today's cost profile for the common case), and the dirty
// check runs before the other-holder note so a sibling session that edited
// nothing gets no chatter on every Stop.
func runHookStopcheck(raw []byte, out io.Writer) error {
	block, note, err := stopcheckEdits(raw)
	if err != nil {
		return err
	}
	if block != "" {
		return emitStopBlock(out, block)
	}
	if note != "" {
		return emitStopNote(out, note)
	}
	return nil
}

// stopHarness is the harness the Stop event came from: the hook's own --harness
// token, else a neutral sniff of the event envelope.
func stopHarness(raw []byte) string {
	if hookHarnessFlag != "" {
		return hookHarnessFlag
	}
	return harnessFromEvent(raw)
}

func joinNonEmpty(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// stopcheckEdits is the ungated-edit half of the Stop hook: the block reason when
// the tree has uncommitted non-exempt changes and no live seat, or the note when
// a sibling session holds the seat; both empty when nothing is wrong or nothing
// can be known. It judges only the driving session: a process satelle dispatched
// (a reviewer, planner, coder or any other seat) holds no seat of its own, and a
// block would run another turn whose output replaces the final output satelle
// reads as its verdict. The driving session's own Stop still flags the tree.
func stopcheckEdits(raw []byte) (block, note string, err error) {
	if stopHookActive(raw) {
		return "", "", nil // anti-loop: never re-block a stop we already blocked
	}
	if isDispatchedProcess() {
		return "", "", nil // not the driver: its final output is a verdict, never to be displaced
	}
	root, ok := repoRootForHook()
	if !ok {
		return "", "", nil // fail open — unresolvable repo blocks nothing
	}
	mine, other, extra, err := stopcheckSeat()
	if err != nil || mine {
		// This session holds a live seat (edits are legitimate) OR engagement is
		// unknowable — stopcheck is a secondary detector, so it fails OPEN rather
		// than blocking a finish on a broken deployment (the PreToolUse gate is the
		// fail-closed one).
		return "", "", nil
	}
	gated, derr := dirtyGatedPaths(root)
	if derr != nil || len(gated) == 0 {
		return "", "", nil // git absent / clean / only exempt (.satelle) changes — nothing to flag
	}
	if other.ItemID != "" {
		return "", stopcheckSiblingNote(other, extra, gated, time.Now().UTC()), nil
	}
	return stopcheckReason(gated), "", nil
}

// repoRootForHook resolves this repo's root from the committed config, or
// (false) when it cannot be resolved — hooks that call it fail open on false.
func repoRootForHook() (string, bool) {
	_, cfgPath, err := config.Load("")
	if err != nil {
		return "", false
	}
	root := config.RepoRootFromConfigPath(cfgPath)
	if strings.TrimSpace(root) == "" {
		return "", false
	}
	return root, true
}

// gateWiredInSettings reports whether the repo's committed hook settings wire the
// PreToolUse edit gate, and whether the check could be made at all. checked=false
// means no settings file was present/readable (the caller fails OPEN — no
// warning); checked=true with wired=false means a settings file exists but no
// PreToolUse Edit-matcher hook invokes `satelle hook gate` — a confident missing
// wire the caller surfaces LOUDLY.
func gateWiredInSettings(repoRoot string) (wired bool, checked bool) {
	for _, path := range []string{
		filepath.Join(repoRoot, ".claude", "settings.json"),
		filepath.Join(repoRoot, filepath.FromSlash(grokHooksRel)),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue // absent/unreadable — skip (fail open on this candidate)
		}
		checked = true
		if settingsWiresGate(raw) {
			return true, true
		}
	}
	return false, checked
}

// settingsWiresGate reports whether a hook-settings JSON wires a PreToolUse
// Edit-matcher hook that invokes the edit gate. Recognises:
//   - legacy one-liner / inline wrapper containing "hook gate" (sty_c75c73ed)
//   - script-file form containing "pretooluse-gate-" (sty_adfb9862)
//   - parameterized form containing "satelle-hook.sh" (epic:minimal-harness-footprint)
//
// Pure over the bytes so it is unit-tested directly; a parse failure returns
// false (no confident wire).
func settingsWiresGate(raw []byte) bool {
	var s struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	for _, e := range s.Hooks.PreToolUse {
		if !strings.Contains(e.Matcher, "Edit") {
			continue
		}
		for _, h := range e.Hooks {
			if strings.Contains(h.Command, "hook gate") ||
				strings.Contains(h.Command, "pretooluse-gate-") ||
				strings.Contains(h.Command, "satelle-hook.sh") {
				return true
			}
		}
	}
	return false
}

// dirtyGatedPaths returns the repo-relative paths git reports as modified/added
// that are NOT edit-gate-exempt — the ungated-edit surface the stopcheck flags.
// Returns an error when git is unavailable or root is not a repo, so the caller
// fails open. Exempt paths (e.g. .satelle/ authored substrate) are filtered out.
func dirtyGatedPaths(root string) ([]string, error) {
	out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		return nil, err
	}
	var gated []string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue // "XY p" is the minimum porcelain line
		}
		path := strings.TrimSpace(line[3:])
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:] // a rename reports "old -> new"; the new path is what exists
		}
		path = strings.Trim(path, `"`)
		if path == "" || exemptTarget(path) {
			continue
		}
		gated = append(gated, path)
	}
	return gated, nil
}

// stopHookActive reports whether the Stop event marks that a stop hook is already
// active — honoured so stopcheck never re-blocks a stop it already blocked
// (anti-loop). Accepts snake_case (Claude) and camelCase (Grok) shapes.
func stopHookActive(raw []byte) bool {
	var ev struct {
		Snake bool `json:"stop_hook_active"`
		Camel bool `json:"stopHookActive"`
	}
	_ = json.Unmarshal(raw, &ev)
	return ev.Snake || ev.Camel
}

// stopcheckReason is the agent-facing block message naming the ungated files
// (capped so a large dirty tree does not flood the reason).
func stopcheckReason(paths []string) string {
	shown := paths
	const max = 10
	suffix := ""
	if len(shown) > max {
		suffix = fmt.Sprintf(" (+%d more)", len(shown)-max)
		shown = shown[:max]
	}
	return "satelle: STOP BLOCKED — the tree has uncommitted, non-exempt changes but NO story is engaged, so these edits were made UNGATED: " +
		strings.Join(shown, ", ") + suffix + ". This is exactly what the edit gate exists to prevent. " +
		"Engage a story now (satelle story create … then satelle story set <id> --status plan) so the change is tracked through its workflow, or revert the ungated edits."
}

// stopcheckSiblingNote is the informational line stopcheck emits when the dirty
// tree is attributed to a live seat held by ANOTHER session (sty_211d8419). It
// names the holding story and session so the reader can tell the edits were
// gated elsewhere, and it never reads as a demand: this session holds no seat
// and is not blocked.
func stopcheckSiblingNote(holder seatInfo, extra int, paths []string, now time.Time) string {
	session := strings.TrimSpace(holder.SessionID)
	if session == "" {
		session = "unstamped"
	}
	more := ""
	if extra > 0 {
		more = fmt.Sprintf(" (+%d more live seat(s))", extra)
	}
	return fmt.Sprintf("satelle: %d uncommitted non-exempt change(s) in this tree are attributed to a live seat held elsewhere — %s, session %s%s. This session holds no seat and is not blocked; those edits were gated in the holding session. Inspect: satelle story seat.",
		len(paths), formatSeat(holder, now), session, more)
}

// emitStopNote writes the allow-with-note JSON (one line) and returns nil. The
// payload is agentcli.StopAllow: no decision field, so the stop proceeds, and
// systemMessage surfaces the note to the operator. It is deliberately NOT
// stopBlockOut — the JSON on stdout must never read as a block on this path
// (sty_211d8419).
func emitStopNote(out io.Writer, note string) error {
	return emitStopAllow(out, agentcli.StopAllow{SystemMessage: note})
}

func emitStopAllow(out io.Writer, allow agentcli.StopAllow) error {
	// A harness whose only Stop channel re-prompts takes no note (nil).
	if b := agentcli.StopAllowOutput(stopEmitHarness, allow); b != nil {
		fmt.Fprintln(out, string(b))
	}
	return nil
}

// stopBlockOut is the Stop-hook block payload (AC6 / sty_5e4bc568 audit): Claude
// Code's Stop control channel IS top-level {"decision":"block","reason":…} —
// distinct from PreToolUse's hookSpecificOutput shape. The same top-level shape
// best-effort covers Grok.
type stopBlockOut struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// emitStopBlock writes the Stop block JSON (one line) and returns nil — the Stop
// hook blocks via the stdout decision, not an exit code (its wiring carries no
// '|| exit 2'). Every block path goes through here so none can emit the wrong
// decision value.
func emitStopBlock(out io.Writer, reason string) error {
	fmt.Fprintln(out, string(agentcli.StopOutput(stopEmitHarness, true, reason)))
	return nil
}

// stopEmitHarness is the harness the Stop answer in flight is encoded for. It is
// set once per stopcheck process (runHookStopcheck), because the block and note
// emitters sit several calls below the payload that names the harness; the
// encodings themselves live in agentcli.StopOutput.
var stopEmitHarness string
