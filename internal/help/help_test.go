package help

import (
	"regexp"
	"strings"
	"testing"
)

// A remote's secrets are declared, pushed and read through satelle-server, so the
// CLI ships no topic that teaches a removed command (sty_9d85a9c3).
func TestNoHelpTopicTeachesTheRemoteControlPlane(t *testing.T) {
	if _, ok := Get("remote-secrets"); ok {
		t.Error("the remote-secrets topic still ships")
	}
	for _, top := range List() {
		// Built by concatenation so the guard itself is no hit for AC6's grep.
		for _, removed := range []string{"satelle" + " remote ", "satelle" + " secret "} {
			if strings.Contains(top.Body, removed) {
				t.Errorf("topic %q directs the reader to the removed command %q", top.Name, strings.TrimSpace(removed))
			}
		}
	}
}

// TestSessionTokenTopic (sty_6ed6318d AC4): minting, the env var, how the user is
// resolved, the token's reach, its lifetime and the snapshot-push exclusion are
// documented so `satelle help session-token` is the one reference for a
// non-interactive session.
func TestSessionTokenTopic(t *testing.T) {
	top, ok := Get("session-token")
	if !ok {
		t.Fatal("session-token topic not found")
	}
	for _, want := range []string{
		"SATELLE_TOKEN",
		"Account → Sessions",
		"POST /api/v1/me/tokens",
		"satelle whoami",
		"30 days",
		"revoke",
		"location registration",
		"workstate",
		"snapshot",
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("session-token topic missing %q", want)
		}
	}
}

func TestAgentDispatchTopic(t *testing.T) {
	top, ok := Get("agent-dispatch")
	if !ok {
		t.Fatal("agent-dispatch topic not found")
	}
	// The topic must teach the whole dispatch contract from deployed docs alone:
	// how the agent is briefed, how it PULLS context by id, the refusals, what
	// makes a step self-sufficient, and the entry-dispatch / exit-review rule.
	for _, want := range []string{
		"agents.toml",            // where the binding lives
		"skills:",                // the rubric requirement
		"inject_principles",      // the principle-injection toggle
		"refuse",                 // fail-loud on a missing binding / grant
		"Bash(satelle:*)",        // the grant a dispatched agent needs
		"satelle story get <id>", // the pull commands (must match the shipped prompt)
		"satelle ledger list --story <id>",
		"self-sufficient",                 // the sufficiency precondition
		"entry",                           // dispatch fires on entry
		"EXIT edge",                       // judge the exit edge
		"{story, from, to, review_skill}", // the stdin shape (pull model, not push)
		"[architect]",                     // the custom-agent worked example (binding)
		"agent: architect",                // the allocation
		".claude/agents",                  // the harness-agent-dir anti-pattern
		// Full-template requirement + placeholders (AC4, sty_6752e35b): bare
		// single-token presets are rejected; only in-loop remains as a bare token.
		// `satelle help agent-dispatch` teaches this without reading the source.
		`command = "in-loop"`,
		"full multi-token command template",
		"rejected",
		"{system}", "{tools}", "{model}", "{payload}",
		"deprecated alias", // harness→command rename is documented as back-compat
		// Dual transport (epic:agent-dispatch-transport): CLI control plane in;
		// command default + optional ACP out; Claude command-only; no MCP process API.
		`interface`,
		"command",
		"acp",
		"stream",
		"DefaultClaudeStreamCommand",
		"no `command` and no `profile=`",
		"driving session is the in-loop executor",
		"one-shot coder",
		"only live relay",
		"CLI verbs",
		"Claude",
		"MCP",
		"story status",
		"effort",    // sty_657f77b9
		"secondary", // sty_5bf61f89
		// The model list sits beside secondary=: the comma list, what moves it
		// on, and that secondary= applies once it is spent (sty_7bfa980d).
		"### Model list (`model = \"a,b\"`)",
		"ordered comma-separated list",
		"settles or exits with no final message",
		"`timeout` or\n`idle_timeout`",
		"rate-limit or unavailability error",
		"Once the list is spent, `secondary=` (below) applies unchanged",
		// Supported adapters + compliance scaffolds (sty_aa726901 / sty_941e60cb).
		"Supported adapters",
		"satelle agents install",
		"compliance",               // sty_9e86f407
		".grok/hooks/satelle.json", // sty_9e86f407
		"story is engaged",         // sty_9e86f407
		// Consultation sessions (sty_a0372443): chat is a generic consultation
		// mechanism — pick the binding, name the speaker, and a consulted
		// binding is told its reply is context, not a verdict.
		"consulting, not judging",
		"satelle-agent-consultation",
		// Rework relay (sty_8e0b29a0): the step key, the verb, the termination
		// contract with its literal marker, and the two permission policies.
		"Rework relay",
		"satelle story rework",
		"rework = { consult",
		"NOT READY: <the single most important thing still wrong>",
		"consumes a round",
		"never** sets status",
		`cc = "*"`,
		"denied by policy",
		// Converge then gate (sty_cec967b5): named section, READY contract,
		// blocked fallback living in the orchestrator skill.
		"Converge then gate",
		"READY",
		"blocked",
		"orchestrator skill",
		// Default live transport (epic:model-selection child 2, sty_119f6fda):
		// an unset interface= resolves from HOW the binding is used, and the
		// preference order between live transports is configuration.
		"live_interfaces",
		"one-shot",
		"live use",
		// Idle-stall detector supersedes the wall-clock cap (sty_752c4ef2):
		// idle_timeout is the config that bounds a dispatch, timeout is the
		// optional hard ceiling (unset by default), and a stall is its own
		// named outcome, distinct from a hard-timeout deadline.
		"idle_timeout",
		"no compiled wall-clock cap",
		"unset by default",
		"stalled",
		"agent-stalled",
		"heartbeat alone does not",
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("agent-dispatch topic missing %q", want)
		}
	}
	// story chat is not how a story is driven or how quality is raised
	// (sty_462603f0).
	for _, ban := range []string{"story chat", "Consultation sessions", "raise implementation quality"} {
		if strings.Contains(top.Body, ban) {
			t.Errorf("agent-dispatch topic must not contain %q", ban)
		}
	}
}

