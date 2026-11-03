// Package health carries the shared FINDING vocabulary — stable identifiers,
// severity, and remediation — that every satelle validation surface speaks
// (sty_e9da28e2).
//
// Before this package, each surface rendered its own prose: `satelle agent
// validate` printed `FAIL <sentence>`, engagement refused with a differently
// worded sentence, and `satelle init` printed a third. An operator could not
// tell that all three were the same defect, and nothing could be matched
// mechanically. A Finding gives one defect one identifier wherever it surfaces.
//
// It is a LEAF: no satelle imports, so `agentvalidate`, `doctor`, `cli`, and
// `verb` can all speak it without a cycle. It carries no rule of its own — it is
// a vocabulary, not a validator. Deciding whether something IS a defect stays
// with the authority that owns that rule.
package health

import (
	"fmt"
	"sort"
	"strings"
)

// Severity orders a finding's consequence. The mapping to exit codes and to
// fail-closed behaviour is the CALLER's, so a surface can treat a warning as
// advisory while another treats the same finding as blocking.
type Severity string

const (
	// SeverityError is a hard defect: a non-zero exit, and a refused engagement.
	SeverityError Severity = "error"
	// SeverityWarn is advisory — printed, never blocking. Used where the
	// underlying test is a heuristic and a false positive would strand a repo.
	SeverityWarn Severity = "warn"
	// SeverityInfo is context an operator asked for (a live probe that passed).
	SeverityInfo Severity = "info"
)

// rank orders severities for Worst().
func (s Severity) rank() int {
	switch s {
	case SeverityError:
		return 3
	case SeverityWarn:
		return 2
	case SeverityInfo:
		return 1
	}
	return 0
}

// AtLeast reports whether s is at least as severe as other.
func (s Severity) AtLeast(other Severity) bool { return s.rank() >= other.rank() }

