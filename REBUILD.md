# satelle rebuild contract

The brief for rebuilding satelle back to its kernel. It is the rebuild's prompt and
its first committed file, and it is deleted when the rebuild merges. Nothing reads
it after that.

## What satelle is

satelle is a stateless, generic, non-process CLI. It lets a user define steps and
reviewers, which use skills, so that **their** process is carried out by agents.
The binary runs configuration. It never holds a process.

The test for every line of Go: **does it name or decide something only one
workflow has?** If yes, it is process and does not belong in the binary.

## Why

The kernel below is why satelle exists, and it worked. Complexity killed it.

| | v0.0.122 (2026-07-05) | now (v0.0.896) | after the rebuild |
|---|---|---|---|
| internal packages | 19 | 76 | 30 |
| Go, non-test | ~18k lines | ~180k lines | ~30k lines (~25k hand-written; 5.4k is generated protobuf) |
| Go tests | ~20k lines | ~279k lines | ~14k lines (unit and the `tests/` integration suite) |

Growth has been about 1.4k lines a day with no plateau. v0.0.122's package set
(`agentcli agentstep app arbor buildinfo cli config docindex help hosted ledger
logfile oplog store structure verb web wfdot workitem workspace`) is a reference
for the kernel's size, not a revert target: later work on the ledger, sync,
satelled and remotes is mechanism worth keeping.

## The kernel: what the binary may know

1. **Work item (story).** id, title, body, acceptance criteria, status, tags,
   parent/child. Create, update, get, list.
2. **Workflow shape.** A workflow is states and transitions read from
   `.satelle/workflows/`. Configuration marks the start state, the terminal
   states and the park states (resuming or not). **No state name appears in Go.**
3. **Gates.** A transition carries gates. A gate dispatches a skill to a reviewer
   agent, which returns a verdict (accept/reject plus notes). A deterministic
   check (a skill with a script block) is a generic script run with the same
   verdict shape. Verdicts go on the ledger.
4. **Performers.** A state may carry a performer: an agent plus a skill.
5. **Agents.** A catalog of preset agents (command, model, tool grants). The
   orchestrator may choose among presets. **Permissions are declared up front and
   enforced by tool grants, never by prompt text. Reviewer agents are read-only.**
   A generic, configurable rule: a reviewer may not run the same model as the
   performer whose work it judges.
6. **Ledger.** An append-only, hash-chained record of every event, in a
   **generic** format: an event names its item, its kind and its payload, and
   knows nothing of any workflow. It is the source of truth. The current ledger
   is reference material, not a constraint: the format may be rewritten. Existing
   stores are archived as they are (moved aside, untouched), and the rebuilt
   binary starts a fresh ledger. Superseded by the operator ruling that
   satelle provides the way across: `satelle upgrade --archive` archives a
   pre-1.0 .satelle and starts v1, and `satelle upgrade --from` imports the
   operator's other repos' story ledgers by a confirmed status map
   (sto_cc74bd5e).
7. **Edit gate and harness hooks.** Generic: an edit is allowed only while a work
   item sits in a performing state allocated to the editing agent. Harness
   adapters (Claude, pi, and grok if present) are thin and agent-agnostic.
   **Cursor (cursor-agent) support is dropped entirely.**
8. **Sync and satelled.** Local store ↔ satelle.dev, and satelled (local serve,
   web UI). satelled stays, and **its interface is maintained** (see the fixed
   boundary). What sits behind it may be rebuilt.
9. **Remotes.** The node contract (below).
10. **help.** The CLI's own contract (what satelle is, how to drive it, what a gate
    and a block mean) is printed by the binary and lives next to the code.
11. **init.** Creates `.satelle/`, writes a **two-state workflow file**
    (`backlog → done`, no gates) as editable configuration the user owns and
    replaces, and validates configuration. A repo with a broken configuration
    refuses to run.

## Not kernel: delete it or move it out

- **Process in Go.** Any Go that names or decides for a particular state, step,
  skill, document, heading, category, workflow, forge flow or policy. Any command
  named after a step (e.g. `satelle pr`, `trunk`, `changelog`, `publish`,
  `release`). A generic setting whose names come only from configuration is fine
  (e.g. `apply_criteria = { doc = …, heading = … }` with **no** default in Go).
- **Git and forge actions in Go.** Commit, push, merge, branch, tag, PR, review,
  status are a step's work, done by an agent under a skill. Read-only git facts
  go through one small adapter.
- **Embedded default process.** `internal/config/substrate` (≈90 markdown files:
  skills, principles, workflows, tasks, remotes) leaves the binary. The binary
  ships only its help text and the init template: the two-state workflow and
  the upgrade skill (operator, 2026-11-06). BDD, story shape, plan
  review and similar practice are this repo's process, kept in this repo's
  `.satelle/` (a future marketplace package, not built now).
- **Documents.** The documents kind leaves satelle; it becomes a separate
  service. Remove the repo documents directory under `.satelle/`, document
  indexing and document sync to satelle.dev, and any command that serves
  documents (see backlog epic `epic:documents-leave-satelle`, sty_3e593f9d).
  **Story attachments stay** (a plan or evidence attached to a story is kernel:
  gates read them). Record the removed document wire under **Wire changes for
  satelle-server**.
- **Backward compatibility.** Migrations for old layouts, deprecated aliases,
  compat shims and dead harness support go. The wire boundary below is the only
  exception.
- **Bloat.** Packages and commands that the kernel does not need (candidates to
  judge, not a verdict: `forge release releasestate trunk fixlane waiver
  placement epicset board throughput reviewscore costview ctxaudit condense
  compact retrieve stats`). Keep one only if a kernel item above needs it, and
  say which.

## The fixed boundary: the wire with satelle-server

satelle-server is rebuilt next, from the results of this rebuild, and the two
branches merge together.

**Maintained (do not change):**

- **satelled's interface:** its API (`api/board.proto` and any HTTP or Connect
  endpoints the web UI and other clients call), its CLI and service entry points,
  and the behaviour a client of that interface relies on. Find the full surface
  during the inventory and list it here.

**May change, recorded as wire changes:**

- the ledger format (`api/story_chain.proto` and the chain), which is being
  rewritten as a generic format;
- `api/checkout_sync.proto` and the payloads sync sends;
- the remote node's join, report and conclude protocol.

**All communication with satelle-server is gRPC, never REST.** Every call the
CLI, satelled or a remote node makes to satelle.dev goes through a service
defined in `api/*.proto`. Each REST/plain-HTTP call that exists today (in
`internal/hosted`, `internal/cli`, `internal/remotenode`, `internal/push` and
elsewhere) is replaced by a gRPC method, and each one is recorded as a wire change
(the endpoint today → the proto service and method that replaces it).

Any change to these is recorded in **Wire changes for satelle-server**, precisely
enough for that repo's rebuild to implement the other side. Sync with satelle.dev
is expected to break on this branch until satelle-server's rebuild lands; tests
use a fake server built from the new wire.

## Remotes

`.satelle/remotes/satelle-cicd/remote.toml` (schema v1: provider, drive, secrets,
setup, network, lifecycle) is the base of the node contract. A node is one image
plus a work item or task, credentials and network rules, and it reports its
setup output, progress, ledger and verdict back.

- A provider's quirks live in its adapter, never in a definition (today's first
  `on_create` line is a Sprites keep-alive workaround; it moves).
- **Local docker is the reference implementation:** the same definition runs as
  a local container and is inspectable with `docker exec`. Sprites is a second,
  thin adapter that starts the same image.

## This repo's own process

`.satelle/` here is this repo's process, written as configuration. Bring it onto
the kernel's configuration schema so it validates against the rebuilt binary.
It must include the plan-stage reviewer of sty_7c477116: it rejects any plan that
adds or extends process in Go, **with no exemption**, runs first and fails fast,
names what each planned Go change decides, and judges the diff again before
integration.

In-flight and backlog stories are not touched. The operator resets them to the
start after the rebuild, as a test of the new process.

## Tests

- Unit tests sit next to kernel code.
- The integration suite (`tests/`, 176 files today) is rewritten around the
  kernel: `init` in an empty directory → a user-defined workflow → create a story
  → gates dispatch a fake agent → verdicts → ledger → terminal state; the edit
  gate; sync against a fake server; a remote node on docker-local (skipped when
  docker is absent).
- Tests of removed behaviour are deleted, not ported.
- **No test talks to satelle.dev.**

## Working rules

1. **Branch first.** Before any edit: `git checkout -b claude/satelle-rebuild`.
   The tracked cloud hook denies edits on any branch not named `claude/satelle-*`.
2. **First commit:** add this file as `REBUILD.md`; untrack `AGENTS.md`,
   `.claude/settings.local.json` and `scripts/claude-cloud-hook.sh`; add
   `AGENTS.md`, `CLAUDE.md` and `.claude/settings*.json` to `.gitignore`.
3. **Never** push to `main`, open a PR, tag, release, or write to satelle.dev.
4. Commit in coherent steps. `go build ./...` and `go vet ./...` pass at every
   commit; `go test ./...` passes at every milestone. Push the branch at each
   milestone so no work is lost.
5. **Go toolchain.** Upgrade to the latest stable Go release (check
   `https://go.dev/dl/?mode=json`; today the repo pins 1.27.0). Bump `go.mod`'s
   `go` line, `mise.toml` and `go-version` in `.github/workflows/*.yml` together
   in one commit, and use new language and standard-library features where they
   simplify the kernel.
6. Keep the sections below current. A ruling you cannot make from this contract
   goes under **Open questions**; do not decide it.

## Done

- `go build ./...`, `go vet ./...` and `go test ./...` pass, including the
  rewritten integration suite.
- An audit (command and output recorded below) shows no Go string literal that
  names a state, step, skill or document of this repo's `.satelle/` workflow.
- `satelle init` in an empty directory writes the two-state workflow, and a story
  created there walks `backlog → done`.
- This repo's `.satelle/` validates against the rebuilt binary.
- The size table above has a third column: after the rebuild.

---

## Inventory

Phase A inventory (2026-11-05, from v0.0.896 at `55e292f`). Each verdict was made
by reading the package's main files and types. Line counts are non-test Go.
"Kept" means the package stays and is cut down to the kernel. The process that
must still leave each kept package is listed under **Cuts inside kept
packages** below the table. "Moved" means its content leaves the binary, or
merges into another package.

### Packages under `internal/`

