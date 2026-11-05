# satelle

Local-first substrate for agent-driven work. Satelle governs the
authored process — stories, tasks, an evidence ledger, and authored markdown
(documents, workflows, principles, skills) — backed by a per-repo SQLite
database. Work moves through a **gated workflow**: the agent executes; isolated
reviewers gate every status change. satelle runs **100% locally**: pure-Go
static binaries (CLI + optional `satelled` UI), no external server, no cgo.

> V6 rebrand of `satellites`. See [`docs/`](./docs) for the product spec, port
> architecture, and the operating model.

## Install

```sh
curl -fsSL https://github.com/bobmcallan/satelle/releases/latest/download/install.sh | sh
```

Downloads the latest **CLI** release for your platform (and the latest
**satelled** when a `serve-v*` release exists), sha256-verifies each, and
installs to `~/.local/bin` (override with `SATELLE_INSTALL_DIR`). Or build from
source: `make install` (builds both `satelle` and `satelled`).

CLI and daemon carry **independent versions** in `.version`
(`satelle.version` / `satelled.version`). GitHub tags are `vX` for the CLI
(always `releases/latest`) and `serve-vY` for the UI binary (not latest).

Already installed? `satelle update` self-updates the CLI and refreshes
`satelled` from its own channel, then restarts the background service.
`satelle update --check` reports availability without installing.

A matching version string is not treated as proof the installed bytes match the
published release: when versions already match, update compares the local file
against the published asset checksum and reinstalls on a mismatch (a retagged
version, or a machine-wide `make install` of unreleased code). To reinstall the
published asset even when version and checksum already look equal:

```sh
satelle update --force
```

## Quickstart

```sh
make install           # build + install satelle and satelled to ~/.local/bin

cd your-repo
satelle init           # scaffold .satelle/ + validate; reports workspace: member|not-member
satelle story create --title "Ship the thing" --priority high
satelle task create  --title "write release notes"
satelle reindex        # index authored markdown under .satelle/
satelle status         # config, database, and store counts
satelle service install   # always-on UI (prefers satelled); or: satelled --port 8787
```

The UI is a **push-fed read-only mirror** (not a live DB browser). Project pages
live under `/r/<slug>/` on the workspace landing (`http://127.0.0.1:8787/`).

### Push-fed UI (CLI → satelled)

The web UI is a **read-only mirror** fed by the CLI (serve-split architecture;
serve-adoption onboarding):

1. Run the UI: `satelled` (or `satelle service install`, which prefers the
   dedicated binary). `satelle serve` remains a **deprecated alias** that prints
   a migration notice and runs the same mirror.
2. Join the workspace and seed the mirror in one verb:
   ```sh
   satelle workspace add         # register + seed; bootstraps endpoint when a matching serve is up
   satelle workspace partitions  # list mirror partitions (repo_key, path, counts)
   satelle workspace prune <repo_key> [--force]  # remove orphan/junk partitions
   satelle workspace remove      # unregister + purge that repo's partition
   ```
   `[server] endpoint` usually lives in **gitignored** `.satelle/satelle.local.toml`
   (per machine). When unset and a local serve answers at the service port
   (default `http://127.0.0.1:8787`) **and** reports a matching `X-Satelle-Instance`
   for this `SATELLE_HOME`, `workspace add` writes that endpoint into
   local.toml and seeds in the same command. Without a matching serve it still
   registers, prints seed skipped, and exits 0. Hermetic tests set
   `SATELLE_SERVER_ENDPOINT=none` so they never auto-probe a live operator serve.
   Mutating verbs drain change + one snapshot before process exit (no manual
   reconcile step).

`satelle ui` / `satelle ui push` were **removed** — they print a pointer to
`satelle workspace add`.

Serve never opens per-repo runtime DBs — only `~/.satelle/serve/mirror.db`,
partitioned by repo-key.

### Substrate freshness without serve

The CLI keeps the doc index, task store, and story-backlog view current **without
a running serve process**:

1. **SessionStart hook** — `satelle init` scaffolds `satelle reindex` on every agent session.
2. **Explicit `satelle reindex`** — full on-demand pass (docs + tasks + backlog view).
3. **Post-story-verb** — `story create` / `story set` best-effort regenerate the disposable backlog view.

Hand-edited tasks or authored markdown still need a reindex (or the next session
start) to land in the store/index. See `satelle reindex --help`.

### Always-on service

Prefer the dedicated **`satelled`** binary for the UI process. Install both
artifacts, then install the service (it picks `satelled` next to the CLI when
present; falls back to `satelle serve` with a notice):

```sh
make install                 # build + place satelle and satelled on PATH
cd your-repo
satelle service install      # systemd user unit (Linux/WSL); --system for persistent system unit
satelle service status       # show state + URL
```

