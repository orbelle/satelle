---
name: satelle-upgrade
type: skill
description: Followed by the session agent after `satelle upgrade --archive`. It reads the archived pre-1.0 .satelle, proposes the v1 workflows, gates, performers and agent bindings that keep each old route's intent (optimised), applies only what the user confirms, writes a status map the user confirms, then runs the import, satelle validate and satelle ledger verify.
---

# Upgrade a pre-1.0 repo onto v1

You port this repo's archived process into the fresh v1 `.satelle` that
`satelle upgrade --archive` wrote, with the user. You propose; the user
confirms; you apply only what was confirmed. Learn v1 first:
`satelle help upgrade`, `satelle help workflows`, `satelle help configuration`,
and `satelle workflow show <name>` for any workflow.

## 1. When and inputs

- Follow this after `satelle upgrade --archive`.
- **The archive** is the `<dest>` of the `archived <src> (<n> files) to <dest>`
  line the archive printed. Without that output, list
  `<satelle runtime path>/archive/` and ask the user which stamp.
- **The old store** is the `old store: <dir>` line. The archive prints it only
  when it found `satelle.db` in the runtime directory or in the archived
  `.satelle`. Without that line, ask the user for the directory that holds the
  pre-1.0 `satelle.db`, and check the file is there. Never use
  `satelle runtime path` as the old store: it names the v1 runtime.
- The archive is read-only to you. When the old store's directory is read-only,
  import from a copy, as `satelle help upgrade` says.
- Your working files are `upgrade-proposal.md` and `status-map.toml` in the
  archive's stamp directory (the parent of `<dest>`).

## 2. Rules

- Write nothing under `.satelle/` and run no import into this repo's runtime
  before the user confirms. The rehearsal in section 8 runs under a throwaway
  `SATELLE_HOME`, writes nothing else, and is told to the user before it runs.
- Every old element appears exactly once in the proposal.
- Never propose a gate whose skill the new `.satelle` lacks.
- **Never make `session` a judge.** Every gate's `agent` and every `panel`
  member is a `claude`, `pi` or `grok` binding with a `read-only` grant
  (`Read`, `Grep`, `Glob`), as `satelle help workflows` (Agents) states and
  `satelle validate` enforces. `session` only performs.
- **Leave no sink.** Every state that is not terminal has a transition out. A
  park state has its `@prior` resume edge and is named in the cancel edge's
  `from`. `satelle validate` checks reachability from start, not that a state
  can be left, so check each workflow with `satelle workflow show` after Apply.
- When an old element's intent is unclear, ask the user; do not guess.

## 3. Read the archive

- `satelle.toml`: every key.
- `workflows/done.toml`: one table per category. Its obligations, in order, are
  the route; `*` is the default table. A table also holds `park`, `cancel` and
  `recover`. Its `[meta]` may hold create_review and amend_review.
- `workflows/step.toml`:
  - per obligation: status, start, terminal, agent, skills, reviewers,
    `reviewer_agent`, `parallel`, requires, freeze, propose, rework,
    reject_budget, `waits_on_children`, schedule, `after_children`, `advise`;
  - its always-on `[[gate]]` tables, each with skill, agent, `on`, `for`,
    `when`, `mandatory` and `applies_to`.
- `workflows/agents.toml` and `agents.local.toml`, per seat: role, command,
  `interface`, tools, model, effort, principles, idle_timeout; and the
  `[model_order]` table.
- `skills/`, `principles/`, `constitution.md`, `tasks/`.

## 4. Map old to new

One proposal row per old element, by these rules.

**Workflows**
- A done table becomes one workflow. Tables with the same route merge, each
  selected by a `[[workflows.rule]] category`. `*` becomes `[workflows] default`.
- done.toml `[meta]` create_review and amend_review are dropped: v1 has no
  create or amend gate.

**States and transitions**
- An obligation becomes a state named by its step's status. `start = true` makes
  it `start` and `terminal = true` makes it `terminal`; without them the first
  obligation is `start` and the last is `terminal`.