// Stable finding identifiers. They are a CONTRACT: an operator scripts against
// them, and two surfaces reporting the same defect must use the same id. Add a
// new id rather than repurposing one whose meaning has shifted.
const (
	// Agents layer / profile resolution.
	IDAgentsLoad          = "agents.load"           // agents.toml absent or unparseable
	IDAgentsBinding       = "agents.binding"        // a binding is invalid (command, interface, timeout, …)
	IDAgentsProfileBroken = "agents.profile.broken" // a machine-wide profile reference does not resolve
	IDEnvUnresolved       = "env.unresolved"        // a ${VAR} in env/settings resolves to nothing
	IDAgentsSameRole      = "agents.same_role"      // the executor and orchestrator seats both resolve to in-loop
	// IDAgentsWiringAbsent: a binding's command names a gate-wiring path its
	// harness declares (e.g. pi's -e .pi/extensions/satelle.ts) that is absent
	// from the repo, so its first dispatch fails on the load.
	IDAgentsWiringAbsent = "agents.wiring_absent"
	// IDAgentsSharedProfile / IDAgentsSharedGlobalRoles: the SHARED agents.toml
	// carries a reference into this machine's ~/.satelle catalog (a profile= or
	// use_global_roles), which does not travel — it belongs in agents.local.toml.
	// IDAgentsSharedMachineValue: it carries a value that belongs to one machine
	// (a literal env value, a literal secret, an absolute path, harness=).
	IDAgentsSharedProfile      = "agents.shared.profile"
	IDAgentsSharedGlobalRoles  = "agents.shared.global_roles"
	IDAgentsSharedMachineValue = "agents.shared.machine_value"

	// Reviewer / allocation safety.
	IDReviewerUnsafe = "reviewer.unsafe" // a reviewer's permission ceiling is escaped
	// IDReviewerIsolation: a reviewer binding skips permission requests without a
	// tool allow-list inside its grant, or its adapter cannot deny an
	// out-of-grant tool — dispatch would refuse it (sty_ef3efb51).
	IDReviewerIsolation = "reviewer.isolation"
	// IDStepUntrusted: a seat on a step that declares untrusted_input is not held
	// read-only (a shell, network, write or unknown tool, an adapter that cannot
	// trim tools, or no read-only context channel for a performer).
	IDStepUntrusted = "step.untrusted_input"
	IDNodeAlloc     = "node.alloc"            // a workflow node/edge allocates a binding that does not resolve
	IDHookAlloc     = "hook.alloc.unresolved" // a lifecycle hook's agent allocation is unusable

	// Required binaries.
	IDBinaryMissing   = "binary.missing"   // a binding's executable is not on PATH
	IDBinaryMalformed = "binary.malformed" // a binding's command cannot be parsed into argv

	// Authored substrate.
	IDWorkflowStructure   = "workflow.structure"   // a workflow/skill/principle/task fails its structure contract
	IDWorkflowConsistency = "workflow.consistency" // cross-workflow consistency (ambiguity, unresolved skills)
	IDCommandRuleAllow    = "command_rule.allow"   // a [[gate.command_rule]] allow entry names no state of any route

	// The process of record (sty_d6e209aa): where the repository's authored
	// process is missing, unreadable or overridden, satelle says so rather than
	// governing by the binary's default in silence.
	IDProcessUnreadable = "process.unreadable" // the authored workflows dir exists and cannot be read
	IDProcessAbsent     = "process.absent"     // the authored workflows dir is absent; the embedded backstop governs
	IDProcessDivergent  = "process.divergent"  // a linked worktree's own copy of the process differs from the main tree's

	// The project tier (sty_8dedc0a4): a bound repo's team-owned paths resolve
	// from the project's deployed version, not the tree.
	IDTeamTreeDiffers   = "team.tree_differs"   // a team-owned path in the tree differs from the project tier, which governs
	IDTeamInitOwned     = "team.init_owned"     // a [team] paths glob names a file satelle init owns; it stays with init
	IDTeamLiteralScript = "team.literal_script" // substrate names a team-owned script by its tree path, not `satelle team path`

	// Deployment.
	IDScaffoldStale   = "scaffold.stale"   // deployed harness scaffolding differs from the binary's canonical form
	IDScaffoldMissing = "scaffold.missing" // canonical harness scaffolding is absent
	IDRepoUnreadable  = "repo.unreadable"  // a registered repo could not be checked at all

	// IDBreakingUnacknowledged: the binary is ahead of the repo's deployed stamp
	// across a CHANGELOG ### Breaking release — the migration is not yet done and
	// `satelle init` has not re-stamped (sty_6e143870).
	IDBreakingUnacknowledged = "breaking.unacknowledged"
	IDConfigStray            = "config.stray" // machine-scope key leftover in a repo file (sty_21a7d16d)
	// IDSubstrateUnlocked: the repo set [gate] lock_substrate_paths = [], the
	// documented opt-out, so a performing story can rewrite the substrate that
	// judges it. Reported so the unlocked posture is visible (sty_992cffc6).
	IDSubstrateUnlocked = "gate.substrate.unlocked"
	// IDSubstrateUntracked: the repo's .satelle/ process substrate is not tracked
	// in git — ignored by the repo's .gitignore or a machine-wide
	// core.excludesFile, or present but never added (sty_45bf867b).
	IDSubstrateUntracked = "repo.substrate.untracked"

	// Workstate sync health (sty_30696eeb).
	IDSyncLocal    = "sync.local"         // work-state areas are all local
	IDSyncOk       = "sync.ok"            // last successful push is within threshold
	IDSyncFailing  = "sync.failing"       // standing hosted-push failure
	IDSyncUnbacked = "sync.unbacked"      // last successful push older than stale_after
	IDSyncConfig   = "sync.config"        // stale_after missing or unparseable
	IDSyncPushOff  = "sync.push_off"      // the machine config turns hosted push off (sty_a21d9c07)
	IDStatusDrift  = "story.status.drift" // work-item row disagrees with last status_transition

	// Substrate sync health (sty_7562055c): the authored areas and documents
	// against the last synced base and the last recorded hosted check. Offline.
	IDSyncSubstrateOk        = "sync.substrate_ok"        // every non-local area is in step (or every area is local)
	IDSyncSubstrateUnpushed  = "sync.substrate_unpushed"  // local files differ from the last synced base
	IDSyncSubstrateUnmerged  = "sync.substrate_unmerged"  // a pull left paths for merging
	IDSyncSubstrateBehind    = "sync.substrate_behind"    // the last recorded check says behind or diverged
	IDSyncSubstrateUnchecked = "sync.substrate_unchecked" // no recorded check, or older than stale_after
	IDSyncSubstrateUnbound   = "sync.substrate_unbound"   // a non-local area but no signed-in server or bound project

	// The context every agent actually receives (sty_a17afa15): what the
	// SessionStart hook injects per harness and what each dispatched seat is
	// handed. Every one is advisory (WARN or INFO) — a context that is too big,
	// repeats itself or names something that does not resolve degrades an agent,
	// it does not stop satelle governing.
	IDContextSessionOverflow    = "context.session.overflow"    // SessionStart context omits documents to read pointers under a harness limit
	IDContextSessionUnavailable = "context.session.unavailable" // a bound harness's SessionStart context cannot be rendered
	IDContextReminderOverClip   = "context.reminder.over_clip"  // the per-prompt reminder exceeds its harness's tool_context_limit_chars
	IDContextDuplicateVerbatim  = "context.duplicate.verbatim"  // a passage is copied word-for-word between two documents
	IDContextDuplicateSemantic  = "context.duplicate.semantic"  // a reviewer judged one document to restate another (opt-in)
	IDContextBaselineOverride   = "context.baseline.override"   // a repo principle or skill shadows a baseline principle by name with another body, or restates a passage of one
	IDContextRefUnresolved      = "context.ref.unresolved"      // a satelle reference names nothing that resolves, or a retired name
	IDContextRefUnwired         = "context.ref.unwired"         // a named skill exists but no route, gate, hook, summary or task wires it
	IDContextIndexStale         = "context.index.stale"         // documents/index.md lists a document that does not exist
	IDContextReviewUnavailable  = "context.review.unavailable"  // the opt-in semantic reviewer could not run or its output did not parse

	// Live probes (opt-in only).
	IDLiveOK           = "live.ok"            // a provider answered
	IDLiveAuth         = "live.auth"          // the provider reported an authentication problem
	IDLiveTimeout      = "live.timeout"       // the probe exceeded its deadline
	IDLiveSpawn        = "live.spawn"         // the provider process could not be started
	IDLiveACPHandshake = "live.acp.handshake" // an ACP peer accepted the pipe but never completed initialize
	IDLiveVersionDrift = "live.version_drift" // a CLI's version differs from the one satelle's measured behavior was captured against, or is unknown
)

