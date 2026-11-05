package agentstep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/agentartifact"
	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// Expect selects the post-run contract for Invoke (sty_ba860c8a / design §4).
// ExpectVerdict runs the reviewer retry/parse loop; ExpectPerform is a single run.
type Expect int

const (
	// ExpectVerdict: isolated judge — output must parse to a gate decision.
	ExpectVerdict Expect = iota
	// ExpectPerform: performer — raw stdout, no verdict parse, no retry.
	ExpectPerform
	// ExpectVerdicts: isolated judge over SEVERAL rubrics in one session
	// (sty_23e10d92) — output must parse to a verdict for at least one of
	// InvokeRequest.Skills; a skill it names no verdict for fails closed. It
	// shares ExpectVerdict's isolation, timeout and retry/repair loop.
	ExpectVerdicts
)

// judges reports whether the contract is an isolated reviewer's — one verdict or
// several — so isolation, capture and timeout treat both identically.
func (e Expect) judges() bool { return e == ExpectVerdict || e == ExpectVerdicts }

// ExpectFromRole maps a resolved binding role to an Expect.
// role=reviewer → verdict; everything else → perform.
func ExpectFromRole(role string) Expect {
	if role == config.RoleReviewer {
		return ExpectVerdict
	}
	return ExpectPerform
}

// InvokeRequest describes one isolated-agent call through the shared Invoke seam.
// Call sites (runReviewer, DispatchExecutor, Retrospect) fill this after their
// own pre-flight; Invoke owns prompt assembly, Runner.Run, and (for verdict)
// retry/parse.
type InvokeRequest struct {
	Binding config.AgentBinding // resolved binding (tools/model/env/settings/role/principles)
	Section string              // agents.toml section name (for role inference / logging)
	Rubric  string              // skill body from the workflow node/edge
	Payload any                 // marshalled to JSON stdin
	// Material is the pre-cap reviewer bytes. Invoke writes them and substitutes
	// paths only when Expect judges and Payload is a transitionPayload. The
	// check path never sets this; executor and consultant payloads stay inline.
	Material *reviewerMaterial
	Charter  string // optional override; empty → charter from role/expect
	// Expect selects the contract. Zero value is ExpectVerdict — callers that
	// perform must set ExpectPerform explicitly.
	Expect Expect
	// Timeout is an OPTIONAL hard ceiling on this ONE run. ≤0 (the default,
	// sty_752c4ef2) means no wall-clock cap — IdleTimeout is what actually
	// bounds a progressing dispatch. An empty Timeout with expect=ExpectVerdict
	// still falls back to the engine's g.agentTimeout (also unset by default).
	Timeout time.Duration
	// IdleTimeout bounds how long this run may go with no REAL event (tool
	// start/end, message, usage — heartbeats excluded) before Watchdog judges
	// it stalled and cancels it (sty_752c4ef2). ≤0 falls back to the engine's
	// g.idleTimeout.
	IdleTimeout time.Duration
	// Runner overrides the runner built from Binding.Command. Reviewer path
	// passes g.runner (bootstrap-resolved); named dispatch builds from the binding.
	Runner agentcli.Runner
	// Attempts bounds verdict retries (0 → engine default). Perform ignores this.
	Attempts int
	// Sink, when set, receives live subprocess stdout (named dispatch live log).
	Sink io.Writer
	// OnEvent observes provider-neutral progressive events. It is composed with
	// the engine's interactive stderr progress consumer.
	OnEvent agentcli.EventHandler
	// Log, when set, is the dispatch log Invoke writes what it sent the agent to
	// before the run and a closing summary to after it (sty_2b89feab). The run's
	// own events reach the log through OnEvent, as the caller wires them.
	Log *dispatchLog

	// Telemetry / progress context for the verdict retry loop (design note:
	// Invoke needs labels beyond the minimal design sketch so retry telemetry
	// and progress lines move intact from runReviewer).
	StoryID string
	Step    string // to-status or step name
	Skill   string // rubric skill name for telemetry/logging
	Actor   string // ledger/telemetry actor (default "reviewer" for verdict)
	// Skills names every rubric a bundled (ExpectVerdicts) run judges, in the
	// order its verdicts are wanted. Ignored by the other contracts.
	Skills []string
}

// InvokeResult is the outcome of one Invoke call.
type InvokeResult struct {
	Stdout   []byte
	Usage    agentcli.UsageResult
	Command  string             // resolved harness command for ledger evidence
	Decision *verb.GateDecision // non-nil when ExpectVerdict and parse succeeded
	// Decisions holds one decision per requested skill, in request order, when
	// ExpectVerdicts parsed (sty_23e10d92). A skill the response named no verdict
	// for is present as a fail-closed reject, never absent.
	Decisions []verb.GateDecision
	Err       error
	// Model is the listed model the returned attempt ran on when a binding's
	// model list moved past its first model (sty_7bfa980d); empty otherwise, so a
	// dispatch with no move is recorded exactly as before. See RanModel.
	Model string
	// NoVerdict is set on a bundled result whose rubrics all failed closed
	// because no attempt named a verdict: the session ran, but the model did not
	// answer, so a binding's model list moves on (sty_7bfa980d).
	NoVerdict bool
	// Counts is the run's turns and tool calls, or the adapter-named reason each
	// is unavailable. Stamped for a judging invocation on every outcome, a failed
	// attempt included — it is what such an attempt measured.
	Counts verb.RunCounts
	// SystemPromptBytes/PayloadBytes are the byte lengths of the system prompt
	// and stdin payload satelle sent for this invocation (sty_363eaf55) —
	// lengths only, never content.
	SystemPromptBytes int
	PayloadBytes      int
	// ToolIsolation records what a reviewer's harness offered (sty_ef3efb51):
	// stamped for ExpectVerdict invocations only.
	verb.ToolIsolation
}

// invocation is the internal prompt-assembly shape used by buildRequest.
// principles is the resolved selector (session|all|…|none); empty means none
// (no constitution/principles block).
type invocation struct {
	charter    string // role charter; empty for the summariser (rubric-only prompt)
	rubric     string // the skill body
	principles string // resolved principles selector; empty/none → no inject
	payload    any
	tools      string
	model      string
	effort     string // optional reasoning effort (sty_657f77b9)
	settings   map[string]any
	env        map[string]string
	scratch    string // this dispatch's scratch dir; "" → no scratch briefing (sty_e7aaf8b1)
	judging    bool   // Expect judges: the work item is paths, the briefing says so
	inline     bool   // judging, but the runner reads no local paths: the work item stays inline
	shell      bool   // the grant admits a shell, so the judging briefing keeps the CLI lines
}

// promptParts are the pieces of an isolated agent's system prompt that follow
// the constitution and the principles (BuildSeatPayload puts those first). Empty parts
// are omitted.
type promptParts struct {
	charter string // role charter
	scratch string // this dispatch's scratch dir
	rubric  string // the skill body
	judging bool   // use the judging briefing instead of the pull-context call to action
	inline  bool   // judging with the work item inline: the verdict rules ride, the path briefing does not
	shell   bool   // judging briefing also names the three CLI lines
	verdict string // the shared verdict rules (ReviewerVerdictDoc); written only when judging
}

// composeSystemPrompt is the canonical order of everything after the constitution
// and principles: role charter, scratch briefing, pull-context call-to-action,
// skill rubric. BuildSeatPayload puts the constitution and principles in
// front of it, for buildRequest and the injected-size report alike, so the number
// the report prints is the length of what a dispatch sends.
func composeSystemPrompt(p promptParts) string {
	var b strings.Builder
	if p.charter != "" {
		b.WriteString(p.charter)
		b.WriteString("\n\n")
	}
	// The scratch briefing rides whenever this dispatch has a scratch dir,
	// charter or not — a charter-less perform dispatch still needs to know
	// where to put evidence (sty_e7aaf8b1 AC2).
	if brief := scratchBriefing(p.scratch); brief != "" {
		b.WriteString(brief)
		b.WriteString("\n\n")
	}
	// A judging reviewer opens the work item by path. Every other isolated
	// agent still gets the pull-context call to action, including the step
	// summariser (SeatSystemPrompt leaves judging unset).
	if p.judging {
		if p.inline {
			b.WriteString(pullContextCallToAction)
		} else {
			b.WriteString(judgingContextBriefing(p.shell))
		}
		// The verdict rules every judging reviewer obeys ride once, ahead of the
		// rubric that owns only its own verdict shape.
		if v := strings.TrimSpace(p.verdict); v != "" {
			b.WriteString("\n\n")
			b.WriteString(v)
		}
	} else {
		b.WriteString(pullContextCallToAction)
	}
	if p.rubric != "" {
		b.WriteString("\n\n---\n\n")
		b.WriteString(p.rubric)
	}
	return b.String()
}

