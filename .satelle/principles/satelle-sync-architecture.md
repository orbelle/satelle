---
name: satelle-sync-architecture
type: principle
tags: [type:principle]
applies_to: ["*"]
description: The target architecture for replicating satelle's work state (epic sty_5345a888) — work state is a server-sequenced ledger, configuration lives in git and never syncs, scope comes from the project binding, and every slice deletes what it supersedes and introduces no new warning in any of the machine's repos, every remaining finding owned by a named story. The architecture review judges each story's fit against it and may push back an amendment.
---

# Sync architecture

satelle has few users, so breaking changes are allowed now. This is the shape
every sync-touching story is planned and reviewed against. A change that does
not fit is rejected, or the architecture is amended first (see "Amending").

## 1. One source of truth per fact, one transport per kind

| Data | Model | Source of truth | Transport |
|---|---|---|---|
| Work state (stories, executions, ledger events) | **Ledger**: one append-only chain per story | the chain | For satelle.dev, the Chain service: `Append` (compare-and-append on the base), `Read` and `Subscribe` (one cursor per story). For a `[ledger] store` command, one object per event at `chains/<item>/<seq:012d>.json` written by conditional create and read back verified (`satelle ledger push --session <session>`, `pull`), plus its blobs at `blobs/<sha256>`. Both keep the same rule: a different hash at a sequence is refused, never merged |
| Configuration (`.satelle/` workflows, principles, skills, constitution, agents, queues, settings) | Files in the control repository (in N=1, the repo itself; [[satelle-control-plane-architecture]]) | git | git only; ledger replication never carries it |

- A story row, local or hosted, is a **projection** of its chain, rebuilt from
  the chain wherever the chain is held. The server derives its rows from the
  chain in a job enqueued in the append transaction. A machine that does not
  hold a story's chain holds the server's derived row, read-only: never pushed,
  never a base for an append, and replaced by the rebuild once the chain is
  fetched (§3, Holdings).
- Ledger replication (`satelle ledger push --session <session>` and `pull`) carries work state
  only: each item's chain of events, item summaries, attachment bodies and
  performer outputs. Configuration moves between instances
  through the repo's git, which is a step's work
  ([[satelle-mechanism-vs-substrate]]); nothing is overwritten silently.
- A second path for the same data (rows beside the chain, a page ledger beside
  the Chain service, a copy beside an overlay) is a defect, not a feature.

## 2. Many instances of one repo

- A **location** is one enrolled machine × checkout. Enrollment uses a
  single-use code, and the device generates its own key; the server keeps only
  the public key. The checkout is a worktree of a repository its control
  repository registers, the control repository itself (`self`) included; in
  N=1 that is the repo's own worktree ([[satelle-control-plane-architecture]]
  §2, §3).
- Offline appends wait locally and are pushed by the next automatic pass once
  the store is reachable. An append that forks a chain another location
  extended is re-based onto the store's head when every local event after the
  fork commutes with each of the store's newer events, by a closed allowlist
  (`item.Commutes`, sto_ebdd6380): a seat release, and a queue add the store's
  newer events neither claimed nor kept. The re-based events keep their ids and
  content, and the same pass pushes them. Any other fork is a conflict: nothing
  is written, the item is reported naming both events, and the local events
  stay for `satelle ledger adopt --session <session>`. Events commute, except **status
  transitions**: a transition must match the story's fold at its position (its
  `from` is the folded status), or it is a chain break, always reported. A
  transition is never re-based, so one whose `from` no longer matches the
  store's fold is reported as a conflict. A break never moves a story out of a state its
  governing route marks final (terminal, or a park that does not resume).
  Elsewhere it applies out of order, so a legacy chain linked once in
  insertion order still folds to the status its transitions reached.
- Only two locations moving the **same** story is a real conflict, and the
  chain itself refuses it: an append must build on the latest event the server
  holds, so the second location's push is refused, reported as a conflict and
  left alone, and it re-reads.
