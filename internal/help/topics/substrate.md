# Substrate — how satelle stores and validates its configuration

satelle's process is **configuration, not code**: workflows, skills, and
principles are authored markdown. This topic explains where that substrate lives,
the format it follows, and how it is validated.

## Three planes (authored / defaults / runtime)

| Plane | Where | Holds |
|---|---|---|
| **Authored** | `<repo>/.satelle/` (git-optional) | satelle.toml, constitution, edited workflows (incl. `workflows/agents.toml`) / skills / principles, documents, tasks |
| **Defaults** | embedded in the binary | sparse repo-agnostic workflows/skills/principles (resolved virtually; see virtual-defaults work) |
| **Runtime** | `~/.satelle/<repo-key>/` | satelle.db (+wal/shm), logs/, backups/, stories attachment cache |

`satelle runtime path` prints the resolved runtime dir and whether this repo is
still on the legacy in-repo layout. `satelle runtime migrate` copies a legacy
DB + runtime dirs to the home key (explicit; never silent). One repo-key maps to
one isolated DB — never a flat multi-repo bag.

## Authored substrate under `.satelle/`

`satelle init` lays a self-documenting skeleton under `.satelle/`:

- `satelle.toml` and `workflows/agents.toml` (both documented, both optional to edit);
- a dir per authored kind — `documents/ workflows/ principles/ skills/ tasks/` —
  each with a `README.md` describing what it should contain (READMEs are dir
  descriptors; the indexer and OKF normaliser skip them);
- the complete **default solution**, materialised on disk so the default
  substrate is visible and editable: the order-zero **baseline workflow** (the
  minimal repo-agnostic lifecycle — edit it in place to layer your own gates),
  the parent/epic container workflow, the task-execution workflow, and every
  gate skill they reference. A workflow whose `applies_to` category is already
  claimed by an authored workflow is skipped (no same-precedence duplicate);
  the embedded copy still backstops the order-zero fallback either way.

The binary still ships embedded canonical defaults; a repo file with the same
(kind, name) overrides its default. `init` materialises the defaults so you never
have to reason about invisible substrate — and never clobbers an authored file
(an existing workflow set is respected wholesale). `satelle restore` re-installs
the embedded skills/principles over drifted copies; `satelle rebase` goes
further — it backs up `workflows/ skills/ principles/` to a timestamped dir under
the runtime `backups/`, wipes them, and redeploys the complete default solution
(the "start clean" recovery).

## Edit-gate lock precedence

A path under [gate] lock_substrate_paths is never edit-exempt: editing it needs
this session engaged in a substrate-lane story (category "substrate"), whatever
else is performing. This overrides the `.satelle/` entry `satelle init` seeds in
edit_exempt_paths; non-locked exempt paths stay exempt. The refusal is the same
through Edit/Write and a shell mutation, with a story performing or with none.
One narrower lane: a story of category "agents-repair" may edit the agents
layer (`.satelle/workflows/agents*.toml`) and nothing else under the lock, and
its close admits no other path — the way to repair a binding that refuses every
other story's engagement.

A shell command is judged by what it writes. A shell -c program is classified as
shell, as the same command run bare is, so a `--body` naming `.satelle/` inside
`bash -lc` is not an edit. Only non-shell interpreter text (python, node, perl,
ruby) is text-scanned, conservatively, and a shell -c program is text-scanned like
it when it is not plain: glued quoting, a double-quoted `$`, backslash or
backtick, a `$(` or backtick anywhere, a dynamic command word, `eval`, `source`
or `.`, or a pipe, heredoc or `<` redirect feeding the shell. The shell's own
NAME=value words and positional arguments are always text-scanned.

## Pre-mutation backup

Before init/restore/rebase **overwrites** an existing file under `.satelle/`,
satelle writes a local copy under the runtime `backups/` tree (kinds:
`pre-mutation/`, `diverged/`, `restore/`, or a timestamped dir for rebase). Local
floor always — heal paths never block for backup. Online/personal push of
pre-images into the bound project's documents partition is **opt-in**
(`[backup] hosted = true`) and requires `satelle login` + project bind;
offline/auth failure degrades to local with a notice. Default is local-only so
init never poisons the documents partition that `satelle sync` pulls (backups/
is a restore exclusion). Set `[backup] local_only = true` (prefer
`satelle.local.toml`) to suppress the advisory that points at the online option.

## Format: Open Knowledge Format (OKF)

Every authored doc carries YAML frontmatter with a required **`type`** key (OKF):
`type: workflow | principle | skill`, and `type: <category>` for free-form
documents. The directory is authoritative for the kind; `type` mirrors it. A
legacy `kind:` key is migrated to `type:` automatically at ingest. Bodies are
ordinary markdown — a route half carries its declaration of done or its step
catalogue, a skill its rubric.