// buildRequest composes an isolated agent's system prompt in ONE canonical order —
// constitution+principles (when selector ≠ none), role charter, pull-context
// call-to-action, skill rubric — marshals the stdin payload, and fills the
// grant/model/dir. It is the single insertion point for anything satelle wants
// EVERY isolated agent to receive.
func (g *Engine) buildRequest(ctx context.Context, inv invocation) (agentcli.Request, error) {
	payload, err := json.Marshal(inv.payload)
	if err != nil {
		return agentcli.Request{}, err
	}
	in := SeatPayloadInput{
		Charter: inv.charter, Scratch: inv.scratch, Rubric: inv.rubric,
		Judging: inv.judging, Inline: inv.inline, Shell: inv.shell,
	}
	if inv.judging {
		in.Verdict = ReviewerVerdictDoc(ctx, g.docs)
	}
	// Principles selector (design §5): when not none, constitution rides order-zero
	// then the selected principles — SessionStart parity (cmd_hook renderAlwaysContent).
	if inv.principles != "" && inv.principles != config.PrinciplesNone {
		in.Constitution = g.constitution
		in.Principles = ResidentPrincipleDocs(ctx, g.docs, inv.principles)
	}
	systemPrompt := BuildSeatPayload(in)
	var settings string
	if len(inv.settings) > 0 {
		sb, serr := json.Marshal(inv.settings)
		if serr != nil {
			return agentcli.Request{}, serr
		}
		settings = string(sb)
	}
	// Every request buildRequest assembles is satelle's own spawn, never the
	// in-loop driving session (sty_719c4a7b AC4) — invoke.go, OpenSessionAs,
	// Summarise and ReviewText are its only callers, and none of them build the
	// interactive session itself. Stamped here, the one seam they all share,
	// so a caller that forgets its own SATELLE_DISPATCH_* markers (the step
	// summariser has no agent/step/item three-tuple to carry) still marks
	// itself. Copy before writing: inv.env may be a shared map (g.reviewerEnv,
	// a binding's own env) that other callers reuse.
	env := make(map[string]string, len(inv.env)+1)
	for k, v := range inv.env {
		env[k] = v
	}
	env[config.SpawnEnv] = "1"
	return agentcli.Request{
		SystemPrompt: systemPrompt,
		Payload:      string(payload),
		AllowedTools: inv.tools,
		Model:        inv.model,
		Effort:       inv.effort,
		Settings:     settings,
		Env:          env,
		Dir:          g.repoRoot,
	}, nil
}

// Invoke is the ONLY path that calls agentcli.Runner.Run for LLM gate/dispatch
// steps (sty_ba860c8a). It resolves prompt assembly from the binding, runs the
// agent, and for ExpectVerdict applies the retry + verdict parse loop.
// A binding whose model= lists several models runs on each in order while the
// current one does not answer (invokeModels, sty_7bfa980d). Once the list is
// spent, a classified rate-limit/unavailable failure retries once on the
// binding's secondary (per-binding secondary= or [defaults] secondary) when
// configured (sty_5bf61f89).
func (g *Engine) Invoke(ctx context.Context, req InvokeRequest) InvokeResult {
	// A secret the secondary resolves is kept out of the log from the first
	// line, before the primary's own input is written.
	if req.Log != nil {
		if sec, _, ok := g.secondaryFor(req); ok {
			req.Log.addSecrets(g.scrubValues(sec.Env))
		}
	}
	res := g.invokeModels(ctx, req)
	if res.Err == nil {
		return res
	}
	if !IsRateLimitOrUnavailable(res.Err, res.Stdout) {
		return res
	}
	sec, secName, ok := g.secondaryFor(req)
	if !ok {
		return res
	}
	section := req.Section
	if section == "" {
		section = "agent"
	}
	g.telemetryEvent(ctx, req.StoryID, req.Actor, "agent-secondary-failover", map[string]any{
		"primary": section, "secondary": secName, "outcome": "rate-limit",
		"primary_err": res.Err.Error(),
	})
	g.emitProgress("primary binding %q rate-limited/unavailable — retrying once on secondary %q…", section, secName)
	secReq := req
	secReq.Binding = sec
	secReq.Section = secName
	secReq.Runner = nil // rebuild runner from secondary binding
	return g.invokePrimary(ctx, secReq)
}

// RanModel is the model a binding's model list moved on to, or fallback (the
// model the caller chose) when it did not move — so a verdict or performance
// row credits the model that answered, not the first one listed.
func (r InvokeResult) RanModel(fallback string) string {
	if r.Model != "" {
		return r.Model
	}
	return fallback
}

// invokeModels runs req on its binding's models in order (sty_7bfa980d): the
// same command, tools, effort, role and payload, only the model changing. It
// moves to the next model only when nextModelReason says the current one did
// not answer, records each move on an agent_invocation row (phase
// model_failover) with the model it ran and why it moved on, and returns the
// last attempt's result. A binding with one model, or none, is one
// invokePrimary exactly as before.
func (g *Engine) invokeModels(ctx context.Context, req InvokeRequest) InvokeResult {
	models := req.Binding.Models()
	if len(models) < 2 {
		return g.invokePrimary(ctx, req)
	}
	section := req.Section
	if section == "" {
		section = "agent"
	}
	var res InvokeResult
	for i, m := range models {
		attempt := req
		attempt.Binding.Model, attempt.Binding.ModelFallbacks = m, nil
		res = g.invokePrimary(ctx, attempt)
		if i > 0 {
			res.Model = m
		}
		if i == len(models)-1 {
			break
		}
		reason, ok := nextModelReason(ctx, res)
		if !ok {
			break
		}
		next := models[i+1]
		cause := "no parseable verdict for any rubric"
		if res.Err != nil {
			cause = res.Err.Error()
		}
		row := map[string]any{
			"phase": "model_failover", "agent": section, "skill": req.Skill,
			"model": m, "next_model": next, "outcome": reason, "error": cause,
		}
		if tp, ok := transitionOf(req.Payload); ok {
			row["from"], row["to"] = tp.From, tp.To
		}
		g.recordInvocation(ctx, req.StoryID, row)
		g.telemetryEvent(ctx, req.StoryID, req.Actor, "agent-model-failover", map[string]any{
			"agent": section, "model": m, "next_model": next, "outcome": reason, "cause": causeOf(res.Err),
		})
		g.emitProgress("binding %q model %q gave no answer (%s) — retrying once on model %q…", section, m, reason, next)
	}
	return res
}

// errNoVerdict marks a verdict dispatch whose attempts all ended without a
// parseable verdict; errGrantRefused marks a reviewer refused before it ran
// because its grant cannot open the material it was handed (sty_7bfa980d).
var (
	errNoVerdict    = errors.New("no verdict")
	errGrantRefused = errors.New("reviewer grant refused")
)

// nextModelReason reports whether a dispatch that ended with res should be
// re-run on the binding's next listed model, and the outcome class it moved on
// for (sty_7bfa980d): the agent did not answer (no final message, no
// parseable verdict), it timed out or stalled, or it hit a classified
// rate-limit/unavailability error. A cancel, a turn cap, a refused grant, a
// broken command and every other failure stay where they are — another model
// would fail them the same way. A verdict, even a fail-closed one, is an
// answer, except a bundle that failed closed because no attempt named one.
func nextModelReason(ctx context.Context, res InvokeResult) (string, bool) {
	if ctx.Err() != nil {
		return "", false
	}
	err := res.Err
	if err == nil {
		return "no-verdict", res.NoVerdict
	}
	switch {
	case errors.Is(err, errGrantRefused), errors.Is(err, agentcli.ErrMaxTurns):
		return "", false
	case asStallError(err) != nil:
		return "stalled", true
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", true
	case errors.Is(err, agentcli.ErrNoAnswer):
		return "no-answer", true
	case errors.Is(err, errNoVerdict):
		return "no-verdict", true
	case IsRateLimitOrUnavailable(err, res.Stdout):
		return "rate-limit", true
	}
	return "", false
}

// secondaryFor is the binding req fails over to on a rate limit, and its name;
// false when none is configured.
func (g *Engine) secondaryFor(req InvokeRequest) (config.AgentBinding, string, bool) {
	if g.resolveSecondary == nil {
		return config.AgentBinding{}, "", false
	}
	section := req.Section
	if section == "" {
		section = "agent"
	}
	return g.resolveSecondary(section, req.Binding)
}

