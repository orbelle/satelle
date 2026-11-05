// Package agentcli abstracts the headless agent CLI the quality-management spine
// shells out to for isolated reviews and summaries. satellites hardcoded claude's
// flag surface (claude -p --append-system-prompt …); satelle routes every
// subprocess through a Runner driven by a CONFIG TEMPLATE, so the operator picks
// their agent and its exact argv in `.satelle/workflows/agents.toml` and no reviewer code
// names a binary or a flag directly.
//
// A command string is a command template: the first token is the binary, the rest
// are argv tokens that may carry the placeholders {system}, {tools}, {model},
// {settings}, and {payload}. At call time satelle substitutes each placeholder into
// its own argv token (so a multi-line system prompt or a JSON payload stays a
// single argument). The work-item payload is ALWAYS also written to the child
// stdin (dual delivery): stdin-first CLIs (claude -p) keep using stdin alone;
// argv-first CLIs (e.g. grok -p {payload}) include {payload} in the template.
//
// agents.toml bindings carry a FULL command template (or "in-loop"/empty). Bare
// single-token CLI names are NOT accepted on the agents.toml path — the operator
// must see the real argv. DefaultClaudeCommand / DefaultGrokCommand are
// the canonical command templates init/migrate expand into. NewRunner generates
// command templates only (init/migrate/detection).
package agentcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Supported agent CLI identifiers.
const (
	CLIClaude = "claude"
	CLIGrok   = "grok"
)

// DefaultClaudeCommand is the claude preset template — the satellites gate argv
// reproduced behind the template seam, hardened with a --disallowedTools denylist
// so the reviewer's grant is a CEILING (deny wins over allow) over any
// permissions the repo's .claude/settings.json would otherwise inherit. {system}
// is the gate/skill body, {tools} the allow-grant, {model} the optional model
// (dropped, with its flag, when unset). The work-item body rides on stdin only —
// this template does NOT place {payload} on -p, so Claude is not double-fed; the
// {payload} placeholder is still available for other harness templates
// (sty_5cf4a1fb dual delivery).
//
// The denylist keeps the work-tree MUTATORS off (Write, Edit, NotebookEdit, and
// Bash — a shell redirect writes files whatever the user's ~/.claude permission
// allows; sty_892517e7) so a
// reviewer can never modify the repo it judges — the read-only invariant. The
// reviewer's default allow-grant ({tools}) is read-only (Read, Grep, Glob) and
// needs no shell: the substrate it reasons about is materialised as markdown
// under .satelle, so it reads it directly. A repo MAY widen the grant in
// .satelle/workflows/agents.toml (transparently), but the default is read-only.
const DefaultClaudeCommand = "claude -p --output-format json --disallowedTools Write,Edit,NotebookEdit,Bash --append-system-prompt {system} --allowedTools {tools} --model {model} --effort {effort}"

// DefaultClaudeStreamCommand is the Claude live-session spawn for
// interface=stream (sty_d244fe1b). System and payload ride the first user
// message, not argv — so this line must not contain {system} or {payload}.
// {tools}/{model}/{effort} stay spawn-time flags (empty drops the preceding
// flag via buildArgs). Reviewers stay on DefaultClaudeCommand (command).
const DefaultClaudeStreamCommand = "claude -p --input-format stream-json --output-format stream-json --verbose --disallowedTools Write,Edit,NotebookEdit,Bash --allowedTools {tools} --model {model} --effort {effort}"

// DefaultGrokCommand is the grok preset template — the proven dogfood reviewer
// command (this repo's own [reviewer]) behind the single-token "grok" preset.
// Grok is argv-first, so the work-item rides on -p {payload} (and also stdin, dual
// delivery). Unlike claude, the read-only grant is BAKED IN (--tools with grok's
// own tool names read_file,grep,list_dir, plus --deny on every mutator) rather
// than drawn from {tools}: the reviewer's default {tools} is Claude tool names
// (Read,Grep,Glob), which grok does not understand — feeding them to grok was the
// exact failure this preset removes. To widen grok's grant, author a full command
// template instead of the bare preset. {model} is dropped (with -m) when unset, so
// grok falls back to its own default unless the binding pins one (e.g. grok-4.5).
//
// The output form is json, not plain: plain carries no usage and no turn count, so
// a grok seat's cost was unmeasured (sty_5ebcd6f9). The json envelope carries
// `usage` and `num_turns` (UnwrapUsage reads both) and its `text` is unwrapped for
// verdict parsing; it carries no tool events (ToolCallsUnavailableReason).
//
// It carries no turn limit: how many turns a run takes is grok's own business,
// not satelle's (sty_58a7bdfe).
const DefaultGrokCommand = "grok -p {payload} --system-prompt-override {system} --tools read_file,grep,list_dir -m {model} --reasoning-effort {effort} --deny Write --deny Edit --deny search_replace --deny write --always-approve --output-format json --no-subagents"