// TestAgentDispatchTopicDocumentsApplyChanges (sty_af0b1761 AC7): the contract of
// a read-only performer reporting changes is taught by the topic alone — the
// declaration, that it requires propose, the ops, the tag prefixes and the
// `changes` envelope, including on a reject line.
func TestAgentDispatchTopicDocumentsApplyChanges(t *testing.T) {
	top, ok := Get("agent-dispatch")
	if !ok {
		t.Fatal("agent-dispatch topic not found")
	}
	start := strings.Index(top.Body, "### Reporting configuration changes")
	end := strings.Index(top.Body, "### Planner transport evidence")
	if start < 0 || end < start {
		t.Fatal("agent-dispatch topic lost its apply_changes section")
	}
	section := top.Body[start:end]
	for _, want := range []string{
		"apply_changes = { ops = [",
		"tags = [\"order:\"",
		"It requires `propose = true`",
		"`add-tags`",
		"`create-child`",
		"prefixes a reported tag may carry",
		`"changes":[`,
		`{"op":"add-tags","story":`,
		`{"op":"create-child","title":`,
		"reject line",
		"**all or nothing**",
		"`story_updated`",
		"`story_created`",
		"performer:<step>",
		"declares no `apply_changes`",
		"`satelle story ready`",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("apply_changes section missing %q", want)
		}
	}
}