| package / command | kept · deleted · moved | why (which kernel item, or why not) |
|---|---|---|
| `workitem` (1.3k) | kept | **1**. `Item` struct, sqlite `Store` Create/Get/List/Update with `ExpectStatus` CAS, Upsert for sync, `Marshal`/`Parse`. |
| `ledger` (2.8k) | kept, format rewritten | **6**. Today: `evidence` table, id-linked `base_id` chain (**not** hash-chained; `chain.go VerifyChain`), `workflow_hash` stamped on every row, about 65 hard-coded `Kind*` constants (ledger.go:25–341). Many of those kinds are named after steps or features: `readiness_accept`, `step_summary`, `trunk_publish`, `release_record`, `suite_run`, `queue_enqueued`, `fix_claim`, `gate_waiver`, `substrate_*`, `driver_usage`, `drive-stopped`. The rewrite keeps Append/List/StoryChain/Verify and the sync primitives (`Upsert`, `ListInsertedAfter`, `RebaseEvents`, marks), adds content hashing, and uses a generic `{item, kind, payload}` event. |
| `storyfold` (0.8k) | kept | **6**. `Fold` derives the item row from its chain (pure); `Route{Park,Final}` is generic. |
| `store` (0.2k) | kept | **1/6**. Opens sqlite (WAL, single writer), `InTx`, `OpenReadOnly`. |
| `oplog` | deleted | Flat `logs/operations.log` mirror of mutations; the ledger is the source of truth. Importers: `verb/wiring.go`, `cli/app.go`. |
| `arbor` | deleted | Dead: slog wrapper with zero importers (verified). |
| `app` (0.3k) | kept | **11/8**. Bootstrap: load config, refuse without `.satelle/` (`ErrNotInitialised`), open the store, runtime marker. |
| `workspace` (0.25k) | deleted | Read-only cross-repo aggregation for `stats`/`throughput` and cross-repo `depends-on:` (`verb/dependson_sibling.go`). The satelled Projects page is push-fed and does not import it. |
| `verb` (22.7k, 83 files) | kept, heavily cut | **1, 2, 3, 4, 6, 7**. Kernel files, about 8.4k lines, stripped further: `verb.go`, `helpers.go`, `wiring.go`, `workitem.go` (`workItemSetCore`, the transition state machine: CAS → `runGate` → `recordGateRound` → `performStep` → tx writes row + transition event → lease settle), `review.go` (the `TransitionGater`/`ExecutorDispatcher`/`PerformerResolver` seams, `GateDecision`/`ReviewerVerdict`), `readiness_round.go` (`recordGateRound`, `dispatchPerformer`), `reviewer_dispatch.go`, `rejection.go`, `transition_edge.go`, `foldstate.go`, `ledger.go`, `single_story.go` (engagement lease), `seat.go`, `storydocs*.go`/`storymd.go` (attachments), `skill_delivery.go`, `sessionmodels.go` (performer model, needed for the reviewer≠performer rule), `working_on.go`, `storyhistory.go`. About 14.3k lines are process and are deleted whole; the list follows under Cuts. |
| `drive` (0.9k) | kept | **4/9**. Headless walk of an item along its route (`successor()`, `park()` via `verb.ResumeParkName`); remote nodes run it. It names no state. |
| `wfroute` (0.6k) | kept, slimmed | **2**. Renders a route from `wfdot.Spec` without naming states. satelled's web (`web/workflow.go`, `web/stages.go`) consumes it. |
| `gatehandle` (1.0k) | deleted | Not kernel: detaches a gate run and delivers the verdict later through the Stop hook (`Claim`, `forward.go` cross-repo, `resume.go`). Gates run synchronously. Importers to change: `config/session.go`, `cli/gate{caller,deliver,resume,xrepo}.go`. |
| `wfhook` (0.4k) | deleted | Its whole content is the lifecycle hooks `create_review`/`amend_review` plus `DefaultAgent="reviewer"`. Kernel gates sit on transitions only. |
| `agentstep` (13.6k) | kept, heavily cut | **3, 4, 5**. Gate: `Gate`/`gateEdge` (engine.go:1876/1943), `runCheck` (:5124, the deterministic script check), `parseDecision` (:5417). Performer: `resolvePerformer` (:2455), `DispatchExecutor` (:2625). Agent: `Invoke` (invoke.go), `isolateReviewer` (isolation.go:36), `defaultTools="Read,Grep,Glob"`. About 60 resolver seams on `Engine` serve non-kernel features. |
| `agentcli` (9.5k) | kept, cut | **5/7**. Tool-grant enforcement (`ClassifyTool`, `ReviewerPermissionPolicy` isolation.go:371, `ReadOnlyEnforced`), thin harness adapters (`HarnessHooks`, deny shapes in hook_output.go), Runner and command templates (agentcli.go:66/73, claude `--disallowedTools Write,Edit,NotebookEdit,Bash`), grok, pi RPC. |
| `agentinstall` (0.7k) | deleted | Launcher scripts under `$SATELLE_HOME/agents/bin` (session-name convenience). `cursor_hooks.go` (405 lines) is all cursor. |
| `agentvalidate` (1.7k) | kept, cut | **11/5**. Binding checks (`checkBinding` :1123), reviewer ceiling (`ceilingProblem`), `reviewerEnforced` (:1434). This is where the reviewer≠performer-model rule belongs; it does not exist anywhere today. |
| `agentartifact` (0.8k) | kept, cut | **4/1**. Performer output contract (`output_*` frontmatter keys, artifact.go:73) and attachment validation. |
| `sessionconfig` (0.3k) | deleted | Config digests stored as a `config-bundle` attachment: provenance, not kernel. It still names the `documents` kind (:41). |
| `hostguard` (0.3k) | deleted | Test infrastructure for `scripts/credfp` only. |
| `lease` (1.3k) | kept | **7**. Records which agent holds an item's seat, the "allocated to the editing agent" half of the edit gate (`AcquireWith` :486); it names no state. |
| `worktree` (0.65k) | moved | Read-only facts (`TopLevel`, `CommonDir`, `IsLinked`, `Head`, `Dirty`, `OnRemote`, `CurrentBranch`, `Ignored`) become the **one read-only git adapter**. Write actions are deleted: `Open` (`git worktree add -b`), `Fetch`, `AddExcludes`, `CollectBranch` (merge/--ff-only/--abort), `PushedBranch`, `Carry`. |
| `procgroup` | kept | **5**. Agent process group kill (Setpgid + group SIGKILL); pure mechanism. |
| `runstatus` | kept | **9**. Remote run states queued/running/concluded/stopped; a leaf. |
| `logfile` | kept | Rotating append writer for satelled and dispatch logs (**5/8**). |
| `logsread` | kept | **5**. Dispatch-log naming and reading (dispatch evidence). |
| `buildinfo` | kept | **10/8**. Version identity of satelle and satelled. |
| `testutil` (0.35k) | kept, rewritten | Test support. `route.go baselineGateSkills={"satelle-step-summary","satelle-estimate-actual-review"}` names skills; `trunkrepos.go` serves trunk tests. |
| `fsmove` | deleted | Used only by the agentstep leftover sweep and `story tidy`, both bloat. |
| `filebundle` | kept | **9**. Ships a definition bundle to a node (remotenode/substrate.go:34–57). |
| `textdiff` | deleted | Used only by substrate reconcile (`cmd_sync_reconcile.go:339`, `verb/substratediff.go:78`). |
| `semver` | deleted | Used only by release, changelog and update (`verb/changelog.go`, `cmd_update.go`, `release_advisory.go`). |
| `config` (12.1k + embedded `substrate/`) | kept, cut hard | **5, 7, 8, 11**. Kernel core: `Config`/`Load` (config.go:1168), agents.go (`AgentBinding`, `AgentsConfig`), resolveagents.go, globalagents.go, vars.go, agentstransport.go, modelselect.go, harness.go, grok.go, session.go, instance.go, hosted.go, global.go, sync.go, workspace.go, runtime.go, scratchpath.go, glob.go, satelleignore.go. |
| `config/substrate` (embedded, 72 files, 5,665 lines) | moved | **Not kernel: embedded default process.** It holds 36 skills, 20 principles, 2 workflows, 6 tasks, the `pr-approve` remote, and the tomls `agents.toml` (10 seats), `categories.toml` (15 names), `harness.toml` (with cursor), `condense.toml`, `doctor.toml` and `fix_lane.toml`. Whatever this repo's `.satelle/` still uses moves there (list under Open questions, Q-embedded); the rest is deleted. |
| `substrate` (0.17k) | deleted | Classifies docs as default/edited/authored against the embedded defaults; it has no purpose once nothing is embedded. |
| `substratereview` (1.6k + 26 md) | deleted | The `satelle substrate review` audit. Process: `ApplyCategory="substrate"`, park default `"blocked"` (evidence.go:291), `d.Name=="step"` (inventory.go:176). It may be re-authored as a skill/task in `.satelle/`. |
| `wfdot` (3.0k) | kept | **2, 3, 4**. `ParseDone`/`ParseSteps` (route_parse.go:387/548, strict decoding), `BuildRoute`/`assemble` (route.go), Spec predicates (`Start`, `IsTerminalState`, `IsParkState`, `IsFinal`, `AdvanceOptions`, `NonTerminalEngagingStates`). It holds by-name leaks (see Cuts) and is to be rewritten onto the kernel schema. |
| `wfequiv` (0.4k) | deleted | Migration instrument for retiring DOT; zero importers (verified). |
| `wfgovern` (1.2k) | kept, shrunk | **2**. `SpecFor`/`RouteFor`/`RouteSpecFor` (spec.go:148–297) pick and resolve the governing route for an item. |
| `structure` (0.5k) | kept, shrunk | **11/3**. Config validation; `CheckFence`/`CheckCommand` extract a skill's script block (fence.go:12/40) for deterministic checks. |
| `help` (67 lines + 21 md) | kept | **10**. Generic embed with List/Get. Topics are trimmed to the CLI contract (see Cuts). |
| `docindex` (1.8k) | moved | **Documents leave.** It is a sqlite index plus watcher over authored markdown, and OKF normalisation rewrites files in place (okf.go:209). The kernel needs only frontmatter parsing (`Frontmatter`, `FrontmatterBody`, `MetaTables` in frontmatter.go), which moves into `config`; the rest is deleted. |
| `docstory` (0.16k) | deleted | Auto-raised "system stories" about failing documents; `Qualifies` hard-codes `StatusDone`/`StatusCancelled`. |
| `doctor` (2.2k) | moved | Merged into `satelle validate` (**11**). It keeps `checkBinaries` (:340) and `checkReviewerIsolation` (:391) (**5**); the drift, tier, sync-health, context and live-probe checks are deleted. |
| `health` (0.3k) | kept, shrunk | **11**. Finding vocabulary (`Severity`, `Finding`). About 30 of its 61 `ID*` constants belong to removed features. |
| `tasktrigger` (0.1k) | deleted | Schedule/count triggers that create and drive task executions. Not kernel (but see the tasks question). |
| `hosted` (7.5k) | kept, rewritten onto gRPC | **8/9**. Single client for satelle.dev: client.go, client_sync.go (gRPC Sync), client_chain.go (gRPC Chain), oauth/discovery/credstore (login), session.go (`SATELLE_TOKEN`), location.go, client_apierror.go (`SignedDo`, node transport). Every REST method becomes gRPC (see Wire changes). |
| `hosted/boardpb` | deleted | Generated stubs for `api/board.proto`. No client code calls them. |
| `push` (0.14k) | kept | **8**. CLI→satelled change doorbell (`POST /ingest/change`, fire-and-forget, 500ms). |
| `mirror` (1.1k) | kept | **8**. satelled's store (sqlite partitions/items/docs under `~/.satelle/serve`) and ingest (`/ingest/change`, `/ingest/snapshot`, `/ingest/remove`). |
| `serve` (1.8k) | kept | **8**. satelled runtime: `Run`, `Pusher` (hostedpush.go), `Reconciler`, `children.go` (execs sibling `satelle` verbs). |
| `web` (4.5k) | kept | **8**. satelled web UI; its interface is maintained (see the boundary list). |
| `board` (0.3k) | deleted | Folds `agent_message`/`message_mark` rows into message states. It is on the bloat list, and agent messaging is not kernel. |
| `snapsync` (2.2k) | kept, provisional | **8**, if `.satelle` config sync stays (Q-config-sync). Snapshot records and a three-way plan; it names no area itself. |
| `syncstate` (0.5k) | kept | **8**. Durable sync outcome in `~/.satelle/<key>/workstate-sync.json`, shared by satelled, doctor and web. |
| `syncscope` (0.13k) | kept | **8**. Single owner of local/personal/team scope (`Resolve` → Me, GetProject, Workspaces). |
| `subsync` (0.3k) | kept with snapsync | **8**. Byte-exact restore and write-aside of synced config files. |
| `remotenode` (2.8k, + devtest, gittest) | kept | **9**. Node half of the node contract: protocol.go (envelope, `Report`), remote.go (controller `Attach`/`Run`), serve.go (`Node.Serve`), nodeapi.go (`NodeClient`), fetch.go (read-only fenced clone; push URL set to `refused://`), setup.go (`RunSetup`), substrate.go (`InstallDefinition`), taskrun.go (heartbeat/conclude), vitals.go, swap.go. |
| `remotechan` (2.1k) | kept | **9**. Sealed performer channel: X25519+HKDF+AES-GCM over a WebSocket relay (open.go:518 `dialRelay`), enrolment tokens, job heartbeats, resume. It names no process. |
| `remotedef` (1.2k) | kept | **9**. remote.toml v1 schema (`Definition`, remotedef.go:31), `Parse`/`Validate` (mirrors the server's validator), secrets hand-off file. |
| `remotesign` (0.1k) | deleted | Dead: Ed25519 request signer with zero importers (verified); nodes authenticate with a bearer node token. |
| `secretref` (0.17k) | deleted | Resolves `infisical://` through the infisical CLI, a backend remotedef already calls retired. Call sites to remove: agentstep/engine.go:1065, scratch.go:114. |
| `forge` (+`forge/github`) (0.85k) | deleted | **Forge actions in Go**: `Forge.Approve/Comment/SetStatus/PullRequestForHead`; `RepoConstitution="repo-constitution"`; GitHub REST. |
| `release` | deleted | Self-update download (`Repo="bobmcallan/satelle"`); only `satelle update` imports it. |
| `releasestate` | deleted | Release-check cache for `update` and the SessionStart release advisory. |
| `trunk` (0.8k) | deleted | **Git writes plus a step-named command**: `git merge --no-edit` (publish.go:143), `git push <remote> HEAD:refs/heads/<trunk>` (:185), `merge --ff-only` (trunk.go:254). Read-only `HasRemote`/`RemoteTrunkRef` go into the git adapter if a caller remains. |
| `fixlane` (0.5k) | deleted | A policy exception to the edit gate with hard-coded refusal classes (fixlane.go:34–48). |
| `waiver` (0.17k) | deleted | `gate_waiver` rows. The gate machinery only injects them as reviewer material (agentstep/reviewer_prep.go); verdict logic never reads them. |
| `placement` (0.3k) | deleted | Epic-parallel, pin and cloud placement; hard-codes `"executor"`/`"reviewer"` (placement.go:153). A plain "binding placement = remote" setting stays as an agents.toml field read by agentstep (**9**). |
| `epicset` (0.2k) | deleted | `ParentCategory="epic-parent"`, `TagPrefix="epic:"`: a category and a tag in Go. Kernel 1's parent/child is `parent_id`. It has 23 importers to change, including the satelled web (Q-satelled-ui). |
| `throughput` (1.6k) | deleted | `satelle stats`; phases split at a `pr_step` state. |
| `reviewscore` (1.5k) | deleted | Reviewer-binding benchmark; also delete `agentstep/judgecase.go` and `tests/reviewcorpus`. |
| `costview` (2.9k) | deleted | Pricing, cost bands, gate-wait phases. It has 22 importers to change, including the satelled web cost panel (Q-satelled-ui). |
| `ctxaudit` (0.6k) | deleted | Wikilink, ref and duplicate-guidance audit over substrate, documents and the embedded defaults. |
| `condense` (0.7k) | deleted | Fits resident docs into a context budget for SessionStart injection, using embedded `condense.toml`. The ledger and sync do not use it. |
| `compact` (1.9k) | deleted | CLI output folding. The one kernel touch, `compact.CompressLog` bounding a deterministic check's output (agentstep/engine.go:5165), becomes a few lines of head/tail inside agentstep. |
| `retrieve` (0.3k) | deleted | CCR blob store (sqlite tables migrated at `store.Open`). Not synced. |

### Commands (`internal/cli`, 51.4k lines; `cmd/satelle`, `cmd/satelled`)

| package / command | kept · deleted · moved | why (which kernel item, or why not) |
|---|---|---|
| `cmd/satelle`, `cmd/satelled` | kept | **8/10**. satelled's flags `--addr`, `--port`, `--version` are maintained. |
| `story create/get/list/set` | kept | **1** (+**2/3** on `set --status`, which goes through `dispatchResponse`→`handOffGate`). |
| `story attach/doc/docs` | kept | **1/3**. Story attachments stay. |
| `story route` | kept | **2**. The item's route and its verdicts so far. |
| `story diff` | kept, thin | **7/3**. Files changed since the engagement baseline, read through the git adapter. This repo's skills use `--recorded` (raise-pr, land, chore-landed, version-stamp, workflow-change-trigger). |
| `story seat`, `seat release` | kept | **7**. |
| `story drive` | kept | **4/9**. Headless dispatch; `node join` reuses it for task wakes. |
| `story rework`, `ready`, `wave`, `queue`(+`withdraw`), `worktree`, `release-record`, `amend`, `archive`/`unarchive`, `estimate`, `actual`, `comparables`, `cost`, `backfill-driver`, `resummarise`, `retrospect`, `message`/`messages`/`message-mark`, `proof`, `definition-edits`, `reconcile`, `recover`, `stop-request`, `tidy`/`untidy`, `waive`, `session`, `sync`, `lessons`, `restamp` | deleted | Process or bloat. rework hard-codes the READY/NOT READY relay contract in Go (cmd_story_rework.go:383–409). ready, wave and queue are epic, readiness and merge-queue process. worktree and release-record are git and release steps. estimate…retrospect are costview, the summariser and the retrospective. proof is a Go-test-name parser. waive is the waiver package. tidy is debris moves. restamp is category re-laning. |
| `story log` | moved | Duplicates `ledger append`. |
| `task create/get/list/set` | kept, provisional | Q-tasks. Remote nodes run "a work item or task". |
| `task archive/validate/trigger` | deleted | Document-shape check, file moves, scheduler (`tasktrigger`). |
| `execution create/get/list/set` | kept, provisional | Q-tasks. |
| `execution record` | deleted | Writes an OKF document under the task folder (documents); `story attach` replaces it. |
| `ledger append/list/verify` | kept | **6**. |
| `ledger record-run/cite-run/citation` | deleted | Suite-run citation for this repo's integration gates; `green\|red`; git HEAD/dirty checks. |
| `hook gate`, `hook stopcheck`, `hook prompt`, `hook context` | kept, thin | **7**. Generic core: `evaluateSeat` (cmd_hook.go:1301), `editPermitted` (:1470), per-harness deny envelope, `[gate] edit_exempt_paths/globs`, temp-dir exemption, dirty-tree stop check. The rest of cmd_hook.go's 3.9k lines goes (see Cuts). |
| `hook commitgate` | deleted (merged into `gate`) | Git commit/push policing (:472–479), `[gate.command_allow] push=["release"]`, fused-engage detection, `[[gate.command_rule]]`. A tree-mutating Bash command is judged by the same edit predicate inside `gate`. |
| `hook explain`, `turnend`, `resume` | deleted | explain only serves the `no_implement` model rule. turnend and resume wake the detached gate (gatehandle). |
| `init` | kept, rewritten | **11**. Today (cmd_init.go:386–763) it writes no workflow file: the route is the embedded `step.toml`/`done.toml`. It also runs about 15 heals and migrations, scaffolds a constitution, documents, principles and launchers, edits `~/.grok/config.toml`, and **spawns `satelle sync push`** (a network write). Target: `.satelle/`, a minimal `satelle.toml`, `workflows/<name>.toml` with `backlog → done` and no gates, harness hooks, then validate. |
| `validate` (+ `workflow validate`, `skill validate`, `agent validate`) | kept, one command | **11** (+**2/3/5**). A single deterministic check of workflows, agents and the skills gates reference; a broken configuration refuses to run. |
| `doctor` | moved | Into `validate` (**11**), keeping the binary-on-PATH and reviewer-isolation checks. |
| `status` | kept, thin | **10/11**. Orientation. `--line` (claude statusline), doc counts and the legacy residue line go. |
| `help`, `version` | kept | **10**. |
| `workflow show` | kept | **2**. |
| `workflow list/create`, `skill create`, `principle create/validate` | deleted | Markdown authoring and category ordering; users edit the files themselves. |
| `agents install/remove` | kept, thin | **7**. Harness hooks for claude, grok and pi. Cursor and the `$SATELLE_HOME/agents/bin` launchers go. |
| `agent show/set/detect/recheck/migrate/profiles push\|restore` | deleted | A machine-wide reviewer-CLI selection that duplicates the agents catalog (**5** puts presets in config). recheck exists for cursor. profiles is REST `/api/v1/me/files`. |
| `sync` (+`push`, `pull`, `status`, `scopes`, `rehydrate`, `config push/deploy`, `workstate push/pull/snapshot/fetch/follow`) | kept | **8**. These verbs are also satelled's exec interface (serve/children.go). |
| `sync documents …`, `sync bindings`, `sync workstate rechain` | deleted | Documents leave. bindings publishes agents.toml to the team catalog. rechain is chain-fork repair (981 lines), which the rewritten ledger replaces. satelled currently execs `sync workstate rechain --conflicted` and `sync documents push --snapshot-only` (Q-satelled-exec). |
| `login`, `logout`, `whoami` | kept | **8**. |
| `project bind/create/list/show/pull-only` | kept | **8**. The embedded-default seed and team-tier write-down go. |
| `settings` (+`server`) | kept | **8/11**. Legacy `web_port`/`sync.server` keys go. |
| `service install/uninstall/status/restart` | kept | **8**. satelled's service entry point is maintained. |
| `serve` | kept (alias), Q-serve | The help calls it a deprecated alias of `satelled`, but `service install` falls back to it, so it is part of satelled's entry points. |
| `project add/list/remove/partitions/prune` (was `workspace …`, sto_b4fa76fe) | kept | **8**. Feeds satelled's mirror ingest; satelled runs no CLI command (sto_1f2f6964). |
| `runtime path/list/reap` | kept | **8**. Runtime plane under `~/.satelle/<key>`. |
| `runtime migrate` | deleted | Compat for the legacy in-repo layout. |
| `logs` | kept | Runtime logs. The `.satelle/logs` symlink pointer goes. |
| `node join` (hidden) | kept | **9**. |
| `team path`, `publish *`, `policy *` | deleted | Team substrate tier, team catalog (a future marketplace) and subscribed command-rule lists. All are REST (see Wire changes). |
| `pr *`, `trunk *`, `changelog`, `fix *`, `review *`, `retrieve`, `stats`, `board`, `reindex`, `doc *`, `substrate *`, `rebase`, `restore`, `migrate` | deleted | Named after steps or forge actions (pr, trunk, changelog). Exception lane (fix). Bloat (review, retrieve, stats, board). Documents (reindex, doc). Embedded default process (substrate, rebase, restore). Backward compatibility (migrate). |
| `update` | deleted, Q-update | Self-update from GitHub releases is distribution, not kernel. It also restarts satelled. |
| `session name` | deleted | Cosmetic launcher label; the launchers go too. |
| CLI support files: bashscan.go (2.0k) | kept, cut | **7**. Keep mutation-target extraction (tokenizer, segments, cd/`-C`, redirect/tee/rm/mv/cp/sed -i). Cut git commit/push detection (:389–520, :764–830), `foreignSatelleVerb` (:1516) and the substrate-lock refs (:1364–1515). |
| containment.go | kept, Q-fence | **7**. Foreign-tree fence. |
| harness_pi.go, harness_hooks.go | kept, thin | **7**. |
| harness_cursor.go, hook_unmatched.go, harness_claude_statusline.go, harness_claude_no_implement.go, hook_noimplement.go | deleted | Cursor; compat statusline heal; model-glob rule (tool grants are the mechanism). |
| substratelock.go, fixlane_*.go, tool_trace.go, command_rules.go, policy_advisory.go, release_advisory.go, drift.go, scaffold_drift.go, embedded_provenance.go, substrate_*.go, principle_heal.go, init_analysis.go, init_foreign_tools.go, validate_injected.go, wikilink.go, placement.go, adoptions.go, team_writedown.go, local_binary.go, project_seed.go (embedded seed), cmd_sync_tier.go, workstate_unpushed.go, workstate_reconcile.go, reworkloop.go, sessionledger.go, advisor_prompt.go, gatexrepo.go, gateresume.go | deleted | Each is process, compat or embedded defaults. substratelock hard-codes the categories `"substrate"`/`"agents-repair"` (:24, :30). advisor_prompt names `satelle-workflow-advisor`. workstate_unpushed is a hold-until-on-remote policy. workstate_reconcile matches trunk commit subjects. |
| gatecaller.go, gatedeliver.go, gate_detach_*.go, child_*.go, dispatch.go | kept, shrunk | **3**. Synchronous gate call and its CLI result. Harness-timed detach (grok 15s) goes with gatehandle. |
| remote_placement.go, remote_run.go | kept, Q-team-remote | **9**. FireRemote, StartPerformerNode, the run-standing poll. |
| uipush.go, uisnapshot.go, middispatchpush.go, web_availability.go, server_endpoint.go | kept | **8**. The satelled ingest client. |

### Cuts inside kept packages

- **verb**
  - Literals to cut:
    - `*req.Status == "done"` (workitem.go:1139).
    - The create default `workitem.StatusBacklog` (:236, :282), which should be the configured start state.
    - `dispatchPayload("done","retrospect",…)` (:1729).
    - `"cancelled"/"canceled"` (engage_validate.go:87).
    - 24 `"executor"`/`"reviewer"` actor literals.
    - `agentcli.HarnessCursor` (readiness.go:311, driver_usage.go:299).
  - Processes inside workitem.go:
    - The step summariser, retrospector, category→`workflow:` stamping and category vocabulary.
    - The readiness mark refusal, `refuseSecondEngagingStory` and `refuseUnreadiedEpic`.
    - `checkTrunkAtEngage` (git fetch + fast-forward).
    - `applyPlannedCriteria` (keep only as config-named `apply_criteria` with no Go default).
    - The queue/wait states, `propose` ordering, `markReady`/`epicAdvice` and the open-epic-children refusals.
    - `refuseDirtyClose` (git status).
    - `recordChangeSet`/`maybeRecordEngagementBaseline` (keep the baseline only via the read-only adapter).
    - `recordDriverUsage`/`recordActual` and `recordRoute` (route doc).
  - In review.go: CreateReviewer, AmendReviewer, Retrospector, StepSummariser, CloudDispatch, answer_progress, `Waived`, panel/Combine/Bundle, ReviewMode, about 20 cost fields.
  - Files deleted whole:
    - amend, readiness, readiness_ready, performer_changes, priorverdicts, criteria (Q-criteria).
    - epic_close, epic_proposal, engage_wave, wave, dependson*, storymembers, after_children, container_wait.
    - queue, release, changelog (+`embedded/CHANGELOG.md`), trunkcheck, worktree_trunk, worktree, changerecord, engagement, storyproof, storytidy, suiterun*, codestate.
    - substratediff, substrateseed, doc, process, processofrecord, storyokf, storyroute (`RouteDocName="route"`, `"## Outcomes"`), storyremoteruns.
    - story_cost, story_actual, story_comparables, gate_value, gatewait, driver_usage*.
    - recover, storyretention, taskarchive, storyarchive, tagvocab, retrieve, messages, board, engagement_mode (epic seat keys), edit_gate (create-review on edit).
    - wiring_restore.go moves to `_test`.
- **agentstep**
  - Skill literals:
    - `summariserSkill="satelle-step-summary"` (engine.go:4301).
    - `structureSkill="satelle-story-review"` (:4298).
    - `retrospectAgent/Skill` (:2967).
    - `engagementSkillCheck="satelle-workflow-skill-check"` (:3765).
    - `cloudPerformerSkill="satelle-cloud-performer"` (cloud.go:37).
    - judgecase.go.
  - State literals: `Status: "backlog"` (:4574/4579), `toStatus=="cancelled"` (:3782).
  - Agent-name meaning: `to.Agent=="reviewer"` (:1904); the executor switch (:2480–2483, :3646, :3843; executor_step.go; reviewer_prep.go); message address `"executor"` (:1610).
  - Git in cloud.go: `worktree.CollectBranch` merge, the `git push -u` hint, the `Satelle-Nonce` trailer.
  - Features beyond gate/performer, all cut:
    - Gates: panels/combine, reviewer_carry, bundles, fail-fast mode, per-criterion verdicts, findings/answers, waivers, deferrals, plan citations, skill-evidence fold, route drift, park-resume extras.
    - Dispatch: cloud/team remotes, placement.Decide, attempt/repair/escalate, the leftover sweep, live consult sessions, secondary failover, the summariser, Retrospect, ReviewCreate/ReviewAmend (`structure.Story`, BDD shape).
    - judgecase.go; the compact/retrieve offload; secretref.
  - Prompt-text permissions (a kernel 5 breach, to be replaced with grants):
    - `executorCharter` "Do NOT change the item's status" (invoke.go:1643) and `consultCharter` (:1657). A performer holding `Bash(satelle:*)` can still run `story set`.
    - `isolateReviewer` "never refuses" (isolation.go:21–37), and `isolation="operator-attested"` silences it. Reviewer read-only is therefore not enforced and must become a hard refusal.
  - Compat: the obsolete in-repo `.satelle/stories/` text (invoke.go:1524/1547), the scalar tools/model fallbacks (:466–475), and the "payload `docs` for the plan and step summaries" text (:1512).
- **agentcli**
  - Cursor, removed entirely:
    - Files: cursor_seat.go, cursor_session.go, driver_usage_cursor.go and their tests/testdata.
    - Identifiers: `CursorCommand` (agentcli.go:100), `HarnessCursor` (harness_detect.go:15 + env markers).
    - Branches: acp.go (`cursor/ask_question`, `cursor/create_plan`), `unwrapCursor`, captured.go, driver_usage.go, harness_facts.go, harness_hooks.go `NoToolMatcher`, hook_output.go `SilentStopAllow`, isolation.go, stop_resume.go, stream.go.
  - Also cut:
    - `CommitMatcher` "commitgate" in `HookSpec`.
    - The claude `--cloud` launcher (cloud.go).
    - Driver usage readers (driver_usage*, claude_transcript.go, session_locate.go).
    - capabilities.go, tool_vocab.go (keep if grant translation across adapters is needed), adapt_command.go.
    - Live consult sessions.
  - Q-acp: grok ACP cannot be held to a grant (isolation.go:829).
- **agentvalidate**: cut the seat-name switches (`"executor","planner","coder"` :633, `"orchestrator"` :369–376, executor/reviewer :330/354/473/551/572/1149/1204, untrusted.go:53–74); the panel/bundle/advisor/rework/wfhook/placement checks; and the `{max_turns}` compat (:1170). Add the reviewer≠performer-model rule.
- **agentartifact**: cut the attempt policy, evidence.go, answers.go and changes.go (the ops report for this repo's `ready` step).
- **lease**: cut the `Migrate` ALTER chain and `in_flight_at` backfill (store.go:311–421); the epic seat key/co-hold (`SeatKey`, `TreeScoped`, `Exclusive`) unless kept generic; and the activity columns not used by satelled.
- **buildinfo**: cut `LegacyDaemonName="satelle-serve"` and `LegacyDaemonVersionKey`.
- **workitem**
  - Cut the `StatusBacklog/InProgress/Done/Cancelled` constants (workitem.go:36–43; used in verb, cli, web, throughput, docstory, and as store.go defaults :167/:246).
  - Cut the `UPDATE … status='backlog' WHERE status='open'` migration and the ALTER shims.
  - Cut the legacy `kind:` read in markdown.go and `Holdable`.
  - triggers.go/inputs.go and `RunCategory="chore"` (triggers.go:121) go with Q-tasks.
  - Q-fields: `Priority`, `Category`, `Archived`, `Kind{task,execution}`.
- **storyfold**: cut seed.go (`definition_snapshot` for pre-fold chains), `ReplayNoVersion`/legacy graph, and reduce the fold to the generic event set.
- **store**: cut `EmbeddedDefaultDocs`/`embeddedWorkflowBody` (sets `wfgovern.EmbeddedRoute`), retrieve wiring, the `storyfold.Seed` call, `workflowVersion()`'s `WorkflowSource{Done,Step,Legacy}` (this repo's two-file format carried into the ledger) and the `DocIndex` documents.
- **ledger**
  - Cut `Migrate.addColumns`/`linkLegacyChains`, `IsReservedKind` (waiver, queue) and typed payloads (`QueueEnqueuedPayload`, `RemoteRunPayload.Step`, `ExecutionLinkPayload`).
  - rounds.go (`CountRejectedRounds`, `ReadinessHolds(planDoc)`) moves to a gate-side reader of generic verdict events.
  - Cut event_telemetry.go cost extraction and tool_permission.go legacy rows.
  - `Actor` stops being a role word.
- **app**: cut `LegacyRuntimeNote`, `PlaneConstitution`, the documents/principles roots on `DocIndex`, `installConfigStamp`, and the per-command setters for trunk, release, tag vocabulary, worktree and engagement mode, plus the waiver/docs/messages resolvers (cli/app.go:94–300, 560–790).
- **config**
  - Seat names as struct fields: `AgentsConfig.Executor`/`.Reviewer` (`toml:"executor"`/`"reviewer"`, agents.go:1062). Name switches at agents.go:302/805/847/851/1178–1185, agentstransport.go:96–97/317–324, resolveagents.go:288–291/589 (`"ready-reviewer"`)/604, vars.go:56–61. Session roles `"in-loop"`, `"orchestrator"`, `"creator"` (session.go:66/110).
  - Skill names: `OperatingPrinciple="satelle-agent-goals"`, `ReviewerVerdictPrinciple="satelle-reviewer-verdict"` (embed.go:36/43), retired.go.
  - `ParallelEpic="epic"` (config.go:929).
  - forge.go (`DefaultForgeProvider`, `pr_heads {"epic/","feat/"}`, `satelle/pr-approval`).
  - Files to cut:
    - trunk.go, fixlane.go, condense.go, pricing.go, categories.go, doctor.go, injected.go.
    - worktree.go/worktree_of.go (branch `epic/{id}`), processhealth.go, projecttier.go, team.go (kimi paths), substratelock.go, command_policy.go.
    - Compat: embed.go, baselineagents.go, stamp.go, migrate.go (`MigrateAgents`), removed_budget.go, strays.go.
  - Fields and tables to cut: `RemovedSyncScopeKeys`, `LegacyAgentsKey`, `Gate.CommandAllow`, `NoImplement*`, `Handoff`, `ReleaseConfig`, `OutputConfig` (~300 lines), `DispatchConfig.leftovers`, the `"documents"` sync area (sync.go:336/560/713/829) and `AuthoredKinds` "documents".
  - Add strict unknown-key refusal for satelle.toml (today only `[worktree]`/`[harness.*]` refuse, harness.go:211).
- **wfdot**
  - Cut `"done"` as the required terminal (wfdot.go:81) and the `doneReachable` seed (:538/546).
  - Cut the agent names `"reviewer","executor","planner","coder"` (:478/582/589/638/678/728), including `IsEditCapableState` = `st.Agent=="executor"`.
  - Cut `StepSummarySkill="satelle-step-summary"` (:31/603) and `DefaultDoneGate` (:25).
  - Park is detected by agent=="reviewer" on synthesised nodes (route.go:662/673; `IsParkState` :781); replace it with an explicit configured park flag plus `resumes`.
  - Process step keys: pr_step, waits_on_children/ready_children/after_children/schedule, mark/propose/apply_changes (`ChangeOps`), placement_tag/delivery_heading/lane_tag/remote_agent/local_tags, answer_progress, rework, advise, untrusted_input, review_mode.
  - Compat: `LegacyScheduleSteps`, removed budget refusers, DOT vocabulary, `applies_to` augmentation rules.
- **wfgovern**: cut the `RouteSourceDone="done"`/`RouteSourceStep="step"` file names, `DerivedRouteName`, `mergeRouteHalf` overlay onto the embedded route, `OrderedWorkflows` tiers, `WorkflowCategory`, `IsFilePairStamp`/`LegacyMarkdownRoute`/`LegacyHash`, `WaitsOnOpenChildren` (epicset), routedrift.go, and the docindex input type.
- **structure**: cut the `Story()` category hint, the `-review` suffix heuristic, `CheckTask` (Q-tasks), `checkPrinciple`, `deprecatedActorKeyword` and the DOT helpers.
- **health**: drop the IDs for removed features (team.*, scaffold.*, breaking.*, config.stray, gate.substrate.unlocked, repo.substrate.untracked, sync.substrate_*, story.status.drift, context.*, live.version_drift, command_rule.allow).
- **help**
  - Keep and rewrite: workflows, reviewer-checks, agent-dispatch (1,661 lines → gates/performers/agents), global-agents, substrate (→ configuration), session, session-token, projects.
  - Delete: create-story, create-review, epic-wave, trunk, fix-lane, compact-output, retrieve, cost-pricing, worktree, workflow-convert, command-rules, doctor (folds into validate), principles (Q-principles).
- **hosted**: delete client_documents.go, the document and attachment cursors in docsync_state.go, boardpb, client_me.go, client_publish.go, the `personalWorkspaceName="personal"` sentinel, and the "older server" compat branches (client.go:46/155/570, credstore.go:54).
- **push / mirror**
  - Cut topic/entity `docs`/`doc` for the documents kind (Q-satelled-docs), the `wave` kind (ingest.go:416) and the ALTER migrations (store.go:60–80).
  - The `doc` kind also carries workflows, which the web reads for stage rails (mirror_panels.go:164), so only the documents kind can go.
- **serve**: cut the `sync documents push --snapshot-only` leg (substratepush.go:36) and the `rechain --conflicted` exec (Q-satelled-exec).
- **web**
  - Cut `StatusBacklog` (mirror_panels.go:154/236, mirror_server.go:254), `StatusDone/StatusCancelled` (stages.go:98) and "prefer a state named done" (web.go:410–416).
  - Cut epicset waves/members (mirror_panels.go:637–800), the costview panels, category→workflow routing (`wfgovern.WorkflowCategory`), the legacy "attached plan document" body parse (:932), the board panel and the documents panel (Q-satelled-ui).
- **syncstate**: cut `PushHeld` (hold until code is on the git remote), the substrate areas if snapsync goes, and policy.go.
- **snapsync**: cut defaults.go (unedited embedded defaults) and the documents area.
- **remotenode**
  - Cut taskrun.go `RunCategory="chore"`, the `remote-node:` tag, the "wake of … on …" title and actor `"remote-node"`.
  - Cut `Report.Workflows` "done.toml merged over the embedded default" (protocol.go:73) and the hard-coded `SubstrateDirs` layout.
  - Cut the Status 400 retry shim (nodeapi.go:251), devtest/github.go (a fake GitHub PR API) and cmd_node.go's `"executor"`/`"reviewer"` (:994–997, 1130–1139) and `doneCategories`/`wfdot.ParseDone` (:1054).
  - **Sprites**: the keep-alive is in both the definition (first `on_create` line) and Go (`spritehold.go SpriteHold`, started at cmd_node.go:341). Delete the definition line, and move `SpriteHold` and the pi symlink line into a sprite adapter.
  - **No docker-local provider exists** (`remotedef.Providers=[fly-sprite]`; no `docker` anywhere in Go). It must be built as the reference adapter.
- **remotedef**: cut `oldKeys` (18 pre-v1 keys, validate.go:147), the infisical refusal (:363), `EnvDefinition`/`FromEnvValue`/`GitHubAppKey`/`RepoSlug` (only `satelle pr` uses them) and `TaskIDPrefix` (Q-tasks). Add `docker-local` to `Providers`.
- **hook/edit gate (cli)**
  - The in-loop performer must come from config, not the literal `executor` (context_harness.go:80/126; wfdot `IsEditCapableState`).
  - Cut the deny text that hard-codes states and agents: `--status plan` at cmd_hook.go:2048/2449/2456/2473/3428/3854; `agent=executor` and `story rework` at :2262/2274; `hookPromptReminder`.
  - Cut `denyIfNoImplement`, `fixLaneEdit`, `substrateLockGate`, `recordToolTrace`, the relay-marker branch and `RecordCursorStop` (:3598).
  - Cut the context injection of `principles:session`, constitution, condense and every advisory (`runHookContext` :2719–2800).
  - Q-multiseat: `pickSessionSeatFor`/`sessionSeatsUnmatched` (about 400 lines).

### Not packages, but in scope

- `api/board.proto` + `hosted/boardpb`: deleted. This is a satelle-server service with no caller, and the proto's header says "This repo … does not serve it". **It is not satelled's API**, despite the contract's wording; see Q-board.
- `api/openapi.yaml`, `api/README.md` REST section: deleted once every call is gRPC.
- `.satelle/documents/` (this repo): deleted (documents leave).
- `tests/` (215 Go files, 35.7k lines): rewritten per the contract. `tests/reviewcorpus` and `tests/plannerbench` are deleted.
- `scripts/`: credfp/credguard/hostguard and skipcheck are judged in Phase B. Release scripts (`build-version.sh`, `check-serve-version.sh`, `install.sh`) are distribution, not Go process.

## Wire changes for satelle-server

### satelled's maintained interface (found; not changed)

satelled serves **plain HTTP only**: no gRPC, no Connect. The surface is:

- **Binary** `satelled`
  - Flags: `--addr` (default `[service] addr`), `--port` (default `[service] port` or 8787), `--version` (prints `satelled <ver> (commit <c>, built <t>)`).
  - Behaviour: SIGINT/SIGTERM, then a 5s graceful stop.
  - Files: mirror DB at `~/.satelle/serve/`, log at `~/.satelle/serve/server.log`.
  - Startup line: `satelled (push-fed mirror) http://…`.
- **HTTP routes** (web/mirror_server.go:105–156, mirror/ingest.go:164–167, serve/serve.go)
  - `GET /static/`, `/favicon.ico`, `/healthz` (body `ok`, header `X-Satelle-Instance`), `/events` (SSE; topics stories/tasks/docs/logs), `/theme`, `/{$}`, `/fragment/projects` (the `/workspace` alias was removed, sto_b4fa76fe).
  - `GET /r/{slug}/{$}`.
  - `GET /r/{slug}/fragment/{stories,tasks,activity,docs,engagement}`.
  - `GET /r/{slug}/fragment/{story,task,execution}/{id}`, `GET /r/{slug}/fragment/workflow/{name}`.
  - `GET /r/{slug}/{story,task,execution}/{id}`, `GET /r/{slug}/doc/{kind}/{name}`, `GET /r/{slug}/help`, `GET /r/{slug}/settings`.
  - `GET|POST /settings/global` (loopback only).
  - `GET /logs`, `GET /fragment/logs`.
  - `/debug/pprof/*` (loopback only).
- **Ingest** (POST only; anything else gets 405)
  - `POST /ingest/change`: `{repo_key, topic, entity, at, origin}`.
  - `POST /ingest/snapshot`: `{repo_key, slug, stories, tasks, executions, docs, ledger_events, story_docs, seats, waves, settings, identity, kinds, merge_kinds, origin}`. `story_docs` carries one row per `<item>/<name>`: the newest version, with `versions` counting them (sto_e2490f1f). Answers 409 `partition_unknown`, or 409 on a slug conflict.
  - `POST /ingest/remove`: `{repo_key}`.
  - Callers: push/publisher.go, cli/uisnapshot.go, cli/cmd_workspace.go.
  - The endpoint comes from `[service] endpoint` (default `http://127.0.0.1:8787`); `SATELLE_SERVER_ENDPOINT` overrides it, and `none` disables it.
- **satelled → CLI exec contract: none in v1.** The pre-rebuild satelled ran the sibling `satelle` (serve/children.go) for `sync workstate push|rechain|snapshot|follow|fetch`, `workspace add`, `sync documents push` and `sync pull`. v1's satelled runs no CLI command: it is fed only by the CLI's pushes (`/ingest/change`, `/ingest/snapshot`, `/ingest/remove`), and `serve/deps_test.go` enforces that it never links the CLI (sto_1f2f6964).
- **CLI entry points**
  - `satelle service install|uninstall|status|restart`. Install flags: `--port`, `--addr`, `--repo`, `--serve-bin`, `--system`. The unit is `satelle.service` with `ExecStart=<satelled> --addr A --port P`, falling back to `satelle serve`.
  - `satelle serve` (`--addr`, `--port`; hidden `--no-watch`, `--base-path`).
  - `satelle project add|remove|list|partitions|prune`.

Parts of that surface the contract's other rules would remove (documents, epics, cost) are listed under Q-satelled-docs and Q-satelled-ui. They are not decided here.

### Already gRPC (may change with the generic ledger)

| service/method | caller | note |
|---|---|---|
| `satelle.sync.v1.Sync/Apply` | hosted/client_sync.go ← cli/cmd_sync_workstate.go:582, cmd_node.go:703 | Pushes items, ledger rows and RunOutputs. Payloads change with the generic ledger. |
| `Sync/Snapshot` | client_sync.go ← workstate_scope.go, cmd_sync_tier.go, workstate_snapshot_pass.go | Paged pull. |
| `Sync/Refresh` | never called | Delete it, or revive it as the token refresh (Q-oauth). |
| `satelle.chain.v1.Chain/Append`, `Read`, `Subscribe` | hosted/client_chain.go ← storyfetch.go, workstate_append.go, workstate_chainpull.go, cmd_sync_workstate_follow.go, cmd_node.go | Compare-and-append on `base_id`; the event carries `workflow_version` and an opaque payload. **To be rewritten**: the event becomes `{id, item_id, kind, payload, prev_hash, hash}`; `workflow_version` drops. The proto's "CLI calls Append only" note is stale (Read and Subscribe are used). |

### REST / plain HTTP today → gRPC replacement

All calls go through `hosted.Client.doAuthed`/`getJSON` (bearer token; retried once on 401 after an OAuth refresh), or through `SignedDo` (node token, no retry). Service names are proposals; the satelle-server rebuild implements them.

| today (method path) | calling file:function | replaced by |
|---|---|---|
| GET `/api/v1/me` | hosted/client.go `Me` ← cli/cmd_login.go (login, whoami), syncscope.go:75 | `satelle.v1.AccountService/GetMe` |
| GET `/api/v1/projects` | client.go `ListProjects` ← cmd_project.go:296 | `satelle.v1.ProjectService/ListProjects` |
| GET `/api/v1/projects/{p}` | client.go `GetProject` ← syncscope.go:80, project_seed.go:38, sync_holder.go:128, substrate_holding.go:51 | `ProjectService/GetProject` |
| POST `/api/v1/projects` | client.go `CreateProject` ← cmd_project.go:275 | `ProjectService/CreateProject` |
| GET `/api/v1/workspaces` | client.go `Workspaces`/`ActiveWorkspaceID` ← cmd_login.go, syncscope.go:91, workstate_attachments.go | `satelle.v1.WorkspaceService/ListWorkspaces` |
| POST `/api/v1/locations` | client_locations.go `RegisterLocationPrincipal` ← `EnsureLocationRegistered`, session_identity.go | `satelle.v1.LocationService/RegisterLocation` (returns the principal id for session tokens) |
| GET `/api/v1/projects/{p}/config` | client.go `ConfigManifest` ← cmd_sync.go:591/1004, cmd_sync_snapshot.go:272/492, cmd_sync_status.go:309 | `satelle.v1.ConfigService/ListConfigFiles` (Q-config-sync) |
| PUT `/api/v1/projects/{p}/config/{path}` | client.go `PushConfigFile` ← cmd_sync.go:677, cmd_sync_snapshot.go:55/83 | `ConfigService/PutConfigFile` |
| GET `/api/v1/projects/{p}/config/{path}[?version=N]` | client.go `ConfigFileContent` ← cmd_sync_snapshot.go:47/75, sync_holder.go:164, cmd_sync.go:1058, cmd_sync_tier.go:32 | `ConfigService/GetConfigFile` |
| GET `/api/v1/projects/{p}/documents?since=` | client_documents.go `ListDocumentChanges` ← cmd_sync_documents.go:150/281, cmd_sync_snapshot.go:94, workstate_attachments.go:302 | **REMOVED** (documents). Attachments move to `satelle.v1.AttachmentService/ListAttachmentChanges` |
| PUT `/api/v1/projects/{p}/documents/{path}` | `PushDocumentFile` ← cmd_sync_documents.go:164, cmd_sync_snapshot.go:110, workstate_attachments.go:161, substrate_backup.go:158 | **REMOVED**. Attachments → `AttachmentService/PutAttachment` (keyed by project, item_id, name); the substrate backup is deleted |
| GET `/api/v1/projects/{p}/documents/{path}` | `DocumentFileContent` ← cmd_sync_documents.go:319, cmd_sync_snapshot.go:102, workstate_attachments.go:305 | **REMOVED**. Attachments → `AttachmentService/GetAttachment` |
| PUT `/api/v1/remote-nodes/{n}/documents/{path}` | `PushDocumentFile` (node mode) ← cmd_node.go `nodePushAttachments` | **REMOVED**. → `AttachmentService/PutAttachment` on the node token |
| GET `/api/v1/workspaces/{ws}/documents?since=`, GET/PUT `…/documents/{path}` | `ListWorkspaceDocumentChanges`, `PushWorkspaceDocumentFile`, `WorkspaceDocumentFileContent` ← workstate_attachments.go:171/314/317 | **REMOVED**. Team attachment copies → `AttachmentService` with a workspace scope |
| GET `/api/v1/me/files`, PUT/GET `/api/v1/me/files/{path}` | client_me.go ← cmd_agent_profiles_backup.go:103/126 (`agent profiles`) | **REMOVED** with `agent profiles` (no replacement) |
| GET `/api/v1/workspaces/{ws}/published`, PUT/GET `…/published/{path}` | client_publish.go ← cmd_publish.go, cmd_sync_bindings.go:168/203, cmd_policy.go:184 | **REMOVED** with `publish`, `sync bindings` and `policy` (future marketplace) |
| POST `/api/v1/remotes/{id}/fire` | client_remote_fire.go `FireRemote` ← remote_placement.go:25, agentstep/team.go:108 | `satelle.v1.RemoteService/FireRemote` (Q-team-remote) |
| GET `/api/v1/remotes/{id}/stories/{sty}/run` | `TeamRunStatus` ← remote_placement.go:29, remote_run.go:46, agentstep/team.go:142 | `RemoteService/GetItemRun` |
| POST `/api/v1/remotes/{id}/nodes` | client_remote_nodes.go `StartPerformerNode` ← remote_placement.go:37 | `RemoteService/StartNode` |
| GET `/api/v1/remote-nodes/{n}` | `GetRemoteNode` ← remote_placement.go:42 | `satelle.v1.NodeService/GetNode` (channel public key) |
| GET `/api/v1/remote-nodes/{n}/definition` (ETag = sha256, `X-Satelle-Definition-Version`) | remotenode/nodeapi.go `Definition` ← cmd_node.go `installNodeDefinition` | `NodeService/GetDefinition` (digest and version become fields) |
| POST `/api/v1/remote-nodes/{n}/secrets/{key}` | nodeapi.go `SecretWithExpiry` ← cmd_node.go `loadNodeSecrets` | `NodeService/ReadSecret` (value, expires_at, refusal code) |
| GET `/api/v1/remote-nodes/{n}/run` | nodeapi.go `Run` ← cmd_node.go:285/493/841 | `NodeService/GetRun` |
| PUT `/api/v1/remote-nodes/{n}/channel-key` | nodeapi.go `PublishChannelKey` ← cmd_node.go:290 | `NodeService/PublishChannelKey` |
| PUT `/api/v1/remote-nodes/{n}/status` (heartbeat; conclude = `run_state=concluded`) | nodeapi.go `Status` ← remotenode/taskrun.go | `NodeService/ReportStatus` (drop the retry-without-findings shim) |
| POST `/api/v1/remote-nodes/{n}/end` | nodeapi.go `End` ← cmd_node.go:163 | `NodeService/EndNode` |
| WebSocket `{relay_url}/{session}?role=local\|remote` | remotechan/open.go:518 `dialRelay` | `satelle.v1.RelayService/Connect` (bidi stream), Q-relay |
| GET `/.well-known/oauth-protected-resource/api`, AS metadata (`/.well-known/oauth-authorization-server`, `/.well-known/openid-configuration`), browser `/oauth/authorize` + loopback callback, POST `/oauth/token` | hosted/discovery.go, oauth.go `postToken` | Q-oauth: the browser redirect cannot be gRPC; the token exchange and refresh could be `satelle.v1.AuthService/ExchangeCode`/`RefreshToken` |
| browser `{server}/logout?session_id=` | cmd_login.go `runLogout` (opens a browser; satelle makes no HTTP call) | unchanged (Q-oauth) |

Not satelle-server, so not wire changes: GitHub releases (`update`, deleted), GitHub REST (`forge`, deleted), arbitrary policy URLs (`policy`, deleted), and the Sprites unix-socket keep-alive (moves into the sprite adapter).

Server routes in `api/openapi.yaml` the client never calls: `/api/v1/projects/{p}/config-rollback` and `POST /api/v1/me/tokens` (minted server-side).

### Removed document wire (summary for satelle-server)

Every `/documents` route on the project, workspace and remote-node scopes is removed from satelle's side. Story attachments, which travel over those routes today as `stories/<id>/<file>`, move to a new `AttachmentService`. The `documents` sync area, `documents.snapshot.json` snapshot records and the substrate backup PUTs are removed.

### Ledger and node protocol (to be specified in Phase B)

- The chain event format changes as described above, with `api/story_chain.proto` rewritten.
- `api/checkout_sync.proto` payloads (`WorkItem`, `LedgerEntry`) carry the generic event.
- The node join/report/conclude moves from the six REST routes to `NodeService`.

### Phase B wire (implemented; satelle-server implements the other side)

Protos: `api/satelle/chain/v2/chain.proto` (service `satelle.chain.v2.Chain`) and
`api/satelle/v1/satelle.proto` (`satelle.v1` services AccountService,
ProjectService, WorkspaceService, LocationService, ItemService,
AttachmentService, RemoteService, NodeService, RelayService). Generated Go:
`internal/wire/{chainpb,satellepb}`. Auth is gRPC metadata
`authorization: Bearer <token>` (user, session or node token); a stored login is
refreshed once on UNAUTHENTICATED through the OAuth token endpoint. The
in-memory reference of the server side is `internal/wire/fakeserver`.

| before | after |
|---|---|
| `satelle.sync.v1.Sync/Apply` | `satelle.chain.v2.Chain/Append` (events) + `satelle.v1.ItemService/PutItems` (folded item summaries for listing) |
| `Sync/Snapshot` | `Chain/Heads` (paged item heads) + `Chain/Read` |
| `Sync/Refresh` | removed (refresh is the OAuth token endpoint) |
| `satelle.chain.v1.Chain/{Append,Read,Subscribe}` | `satelle.chain.v2.Chain/{Append,Read,Subscribe}`: event `{id, item_id, kind, actor, at, payload, prev_hash, hash, seq}`; compare-and-append on `prev_hash` (was `base_id`), refusal detail `StaleHead` (was `StaleBase`); `workflow_version` dropped; the canonical hash is specified in the proto and pinned by `internal/ledger/canonical_test.go` |
| GET `/api/v1/me` | `AccountService/GetMe` |
| GET/POST `/api/v1/projects`, GET `/api/v1/projects/{p}` | `ProjectService/{ListProjects,CreateProject,GetProject}` |
| GET `/api/v1/workspaces` | `WorkspaceService/ListWorkspaces` |
| POST `/api/v1/locations` | `LocationService/RegisterLocation` |
| `/api/v1/projects/{p}/config*` | **removed** (no config sync, ruling Q-config-sync) |
| every `/documents` route (project, workspace, remote-node) | **removed**; story attachments use `AttachmentService/{PutAttachment,GetAttachment,ListAttachmentChanges}` keyed by (project, item_id, name, sha256) |
| `/api/v1/me/files*`, `/api/v1/workspaces/{ws}/published*` | **removed** |
| POST `/api/v1/remotes/{id}/fire` | `RemoteService/FireRemote` |
| GET `/api/v1/remotes/{id}/stories/{s}/run` | `RemoteService/GetItemRun` |
| POST `/api/v1/remotes/{id}/nodes` | `RemoteService/StartNode` |
| GET `/api/v1/remote-nodes/{n}` | `NodeService/GetNode` |
| GET `…/definition` (ETag, version header) | `NodeService/GetDefinition` (`digest`, `version`, `not_modified` fields) |
| POST `…/secrets/{key}` | `NodeService/ReadSecret` (`value`, `expires_at`, `refusal`, `refusal_reason`) |
| GET `…/run` | `NodeService/GetRun` |
| PUT `…/channel-key` | `NodeService/PublishChannelKey` |
| PUT `…/status` (heartbeat; conclude) | `NodeService/ReportStatus` (the retry-without-findings shim is gone; `lost_after_seconds` in the answer) |
| POST `…/end` | `NodeService/EndNode` |
| WebSocket `{relay_url}/{session}?role=` | `RelayService/Connect` (bidi; first frame names session and role) |
| OAuth discovery, browser authorize, `/oauth/token` | unchanged HTTP (exempt, ruling Q-oauth) |

Behaviour the server must match:

- **Relay.** One side's EOF ends only that side's stream. The sealed channel
  sends a sealed bye on close, and on a key mismatch it answers with a bare
  refusal frame. The relay forwards both as opaque bytes.
- **Remote definition schema** (the server's validator mirrors
  `internal/remotedef`). Keys are provider, machine, repo, drive, secrets,
  setup, network, lifecycle and satelle_version. `environment`, `identity` and
  `trigger` are dropped. The item drive kind is `item` (was `story`). Providers
  are `docker-local` and `fly-sprite`. The server still starts sprites; the
  sprite keep-alive and the pi symlink live in satelle's sprite adapter, not in
  the definition.
- **Nodes.** A node is admitted by its published channel key; the local-mint
  token book is gone. The node token reaches `satelle node join` only through
  `SATELLE_NODE_TOKEN`.
- **Cloud seat.** No server call. `satelle story collect <id> --branch B`
  records `branch.collected {branch, commit, base}` on the ledger, and the
  item's gates judge base..commit.

## Open questions for the operator

Each is a ruling the contract does not make. The inventory records a lean; Phase B does not decide it.

1. **Q-tasks.** Do tasks and executions survive as work-item kinds? Kernel 1 names only stories; the remote contract says "a work item or task". Affects `task`/`execution` commands, workitem `Kind`, `triggers.go`, `RunCategory="chore"`, `TaskIDPrefix`, and remotenode taskrun. Lean: one item type with a free `kind`, no Go meaning.
    **Ruled:** kept as item kinds; a task names an input selection and a run records the items it took; `satelle task run`; no triggers. (Operator rulings)
2. **Q-transitions.** Kernel 2 says states and transitions are read from config. Today transitions are *derived* (topological sort of `requires`, plus synthesised park/cancel/recover edges in wfdot/route.go). Should the kernel schema author transitions explicitly (lean), or keep the derived route?
    **Ruled:** explicit transitions in configuration. (Operator rulings)
3. **Q-category.** Category → route lane selection (done.toml per-category tables, `ListFor`, `WildcardCategory`) is not in kernel 2. Drop it (one workflow per repo, or a workflow chosen by a configured tag), or keep a generic `applies_to`?
    **Ruled:** configured selection rules on category or tags, first match else default. (Operator rulings)
4. **Q-fields.** Kernel 1 lists id, title, body, AC, status, tags, parent/child. Keep `priority`, `category`, `archived`, `assignee`?
    **Ruled:** keep category, kind, workflow; `assignee` carried, no behaviour; `priority` becomes a tag; `archived` goes. (Operator rulings)
    **Changed 2026-11-10** (operator directive, sto_600b9a85): `assignees` is a list, and every user holding a live seat is an assignee, marked working.
5. **Q-criteria.** Are per-criterion verdicts (verb/criteria.go, agentstep/criteria.go) part of "verdict = accept/reject plus notes"?
    **Ruled:** no; accept/reject plus notes. (Operator rulings)
6. **Q-executor.** `[executor]` (the in-loop session) and `[reviewer]` (default gate seat) are struct fields and built-in meanings in config, agentstep, wfdot and the hook. Lean: the agents catalog is a free map, and the in-loop/performing binding is named by the workflow.
    **Ruled:** free agents map; the workflow names the binding. (Operator rulings)
7. **Q-principles.** Do principles and `principles:session` injection (hook context, `sessionPrinciples`, constitution) survive? Not in kernel 1–11. Lean: they leave Go; a harness can inject a configured file list.
    **Ruled:** principles leave Go; a configured file list the hook injects. (Operator rulings)
8. **Q-summariser.** The step summariser and retrospective are hard-wired skills. Lean: they become ordinary performers or gates in this repo's config.
    **Ruled:** ordinary performers or gates in this repo's config. (Operator rulings)
9. **Q-multiseat.** Does the edit gate need multiple seats per session/worktree (cli `pickSessionSeatFor` etc., lease `SeatKey`/`TreeScoped`)?
    **Ruled:** seats stay as session-at-location bindings, no concurrency cap. (Operator rulings)
10. **Q-fence.** Is the foreign-tree fence (cli/containment.go) part of the kernel edit gate? Should a generic "locked paths while performing" rule replace the category-based substrate lock?
    **Ruled:** fence stays; one generic `locked_paths` replaces the substrate lock. (Operator rulings)
11. **Q-command-rule.** Should `[[gate.command_rule]]` survive as generic config, given that kernel 5 enforces through tool grants?
    **Ruled:** command rules go. (Operator rulings)
12. **Q-acp.** Grok's ACP mode cannot be held to a tool grant (agentcli/isolation.go:829). Drop ACP, or keep grok command mode only?
    **Ruled:** grok ACP goes; grok in command mode; validate refuses a read-only seat on an adapter that cannot enforce grants. (Operator rulings)
13. **Q-gate-async.** Do long gates need detached delivery (gatehandle, turnend/resume) at all? Lean: no, gates run synchronously.
    **Ruled:** synchronous; a harness timeout becomes an open question. (Operator rulings)
14. **Q-update.** Is `satelle update` (self-update, which also restarts satelled) kernel?
    **Ruled:** stays, slimmed. (Operator rulings)
15. **Q-serve.** `satelle serve` is a deprecated alias, but the `service install` unit falls back to it. Is it part of satelled's maintained entry points?
    **Ruled:** the alias goes; the unit calls `satelled`. (Operator rulings)
16. **Q-satelled-exec.** satelled execs `sync workstate rechain --conflicted` and `sync documents push --snapshot-only` (serve/children.go). Removing rechain (ledger rewrite) and documents changes that de-facto interface. Allowed, or keep stub verbs?
    **Ruled:** satelled does not exec satelle verbs; the CLI pushes. (Operator rulings)
17. **Q-satelled-docs.** Removing documents touches satelled's maintained interface: `GET /r/{slug}/doc/{kind}/{name}`, `/fragment/docs`, the Snapshot `docs` field and the `docs` SSE topic. The same `doc` kind also carries workflows, which the board needs. Partial cut (documents kind only), or re-source workflows?
    **Ruled:** repo documents and their panels go; attached documents come from the ledger. (Operator rulings)
18. **Q-satelled-ui.** The web UI renders epic waves/members (epicset), cost/band/driver rows (costview) and the message board. Are those panels part of the maintained interface? If not, the UI switches to `parent_id` children and drops cost and board.
    **Ruled:** epic waves, cost and board panels go; story detail shows route, verdicts and attachments. (Operator rulings)
19. **Q-board.** The contract names `api/board.proto` as satelled's API. It is in fact an unserved satelle-server message-board service with no caller. Confirm it is deleted.
    **Ruled:** deleted. (Operator rulings)
20. **Q-config-sync.** Is `.satelle` config sync (ConfigService + snapsync + subsync, `sync config push/deploy`, `sync pull`) part of kernel 8, or only work state (items + ledger + attachments)?
    **Ruled:** config sync deleted; sync carries items, ledger and attachments. (Operator rulings)
21. **Q-team.** Team substrate tier (`team path`, team_writedown, project tier holder): part of sync, or deleted (lean)?
    **Ruled:** deleted. (Operator rulings)
22. **Q-team-remote.** Is team-remote placement (FireRemote, TeamRunStatus, `remote_wait`, cloud performer) kernel 9, or only "start a node for an item"?
    **Ruled:** fire for an item or task, node reports back, remote placement, and the cloud seat stay. (Operator rulings)
23. **Q-relay.** Must the remote channel's WebSocket relay become a gRPC bidi stream? It is byte forwarding, not an API call.
    **Ruled:** gRPC bidi stream. (Operator rulings)
24. **Q-oauth.** "All communication is gRPC" collides with OAuth: the browser authorize redirect, RFC 8414/PRM discovery, and a token endpoint that may belong to an external IdP. Is OAuth exempt?
    **Ruled:** OAuth browser, discovery and token endpoints are exempt. (Operator rulings)
25. **Q-remotedef-keys.** The contract lists the remote schema as provider, drive, secrets, setup, network, lifecycle. Go also has `machine`, `identity`, `repo`, `trigger`, `environment`, `satelle_version`. Keep which?
    **Ruled:** keep provider, machine, repo, drive, secrets, setup, network, lifecycle, satelle_version. (Operator rulings)
26. **Q-lease-state.** "Stateless CLI" against a local sqlite lease table and a row cache. Should seat allocation and item rows be pure folds of the ledger?
    **Ruled:** the ledger is the record; sqlite is a cache rebuilt from it. (Operator rulings)
27. **Q-provenance.** Should per-invocation config provenance (sessionconfig digests, `workflow_hash` on events) carry over into the generic ledger?
    **Ruled:** `workflow_hash` drops. (Operator rulings)
28. **Q-embedded.** This repo's route names skills and seats that exist **only** in the embedded substrate. They must be copied into `.satelle/` when embedding goes, or dropped from the route:
    **Ruled:** copy only what the route still uses; drop summary, estimate and epic-retrospective. (Operator rulings)
    - Skills: ready, satelle-step-summary, satelle-panel-all-accept, satelle-workflow-advisor, satelle-docs-only-check, satelle-substrate-only-check, satelle-epic-branch-check, satelle-epic-retrospective, satelle-estimate-actual-review, satelle-instruction-change-review, satelle-instruction-change-trigger, satelle-workflow-change-review, satelle-story-create-review, satelle-story-amend-review, satelle-story-plan-review, satelle-story-done-review, satelle-story-blocked-review, satelle-story-blocked-triage, satelle-story-cancel-review, satelle-task-validate-before-review, satelle-task-validate-after-review.
    - Seat: `orchestrator`.
    - Session principle: `satelle-agent-goals`.
    - Phase B will copy what the rewritten config still references. Confirm whether that is wanted, or whether this repo's route should be simplified at the same time.
29. **Q-skill-refs.** This repo's skills call commands being deleted, for example `story definition-edits` (satelle-definition-unchanged-check.md:42) and waivers (satelle-story-release-review.md). Phase B rewrites those skills onto `ledger list`; flag any skill you would rather keep frozen.
    **Ruled:** rewrite onto `ledger list`. (Operator rulings)

### Phase B open questions

These came up in Phase B and are not decided here.

30. **Q-grok-tools.** The grok adapter's write/shell tool names (`write_file`,
    `run_terminal_cmd`) are guesses. Reviewers on grok use only the read tools
    (`read_file`, `grep`, `list_dir`), which are confirmed from the old preset.
    Confirm the write/shell names against grok before a grok performer is
    configured.
