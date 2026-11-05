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

| | v0.0.122 (2026-07-05) | now (v0.0.896) |
|---|---|---|
| internal packages | 19 | 76 |
| Go, non-test | ~18k lines | ~180k lines |
| Go tests | ~20k lines | ~279k lines |

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
   binary starts a fresh ledger; no migration or import is built.
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
  ships only its help text and the two-state init file. BDD, story shape, plan
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

| package / command | kept · deleted · moved | why (which kernel item, or why not) |
|---|---|---|

## Wire changes for satelle-server

## Open questions for the operator

## Audit
