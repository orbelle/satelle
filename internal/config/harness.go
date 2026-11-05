// Per-harness SessionStart injection limit (sty_ce1a2733). Each in-loop harness
// delivers only so much hook additionalContext inline; the limit is a fact about
// that harness, so it is configuration — an embedded default
// (substrate/config/harness.toml) a repo overrides in satelle.toml — never a
// compiled constant tied to one provider.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// HarnessConfig is one [harness.<name>] table.
//
//	[harness.claude]
//	context_limit_bytes = 9500
type HarnessConfig struct {
	// ContextLimitBytes is the SessionStart additionalContext budget for the
	// harness; zero or negative means unset (fall through to the default).
	ContextLimitBytes int `toml:"context_limit_bytes"`
	// SessionContextEvent is the hook event whose additionalContext delivers
	// the session principle set. Empty means unset — no unknown-table fallback
	// and no provider fallback.
	SessionContextEvent string `toml:"session_context_event"`
	// ToolContextLimitChars is the character (rune) clip for a non-SessionStart
	// session-context event. Zero means unset (no tool-event budget).
	ToolContextLimitChars int `toml:"tool_context_limit_chars"`
	// GateWiring lists the tree-relative paths this harness needs present in a
	// tree for its edit and commit gates to govern a performer run there
	// (sty_f141c77f). Empty means none declared, which a dispatch treats as
	// absent wiring.
	GateWiring []string `toml:"gate_wiring"`
}

const (
	harnessFile = "substrate/config/harness.toml"
	// unknownHarness names the fallback table for a harness with no entry of its
	// own. It is the adapter-neutral token, not any provider.
	unknownHarness = "unknown"
	// lastResortContextLimit applies only when neither the repo config nor the
	// embedded defaults yield a value (an unreadable embed). Neutral, and the
	// tightest safe budget.
	lastResortContextLimit = 8192
)

var (
	embeddedHarnessOnce sync.Once
	embeddedHarness     map[string]HarnessConfig
	embeddedHarnessErr  error
)

// EmbeddedHarness returns the embedded default [harness.*] tables. Parsed once;
// empty on parse failure (EmbeddedHarnessErr reports it, tests assert nil).
func EmbeddedHarness() map[string]HarnessConfig {
	embeddedHarnessOnce.Do(func() {
		raw := []byte(embeddedHarnessTOML)
		var file struct {
			Harness map[string]HarnessConfig `toml:"harness"`
		}
		if _, err := toml.Decode(string(raw), &file); err != nil {
			embeddedHarnessErr = err
			return
		}
		embeddedHarness = file.Harness
	})
	out := make(map[string]HarnessConfig, len(embeddedHarness))
	for k, v := range embeddedHarness {
		out[k] = v
	}
	return out
}

// EmbeddedHarnessErr is non-nil when the embedded harness defaults failed to
// load or parse. Exposed for tests.
func EmbeddedHarnessErr() error {
	_ = EmbeddedHarness()
	return embeddedHarnessErr
}

// ContextLimit returns the SessionStart injection budget (bytes) for harness.
// Resolution: the repo's [harness.<name>], the embedded [harness.<name>], the
// repo's [harness.unknown], the embedded [harness.unknown], then a neutral
// last-resort value. An unrecognised name never resolves to another provider.
func (c Config) ContextLimit(harness string) int {
	name := strings.ToLower(strings.TrimSpace(harness))
	if name == "" {
		name = unknownHarness
	}
	tables := []map[string]HarnessConfig{c.Harness, EmbeddedHarness()}
	for _, key := range []string{name, unknownHarness} {
		for _, tbl := range tables {
			if v := tbl[key].ContextLimitBytes; v > 0 {
				return v
			}
		}
	}
	return lastResortContextLimit
}

// SessionContextEvent returns the hook event that delivers the session principle
// set for harness. Resolution is the repo's [harness.<name>] then the embedded
// [harness.<name>], and "" when unset. It does not fall through to
// [harness.unknown] or to another provider — an absent event is unavailable,
// not a guess.
func (c Config) SessionContextEvent(harness string) string {
	name := strings.ToLower(strings.TrimSpace(harness))
	if name == "" {
		return ""
	}
	if v := strings.TrimSpace(c.Harness[name].SessionContextEvent); v != "" {
		return v
	}
	return strings.TrimSpace(EmbeddedHarness()[name].SessionContextEvent)
}