31. **Q-fly-rule.** Command rules are gone (ruling Q-command-rule). This repo's
    "Fly.io is operator-only" rules have no equivalent for the interactive
    session, because grants bind only dispatched agents. Is a binding
    `deny = ["Bash(fly:*)"]` enough, or should `locked_paths` gain a command
    form?
32. **Q-cloud-launch.** The cloud seat has no launcher. satelle records a
    `cloud:` seat and `story collect` brings the branch back for the local
    gates, but starting the claude.ai/code session is done by hand. Should
    satelle launch it, and through what?
33. **Q-node-docker-e2e.** The docker-local test starts a container and walks a
    story inside it. A full `node join` against a server from inside a
    container was not exercised, because the fake server listens on host
    loopback. Should that run against a server reachable from the container?
34. **Q-identity.** satelled's top bar shows no email, because the CLI pushes an
    empty identity unless logged in. Push the `whoami` identity when logged in?
35. **Q-gate-timeout.** Gates run synchronously (ruling Q-gate-async). No
    harness timeout was observed in this sandbox. Measure long gates
    (build/test checks) under real claude/pi/grok hooks before closing this.
    - **claude, measured 2026-11-07** (Claude Code 2.1.319, satelle branch
      build of sto_41e7f0e2). A check gate `long` (`sleep 900`) with
      `timeout = "20m"` ran inside `satelle story set <id> --status done`,
      which an interactive claude session ran as a background command, with
      the session's PreToolUse hook live. The move took 900s, recorded
      `gate.run long accept` with duration_ms 900003, and the item reached
      done. A gate's own `timeout` is what bounds it. The harness bounds only
      the shell command, so run a long move as the harness's background
      command (`satelle story set --help`).
    - **claude foreground control: not run.** A foreground command is capped
      at 10 minutes (600000 ms), so a 15-minute gate cannot finish there.
      This is why long moves go in the background.
    - **pi and grok:** installed on the measuring machine but not yet
      measured. This question stays open for them.