// Request is one headless agent invocation.
type Request struct {
	SystemPrompt string // {system}: appended as the system prompt (the gate/skill body)
	// Payload is the work-item / transition JSON. Dual delivery (sty_5cf4a1fb):
	// always written to the subprocess stdin, and substituted into {payload} when
	// that token appears in the harness template (one argv token). Empty Payload
	// is still delivered (empty stdin + empty argv token if {payload} is present)
	// — unlike {model}/{settings}, empty does not drop a preceding flag.
	Payload      string
	AllowedTools string // {tools}: comma-separated tool grant
	Model        string // {model}: optional model override; "" drops the placeholder
	// Effort is {effort}: optional reasoning/thinking level (sty_657f77b9);
	// "" drops the placeholder AND a directly preceding flag (like Model).
	Effort string
	// Settings is {settings}: a pre-marshalled JSON object mirroring claude's
	// settings.local.json schema (env/model/permissions), already ${VAR}-resolved
	// by the caller (config.ResolveAgentEnvs / agentstep.buildRequest) — agentcli
	// never sees the raw map, only this JSON string, like Payload. "" drops the
	// placeholder AND a directly preceding flag, exactly like an empty Model, so a
	// binding with no settings emits no --settings arg. Passed INLINE (no
	// temp-file), so a secret it carries never touches disk. Values MAY be
	// secrets, so like Env it is never included in Command() evidence, logs, or
	// error strings.
	Settings string
	Dir      string // working directory for the subprocess
	// Env sets environment variables on the subprocess, layered onto the parent
	// env with these keys winning (composeEnv). Already ${VAR}-resolved by the
	// caller (config.ResolveAgentEnvs). Values MAY be secrets (an API token), so
	// Env is NEVER included in Command() evidence, logs, or error strings
	// (sty_001558ce).
	Env map[string]string
	// Unset names variables of the parent environment the subprocess must NOT
	// receive (composeEnv drops them). It is how a request keeps a credential the
	// launching process holds — a remote's private key — from an agent: Env can
	// only set, never remove (sty_0b1c72a9).
	Unset []string
	// Redact is every secret value satelle resolved into Env (sty_bd8d963c),
	// keyed by the reference it came from. The engine scrubs a run's output of
	// these values where that output enters satelle — the returned bytes, the
	// error, every Event field and every Sink byte — so no reader downstream
	// ever sees one. In memory only: never logged, serialised or recorded.
	Redact map[string]string `json:"-"`
	// Sink, when non-nil, receives a live TEE of the subprocess's stdout/stderr AS
	// IT RUNS (stderr lines prefixed "[stderr] "), so a caller can write it to a
	// tailable per-dispatch log an operator watches while a long dispatch runs —
	// the only way to see a hang or an approaching timeout before the SIGKILL
	// (sty_0aa67b7f). It never affects the accumulated bytes runProcess returns for
	// the final JSON/usage parse; nil is a no-op (the default, unchanged behaviour).
	// Sink.Write errors are ignored (best-effort, like the reviewer/executor logs).
	Sink io.Writer
	// OnEvent receives provider-neutral progressive events without changing the
	// authoritative stdout returned by Run.
	OnEvent EventHandler
	// HeartbeatInterval controls liveness events while the transport is silent.
	// Zero uses the production default; a negative value disables heartbeats.
	HeartbeatInterval time.Duration
	// LivenessInterval, when positive, makes the COMMAND transport sample the
	// child process tree's CPU time at that cadence and emit an EventProgress
	// each time it advances, so a one-shot CLI that prints nothing until exit
	// is distinguishable from a hung one (sty_db62a3b9). Zero disables the
	// probe. Stream and ACP ignore it.
	LivenessInterval time.Duration
	// Capture selects what an ACP runner returns from streamed session/update
	// chunks (sty_844b6ab1). Zero value is CaptureAnswer: keep only the final
	// agent_message run after tool_call fences, so narration before tools does
	// not contaminate persisted artifacts. CaptureFull returns every message
	// chunk concatenated (byte-identical to pre-fix capture) — used by the
	// verdict path so parseDecision still sees a decision emitted before
	// trailing chatter. Command-transport runners ignore this field entirely.
	Capture CaptureMode
	// ReadOnly marks a reviewer dispatch (sty_ef3efb51): the run may use only the
	// tools AllowedTools grants. The adapter then trims the tools the harness
	// offers where it can (claude --tools, --strict-mcp-config) and every live
	// transport denies a tool outside the grant by name, whatever permission mode
	// the harness reports. Zero value: an ordinary performer request, unchanged.
	ReadOnly bool
	// OnIsolation, when non-nil, receives each runtime isolation observation of a
	// ReadOnly ACP session (sty_2d5e583a): a peer that offers no ask mode, or a
	// tool that ran outside the grant without asking. It only reports — the run
	// continues and its verdict stands. It may be called from a reader goroutine.
	OnIsolation func(IsolationNote)
	// DeniedTools are tools the run must never use, in claude's rule syntax
	// (Bash(satelle story set:*)). The adapter translates them into the
	// harness's deny flag; an adapter that cannot express a deny refuses the
	// run (deny.go).
	DeniedTools []string
}

// CaptureMode selects which ACP agent_message_chunk text is returned as the
// agent's output (sty_844b6ab1). Command transport ignores CaptureMode.
type CaptureMode int

const (
	// CaptureAnswer (default / zero value): the last non-empty agent_message
	// segment, closed when a tool_call / tool_call_update intervenes. Drops
	// pre-tool narration that would otherwise contaminate step summaries and
	// other prose artifacts.
	CaptureAnswer CaptureMode = iota
	// CaptureFull: every agent_message_chunk concatenated in order — the
	// pre-sty_844b6ab1 behaviour. Required for ExpectVerdict so a decision
	// JSON that appears before trailing chatter is still parseable.
	CaptureFull
)

