# Epic wave: which children may start now

`satelle story wave <epic-parent-id>` prints the children of an epic that may
start now, one id per line. It is an **assessment only**: it writes no status,
lease or tag, and it does not engage, dispatch, open a worktree or spawn an
agent.

`--json` prints the same response as one object, for a script. `runnable` is the
list of child ids that may start (the ids the text form prints) and `omitted`
lists every other non-terminal child; both are always present, as empty arrays
when nothing qualifies. Each `omitted` entry has `id`, `cause`
(`waiting_on_dependency` or `no_route`), `waiting_on` (for a dependency hold,
each blocker's `id`, `status` and `cancelled` flag) and the prose `reason`.
`container`, `theme` and `schedule` name the epic. A refusal still exits
non-zero and prints no object.

## What it reads

- **Membership** is the epic set — every story carrying the epic's `epic:<theme>`
  tag, the same set close uses. Not `parent_id`, not a second list.
- **Schedule** is **parallel by default** — a container that declares nothing runs
  its children in parallel. The override is `sequential = true` on the container's
  `waits_on_children` step in `step.toml`, which runs one child at a time. The older
  `schedule = "parallel"` / `"sequential"` key still works with the same meaning;
  `satelle validate` warns that it is deprecated.
- **Edges** are the `depends-on:<story id>` tags on the children.

## The wave

A child is runnable when it is not terminal (not done, not cancelled) and every
`depends-on` target is **done by that story's own route**. A child with no
`depends-on` is eligible. A dependency that is in progress, blocked, missing or
**cancelled** does not satisfy the edge — a cancelled story's commits are not
there. The child stays out of the wave and a `# omitted <id>: waiting on <dep>`
line names the dependency (a cancelled one is called out as cancelled).

- **parallel** (the default) returns every runnable child.
- **sequential** (`sequential = true`) returns the runnable set only when it holds exactly one id. With
  more than one it exits non-zero, names those ids, and says to add `depends-on`
  edges. It never picks one by `order:`, `created_at` or title.

## Refusals (non-zero exit, no id printed as runnable)

- the container's route declares two different schedules and the container's status
  selects neither (no schedule at all is not a refusal — it is parallel);
- two epic-parents carry the epic tag;
- the container has no `epic:<theme>` tag (or more than one);
- the story is not an epic-parent.

There is no fallback to `order:` or to every child — the declaration is the
authority.

## Depending on a story in another repo

`depends-on:<repo>/<story id>` (for example `depends-on:satelle-server/sty_620c56ea`)
holds a child until a story in **another registered repo** is finished — for a
change that must not ship before the other repo's does. `<repo>` is the repo's
registry name: the last segment of its path in `satelle workspace list`, the same
name the web partition shows. It is not the hosted project slug.

- **Write time** checks only the form: `<repo>` must match exactly one registered
  repo, not this one (a worktree counts as its main tree), and `<story id>` must
  be a story id. Whether the story exists is wave's question. A cross-repo target
  is outside the epic-set and cycle checks, and a story whose only dependencies
  are cross-repo needs no epic set. There is no cross-repo cycle check.
- **Satisfied** only when the other repo's story is terminal on **its own
  route** and every **container** above it — its `parent_id` chain and the
  epic-parent of each `epic:<theme>` it carries — is terminal too. A child's done
  does not carry the release; the container's close does.
  A done story tagged `lane:trunk` is the exception: it released to trunk itself,
  so it satisfies the edge whatever its containers' state. Otherwise the child is
  omitted, and the line reads `<repo>/<id> (<status>)`, or
  `<repo>/<id> (done; container <id> open)` when the story is done under an open
  container. A cancelled story is called out as cancelled, as for a local one.
- **Unresolved is a blocker.** A repo that is not registered, an ambiguous repo
  name, a missing `.satelle/` or database, a workflow that cannot be read, a route
  that governs no story, or a story that is not found makes the dependency
  `unresolved: <why>`. Wave still exits zero and omits the child; it never treats
  an unreadable dependency as satisfied.
- **Read-only.** The other repo is read in-process: its database is opened
  read-only (nothing created, nothing migrated) and its workflows are read from
  disk (nothing indexed). No command runs in it and its tree is not touched.

Cut a child that depends across repos from trunk; its dependency's change lives in
another repo and is not on any branch here.

## A child waits for its epic to be ready

A child cannot leave backlog into an engaging status (`satelle story set <child>
--status plan`, or `story create` straight into one) until its epic's readiness
round holds. That round is what sequences the children — it adds the
`depends-on` edges and the `lane:` tags — so a child engaged before it runs gets
neither. The refusal names the epic and the command that readies it, `satelle
story ready <epic>`, which judges the epic and, unless its readiness step declares
`ready_children = false`, also runs each backlog child's readiness round (no round
changes a status), and is not refused itself. The check keys on the child's entry state, so a child on any
route is covered, but only when the epic's own route declares a readiness step: a
step entered from its start that declares `waits_on_children`. The default
substrate's epic route declares one (`ready`, performed by `ready-reviewer`), so a
repo with no workflow of its own is gated; an overlay whose epic route omits it
is not, and `satelle story ready <epic>` fails there with "declares no readiness
step". A story in no epic set, and the epic itself, are unaffected, and a child
already mid-flight keeps moving. Leaving
backlog without engaging stays open: you can still `cancel` (or park) a child of
an epic that is not ready.