- A **seat** (`satelle story seat`) is a `seat.bound` event on the item's
  chain that binds a session at a location (a worktree) to the item; `satelle
  story release` ends it. The edit gate enforces seats: a session's edit is
  allowed only while it holds a seat at that location on an item whose current
  state's performer may change the tree; a seat on a state that allows
  attachments only (`edits = "attachments"`) refuses every tree edit there.
  The location must be a worktree of the item's target repository (its
  set-once `repo`): the gate allows a seated target's tree and refuses every
  other repository's, failing closed. In N=1 the target is the session's own
  repository, so the rule is the cross-repo fence; for a control repository it
  is the allowlist of [[satelle-control-plane-architecture]] §6, which
  sto_5c68f06c brings; a protected repository is seated only by an
  interactive session on an item that targets it (§1 there). A seat is not a
  lock: several sessions may hold seats
  on one item, and there is no lease. Changes to one area of code run one after
  another ([[satelle-start-small]]). An edit belongs to the item seated at the
  worktree that holds its target path, and what it may do follows from that
  item's status and the workflow. Server-side location holds are removed.
- Other online locations see a change within seconds: `satelle ledger follow
  --session <session>` (the `satelle-follow` service) holds the store's `Chain.Subscribe` open and
  runs the same sync pass on each local write and each streamed event it does
  not hold, never applying a streamed event directly (sto_e4a6f77f).
  `satelle-sync.timer` stays the fallback: its pass pushes and pulls whatever
  the subscription missed. `satelle ledger follow --help --session <session>`
  names the reconnect and retry intervals.

## 3. Scope is the project binding

- **Local:** the repo is unbound or the user is not signed in. Nothing leaves
  the machine. This is the default.
- **Personal:** bound to a project in the user's personal workspace. All work
  state replicates with it (`satelle ledger push --session <session>` and `pull`, satelle.dev being
  the default `[ledger] store` while signed in); the `.satelle` tree stays in
  git.
- **Team:** bound to a project in a team workspace. Members see the work state
  according to their role (admin, contributor or viewer).
- `*.local.*` files never leave the machine, in any scope.
- Scope is set by command (`satelle project bind`), never by hand-editing. Per-area
  `[sync]` keys and the `shared` scope are removed.

**Holdings.** Every machine holds every story's row of its project, so its
lists are complete. Local and personal scope also hold every story's chain (a
complete local store). Team scope holds a story's full chain only while the
story is in a performing state of its governing route, whichever location works
it, or after its history is requested (opened); otherwise the row alone (§1).

**One UI.** The local web UI is the only story UI. The server is storage and an
API; it serves no story pages for the local UI to link to.

**Configuration is the repo's.** satelle reads its configuration from the
control repository's `.satelle/` alone (`satelle.toml`, `agents.toml`,
`workflows/`, `skills/`, `queues/`), in every scope; in N=1 that is the repo's
own, and a target repository holds none
([[satelle-control-plane-architecture]] §2). No hosted or user-level copy
layers over it, and nothing satelle replicates writes into the tree; a change
to the configuration reaches other instances, and a team, through the control
repository's git.

**One project, its repositories.** A control repository binds one project.
The project binds one or more GitHub repositories (each an installation and a
repository); in N=1 it binds the one. A publish to GitHub names the item, and
satelle-server takes the repository from the item's set-once `repo`, checks it
is one the project binds, and acts on that repository alone; an item naming
none in a project with one binding resolves to that binding. A caller never
names the repository. This is the rule sto_d9427fec and its satelle-server
counterpart are held to (§6 rule 4).
`~/.satelle/config.toml` holds this machine's settings (`satelle settings --session <session>`),
not process.

## 4. Commands