// UsageResult is the cost of one agent invocation — token counts and, when the
// caller times the run, its wall-clock duration. Populated from a machine-readable
// agent envelope (claude's `--output-format json`); a plain-text harness leaves
// Available false, so unreported usage is distinct from a reported zero. It
// never carries the env or any secret — only numbers.
//
// InputTokens is the full prompt the provider processed for this invocation,
// including cache-creation and cache-read tokens when the envelope reports them
// (sty_8178f1c6). Anthropic's three input fields are disjoint; we sum them so a
// reader of satelle story cost never mistakes the uncached remainder for the
// whole prompt. Rows recorded before that change omitted cache and understate
// input — they keep that meaning; no migration rewrites them.
type UsageResult struct {
	// Adapter names the harness that produced this usage (HarnessClaude,
	// HarnessGrok, HarnessPi), stamped by agentcli from the binding's
	// spawn so a ledger row names the adapter even when the usage is available.
	// Empty when the spawn is unrecognised (HarnessUnknown is never recorded).
	Adapter      string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	// FreshInputTokens/CacheCreationInputTokens/CacheReadInputTokens are the
	// disjoint components InputTokens sums (sty_363eaf55) — kept alongside the
	// total so a caller can tell whether a change cut fresh tokens or only cache
	// reads, which are priced differently. Zero on a transport/envelope that
	// reports no cache split (InputTokens still carries the uncached figure).
	FreshInputTokens         int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
	// CacheSplitAvailable is true only when the provider itself reported cache
	// tokens (under its own field names), so a zero split is a measurement.
	// False means the provider gave no cache fields and the three split fields
	// above are unreported, not zero (sty_c8d45201).
	CacheSplitAvailable bool
	Duration            time.Duration
	// UnavailableReason names the adapter and why usage is unavailable when
	// Available is false (sty_c8d45201).
	UnavailableReason string
	// Available distinguishes a transport-reported zero from usage that was not
	// reported at all.
	Available bool
	// ModelResolved is the canonical model id the transport actually ran
	// (sty_87b86044) — e.g. "claude-opus-5-5" for the configured alias "opus".
	// Empty when the transport did not report it (ACP, plain text, or a JSON
	// envelope without a modelUsage map); callers normalize that to
	// ModelUnavailable (or an adapter-named ModelUnavailableFor reason) before recording it, so a stored value is never empty.
	ModelResolved string
	// Models is every entry a transport's modelUsage map reported — several
	// when a background model ran beside the main one. Nil when unreported.
	Models []ModelUsage
	// CostUSD is the invocation's dollar cost, read from the provider's own
	// figure (a top-level total_cost_usd, grok's costUsdTicks, or the sum of
	// Models' own costUSD) — never derived from a token count and a rate
	// (sty_c4df7376). Nil means the provider reported no cost; CostUnavailableReason
	// then names the adapter and why.
	CostUSD *float64
	// CostUnavailableReason names the adapter and why CostUSD is nil. Set only
	// when CostUSD is nil.
	CostUnavailableReason string
	// Turns is the model turns the run reported (the envelope's num_turns), only
	// meaningful when TurnsAvailable. False means the run reported no count —
	// never a zero (sty_a7914904; satelle-agent-agnostic §2).
	Turns          int
	TurnsAvailable bool
}

// claudeJSONEnvelope is the shape of `claude -p --output-format json` output: the
// model's text lands in `result`, with token usage alongside. Only the fields we
// record are declared; extra fields are ignored.
//
// Cache fields (cache_creation_input_tokens, cache_read_input_tokens) are
// disjoint from input_tokens under Anthropic's usage shape; UnwrapUsage sums
// all three into UsageResult.InputTokens (sty_8178f1c6).
type claudeJSONEnvelope struct {
	Result string `json:"result"`
	Usage  *struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	// ModelUsage keys resolved-model usage by canonical model id (sty_87b86044),
	// e.g. {"claude-opus-5-5":{"inputTokens":2,"outputTokens":11,"costUSD":0.1}}.
	// Parsed lazily via parseModelUsage — kept raw here so an absent field
	// unmarshals as nil without needing a second pass.
	ModelUsage json.RawMessage `json:"modelUsage"`
	// TotalCostUSD is the envelope's own top-level dollar figure, when reported.
	// Absent on the captured claude_result.json fixture — cost then falls back
	// to the sum of ModelUsage's own costUSD (costFromModels).
	TotalCostUSD *float64 `json:"total_cost_usd"`
	// NumTurns is the run's own turn count, when the envelope carries one.
	NumTurns *int `json:"num_turns"`
	// Subtype is the result's kind: "success", or an "error_*" kind such as
	// error_max_turns, whose envelope carries usage and num_turns but no result.
	Subtype string `json:"subtype"`
}

// grokJSONEnvelope is the shape of `grok -p … --output-format json` headless
// output: the model's reply is in `text` (often a string that itself contains a
// satelle decision JSON). Without unwrapping, parseDecision cannot see a
// decision nested inside the escaped text field (sty_5cf4a1fb dogfood). Token
// usage is not consistently present on this envelope; when absent, Usage is zero.
type grokJSONEnvelope struct {
	Text string `json:"text"`
	// Usage is grok's snake_case (Anthropic-shaped) usage, mapped by claudeUsageFromMap
	// when present (sty_c8d45201); an envelope without them records unavailable.
	Usage      map[string]any  `json:"usage"`
	ModelUsage json.RawMessage `json:"modelUsage"`
	// TotalCostUSD is grok's own top-level dollar figure (matches
	// total_cost_usd_ticks * 1e-10 on the real capture); falls back to
	// costFromModels when absent.
	TotalCostUSD *float64 `json:"total_cost_usd"`
	NumTurns     *int     `json:"num_turns"`
}