## Operator rulings

Ruled 2026-11-05. These answer the inventory's open questions. Where a ruling and
the contract above disagree, the ruling wins.

### Positioning

- **satelle is local and solo-developer first.** Team behaviour is the commercial
  upgrade and lives in satelle-server. satelle carries team fields through sync
  but gives them no behaviour. Several developers may work on the same story at
  once; satelle does not care.

### Work items and workflows

- **Q-tasks.** A task is a **repeating workflow that takes inputs**: a selection
  of items (for example, every open story) over which it runs a structured
  review or plan. A run of a task is an execution with its own route and
  ledger. Tasks and executions stay as item kinds, with no kind-specific Go
  beyond "a task names its input selection; a run records which items it took".
  Merging and releasing completed work is a task, not a story step. **satelle
  does not control when tasks run**: it runs one when asked (`satelle task run`);
  repeating it is external (cron, a satelle-server remote, or a routine).
  `tasktrigger` and `RunCategory="chore"`-style literals go.
- **Q-category and epics.** A workflow is selected by **configured rules that
  match an item's category or tags**: for example `category = "substrate"`,
  `"bug"`, `"feature"`, or the tag `epic` / `epic:parent`. The rules, names and
  their order live in configuration; Go only evaluates "first rule that matches,
  else the default". `epicset`'s hard-coded `epic-parent`/`epic:` go.