When the epic's readiness step declares `ready_children = false`, `satelle story
ready <epic>` plans no child: children are planned when the wave lets them start,
one wave at a time, each against the code its dependencies delivered.

## Engagement honours the wave

Entering an engaging status (`satelle story set <child> --status plan`) is
refused for a child of an epic that the wave would not return, and the refusal
quotes the wave's reason: the dependency that is not done, the sequential wave
that is wider than one (add `depends-on`), or that the story is not in the epic
set. A child the wave returns still meets the seat mode and the
one-engagement-per-worktree rule, so two eligible siblings co-engage only from
distinct worktrees. Only a *new* engagement is checked; a child already
mid-flight keeps moving.

**Seats.** `[engagement] parallel = "epic"` lets siblings hold seats alongside
each other (and nothing else); `"worktree"` lets any stories do, from different
epics or none, one seat per git working tree. With the parallel default siblings
do, from distinct worktrees. A child's epic is the one the wave places it under —
its `parent_id`, or the container of an `epic:<theme>` it carries — so siblings
joined by tag alone share the epic's seat key. `sequential = true` narrows both
modes: the epic's children then go one at a time (under `epic` they key on their
own ids; under `worktree` they share the epic's key and exclude each other, while
unrelated stories still co-hold). Under `parallel = "none"` the schedule changes
nothing about seats. Stories that are not children of an epic are unaffected
under `epic`. See `satelle help workflow-convert` for `sequential` and
`depends-on`.

## A container step that waits on a child obligation

A container step may declare `after_children = "<obligation>"` in `step.toml`.
Entering it is refused until every child has discharged that obligation on its
own route; the refusal names each child that has not, with its status. A child
has discharged it when it is done or cancelled, or sits at or past the step of
its own route that provides the obligation. A child whose route has no such step
holds the container back, and the refusal says so.

The step may name a performer. Entering it takes a seat for the container, under
the epic key its children already share, so a child that has discharged the
obligation can still engage its later steps from another worktree while the
container holds it — still one worktree per lease. That needs
`[engagement] parallel = "epic"`: in `none` mode the container and a child hold
different keys and conflict. The container's `waits_on_children` step still
holds no seat. Without `after_children`, container engagement is unchanged, and
`children-resolved` still requires every child terminal.

The binary only gates the entry and the seat. It runs no git command; a merge is
a skill the step names.

## Where a child runs

A child's step runs locally unless its step declares otherwise. A performer step
in `step.toml` may declare `remote_agent = "<binding>"` (a `role = "agent"`,
`interface = "cloud"` binding in `agents.toml`) and `local_tags = ["<tag>", …]`.
The step is then performed by `remote_agent`, in a cloud session, when **all** of
these hold:

- the child belongs to a parallel epic — the default, so any container that does not
  set `sequential = true` (the wave's own membership and schedule — a sequential epic, or a story that is
  not an epic child, keeps the step's own `agent =`);
- the child carries none of the step's `local_tags` (a tag the repo uses to pin a
  child local, such as the children that edit shared substrate);
- the session is **signed in**. A local-only session performs the step with its
  own `agent =` and records the ledger note
  `placement: remote declared, local used — not signed in`. A git email is not
  a sign-in.

A step with no `remote_agent` is unchanged, and the epic's merge never moves: a
container step (`waits_on_children` or `after_children`) cannot declare
`remote_agent`, so integration and release stay local. Whether a step *can* run
remote is therefore declared configuration (`remote_agent`, or a binding's
`placement = "remote"`), never a decision of the agent driving the epic.

**The orchestrator places each child.** Where each child runs is the
orchestrator's choice, made at the epic's readiness round: `in-loop` (the driving
session), `local` (a headless subagent) or `team:<path>` (a team remote). It
sends the ready performer one line per child with its reason
(`satelle story message <epic> --from orchestrator --to ready --body …`); the
round records the reason under the ready step's `delivery_heading` and the
placement as a tag under its `placement_tag` prefix (`placement:in-loop`,
`placement:local`, `placement:team:<path>`). The delivering steps declare the same
`placement_tag`, so a pinned child is placed by its pin at dispatch, ahead of
`local_tags` and `remote_agent`. `remote` is not a pin: the round refuses it, and
a dispatch fails naming a pin it cannot honour. `satelle story wave` prints what
the engine resolves for each open child — `# delivery <id>: <order>, lane=…,
step=…, <placement>, agent=… — <reason>` — so the choice is read, not guessed.

**The driver pushes first.** A cloud session is based on the child's pushed
branch, and satelle never pushes for you. Before presenting a remote child's
performer step, push its worktree branch: `git push -u <remote> <branch>`. If you
forget, the dispatch is refused before anything launches, naming the child, the
remote placement and that command.

**Rework is refused for a remote child.** A cloud session is one-shot and cannot
be a relay partner, so `satelle story rework` on a child placed remote is refused
and ledgered (`rework_refused`). Re-present the edge instead: that launches a
fresh cloud session carrying the reviewers' findings in its payload. A remote
child that keeps being rejected parks to `blocked` after the step's declared
gate-rejection count, with the last objection quoted — nothing is retried
locally. A local child (a local tag, a sequential epic, or the not-signed-in
fallback) still opens the relay.

**Failure.** A remote child whose cloud dispatch fails stays at its from-state,
with the failure and the session URL on the ledger (`cloud_dispatch`, which also
records `placement: remote`). The base agent is not dispatched in its place. The
container's `after_children` step keeps refusing entry, naming that child, until
it has discharged the obligation.