Foreground: `satelled --addr 0.0.0.0 --port 8787`. The old `satelle serve`
subcommand still works as a deprecated alias.

Settings live in the machine-wide `~/.satelle/config.toml` (`[service]` port /
addr / repo). Change the port there (or pass `--port`) and re-run
`satelle service install`. The service binds `0.0.0.0` by default, so in **WSL**
it's reachable from a Windows browser at `http://localhost:<port>`. On native
Windows (no systemd), `service install` prints Task Scheduler steps instead.

`init` is idempotent and writes a managed `.gitignore` block (the local
`.satelle/satelle.db` stays out of git; the config and authored markdown are
committed). It's also optional — a repo with no `.satelle/satelle.toml` runs
zero-config on defaults, with data in `.satelle/satelle.db` travelling with the
repo it governs.

## Commands

| Group | Verbs |
|-------|-------|
| `story` / `task` | `create`, `get`, `list`, `set` |
| `task` (only) | `archive` — dispose of a superseded header (record archived + files moved to `.satelle/backups/`) |
| `ledger` | `append`, `list` |
| `doc` | `list`, `get` |
| `hosted` | `login`, `logout`, `whoami` — authenticate to a hosted satelle-server (OAuth 2.1 + PKCE); optional, the local service runs standalone without it |
| `project` | `create`, `list`, `bind`, `show` — manage projects on the hosted satelle-server; `bind` records which hosted project this repo's personal sync targets. Requires `login` for create/list |
| `sync` | `scopes`, `config` (push/deploy), `documents` (push/pull), `workstate` (push/pull), `rehydrate`/`pull` (no push) — personal opt-in targets **this repo's bound hosted project only**; local is the default. `*.local` files are never pushed, at any scope. **Recover after clone/wipe:** install → `login` → `project bind <slug>` → `sync rehydrate` (config deploy first, then documents + workstate pull) |
| `publish` | `push`, `list`, `adopt`, `check` — team catalog (select local artifacts to share; not a second home for the repo). Distinct from personal `sync` |
| `settings` | Read/write config in two scopes: repo `<key> [value]` (committed `.satelle/satelle.toml`; no args lists all) is the default; `--global server <url>` sets the machine-wide hosted server in `~/.satelle/config.toml` (no login; sign in after via the UI or `login`) |
| | `init`, `reindex`, `status`, `version` |
| `workspace` | `add` (register + seed mirror), `remove`, `list` |
| UI process | `satelled` (dedicated binary); `satelle serve` is a deprecated alias |

The CLI is the sole writer into repo stores. The UI process only serves the
push-fed mirror (plus ingest).

## Workflows & gates — the agent model

satelle governs work as a **gated workflow**: a story or task moves through a
lifecycle of **steps**, and it is `done` only when its status says so — reached
through every gate on the path.

- **The agent is the executor** — it does the work and drives the story forward.
- **satelle is the gatekeeper of status** — each forward transition is judged by
  an isolated, fresh-context **reviewer** (an `agent -p` rubric, or a deterministic
  functional check). Accept enacts the transition; reject pushes notes back. A
  reviewer is **read-only** — it judges, never mutates. Each gate is one isolated,
  fresh-context call over a payload satelle builds; satelle does the context
  selection, the reviewer reads what it needs through its read-only tools.

The lifecycle is **authored substrate**, and satelle DERIVES it from two files
under `.satelle/workflows`: `done.toml` declares what done means per category (an
ordered list of obligations, plus the park and cancel states), and `step.toml` is
the catalogue of steps and always-on gates that discharge them — each step
keyed by the obligation it discharges, naming what it `requires`, its `agent`,
and the `reviewers` gating entry to it. The binary owns ORDER (a topological sort of the
prerequisites) and topology (cancel, park, backward movement), so neither is
authored. The embedded route (`backlog → in_progress → done`, plus container and
task-run sections) is the order-zero default a repo edits or overrides. A repo
that still carries a retired DOT graph governs nothing until it converts —
satelle refuses transitions under it, naming `satelle help workflow-convert`,
rather than silently dropping every gate it authored. How each agent runs is bound in
`.satelle/workflows/agents.toml` — the reviewer's
agent CLI (`claude` and `grok` presets — see
`satelle help agent-dispatch`) and its read-only grant; the executor runs
in-loop. `satelle agents install claude|grok|pi|all` installs launchers and
repo compliance hooks (`.claude` / `.grok`) so governed edits need
an engaged story.

Process is configuration — change the workflow or its skills, change the process,
with no binary release. See `satelle help reviewer-checks` and the
`satelle-agent-model` and `satelle-route-standard` principles.

Optional **step-scoped command policy** (`[gate.command_allow]` in
`satelle.toml`): restrict named git subcommands (e.g. `push = ["release"]`) to
permitted story statuses while engaged. Opt-in only — unset leaves the
commitgate as engage-only. See `satelle hook commitgate --help` and the init
scaffold comments.