## Validation is deterministic code

The per-noun `satelle <noun> validate` (and the reindex pass) check each doc with a **deterministic
structure check** (`internal/structure`) — frontmatter keys, kebab name matching
the file, a usable definition, a non-stub body, and for a workflow the graph
(connected, terminal `done`, `backlog` start, resolvable executor skills). These
are CODE, not LLM rubrics: harness-independent and never flaky. A swapped agent
(claude, grok) cannot change what "valid" means. `satelle <noun> validate` needs no
agent CLI.

### Injected context size

`satelle validate` ends with an `# injected context` section: how many bytes of
instruction text reach an agent, per kind (session-resident principles, the
constitution, the largest skill of each kind) and per seat. The driver is one row
per in-loop harness (`driver[claude]`, `driver[grok]`), each under that harness's
own SessionStart limit, from the same assembly the hook uses; a row notes what the
limit omitted. Every dispatched seat is one row labelled with its resolved adapter
and interface (`reviewer[claude/stream]`): constitution and principles when its
binding injects them, the charter, and its largest skill. A figure that cannot be
computed prints `unavailable (<why>)`, never a number.

Tokens are an estimate: bytes divided by one provider-neutral ratio, because no
adapter reports tokens before a call.

```toml
[validate.injected]
bytes_per_token = 4          # the default; a unit conversion, not a budget

[validate.injected.budget]   # estimated tokens; the binary ships none
driver   = 6000              # a seat or kind name, or an exact label: "driver[claude]"
reviewer = 9000
```

A row over its budget prints `WARN`. A budget only warns: it never fails
`satelle validate` and never changes its exit code. With no key set for a row it
prints `no budget`.

`satelle-repo-agnostic` (only satelle's OWN embedded `scope: system` substrate
must avoid repo-specifics) is a satelle-dev concern — never a runtime gate. Your
project substrate is meant to be opinionated; satelle never judges it for that.

## Authoring

Drop a markdown file under the right `.satelle/<kind>/` dir and run `satelle
reindex` — or use `satelle skill|workflow|principle create --from <file>`, which
writes through the deterministic structure check and refuses a non-conforming
artifact. List with `satelle doc list`; read one with `satelle doc get <kind>
<name>`.

## Reviewing the substrate

`satelle substrate review` judges whether each item earns its cost. It lists
every principle, skill, task, workflow step and gate, and agent seat with its
origin (`embedded`, `seeded`, `edited`, `repo`, `team`, `local`) and the
evidence this repo's ledger holds for it, broken down by the model that ran each
seat: gate rejections and their reasons, how many were amended, reversed or
re-presented to accept, reject-budget parks, rework rounds, flaky reruns, and
panel dissent (`dissent: not recorded` where the ledger holds no per-seat
verdicts). It also lists every constitution rule.

Judging runs under `satelle-substrate-review`, one dispatch per area
(principles with the constitution rules, skills, workflows, agents, tasks),
split further only when an area's manifest outgrows the seat's
`[validate.injected.budget]`. Each judge gets a manifest of paths: the item's
file, the current embedded default of an edited or seeded copy, and a file of
its history. It reads them itself, and gives each item a disposition (remove,
reduce, keep, conflict) with its reason and evidence, and each rule a
`file:line` finding or "no finding". The judge is the repo's in-loop model on
the reviewer seat; `--binding` names another agents.toml section. An item left
out, or returned without its reason and evidence, is shown as "not judged". A
batch whose answer does not parse keeps the raw answer beside the report, and
the other batches are still reported. The report is printed (`--json` for
JSON), and its JSON is written to `--out` (outside the tree) or to a file under
the OS temp dir. No substrate file changes.

Each review runs as an execution of the task `tsk_substrate-review`, an
embedded default a repo overrides with its own `tasks/tsk_substrate-review.md`.
The execution enters `in_progress` through the execution route before anything
is gathered, so the run shows in the local UI's Activity tab while it runs. It
records each stage (`gather`, `judge` per batch, `report`) on its ledger,
carries the JSON report as the attachment `substrate-review-report-<exe_id>`,
and moves to `done` through the route's close gate. On an error, or a gate that
refuses, it moves to `cancelled` with the reason recorded as its last stage; a
report already written stays where it is.

`satelle substrate review --apply <item>` (repeatable) raises one story of
category `substrate` for each item you choose. The story carries the item's
origin, disposition and evidence, and names the review's execution in an
`execution:<exe_id>` tag. No file changes here either; the cut is made
when that story runs through the substrate lane. `--from <report.json>` applies
from a saved report and skips a second judgement. An `embedded` item has no file
in this repo to cut, and a `team` item belongs to the project tier, so both are
refused with the reason.

See also: `satelle help reviewer-checks`, `satelle help principles`.
