package agentcli

import (
	"encoding/json"
	"strings"
)

// PreToolUse deny encodings (sty_be756616). Each harness reads a different JSON
// shape from a PreToolUse hook; the shapes live here, in the provider adapter,
// so neither the hook verb nor the installed satelle-hook.sh wrapper carries a
// second copy ([[satelle-agent-agnostic]] §1).

// ClaudePreToolUseDenyOut is Claude Code's PreToolUse deny shape (sty_5e4bc568).
// Claude's schema rejects top-level decision/reason; only hookSpecificOutput is
// valid. permissionDecisionReason is the model-visible deny channel. Exported so
// a caller can decode a deny it received (the cli tests do).
type ClaudePreToolUseDenyOut struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// grokPreToolUseDenyOut is Grok Build's PreToolUse deny shape: top-level
// decision + reason (sty_e4902c51 / sty_5e4bc568).
type grokPreToolUseDenyOut struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// hookDenyShape is one harness's deny encoding plus the shell glob the installed
// wrapper uses to recognise it in a verb's output.
type hookDenyShape struct {
	harness string
	match   string
	encode  func(reason string) any
}

// hookDenyShapes are the harnesses with their own deny shape, in the order the
// wrapper's case arms are generated. Every other harness — claude, pi and an
// unrecognised one — takes the claude shape (defaultHookDenyShape), so a deny
// stays effective for a harness this table does not name.
var hookDenyShapes = []hookDenyShape{
	{
		harness: HarnessGrok,
		match:   `*'"decision"'*'"deny"'*`,
		encode:  func(r string) any { return grokPreToolUseDenyOut{Decision: "deny", Reason: r} },
	},
}

var defaultHookDenyShape = hookDenyShape{
	match: `*'"permissionDecision"'*'"deny"'*`,
	encode: func(r string) any {
		var doc ClaudePreToolUseDenyOut
		doc.HookSpecificOutput.HookEventName = "PreToolUse"
		doc.HookSpecificOutput.PermissionDecision = "deny"
		doc.HookSpecificOutput.PermissionDecisionReason = r
		return doc
	},
}

func hookDenyShapeFor(harness string) hookDenyShape {
	for _, s := range hookDenyShapes {
		if s.harness == harness {
			return s
		}
	}
	return defaultHookDenyShape
}

// PreToolUseDeny returns the JSON bytes (no trailing newline) of a PreToolUse
// deny for harness carrying reason. An empty reason gets a placeholder so the
// deny always explains itself. Harnesses without their own shape get Claude's.
func PreToolUseDeny(harness, reason string) []byte {
	if strings.TrimSpace(reason) == "" {
		reason = "satelle: denied (no reason supplied)"
	}
	b, err := json.Marshal(hookDenyShapeFor(harness).encode(reason))
	if err != nil {
		// The shapes are plain string structs; Marshal cannot fail.
		panic("agentcli: marshal PreToolUse deny: " + err.Error())
	}
	return b
}

// PreToolUseDenyMatch returns the shell case-glob that recognises harness's
// PreToolUse deny in a hook verb's stdout. The wrapper passes a matching output
// through byte-for-byte.
func PreToolUseDenyMatch(harness string) string {
	return hookDenyShapeFor(harness).match
}

// warnShape is one harness's PreToolUse allow-with-message recognition glob for
// the wrapper. Only a harness whose HarnessFacts.PreToolUseWarn is available has
// one: every other harness's warning is recorded, never delivered or guessed at.
type warnShape struct {
	harness string
	match   string
}

var hookWarnShapes = []warnShape{
	{harness: HarnessClaude, match: `*'"additionalContext"'*`},
}

// PreToolUseWarn returns the JSON bytes (no trailing newline) of a PreToolUse
// allow-with-message for harness carrying message, or nil when the harness
// cannot deliver one (HarnessFacts.PreToolUseWarn). The command still runs; the
// answer carries no permission decision.
func PreToolUseWarn(harness, message string) []byte {
	if !FactsFor(harness).PreToolUseWarn.Available {
		return nil
	}
	return HookEnvelope("PreToolUse", "", message, "")
}

// PreToolUseWarnMatch returns the shell case-glob that recognises harness's
// PreToolUse warn in a hook verb's stdout, or "" when the harness has none.
func PreToolUseWarnMatch(harness string) string {
	for _, s := range hookWarnShapes {
		if s.harness == harness {
			return s.match
		}
	}
	return ""
}

// HookWarnHarnesses lists the harnesses with a PreToolUse warn shape (the
// wrapper generates one pass-through arm per entry).
func HookWarnHarnesses() []string {
	out := make([]string, 0, len(hookWarnShapes))
	for _, s := range hookWarnShapes {
		out = append(out, s.harness)
	}
	return out
}

// HookDenyHarnesses lists the harnesses that have their own deny shape (the
// wrapper generates one case arm per entry; everything else is the default arm).
func HookDenyHarnesses() []string {
	out := make([]string, 0, len(hookDenyShapes))
	for _, s := range hookDenyShapes {
		out = append(out, s.harness)
	}
	return out
}

// Session-context and Stop encodings (sty_7d098d50). A harness whose hooks speak
// their own shape for these answers lives here, beside the deny shapes; every
// other harness takes the Claude-shaped envelopes the cli builds.