// UnwrapUsage splits an agent's raw stdout into the INNER result text (what verdict
// parsing consumes, unchanged) and its token UsageResult. Recognized envelopes:
//
//   - Claude `--output-format json`: unwraps `.result` and captures `.usage`
//   - Grok `--output-format json`: unwraps `.text` (usage zero when absent)
//
// Otherwise stdout is returned verbatim with Available false — plain-text
// harnesses keep working and explicitly report unavailable cost. Duration is set by the
// caller (UnwrapUsage measures only tokens).
func UnwrapUsage(stdout []byte) ([]byte, UsageResult) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return stdout, UsageResult{}
	}
	// JSONL (grok streaming-json, claude stream-json): the
	// whole stdout is not one JSON document, so read usage off its event lines.
	// The text is returned verbatim, as before.
	if !json.Valid(trimmed) {
		if u, ok := jsonlUsage(trimmed); ok {
			return stdout, u
		}
		return stdout, UsageResult{}
	}
	var claude claudeJSONEnvelope
	// An error result (error_max_turns, …) has no `result` text but still reports
	// what the run used: its usage and turns are read, and its stdout is returned
	// verbatim so the caller can still see why it ended.
	if err := json.Unmarshal(trimmed, &claude); err == nil && (claude.Result != "" || strings.HasPrefix(claude.Subtype, "error")) {
		u := UsageResult{}
		if claude.Usage != nil {
			u.Available = true
			u.CacheSplitAvailable = true
			// Sum uncached + cache-creation + cache-read: the three fields are
			// disjoint components of one prompt (sty_8178f1c6).
			u.FreshInputTokens = claude.Usage.InputTokens
			u.CacheCreationInputTokens = claude.Usage.CacheCreationInputTokens
			u.CacheReadInputTokens = claude.Usage.CacheReadInputTokens
			u.InputTokens = claude.Usage.InputTokens +
				claude.Usage.CacheCreationInputTokens +
				claude.Usage.CacheReadInputTokens
			u.OutputTokens = claude.Usage.OutputTokens
			u.TotalTokens = u.InputTokens + u.OutputTokens
		}
		u.ModelResolved = noModelReport("claude command")
		if primary, models, ok := parseModelUsage(claude.ModelUsage); ok {
			u.ModelResolved = primary
			u.Models = models
		}
		applyCost(&u, claude.TotalCostUSD, "claude command")
		applyTurns(&u, claude.NumTurns)
		if claude.Result == "" {
			return stdout, u
		}
		return []byte(claude.Result), u
	}
	var grok grokJSONEnvelope
	if err := json.Unmarshal(trimmed, &grok); err == nil && (strings.TrimSpace(grok.Text) != "" || grok.Usage != nil || grok.NumTurns != nil) {
		// A grok envelope reports usage only when grok put a `usage` object on
		// it; otherwise record it unavailable with the adapter's reason rather
		// than a zero (sty_c8d45201), and the model explicitly unavailable
		// rather than a zero value for a caller to guess at (sty_87b86044).
		// A run cut off at its turn cap may leave `text` empty yet still report
		// usage and turns: those are kept and stdout comes back verbatim, as the
		// claude error branch does, so the caller can still see why it ended.
		text := []byte(grok.Text)
		if strings.TrimSpace(grok.Text) == "" {
			text = stdout
		}
		if grok.Usage != nil {
			u := claudeUsageFromMap(grok.Usage, "grok")
			u.ModelResolved = noModelReport("grok command")
			if primary, models, ok := parseModelUsage(grok.ModelUsage); ok {
				u.ModelResolved = primary
				u.Models = models
			}
			applyCost(u, grok.TotalCostUSD, "grok command")
			applyTurns(u, grok.NumTurns)
			return text, *u
		}
		u := unavailableUsage("grok", "--output-format json envelope carries no usage object")
		u.ModelResolved = noModelReport("grok command")
		applyCost(&u, grok.TotalCostUSD, "grok command")
		applyTurns(&u, grok.NumTurns)
		return text, u
	}
	return stdout, UsageResult{}
}

// Runner invokes an agent CLI headlessly and returns its stdout.
type Runner interface {
	// Name reports the agent CLI identifier (the template's binary).
	Name() string
	// Command reports the resolved command/harness template (binary + argv with the
	// {system}/{tools}/{model}/{settings}/{payload} placeholders intact) — a
	// concise, body-free description of HOW the agent is invoked, recorded as
	// invocation evidence (sty_fb3e0873). It never expands the rubric body or the
	// payload bytes (the literal {payload} token stays in the string when present).
	Command() string
	// Run executes the agent over req and returns its raw stdout.
	Run(ctx context.Context, req Request) ([]byte, error)
}

// UsageRunner is implemented by transports whose resolved-model usage cannot be
// recovered by sniffing Run's returned bytes for a JSON envelope (UnwrapUsage) —
// the stream transport's modelUsage arrives on a `result` event alongside the
// already-unwrapped decision text, never inside it. runOnce prefers RunUsage
// over Run+UnwrapUsage when a Runner implements this (sty_87b86044 AC2).
type UsageRunner interface {
	Runner
	// RunUsage behaves like Run but also returns the usage the transport
	// reported for the turn, including any resolved model(s).
	RunUsage(ctx context.Context, req Request) ([]byte, UsageResult, error)
}

// NewRunner returns the Runner for a bare CLI NAME — the preset. An empty name
// defaults to claude; "grok" expands to the grok command preset. NewRunner cannot
// return an ACP runner (no interface arg); see help agent-dispatch. Unknown
// names error. Callers with a full command template use RunnerFromCommand.
func NewRunner(name string) (Runner, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", CLIClaude:
		return templateFromCommand(DefaultClaudeCommand), nil
	case CLIGrok:
		return templateFromCommand(DefaultGrokCommand), nil
	default:
		return nil, fmt.Errorf("agentcli: unknown agent cli %q (want %q or %q, or a full command template)", name, CLIClaude, CLIGrok)
	}
}