The sync surface is `satelle init --session <session>`, `satelle login --session <session>`, `satelle project bind`
and three ledger verbs for work state, the only replication path: `satelle
ledger push --session <session>` (offer the local events the store lacks, with their blobs; a
diverged chain is reported and left alone, exit 1), `satelle ledger pull --session <session>`
(import the store's events the local ledger lacks, and their blobs; a fork is
refused) and `satelle ledger status --session <session>` (each item's local and store head). Each
takes item ids to replicate only those. They replicate to the `[ledger] store`:
`"satelle.dev"` (the default while signed in, over the Chain and Attachment
services), `"local"` (the default otherwise; nothing replicates) or an adapter
`{ command, args }` (proven by `satelle ledger store-test --session <session>`, which checks only a
configured command). Every store keeps the same rule: a different hash at a
sequence is refused, never merged. The one exception is a file-defined
queue's chain whose local events past the fork point are only satelle's own
file-mirror events: no work was done past the fork, so it takes the store's
chain for that id and the file is mirrored onto that (operator, 2026-11-13;
sto_80da4589; widened on 2026-11-25 from a chain holding only mirror events to
its tail past the fork point, sto_7a47551c). A fork holding any other local
event is refused, and the only way out of it is the operator's
`satelle ledger adopt <item> --session <session>` on named items, which takes
the store's chain and keeps the local events it sets aside (sto_fc858c43); its
mechanics are in `satelle help ledger-store --session <session>`. A missing blob is a recorded gap, and
`satelle ledger verify --session <session>` exits 3 while one remains. The hot path never contacts
the store: hooks, gates, and story and ledger reads work with the store
unreachable. Remote paths (`satelle remote fire`, a remote placement, a node,
`satelle remote status`) replicate only their run's item through satelle-server,
whatever `[ledger] store` names, with the same replicator. `satelle service
install --session <session>` also installs `satelle-sync.timer`, which runs the internal verb
`satelle ledger autosync --session <session>` every 30 s: for each project on the machine whose
store is not local it pushes then pulls through the same replicator and records
the outcome, which satelled's project page shows. No one runs a command for a
machine to converge. The same pass may also run doctor's provable fixes that
need no answer: releasing a seat whose item is in a terminal state or whose
session is gone, and restarting satelled on a version skew. It logs each
action it takes, and what it leaves is reported to the operator at session
start (operator, 2026-11-13). Replication stays the only path for work state:
a fix the pass makes is a ledger event like any other and replicates through
push and pull. The former sync verb
and its state file were removed (sto_9b98696a).
Every other sync, reconcile, rehydrate, deploy, bindings, publish or repair
verb is internal or removed. The checks (`satelle validate --session <session>`, `satelle status --session <session>`)
report; they do not repair.

## 5. Server

Postgres is the store, the sequencer and the signal: unique `(project, story,
base)` and `(project, story, seq)` constraints order each chain, `pg_notify`
inside the append transaction feeds `Subscribe`, and River jobs (enqueued in the
same transaction) do derived work such as row projection and team fan-out.
Everything is keyed by project. No new message bus: NATS stays only for the live
remote command channel. The server order, from its own review: remove holds;
key by project; River and the derive job; story data out of Snapshot.

## 6. Rules every slice carries

1. **Remove what you supersede, in the same story.** The code path, verb, flag,
   table, column, config key, test and help text the change replaces are
   deleted, with their callers migrated. No compatibility shim, dual path,
   deprecated alias or "kept for now".
2. **Declare breaking changes.** A `### Breaking` entry in the release notes states what
   broke and the exact command that heals it. `init-heals:` only when `satelle
   init --session <session>` alone heals it.
3. **No new warnings across the machine.** The binary and the web service are
   machine-wide and serve every registered repo at once; one `satelle update --session <session>`
   moves them all, including sessions already running. A slice introduces no
   validation error and no satelle session-start warning in any registered
   repo (`satelle project list --session <session>`): it is proven by `satelle validate --session <session>` and a
   session start in each registered repo, captured before the release and
   after `satelle update --session <session>` (and `satelle init --session <session>` where the release's breaking
   change says it heals), and attached to the story. Every validation error or
   warning present after the release is
   either fixed or pre-existing and owned by a named story; none is unowned.
4. **Both repos.** A change that spans client and server names its counterpart
   story in satelle-server, and neither lands a dual path waiting for the other.

## Amending

This document is the plan, not a cage. When the architecture review finds a
story right and this architecture wrong, it rejects with a finding headed
**architecture amendment** that names the section and the proposed text. The
driving session then amends this principle under a `substrate` story (so the
change is reviewed like any other), and re-presents the plan against the
amended text. A plan never departs from this document silently.

See [[satelle-mechanism-vs-substrate]].