- A step's `requires` becomes a transition from the required step's state to
  its own.
- `park` becomes a `park = true`, `resumes = true` state with two transitions:
  - one `from = ["*"]` into it, carrying its gate;
  - the resume, `from = ["<park>"]`, `to = "@prior"`, which returns the item to
    the state it was parked from, with no gate unless the user names one.
  - Without the resume the park state is a sink: `*` does not stand for a park
    state, and `@prior` is allowed only from a resuming park.
  - Its advisor and advisor_skill become the park state's performer.
- `cancel` becomes a terminal state with a transition `from = ["*", "<park>"]`,
  carrying its gate when it names one. The park is named because `*` does not
  stand for it, so a parked item can still be cancelled. A table with no park
  has `from = ["*"]`.
- `recover` becomes explicit backward transitions from each listed status to
  the step's status.

**Performers**
- A step's agent and skill become the state's `performer`. A step with more
  than one skill is a row asking which one stays: a v1 performer has one skill.
  A step with no agent has no performer.
- An in-loop command becomes a binding with `command = "session"`, which only
  performs (see Agents).

**Step reviewers**
- A step's reviewers become gates on each transition into its state, in one
  group.
- A reviewer whose skill is carried as `type: check` (section 7) becomes a gate
  with no `agent` and no `panel`. Every other reviewer is judged.
- `reviewer_agent` becomes each judged gate's `agent`. With none, the agent is
  the `reviewer` binding (the old engine judged an edge with no agent on its
  reviewer seat). Either way it meets the judge rule under Agents; when the
  archive has no reviewer seat, or it cannot meet the rule, the row asks the
  user which judge binding the gate uses.
- `propose = true`: the old step's agent produced its artifact on the edge
  into the state, and the step's reviewers judged it before the move. A v1
  performer works inside its state and a gate judges on the way out, so a
  proposing step's reviewers go on the transitions out of its state (joining
  the next step's reviewers there), not into it. The row says so.
- `parallel` is a concurrency cap, not a switch. Absent or `0` becomes
  `mode = "sequential"`: the first reject stops the round. A count of 1 or more,
  or `true`, becomes `mode = "parallel"`: every gate runs. The count itself is
  dropped: v1 has no cap.

**Always-on `[[gate]]` tables**, key by key:
- `skill` becomes `[gates.<skill>]` in each workflow `for` names; a merged
  workflow gets it once.
- `on` names states: the gate joins every transition into each of them. With no
  `on`, it joins every transition except those into the park and cancel states.
- `agent` becomes the gate's `agent`, under the judge rule; with none, the
  `reviewer` binding. A gate whose skill is carried as `type: check` takes no
  agent; an old agent on it is dropped in its row.
- `when` becomes `trigger` when the trigger skill is in the archive and is
  carried as `type: check`. Otherwise the row offers `paths` the user confirms
  stand for what the trigger looked for, or drops the gate.
- `applies_to` (a tag scope) becomes `paths` the user confirms stand for the
  tag, or a `trigger` check, or the gate is dropped.
- `mandatory = true` needs no v1 key: it made a failure block where the default
  was best-effort, and every v1 gate's failure rejects. So a gate without it
  changes from fail-open to blocking, and its row says so.
- Placement: a gate carried as `type: check` goes in a leading `sequential`
  group, so cheap checks fail first. A judged gate joins the step's reviewer
  group, or forms a `sequential` group when the step has none.

**Child and advisory keys**
- `after_children` and `waits_on_children` have no v1 key: v1 shows children to
  a judge, and nothing waits on them. The row is dropped, or the user authors a
  check skill that rejects while a child is not terminal, which becomes a gate
  on the transition into that state.
- `advise = { agent, skill }` has no v1 key: v1 runs nothing after a transition
  without gating it. The row is dropped; its skill is still offered for carry
  over, to run by hand.

**Agents.** An old `[name]` becomes `[agents.name]`:
- `command = "in-loop"` becomes `command = "session"` only for a seat that
  performs. The session adapter does not enforce grants, so a `session` binding
  never judges.
