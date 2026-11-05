// harness_hooks.go — the ONE definition of each harness's satelle hook surface
// (sty_338a53f8). Three paths used to answer "what does a satelle scaffold look
// like for this harness" independently: the BUILDER that writes a fresh file,
// the HEALER that appends missing entries to an existing one, and the CHECKER
// that decides whether the result is complete. They could disagree, so all
// three now read this table.
//
// It lives in agentcli, beside harness_facts.go, so the layers below cli can ask
// which tools a harness's hooks actually observe (sty_23ceeca2: the skill-evidence
// check reports a declared item as unverifiable when no hook sees its tool).
package agentcli

// HookSpec is one harness's satelle hook surface.
//
// GateMatcher / CommitMatcher are the PreToolUse matchers: the tool-name
// alternation the scaffold writes, and therefore the only one the healer may
// expect. Events is the set of hook events a satelle scaffold for this harness
// actually installs — and therefore the set the completeness check may demand.
type HookSpec struct {
	GateMatcher   string
	CommitMatcher string
	Events        []string
	// TurnEndEvents are the events a harness fires when it ends a turn or its
	// session without a Stop hook (sty_7e4393fc): the scaffold wires `satelle
	// hook turnend` to each so a gate still owed to that session is resumed. Empty
	// for a harness that fires none.
	TurnEndEvents []string
	// NoToolMatcher is true for a harness whose hook file cannot filter a hook by
	// tool name (cursor's hooks.json entries carry no matcher): its gate and
	// commitgate fire for EVERY tool call, so the verbs classify the tool
	// in-process (ClassifyTool) and fail closed on one they cannot place.
	// GateMatcher / CommitMatcher are empty for such a harness.
	NoToolMatcher bool
}

// HasEvent reports whether this harness's scaffold installs the named event.
func (s HookSpec) HasEvent(event string) bool {
	for _, e := range s.Events {
		if e == event {
			return true
		}
	}
	return false
}

// FullHookEvents is the event set a harness gets when it supports all of them.
var FullHookEvents = []string{"SessionStart", "PreToolUse", "UserPromptSubmit", "Stop"}

// HarnessHooks returns the hook surface for a harness. An unknown harness gets
// the claude shape, which is what every caller already defaulted to.
func HarnessHooks(harness string) HookSpec {
	switch harness {
	case "grok":
		return HookSpec{
			GateMatcher:   "Edit|Write|MultiEdit|NotebookEdit|search_replace|write",
			CommitMatcher: "Bash|run_terminal_command",
			Events:        FullHookEvents,
		}
	case "pi":
		// pi's built-in tool ids are lower-case (sty_b3c7b37d); the extension
		// matches them anchored, so these are whole names, not substrings.
		return HookSpec{
			GateMatcher:   "edit|write",
			CommitMatcher: "bash",
			Events:        FullHookEvents,
		}
	default: // claude
		return HookSpec{
			GateMatcher:   "Edit|Write|MultiEdit|NotebookEdit",
			CommitMatcher: "Bash",
			Events:        FullHookEvents,
			TurnEndEvents: []string{"StopFailure", "SessionEnd"},
		}
	}
}