// Interface names for RunnerFromBinding (mirror config.Interface* without importing
// config — agentcli must not depend on the config package).
const (
	InterfaceCommand = "command"
	InterfaceACP     = "acp"
	InterfaceStream  = "stream"
	InterfacePiRPC   = "pi-rpc"
)

// RunnerFromBinding resolves an agents.toml transport + command to a Runner
// (epic:agent-dispatch-transport). iface is "command" (default when empty),
// "acp", or "stream".
//
//   - command / empty: same as RunnerFromCommand (full argv template; in-loop → nil).
//   - acp: spawn line only (no {system}/{payload} substitution on argv); ACP session
//     protocol carries system/payload. Does not silently fall back to command.
//   - stream: Claude-style stream-json spawn; {system}/{payload} rejected (they
//     ride the first user message) while {tools}/{model}/{effort} remain argv.
//   - pi-rpc: a pi command run in pi's RPC mode (pi_rpc.go); the payload is
//     the prompt command, and a template that would not start cold is refused.
func RunnerFromBinding(iface, command string) (Runner, error) {
	switch strings.ToLower(strings.TrimSpace(iface)) {
	case "", InterfaceCommand:
		return RunnerFromCommand(command)
	case InterfaceACP:
		return newACPRunner(command)
	case InterfaceStream:
		return newStreamRunner(command)
	case InterfacePiRPC:
		return newPiRPCRunner(command)
	default:
		return nil, fmt.Errorf("agentcli: unknown interface %q (want %q, %q, %q, or %q)", iface, InterfaceCommand, InterfaceACP, InterfaceStream, InterfacePiRPC)
	}
}

// ErrNotLiveCapable is returned by OpenerFromBinding for command / in-loop /
// empty bindings that cannot open a Session (sty_1de7494c).
var ErrNotLiveCapable = errors.New("agentcli: binding is not live-capable (want interface=acp or stream)")

// OpenerFromBinding resolves an agents.toml transport + command to a
// SessionOpener. acp and stream return the existing Open methods; command,
// empty, and in-loop return ErrNotLiveCapable so the caller can fall back to
// today's hook channel rather than guess.
func OpenerFromBinding(iface, command string) (SessionOpener, error) {
	switch strings.ToLower(strings.TrimSpace(iface)) {
	case InterfaceACP:
		r, err := newACPRunner(command)
		if err != nil {
			return nil, err
		}
		return r.(acpRunner).Open, nil
	case InterfaceStream:
		r, err := newStreamRunner(command)
		if err != nil {
			return nil, err
		}
		return r.(streamRunner).Open, nil
	case "", InterfaceCommand:
		return nil, ErrNotLiveCapable
	case InterfacePiRPC:
		return nil, errPiRPCNotLive
	default:
		return nil, fmt.Errorf("agentcli: unknown interface %q (want %q, %q, or %q)", iface, InterfaceCommand, InterfaceACP, InterfaceStream)
	}
}

// RunnerFromCommand resolves an agents-layer command binding to a Runner. An empty
// or "in-loop" command returns (nil, nil): no agent-CLI runner, so the caller keeps
// its configured default. A SINGLE non-in-loop token is rejected (bare CLI presets
// removed — write a full command template, or run satelle init to migrate). A
// MULTI-token command is a literal command template: the first token is the
// binary, the rest the argv template.
func RunnerFromCommand(command string) (Runner, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 || strings.ToLower(fields[0]) == "in-loop" {
		return nil, nil
	}
	if len(fields) == 1 {
		return nil, fmt.Errorf("agentcli: bare CLI preset %q removed — write a full command template (see agentcli.DefaultClaudeCommand) or run satelle init to migrate", fields[0])
	}
	return templateRunner{binary: fields[0], argTemplate: fields[1:]}, nil
}

// Detect returns the first supported agent CLI found on PATH (claude preferred),
// or "" when none is installed. Used by the install-time selection.
func Detect() string {
	for _, c := range []string{CLIClaude, CLIGrok} {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return ""
}

// Available reports whether the named CLI's binary is on PATH. Known CLI names
// (claude/grok) are looked up directly — availability does not route
// through the agents.toml preset resolver (which no longer accepts bare tokens).
func Available(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case CLIClaude, CLIGrok:
		_, err := exec.LookPath(n)
		return err == nil
	default:
		return false
	}
}

// templateRunner executes a command template: a binary plus an argv template whose
// tokens may carry {system}/{tools}/{model}/{settings}/{payload} placeholders. It
// is the single code path for every agent CLI — claude, grok, or any
// operator-supplied binary.
type templateRunner struct {
	binary      string
	argTemplate []string
}

// templateFromCommand parses a full command string into a templateRunner.
func templateFromCommand(command string) templateRunner {
	fields := strings.Fields(command)
	return templateRunner{binary: fields[0], argTemplate: fields[1:]}
}

func (t templateRunner) Name() string { return t.binary }

// Command joins the binary and argv template (placeholders intact) into the
// payload-free harness string recorded as invocation evidence.
func (t templateRunner) Command() string {
	return strings.Join(append([]string{t.binary}, t.argTemplate...), " ")
}