// invokePrimary runs one binding without secondary failover.
func (g *Engine) invokePrimary(ctx context.Context, req InvokeRequest) InvokeResult {
	binding := req.Binding
	section := req.Section
	if section == "" {
		section = "agent"
	}
	role := config.ResolvedRole(section, binding)
	expect := req.Expect
	// When the caller did not set ExpectPerform explicitly and role is reviewer,
	// default remains ExpectVerdict (zero value). For perform-role bindings the
	// caller must set ExpectPerform (DispatchExecutor/Retrospect do).
	_ = role

	charter := req.Charter
	if charter == "" {
		switch expect {
		case ExpectVerdict, ExpectVerdicts:
			charter = reviewerCharter()
		case ExpectPerform:
			// Perform charter is call-site specific (agent/step/workflow names);
			// empty is valid (e.g. if the caller already inlined context).
		}
	}

	principles := binding.ResolvedPrinciples()
	inv := invocation{
		charter:    charter,
		rubric:     req.Rubric,
		principles: principles,
		payload:    req.Payload,
		tools:      binding.Tools,
		model:      binding.Model,
		effort:     binding.Effort,
		settings:   binding.Settings,
		env:        binding.Env,
	}
	// Fall back to engine reviewer caches when the binding leaves grant fields
	// empty (tests that only set g.tools/g.model still work until order:3 deletes
	// the scalar path entirely). Prefer binding values when set.
	if inv.tools == "" {
		inv.tools = g.tools
	}
	if inv.model == "" {
		inv.model = g.model
	}
	if len(inv.env) == 0 {
		inv.env = g.reviewerEnv
	}
	// The values this run's environment resolved, before any dispatch marker or
	// scratch path is overlaid, are what its log never writes.
	req.Log.addSecrets(g.scrubValues(inv.env))
	// Mark every isolated dispatch — perform AND verdict alike (sty_719c4a7b
	// AC4): a verdict reviewer's child process inherits the driver's
	// SATELLE_SESSION from the OS environment exactly as a performer's does
	// (agentcli's composeEnv bases every transport on os.Environ()), so an
	// unmarked verdict dispatch would still trip bindSessionID's in-loop
	// publish and overwrite the driver's own file. PreToolUse also reads this
	// marker to distinguish the child the workflow deliberately dispatched
	// from the driving session trying to work ahead while a transition is in
	// flight. Copy before overlay so binding-owned environment remains intact
	// and reserved marker keys cannot accidentally identify a different
	// dispatch.
	env := make(map[string]string, len(inv.env)+3)
	for k, v := range inv.env {
		env[k] = v
	}
	// The judged or performed item's typed inputs (SATELLE_INPUT_<NAME>); the
	// reserved markers below are overlaid after them.
	for k, v := range workitem.InputEnv(payloadInputs(req.Payload)) {
		env[k] = v
	}
	// A process secret this process's own environment lacks (a node's declared
	// secret that the harness above this satelle did not pass down, read back
	// from join's hand-off) is given to the child here, in its environment only,
	// unless the binding sets that key itself. The log already scrubs it
	// (scrubValues, above).
	for k, v := range g.processSecrets {
		if _, set := env[k]; !set && os.Getenv(k) == "" {
			env[k] = v
		}
	}
	env[config.DispatchAgentEnv] = section
	env[config.DispatchStepEnv] = req.Step
	env[config.DispatchItemEnv] = req.StoryID
	if id := config.SessionFromEnv(); id != "" {
		env[config.SessionEnv] = id
	}
	inv.env = env

	// Every dispatch gets its own scratch directory (sty_e7aaf8b1 AC1): TMPDIR
	// and SATELLE_SCRATCH are RESERVED keys and win over any binding env of the
	// same name, so overlay them last.
	scratchDir, screrr := newScratch(g.repoRoot, req.StoryID)
	if screrr != nil {
		return InvokeResult{Err: screrr}
	}
	inv.scratch = scratchDir
	// Binding env, [vars]-expanded values and SATELLE_INPUT_* resolve together,
	// here at the point of use; an unresolvable reference fails the dispatch
	// before any child runs (sty_bd8d963c).
	var resolved map[string]string
	var rerr error
	if inv.env, resolved, rerr = dispatchEnv(ctx, inv.env, scratchDir); rerr != nil {
		finishScratch(scratchDir, false)
		return InvokeResult{Err: rerr}
	}
	req.Log.addSecrets(resolved)
	inv.judging = expect.judges()
	inv.inline = inv.judging && !readsLocalPaths(req.Runner)
	inv.shell = agentcli.GrantAdmitsShell(inv.tools)

	// The full swap is only the judging path, and only when the caller handed
	// over the pre-cap material; a local performer swaps its documents alone. A
	// functional check never reaches Invoke. A direct Invoke of a map payload
	// (no Material) stays inline.
	swapped := false
	var ref *referencedPayload
	if tp, mat, ok := referencedWorkItem(req); ok {
		r, werr := writeReviewerMaterial(scratchDir, mat, tp)
		if werr != nil {
			g.recordInvocation(ctx, req.StoryID, map[string]any{
				"phase": "scratch_kept", "scratch_dir": scratchDir, "agent": section,
			})
			finishScratch(scratchDir, true)
			return InvokeResult{Err: werr}
		}
		inv.payload = r
		ref = &r
		swapped = true
	} else if tp, ok := performerDocsByPath(req, inv.tools); ok {
		p, werr := docsByPath(scratchDir, tp)
		if werr != nil {
			finishScratch(scratchDir, true)
			return InvokeResult{Err: werr}
		}
		inv.payload = p
		ref = &referencedPayload{Docs: p.Docs}
	}

	agentReq, err := g.buildRequest(ctx, inv)
	if err != nil {
		finishScratch(scratchDir, false)
		return InvokeResult{Err: err}
	}
	agentReq.Redact = g.scrubValues(nil, resolved)
	if req.Sink != nil {
		agentReq.Sink = req.Sink
	}
	var eventMu sync.Mutex
	var lastMessage time.Time
	// Throttled in-flight activity-detail refresh (sty_752c4ef2 AC5): only for
	// a genuine DISPATCH (ExpectPerform) with a story to attribute it to. Not
	// gate reviewers or the summariser — those are not the "in-flight dispatch"
	// AC6/AC7 render.
	var actCount int
	var actPid int
	var lastActivityPush time.Time
	trackActivity := expect == ExpectPerform && g.activityDetail != nil && req.StoryID != ""
	// Interactive ask-the-user questions the transport denied or auto-answered,
	// ledgered after the run (sty_32795645) — never from inside the callback.
	var asked []agentcli.Event
	// The tool list the harness itself reported offering (claude stream-json
	// system/init), when it does — the source of offered_tool_count over the
	// rendered allow-list (sty_ef3efb51).
	var harnessTools []string
	tally := &toolTally{}
	agentReq.OnEvent = func(ev agentcli.Event) {
		eventMu.Lock()
		defer eventMu.Unlock()
		if req.OnEvent != nil {
			req.OnEvent(ev)
		}
		switch ev.Kind {
		case agentcli.EventStart:
			g.emitProgress("agent %s started", section)
			if pid, err := strconv.Atoi(ev.Meta[agentcli.EventMetaPid]); err == nil {
				actPid = pid
			}
		case agentcli.EventHeartbeat:
			g.emitProgress("agent %s still running…", section)
		case agentcli.EventToolStart:
			tally.add()
			g.emitProgress("agent %s tool started: %s", section, progressLabel(ev.Tool))
		case agentcli.EventToolEnd:
			g.emitProgress("agent %s tool finished: %s", section, progressLabel(ev.Tool))
		case agentcli.EventMessage:
			// Token-level ACP chunks can be extremely noisy. Preserve every event
			// in normalized diagnostics, but rate-limit interactive prose.
			if time.Since(lastMessage) >= time.Second && strings.TrimSpace(ev.Text) != "" {
				g.emitProgress("agent %s: %s", section, progressLabel(ev.Text))
				lastMessage = time.Now()
			}
		case agentcli.EventFailed:
			g.emitProgress("agent %s failed: %s", section, progressLabel(ev.Error))
		case agentcli.EventInteractiveDenied:
			g.emitProgress("agent %s asked a question (%s): %s", section, ev.Meta[agentcli.EventMetaResponse], progressLabel(ev.Text))
			asked = append(asked, ev)
		case agentcli.EventSessionInit:
			if ev.Tools != nil {
				harnessTools = append([]string(nil), ev.Tools...)
			}
		}
		// A text-less EventProgress is the command transport's report that the
		// performer's process tree did work while its CLI printed nothing — the
		// only activity that transport can expose. It refreshes the last-activity
		// time as "working" but is not counted as a real event, so the idle-warn
		// count and the stall detector keep their semantics.
		realEv := isRealEvent(ev.Kind)
		if trackActivity && (realEv || (ev.Kind == agentcli.EventProgress && ev.Text == "")) {
			label := "working"
			if realEv {
				actCount++
				label = eventLabel(ev)
			}
			now := time.Now()
			if (realEv && actCount == 1) || now.Sub(lastActivityPush) >= activityDetailThrottle {
				lastActivityPush = now
				g.activityDetail(req.StoryID, ActivityDetail{
					Agent: section, Model: agentReq.Model, Pid: actPid, EventLabel: label,
					EventAt: now, EventCount: actCount,
				})
			}
		}
	}
	// Verdict parsing extracts decision JSON from anywhere in the blob
	// (parseDecision). Keep the full ACP stream so a decision emitted before
	// trailing chatter is not dropped by the answer-only segment rule
	// (sty_844b6ab1 AC6). Prose paths (summariser, perform) keep the zero
	// value CaptureAnswer so narration before tool fences is stripped.
	if expect.judges() {
		agentReq.Capture = agentcli.CaptureFull
	}

	runner := req.Runner
	if runner == nil {
		r, rerr := g.newRunner(binding.ResolvedInterface(), binding.CommandTemplate())
		if rerr != nil {
			finishScratch(scratchDir, false)
			return InvokeResult{Err: fmt.Errorf("broken command for binding %q: %w", section, rerr)}
		}
		if r == nil {
			finishScratch(scratchDir, false)
			return InvokeResult{Err: fmt.Errorf(
				"binding %q is command=in-loop and cannot produce an isolated agent run", section)}
		}
		runner = r
	}
	cmdStr := runner.Command()

	// Reviewer tool isolation (sty_ef3efb51): a reviewer judges, so it may use
	// only the tools its binding grants. Warn — before the process starts — of a
	// binding no adapter path can keep inside that grant (sty_2d5e583a: it still
	// runs), then mark the request so the adapter trims what the harness offers
	// and every live transport denies a tool outside the grant by name.
	var isolation verb.ToolIsolation
	var guard *isolationGuard
	if expect.judges() {
		var ierr error
		isolation, guard, ierr = g.isolateReviewer(ctx, reviewerDispatch{
			StoryID: req.StoryID, Actor: req.Actor, Skill: req.Skill, Step: req.Step, Section: section,
		}, runner, &agentReq)
		if ierr != nil {
			finishScratch(scratchDir, false)
			return InvokeResult{Err: ierr, Command: cmdStr}
		}
	} else {
		// Performer limits are grants, not prompt text: a performer may not move
		// the item it performs. An adapter that cannot express the deny refuses.
		if derr := agentcli.DenyProblem(runner); derr != nil {
			finishScratch(scratchDir, false)
			return InvokeResult{Err: fmt.Errorf("performer binding %q refused: %w", section, derr), Command: cmdStr}
		}
		agentReq.DeniedTools = append([]string(nil), agentcli.PerformerDeniedTools...)
	}
	// A judging reviewer that was handed files must be able to open them.
	// An empty read class skips the runner; the edge does not accept.
	if swapped && !agentcli.GrantAdmitsRead(inv.tools) {
		err := agentcli.Mark(fmt.Errorf("unavailable: %s: reviewer grant admits no read tool; the material at %s could not be opened",
			runner.Name(), filepath.Join(scratchDir, "material")), errGrantRefused)
		g.recordInvocation(ctx, req.StoryID, map[string]any{
			"phase": "scratch_kept", "scratch_dir": scratchDir, "agent": section,
		})
		finishScratch(scratchDir, true)
		return InvokeResult{Err: err, Command: cmdStr}
	}

	timeout := req.Timeout
	if timeout <= 0 && expect.judges() {
		timeout = g.agentTimeout
	}
	idle := req.IdleTimeout
	if idle <= 0 {
		idle = g.idleTimeout
	}
	busy, berr := g.busyTimeoutFor(section, binding)
	if berr != nil {
		finishScratch(scratchDir, false)
		return InvokeResult{Err: fmt.Errorf("binding %q: invalid busy_timeout: %w", section, berr)}
	}

	// A leftover sweep only ever runs for a PERFORM dispatch — reviewers are
	// read-only — and only when this repo configured a rule, so a repo with no
	// [dispatch.leftovers] pays no extra git cost (sty_e7aaf8b1 AC5/AC6).
	var leftoverBefore map[string]bool
	sweepConfigured := len(g.leftoverRule.Patterns) > 0 || strings.TrimSpace(g.leftoverRule.ContentRegex) != ""
	if expect == ExpectPerform && sweepConfigured {
		leftoverBefore, _ = untrackedSnapshot(g.repoRoot)
	}

	if req.Log != nil {
		logInput(req.Log, agentReq.SystemPrompt, agentReq.Payload, req.Payload, scratchDir, ref)
	}

	var res InvokeResult
	switch expect {
	case ExpectPerform:
		out, usage, runErr := g.runOnceBusy(ctx, runner, agentReq, timeout, idle, busy)
		if se := asStallError(runErr); se != nil {
			res = g.stallResult(ctx, req, cmdStr, se)
			res.Usage = usage
		} else {
			res = InvokeResult{Stdout: out, Usage: usage, Command: cmdStr, Err: runErr}
		}
	default: // ExpectVerdict, ExpectVerdicts
		res = g.invokeVerdict(ctx, req, runner, agentReq, cmdStr, timeout, idle, busy, tally)
		if gerr := guard.err(); gerr != nil {
			// A tool outside the read-only grant was observed: the verdict fails.
			res.Decision, res.Decisions, res.Err = nil, nil, gerr
		}
	}
	// The run's turns and tool calls, on every outcome: a failed attempt
	// measured them too. A judging run records them; a decision carries them
	// only on the verdict it produced, and a bundle puts them on its first verdict.
	counts := tally.counts(res.Usage, binding.ResolvedInterface(), cmdStr)
	if expect.judges() {
		res.Counts = counts
		if res.Decision != nil {
			res.Decision.RunCounts = counts
		}
	}
	if req.Log != nil {
		s := dispatchSummary{
			Turns:     countLine(counts.Turns, counts.TurnsUnavailableReason),
			ToolCalls: countLine(counts.ToolCalls, counts.ToolCallsUnavailableReason),
			Usage:     usageLine(res.Usage, noUsageReason(runner)),
			Outcome:   "ok",
			Answer:    g.finalAnswer(expect, res),
		}
		if res.Err != nil {
			s.Outcome, s.Error = classifyOutcome(res.Err), res.Err.Error()
		}
		req.Log.summary(s)
	}
	// Byte lengths of what satelle actually sent (sty_363eaf55) — lengths only,
	// never content. Stamped uniformly regardless of outcome (success, failure,
	// or stall) since the request was already dispatched by this point.
	res.SystemPromptBytes = len(agentReq.SystemPrompt)
	res.PayloadBytes = len(agentReq.Payload)
	eventMu.Lock()
	askedNow := append([]agentcli.Event(nil), asked...)
	reported := harnessTools
	eventMu.Unlock()
	if expect.judges() {
		if reported != nil {
			// The harness's own report wins over the rendered allow-list.
			n := agentcli.OfferedToolCount(reported)
			isolation.OfferedToolCount, isolation.OfferedToolsSource = &n, agentcli.OfferedSourceHarness
		}
		res.ToolIsolation = isolation
	}
	g.ledgerInteractiveDenied(ctx, req, section, askedNow)

	var swept []string
	var sweepFailed bool
	if expect == ExpectPerform && leftoverBefore != nil {
		files, serr := SweepLeftovers(g.repoRoot, scratchDir, leftoverBefore, g.leftoverRule)
		if serr != nil {
			// SweepLeftovers may have already moved SOME matched files out of the
			// tree before hitting this error — files still reports every match it
			// attempted. Never delete scratch on this path: that would silently
			// destroy whatever it already moved, with nothing left to recover it
			// from (sty_e7aaf8b1 AC5).
			sweepFailed = true
			g.recordInvocation(ctx, req.StoryID, map[string]any{
				"phase": "leftovers_failed", "scratch_dir": scratchDir, "agent": section,
				"files": files, "error": serr.Error(),
			})
		} else if len(files) > 0 {
			swept = files
			g.ledgerLeftovers(ctx, req.StoryID, scratchDir, g.leftoverRule.ResolveAction(), files)
		}
	}
	keepScratch := res.Err != nil || sweepFailed || (len(swept) > 0 && g.leftoverRule.ResolveAction() == config.LeftoverActionMove)
	if res.Err != nil {
		g.recordInvocation(ctx, req.StoryID, map[string]any{
			"phase": "scratch_kept", "scratch_dir": scratchDir, "agent": section,
		})
	}
	finishScratch(scratchDir, keepScratch)
	return res
}

