package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/board"
	"github.com/bobmcallan/satelle/internal/compact"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/docstory"
	"github.com/bobmcallan/satelle/internal/epicset"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/logfile"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/oplog"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/waiver"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// storeAnnotation marks a command as needing the local store. The root's
// persistent pre-run opens the bootstrap (config + db) only for these commands
// and closes it after — so `satelle version` / `--help` never create a db.
const storeAnnotation = "needs-store"

// storeOptionalAnnotation marks a store-backed command that must ALSO be
// runnable from a directory satelle does not govern — because what it reports is
// not about the current repo. `satelle doctor --all` is the case: it enumerates
// the workspace registry, so refusing it in an ungoverned cwd would make the
// estate un-inspectable from anywhere but a satelle repo, and the upgrade
// guidance names it as the blind-safe thing to run (sty_0f471251).
//
// Only ErrNotInitialised is tolerated. Any other bootstrap failure still stops
// the command — this widens WHERE a command may run, never what it ignores.
const storeOptionalAnnotation = "store-optional"

// needsStoreOptional flags a store-backed command that tolerates an ungoverned
// working directory. The command MUST handle a nil app.
func needsStoreOptional() map[string]string {
	return map[string]string{storeAnnotation: "1", storeOptionalAnnotation: "1"}
}

// appCtxKey carries the opened *app.App on the command context.
type appCtxKey struct{}

// uiDrainCtxKey carries the per-invocation UI push drain (sty_9ba3d709).
type uiDrainCtxKey struct{}

// needsStore returns a cobra annotations map flagging a store-backed command.
func needsStore() map[string]string { return map[string]string{storeAnnotation: "1"} }

// hostedOriginAnnotation marks a verb whose store change is materialised from
// the hosted project (pull, snapshot, rechain, rehydrate). Its end-of-verb UI
// drain stamps mirror.OriginHosted so satelled never pushes that change back.
const hostedOriginAnnotation = "hosted-origin"

// hostedOrigin adds hostedOriginAnnotation to ann (nil starts a new map).
func hostedOrigin(ann map[string]string) map[string]string {
	if ann == nil {
		ann = map[string]string{}
	}
	ann[hostedOriginAnnotation] = "1"
	return ann
}

// ingestOrigin is the mirror origin cmd's UI drain stamps.
func ingestOrigin(cmd *cobra.Command) mirror.IngestOrigin {
	if cmd != nil && cmd.Annotations[hostedOriginAnnotation] != "" {
		return mirror.OriginHosted
	}
	return mirror.OriginLocal
}