- A command line for a known CLI becomes that adapter (`claude`, `pi`, `grok`),
  which builds its own flags. A command line naming none of them is a row the
  user decides: one of the three adapters, or the seat is dropped.
- model and effort are kept; tools, a comma string, becomes a list.
- **Judge rule.** Every binding a proposed gate names (as `agent`, in a `panel`,
  or as the reviewer default) is a `claude`, `pi` or `grok` binding whose tools
  are within `Read`, `Grep` and `Glob`:
  - An in-loop seat that a `reviewer_agent` or a `[[gate]]` agent names becomes
    a new judge binding on the adapter the user picks, read-only. The row shows
    the split from the session performer.
  - An old judge seat with more tools (`WebFetch`, a `Bash(...)` scope) loses
    them. Its row names each dropped tool and asks the user to keep the gate
    read-only, to carry its skill as a `type: check` that runs the command
    itself, or to drop the gate.
  - A seat that both performs and judges stays one binding only when it meets
    the rule; otherwise it splits into the performer and a `<name>-judge`.
  - Check every judge against this rule before showing the proposal.
- role is dropped: v1 has no role; the judge rule decides who may judge.
- `interface` (stream or command) is dropped: the adapter decides how it runs.
- principles is dropped from the binding; the principles the session should see
  go in `[hook] inject`.
- idle_timeout and `[model_order]` are dropped: a v1 binding has no timeout or
  model fallback.
- Each `agents.local.toml` override is its own row, merged into the binding of
  the same name: v1 reads only `.satelle/agents.toml`.

**Settings**
- Each of these is a row with "no v1 equivalent": reject_budget, rework,
  freeze, schedule, bundle, `[review]`, `[categories]`,
  `[engagement]`, `[fix_lane]`, `[worktree]`, substrate_roots, and the
  data_dir, db, web_port and log settings.
- `[gate] edit_exempt_paths` and edit_exempt_globs become the v1
  `[gate] edit_exempt_paths`.
- `[sync]` is proposed; the user decides.

**Anything else.** A key or table these rules do not name gets its own row, and
you ask the user about it before the proposal is confirmed.

## 5. Optimise

Drop, each as a row with its reason:
- a step no table's obligations reach;
- an unwired check: a check skill that no step's reviewers, no `[[gate]]`
  table's skill and no `when` names. A skill a `[[gate]]` table names is wired
  and is never dropped as unwired;
- a `[[gate]]` table whose `for` names no table;
- a reviewer duplicated on one edge, including a `[[gate]]` landing on an edge
  whose step already names its skill (keep one);
- a reviewer repeated on consecutive edges (keep one gate with `carry = true`);
- a gate whose skill or `when` trigger the archive lacks, because the old binary
  built it in: dropped, or replaced by a skill the user agrees to author.

**A gate whose skill exists is kept.** Only these reasons drop it, each a row
the user confirms:
- a duplicate rule above, which keeps one gate for that skill;
- its `for` names no table;
- its `when` trigger is missing, or it has an `applies_to` scope, and the user
  confirms no `paths` or `trigger` that stand for it;
- its judge has tools beyond the read-only grant, and the user chooses to drop
  the gate.

Nothing else drops it. Use one skill per job across workflows, preferring a
skill the new `.satelle` already holds.

## 6. Propose and confirm

- Write `upgrade-proposal.md` with tables: Workflows; States and transitions;
  Gates; Performers; Agent bindings; Settings; Skills and principles.
- Columns: old, new, action (kept, changed, merged or dropped), why.
- Show it to the user. Record each row's answer (confirmed or changed) and the
  date. Show it again after any change. Apply only confirmed rows.

## 7. Apply and carry over

- Write `workflows/*.toml`, `agents.toml` and `satelle.toml`.
- Carry the repo-local skills into `.satelle/skills/`. Leave out `README.md`
  files (they would load as skills) and the old built-in skills.
