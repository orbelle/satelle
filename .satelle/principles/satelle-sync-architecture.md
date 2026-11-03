---
name: satelle-sync-architecture
type: principle
tags: [type:principle]
applies_to: ["*"]
description: The target architecture for satelle sync (epic sty_5345a888) — work state is a server-sequenced ledger, substrate syncs git-like, scope comes from the project binding, and every slice deletes what it supersedes and introduces no new warning in any of the machine's repos, every remaining finding owned by a named story. The architecture review judges each story's fit against it and may push back an amendment.
---

# Sync architecture

satelle has few users, so breaking changes are allowed now. This is the shape
every sync-touching story is planned and reviewed against. A change that does
not fit is rejected, or the architecture is amended first (see "Amending").

## 1. One source of truth per fact, one transport per kind

| Data | Model | Source of truth | Transport |
|---|---|---|---|
| Work state (stories, executions, ledger events) | **Ledger**: one append-only chain per story | the chain | Chain service only: `Append` (compare-and-append on the base), `Read` and `Subscribe` (one cursor per story) |
| Substrate (`.satelle/` workflows, principles, skills, constitution, agents, tasks, settings, documents) | **Git-like**: a snapshot DAG per project (tree hash + parents) | the DAG head | snapshot exchange; the client merges |

- A story row, local or hosted, is a **projection** of its chain, rebuilt from
  the chain wherever the chain is held. The server derives its rows from the
  chain in a job enqueued in the append transaction. A machine that does not
  hold a story's chain holds the server's derived row, read-only: never pushed,
  never a base for an append, and replaced by the rebuild once the chain is
  fetched (§3, Holdings).
- Substrate sync fast-forwards when one side is behind. When both sides moved,
  different files merge automatically and the same file merges three ways. The
  user is asked only on a true same-file conflict, and nothing is overwritten
  silently.
- A second path for the same data (rows beside the chain, a page ledger beside
  the Chain service, a copy beside an overlay) is a defect, not a feature.

## 2. Many instances of one repo

- A **location** is one enrolled machine × checkout. Enrollment uses a
  single-use code, and the device generates its own key; the server keeps only
  the public key.
- Offline appends wait locally and re-base onto the server head on reconnect.
  Events commute, except **status transitions**: a transition must match the
  story's fold at its position (its `from` is the folded status), or it is a
  chain break, always reported. A break never moves a story out of a state its
  governing route marks final (terminal, or a park that does not resume).
  Elsewhere it applies out of order, so a legacy chain linked once in
  insertion order still folds to the status its transitions reached.
- Only two locations moving the **same** story is a real conflict, and the
  chain itself refuses it: an append must build on the latest event the server
  holds, so the second location's next append is refused and it re-reads.
  There is no seat or lease. A `working_on` event is advisory presence, shown
  to other locations, never a lock. How many stories may be in a performing
  state at once is configuration (an epic or project may set one at a time;
  changes to one area of code run one after another, [[satelle-start-small]]),
  counted from the chains. An edit, commit or push belongs
  to the story whose worktree holds its target path, and what it may do follows
  from that story's status and the workflow. Server-side location holds are
  removed.
- Other locations see a change through `Subscribe` within seconds, not only at
  the next pull.

## 3. Scope is the project binding

- **Local:** the repo is unbound or the user is not signed in. Nothing leaves
  the machine. This is the default.
- **Personal:** bound to a project in the user's personal workspace. The whole
  `.satelle` tree and all work state sync with it.
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

**Layering, the same for every substrate kind** (highest wins):
`*.local.*` > project tier > repo > user (`~/.satelle`) > embedded.

- **Project tier.** In a bound repo, the team-owned paths — `[team] paths` in
  `satelle.toml`, globs under `.satelle/`, by default `workflows/agents.toml`,
  `workflows/**` and `skills/**` — resolve from the project's deployed hosted
  version, held at `<runtime>/project-tier/`. A tree copy of a team-owned path
  does not govern, even a file that exists only in the tree. Session start and
  bare `satelle sync` write the tier's copy into the tree, backing up a local
  edit first. A local edit becomes the team version only by
  `satelle sync config push` then `deploy`.
- `agents.local.toml` and `satelle.local.toml` override the tier, and are
  never written by it.
- For a team-owned `agents.toml`, `sync bindings` stands down: push refuses
  and pull writes no `agents.workspace.toml`. No sync area other than the
  project's config store carries a team-owned file.
- An unbound repo, or a bound project whose deployed version holds no
  team-owned file, has no project tier.

## 4. Commands

`satelle init`, `satelle login`, `satelle project bind`, `satelle sync` (bring
everything into step; stop only on a real conflict and ask) and `satelle sync
status` (ahead, behind or conflicted, for substrate and work state). Every other
sync, reconcile, rehydrate, deploy, bindings, publish or repair verb becomes
internal or is removed. `satelle doctor` reports; it does not repair.

## 5. Server

Postgres is the store, the sequencer and the signal: unique `(project, story,
base)` and `(project, story, seq)` constraints order each chain, `pg_notify`
inside the append transaction feeds `Subscribe`, and River jobs (enqueued in the
same transaction) do derived work such as row projection and team fan-out.
Everything is keyed by project. No new message bus: NATS stays only for the live
remote command channel. The server order, from its own review: remove holds;
key by project; River and the derive job; story data out of Snapshot; the
substrate DAG on the content-addressed config store; the team overlay.

## 6. Rules every slice carries

1. **Remove what you supersede, in the same story.** The code path, verb, flag,
   table, column, config key, test and help text the change replaces are
   deleted, with their callers migrated. No compatibility shim, dual path,
   deprecated alias or "kept for now".
2. **Declare breaking changes.** A `### Breaking` entry in CHANGELOG states what
   broke and the exact command that heals it. `init-heals:` only when `satelle
   init` alone heals it.
3. **No new warnings across the machine.** The binary and the web service are
   machine-wide and serve every registered repo at once; one `satelle update`
   moves them all, including sessions already running. A slice introduces no
   FAIL and no satelle session-start warning in any registered repo: it is
   proven by `satelle doctor --all` and a session start in each registered
   repo, captured before the release and after `satelle update` and `satelle
   init --all --yes` (without `--yes` it is a dry run that heals nothing), and
   attached to the story. Every FAIL or warning present after the release is
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

See [[satelle-mechanism-vs-substrate]], [[satelle-no-drifting-docs]].