// toolIsolation maps an adapter's reviewer description onto the ledger shape: a
// count only where the adapter has a real offered-tool figure.
func toolIsolation(iso agentcli.ReviewerIsolation) verb.ToolIsolation {
	out := verb.ToolIsolation{OfferedToolsSource: iso.OfferedSource, IsolationLimitation: iso.Limitation}
	if iso.OfferedTools != nil {
		n := agentcli.OfferedToolCount(iso.OfferedTools)
		out.OfferedToolCount = &n
	}
	return out
}

// ledgerInteractiveDenied records one "agent-interactive-denied" entry per
// ask-the-user question the transport denied or auto-answered, with the tool
// name and question text, so an Ask that would once have stalled the dispatch is
// visible (sty_32795645).
func (g *Engine) ledgerInteractiveDenied(ctx context.Context, req InvokeRequest, section string, asked []agentcli.Event) {
	for _, ev := range asked {
		g.ledgerInteractiveDeniedFor(ctx, req.StoryID, req.Actor, section, req.Step, ev)
	}
}

// ledgerInteractiveDeniedFor writes the row for one denied ask; shared by the
// one-shot and live-session paths. An empty actor defaults to "executor".
func (g *Engine) ledgerInteractiveDeniedFor(ctx context.Context, storyID, actor, section, step string, ev agentcli.Event) {
	if actor == "" {
		actor = "executor"
	}
	g.telemetryEvent(ctx, storyID, actor, "agent-interactive-denied", map[string]any{
		"agent": section, "step": step, "tool": ev.Tool, "question": ev.Text,
		"response": ev.Meta[agentcli.EventMetaResponse],
	})
}