// openAppForCmd opens the bootstrap and stashes it on the command's context.
// Called from the root's PersistentPreRunE for store-backed commands.
func openAppForCmd(cmd *cobra.Command) error {
	a, err := app.Open()
	if err != nil {
		// An ungoverned repo is an operator condition, not a bootstrap fault:
		// return the actionable line bare rather than wrapped in `bootstrap:`
		// noise (sty_20a7824c). Non-zero, deliberately — this is the same class
		// of refusal these verbs already returned via requireAgents, so scripts
		// branching on a satelle verb keep their current semantics.
		if errors.Is(err, app.ErrNotInitialised) {
			return err
		}
		return fmt.Errorf("bootstrap: %w", err)
	}
	// Drift / breaking-surface gate: a deployed repo behind a breaking binary
	// release that declares `init-heals:` fails closed and names `satelle init` as
	// the heal path; a release whose migrations are manual only warns, once per
	// session, and the command runs (sty_6e143870).
	// `restore` is a heal command and must not sit behind the stamp gate it
	// heals (sty_a9ec33e7) — keep confirmation; keep other store verbs gated.
	if cmd.Name() != "restore" {
		bw, derr := checkBreakingDrift(a.RepoRoot)
		if derr != nil {
			_ = a.Close()
			return derr
		}
		warnBreakingOnce(a.RuntimeDir, bw, cmd.ErrOrStderr())
	}
	// Lazy harness install (epic:minimal-harness-footprint): if this process is
	// inside a Claude/Grok session and the repo lacks that harness scaffold,
	// install it now (idempotent). First session of a new harness may still
	// have run without hooks; next session picks them up.
	ensureLazySessionHarness(a.RepoRoot)
	// Scaffold drift (sty_ac25b787, sty_e56ea643): deployed harness wrappers
	// behind the binary are satelle-owned bytes that `satelle init` heals, so
	// they WARN on stderr and never refuse — only a release's own CHANGELOG
	// ### Breaking that declares `init-heals:` (checkBreakingDrift above) stops a
	// command. `status` prints the full block itself.
	if cmd.Name() != "status" {
		warnScaffoldDrift(a.RepoRoot, cmd.ErrOrStderr())
	}
	// Broken configuration refuses to run (sty_d0d6bb67): an initialized repo
	// (this command reached the store, so .satelle exists) must carry a loadable
	// agents layer — no silent fallback to compiled defaults. `satelle init` is
	// not store-backed, so a fresh repo still bootstraps and (re)seeds the file.
	eff, err := requireAgents(a)
	if err != nil {
		_ = a.Close()
		return err
	}
	agents := eff.Agents
	// Wire the opened stores into the verb registry — the single seam both the
	// CLI and the web server dispatch through. The CLI is one-shot, so wiring
	// the package globals per invocation is correct.
	verb.SetWorkItemStore(a.Store.Stories)
	verb.SetLedgerStore(a.Store.Ledger)
	verb.SetTxRunner(a.Store.InTx)
	verb.SetDocIndexStore(a.Store.DocIndex)
	verb.SetAuthoredDirs(a.AuthoredDirs().Dirs)
	verb.SetSubstrateConfigDir(a.ProcessDataDir)
	verb.SetLeaseStore(a.Store.Leases)
	verb.SetRetrieveStore(a.Store.Retrieve)
	// UI push drain (sty_9ba3d709 / sty_126228b2 / sty_21a7d16d): machine
	// [service] endpoint (env > config > derived localhost:port). SATELLE_SERVER_ENDPOINT=none
	// disables push (hermetic tests). Clear first so a prior test/process state
	// cannot leak sinks into this one-shot invocation.
	verb.SetChangeNotifier(nil)
	var drain *uiDrain
	var serveEP string
	if gc, gerr := config.LoadGlobal(); gerr == nil {
		serveEP = gc.Service.ResolveEndpoint()
	}
	if ep := serveEP; ep != "" {
		drain = &uiDrain{
			endpoint: ep,
			repoKey:  config.RepoKey(a.RepoRoot),
			app:      a,
			origin:   ingestOrigin(cmd),
		}
		verb.AddChangeNotifier(drain.mark)
	}
	// Stories attachments are RUNTIME state (home-keyed under RuntimeDir —
	// sty_4660bbe1). The database is the sole story store (markdown mirror
	// removed, sty_fa1e02e1); this dir holds plan/step-summary attachments only.
	verb.SetStoryDir(filepath.Join(a.RuntimeDir, "stories"))
	// Authored root (documents/, workflows/, …) — needed when a verb spans both
	// planes (e.g. migrateLegacySummaries moves docs → runtime stories/).
	verb.SetDataDir(a.ProcessDataDir)
	// Archive-retention policy for the closed-story attachment dirs — a no-op
	// unless satelle.toml sets a count/age policy (sty_aba7200c).
	verb.SetStoryRetention(a.Config.StoriesKeepClosed, a.Config.StoriesKeepDays)
	// CCR retrieval-store retention (sty_b0577532): a no-op unless satelle.toml
	// sets retrieve_keep_days.
	verb.SetRetrieveRetention(a.Config.RetrieveKeepDays)
	// Binary attachment cap + content-type allowlist (sty_40e5a305): enforced
	// in the verb so CLI and any future hosted/MCP caller share one rule.
	verb.SetAttachmentPolicy(a.Config.ResolveAttachmentMaxBytes(), a.Config.ResolveAttachmentAllowTypes())
	// Backups root is also runtime (sibling of stories/, not of tasks/).
	verb.SetBackupsDir(filepath.Join(a.RuntimeDir, "backups"))
	// Seat concurrency mode (sty_c098dc2d): [engagement] parallel selects the
	// arbitration KEY a story claims the engagement seat under — "none" (the
	// default, and what this repo has always enforced: one performing story) or
	// "epic" (sibling children of one epic, one working tree per lease). Unwired
	// is "none", so a repo with no [engagement] section is unaffected.
	verb.SetEngagementMode(a.ProcessConfig)
	// Worktree declaration (sty_804c566b): the process-of-record [worktree] table
	// and the main tree it carries from, so a worktree reads the main tree's
	// declaration rather than one of its own.
	verb.SetWorktreeConfig(a.ProcessConfig, a.ProcessRoot)
	// Start-of-work trunk check (sty_9f3e51d1): the process-of-record [trunk]
	// declaration, and the invoking tree the check inspects and the engagement
	// baseline is taken in. Unwired, the check does nothing.
	verb.SetTrunkConfig(a.PlaneConfig().Trunk, a.RepoRoot)
	// A detached gate run also appends each line to its verdict log, the only
	// channel that reaches the driving session.
	verb.SetTrunkOutput(trunkOutput(cmd.ErrOrStderr()))
	// A plan's criteria skipped in favour of an accepted amend are reported on
	// the same channel, so the operator driving the transition sees why.
	verb.SetApplyOutput(trunkOutput(cmd.ErrOrStderr()))
	// Process-of-record reporting (sty_d6e209aa): where the authored process
	// lives, so an absent workflows dir or a divergent worktree copy is reported
	// at engage, in a refusal and in the route document. Data only — nothing is
	// compared until a surface asks.
	verb.SetProcessProbe(&verb.ProcessProbe{
		Process: a.ProcessConfig, InvokingRoot: a.RepoRoot, ProcessRoot: a.ProcessRoot,
	})
	// Controlled tag vocabulary (sty_034d843c): validate namespaces declared in
	// satelle.toml [tags.vocabulary] at story/task create and set. Independent of
	// the agent CLI — must work with no harness installed.
	verb.SetTagVocabulary(a.Config)
	// [release] fields: the metadata keys `story release-record` accepts.
	verb.SetReleaseFields(a.Config.Release.Fields)
	// Assignee holder (sty_8ccaa906): local credstore PrincipalID only — no
	// hosted.Client.Me, no network. Local-only (no credential) means
	// unassigned (offline team-of-1). The one user rule is resolveUser
	// (sty_e698d914).
	verb.SetAssigneeResolver(func() string { return resolveUser(a.Config, a.RepoRoot).Holder() })
	// Session token (sty_6ed6318d): an engage under SATELLE_TOKEN first learns
	// the token's user, and is refused while it cannot — the empty holder would
	// otherwise pass the wrong-holder guard unnoticed.
	verb.SetEngageGuard(func(ctx context.Context) error { return ensureSessionPrincipal(ctx, a.Config, a.RepoRoot) })
	// Close guard: a terminal move names the gated uncommitted paths of the
	// story's seat tree before the seat is released. The classification stays
	// with the edit gate's dirtyGatedPaths.
	verb.SetDirtyPathsProbe(func(_ context.Context, tree string) ([]string, error) { return dirtyGatedPaths(tree) })
	// Person actor for ledger rows that name the satelle user: the account
	// PrincipalID when signed in, the git email when local-only.
	verb.SetActorResolver(func() string { return resolveUser(a.Config, a.RepoRoot).Actor() })
	// The location a working_on row names (sty_40853d02): this machine's hosted
	// location, empty when no server or no bound project. Read from local state
	// only — no network, and nothing is claimed or refused. A story worktree names
	// its main checkout's location (sty_9ece9394).
	verb.SetLocationResolver(func() (string, string) {
		if !verb.RepoBound(a.Config) {
			return "", ""
		}
		return thisLocation(a)
	})
	// Engage precondition (sty_93eec36d): agents.toml + workflow agent= validation
	// before a story leaves its entry state. agents already loaded by requireAgents.
	// Vars are the LAYERED KV (machine-wide catalog [vars] under the repo's own,
	// repo keys winning — sty_c7dfeedf), so a profile that references ${SECRET}
	// resolves from the operator's machine without the value entering the repo.
	verb.SetAgentsConfig(agents, eff.Vars)
	// A task, unlike a story, IS authored substrate: its <data_dir>/tasks/tsk_*.md
	// work-definition file is the source of truth and the store is its index
	// (sty_c1f9e74c). That substrate is the process of record. Sync, migrate,
	// and runtime do not go through SetTaskDir; they keep the location DataDir.
	verb.SetTaskDir(filepath.Join(a.ProcessDataDir, "tasks"))
	// Wire the flat-file operation log (runtime logs/operations.log): a plain-text
	// mirror of state-mutating verbs that a read-only reviewer can scan to verify a
	// DB change the SQLite store hides from it (sty_be257fef).
	verb.SetOpLog(oplog.New(a.RuntimeDir, logRotation(a)))
	// Wire the isolated reviewer that gates status transitions. The agent CLI is
	// the install-time choice (global config); the gate is inert until a
	// workflow names a reviewer skill whose rubric is installed.
	if gc, gerr := config.LoadGlobal(); gerr == nil {
		if runner, rerr := agentcli.NewRunner(gc.Agent.ResolveCLI()); rerr == nil {
			rev := agentstep.New(runner, a.Store.DocIndex, a.RepoRoot, "")
			rev.SetLogDir(filepath.Join(a.RuntimeDir, "logs"), logRotation(a))
			// A gated transition legitimately blocks for minutes while the nested
			// reviewer runs — emit progress to stderr so it is visibly distinct from
			// a hang (sty_6c88ca10). stderr keeps stdout's JSON payload clean.
			// An agent-facing call keeps it off the agent's stream (sty_c4b92c9e):
			// the sink is stderr only for a person at a terminal.
			rev.SetProgress(gateProgressSink(a.Config, a.RepoRoot, a.RuntimeDir))
			// Queryable gate progress on the engagement lease (sty_598a8e1b).
			// Best-effort: a SetActivity failure must never fail a transition.
			// A gate round's stamp is also pushed to the local serve mirror, so
			// the story list's progress chip counts the round's gates as each
			// returns (sty_216d5205); a reviewer session emits no dispatch detail
			// that would push it otherwise.
			leases := a.Store.Leases
			pushEndpoint := gc.Service.ResolveEndpoint()
			rev.SetActivity(func(itemID string, act agentstep.Activity) {
				if leases == nil || itemID == "" {
					return
				}
				_ = leases.SetActivity(context.Background(), itemID, lease.Activity{
					Label: act.Label, Index: act.Index, Total: act.Total,
					Gates: act.Gates, Passed: act.Passed, Failed: act.Failed,
				})
				if act.Gates {
					pushSeatSnapshot(a, pushEndpoint)
				}
			})
			// In-flight dispatch metadata on the SAME lease row (sty_752c4ef2):
			// agent, model, pid, and the last real event, refreshed throttled
			// (agentstep.activityDetailThrottle) rather than once per phase.
			// Pushed to the local serve mirror on the same throttled beat
			// (activityDetailSink) so a running dispatch's web indicator
			// (AC6) and `satelle story seat` (AC7) stay fresh WHILE it runs.
			rev.SetActivityDetail(func(itemID string, d agentstep.ActivityDetail) {
				activityDetailSink(leases, a, pushEndpoint, itemID, d)
			})
			if aerr := applyAgentGrants(rev, a, agents); aerr != nil {
				_ = a.Close()
				return aerr
			}
			rev.SetChildrenResolver(childrenResolver(a))
			// Attachment payload injection (sty_58fa970e): Bash-less reviewers
			// receive plan/step-summary bodies in the transition payload.
			rev.SetDocsResolver(docsResolver(a))
			// In-loop run trace (sty_23ceeca2): the skill-evidence check compares a
			// step the driving session performed with the tool calls its hooks saw.
			rev.SetToolTraceResolver(toolTraceResolver())
			// Prior-verdict injection (sty_0f5e600c): a re-reviewed edge carries
			// what it already judged, so the gate judges the delta.
			rev.SetPriorVerdictsResolver(priorVerdictsResolver())
			// A performer run at the step it is in answers the exit that rejected it.
			rev.SetExitRejectResolver(verb.LatestExitReject)
			// Definition-edit injection (sty_5262592e): the intent reviewer judges
			// each edited acceptance criterion against the story's purpose.
			rev.SetDefinitionEditsResolver(definitionEditsResolver())
			// Rework-relay deferrals (sty_83620b39): an objection the consult seat
			// deferred to a later gate reaches that gate's payload.
			rev.SetDeferralsResolver(func(ctx context.Context, itemID string) []agentstep.DeferredItem {
				return agentstep.DeferralsFrom(verb.StoryLedgerKind(ctx, itemID, ledger.KindAgentInvocation))
			})
			// Developer waivers (sty_bef97244): a gate is handed the waivers the
			// developer recorded for this story against its own skill.
			rev.SetWaiversResolver(waiversResolver(a))
			// Finding answers (sty_f948d345): a gate is told which of the findings
			// its edge's performer was offered went unanswered.
			rev.SetFindingAnswersResolver(func(ctx context.Context, itemID, from, to string) (agentstep.FindingAnswers, bool) {
				return agentstep.FindingAnswersFrom(verb.StoryLedgerKind(ctx, itemID, ledger.KindAgentInvocation), from, to)
			})
			// Engagement-diff injection (sty_a125b440): Bash-less reviewers
			// receive the live slice in the transition payload. Enumeration
			// only; a missing baseline is a marker, never a refused gate.
			rev.SetDiffResolver(diffResolver())
			// Plan-citation excerpts: the tree a gate judges, resolved without
			// requiring the engagement baseline the readiness edge lacks.
			rev.SetCitationTreeResolver(func(ctx context.Context, itemID string) (string, error) {
				return verb.StoryGateTree(verb.WithGateStory(ctx, itemID), itemID)
			})
			// Ranked diff compressor (sty_918e2086): noise-strip then ranked hunk
			// selection are the gate payload's PRIMARY patch reducer; the backstop
			// offloader keeps the diffPayloadCeiling ceiling's overflow recoverable
			// even when the compressor alone was not enough (or is unwired).
			rev.SetDiffCompressor(diffCompressor(a))
			rev.SetDiffOffloader(diffOffloader(a))
			// Functional-check log compressor (sty_ef930f81): a failing check's
			// reject notes get the same ranked-keep treatment a gate payload's
			// patch does, through the SAME retrieval store.
			rev.SetCheckLogCompressor(checkLogCompressor(a))
			rev.SetMessagesResolver(messagesResolver())
			rev.SetMessagesDelivered(messagesDelivered())
			rev.SetArtifactAttacher(verb.AttachItemDoc)
			// Structured retry/failure/timeout telemetry (sty_b73c3236): the engine
			// sees each dispatch ATTEMPT (a killed/timed-out subprocess) the verb
			// layer never does, so it writes those events itself via this sink.
			rev.SetTelemetry(func(ctx context.Context, storyID, actor, kind string, data map[string]any) {
				_ = verb.AppendTelemetryKeepRow(ctx, storyID, actor, kind, data)
			})
			verb.SetTransitionGater(rev)
			// Stamp the governing workflow on every story at create — independent of
			// create-gating (sty_3800ac23).
			verb.SetWorkflowResolver(rev)
			// Named-agent executor dispatch (sty_fd427546): a workflow node's
			// agent=<name> allocation runs that binding's harness at the transition.
			// agents.toml defines WHO, the DOT defines WHERE, the binary only runs it.
			rev.SetNamedAgents(agents.NamedBinding)
			// A binding's declared placement (sty_7028d996): remote and team:<path>
			// reach satelle.dev when signed in; otherwise such a step refuses.
			wirePlacement(rev)
			// Model selection (sty_7069bced): the session-model resolver feeds
			// config.SelectModel's inherited/creator tiers. Without it, tiers 3
			// and 4 of the precedence never apply — every dispatch with no
			// explicit/step/agent model falls straight to cli-default.
			rev.SetSessionModelsResolver(verb.SessionModels)
			rev.SetInvocationRecorder(verb.AppendAgentInvocation)
			rev.SetSuiteEvidence(verb.RecordCheckRun, verb.FindCheckRun)
			// Leftover-file sweep config (sty_e7aaf8b1): what a coder/driving
			// session leaves in the tree that this repo wants moved to scratch
			// (or flagged) at session close. Unset by default — the binary ships
			// no opinion about what a leftover looks like.
			rev.SetLeftoverRule(a.ProcessConfig.Dispatch.Leftovers)
			// A live session (the rework relay's coder seat and
			// rework.consult binding) resolves an unset interface= to the
			// binding CLI's best live transport, not always command
			// (epic:model-selection child 2) — the seam agent validate shares.
			rev.SetLiveNamedAgents(agents.LiveBinding)
			// Rate-limit secondary failover (sty_5bf61f89): per-binding secondary=
			// or [defaults] secondary names a fallback binding for one retry.
			agentsCfg := agents
			rev.SetSecondaryResolver(func(section string, b config.AgentBinding) (config.AgentBinding, string, bool) {
				return agentsCfg.ResolveSecondary(section, b)
			})
			// Idle-stall bound resolution (sty_752c4ef2): a binding's own
			// idle_timeout= wins over [defaults] idle_timeout, which wins over
			// the shipped default — the same ladder listSeatsJSON and
			// `satelle story seat` already display, so a gate, dispatch,
			// retrospective or summary actually stalls where the web/CLI say
			// it will.
			rev.SetIdleTimeoutResolver(func(section string, b config.AgentBinding) (time.Duration, error) {
				return agentsCfg.ResolveIdleTimeout(b, agentstep.DefaultIdleTimeout)
			})
			// CPU-liveness cap for a silent command-transport run (sty_db62a3b9),
			// same binding → [defaults] → shipped-default ladder.
			rev.SetBusyTimeoutResolver(func(section string, b config.AgentBinding) (time.Duration, error) {
				return agentsCfg.ResolveBusyTimeout(b, config.DefaultBusyTimeout)
			})
			verb.SetExecutorDispatcher(rev)
			// The retrospective dispatcher (sty_b53730e2): `satelle story retrospect`
			// runs the [retrospective] agent over a finished story to file proposals.
			verb.SetRetrospector(rev)
			// The amendment gate (sty_81aa4d8f): `satelle story amend` runs the
			// skill the workflow's amend_review hook declares. Always wired — a repo
			// that declares no such hook still refuses the amendment (the engine
			// returns ungated and the verb treats an unjudged amendment as refused),
			// so wiring it never lifts a freeze on its own.
			verb.SetAmendReviewer(rev)
			// Point a structure-guard refusal at the story the indexer already
			// raised about the document that will not parse (sty_88d40a60). The
			// engine stays store-free; this is the injection.
			storyStore := a.Store.Stories
			rev.SetTrackingStoryResolver(func(ctx context.Context, kind, name string) string {
				return docstory.IDForDoc(docstory.Open(ctx, storyStore.List), kind, name)
			})
			// The summariser recaps gated transitions; inert until gating is active.
			verb.SetStepSummariser(rev)
			// Create-gating is opt-in per repo (satelle.toml [review] gate_create):
			// the rubric ships embedded, but enforcing it is the operator's choice.
			// Always set (or clear) so a prior command that left the package-global
			// wired cannot leak into an ungated repo in the same process.
			if a.ProcessConfig.Review.GateCreate {
				verb.SetCreateReviewer(rev)
			} else {
				verb.SetCreateReviewer(nil)
			}
		}
	}
	ctx := context.WithValue(cmd.Context(), appCtxKey{}, a)
	if drain != nil {
		ctx = context.WithValue(ctx, uiDrainCtxKey{}, drain)
	}
	cmd.SetContext(ctx)
	return nil
}

