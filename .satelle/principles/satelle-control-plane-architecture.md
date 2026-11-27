---
name: satelle-control-plane-architecture
type: principle
tags: [type:principle]
applies_to: ["*"]
description: The target architecture for a control repository governing several repositories (epic sto_5afbd604). A control repository is process scope, never a security boundary: security is each registered repository's tier (standard or protected) and three levels, infrastructure, satelle's hooks and gates, and principles. Its .satelle is the control directory, and each item's set-once target repository is the work directory that git facts, performers, the edit gate, queues and the GitHub binding act on. With no .satelle/repos.toml a repository is its own only target, self, on the same code path. A skill reaches its substrate through SATELLE_SUBSTRATE_DIR. A dispatched performer is confined per run or dispatched read-only, and never writes a protected repository; an interactive session is gated from the control repository, and a forge-level check binds to the item's repository.
---

# Control-plane architecture

This is the shape every story of the external control-plane epic
(sto_5afbd604) is planned and reviewed against, beside
[[satelle-sync-architecture]], which points here where it changed. These are
the rules the epic's slices are held to; which slices are built is the state
of the epic's stories (`satelle story list --parent sto_5afbd604 --session
<session>`), not this page. Every rule here reduces to the N=1 case (§3),
which is how a repository without the slices behaves. A plan that does not fit
is rejected, or this page is amended first, as [[satelle-sync-architecture]]
"Amending" says.

## 1. Process scope, the tier rule and three enforcement levels

- A **control repository** holds the `.satelle/` that governs a set of
  repositories: their workflows, skills, principles, epics and ledger, under
  one project binding. Where it governs several repositories it is a
  repository of its own: it holds the substrate, the code repositories are
  its targets and hold no `.satelle/`, and its own `self` target is that
  substrate. A registered repository that had a substrate of its own has it
  merged in as that repository's specific part.
- A substrate is process scope, never a security boundary. Choosing a
  repository's constitution or skills for a run is for relevance, not
  isolation.
- Every agent may read the ledger's items for every registered repository:
  awareness across repositories is intended, not a leak.
- A control repository that takes over repositories starts on a clean
  ledger; importing a ledger it replaces is later and optional.
- A local checkout is onboarded by one control repository; two claiming it is
  an error doctor reports (sto_24387c93).

**Three enforcement levels.** Security rests on three levels, each relied on
only for what it can hold:

- **Level 0, infrastructure:** identity, credentials, the host and the
  sandbox. The operator provisions it outside satelle, and it needs no
  substrate change. It is the one level relied on against a deliberate actor;
  satelle neither provisions nor replaces it.
- **Level 1, satelle's hooks and gates:** seats, the edit gate, dispatch
  grants and the workflow's gates. They are relied on to keep an agent on its
  route and its edits on the seated target; they run as the agent's own OS
  user, so they are not relied on against a forgery of satelle's runtime
  (`satelle help edit-gate --session <session>`).
- **Level 2, principles:** this page and its siblings. They direct agents and
  judges and are what plans are reviewed against; nothing relies on them to
  enforce.

**The tier rule.** Each registered repository has a tier, `standard` or
`protected`. A standard repository gets the rules of §2 to §6. For a
protected repository level 1 also enforces that:

- no dispatched performer holds a write grant into it;
- a seat at it is held only by an interactive session on an item whose `repo`
  names it;
- its changes land only through a pull request the operator approves.

A write a dispatched performer, or a session not seated on an item for it,
attempts in a protected repository is refused, and the refusal names the
tier.

## 2. Control directory and work directory

- The **control directory** is the control repository's `.satelle/`: the
  configuration, the ledger and its project binding, the harness hook files
  `satelle agents install` writes, and the untracked `.satelle/.env` a remote
  reads ([[satelle-agent-operates-remotes]]).
- A **work directory** is a worktree of a registered repository: its code, its
  history and its refs. A target repository holds no `.satelle/` and no
  satelle hook file.
- Repositories are registered by name, canonical origin and tier (§1) in the
  tracked `.satelle/repos.toml` and mapped to this machine's checkouts in the
  untracked `.satelle/repos.local.toml`; the fields are sto_24387c93's. `self`,
  the control repository, is always a valid target.
- What acts on code follows the item's work directory: git facts, the diff
  base and plan-touches (sto_57932ef8), a dispatched performer's working
  directory (sto_0b98d82c), the edit gate's allowlist (§6, sto_5c68f06c) and
  queue membership (sto_530e3e17). What governs follows the control directory.
  A linked worktree resolves to the same control directory and target
  repository as its main checkout, with itself as the work directory
  (sto_df2091c3).
- Git stays read-only in every work directory: the binary reads a registered
  repository (status, diffs, merge bases, a bundle written outside it) and
  never writes it, its refs or its remotes, a fetch included. A fetch that
  moves a target's refs is a step skill's action, or the fetch command a
  registry entry configures, run as that configuration under the user's own
  git credentials; satelle holds none ([[satelle-mechanism-vs-substrate]],
  [[satelle-operator-rulings]]).

## 3. N=1 is self, on the same path

A repository with `.satelle/` and no `.satelle/repos.toml` is a control
repository whose only target is itself, `self`, implicitly: the control
directory is `<worktree>/.satelle` and the work directory is that worktree.
One resolver serves both cases; there is no in-repo path beside it, and no
existing project is made to migrate. A single-repo project keeps its repo
key, runtime path, satelle.dev binding and stories; the standing suite
(sto_8394d46d) is the proof each binary-changing slice runs at its gate.

## 4. An item's repository is set once

An item names its target repository in `repo`, set when it is created (`self`
when omitted) and validated against `.satelle/repos.toml`. It does not change
once the item has held a seat. An item whose chain records no repo is `self`,
read without appending an event; one naming a repository the registry lacks is
shown with that name and refused a seat (sto_2c7df39d). satelle-server reads
the same field to choose the GitHub repository (§6).

## 5. A skill reaches its substrate by variable

Every check, setup command, dispatched performer and judge satelle runs gets
`SATELLE_SUBSTRATE_DIR`, the control directory governing the run
(sto_bb202344; `satelle help workflows --session <session>`). A skill reads
the substrate through it, never through a `.satelle/...` path relative to its
working directory, because a target repository's worktree holds none. In N=1
the two name the same directory.

## 6. Gating guarantees

The edit gate is an allowlist: a session's tree edit, by a file tool or a
shell write alike, is allowed only inside the worktree of the target
repository of an item it holds a tree-edit seat on, subject to that item's
state, `[gate] locked_paths` and `edit_exempt_paths`. Every other
repository's tree is refused, and a seat whose target has no mapped checkout,
or whose checkout's origin does not match the registry, refuses every edit,
failing closed (sto_5c68f06c). In N=1 the allowlist is the session's own
repository, which is the fence `satelle help edit-gate --session <session>`
describes. What carries the rule to a tool call depends on who launched the
agent:

- **A dispatched performer.** satelle builds its argv and environment, so it
  can inject confinement for one run with no file in the target. A protected
  repository never gets a write grant (§1). A harness is dispatched with a
  write grant into a standard target repository other than `self` only when
  its recorded probe shows per-run confinement that keeps every
  write its grant allows, by file tools and shell, inside the seated target's
  worktree and the temp directories. A harness whose record shows
  confinement that leaves any other path writable, shows none, or holds no
  observation is dispatched there read-only: no edit, write or shell in its
  grant. Which harness is which is the record, not this page:
  `.satelle/tasks/harness-confinement.md` and its `recorded/<harness>.txt`,
  re-run by that task after a harness upgrade, and the argv each adapter
  builds in `internal/integration/<harness>/`.
- **An interactive session.** satelle does not launch it and injects nothing;
  it is gated only by the hook files its harness loads. A session that edits
  a target repository runs in the control repository, where those files are,
  seated on the item, and the gate judges each path against the seated
  target. This is the only way a protected repository is seated (§1). A
  session started inside a hookless target repository loads none of
  satelle's hooks and carries no satelle guarantee; user-home hooks that would
  close that gap are deferred by the epic until evidence asks for them.
- **A forge-level check.** A review or commit status satelle publishes
  (`satelle github review --session <session>`, `satelle github status
  --session <session>`) goes to the item's repository only: the project binds
  one or more GitHub repositories, and satelle.dev resolves the one to act on
  from the item's set-once `repo` against them, never from a repository the
  caller names (sto_d9427fec and its satelle-server counterpart). It is the
  one check that holds whichever session or harness produced the change, so a
  target repository that depends on it requires it in its own forge ruleset,
  which is that repository's configuration, not satelle's. A protected
  repository's change lands only through a pull request the operator
  approves (§1).

See [[satelle-mechanism-vs-substrate]].