// asStallError extracts a *StallError from err via errors.As, or nil.
func asStallError(err error) *StallError {
	var se *StallError
	if errors.As(err, &se) {
		return se
	}
	return nil
}

// stallResult builds the ledgered "agent-stalled" telemetry and the refusal
// InvokeResult for a stalled dispatch (AC2), shared by the verdict retry loop
// and the perform path so a Watchdog firing is recorded and phrased
// identically wherever it happens.
func (g *Engine) stallResult(ctx context.Context, req InvokeRequest, cmdStr string, se *StallError) InvokeResult {
	name := req.Skill
	if name == "" {
		name = req.Section
	}
	actor := req.Actor
	if actor == "" {
		actor = "executor"
	}
	g.telemetryEvent(ctx, req.StoryID, actor, "agent-stalled", map[string]any{
		"skill": name, "step": req.Step, "idle": se.Idle.String(),
		"last_event": se.LastEvent, "last_event_at": se.LastEventAt,
	})
	return InvokeResult{Command: cmdStr, Err: fmt.Errorf(
		"agent %q %w — the transition was NOT enacted", name, se)}
}

// logInput writes what a dispatch was sent, before it runs: the system prompt
// and payload as delivered, the sections a gate or rework relies on, and the
// content of each scratch material file, which is deleted after the run. A
// persistent story document is written as its path, never copied. sent is the
// payload before any swap to scratch paths; ref is the swapped work item, nil
// when the payload went inline.
func logInput(l *dispatchLog, prompt, payload string, sent any, scratch string, ref *referencedPayload) {
	tp, _ := transitionOf(sent)
	material := inputMaterial(tp, scratch, ref)
	l.input("system prompt", substituteDocPaths(prompt, tp.Docs))
	l.input("payload", substituteDocPaths(payload, tp.Docs))
	l.input("sections", renderInputSections(tp, material))
	for _, m := range material {
		if !m.inline {
			continue
		}
		b, err := os.ReadFile(m.path)
		if err != nil {
			l.input("material "+m.path, "unreadable: "+err.Error())
			continue
		}
		l.input("material "+m.path, string(b))
	}
}

// substituteDocPaths replaces, in text, the JSON-encoded body of each document
// read from a persistent file with that file's path. Matching the encoded form
// keeps every other byte of the payload as it was sent.
func substituteDocPaths(text string, docs []DocState) string {
	for _, d := range docs {
		if d.Path == "" || d.Body == "" {
			continue
		}
		body, berr := json.Marshal(d.Body)
		path, perr := json.Marshal(d.Path)
		if berr != nil || perr != nil {
			continue
		}
		for _, key := range []string{`"body":`, `"body": `} {
			text = strings.ReplaceAll(text, key+string(body), key+string(path))
		}
	}
	return text
}

// materialFile is one file a dispatch's material names. inline marks a file
// that will not outlive the dispatch, whose content the log therefore carries.
type materialFile struct {
	path, note string
	inline     bool
}

// inputMaterial lists the files a dispatch was handed: each scratch file of a
// swapped work item (a scratch copy of a persistent document names the
// original), or else each persistent document the inline payload carries.
func inputMaterial(tp transitionPayload, scratch string, ref *referencedPayload) []materialFile {
	var out []materialFile
	if ref == nil {
		for _, d := range tp.Docs {
			if d.Path != "" && d.Body != "" {
				out = append(out, materialFile{path: d.Path, note: d.Name})
			}
		}
		return out
	}
	sources := map[string]string{}
	for _, d := range ref.Docs {
		if d.source != "" {
			sources[d.Path] = d.source
		}
	}
	_ = filepath.WalkDir(filepath.Join(scratch, "material"), func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		if abs, aerr := filepath.Abs(p); aerr == nil {
			p = abs
		}
		if src := sources[p]; src != "" {
			out = append(out, materialFile{path: p, note: "scratch copy of " + src + ", removed after dispatch"})
			return nil
		}
		out = append(out, materialFile{path: p, note: "scratch, removed after dispatch; content below", inline: true})
		return nil
	})
	return out
}

// renderInputSections names, under the payload's own field names, the parts of
// a payload a gate or rework relies on, each "none" when the payload has none.
func renderInputSections(tp transitionPayload, material []materialFile) string {
	var b strings.Builder
	section := func(label string, lines []string) {
		if len(lines) == 0 {
			fmt.Fprintf(&b, "%s: none\n", label)
			return
		}
		fmt.Fprintf(&b, "%s:\n", label)
		for _, l := range lines {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}
	var findings []string
	for _, f := range tp.Findings {
		line := "- " + f.ID
		if f.Seat != "" {
			line += " seat=" + f.Seat
		}
		line += " decision=" + f.Decision
		if f.Blocking {
			line += " blocking"
		}
		findings = append(findings, line)
	}
	section("findings_to_answer", findings)
	var messages []string
	for _, m := range tp.Messages {
		first, _, _ := strings.Cut(strings.TrimSpace(m.Body), "\n")
		messages = append(messages, fmt.Sprintf("- %s %s → %s: %s", m.ID, m.From, m.To, first))
	}
	section("messages", messages)
	var criteria []string
	for _, l := range strings.Split(tp.Story.AcceptanceCriteria, "\n") {
		if strings.TrimSpace(l) != "" {
			criteria = append(criteria, l)
		}
	}
	section("acceptance_criteria", criteria)
	var files []string
	for _, m := range material {
		files = append(files, fmt.Sprintf("- %s (%s)", m.path, m.note))
	}
	section("material", files)
	return strings.TrimRight(b.String(), "\n")
}

// dispatchSummary closes a dispatch log: the run's progress, each figure a
// number or the adapter-named reason it is unavailable, then its final answer.
type dispatchSummary struct {
	Turns, ToolCalls, Usage, Outcome, Error, Answer string
}

func (s dispatchSummary) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "turns: %s\ntool_calls: %s\nusage: %s\noutcome: %s\n", s.Turns, s.ToolCalls, s.Usage, s.Outcome)
	if s.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", s.Error)
	}
	answer := s.Answer
	if strings.TrimSpace(answer) == "" {
		answer = "none"
	}
	b.WriteString("final_answer:\n" + answer)
	return b.String()
}

// countLine is a measured count, or the reason it was not measured.
func countLine(n *int, reason string) string {
	if n != nil {
		return strconv.Itoa(*n)
	}
	return reason
}

// usageLine is the run's reported usage, or why it has none (fallback when the
// result carries no reason of its own).
func usageLine(u agentcli.UsageResult, fallback string) string {
	if !u.Available {
		if u.UnavailableReason != "" {
			return u.UnavailableReason
		}
		return fallback
	}
	s := fmt.Sprintf("input=%d output=%d total=%d", u.InputTokens, u.OutputTokens, u.TotalTokens)
	if u.ModelResolved != "" {
		s += " model=" + u.ModelResolved
	}
	return s
}

// noUsageReason names the adapter of a run that reported no usage.
func noUsageReason(r agentcli.Runner) string {
	return r.Name() + " adapter: transport reported no usage"
}

// verdictAnswer is a decision as a dispatch summary writes it.
type verdictAnswer struct {
	Skill    string `json:"skill,omitempty"`
	Decision string `json:"decision"`
	Notes    string `json:"notes,omitempty"`
}