func (t templateRunner) Run(ctx context.Context, req Request) ([]byte, error) {
	if err := checkDeny(t.binary, t.argTemplate, InterfaceCommand, req); err != nil {
		return nil, err
	}
	return runProcess(ctx, t.binary, reviewerArgs(t.binary, buildArgs(t.argTemplate, req), req), req)
}

// piRunClock reads the pi run window's start and end in RunUsage. Tests replace it
// to place the window exactly; a test that does must not run in parallel.
var piRunClock = time.Now

// RunUsage implements UsageRunner for the command transport. Every adapter but pi
// reports usage on stdout, so it is what runOnce's Run+UnwrapUsage fallback
// returned. pi prints plain text and keeps its usage in its own session record, so
// that run is attributed from the record (piRunUsage), or recorded as a pi-named
// unavailable — never the generic "transport reported no usage" (sty_58a9bdc8).
func (t templateRunner) RunUsage(ctx context.Context, req Request) ([]byte, UsageResult, error) {
	if adapterOf(t.binary, t.argTemplate) != HarnessPi {
		out, err := t.Run(ctx, req)
		if err != nil {
			return out, UsageResult{}, err
		}
		text, usage := UnwrapUsage(out)
		usage.stampAdapter(adapterOf(t.binary, t.argTemplate))
		return text, usage, nil
	}
	start := piRunClock()
	out, err := t.Run(ctx, req)
	return out, piRunUsage(req.Dir, start, piRunClock()), err
}

// stampAdapter records the harness behind a spawn on its usage. An unrecognised
// spawn stays empty rather than naming a guess ([[satelle-agent-agnostic]] §3).
func (u *UsageResult) stampAdapter(adapter string) {
	if adapter != HarnessUnknown {
		u.Adapter = adapter
	}
}

// buildArgs substitutes the placeholders in an argv template against req.
//
// Exact-token form: each of {system}/{tools}/{model}/{effort}/{settings}/{payload}
// as its own argv element becomes one argument (multi-word values stay one token).
// Empty {model}, {effort}, or {settings} drops the placeholder AND a directly
// preceding flag token (e.g. "--model {model}"), so the default template can
// carry the flag without emitting an empty value.
//
// Fused form (sty_aa726901): a token that *contains* (but is not equal to)
// {model}, {effort}, or {settings} is expanded in place when the value is set
// (e.g. model_reasoning_effort={effort} → model_reasoning_effort=high). When the
// value is empty, the whole token is dropped AND a directly preceding -flag is
// dropped (so `-c model_reasoning_effort={effort}` vanishes when effort is unset).
// {system}, {tools}, and {payload} are exact-token-only — fused use is not
// substituted (they carry whole multi-line values; empty payload must not drop a flag).
//
// {payload} never drops a preceding flag when empty: an empty payload is a
// deliberate empty user message (sty_5cf4a1fb). Payload is also always written to
// stdin by runProcess — dual delivery — whether or not {payload} appears here.
func buildArgs(argTemplate []string, req Request) []string {
	args := make([]string, 0, len(argTemplate))
	for _, tok := range argTemplate {
		switch tok {
		case "{system}":
			args = append(args, req.SystemPrompt)
		case "{tools}":
			args = append(args, req.AllowedTools)
		case "{payload}":
			// Always one token, even when empty — do not drop a preceding flag.
			args = append(args, req.Payload)
		case "{model}":
			if strings.TrimSpace(req.Model) == "" {
				if n := len(args); n > 0 && strings.HasPrefix(args[n-1], "-") {
					args = args[:n-1] // drop the now-valueless flag
				}
				continue
			}
			args = append(args, req.Model)
		case "{effort}":
			if strings.TrimSpace(req.Effort) == "" {
				if n := len(args); n > 0 && strings.HasPrefix(args[n-1], "-") {
					args = args[:n-1]
				}
				continue
			}
			args = append(args, req.Effort)
		case "{settings}":
			if strings.TrimSpace(req.Settings) == "" {
				if n := len(args); n > 0 && strings.HasPrefix(args[n-1], "-") {
					args = args[:n-1] // drop the now-valueless flag
				}
				continue
			}
			args = append(args, req.Settings)
		default:
			// Fused {model}/{effort}/{settings} (sty_aa726901) — e.g.
			// -c model_reasoning_effort={effort}. Exact-token cases handled above.
			if sub, ok, empty := fusedPlaceholder(tok, req); ok {
				if empty {
					if n := len(args); n > 0 && strings.HasPrefix(args[n-1], "-") {
						args = args[:n-1]
					}
					continue
				}
				args = append(args, sub)
				continue
			}
			args = append(args, tok)
		}
	}
	return args
}

// fusedPlaceholder expands a token that embeds {model}, {effort}, or {settings}
// as a substring. ok is true when a fused placeholder was present. empty is true
// when the substituted value is blank (caller drops the token and prior flag).
// {system}/{tools}/{payload} are never fused.
func fusedPlaceholder(tok string, req Request) (sub string, ok bool, empty bool) {
	type pair struct {
		ph, val string
	}
	for _, p := range []pair{
		{"{model}", strings.TrimSpace(req.Model)},
		{"{effort}", strings.TrimSpace(req.Effort)},
		{"{settings}", strings.TrimSpace(req.Settings)},
	} {
		if !strings.Contains(tok, p.ph) || tok == p.ph {
			continue
		}
		// Refuse fusing {system}/{tools}/{payload} even if mixed into the token.
		if strings.Contains(tok, "{system}") || strings.Contains(tok, "{tools}") || strings.Contains(tok, "{payload}") {
			return "", false, false
		}
		if p.val == "" {
			return "", true, true
		}
		return strings.ReplaceAll(tok, p.ph, p.val), true, false
	}
	return "", false, false
}