// TestAgentDispatchFindingsToAnswer (sty_d7e4f539 AC1): the topic teaches that a
// re-presented performer is handed each gate seat's latest findings by satelle
// itself (sty_f948d345) and answers each as adopted or rebutted.
func TestAgentDispatchFindingsToAnswer(t *testing.T) {
	top, ok := Get("agent-dispatch")
	if !ok {
		t.Fatal("agent-dispatch topic not found")
	}
	start := strings.Index(top.Body, "### Answering a re-presented edge's findings")
	end := strings.Index(top.Body, "### Planner transport evidence")
	if start < 0 || end < start {
		t.Fatal("agent-dispatch topic lost its findings_to_answer section")
	}
	section := strings.Join(strings.Fields(top.Body[start:end]), " ")
	for _, want := range []string{
		"`findings_to_answer`",
		"each gate seat's latest verdict",
		"`answer` is `adopted` or `rebutted`",
		"`finding_answers`",
		"The newest directed messages are kept",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("findings_to_answer section missing %q", want)
		}
	}
}

// TestNoManualFindingsRelay (sty_d7e4f539 AC2): since sty_f948d345 (release
// 0.0.720, commit 49cfc289) satelle delivers a re-presented edge's findings to the
// performer itself, so no embedded topic may tell an orchestrator to relay them by
// hand. The phrases are ones that only describe a manual relay; the automated
// `story rework` relay and the drive's own forwarding do not use them.
func TestNoManualFindingsRelay(t *testing.T) {
	banned := []string{
		"relay the reviewers' notes",
		"relay each seat",
		"relay them by hand",
		"Round-N findings answered",
		"receives reviewer findings only as messages",
	}
	for _, top := range List() {
		body := strings.ToLower(strings.Join(strings.Fields(top.Body), " "))
		for _, ban := range banned {
			if strings.Contains(body, strings.ToLower(ban)) {
				t.Errorf("help topic %q instructs a manual findings relay: %q", top.Name, ban)
			}
		}
	}
}

func TestAgentDispatchTopicModelSelection(t *testing.T) {
	top, ok := Get("agent-dispatch")
	if !ok {
		t.Fatal("agent-dispatch topic not found")
	}
	for _, want := range []string{
		"Model selection",
		"agents.toml",
		"model =",
		"--model",
		"cli-default",
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("agent-dispatch topic missing %q", want)
		}
	}
}

// TestAgentDispatchTopicMentionsScratch (sty_e7aaf8b1 AC7): the scratch
// directory, the file-free attach path, and the leftover-sweep config must be
// teachable from deployed docs alone.
func TestAgentDispatchTopicMentionsScratch(t *testing.T) {
	top, ok := Get("agent-dispatch")
	if !ok {
		t.Fatal("agent-dispatch topic not found")
	}
	for _, want := range []string{
		"SATELLE_SCRATCH",
		"TMPDIR",
		"--body",
		"dispatch.leftovers",
		"scratch_kept",
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("agent-dispatch topic missing %q", want)
		}
	}
}

// TestReworkMarkerContractStatedIdentically: the READY contract appears in the
// help topic AND in `satelle story rework --help`. A contract stated in two
// places is a contract that drifts, so both are pinned to the same literals —
// the CLI half is asserted in internal/cli (TestReworkCommandLongStatesTheMarkerContract).
func TestReworkMarkerContractStatedIdentically(t *testing.T) {
	top, ok := Get("agent-dispatch")
	if !ok {
		t.Fatal("agent-dispatch topic not found")
	}
	for _, want := range []string{"READY", "NOT READY:"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the marker %q must appear verbatim in the topic", want)
		}
	}
	if strings.Contains(top.Body, "Ready\n") || strings.Contains(top.Body, "ready marker is `ready`") {
		t.Errorf("the marker is case-sensitive; the topic must not show a lowercase form as the contract")
	}
}