// StampRefusalMarker is the phrase every breaking-drift message carries, where
// the binary is ahead of a repo's deployed stamp across a ### Breaking release.
// The drift gate composes its messages from it and the surfaces that replay a
// recorded refusal (doctor, init) recognise it by it, so the text has one owner.
const StampRefusalMarker = "across BREAKING release"

// IsStampRefusal reports whether reason is the drift gate's refusal of a repo
// whose deployed stamp is behind a breaking release — a failure `satelle init`
// heals, not a retry of the command that recorded it.
func IsStampRefusal(reason string) bool {
	return strings.Contains(reason, StampRefusalMarker) && strings.Contains(reason, "deployed stamp")
}

// RechainRemediation is the fix for a story a push could not publish because its
// local chain left its hosted one: rechain merges the two, dry run first. Doctor
// and the push name it, so the text has one owner; it never points at --force,
// which drops the local work (sty_f321c29e).
const RechainRemediation = "run `satelle sync workstate rechain --dry-run`, then `satelle sync workstate rechain`"

// ids is the registry every constant above belongs to — the uniqueness test
// reads it, so a copy-pasted duplicate fails the build's tests rather than
// silently collapsing two defects into one identifier.
var ids = []string{
	IDAgentsLoad, IDAgentsBinding, IDAgentsProfileBroken, IDAgentsSameRole, IDAgentsWiringAbsent, IDEnvUnresolved,
	IDAgentsSharedProfile, IDAgentsSharedGlobalRoles, IDAgentsSharedMachineValue,
	IDReviewerUnsafe, IDReviewerIsolation, IDStepUntrusted, IDNodeAlloc, IDHookAlloc,
	IDBinaryMissing, IDBinaryMalformed,
	IDWorkflowStructure, IDWorkflowConsistency, IDCommandRuleAllow,
	IDProcessUnreadable, IDProcessAbsent, IDProcessDivergent,
	IDScaffoldStale, IDScaffoldMissing, IDRepoUnreadable, IDConfigStray, IDSubstrateUnlocked, IDSubstrateUntracked, IDBreakingUnacknowledged,
	IDSyncLocal, IDSyncOk, IDSyncFailing, IDSyncUnbacked, IDSyncConfig, IDSyncPushOff,
	IDSyncSubstrateOk, IDSyncSubstrateUnpushed, IDSyncSubstrateUnmerged, IDSyncSubstrateBehind,
	IDSyncSubstrateUnchecked, IDSyncSubstrateUnbound,
	IDStatusDrift,
	IDContextSessionOverflow, IDContextSessionUnavailable, IDContextReminderOverClip, IDContextDuplicateVerbatim,
	IDContextDuplicateSemantic, IDContextBaselineOverride, IDContextRefUnresolved, IDContextRefUnwired, IDContextIndexStale,
	IDContextReviewUnavailable,
	IDLiveOK, IDLiveAuth, IDLiveTimeout, IDLiveSpawn, IDLiveACPHandshake, IDLiveVersionDrift,
}