- **Retype script gates.** A carried skill whose body holds a ```check block is
  a script the old binary ran: set its frontmatter to `type: check`, since v1
  runs a skill's first ```sh or ```check block only for that type. Its gates
  name no `agent` and no `panel`. A ```sh block in any other skill is an example
  for its reader, so that skill stays `type: skill` and is judged. Each retype
  is a row in the Skills table.
- **Rewrite commands** in each carried skill, checking each against
  `satelle <cmd> --help`:

  | old | v1 |
  |---|---|
  | `story doc <id> <name>` (prints one document) | `story attachment <id> <name>` |
  | `story docs <id>` | `story attachments <id>` |
  | `ledger list --story <id>` (the item's history) | `ledger list <id>` |
  | `story attach <id> --name N --type <doc type> --body "…"` | `story attach <id> --name N --file -`, body on stdin (`--file -` reads stdin); v1's `--type` is a media type, so the old doc type is dropped |
  | `story log`, `story messages`, `story estimate`, `story actual` | none: remove the line, and the row says what the skill loses |

  `story route <id>` is v1's workflow position and gate results, not history.
  Imported items keep their old `sty_` ids and new items get `sto_`, so an id
  pattern that accepts only `sty_` must also accept `sto_`.
- Rewrite a judge skill's answer to the verdict object `satelle help workflows`
  documents, and checks to read `SATELLE_ITEM`, `SATELLE_FROM`, `SATELLE_TO`
  and `SATELLE_BASE`.
- Carry the principles the user keeps into `.satelle/principles/`, and list in
  `[hook] inject` the ones the session should see.
- Run `satelle validate` until it reports nothing. Then run
  `satelle workflow show <name>` for each workflow and check the no-sink rule:
  each park state shows its `@prior` edge and appears in the cancel edge. Then
  `satelle agents install <harness>`. If a global gitignore hides
  `.claude/settings.json`, tell the user.

## 8. Status map

- List every old status: the step.toml statuses, plus a dry run with the map so
  far: `satelle upgrade --from <store> --map <map> --dry-run` refuses and lists
  every unmapped `<kind> <status>: n item(s)`.
- Write `status-map.toml` in the format of section 2 of `satelle help upgrade`.
- **Categories that share a status.** A map key is `<kind>/<status>` or
  `<status>`, never a category, and the import writes the key's workflow onto
  every item it matches. An imported item keeps that workflow: v1 refuses a
  later workflow change. So when categories the proposal routes to different
  workflows hold items of the same kind and status, one key sends them all to
  one workflow. Find each case before the import:
  - Tell the user, then rehearse the import in a throwaway runtime: under
    `SATELLE_HOME=$(mktemp -d)`, run `satelle upgrade --from <store> --map <map>`,
    then `satelle <kind> list --json` for each kind the dry run lists (`--json` gives each item's category, workflow and status). Delete
    the directory after.
  - Count items by kind, category, workflow and status. A category should have
    the workflow its `[[workflows.rule]]` selects in the proposal; an item with
    no category, or one no rule names, should have `[workflows] default`. An
    item whose workflow differs is off its route.
- Show the user one table with a row per key and category: key, category, item
  count, the workflow and state the key gives, the workflow the category's rule
  selects, and why.
- Each row with items off their route names one resolution the user confirms:
  - **one shared workflow:** the categories share one workflow. This changes
    the proposal: confirm it again under section 6, apply it, rehearse again;
  - **items leave their route:** the key's target stands; the row lists those
    item ids, and they stay in that workflow after the import;
  - **no import:** stop and report that the map cannot separate the categories.
- Record each row's answer in `upgrade-proposal.md`.
- If the import refuses because of the start_clean setting, stop here and
  report it.

## 9. Import and validate

- Run the dry run and show the user its output.
- Run the import, then `satelle validate` and `satelle ledger verify`.
- Run the category check of section 8 again in this repo's runtime, and report
  that every item off its category's route is in a row the user confirmed.
- Report each output, and where the proposal and the map are.