// TestReviewerChecksTopic pins the lifecycle section: the authored form is a
// DERIVED ROUTE (sty_d953c5d8), the validate sentence and the done-gate note sit
// outside it, and the gates a step declares are named where they belong.
func TestReviewerChecksTopic(t *testing.T) {
	top, ok := Get("reviewer-checks")
	if !ok {
		t.Fatal("reviewer-checks topic not found")
	}
	for _, want := range []string{
		"satelle <noun> validate",
		"DETERMINISTIC",
		"The done gate is **not** mandated",
		"derived route",
		"gating ENTRY to it",
		"story proof",
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("reviewer-checks topic missing %q", want)
		}
	}
}

// TestWorkflowsTopic pins the binding-form section (sty_9882b8c6), restated for
// the TOML route grammar (sty_a69d7090): a gate belongs to the step it admits,
// and an always-on `[[gate]]` is the multi-step form.
func TestWorkflowsTopic(t *testing.T) {
	top, ok := Get("workflows")
	if !ok {
		t.Fatal("workflows topic not found")
	}
	for _, want := range []string{
		"Binding a reviewer: a step's `reviewers` vs an always-on `[[gate]]`",
		"The over-fire trap",
		"first-reject short-circuit",
		"List order = execution order",
		"Concurrency is the default",
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("workflows topic missing %q", want)
		}
	}
}

func TestProjectsTopic(t *testing.T) {
	top, ok := Get("projects")
	if !ok {
		t.Fatal("projects topic not found")
	}
	// The topic must teach the key agent rule: add another project with
	// `workspace add`, served additively under /<slug>/.
	for _, want := range []string{"workspace add", "/<slug>/", "service install", "~/.satelle/config.toml"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("projects topic body missing %q", want)
		}
	}
	// An operator who sees a stale UI must find the recovery here rather than
	// having to know that `workspace add` happens to re-seed (sty_e6e467fe).
	for _, want := range []string{"stale", "re-request", "satelle workspace add"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("projects topic must document the stale-mirror recovery: missing %q", want)
		}
	}
}

// TestEmbeddedHelpHasNoUnreachableReferences sweeps every shipped help topic
// for unreachable/repo-local authority (sty_a319db89 AC2 help half).
//
// Scope is help topics ONLY. Embedded substrate is owned by
// internal/config/substrate_conformance_test.go (dogfoodIDRe, dogfood-repo
// deferral, gitignored-path deferral) — do not re-scan that corpus from here.
//
// Bare "(sty_xxxxxxxx)" provenance annotations are allowed: agent-dispatch and
// others use them as citations, not as "see sty_… for the rule". Authority
// deferral (see/per/reason/decision … sty_) is banned.
func TestEmbeddedHelpHasNoUnreachableReferences(t *testing.T) {
	dogfoodRepo := regexp.MustCompile(`(?i)dogfood[ -]?repo`)
	// Authority deferral to a story id (not bare provenance parentheticals like
	// "… (sty_xxxxxxxx)." at end of a sentence, and not "per-binding").
	// Word-bounded verbs only — "per-binding" must not match \bper\b.
	styAuthority := regexp.MustCompile(`(?i)(\bsee\b|\bconsult\b|\brationale\b|full reason|\bdecision\b).{0,40}sty_[0-9a-f]{8}|\bper\s+sty_[0-9a-f]{8}`)
	// Deferral to a gitignored tree path as something the reader should open.
	gitignoredDeferral := regexp.MustCompile(`(?i)(see|consult|read|open|record in|decision).{0,60}\.satelle/(documents|stories)/`)
	for _, top := range List() {
		for i, line := range strings.Split(top.Body, "\n") {
			ln := i + 1
			if m := dogfoodRepo.FindString(line); m != "" {
				t.Errorf("%s:%d: dogfood-repo reference in shipped help (%q)", top.Name, ln, m)
			}
			if m := styAuthority.FindString(line); m != "" {
				t.Errorf("%s:%d: story id cited as authority for a product rule (%q)", top.Name, ln, m)
			}
			if m := gitignoredDeferral.FindString(line); m != "" {
				t.Errorf("%s:%d: defers to a gitignored path (%q)", top.Name, ln, m)
			}
		}
	}
}