func verdictOf(skill string, d verb.GateDecision) verdictAnswer {
	word := "reject"
	if d.Accept {
		word = "accept"
	}
	return verdictAnswer{Skill: skill, Decision: word, Notes: d.Notes}
}

// finalAnswer is the decoded answer of a run — its verdict(s), its artifact, or
// its decision — and, when nothing decodes, the tail of its output and where
// the whole of it is logged.
func (g *Engine) finalAnswer(expect Expect, res InvokeResult) string {
	var v any
	switch {
	case len(res.Decisions) > 0:
		vs := make([]verdictAnswer, len(res.Decisions))
		for i, d := range res.Decisions {
			vs[i] = verdictOf(d.Skill, d)
		}
		v = vs
	case res.Decision != nil:
		v = verdictOf("", *res.Decision)
	}
	if v == nil {
		if len(bytes.TrimSpace(res.Stdout)) == 0 {
			return ""
		}
		if a, err := agentartifact.Decode(res.Stdout); err == nil {
			return fmt.Sprintf("artifact %s (%s):\n%s", a.Name, a.Type, a.Body)
		}
		if d, err := parseDecision(res.Stdout); err == nil {
			v = verdictOf("", d)
		}
	}
	if v != nil {
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b)
	}
	tail := verb.LastMessage(strings.TrimSpace(string(res.Stdout)))
	if g.logDir != "" {
		full := "executor.log"
		if expect.judges() {
			full = "reviewer.log"
		}
		tail += "\nfull output: " + filepath.Join(g.logDir, full)
	}
	return tail
}

func progressLabel(s string) string {
	s = agentcli.SafeText(s)
	if s == "" {
		return "activity"
	}
	return s
}

// IsRateLimitOrUnavailable reports whether an isolated-agent failure should
// trigger secondary failover (sty_5bf61f89): rate limits, capacity, quota, and
// hard unavailability. Other errors stay fail-loud without fallback.
func IsRateLimitOrUnavailable(err error, stdout []byte) bool {
	var b strings.Builder
	if err != nil {
		b.WriteString(err.Error())
		b.WriteByte(' ')
	}
	b.Write(stdout)
	s := strings.ToLower(b.String())
	for _, needle := range []string{
		"rate limit", "rate-limit", "ratelimit", "too many requests", " 429", "status 429",
		"overloaded", "capacity", "resource_exhausted", "quota exceeded", "quota_exceeded",
		"temporarily unavailable", "service unavailable", " 503", "status 503",
		"usage limit", "tokens per min", "request limit",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// invokeVerdict runs the ExpectVerdict retry loop: parse JSON/prose decision,
// retry transient no-verdict, fail loud on timeout after attempts.
func (g *Engine) invokeVerdict(ctx context.Context, req InvokeRequest, runner agentcli.Runner, agentReq agentcli.Request, cmdStr string, timeout, idle, busy time.Duration, tally *toolTally) InvokeResult {
	skill := req.Skill
	if skill == "" {
		skill = "reviewer"
	}
	actor := req.Actor
	if actor == "" {
		actor = "reviewer"
	}
	attempts := req.Attempts
	if attempts < 1 {
		attempts = g.attempts
	}
	if attempts < 1 {
		attempts = 1
	}
	storyID := req.StoryID
	step := req.Step

	var lastErr error
	var lastOut []byte
	// lastNoVerdict is true when the LAST attempt ran to completion but a bundled
	// response named no verdict for any rubric — as opposed to a runner failure.
	// Only that case fails every rubric closed; a runner failure stays an error,
	// exactly as it does for a lone gate.
	var lastNoVerdict bool
	var lastUsage agentcli.UsageResult
	for attempt := 1; attempt <= attempts; attempt++ {
		lastNoVerdict = false
		if werr := g.retryWait(ctx, attempt); werr != nil {
			return InvokeResult{Command: cmdStr, Err: werr}
		}
		g.emitProgress("running reviewer %s (attempt %d/%d, may take several minutes)…", skill, attempt, attempts)
		tally.reset()
		out, usage, rerr := g.runOnceBusy(ctx, runner, agentReq, timeout, idle, busy)
		if rerr != nil {
			if se := asStallError(rerr); se != nil {
				return g.stallResult(ctx, req, cmdStr, se)
			}
			if errors.Is(rerr, context.DeadlineExceeded) && ctx.Err() == nil {
				g.logReviewerFailure(skill, attempt, attempts, rerr, nil)
				g.telemetryEvent(ctx, storyID, actor, "agent-timeout", map[string]any{
					"skill": skill, "step": step, "attempt": attempt, "attempts": attempts,
					"cause": causeOf(rerr),
				})
				return InvokeResult{Command: cmdStr, Err: agentcli.Mark(fmt.Errorf(
					"reviewer: %s timed out after %s — the gate did not complete and the transition was NOT enacted; retry when the agent backend is responsive", skill, timeout), rerr)}
			}
			if agentcli.DetectMaxTurns(out) {
				rerr = fmt.Errorf("%w: %w", agentcli.ErrMaxTurns, rerr)
			}
			lastErr, lastOut, lastUsage = rerr, nil, usage
			g.logReviewerFailure(skill, attempt, attempts, rerr, nil)
			g.telemetryEvent(ctx, storyID, actor, "agent-retry", map[string]any{
				"skill": skill, "step": step, "attempt": attempt, "attempts": attempts, "outcome": classifyOutcome(rerr),
				"cause": causeOf(rerr),
			})
			continue
		}
		if req.Expect == ExpectVerdicts {
			// Bundled: the same retry/repair loop, a different parse — one verdict
			// per requested rubric, a missing one failing closed (sty_23e10d92).
			decs, perr := parseBundleDecisions(out, req.Skills)
			if perr != nil {
				lastErr, lastOut, lastUsage, lastNoVerdict = perr, out, usage, true
				if agentcli.DetectMaxTurns(out) {
					// Cut short at the turn cap, not a session that judged and named
					// no verdict: it is an error, never a fail-closed reject.
					lastErr, lastNoVerdict = fmt.Errorf("%w: %w", agentcli.ErrMaxTurns, perr), false
				}
				g.logReviewerFailure(skill, attempt, attempts, perr, out)
				g.telemetryEvent(ctx, storyID, actor, "agent-retry", map[string]any{
					"skill": skill, "step": step, "attempt": attempt, "attempts": attempts, "outcome": "no-verdict",
					"cause": causeOf(perr),
				})
				continue
			}
			for i := range decs {
				decs[i].Gated = true
				decs[i].Command = cmdStr
				decs[i].Context = decs[i].Skill
			}
			return InvokeResult{Stdout: out, Usage: usage, Command: cmdStr, Decisions: decs}
		}
		dec, perr := parseDecision(out)
		if perr != nil {
			if pd, ok := parseProseDecision(out); ok {
				g.logProseFallback(skill, pd.Accept)
				pd.Gated = true
				pd.Skill = skill
				pd.Command = cmdStr
				pd.Context = skill
				g.setDecisionUsage(&pd, usage, agentReq.Model)
				return InvokeResult{Stdout: out, Usage: usage, Command: cmdStr, Decision: &pd}
			}
			if agentcli.DetectMaxTurns(out) {
				perr = fmt.Errorf("%w: %w", agentcli.ErrMaxTurns, perr)
			}
			lastErr, lastOut, lastUsage = perr, out, usage
			g.logReviewerFailure(skill, attempt, attempts, perr, out)
			g.telemetryEvent(ctx, storyID, actor, "agent-retry", map[string]any{
				"skill": skill, "step": step, "attempt": attempt, "attempts": attempts, "outcome": "no-verdict",
				"cause": causeOf(perr),
			})
			continue
		}
		dec.Gated = true
		dec.Skill = skill
		dec.Command = cmdStr
		dec.Context = skill
		g.setDecisionUsage(&dec, usage, agentReq.Model)
		return InvokeResult{Stdout: out, Usage: usage, Command: cmdStr, Decision: &dec}
	}
	where := ""
	if len(bytes.TrimSpace(lastOut)) > 0 && g.logDir != "" {
		where = " — full reviewer output logged to " + filepath.Join(g.logDir, "reviewer.log")
	}
	g.telemetryEvent(ctx, storyID, actor, "agent-failure", withCause(map[string]any{
		"skill": skill, "step": step, "attempts": attempts, "outcome": classifyOutcome(lastErr),
	}, lastErr))
	if req.Expect == ExpectVerdicts && lastNoVerdict {
		// The session ran but named no verdict for any rubric, and the repair
		// attempts did not change that: every rubric fails closed (sty_23e10d92).
		why := fmt.Sprintf("no parseable verdict after %d attempts", attempts)
		decs := make([]verb.GateDecision, len(req.Skills))
		for i, s := range req.Skills {
			decs[i] = failClosedDecision(s, why)
			decs[i].Gated = true
			decs[i].Command = cmdStr
			decs[i].Context = s
		}
		return InvokeResult{Stdout: lastOut, Usage: lastUsage, Command: cmdStr, Decisions: decs, NoVerdict: true}
	}
	return InvokeResult{Command: cmdStr, Usage: lastUsage, Err: agentcli.Mark(fmt.Errorf(
		"reviewer: %s produced no verdict after %d attempts (empty/ambiguous reviewer output or a transient agent failure — e.g. a rate-limited or killed subprocess under concurrent sessions; retry, or reduce concurrent satelle sessions): %w%s%s",
		skill, attempts, lastErr, outputTail(lastOut), where), errNoVerdict)}
}

// runOnce executes a single bounded agent run against a caller-supplied
// runner, an OPTIONAL hard deadline, and an OPTIONAL idle-stall bound
// (sty_752c4ef2). hard ≤0 disables the wall-clock deadline; idle ≤0 disables
// the Watchdog. Prefer Invoke for LLM steps; runOnce remains the primitive
// used by Invoke and by Summarise (until folded).
//
// The Watchdog sits ABOVE the runner: it wraps ctx (context.WithCancelCause)
// and req.OnEvent, so every transport — which spawns its child via
// exec.CommandContext(ctx, …) against this SAME context — is bounded with no
// transport-specific code. A run that ends because the watchdog fired surfaces
// as a *StallError (retrievable via errors.As or StallCause(ctx)), which the
// caller maps to the "stalled" outcome and refusal text instead of a bare
// context.Canceled.
func (g *Engine) runOnce(ctx context.Context, runner agentcli.Runner, req agentcli.Request, hard, idle time.Duration) ([]byte, agentcli.UsageResult, error) {
	return g.runOnceBusy(ctx, runner, req, hard, idle, 0)
}

// runOnceBusy is runOnce plus a busy cap (sty_db62a3b9): busy>0 (with idle>0)
// turns on the command transport's CPU-liveness probe, so a silent one-shot CLI
// whose process tree keeps burning CPU is not judged stalled until busy has
// elapsed since its last real event. Stream and ACP transports never emit the
// probe's EventProgress, so they are unaffected.
func (g *Engine) runOnceBusy(ctx context.Context, runner agentcli.Runner, req agentcli.Request, hard, idle, busy time.Duration) ([]byte, agentcli.UsageResult, error) {
	out, usage, err := g.runOnceBusyRaw(ctx, runner, req, hard, idle, busy)
	// Redact at the source (sty_bd8d963c): runOnceBusyRaw scrubs the events and
	// live Sink as they arrive; the returned bytes and error are scrubbed here,
	// so no caller of runOnceBusy ever holds a raw copy.
	out, err = redactBytes(out, req.Redact), redactErr(err, req.Redact)
	if !usage.Available && usage.UnavailableReason == "" {
		// An adapter that knows why names it; every other unreported run still
		// names the adapter (the runner's CLI) rather than a bare false
		// (sty_c8d45201).
		usage.UnavailableReason = noUsageReason(runner)
	}
	return out, usage, err
}

func (g *Engine) runOnceBusyRaw(ctx context.Context, runner agentcli.Runner, req agentcli.Request, hard, idle, busy time.Duration) ([]byte, agentcli.UsageResult, error) {
	parentCtx := ctx
	hardCtx := ctx
	if hard > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, hard)
		hardCtx = ctx
		defer cancel()
	}
	if idle > 0 {
		wd := NewWatchdog(idle)
		if busy > 0 {
			wd.SetBusyCap(busy)
			req.LivenessInterval = watchdogTick(idle)
		}
		var stop func()
		ctx, stop = wd.Start(ctx)
		defer stop()
		parent := req.OnEvent
		req.OnEvent = func(ev agentcli.Event) {
			wd.TouchEvent(ev)
			if parent != nil {
				parent(ev)
			}
		}
	}
	// The redactor is the outermost handler, next to the transport, so the
	// watchdog's last-event label is scrubbed too (sty_bd8d963c).
	req, flush := redactRequest(req)
	defer flush()
	mapStall := func(err error) error {
		if err == nil {
			return nil
		}
		if se := StallCause(ctx); se != nil {
			return se
		}
		// A transport killed by the hard bound may report only its exit
		// ("signal: killed"); mark the deadline so the run reads as a timeout
		// wherever it is classified (sty_7bfa980d).
		if hard > 0 && parentCtx.Err() == nil && errors.Is(hardCtx.Err(), context.DeadlineExceeded) && !errors.Is(err, context.DeadlineExceeded) {
			return agentcli.Mark(err, context.DeadlineExceeded)
		}
		return err
	}
	start := time.Now()
	// A transport that can report its own resolved-model usage (stream: modelUsage
	// arrives on an EventUsage, never inside the returned bytes) is preferred over
	// sniffing Run's output for a JSON envelope (sty_87b86044 AC2).
	if ur, ok := runner.(agentcli.UsageRunner); ok {
		text, usage, err := ur.RunUsage(ctx, req)
		usage.Duration = time.Since(start)
		return text, usage, mapStall(err)
	}
	raw, err := runner.Run(ctx, req)
	elapsed := time.Since(start)
	if err != nil {
		// A run the harness cut short (a turn cap exits non-zero) still reported
		// what it used before it stopped: keep that, never read it as zero.
		_, partial := agentcli.UnwrapUsage(raw)
		partial.Duration = elapsed
		return raw, partial, mapStall(err)
	}
	text, usage := agentcli.UnwrapUsage(raw)
	usage.Duration = elapsed
	return text, usage, nil
}