// closeAppForCmd drains pending UI pushes (if any), then closes the bootstrap
// stashed on the command context. Safe to call twice (error path + PostRun):
// drain is once-guarded; close clears the app from context.
func closeAppForCmd(cmd *cobra.Command) {
	if cmd == nil {
		return
	}
	// Drain BEFORE close so snapshot build still reads open stores (AC2).
	if d, ok := cmd.Context().Value(uiDrainCtxKey{}).(*uiDrain); ok && d != nil {
		d.flush()
	}
	if a, ok := cmd.Context().Value(appCtxKey{}).(*app.App); ok && a != nil {
		_ = a.Close()
		// Clear so a second closeAppForCmd (ExecuteC cleanup after PostRun) is a no-op.
		cmd.SetContext(context.WithValue(cmd.Context(), appCtxKey{}, (*app.App)(nil)))
	}
}

// appFrom returns the opened *app.App from the command context. It is present
// for any command carrying the storeAnnotation (the pre-run opened it).
func appFrom(cmd *cobra.Command) (*app.App, error) {
	a, ok := cmd.Context().Value(appCtxKey{}).(*app.App)
	if !ok || a == nil {
		return nil, fmt.Errorf("internal: store not initialised for %q", cmd.CommandPath())
	}
	return a, nil
}