- **Containers (epics).** "A container may not close while its children are
  open" is a **deterministic check gate in configuration** (a script that lists
  the item's children through the CLI), not a Go step key. `waits_on_children`
  and friends go.
- **Q-transitions.** Transitions are written out explicitly in configuration.
- **Q-fields.** Kept: id, title, body, acceptance criteria, status, tags,
  parent, kind, category, workflow. `assignees` (a list, since 2026-11-10;
  before, a carried `assignee` string) holds the explicit assignees; the
  users holding a live seat join them, marked working (sto_600b9a85).
  `priority` becomes a tag.
  `archived` goes.
- **Q-criteria.** A verdict is accept/reject plus notes.

### Gates: order, fail-fast, parallel, relevance (tokenomics)

Gates are optimised for round trips and tokens. Generic configuration on a
transition, no names in Go:

- **Order and mode.** A transition's gates run in a configured order, either
  sequentially with **fail-fast** (the first reject stops the round) or as a
  **parallel** group; a transition may mix ordered groups.
- **Relevance.** A gate runs only when something relevant to it changed: a
  configured condition (changed paths matching globs, or a deterministic trigger
  script) decides; an irrelevant gate is recorded as skipped on the ledger, never
  silently.
- **Carry.** On a re-presented transition, a gate that already accepted is not
  re-run unless what it covers changed since its accept.