// ResidentPrincipleDocs returns the principle documents a seat is handed for the
// given selector (design §5.1), in the order it receives them: session =
// principles:session- or principles:seat-tagged (+ operating principle guarantee); system = embedded
// docs; project = non-embedded; all = every body; none = empty; comma-list =
// union (name-sorted, deduped). Exported so the injected-size report and the
// context audit measure exactly what a dispatch fits and sends.
func ResidentPrincipleDocs(ctx context.Context, docs DocGetter, selector string) []docindex.Doc {
	selector = strings.TrimSpace(selector)
	if selector == "" || selector == config.PrinciplesNone {
		return nil
	}
	// Back-compat: session path is alwaysPrinciples (operating-principle guarantee).
	if selector == config.PrinciplesSession {
		return sessionPrincipleDocs(ctx, docs)
	}

	want := map[string]bool{}
	for _, p := range strings.Split(selector, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			want[p] = true
		}
	}
	if want[config.PrinciplesSession] && len(want) == 1 {
		return sessionPrincipleDocs(ctx, docs)
	}

	// Collect by classification.
	var picked []docindex.Doc
	seen := map[string]bool{}
	add := func(d docindex.Doc) {
		if seen[d.Name] {
			return
		}
		seen[d.Name] = true
		if strings.TrimSpace(stripFrontmatter(d.Body)) != "" {
			picked = append(picked, d)
		}
	}
	match := func(d docindex.Doc) bool {
		if want[config.PrinciplesAll] {
			return true
		}
		if want[config.PrinciplesSystem] && d.Embedded {
			return true
		}
		if want[config.PrinciplesProject] && !d.Embedded {
			return true
		}
		if want[config.PrinciplesSession] && isSeatResident(d.Body) {
			return true
		}
		return false
	}
	if listed, err := docs.List(ctx, "principles"); err == nil {
		sort.Slice(listed, func(i, j int) bool { return listed[i].Name < listed[j].Name })
		for _, d := range listed {
			if match(d) {
				add(d)
			}
		}
	}
	// Operating principle guarantee when session/all is in the selector union.
	if want[config.PrinciplesSession] || want[config.PrinciplesAll] {
		if !seen[config.OperatingPrinciple] {
			if d, err := docs.Get(ctx, "principles", config.OperatingPrinciple); err == nil {
				if want[config.PrinciplesAll] || want[config.PrinciplesSession] ||
					(want[config.PrinciplesSystem] && d.Embedded) ||
					(want[config.PrinciplesProject] && !d.Embedded) {
					add(d)
				}
			}
		}
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].Name < picked[j].Name })
	return picked
}

// ReviewerVerdictDoc returns the body of the shared reviewer-verdict principle
// (config.ReviewerVerdictPrinciple) with its frontmatter stripped: the rules every
// judging dispatch is handed once, in place of each reviewer skill restating them.
// A repo document of that name shadows the embedded default, so the rules stay
// configuration. "" when the document is missing or blank. Exported so the
// injected-size report and the context audit measure what a dispatch sends.
func ReviewerVerdictDoc(ctx context.Context, docs DocGetter) string {
	if docs == nil {
		return ""
	}
	d, err := docs.Get(ctx, "principles", config.ReviewerVerdictPrinciple)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(stripFrontmatter(d.Body))
}