// thisLocation is this machine's hosted location for a's checkout: its persisted
// id and its label. A linked git worktree is the same checkout as its main tree,
// so both resolve through config.CanonicalRepoRoot — a story engaged from a
// `satelle story worktree` tree names the location the main tree registered, not
// none (sty_9ece9394). Every reader and writer of "this machine's location" goes
// through here so they agree.
func thisLocation(a *app.App) (id, label string) {
	root := config.CanonicalRepoRoot(a.RepoRoot)
	return hosted.KnownLocationID(root), hosted.LocationLabel(root)
}

// engineForCmd builds a agentstep.Engine over the opened store and the install-time
// agent CLI — the concrete reviewer used by the read paths (the per-noun `satelle <noun> validate`,
// `satelle <object> create`) that need structure verdicts directly.
func engineForCmd(cmd *cobra.Command) (*agentstep.Engine, *app.App, error) {
	a, err := appFrom(cmd)
	if err != nil {
		return nil, nil, err
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		return nil, nil, err
	}
	runner, err := agentcli.NewRunner(gc.Agent.ResolveCLI())
	if err != nil {
		return nil, nil, fmt.Errorf("an agent CLI is required: %w", err)
	}
	rev := agentstep.New(runner, a.Store.DocIndex, a.RepoRoot, "")
	rev.SetLogDir(filepath.Join(a.RuntimeDir, "logs"), logRotation(a))
	// Same prior-verdict injection as the transition gater (sty_0f5e600c): this
	// read-path engine gates `create` and the per-noun validates too. It
	// deliberately wires no docs resolver — that omission is this site's own
	// question, out of scope here.
	rev.SetPriorVerdictsResolver(priorVerdictsResolver())
	rev.SetExitRejectResolver(verb.LatestExitReject)
	rev.SetMessagesResolver(messagesResolver()) // create-path engine; harmless, never fills a transition payload
	// The store an oversized diff's overflow is kept in: the rework relay opens
	// its sessions on this engine.
	rev.SetDiffOffloader(diffOffloader(a))
	eff, err := requireAgents(a)
	if err != nil {
		return nil, nil, err
	}
	if err := applyAgentGrants(rev, a, eff.Agents); err != nil {
		return nil, nil, err
	}
	rev.SetNamedAgents(eff.Agents.NamedBinding)
	rev.SetLiveNamedAgents(eff.Agents.LiveBinding)
	rev.SetOneShotAgents(eff.Agents.OneShotBinding)
	rev.SetSessionModelsResolver(verb.SessionModels)
	rev.SetInvocationRecorder(verb.AppendAgentInvocation)
	rev.SetSuiteEvidence(verb.RecordCheckRun, verb.FindCheckRun)
	rev.SetLeftoverRule(a.ProcessConfig.Dispatch.Leftovers)
	// The live sessions this engine opens (the rework relay) ledger
	// a denied ask-the-user through this sink (sty_ff50f788); unwired, the row
	// is silently dropped.
	rev.SetTelemetry(func(ctx context.Context, storyID, actor, kind string, data map[string]any) {
		_ = verb.AppendTelemetryKeepRow(ctx, storyID, actor, kind, data)
	})
	return rev, a, nil
}