// composeEnv layers overlay onto base ("KEY=VALUE" entries, as from os.Environ),
// with overlay keys WINNING: any base entry whose key is in overlay is dropped,
// then overlay's pairs are appended in sorted-key order. The binding-wins
// guarantee and the deterministic order are OURS — not delegated to os/exec's
// duplicate-key behaviour — so it is unit-testable without an exec. An empty
// overlay returns base unchanged (sty_001558ce). Names in unset are dropped from
// base and, being absent from the result, are not set in the child at all — the
// one way to keep a variable of the parent (a remote's private key) from an agent.
func composeEnv(base []string, overlay map[string]string, unset ...string) []string {
	if len(overlay) == 0 && len(unset) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overlay))
	for _, kv := range base {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if _, shadowed := overlay[key]; !shadowed && !slices.Contains(unset, key) {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(overlay))
	for k := range overlay {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+overlay[k])
	}
	return out
}

// The dispatched subprocess inherits the launching shell's environment UNCHANGED,
// with only the binding's agents.toml `env` overlay added (composeEnv, overlay
// wins). There is NO secret filtering here — no denylist, no allowlist — on
// purpose. The env-leak bug was "found and fixed" twice (an ANTHROPIC_* denylist,
// then a PATH/HOME allowlist); each fix was satelle secretly deciding which parent
// vars reach the child, so the next new var re-triggered another fix. The filter
// was the bug. The separation auth needs does not require a filter: the subprocess
// runs with cwd = the repo root (agentstep sets Request.Dir = repoRoot), so
// `claude -p` reads .claude/settings(.local).json, whose `env` block OVERRIDES any
// inherited ANTHROPIC_* — verified: a bogus ANTHROPIC_* in the launching env is
// ignored when claude runs in the repo dir (and CLAUDE_CODE_*/AI_AGENT markers do
// not affect claude's auth). A binding that wants a different provider (e.g.
// [retrospective] on the GLM endpoint) sets it explicitly in its agents.toml `env`;
// that overlay is the ONLY env satelle contributes beyond the inherited shell.

// runProcess runs binary with args, feeding req.Payload on stdin in req.Dir, and
// returns the accumulated stdout. It streams via StdoutPipe/StderrPipe rather than
// buffering with cmd.Output(), so a non-nil req.Sink observes both streams
// INCREMENTALLY, before the process exits — a hang or an approaching timeout is
// visible on the sink, and a SIGKILL (ctx deadline) leaves whatever was already
// streamed on disk (sty_0aa67b7f). The returned bytes are the exact accumulated
// stdout regardless of req.Sink, so the caller's final JSON/usage parse is
// unaffected. A non-zero exit carries failureDetail in the error: the CLI's own
// error from stdout and the stderr, scrubbed and bounded.
func runProcess(ctx context.Context, binary string, args []string, req Request) ([]byte, error) {
	onEvent, stopEvents := eventStream(req)
	defer stopEvents()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader(req.Payload)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	cmd.Env = composeEnv(os.Environ(), req.Env, req.Unset...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("agentcli: %s: %w", binary, err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("agentcli: %s: %w", binary, err)
	}
	if err := cmd.Start(); err != nil {
		ev := newEvent(EventFailed)
		ev.Error = err.Error()
		emitEvent(onEvent, ev)
		return nil, fmt.Errorf("agentcli: %s: %w", binary, err)
	}
	emitEvent(onEvent, newStartEvent(cmd.Process.Pid))

	var stdout, stderr bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	adapter := commandAdapter{}
	go func() { defer wg.Done(); teeEventLines(stdoutPipe, &stdout, req.Sink, "", false, adapter, onEvent) }()
	go func() {
		defer wg.Done()
		teeEventLines(stderrPipe, &stderr, req.Sink, "[stderr] ", true, adapter, onEvent)
	}()
	stopProbe := startLivenessProbe(cmd.Process.Pid, req.LivenessInterval, onEvent)
	wg.Wait()
	stopProbe()

	if err := cmd.Wait(); err != nil {
		detail := failureDetail(stdout.Bytes(), stderr.String(), req.Redact)
		ev := newEvent(EventFailed)
		if ctx.Err() != nil {
			ev.Error = ctx.Err().Error()
		} else {
			ev.Error = err.Error()
		}
		if detail != "" {
			ev.Error += ": " + detail
		}
		emitEvent(onEvent, ev)
		if detail != "" {
			return stdout.Bytes(), fmt.Errorf("agentcli: %s: %w: %s", binary, err, detail)
		}
		return stdout.Bytes(), fmt.Errorf("agentcli: %s: %w", binary, err)
	}
	emitEvent(onEvent, newEvent(EventCompleted))
	return stdout.Bytes(), nil
}

// failureDetailCap bounds each part of a failed run's error text (sty_4bf6adbe).
const failureDetailCap = 2048

// failureScanCap bounds how much of stdout is scanned for the CLI's own error, so
// a long stream-json run costs no more than its tail.
const failureScanCap = 64 << 10

// failureDetail is the text a non-zero exit carries in its error (sty_4bf6adbe):
// the CLI's own error, then the stderr. A CLI that reports its failure as JSON on
// stdout (`claude -p --output-format json` writes {"is_error":true,"result":...}
// and leaves stderr empty) is read from the last such object; otherwise the
// stdout stands in. Every secret value in redact, then every secret-shaped
// pattern, is scrubbed from each part BEFORE it is clipped to 2048 bytes, so a
// secret cut by the clip cannot survive as a fragment.
func failureDetail(stdout []byte, stderr string, redact map[string]string) string {
	if len(stdout) > failureScanCap {
		stdout = stdout[len(stdout)-failureScanCap:]
	}
	clean := func(s string) string {
		return clipBytes(RedactSecrets(redactValues(s, redact)), failureDetailCap)
	}
	var parts []string
	if msg := stdoutFailure(stdout); msg != "" {
		parts = append(parts, clean(msg))
	}
	if msg := strings.TrimSpace(stderr); msg != "" {
		parts = append(parts, "stderr: "+clean(msg))
	}
	return strings.Join(parts, "; ")
}

// stdoutFailure is the CLI's own error on stdout: the result (else error) of the
// last JSON line that is an error, else the trimmed stdout.
func stdoutFailure(stdout []byte) string {
	lines := bytes.Split(stdout, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var v struct {
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
			Error   any    `json:"error"`
		}
		if json.Unmarshal(line, &v) != nil {
			continue
		}
		errText := ""
		switch e := v.Error.(type) {
		case nil:
		case string:
			errText = e
		default:
			if b, err := json.Marshal(e); err == nil {
				errText = string(b)
			}
		}
		if !v.IsError && errText == "" {
			continue
		}
		if strings.TrimSpace(v.Result) != "" {
			return strings.TrimSpace(v.Result)
		}
		if errText != "" {
			return errText
		}
		return string(line)
	}
	return strings.TrimSpace(string(stdout))
}

// clipBytes caps s at max bytes, keeping its head and its tail (half each, cut on
// rune boundaries) around an ellipsis: a CLI usually leads with its reason and
// ends with the line that failed, and a pure tail would drop the reason (cursor's
// "Cannot use this model: …" precedes kilobytes of model names).
func clipBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2
	head, tail := s[:half], s[len(s)-half:]
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return head + "…" + tail
}