### Seat concurrency — who may engage at once

A story in a performing state holds the project's **engagement seat**. By
default the seat is single-occupancy: one performing story, everything else
refused. That default is what makes "is work in flight?" a one-lease question.

A repo may widen it per project:

```toml
[engagement]
parallel = "none"   # default — one performing story at a time
parallel = "epic"   # children of one parent may engage concurrently
```

Under `epic` the seat is claimed at the **parent**, not the story: the first
engagement keys the seat on its `parent_id`, and further engagements are
admitted only if they share that parent **and** run from a **distinct git
working tree**. A story under a different parent — or none — is refused while
that seat is held, and the refusal names the parent holding it. A parentless
story engaging first claims the seat as a singleton, which is `none` for its
duration. The seat frees when the last sub-lease frees.

Two rules follow, and both are load-bearing:

- **One working tree per lease.** Each lease records the tree it was engaged
  from; a second engagement from an already-leased tree is refused, even for a
  valid sibling — two engaged stories sharing a tree would attribute each
  other's edits. `satelle story diff` is anchored to the recorded tree for the
  same reason: run it elsewhere and it refuses, naming where to run it.
- **The parent is not engaged unless its route says so.** Its id is the
  arbitration key, not a lease, and a container idling at a `waits_on_children`
  step holds none. A container step may declare `after_children = "<obligation>"`:
  entry is refused until every child has discharged that obligation on its own
  route, and the step may name a performer, which then takes a seat under the
  same key so parent and children co-hold from distinct worktrees. The binary
  only gates that entry and seat; what the step does, a merge included, is a
  skill the route names.

**What satelle does not own.** Satelle arbitrates concurrent *engagement*. It
does not define how concurrent work *converges* — merge order, whether a batch
pays for one integration suite at its head, citation, one push, one CI watch.
That is workflow, and workflow is the repo's own substrate (`done.toml`,
`step.toml`, skills), which already expresses it: categories are free-form and
ledger `cite-run` already crosses stories. Whether a given parent's children run
as a wave is decided per parent by its user or agent, from that parent's
contents, inside the workflow the repo already has — no dedicated lane,
category or extra concept to learn.

Satelle performs **no sibling-contention policing**: no file-overlap detection
between concurrent stories, no merge ordering, no batch semantics. That is a
design choice, not a gap. A repo that turns the mode on with a workflow that
does not suit it has a substrate defect in that repo, not a satelle issue.

## Architecture

- **Pure-Go SQLite** (`modernc.org/sqlite`, no cgo) — one static binary.
- **Gated workflows:** authored DOT lifecycles drive each story; isolated
  reviewers gate status transitions, with the agent backend bound per repo.
- **System-of-record split:** stories/tasks/ledger are dynamic SQLite rows;
  authored markdown is the source of truth, synced into a SQLite index by a
  directory monitor.
- **Config:** per-repo `.satelle/satelle.toml` with defaults for every setting
  and a gitignored `satelle.local.toml` overlay. Optional `stories_keep_closed`
  (count) and `stories_keep_days` (age) prune closed stories' attachment dirs
  under the home-keyed runtime plane (`~/.satelle/<repo-key>/stories/`), moving
  them to the runtime backups tree; a non-terminal story's dir is always kept.

See [`docs/spec.md`](./docs/spec.md), [`docs/architecture.md`](./docs/architecture.md),
and [`docs/agent-model.md`](./docs/agent-model.md) (the operating
model: reviewer premise, DOT workflows, isolated fresh-context review).

## Development

```sh
go test ./...           # every package, the integration suite under tests/ included
make test               # the same under credguard and a hermetic HOME (STRICT=1 optional)
make integration        # only the integration suite
```

The integration suite (`tests/`) builds `satelle` and `satelled` once and drives
them as a user would, each test in its own home and fresh git repo: `init` and a
walk to done, a user-defined workflow judged by a fake `claude` on `PATH`, the
edit gate, attachments, task runs, sync between two homes over the in-process
fake satelle-server, satelled's pages, and a docker-local node (skipped without
a docker daemon and a small local image). No test reaches past loopback.

CI (`test.yml`) runs build, vet, the windows cross-build, `make test`, the
literal audit (`scripts/audit-literals.sh`), gofmt and the no-cgo static build.
Releases are cut by `.github/workflows/release.yml` when a CLI or serve version
in `.version` has no matching tag yet (`vX` / `serve-vY`); `satelle update`
installs them.

`make judgment` runs the opt-in LLM rubric fixtures under `tests/llm/`
(`-tags llm`). They cost tokens and call a live model; never in default CI.

## License

MIT — see [LICENSE](./LICENSE).