// logRotation builds the shared flat-log rotation config from the repo's resolved
// satelle.toml (logs_max_size_kb / logs_max_files), used for operations.log and
// reviewer.log alike (sty_a67e6e8c).
func logRotation(a *app.App) logfile.Config {
	return logfile.Config{
		MaxSizeBytes: a.Config.ResolveLogsMaxSizeBytes(),
		MaxFiles:     a.Config.ResolveLogsMaxFiles(),
	}
}

// childrenResolver lists a container's member stories (id + status) from the DB,
// for the container close gate's payload — so a parent/epic close is judged from
// the database, never an on-disk story mirror (sty_fa1e02e1). Membership is
// epicset's (sty_9f4f8e12): the epic: tag set for an epic-parent, parent_id links
// for any other container. The result is a snapshot hint; the close re-reads.
func childrenResolver(a *app.App) func(ctx context.Context, item workitem.Item) []agentstep.ChildState {
	return func(ctx context.Context, item workitem.Item) []agentstep.ChildState {
		if item.ID == "" {
			return nil
		}
		kids, err := epicset.Members(ctx, a.Store.Stories, item)
		if err != nil {
			return nil
		}
		out := make([]agentstep.ChildState, 0, len(kids))
		for _, k := range kids {
			out = append(out, agentstep.ChildState{ID: k.ID, Status: k.Status})
		}
		return out
	}
}