// IDs returns every declared finding identifier, sorted.
func IDs() []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// Finding is one health observation.
//
// Detail is the operator-facing sentence — the SAME text a surface would have
// printed on its own, so adopting findings changes identifiers and structure
// without rewording established messages. It must never contain a secret value;
// name an environment key, never its contents.
type Finding struct {
	ID          string   `json:"id"`
	Severity    Severity `json:"severity"`
	Title       string   `json:"title"`
	Detail      string   `json:"detail"`
	Remediation string   `json:"remediation,omitempty"`
	// Artifact is what the finding is about — a file path, a binding section, a
	// workflow name. Free-form and repo-relative where it is a path.
	Artifact string `json:"artifact,omitempty"`
}

// Error builds an error-severity finding.
func Error(id, title, detail string) Finding {
	return Finding{ID: id, Severity: SeverityError, Title: title, Detail: detail}
}

// Warn builds a warn-severity finding.
func Warn(id, title, detail string) Finding {
	return Finding{ID: id, Severity: SeverityWarn, Title: title, Detail: detail}
}

// Info builds an info-severity finding.
func Info(id, title, detail string) Finding {
	return Finding{ID: id, Severity: SeverityInfo, Title: title, Detail: detail}
}

// WithRemediation returns f carrying the fix to suggest.
func (f Finding) WithRemediation(s string) Finding { f.Remediation = s; return f }

// About returns f carrying the artifact it concerns.
func (f Finding) About(artifact string) Finding { f.Artifact = artifact; return f }

// String renders one finding as `id — detail (remediation)`.
func (f Finding) String() string {
	s := f.ID + " — " + f.Detail
	if f.Remediation != "" {
		s += " (" + f.Remediation + ")"
	}
	return s
}

// Findings is an ordered list of observations.
type Findings []Finding

// Worst returns the highest severity present, or "" when empty.
func (f Findings) Worst() Severity {
	worst := Severity("")
	for _, x := range f {
		if x.Severity.rank() > worst.rank() {
			worst = x.Severity
		}
	}
	return worst
}

// OK reports whether nothing rises to an error.
func (f Findings) OK() bool { return f.Worst() != SeverityError }

// WithSeverity returns only the findings at one severity.
func (f Findings) WithSeverity(s Severity) Findings {
	var out Findings
	for _, x := range f {
		if x.Severity == s {
			out = append(out, x)
		}
	}
	return out
}

// Details returns the operator-facing sentence of each finding at severity s —
// the compatibility seam for surfaces that still carry plain string lists.
func (f Findings) Details(s Severity) []string {
	var out []string
	for _, x := range f {
		if x.Severity == s {
			out = append(out, x.Detail)
		}
	}
	return out
}

// RenderRefusal formats an error-severity set for a fail-closed message —
// the shared shape a refusal uses so an operator sees the same identifiers a
// diagnostic would print. Returns "" when nothing rises to an error.
func RenderRefusal(prefix string, f Findings) string {
	errs := f.WithSeverity(SeverityError)
	if len(errs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d problem(s)):", prefix, len(errs))
	for _, x := range errs {
		fmt.Fprintf(&b, "\n  - %s", x.String())
	}
	return b.String()
}
