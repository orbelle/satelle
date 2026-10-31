package help

import (
	"regexp"
	"strings"
	"testing"
)

func TestListContainsCoreTopics(t *testing.T) {
	names := map[string]bool{}
	for _, top := range List() {
		names[top.Name] = true
		if top.Title == "" {
			t.Errorf("topic %q has no title", top.Name)
		}
		if strings.TrimSpace(top.Body) == "" {
			t.Errorf("topic %q has empty body", top.Name)
		}
	}
	for _, want := range []string{"create-story", "reviewer-checks", "principles", "projects", "create-review", "agent-dispatch", "workflow-convert", "retrieve", "compact-output", "epic-wave", "session-token", "session"} {
		if !names[want] {
			t.Errorf("missing help topic %q", want)
		}
	}
}

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

// The trunk and worktree topics document the bring-level step for a trunk base
// (sty_92337a13): set-head auto, the unresolved stop and its message, the hint,
// --strict, --refuse and base_refuse with its default.
func TestTrunkAndWorktreeTopicsDocumentTheBringLevelStep(t *testing.T) {
	const unresolved = "satelle: trunk unresolved — cannot tell whether <ref> is the trunk; pass --trunk-branch <name> or run git remote set-head <remote> --auto"
	for name, wants := range map[string][]string{
		"worktree": {"brought level", "not the trunk", "set-head", unresolved, "--trunk-branch", "[trunk] branch", "base_refuse", "dirty, diverged, ahead, behind"},
		"trunk":    {"set-head", unresolved, "--trunk-branch", "[trunk] branch", "--strict", "--refuse", "base_refuse", "dirty, diverged, ahead, behind, offline, unresolved"},
	} {
		topic, ok := Get(name)
		if !ok {
			t.Fatalf("missing help topic %q", name)
		}
		for _, w := range wants {
			if !strings.Contains(topic.Body, w) {
				t.Errorf("topic %q lacks %q", name, w)
			}
		}
		// --branch names the new worktree's branch, never the trunk.
		for _, ln := range strings.Split(topic.Body, "\n") {
			if strings.Contains(ln, "pass --branch") || strings.Contains(ln, "pass `--branch`") {
				t.Errorf("topic %q tells the operator to pass --branch for the trunk: %q", name, ln)
			}
		}
	}
}