// docsResolver lists an item's attachments for transition-payload injection
// (sty_58fa970e) — plan/step summaries ride in the payload so Bash-less
// reviewers need no disk path. verb.ItemDocs uses the package-global storyDir
// wired by SetStoryDir above.
func docsResolver(a *app.App) func(ctx context.Context, itemID string) []agentstep.DocState {
	return func(ctx context.Context, itemID string) []agentstep.DocState {
		if itemID == "" {
			return nil
		}
		docs, err := verb.ItemDocs(ctx, itemID)
		if err != nil {
			return nil
		}
		out := make([]agentstep.DocState, 0, len(docs))
		for _, d := range docs {
			out = append(out, agentstep.DocState{
				Name:        d.Name,
				Type:        d.Type,
				Body:        d.Body,
				Path:        d.Path,
				Binary:      d.Binary,
				ContentType: d.ContentType,
				Size:        d.Size,
				SHA256:      d.SHA256,
			})
		}
		return out
	}
}

// priorVerdictsResolver lists the verdicts already recorded on the edge under
// review for transition-payload injection (sty_0f5e600c), so a re-review judges
// the delta instead of re-reading the artefact with no memory. verb.PriorVerdicts
// reads the package-global ledger store wired above; a read failure degrades to
// no prior verdicts and never fails the transition.
func priorVerdictsResolver() func(ctx context.Context, itemID, from, to string, ex verb.StaleExemption) []agentstep.PriorVerdict {
	return func(ctx context.Context, itemID, from, to string, ex verb.StaleExemption) []agentstep.PriorVerdict {
		if itemID == "" {
			return nil
		}
		verdicts, err := verb.PriorVerdicts(ctx, itemID, from, to, ex)
		if err != nil || len(verdicts) == 0 {
			return nil
		}
		out := make([]agentstep.PriorVerdict, 0, len(verdicts))
		for _, v := range verdicts {
			out = append(out, agentstep.PriorVerdictFrom(v))
		}
		return out
	}
}