- **Panels.** One skill judged by several bindings (for example, different
  models) with a configured combine rule (all accept) stays, as a generic gate
  option.
- Record per gate run: tokens, duration, result, so the cost of a route is
  visible (a ledger event, not a cost-band feature).

### Engagement (seats and location)

- **Q-multiseat / seats.** Seats stay, but **not as a concurrency limit**. That
  limit was imposed for sequential development because the default route ran
  from backlog to release with no separate merge-and-release step. No cap on how
  many items a session engages or how many sessions work one item.
- A seat binds **a session at a location** to an item in a performing state; the
  edit gate reads it. **Location is part of the seat**: local worktree, a remote
  node, or a cloud session (claude.ai/code). A workflow's binding may place a
  step on a remote or cloud seat; its edits are judged by the same gates when
  its work comes back. `lease` shrinks to this.

### Agents and harnesses

- **Q-executor.** The agents catalog is a free map; the workflow names which
  binding performs or judges each step.
- **Q-acp.** Claude, pi and grok are all supported, each through a thin adapter
  chosen by the binding's `command`. **Grok ACP goes**; grok runs in command
  mode. Any adapter that cannot enforce a binding's tool grants may not back a
  reviewer: `validate` refuses a read-only seat on it.
- **Reviewer read-only is enforced, not attested.** `isolateReviewer` refuses;
  `isolation="operator-attested"` goes. Performer limits ("do not change the
  item's status") are enforced by grants, not prompt text. This lands first in
  Phase B.
- **Q-principles, Q-summariser.** Principles leave Go (a configured list of
  files the hook injects). The summariser and retrospective become ordinary
  performers or gates in this repo's config.

### Documents and the ledger

- **Repo documents go** (`.satelle/documents`, its index and sync).
- **Documents in the ledger are very important.** A story's attached documents
  (plan, evidence, output, summaries) are recorded on the ledger, synced to
  satelle.dev over gRPC (`AttachmentService`), and shown in satelled. Treat them
  as kernel, with tests.
- **The ledger is the history of record; sqlite is a cache** rebuilt from it.
  "Stateless" means no process in the binary, not no storage (Q-lease-state).
- **Q-provenance.** `workflow_hash` drops from events.

### Sync and satelle.dev

- **Q-config-sync.** Delete config sync. `.satelle/` is in git; sync carries
  work state only: items, ledger, attachments.
- satelle logs in to satelle.dev to enable the remote ledger; login stays.
- **Q-team, Q-board.** Team substrate tier and the board client are deleted.
- **Q-oauth.** OAuth's browser authorize, discovery and token endpoints are
  exempt from the gRPC rule. Everything else to satelle-server is gRPC.
- **Q-relay.** The remote channel relay becomes a gRPC bidi stream.

### satelled

- satelled stays in this repo, local first. Its **routes, ingest endpoints and
  CLI entry points are maintained**; its **pages align to the kernel**: the
  story list and actions pages are good and stay; the story detail page is
  improved (route, verdicts, attached documents from the ledger); panels for
  removed features (epic waves, cost, board, repo documents) go (Q-satelled-ui,
  Q-satelled-docs).
- **Real-time, filterable view of every story** is the point: satelled keeps its
  own sqlite index fed by CLI push. It is **decoupled from the CLI**: it does
  not exec `satelle` verbs back (Q-satelled-exec); the CLI pushes what it needs.
- **Windows is a secondary priority** (the operator cannot test it). In this
  rebuild only: keep Linux-only calls in small platform files, and cross-compile
  for windows/amd64 at each milestone (`GOOS=windows go build ./...`). No Windows
  service work.
- **Q-serve.** The `satelle serve` alias goes; the unit calls `satelled`.

### Remotes

- **Q-team-remote.** Kept: fire a remote for an item or a task, the node runs and
  reports back, and "this binding runs on a remote" as a placement. **The cloud
  seat (a claude.ai/code session performing a step) stays** as a location type
  behind the same placement seam, with its branch collected back and judged by
  the local gates; drop only its process literals (`satelle-cloud-performer`
  skill name in Go, nonce trailer conventions not read by the kernel).
- **Q-remotedef-keys.** Keep provider, machine, repo, drive, secrets, setup,
  network, lifecycle, satelle_version. Drop identity and trigger unless a kept
  feature reads them.
- Build the **docker-local** provider as the reference; move `SpriteHold` and the
  pi symlink into the sprite adapter.

### Other

- **Q-gate-async.** Gates run synchronously. If a long gate hits a harness
  timeout, record it as an open question rather than rebuilding gatehandle.
- **Q-update.** `satelle update` stays, slimmed.
- **Q-command-rule, Q-fence.** Command rules go (grants). The foreign-tree fence
  stays; one generic `locked_paths` setting replaces the category substrate
  lock.
- **Q-embedded, Q-skill-refs.** Copy into `.satelle/` only the skills and seats
  this repo's route still uses; drop the summary, estimate and epic-retrospective
  ones. Rewrite skills that call deleted commands onto `ledger list`.

## Audit

Command: `scripts/audit-literals.sh`. It collects every workflow name, state,
gate, skill, agent binding, and rule category and tag from this repo's
`.satelle/`, then greps every non-test Go file under `internal/` and `cmd/` for
each one as a quoted literal. A short allowlist covers words that coincide with
mechanism, each one reviewed: `"done"` as a shell keyword in hook/bashscan.go and
as the relay's end-of-stream message, and `"cancelled"` as a pi RPC protocol key.

B2 result (2026-11-05):

```
names checked: 77
no Go string literal names this repo's workflow
```

B4 result (2026-11-05, final, after satelled and the integration suite):

```
$ scripts/audit-literals.sh
names checked: 77
no Go string literal names this repo's workflow
```

The audit runs locally in the format-vet gate (GitHub runs no tests).

## Progress

Phase B, started 2026-11-05. Go stays at 1.27.0: go.dev is unreachable from the
build sandbox, so the latest-release check (working rule 5) could not run; recheck
when it can.

### Done

- **B1 (enforcement and deletion), commits 1e90624..cc4d435.**
  - Reviewer read-only is a hard refusal (`agentcli/deny.go ReviewerProblem`,
    applied in `agentstep/isolation.go isolateReviewer` before any process
    starts); a tool breach observed at run time fails the verdict;
    `operator-attested` is gone.
  - Performers carry a denied-tools grant for `satelle story|task|execution set`
    (`agentcli/deny.go PerformerDeniedTools`); an adapter that cannot express a
    deny refuses. Grok ACP is refused; validate/doctor refuse a reviewer on an
    adapter that cannot enforce grants.
  - Every package, command and file marked deleted is gone, cursor included;
    `internal/config/substrate` is gone after copying the 18 skills, the
    `satelle-agent-goals` principle and the `orchestrator` seat this repo's route
    used into `.satelle/`.
  - Non-test Go 180k → 103k lines; unit tests 279k → 118k + tests/ 28k.

- **B2 (kernel), commits b09d402..HEAD.**
  - The fresh kernel (ledger, cache, item, workflow, agent, gate, gitfacts,
    kernel, hook) replaces verb, agentstep, agentcli, wfdot, wfgovern, wfroute,
    storyfold, workitem, lease, drive, docindex, agentvalidate, agentartifact,
    doctor, structure, health, testutil, app, store, worktree, runstatus,
    syncscope and the old ledger. `internal/cli` is rewritten as a thin layer
    over `kernel`.
  - Commands: `init [--hooks]`, `validate`, `status`, `help`, `version`,
    `story create|get|list|set|attach|attachments|route|diff|seat|release|perform`,
    `task create|get|list|set|run`, `execution get|list`,
    `ledger append|list|verify|rebuild`, `workflow show`,
    `hook gate|context|prompt|stopcheck`, `agents install|remove`, `login`,
    `logout`, `whoami`, `service *` (the unit runs `satelled`), `project *` (this machine's projects; was `workspace *`), `workspace list` (satelle.dev),
    `runtime *`, `logs`.
  - satelled no longer execs the CLI (`serve/deps_test.go` enforces it).
  - This repo's `.satelle/` is on the new schema and validates. Workflows:
    `delivery` (default), `chore`, `epic`, `runbook` (executions) and `standing`
    (tasks). The plan-stage reviewer `binary-scope` runs first on
    plan → in_progress (fail-fast), again on in_progress → integration over the
    diff, and its skill states "no exemption".
  - Check skills are `type: check`. A script starting with `#!` is run by the
    interpreter that line names (the file itself is never executed); the transition is passed in the environment and as JSON on stdin.
  - Verdict JSON key is `verdict`.
  - `satelle init` in an empty git directory, then `story create` and
    `story set --status done`, walks backlog → done; `ledger verify` passes.
  - The audit script `scripts/audit-literals.sh` passes (see Audit).
  - Non-test Go is 27k lines.

### Choices made in B2 (contract silent)

- **Item ids** are `<prefix>_<8 hex>`. The prefix is `[items] prefix.<kind>`,
  else the kind's first three letters (a new story is `sto_…`). No migration.
- **Storage.**
  - The ledger and cache live in the home-keyed runtime dir
    (`~/.satelle/<repo-key>`), so the worktrees of one repo share one ledger.
  - One store-wide append lock.
- **Session and seats.**
  - `SATELLE_SESSION` wins over the hook payload's session id.
  - A seat at `local:<tree>` matches only that worktree.
  - Any non-terminal state may take a seat; the edit gate requires the state
    to have a performer.
- **Edit gate order:**
  1. temp dirs and paths in no tree: allowed;
  2. another repo's tree: denied (the fence);
  3. `locked_paths`: denied;
  4. `edit_exempt_paths`: allowed;
  5. otherwise a seat is required.
  An invalid configuration still allows edits under `.satelle/` so it can be
  repaired.
- **Adapters.**
  - Only claude, pi, grok and session.
  - A performer with Bash can only run on claude, because pi and grok cannot
    express a scoped deny.
  - Remote and cloud placements return an error until B3.
- **Gate options.**
  - A rule's category/tag/kind are ANDed.
  - A gate is relevant when both its `paths` and its `trigger` agree.
  - Carry uses the latest non-skipped result on the same from → to with equal
    coverage.
  - A panel records one result.
- **Item kinds** `story`, `task` and `execution` are kernel vocabulary. A
  workflow, state or gate of this repo is never named after one, so the
  literal audit stays meaningful.
- **Command rules are gone.** This repo's "Fly.io is operator-only" command
  rules have no replacement in the interactive session. A dispatched binding
  can carry `deny = ["Bash(fly:*)"]`.

- **B3 (wire and remotes), commits f1b0ee7..cf11e51.**
  - Every call to satelle-server is gRPC (table under "Phase B wire"), except
    the OAuth endpoints.
  - `internal/hosted` is a gRPC client.
  - `internal/wire/fakeserver` implements every service in memory, and tests
    use it.
  - `sync push|pull|status`: compare-and-append and no automatic rechain; a
    conflict prints the diverging events. Attachments travel through
    AttachmentService.
  - `project *`, `login`, `logout` and `whoami` are back.
  - The relay is `RelayService/Connect`; WebSocket, `coder/websocket` and
    chromedp are gone.
  - Node contract (`satelle node join`) over NodeService.
  - Provider seam `internal/remote/provider`:
    - `docker-local` is the reference: all docker calls are in one file, and a
      container is inspectable with `docker exec`.
    - `fly-sprite` holds SpriteHold and the pi link.
  - Placement:
    - `remote:<id>` goes through StartNode plus the relay.
    - `cloud` uses a `cloud:` seat plus `story collect`.
    - `kernel.Drive` is the headless walk.
  - `remote fire|status` commands.
  - The docker-local integration test is skipped in this sandbox: the docker
    CLI is present but there is no daemon. A fake-docker argv test always runs.
  - Non-test Go, excluding generated code, is 24k lines.

- **B4 (satelled, tests, audit), commits b7ecea7..3ccab8c.**
  - satelled runs on the kernel and never execs the CLI. Its routes and ingest
    endpoints still answer, and an old snapshot is still accepted.
  - `internal/uipush` pushes every ledger write, including a sync pull and a
    refused transition, as a change plus a merge snapshot, falling back to a
    full snapshot. It pushes only to this home's satelled.
  - The story detail page shows the fields, the route drawn from the pushed
    workflow (current state, park and terminal marked, next transitions),
    every verdict grouped by presentation (result, binding, tokens, duration,
    notes) and the attached documents (markdown rendered).
  - The epic waves, cost, board and repo documents panels are gone.
  - The integration suite under `tests/` drives the real binaries:
    - init → backlog → done;
    - a user workflow with a fake claude covering every gate option, park and
      resume;
    - validate refusals, the reviewer≠performer rule, the edit gate,
      attachments and task runs;
    - sync between two homes through the fake server;
    - satelled pages;
    - docker-local, which is skipped here because the sandbox has no docker
      daemon.
  - `satelle update` (slimmed) and `satelle settings` are back.
  - CI runs build, vet, the windows cross-build, tests, the audit and gofmt.
  - All gates pass at the final commit.

### Deviations to repair

- `satelle update` was deleted in B1 against ruling Q-update; it was rebuilt
  slimmed in B4. `story route` and `story diff` were deleted in B1 and returned
  in B2. Both are repaired.

### Next

Phase B is complete. What is left is for the operator: the open questions under
"Phase B open questions", resetting the in-flight and backlog stories to the
start (the stores are archived, not migrated), and merging this branch together
with satelle-server's rebuild.

### Kernel design (B2) — choices where the contract was silent

The B2 kernel is written fresh in small packages; verb, agentstep, wfdot,
wfgovern, storyfold, workitem, lease, drive and the old ledger are replaced, not
trimmed. Reused as mechanism: store (sqlite open), procgroup, logfile, logsread,
buildinfo, the pi RPC adapter and claude/grok envelope parsing from agentcli,
bashscan's mutation-target extraction, containment's fence.

**Packages**

| package | owns |
|---|---|
| `internal/ledger` | generic hash-chained events, the JSONL history of record, verify, blobs |
| `internal/cache` | sqlite cache of items, events, seats, attachments; rebuilt from the ledger |
| `internal/item` | the item fold (pure) and item ids |
| `internal/workflow` | the workflow schema, parsing (strict), validation, selection rules |
| `internal/agent` | the agents catalog, grants, adapters (claude, pi, grok, exec), dispatch |
| `internal/gate` | running a transition's gate groups: order, fail-fast, parallel, relevance, carry, panels, checks |
| `internal/kernel` | the operations: create, update, transition, attach, seat, release, task run, perform |
| `internal/gitfacts` | the one read-only git adapter |
| `internal/hook` | the edit gate and harness hook envelopes |
| `internal/config` | `satelle.toml` (strict), paths, runtime dir, init template |
| `internal/cli` | cobra commands, thin over `kernel` |

**Configuration layout** (`.satelle/`, in git, owned by the user)

- `satelle.toml` — strict: an unknown key refuses to run. Sections: `[workflows]`
  (`default`, `[[workflows.rule]]` with `workflow` and one of `category`, `tag`,
  `kind`), `[gate]` (`edit_exempt_paths`, `locked_paths`), `[hook]` (`inject` =
  list of files the context hook prints), `[rules]`
  (`reviewer_differs_from_performer`, bool), `[items]` (`prefix` map kind → id
  prefix; default is the kind's first three letters), `[service]`, `[sync]`.
- `agents.toml` — `[agents.<name>]`: `command` (a preset name `claude`, `pi`,
  `grok`, or a full command template; `session` means the interactive session
  performs and nothing is dispatched), `model`, `effort`, `tools` (grant list in
  satelle's vocabulary: `Read Grep Glob Edit Write Bash Bash(<prefix>:*)`),
  `deny` (extra denied tools), `placement` (`local` default, `remote:<id>`,
  `cloud`). Read-only is derived: a binding that judges a gate must have a grant
  of read tools only, and its adapter must enforce grants, else `validate`
  refuses.
- `workflows/<name>.toml` — one workflow per file:

```toml
name = "story"
[states.backlog]
start = true
[states.plan]
performer = { agent = "planner", skill = "plan" }
[states.blocked]
park = true
resumes = true          # a transition to "@prior" returns to the parked-from state
[states.done]
terminal = true

[gates.plan-review]
skill = "plan-review"   # .satelle/skills/plan-review.md
agent = "reviewer"      # or panel = ["reviewer", "reviewer-alt"], combine = "all"
paths = ["**/*.go"]     # relevance: runs only if a changed path matches
trigger = "go-changed"  # relevance: a check skill whose exit 0 means relevant
carry = true            # an earlier accept with the same coverage is carried

[[transition]]
from = ["backlog"]      # "*" = any non-terminal, non-park state
to = "plan"
groups = [
  { mode = "sequential", gates = ["binary-scope", "plan-review"] },  # fail-fast
  { mode = "parallel", gates = ["a", "b"] },
]
```

- `skills/<name>.md` — markdown with frontmatter. A skill whose frontmatter has
  `type: check` is a deterministic check: its first fenced `sh` block runs with
  `SATELLE_ITEM`, `SATELLE_FROM`, `SATELLE_TO`, `SATELLE_BASE` in the
  environment; exit 0 accepts, any other code rejects, output (bounded head and
  tail) is the notes.

**Ledger.** Event = `{id, item, kind, at, actor, payload, prev, hash}`; `hash` =
sha256 over the canonical JSON of the event without `hash`; `prev` is the
previous event's hash on the same item's chain ("" for the first). History of
record: `<runtime>/ledger/<item>.jsonl`, append-only, one file per item, locked
per append. Blobs (attachment bodies) live content-addressed in
`<runtime>/blobs/<sha256>`. `ledger verify` walks every chain. The sqlite cache
at `<runtime>/cache.db` keeps per-file offsets and ingests any tail it has not
seen; `ledger rebuild` drops and rebuilds it. The kernel's own event kinds are
mechanism, not process: `item.created`, `item.updated`, `item.transitioned`,
`gate.run` (`transition`, `gate`, `binding`, `result` accept/reject/skipped/
carried, `notes`, `tokens_in`, `tokens_out`, `duration_ms`, `coverage`),
`performer.run`, `seat.bound`, `seat.released`, `attachment.added`, `task.run`.
A performer answer ending in an `{"artifact":{name,type,body}}` object also
appends `attachment.added` named by the artifact (media type
`text/markdown; type=<type>`); a malformed artifact fails the run and attaches
nothing (sto_42f9cae5). Any other kind may be appended by `ledger append` and is carried, never read.

**Items.** Fields: id, kind, title, body, criteria, status, tags, parent,
category, workflow, assignees (a list; every user holding a live seat is
also an assignee, marked working). The workflow is
chosen at create (explicit `--workflow`, else the first matching rule, else the
default) and stamped on the item. Status starts at the workflow's start state.
An item that arrives from the server without a workflow (satelle-server never
decides process) is placed on first pull by the same rules, as item.updated
{workflow} then item.transitioned {from, to: start} by actor `satelle`,
appended to the server first so every home agrees (sto_aca60d04).

**Transitions.** `story set --status X` finds the explicit transition from the
current state, runs its groups in order (sequential groups stop at the first
reject; parallel groups run every gate), records every gate on the ledger
(including skipped and carried), and appends `item.transitioned` only when all
accept. Coverage for carry = sha256 of the item's body, criteria and
attachments plus `git diff <base> -- <gate paths>`; base is the seat's commit,
else HEAD at create.

**Seats.** `seat.bound` = `{session, location, base}`; location is `local:<path>`,
`remote:<node>` or `cloud:<url>`. No cap on seats. The edit gate allows an edit
when the editing session holds a seat on an item whose current state has a
performer and the seat's location is this worktree; `[gate] edit_exempt_paths`
are always allowed and `locked_paths` are never allowed by a seat.

**Tasks.** A task item carries `inputs` (a selection: `kind`, `status`, `tag`,
`category`). `satelle task run <id>` creates an execution item (parent = the
task) with `task.run` = the item ids taken, under the workflow the rules pick.