// redactValues replaces every non-empty value in redact with [REDACTED], longest
// first so a secret that contains another is scrubbed whole.
func redactValues(s string, redact map[string]string) string {
	vals := make([]string, 0, len(redact))
	for _, v := range redact {
		if v != "" {
			vals = append(vals, v)
		}
	}
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, v := range vals {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	return s
}

const defaultHeartbeatInterval = 15 * time.Second

// startLivenessProbe samples pid's process-tree CPU time every interval and
// emits an EventProgress whenever it advanced. It is transport mechanism, not
// a provider adapter: every command binding gets it. interval<=0 or a nil
// handler disables it; where the platform cannot read CPU time it emits one
// explicit "unavailable" failure-free note and stops (behaviour stays strict).
func startLivenessProbe(pid int, interval time.Duration, onEvent EventHandler) func() {
	if interval <= 0 || onEvent == nil {
		return func() {}
	}
	last, ok := procTreeCPU(pid)
	if !ok && runtime.GOOS != "linux" {
		ev := newEvent(EventProgress)
		ev.Text = "liveness probe unavailable on " + runtime.GOOS
		emitEvent(onEvent, ev)
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if cur, ok := procTreeCPU(pid); ok && cur > last {
					last = cur
					emitEvent(onEvent, newEvent(EventProgress))
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); <-stopped }) }
}

func eventStream(req Request) (EventHandler, func()) {
	if req.OnEvent == nil || req.HeartbeatInterval < 0 {
		return req.OnEvent, func() {}
	}
	interval := req.HeartbeatInterval
	if interval == 0 {
		interval = defaultHeartbeatInterval
	}
	var mu sync.Mutex
	lastActivity := time.Now()
	handler := func(ev Event) {
		if ev.Kind != EventHeartbeat && ev.Kind != EventProgress {
			mu.Lock()
			lastActivity = time.Now()
			mu.Unlock()
		}
		emitEvent(req.OnEvent, ev)
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mu.Lock()
				silent := time.Since(lastActivity) >= interval
				mu.Unlock()
				if silent {
					emitEvent(req.OnEvent, newEvent(EventHeartbeat))
				}
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return handler, func() { once.Do(func() { close(done) }) }
}

// teeLines reads r line-by-line until EOF (the pipe closing, whether from a clean
// exit or a SIGKILL), accumulating each RAW line into buf — the exact bytes a
// non-streaming read would have produced — and, when sink is non-nil, writing an
// immediate copy (with prefix, for stderr) to sink so a tailing observer sees it
// before the process finishes. Reading line-by-line rather than with io.Copy keeps
// stderr's "[stderr] " prefix aligned to real lines without a token-length limit
// (unlike bufio.Scanner). Sink write errors are ignored (best-effort).
func teeLines(r io.Reader, buf *bytes.Buffer, sink io.Writer, prefix string) {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			buf.Write(line)
			if sink != nil {
				if prefix != "" {
					_, _ = sink.Write([]byte(prefix))
				}
				_, _ = sink.Write(line)
			}
		}
		if err != nil {
			return
		}
	}
}

func teeEventLines(r io.Reader, buf *bytes.Buffer, sink io.Writer, prefix string, stderr bool, adapter commandAdapter, onEvent EventHandler) {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			buf.Write(line)
			if sink != nil && !isHiddenReasoningLine(bytes.TrimSpace(line)) {
				_, _ = sink.Write([]byte(prefix + RedactSecrets(string(line))))
			}
			for _, ev := range adapter.Adapt(line, stderr) {
				emitEvent(onEvent, ev)
			}
		}
		if err != nil {
			return
		}
	}
}