// waiversResolver lists the developer waivers recorded for a story, for
// gate-payload injection (sty_bef97244). An unreadable ledger degrades to no
// waivers and never fails the transition.
func waiversResolver(a *app.App) func(ctx context.Context, itemID string) []agentstep.WaiverItem {
	return func(ctx context.Context, itemID string) []agentstep.WaiverItem {
		ws, err := waiver.List(ctx, a.Store.Ledger, itemID)
		if err != nil {
			return nil
		}
		out := make([]agentstep.WaiverItem, 0, len(ws))
		for _, w := range ws {
			out = append(out, agentstep.WaiverItem{
				ID: w.ID, Gate: w.Gate, Check: w.Check, Reason: w.Reason, Actor: w.Actor,
				CreatedAt: w.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
		return out
	}
}

// definitionEditsResolver lists the definition edits recorded for a story, for
// transition-payload injection (sty_5262592e). verb.DefinitionEdits reads the
// package-global ledger store wired above; an unreadable ledger degrades to no
// edits and never fails the transition.
func definitionEditsResolver() func(ctx context.Context, itemID string) []agentstep.DefinitionEdit {
	return func(ctx context.Context, itemID string) []agentstep.DefinitionEdit {
		if itemID == "" {
			return nil
		}
		edits := verb.DefinitionEdits(ctx, itemID)
		if len(edits) == 0 {
			return nil
		}
		out := make([]agentstep.DefinitionEdit, 0, len(edits))
		for _, e := range edits {
			out = append(out, agentstep.DefinitionEdit{Field: e.Field, Old: e.Old, New: e.New, Actor: e.Actor, At: e.At})
		}
		return out
	}
}

// diffResolver enumerates the live engagement slice for reviewer-payload
// injection (sty_a125b440). verb.StoryDiff is the same derivation as
// `satelle story diff`; ANY error (no baseline, empty sha, foreign tree, git
// unavailable) becomes a no-baseline marker so the transition cannot fail on
// enumeration. It runs for a gate judging itemID, so it reads that story's own
// tree from whichever tree the transition is presented (verb.WithGateStory).
func diffResolver() func(ctx context.Context, itemID string) *agentstep.DiffState {
	return func(ctx context.Context, itemID string) *agentstep.DiffState {
		if itemID == "" {
			return &agentstep.DiffState{NoBaseline: true, Note: "empty item id"}
		}
		res, err := verb.StoryDiff(verb.WithGateStory(ctx, itemID), itemID, verb.StoryDiffOpts{Patch: true, IncludeSubstrate: true})
		if err != nil {
			return &agentstep.DiffState{NoBaseline: true, Note: err.Error()}
		}
		return &agentstep.DiffState{
			Baseline: res.Baseline,
			Dirty:    res.DirtyAt,
			Files:    res.Files,
			Stat:     res.Stat,
			Patch:    res.Patch,
			Note:     res.Note,
			Source:   res.Source,
		}
	}
}

// diffCompressor wires the ranked diff compressor (sty_918e2086) a gate
// payload runs on its patch before diffPayloadCeiling: order-3 noise
// stripping (CompactPatch, cfg.NoisePatterns) always runs first, then order-4
// ranked hunk selection (RankPatch) when [output.diff_rank] is enabled. Both
// offload what they drop through the SAME retrieval store every other
// offload/retrieve path in this codebase shares (retrieveAdapter), keyed to
// itemID so retention/pruning covers it.
func diffCompressor(a *app.App) func(ctx context.Context, itemID, patch string) string {
	return func(ctx context.Context, itemID, patch string) string {
		if a.Store == nil || a.Store.Retrieve == nil {
			return patch
		}
		off := retrieveAdapter{ctx: ctx, store: a.Store.Retrieve, storyID: itemID}
		out := compact.CompactPatch(patch, a.Config.Output.NoisePatterns, off)
		if !a.Config.Output.DiffRank.Enabled {
			return out
		}
		return compact.RankPatch(out, a.Config.Output.DiffRank.Resolve(), off)
	}
}

// diffOffloader wires the store fillDiff's ceiling backstop uses to keep an
// oversized patch's overflow tail recoverable (sty_918e2086) — the same
// retrieval store diffCompressor's offloads share, so a marker either one
// leaves resolves through the same `satelle retrieve <hash>` path.
func diffOffloader(a *app.App) func(ctx context.Context, itemID string, content []byte) (string, error) {
	return func(ctx context.Context, itemID string, content []byte) (string, error) {
		if a.Store == nil || a.Store.Retrieve == nil {
			return "", fmt.Errorf("diff offloader: no retrieval store wired")
		}
		return retrieveAdapter{ctx: ctx, store: a.Store.Retrieve, storyID: itemID}.Put(content)
	}
}

// checkLogCompressor wires the log compressor (sty_ef930f81) runCheck uses to
// build a failing functional check's reject notes, through the SAME
// retrieval store every other offload/retrieve path in this codebase shares
// (retrieveAdapter), keyed to itemID so retention/pruning covers it.
func checkLogCompressor(a *app.App) func(ctx context.Context, itemID, log string) string {
	return func(ctx context.Context, itemID, log string) string {
		if a.Store == nil || a.Store.Retrieve == nil {
			return compact.CompressLog(log, a.Config.Output.CheckLog.Resolve(), nil)
		}
		off := retrieveAdapter{ctx: ctx, store: a.Store.Retrieve, storyID: itemID}
		return compact.CompressLog(log, a.Config.Output.CheckLog.Resolve(), off)
	}
}

// messagesResolver injects engagement-windowed agent messages into gate and
// executor payloads (sty_2db624d0). Never fails the transition.
func messagesResolver() func(ctx context.Context, itemID string, addresses []string) []agentstep.MessageState {
	return func(ctx context.Context, itemID string, addresses []string) []agentstep.MessageState {
		raw := verb.MessagesSince(ctx, itemID, addresses)
		if len(raw) == 0 {
			return nil
		}
		out := make([]agentstep.MessageState, len(raw))
		for i, m := range raw {
			out[i] = agentstep.MessageState{
				ID:            m.ID,
				From:          m.From,
				To:            m.To,
				Body:          m.Body,
				CreatedAt:     m.CreatedAt.UTC().Format(time.RFC3339),
				EngagementSHA: m.EngagementSHA,
				State:         m.State,
			}
		}
		return out
	}
}

// messagesDelivered marks the messages a performer's dispatch injected delivered
// (sty_73c9aa10), through the same in-process verb `satelle story message-mark`
// runs. A mark that fails does not stop the rest; the errors come back joined.
func messagesDelivered() func(ctx context.Context, itemID string, msgIDs []string) error {
	return func(ctx context.Context, itemID string, msgIDs []string) error {
		var errs []error
		by := config.ResolveSession()
		for _, id := range msgIDs {
			if _, err := verb.StoryMessageMark(ctx, verb.MarkRequest{
				ID: itemID, Message: id, State: string(board.Delivered), By: by,
			}); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}

// skillResolver returns a predicate reporting whether a skill name resolves in
// the substrate (project ∪ embedded), for the deterministic workflow structure
// check's executor-skill actionability. Used by the per-noun `satelle <noun> validate` and `reindex`.
func skillResolver(a *app.App) func(skill string) bool {
	return func(skill string) bool {
		_, err := a.Store.DocIndex.Get(context.Background(), "skills", skill)
		return err == nil
	}
}

// requireAgents loads the EFFECTIVE agents layer for an INITIALIZED repo and
// refuses when it is broken (sty_d0d6bb67): a malformed file, or the retired
// actors.toml with no agents.toml. An absent agents.toml is NOT broken: the
// embedded baseline seats run and a repo file names only the seats it changes
// (sty_6602bb44). The error names the file and the fix.
//
// "Effective" means the repo file folded with the machine-wide profile catalog
// by the documented precedence (sty_c7dfeedf) — resolved through the single
// config.LoadEffectiveAgents site, never merged here. A repo that references no
// profile resolves exactly as before, catalog present or not.
func requireAgents(a *app.App) (config.EffectiveAgents, error) {
	// agents.toml is authored substrate — always under DataDir, never RuntimeDir
	// (sty_4660bbe1: the DB leaving the repo must not take the agents layer with it).
	dataDir := a.PlaneDir()
	// agents.toml lives inside the workflows dir. When that dir exists and cannot
	// be read, the cause is the dir, not the file: say so by name, and say that the
	// embedded route would otherwise govern, rather than blaming a broken
	// agents.toml (sty_d6e209aa).
	if wf := a.AuthoredDirs().WorkflowsDir(); wf != "" {
		if state, perr := docindex.ProbeDir(wf); state == docindex.DirUnreadable {
			return config.EffectiveAgents{}, fmt.Errorf("%w: %s",
				wfgovern.ErrAuthoredProcessUnreadable, wfgovern.UnreadableMessage(wf, perr.Error()))
		}
	}
	// AgentsPath prefers the canonical workflows/ location and falls back to the
	// legacy one, so an unconverted repo still runs (sty_10f732ed). The message
	// names the CANONICAL path — where the file belongs, not where it used to be.
	rel := config.DefaultDataDir + "/" + config.AgentsRel
	agentsPath, _ := config.AgentsPath(dataDir)
	if _, err := os.Stat(agentsPath); os.IsNotExist(err) {
		if _, lerr := os.Stat(filepath.Join(dataDir, config.ActorsConfigName)); lerr == nil {
			return config.EffectiveAgents{}, fmt.Errorf(
				"missing %s but found the retired %s/%s — rename it to %s (the legacy filename is no longer loaded)",
				rel, config.DefaultDataDir, config.ActorsConfigName, config.AgentsConfigName)
		}
		// No repo file: the embedded baseline seats run (sty_6602bb44).
	}
	eff, err := config.LoadEffectiveAgents(dataDir, a.PlaneConfig().Vars)
	if err != nil {
		return config.EffectiveAgents{}, fmt.Errorf("broken %s: %w — fix it, or delete it and run `satelle init` to reseed the default", rel, err)
	}
	// Resolve every binding's env ${VAR} against the LAYERED [vars] KV ONCE, here
	// at load — fail-fast, the same style as the broken-file refusal above: an
	// unknown var in any binding refuses the command with an actionable message
	// rather than dispatching an agent with a blank credential later (sty_001558ce).
	resolved, err := config.ResolveAgentEnvs(eff.Agents, eff.Vars)
	if err != nil {
		return config.EffectiveAgents{}, fmt.Errorf("%s: %w", rel, err)
	}
	eff.Agents = resolved
	return eff, nil
}

// planeWiringGuard is the dispatch wiring guard for a: built from the process
// of record (the main tree's configuration), never the invoking worktree's own.
func planeWiringGuard(a *app.App) agentstep.WiringGuard {
	return agentstep.WiringGuardFrom(a.PlaneConfig())
}

// applyAgentGrants binds the loaded agents layer onto the engine: the reviewer's
// binding (tools/model/env/principles/role), constitution order-zero, and harness.
// A broken harness value is an error — the configuration executes as defined or
// refuses (sty_d0d6bb67).
func applyAgentGrants(rev *agentstep.Engine, a *app.App, agents config.AgentsConfig) error {
	// The per-executable [model_order] feeds config.SelectModel's order tier
	// (sty_4fde0a50); every engine that resolves a model gets it here.
	rev.SetModelOrder(agents.OrderFor)
	// The wiring guard reads the main tree's configuration, never the
	// invoking worktree's own, so a worktree cannot exempt itself
	// (sty_f141c77f).
	if a != nil {
		rev.SetWiringGuard(planeWiringGuard(a))
	}
	rb := agents.ReviewerBinding()
	// Store the whole binding as the single resolution shape for Invoke
	// (sty_ba860c8a); scalar caches are synced from it.
	rev.SetReviewerBinding(rb)
	// Project constitution rides order-zero in isolated briefings whenever
	// principles ≠ none (design §5.3) — SessionStart parity with cmd_hook.
	if a != nil {
		rev.SetConstitution(readConstitution(a.PlaneConstitution()))
	}
	// Select the reviewer's agent CLI from the agents-layer command binding
	// (default claude). An unset/in-loop command keeps the global [agent] cli
	// configured at construction; an unresolvable one refuses.
	r, err := agentcli.RunnerFromBinding(rb.ResolvedInterface(), rb.CommandTemplate())
	if err != nil {
		return fmt.Errorf("broken %s/%s: reviewer command: %w",
			config.DefaultDataDir, config.AgentsRel, err)
	}
	if r != nil {
		rev.SetRunner(r)
	}
	return nil
}