// isolatedAgentBriefing is the shared body of every isolated-agent charter.
const isolatedAgentBriefing = "The work item arrives on stdin as JSON. The " +
	"substrate you reason about — skills, principles, workflows — lives as markdown " +
	"under `.satelle/`; read it directly to resolve anything this rubric references " +
	"but does not inline (including embedded defaults that are not files on disk)."

// pullContextCallToAction rides in EVERY isolated-agent prompt (via buildRequest).
// Attachment READ CHANNEL (sty_58fa970e / sty_4660bbe1): the transition payload's
// `docs` array carries every attachment (name, type, body) so Bash-less reviewers
// can judge without any disk path. Prefer that. Shell-granted agents may also
// pull more via the satelle CLI. NEVER name the obsolete in-repo .satelle/stories/
// path — writing/reading there recreates pre-relocation residue.
const pullContextCallToAction = "## Reconstruct your context (you start fresh)\n\n" +
	"You are dispatched with NO conversation history — the stdin payload carries the " +
	"work item (its `id`, title, body, acceptance criteria), the transition, and a " +
	"`docs` array of every attached document (`name`, `type`, `body`). Prefer the " +
	"payload `docs` for the plan and step summaries — that is how a Bash-less " +
	"reviewer reads what it judges. The payload `diff` object (when present) is " +
	"the engagement slice (`files`, `stat`, `patch`) — enumeration, not a verdict. " +
	"A doc that carries a `path` instead of a `body` was written there for this " +
	"dispatch: read it at that path. When `truncated: true` on a doc, the body is " +
	"omitted; pull the full text with the CLI when your grant includes shell:\n\n" +
	"- `satelle story get <id>` — the full current record.\n" +
	"- `satelle story docs <id>`, then `satelle story doc <id> <name>` — attachments " +
	"beyond (or fuller than) the payload.\n" +
	"- `satelle ledger list --story <id>` — the evidence ledger (transitions, review " +
	"verdicts, summaries).\n\n" +
	"Do **not** look under in-repo `.satelle/stories/` — that path is obsolete " +
	"post-relocation and must not be recreated. Do not assume absence: check the " +
	"payload `docs` (and CLI when available) before concluding a document is missing."

// reviewerCharter is the charter for an isolated gate reviewer.
func reviewerCharter() string {
	return "## You are an isolated satelle reviewer\n\n" +
		"You judge only — you CANNOT modify the repository, and your tool grant is " +
		"read-only. The context briefing below says how the work item is delivered and how to open it. " +
		isolatedAgentBriefing +
		" Judge the OUTCOME the story claims against this rubric and return your verdict."
}

// judgingContextBriefing tells a judging reviewer that the work item on the
// transport carries the story inline and the large fields as paths. It names
// no tool. shell keeps the three CLI lines for a binding that also has a shell.
func judgingContextBriefing(shell bool) string {
	var b strings.Builder
	b.WriteString("## Reconstruct your context (you start fresh)\n\n")
	b.WriteString("You are dispatched with NO conversation history. The work item is the JSON on the transport. ")
	b.WriteString("Story fields, including the body and acceptance criteria, and children are inline. ")
	b.WriteString("`docs[].path`, `diff.patch_path`, and the path objects on `messages`, `prior_verdicts`, `definition_edits`, and `ledger` are files under this dispatch's scratch directory `material`. ")
	b.WriteString("Open them with the configured read grant. ")
	b.WriteString("A `ledger` object whose `unavailable` field is set names why that listing could not be opened. ")
	b.WriteString("Do not look under in-repo `.satelle/stories/` — that path is obsolete post-relocation and must not be recreated.")
	if shell {
		b.WriteString("\n\nWhen the grant also admits a shell:\n\n")
		b.WriteString("- `satelle story get <id>` — the full current record.\n")
		b.WriteString("- `satelle story docs <id>`, then `satelle story doc <id> <name>` — attachments beyond (or fuller than) the files.\n")
		b.WriteString("- `satelle ledger list --story <id>` — the evidence ledger (transitions, review verdicts, summaries).")
	}
	return b.String()
}

// referencedWorkItem reports the transition a judging Invoke should rewrite
// into paths. Anything else — a performer, a check, a bare map — stays as the
// caller passed it.
func referencedWorkItem(req InvokeRequest) (transitionPayload, *reviewerMaterial, bool) {
	if !req.Expect.judges() || req.Material == nil || !readsLocalPaths(req.Runner) {
		return transitionPayload{}, nil, false
	}
	tp, ok := transitionOf(req.Payload)
	if !ok {
		return transitionPayload{}, nil, false
	}
	return tp, req.Material, true
}

// docsReferenced is a payload with its documents sent as paths (sty_73a2dc68,
// satelle-dispatch-pointers): every field of the transition payload as it was,
// except docs, which this shallower field shadows.
type docsReferenced struct {
	transitionPayload
	Docs []referencedDoc `json:"docs,omitempty"`
}

// docsByPath writes tp's documents, full bodies, under the dispatch scratch and
// returns the payload that names each one by path in place of its body.
func docsByPath(scratch string, tp transitionPayload) (docsReferenced, error) {
	docs, err := writeMaterialDocs(filepath.Join(scratch, "material"), tp.docBodies)
	if err != nil {
		return docsReferenced{}, err
	}
	return docsReferenced{transitionPayload: tp, Docs: docs}, nil
}

// readsDocsByPath reports whether a seat that runs on this machine can be sent
// tp's documents as paths: it carries some, and the grant can open a file.
func readsDocsByPath(tp transitionPayload, tools string) bool {
	return len(tp.docBodies) > 0 && agentcli.GrantAdmitsRead(tools)
}

// performerDocsByPath reports the performer payload whose documents should go as
// paths: a perform dispatch on a runner that reads this machine's scratch, which
// readsDocsByPath admits. A remote performer, the cloud prompt and a grant with
// no read tool keep the bodies inline.
func performerDocsByPath(req InvokeRequest, tools string) (transitionPayload, bool) {
	if req.Expect != ExpectPerform || !readsLocalPaths(req.Runner) {
		return transitionPayload{}, false
	}
	tp, ok := transitionOf(req.Payload)
	if !ok || !readsDocsByPath(tp, tools) {
		return transitionPayload{}, false
	}
	return tp, true
}

// transitionOf is payload as a transition payload, false when it is another
// shape (a check's bare map, a nil pointer).
func transitionOf(payload any) (transitionPayload, bool) {
	switch p := payload.(type) {
	case transitionPayload:
		return p, true
	case *transitionPayload:
		if p != nil {
			return *p, true
		}
	}
	return transitionPayload{}, false
}

// pathless is implemented by a runner that cannot open this machine's scratch
// paths (a remote performer), so the work item it judges stays inline.
type pathless interface{ localPathsUnreadable() }

// readsLocalPaths reports whether the runner performs on this machine, where the
// dispatch's scratch paths are readable. A nil runner is built locally.
func readsLocalPaths(r agentcli.Runner) bool {
	_, remote := r.(pathless)
	return !remote
}

// payloadInputs reports the input values of the item a transition payload
// carries; a payload of any other shape carries none.
func payloadInputs(payload any) map[string]any {
	tp, _ := transitionOf(payload)
	return tp.Story.InputValues
}

// executorCharter is the charter for an isolated named executor performing a step.
func executorCharter(agent, step, workflow string) string {
	return fmt.Sprintf("## You are the isolated satelle executor agent %q "+
		"performing step %q of the workflow %q\n\n"+
		"Perform the step's work in this repository. ", agent, step, workflow) +
		isolatedAgentBriefing
}

// consultCharter is the charter for a named binding opened as a CONSULTANT by
// the rework relay (`rework.consult`) — a reviewer consulted while the coder
// converges. Consultation is not review
// ([[satelle-agent-consultation]]): the reply is context the caller may act on,
// the gate that judges the edge still runs cold and one-shot over the payload
// satelle builds, and the consultant never touches story state (sty_a0372443).
func consultCharter(binding, role, status string) string {
	return fmt.Sprintf("## You are the %q binding %q, CONSULTED on this story at status %q\n\n"+
		"You are consulting, NOT judging. Your reply is CONTEXT, not a verdict: the "+
		"reviewer that judges this edge still runs cold and one-shot over the payload "+
		"satelle builds, and its verdict alone advances the stage. Stay centered on the "+
		"story's acceptance criteria — the conversation is context, never a criterion. ",
		role, binding, status) + isolatedAgentBriefing
}