// TestCompactOutputTopic (sty_75b76691 AC5): the config keys, the flags, the
// table/diff fold shapes, and the marker/retrieve tie-in are all documented so
// `satelle help compact-output` is the single reference for what an agent sees
// when a pulled command renders compact.
func TestCompactOutputTopic(t *testing.T) {
	top, ok := Get("compact-output")
	if !ok {
		t.Fatal("compact-output topic not found")
	}
	for _, want := range []string{
		"compact_for_agents",
		"compact_commands",
		"noise_patterns",
		"SATELLE_SCRATCH",
		"CLAUDECODE=1",
		"--compact",
		"--json",
		"<<ccr:HASH,KIND,SIZE>>",
		"satelle retrieve <hash>",
		"... (repeated N times)",
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("compact-output topic missing %q", want)
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

// TestRetrieveTopic (AC4): the marker grammar, the verb, retention, and the
// reachability rules are all documented so `satelle help retrieve` can be the
// single reference a compressor author or a reviewing agent reaches for.
func TestRetrieveTopic(t *testing.T) {
	top, ok := Get("retrieve")
	if !ok {
		t.Fatal("retrieve topic not found")
	}
	for _, want := range []string{
		"<<ccr:HASH>>",
		"Retrieve more: satelle retrieve HASH",
		"retrieve.MarkerRE",
		"retrieve.FindHashes",
		"satelle retrieve <hash>",
		"retrieve_keep_days",
		"Bash(satelle:*)",
		"GrantAllowsMutators",
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("retrieve topic missing %q", want)
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

// TestAgentDispatchTopicModelSelection (sty_7069bced / epic:model-selection
// order:3 AC7): the precedence, the step/agent
// override syntax, and the cli-default fallback must all be teachable from
// deployed docs alone.
// TestAgentDispatchTopicListsEveryWarnedIsolationConfig (sty_2d5e583a AC5): the
// "Reviewer tool isolation" section lists every configuration that runs with a
// warning, its gap and its fix, states the breach rule and the attestation, and
// no longer claims a refusal.
func TestAgentDispatchTopicListsEveryWarnedIsolationConfig(t *testing.T) {
	top, ok := Get("agent-dispatch")
	if !ok {
		t.Fatal("agent-dispatch topic not found")
	}
	start := strings.Index(top.Body, "### Reviewer tool isolation")
	end := strings.Index(top.Body, "### What each harness reports")
	if start < 0 || end < start {
		t.Fatal("agent-dispatch topic lost its Reviewer tool isolation section")
	}
	section := top.Body[start:end]
	for _, want := range []string{
		"Warned, never blocked",
		"grok ACP",
		"no adapter recognises",
		"`--always-approve`",
		"`--dangerously-skip-permissions`",
		"no ask mode",
		"tools not held to the grant",
		"usage accounting not to standard",
		"scoped grant not enforced",
		"reviewer-isolation-breach",
		"never aborts",
		"isolation = \"operator-attested\"",
		"INFO",
		"WARN",
		"fix",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("Reviewer tool isolation section missing %q", want)
		}
	}
	for _, ban := range []string{"is refused", "refused before", "will refuse", "is not dispatched", "refusal stands"} {
		if strings.Contains(section, ban) {
			t.Errorf("Reviewer tool isolation section still claims a refusal: %q", ban)
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

// TestCreateReviewTopic asserts the worked example is complete enough to
// self-serve (sty_51ad783b): the full skill anatomy, the workflow binding, the
// opt-in framing, and how to confirm the wiring.
func TestCreateReviewTopic(t *testing.T) {
	top, ok := Get("create-review")
	if !ok {
		t.Fatal("create-review topic not found")
	}
	for _, want := range []string{
		"type: skill",                         // the rubric skill frontmatter
		`{"decision": "accept", "notes": ""}`, // the verdict contract
		"create_review: my-create-review",     // the workflow binding
		"gate_create = true",                  // the repo opt-in
		"workflow validate",                   // how a broken binding is surfaced
		"deterministic",                       // the degradation story (opt-in framing)
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("create-review topic missing %q", want)
		}
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

// TestHelpTopicsTeachTheTOMLRouteForm keeps the live-form operator topics on the
// form the parser actually accepts (sty_a69d7090). The markdown halves and their
// `## gate` sections are retired, so naming them as a CURRENT form teaches a
// route the binary cannot read. wants names the halves each topic must still
// talk about, so the guard cannot pass by a rewrite that simply stops mentioning
// the form.
//
// workflow-convert is deliberately absent: it is the md→toml mapping and MUST
// keep the retired spellings; TestWorkflowConvertTopicCoversTheMarkdownRouteSource
// pins them.
func TestHelpTopicsTeachTheTOMLRouteForm(t *testing.T) {
	topics := []struct {
		name  string
		wants []string
	}{
		{"workflows", []string{"done.toml", "step.toml", "[[gate]]"}},
		{"reviewer-checks", []string{"done.toml", "step.toml"}},
		{"agent-dispatch", []string{"step.toml"}},
		{"create-review", []string{"done.toml"}},
	}
	banned := []string{"done.md", "step.md", "## gate"}
	for _, topic := range topics {
		top, ok := Get(topic.name)
		if !ok {
			t.Errorf("help topic %q not found", topic.name)
			continue
		}
		for _, b := range banned {
			if strings.Contains(top.Body, b) {
				t.Errorf("%s still teaches the retired route form %q", topic.name, b)
			}
		}
		for _, want := range topic.wants {
			if !strings.Contains(top.Body, want) {
				t.Errorf("%s does not mention %q — it teaches the authored route form", topic.name, want)
			}
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

func TestGet(t *testing.T) {
	top, ok := Get("create-story")
	if !ok {
		t.Fatal("create-story topic not found")
	}
	if !strings.Contains(top.Body, "acceptance criteria") {
		t.Errorf("create-story body missing expected content")
	}
	if _, ok := Get("does-not-exist"); ok {
		t.Error("expected miss for unknown topic")
	}
}

// TestWorkflowConvertTopic: when the DOT front end retired, a repo that had not
// converted started REFUSING transitions, and the refusal points an agent here
// (sty_d953c5d8). This topic is therefore the only thing standing between a
// broken repo and a stuck agent, so it must actually carry the mapping — not
// just say that a conversion is owed.
func TestWorkflowConvertTopic(t *testing.T) {
	top, ok := Get("workflow-convert")
	if !ok {
		t.Fatal("the conversion guide must ship: every refusal names it")
	}
	// The two files, and the frontmatter rule that trips every first attempt.
	for _, want := range []string{"done.toml", "step.toml", "applies_to"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the guide does not mention %q", want)
		}
	}
	// Every route-grammar key an agent has to write. A key missing here is a key
	// the agent has to guess.
	for _, key := range []string{
		"status", "requires", "reviewers", "reviewer_agent", "parallel",
		"terminal", "start", "park", "cancel", "recover", "[[gate]]", "for", "mandatory",
		"reviewer_carry",
	} {
		if !strings.Contains(top.Body, key) {
			t.Errorf("the guide does not cover the %q key", key)
		}
	}
	// The one value the key takes, spelled as an agent has to write it.
	if want := `reviewer_carry = "until_accept"`; !strings.Contains(top.Body, want) {
		t.Errorf("the guide does not document %q", want)
	}
	// The two mistakes the conversion actually makes: authoring the topology the
	// binary owns, and forgetting that a category-specific workflow is a section.
	for _, want := range []string{"Do not author topology", "SECTION"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the guide does not warn about %q", want)
		}
	}
	// And how to prove the conversion kept every gate.
	for _, want := range []string{
		"satelle workflow validate", "satelle workflow show", "satelle story route", "satelle migrate",
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the guide does not name the verification step %q", want)
		}
	}
}

// TestWorkflowConvertTopicCoversTheWild guards the gap sty_e184768e measured: a
// survey of every authored DOT graph on a real fleet found 15 constructs in use
// and this topic documented 8. An undocumented construct is one an agent either
// transcribes into a key the grammar rejects, or — worse — drops in silence.
func TestWorkflowConvertTopicCoversTheWild(t *testing.T) {
	top, ok := Get("workflow-convert")
	if !ok {
		t.Fatal("the conversion guide must ship: every refusal names it")
	}
	// Every construct the survey found. The eight already covered are asserted
	// by TestWorkflowConvertTopic; these are the seven that were not.
	for _, construct := range []string{
		"reviewer_skill", "rankdir", "on_enter_agent", "on_enter_prompt",
		"goal=", "vars=", "guardrails",
	} {
		if !strings.Contains(top.Body, construct) {
			t.Errorf("no target documented for %q, which is present in the wild", construct)
		}
	}
	// The dangerous one: a RETIRED mechanism, not a renamed key. An agent must
	// be told to re-home the advisor, and told that nothing dispatches it.
	for _, want := range []string{"RETIRED", "retired, not renamed", "advise = {", "ORCHESTRATOR"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the on_enter callout is missing %q", want)
		}
	}
	// The quiet one: real operator intent with no home in the grammar, and no
	// warning when it vanishes. Both halves must be said.
	for _, want := range []string{"constitution", "nothing will warn you"} {
		if !strings.Contains(strings.ToLower(top.Body), strings.ToLower(want)) {
			t.Errorf("the goal/vars/guardrails callout is missing %q", want)
		}
	}
	// The two per-repo DECISIONS — named as decisions, with the traps that make
	// them decisions rather than lookups.
	for _, want := range []string{
		"The two decisions only you can make",
		"BY NAME", "[substrate]", "ungated",
	} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the decisions section is missing %q", want)
		}
	}
	// The verify loop must be an instruction with an ORDER: migrate deletes the
	// graph you diff against.
	for _, want := range []string{"gate by gate", "before** `satelle migrate --yes`"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the verification loop is missing %q", want)
		}
	}
}

// TestWorkflowConvertTopicCoversTheMarkdownRouteSource (sty_81bb0dde AC5): the
// route source became TOML, and a repo already on the MARKDOWN route source is
// refused by name until it converts. That refusal points here — this topic, not
// a second one — so the key-by-key mapping has to be in it. Every construct the
// markdown spelling had needs a target, or an agent either transcribes a key the
// decoder rejects or drops it in silence.
func TestWorkflowConvertTopicCoversTheMarkdownRouteSource(t *testing.T) {
	top, ok := Get("workflow-convert")
	if !ok {
		t.Fatal("the conversion guide must ship")
	}
	if !strings.Contains(top.Body, "md-to-toml") {
		t.Fatal("the guide carries no md-to-toml mapping — the refusal names this page")
	}
	for _, construct := range []string{
		"`---` frontmatter block", "`[meta]` table",
		"`## <category>` in done.md", "`- <obligation>` list",
		"`park: <state> @<gate>`", "`cancel: <state> @<gate>`", "`recover: <step> from a, b`",
		"`+ <tag> <obligation>`", "tag_obligation",
		"`## <name>` in step.md", "`provides: <obligation>`",
		"`## gate <skill>`", "`<!-- comment -->`",
		"a `hooks:` block list", "[[meta.hooks]]",
	} {
		if !strings.Contains(top.Body, construct) {
			t.Errorf("no md-to-toml target documented for %q", construct)
		}
	}
	// The ONE part that is not a transcription: markdown keyed a step by its
	// heading and named the obligation in `provides:`; TOML keys it by the
	// obligation. An agent that misses this writes a catalogue that collides.
	for _, want := range []string{"the KEY is what the step `provides:`", "stage names repeat"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the step-key rule is missing %q", want)
		}
	}
	// And the prose the new format makes unnecessary must be named as deletable,
	// or every converted repo carries tuition for a format it no longer uses.
	for _, want := range []string{"HOW TO READ THIS FILE", "Delete them"} {
		if !strings.Contains(top.Body, want) {
			t.Errorf("the guide does not say to drop the retired preamble: missing %q", want)
		}
	}
}

// TestWorkflowConvertTopicIsRepoAgnostic: the topic ships in the binary and is
// read by every repo. A story id, a this-repo category or an example lifted
// from one repo's graph reads as instructions to everyone else.
//
// Stricter than TestEmbeddedHelpHasNoUnreachableReferences (which permits bare
// (sty_…) provenance annotations elsewhere): workflow-convert must stay fully
// generic. Kept as its own test so that guarantee cannot be weakened by folding
// into the corpus-wide rule (sty_a319db89 architecture note).
func TestWorkflowConvertTopicIsRepoAgnostic(t *testing.T) {
	top, ok := Get("workflow-convert")
	if !ok {
		t.Fatal("the conversion guide must ship")
	}
	if strings.Contains(top.Body, "sty_") {
		t.Error("the topic names a story id — it ships to repos that have never seen it")
	}
	// A named graph file is an example nobody else has on disk — including the
	// embedded defaults, which no longer ship. Describe the graph by what it
	// DECLARES (`applies_to: [...]`), never by its filename.
	if strings.Contains(top.Body, "-workflow.md") {
		t.Error("the topic names a specific graph file — describe graphs by what they declare")
	}
	// A path under .satelle/skills or a bare skill name is this repo's
	// substrate; the topic's own examples must stay generic.
	if strings.Contains(top.Body, ".satelle/skills/") {
		t.Error("the topic reaches into a repo's authored skills")
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

// TestEpicWaveDocumentsCrossRepoDependsOn: the cross-repo depends-on form, the
// name it takes, when it is satisfied, and that unresolved blocks are all in
// `satelle help epic-wave`.
func TestEpicWaveDocumentsCrossRepoDependsOn(t *testing.T) {
	wave, ok := Get("epic-wave")
	if !ok {
		t.Fatal("epic-wave topic not found")
	}
	start := strings.Index(wave.Body, "## Depending on a story in another repo")
	if start < 0 {
		t.Fatal("epic-wave topic lost its cross-repo depends-on section")
	}
	section := wave.Body[start:]
	if end := strings.Index(section[3:], "\n## "); end >= 0 {
		section = section[:end+3]
	}
	for _, want := range []string{
		"depends-on:<repo>/<story id>",
		"satelle workspace list",      // where the repo name comes from
		"not the hosted project slug", // and what it is not
		"**its own",                   // satisfied by the other repo's route...
		"container",                   // ...and every container above the story
		"(done; container <id> open)",
		"**Unresolved is a blocker.**",
		"unresolved: <why>",
		"read-only",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("epic-wave cross-repo section missing %q", want)
		}
	}
}

// TestReadyChildrenDocumented (sty_2b737ed5): the key is documented in
// workflow-convert and epic-wave says children are planned when the wave lets
// them start under it.
func TestReadyChildrenDocumented(t *testing.T) {
	for topic, wants := range map[string][]string{
		"workflow-convert": {"`ready_children = false`", "planned at its own engagement", "refused naming `ready_children`"},
		"epic-wave":        {"`ready_children = false`", "children are planned when the wave lets them start"},
	} {
		got, ok := Get(topic)
		if !ok {
			t.Fatalf("%s topic not found", topic)
		}
		for _, want := range wants {
			if !strings.Contains(got.Body, want) {
				t.Errorf("%s does not carry %q", topic, want)
			}
		}
	}
}

// TestEpicWaveHelpStatesEpicReadinessRule (sty_a476b913): a child cannot engage
// before its epic is ready, the refusal names the readying command, and cancel
// stays open.
func TestEpicWaveHelpStatesEpicReadinessRule(t *testing.T) {
	wave, ok := Get("epic-wave")
	if !ok {
		t.Fatal("epic-wave topic not found")
	}
	for _, want := range []string{
		"## A child waits for its epic to be ready",
		"cannot leave backlog into an engaging status",
		"satelle\nstory ready <epic>",
		"`cancel`",
		// sty_25064a6c: the gate needs a readiness step on the epic's route, and the
		// default substrate's epic route declares one.
		"only when the epic's own route declares a readiness step",
		"default\nsubstrate's epic route declares one",
	} {
		if !strings.Contains(wave.Body, want) {
			t.Errorf("epic-wave topic missing %q", want)
		}
	}
	if strings.Contains(wave.Body, "a child on a\nroute with no readiness step is covered too") {
		t.Error("epic-wave topic still says a child on a route with no readiness step is covered")
	}
}

// TestRemotePlacementTopics (sty_dde8b6a4): where a parallel epic child runs is
// stated in `satelle help epic-wave`, and the two step.toml keys that declare it
// in `satelle help agent-dispatch`.
func TestRemotePlacementTopics(t *testing.T) {
	wave, ok := Get("epic-wave")
	if !ok {
		t.Fatal("epic-wave topic not found")
	}
	start := strings.Index(wave.Body, "## Where a child runs")
	if start < 0 {
		t.Fatal("epic-wave topic lost its Where a child runs section")
	}
	section := wave.Body[start:]
	for _, want := range []string{
		"remote_agent", "local_tags", "parallel epic", "`sequential = true`", // the placement rule
		"not signed in",                 // the fallback
		"git push -u <remote> <branch>", // the driver's push duty
		"satelle story rework",          // rework is refused...
		"Re-present the edge",           // ...and the edge re-presented
		"parks to `blocked`",            // parking after the rejection count
		"gate-rejection count",
		"stays at its from-state", // failure
		"session URL",
		"`after_children` step keeps refusing",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("epic-wave Where a child runs section missing %q", want)
		}
	}
	dispatch, ok := Get("agent-dispatch")
	if !ok {
		t.Fatal("agent-dispatch topic not found")
	}
	for _, want := range []string{"remote_agent", "local_tags", "interface = \"cloud\"", "waits_on_children", "satelle help epic-wave"} {
		if !strings.Contains(dispatch.Body, want) {
			t.Errorf("agent-dispatch topic missing %q", want)
		}
	}
}