// HookContextOut is the Claude Code hook output that injects advisory context:
// the one envelope every context-bearing event (SessionStart, UserPromptSubmit,
// PreToolUse allow-with-nudge) is answered with, and the one place the top-level
// systemMessage — the channel a harness shows the USER — is added to it. Exported
// so a caller can decode an answer it received (the cli tests do).
type HookContextOut struct {
	HookSpecificOutput struct {
		HookEventName      string `json:"hookEventName"`
		PermissionDecision string `json:"permissionDecision,omitempty"`
		AdditionalContext  string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
	// SystemMessage is shown to the user, not the model. Absent unless a notice
	// was given (SessionContextOutput).
	SystemMessage string `json:"systemMessage,omitempty"`
}

// HookEnvelope returns the JSON bytes (no trailing newline) of the Claude-shaped
// context answer for event. permissionDecision "" omits the field (SessionStart
// makes no permission decision; a PreToolUse allow-with-nudge sets "allow").
// notice "" omits systemMessage.
func HookEnvelope(event, permissionDecision, context, notice string) []byte {
	var doc HookContextOut
	doc.HookSpecificOutput.HookEventName = event
	doc.HookSpecificOutput.PermissionDecision = permissionDecision
	doc.HookSpecificOutput.AdditionalContext = context
	doc.SystemMessage = notice
	b, err := json.Marshal(doc)
	if err != nil {
		panic("agentcli: marshal hook envelope: " + err.Error())
	}
	return b
}

// SessionContextOutput returns the JSON bytes (no trailing newline) of a
// session-context answer: context for the model, and notice — a short line the
// user should also see — through the harness's own user-visible channel.
//
//   - claude: the hookSpecificOutput envelope for event plus a top-level
//     systemMessage.
//   - pi: the same document; the extension's session_start passes systemMessage
//     to notify().
//   - cursor: additional_context only. UNSUPPORTED for the user: no capture under
//     testdata/cursor shows a sessionStart user-facing field being displayed
//     (user_message is documented only on deny answers, 12-snake). Revisit with a
//     probe if cursor documents one.
//   - grok: context only. UNSUPPORTED: Grok Build documents no user-visible
//     SessionStart channel.
//
// For an unsupported harness the notice is kept in model context, once: a caller
// that already placed the line in context (the release line) is not duplicated.
// Callers pass text, never a field name. An unrecognised harness takes the
// claude shape, as every other hook answer does.
func SessionContextOutput(harness, event, context, notice string) []byte {
	switch harness {
	case HarnessGrok:
		return HookEnvelope(event, "", contextWithNotice(context, notice), "")
	default:
		return HookEnvelope(event, "", context, notice)
	}
}

// contextWithNotice keeps a notice the harness cannot show the user in the
// model's context, line by line, skipping each line the context already carries
// (a notice may join several advisories, each placed in context by its owner).
func contextWithNotice(context, notice string) string {
	var missing []string
	for _, line := range strings.Split(notice, "\n") {
		if strings.TrimSpace(line) != "" && !strings.Contains(context, line) {
			missing = append(missing, line)
		}
	}
	if len(missing) == 0 {
		return context
	}
	added := strings.Join(missing, "\n")
	if strings.TrimSpace(context) == "" {
		return added
	}
	return added + "\n\n" + context
}

// StopAllow is the Stop-hook allow-with-note payload: no decision field, so the
// stop proceeds, and systemMessage surfaces the note to the operator. It is
// deliberately NOT a block — the JSON on stdout must never read as a block on
// this path (sty_211d8419).
//
// Pending and Limited are the settle answer of a harness that cannot hold its
// stop (HarnessFacts.SettleNotifyOnly), and are absent from every other
// harness's output: Pending names gate runs still going, whose verdict the
// harness adapter asks for again; Limited names runs whose verdict was left
// undelivered because the run ends at settle.
type StopAllow struct {
	SystemMessage string   `json:"systemMessage"`
	Pending       []string `json:"pending,omitempty"`
	Limited       []string `json:"limited,omitempty"`
}

// StopAllowOutput returns the JSON bytes (no trailing newline) of allow, or nil
// for a harness whose Stop allow carries no output (SilentStopAllow).
func StopAllowOutput(harness string, allow StopAllow) []byte {
	b, err := json.Marshal(allow)
	if err != nil {
		panic("agentcli: marshal stop allow: " + err.Error())
	}
	return b
}

// defaultStopBlockOut is the top-level decision/reason Stop block Claude reads
// (sty_5e4bc568 AC6); best-effort for Grok.
type defaultStopBlockOut struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// StopOutput returns the JSON bytes (no trailing newline) answering a Stop event
// with reason: cursor re-prompts through followup_message, every other harness
// blocks through the top-level decision/reason shape. A non-block answer is nil:
// cursor treats any followup_message as a re-prompt, so its allow prints nothing
// (SilentStopAllow); the other harnesses' allow-with-note is built by the caller.
func StopOutput(harness string, block bool, reason string) []byte {
	if !block {
		return nil
	}
	var doc any = defaultStopBlockOut{Decision: "block", Reason: reason}
	b, err := json.Marshal(doc)
	if err != nil {
		panic("agentcli: marshal stop output: " + err.Error())
	}
	return b
}