// ToolContextLimitChars returns the character clip for a non-SessionStart
// session-context event. Resolution matches SessionContextEvent (repo, then
// embedded, no unknown fallback). Zero means unset — no tool-event budget.
func (c Config) ToolContextLimitChars(harness string) int {
	name := strings.ToLower(strings.TrimSpace(harness))
	if name == "" {
		return 0
	}
	if v := c.Harness[name].ToolContextLimitChars; v > 0 {
		return v
	}
	return EmbeddedHarness()[name].ToolContextLimitChars
}

// GateWiring returns the tree-relative paths harness needs for its gates to
// govern a performer, or nil when none are declared. Resolution is the repo's
// [harness.<name>] then the embedded [harness.<name>]; it never falls through
// to [harness.unknown] or another provider — undeclared wiring is absent
// wiring, not a guess.
func (c Config) GateWiring(harness string) []string {
	name := strings.ToLower(strings.TrimSpace(harness))
	if name == "" {
		return nil
	}
	if v := c.Harness[name].GateWiring; len(v) > 0 {
		return append([]string(nil), v...)
	}
	return append([]string(nil), EmbeddedHarness()[name].GateWiring...)
}

// PresentGateWiring returns the GateWiring paths of harness that exist under
// root — the evidence that a session of that harness runs in that tree.
func (c Config) PresentGateWiring(root, harness string) []string {
	var out []string
	for _, p := range c.GateWiring(harness) {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// Harnesses is every harness with a [harness.<name>] table, embedded or the
// repo's, sorted, without the neutral unknown fallback (which is a budget, not a
// harness).
func (c Config) Harnesses() []string {
	set := map[string]bool{}
	for k := range EmbeddedHarness() {
		set[k] = true
	}
	for k := range c.Harness {
		set[k] = true
	}
	delete(set, unknownHarness)
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// BudgetHarnesses is Harnesses plus the unknown fallback, last — the set a
// caller budgeting session context resolves.
func (c Config) BudgetHarnesses() []string {
	return append(c.Harnesses(), unknownHarness)
}

// validateHarness refuses a malformed [harness.<name>] gate_wiring entry at
// load time, naming the table and key. It only reads.
func validateHarness(cfg Config, path string) error {
	for name, h := range cfg.Harness {
		for _, raw := range h.GateWiring {
			if _, why := relPathProblem(raw); why != "" {
				return fmt.Errorf("config: %s: [harness.%s] gate_wiring entry %q — %s", path, name, raw, why)
			}
		}
	}
	return nil
}

// undecodedGovernedKeys names the keys an overlay declares under [worktree] or
// [harness.<name>] that no field decodes: those two tables are strict, so a
// misspelled key is refused instead of silently ignored (sty_f141c77f). Other
// tables keep their existing lenient decode.
func undecodedGovernedKeys(md toml.MetaData, path string) error {
	for _, k := range md.Undecoded() {
		switch {
		case len(k) >= 2 && k[0] == "worktree":
			return fmt.Errorf("config: %s: unknown key [worktree] %s", path, strings.Join(k[1:], "."))
		case len(k) >= 3 && k[0] == "harness":
			return fmt.Errorf("config: %s: unknown key [harness.%s] %s", path, k[1], strings.Join(k[2:], "."))
		}
	}
	return nil
}

// MaxContextLimit returns the largest limit among the named harnesses (every
// configured one, plus the unknown fallback, when none are named) — the budget
// a resident set must fit for at least one harness to receive it inline.
func (c Config) MaxContextLimit(harnesses ...string) int {
	if len(harnesses) == 0 {
		harnesses = c.BudgetHarnesses()
	}
	high := 0
	for _, h := range harnesses {
		if v := c.ContextLimit(h); v > high {
			high = v
		}
	}
	return high
}

// embeddedHarnessTOML is the per-harness adapter facts (context limits, the
// session-context event, where the gate is wired), formerly an embedded file.
const embeddedHarnessTOML = `# Per-harness SessionStart injection limit (bytes) — how much additionalContext
# an in-loop harness delivers to the model INLINE. A repo overrides a value in
# satelle.toml under the same [harness.<name>] table. The renderer degrades to a
# compact index plus an explicit read instruction when the resident set does not
# fit (sty_ce1a2733), so a limit is a budget, never a truncation point.
#
# session_context_event is the hook event whose additionalContext delivers the
# session principle set. tool_context_limit_chars is that event's clip, counted
# in characters (runes), and only applies when the event is not SessionStart.
# context_limit_bytes stays the SessionStart byte budget. A harness with no
# session_context_event emits nothing when a hook event is present, and the
# diagnostic 'satelle hook context' (no event) still emits.
#
# The installed hook command is whatever session_context_event resolved to at
# the last 'satelle init' (or heal). Changing the key without re-running init
# leaves the old event in the hooks file.
#
# | harness | session_context_event | delivery |
# | claude  | SessionStart          | hook additionalContext (unchanged) |
# | pi      | SessionStart          | extension-consumed stdout on a synthetic SessionStart. Pi is not retargeted. |
# | cursor  | sessionStart          | hook output additional_context (cursor spells the event in camelCase) |
# | grok    | PostToolUse           | hookSpecificOutput envelope; dedicated matcher-less command on that event |
# | unknown | (none)                | unavailable; emit nothing when an event is present |
#
# claude  — Claude Code keeps only a ~2 KB preview of a hook's additionalContext
#           above roughly 10 KB and writes the rest to a file the agent is not
#           made to read (observed: "Output too large (15.5KB) … Preview (first
#           2KB)"). 9500 leaves headroom under that cliff.
# grok   — ignores SessionStart hook stdout entirely (its hooks guide: "For events
#           like SessionStart or Notification, stdout is ignored"), so the
#           SessionStart budget is moot. Its documented hook additionalContext
#           clip is 10,000 characters, on PreToolUse/PostToolUse/Stop only. See
#           the grok-context-limit attachment on sty_ce1a2733. 16384 is kept as
#           the SessionStart byte budget. The session set is delivered on
#           session_context_event, clipped to tool_context_limit_chars.
# pi     — the generated extension (.pi/extensions/satelle.ts) receives the hook's
#           additionalContext and appends it to the run's system prompt, so pi has
#           no preview cliff of its own to measure (sty_b3c7b37d). It takes the
#           same budget as unknown ON PURPOSE rather than inheriting claude's: a
#           number is a claim about the harness, and none has been made here.
# cursor — cursor-agent delivers a sessionStart hook's additional_context to the
#           model (print mode, testdata/cursor/9-ctx-print.out). No cursor limit
#           has been measured, so it takes the same budget as pi and unknown on
#           purpose: a number is a claim about the harness, and none has been
#           made. cursor sends the event as sessionStart, and the channel is
#           matched exactly, so the value here is cursor's own spelling.
#           Unavailable on cursor, recorded rather than approximated
#           (sty_7d098d50):
#             - stop: unavailable in print mode (no stop event is dispatched;
#               sty_383ff068 capture 4). An interactive stop can be answered with
#               a followup_message, not vetoed.
#             - prompt reminder (beforeSubmitPrompt): unavailable — print mode
#               does not dispatch it, and interactive context delivery is unproven.
# unknown — anything unrecognised gets the smallest safe budget. Nothing
#           unrecognised is assumed to be Claude, but the safe choice is the
#           tightest one.
#
# gate_wiring (sty_f141c77f) lists the tree-relative paths a harness needs in a
# tree for its edit and commit gates to govern a performer run there. A dispatch
# into a linked worktree is refused (or, under [worktree] absent_wiring =
# "fail-open", run visibly ungated) when one is missing. Besides these paths,
# each present wiring file is scanned for the hook wrapper script it calls, and
# that script must exist too. Both keys are read from the MAIN tree's
# configuration, so a worktree cannot exempt itself. A harness with no
# gate_wiring is treated as having absent wiring; 'unknown' declares none, so an
# unrecognised command is refused unless a repo declares [harness.unknown]
# gate_wiring itself.
[harness.claude]
context_limit_bytes = 9500
session_context_event = "SessionStart"
gate_wiring = [".claude/settings.json"]

[harness.grok]
context_limit_bytes = 16384
session_context_event = "PostToolUse"
tool_context_limit_chars = 10000
gate_wiring = [".grok/hooks/satelle.json"]

[harness.pi]
context_limit_bytes = 9500
session_context_event = "SessionStart"
gate_wiring = [".pi/extensions/satelle.ts"]

[harness.unknown]
context_limit_bytes = 9500
`
