# Machine-wide agent profiles — one provider definition, many repos

An operator with several satelle repositories used to restate the same Claude
or Grok binding in each one. The **profile catalog** at
`~/.satelle/agents.toml` holds those definitions once; each repository then
**explicitly** points a binding at a profile.

The split is the constitution's line, applied to configuration:

| Where | What lives there |
| --- | --- |
| `~/.satelle/agents.toml` (machine-wide) | **Execution** configuration — how an agent runs: command, transport, model, effort, tools, timeout, env, settings. |
| `.satelle/workflows/agents.toml` (repo) | Which logical roles this repo has, and which profile (or inline binding) each one uses. |
| `.satelle/workflows/*.md` (repo) | **Process** — which step runs which skill, which gate judges which edge. Never machine-wide. |

## Repo agents.toml posture (committed substrate)

**`.satelle/workflows/agents.toml` is committed substrate by product default.**
It declares the logical roles your workflows name (`agent=<name>`) and which
profile or inline binding each one uses. A clone that tracks process under
`.satelle` needs this file to run any gated step. Secrets and machine-specific
execution detail never belong in it — put those in the catalog
(`~/.satelle/agents.toml`) or the gitignored `satelle.local.toml`.

`.satelle` as a whole stays **git-optional** (continuity is local disk or
personal rehydrate). This is recommended posture when a team tracks process,
not an enforcement: a repo that ignores all of `.satelle/` still runs.

**Fresh clone checklist**

1. If the repo commits `.satelle/workflows/agents.toml` and bindings are fully
   inline (no `profile=`), gated steps run after a normal init of any other
   missing scaffold.
2. If bindings reference profiles, seed or restore the catalog
   (`satelle agent migrate`, or restore a backed-up `~/.satelle/agents.toml`),
   then run `satelle agent validate` so every binding resolves.
3. Put secrets under `[vars]` in `satelle.local.toml` or the catalog — never in
   the committed agents file.

**Why committed.** Workflows name roles with `agent=<name>`; a clone whose
agents file never declares those roles cannot run the gated steps that name
them — the role set travels with the process it serves. The product already
behaves this way: `satelle init` scaffolds a runnable agents file whose seeded
body says secrets never belong in it, load is fail-closed on a missing or
malformed agents.toml, and the managed gitignore block ignores
`satelle.local.toml`, `workflows/agents.local.toml` and `.satelle/satelle`,
never `workflows/agents.toml`.
Operator-local pressure is relieved elsewhere: `profile=` keeps command, model,
and effort machine-side; `[vars]` plus `satelle.local.toml` keep secrets out of
the tree; repo-inline values still win per field. None of this forces git
tracking of `.satelle` as a whole — continuity stays local disk or personal
rehydrate; "committed substrate" means *if* you track process, track the agents
layer with it.

A profile that tries to carry process — `applies_to`, `skill`, `prompt`, `on`,
`output_*`, a workflow name — is **refused at load**. That refusal is the whole
point: a machine-wide file must not be able to change what any repo's process
*is*.

## The catalog

```toml
# ~/.satelle/agents.toml

[vars]
# Machine-wide KV for ${NAME} in a profile's env/settings. Secrets live here, on
# this machine — expanded in memory at dispatch, never written into a repo.
GLM_API_KEY = "sk-…"

[profiles.claude-opus]
role       = "reviewer"
interface  = "command"
command    = "claude -p --disallowedTools Write,Edit,NotebookEdit,Bash --append-system-prompt {system} --allowedTools {tools} --model {model}"
tools      = "Read,Grep,Glob"
model      = "opus"
effort     = "high"
timeout    = "45m"
principles = "session"
secondary  = "grok-acp"

[profiles.grok-acp]
role      = "reviewer"
interface = "acp"
command   = "grok agent stdio"
tools     = "read_file,grep,list_dir"
model     = "grok-4.5"

[roles]
# OPT-IN per-role defaults — see tier 3 below. Reaches only a repo that asks.
# reviewer = "claude-opus"

[seats]
# A seat's profile on every repo of this machine — see global-seat below.
# reviewer = "claude-opus"
```

A profile carries exactly the `AgentBinding` execution keys: `role`,
`interface`, `command`, `tools`, `model`, `effort`, `timeout`, `principles`,
`env`, `settings`, `secondary`, and `profile` (to extend another profile).
Anything else — a policy key or a typo — fails the load by name.

## Consuming a profile from a repo

```toml
# .satelle/workflows/agents.toml
[reviewer]
profile = "claude-opus"   # explicit reference
effort  = "low"           # …and this still wins over the profile's "high"
```

Scalar fields: the repo's non-empty value wins. `env` and `settings` merge
key-wise, with the repo's key winning; a repo cannot delete a profile's key.
**`role` is identity, not an override** — a repo declaring a role that
contradicts the profile's is refused rather than silently resolved either way.
Restating the same role is fine.

A profile may extend another with `profile = "<name>"`. The outermost wins field
by field; a reference cycle is refused, naming the loop.

## Precedence

Highest first:

0. **local** — an inline value in `.satelle/workflows/agents.local.toml`, this
   machine's override of the shared file. Same schema, field by field, and it
   may name the `profile=` and set `[defaults] use_global_roles` for this machine
   alone. It is gitignored and **never syncs**; `agent validate` and `doctor`
   warn on machine-specific content left in the shared file (`profile=`,
   `use_global_roles`, a literal env value, an absolute path), and a push of it
   is refused (`satelle help agent-dispatch`). Only agents.toml has a local twin.
   Between local and repo sits **global-seat**: the catalog's `[seats]` table
   maps a seat to a profile (`coder = "opus-coder"`) for every repo on this
   machine, so a repo needs no local file to pick its seats' models. A local
   `profile=` on the seat suppresses it.
1. **repo** — an inline value on the binding in `.satelle/workflows/agents.toml`
2. **profile** — the profile the binding explicitly names via `profile=`
   (and, transitively, whatever that profile extends)
3. **global-role** — the catalog's `[roles]` default for the binding's role, and
   **only** when the repo opts in with `[defaults] use_global_roles = true`
4. **embedded** — satelle's compiled fallback (`in-loop` for the executor, the
   default Claude template and read-only grant for the reviewer)

**There is no implicit same-name merge.** A profile called `reviewer` and a repo
`[reviewer]` that never mentions it do *not* combine — the repo resolves
byte-identically whether or not the catalog exists. A repo with no `profile=`
anywhere is untouched by anything the operator adds to the catalog later. That
guarantee is what makes a shared catalog safe on a machine holding pinned
repositories.

## Seeing what resolved, and from where

```bash
satelle agent profiles     # the catalog: every profile and role default
satelle agent validate     # every binding's effective fields + their source
```

`agent validate` renders each grant and then, per field, the tier that supplied
it:

```
GRANT [reviewer] role=reviewer … model="opus" effort="low" …
       source: command = "claude -p …" ← profile:claude-opus (~/.satelle/agents.toml)
       source: effort = "low" ← repo (.satelle/workflows/agents.toml)
       source: model = "opus" ← local (.satelle/workflows/agents.local.toml)
       source: tools = "Read,Grep,Glob" ← embedded
```

`env` and `settings` lines name the field and its source only — values may be
secrets and are never printed.

The same run refuses, with the offender named: a missing profile, a reference
cycle, a repo/profile role conflict, an invalid `interface`, a reviewer whose
merged binding escapes its read-only ceiling, and an unresolved `${VAR}`. The
ceiling is judged on the **merged** binding, so a profile cannot smuggle a
capability past a check by supplying it machine-wide.

## Variables

The catalog's `[vars]` is the base; a repo's own `[vars]` (and its gitignored
`satelle.local.toml` overlay) win per key. Expansion happens in memory at
dispatch wiring, so a machine-wide secret referenced as `${NAME}` reaches the
agent process without ever being written into a repository.

## Cloud identity

satelle no longer manages cloud identity or credentials: signing in, choosing an
identity and limiting its rights are your own tooling and the cloud's RBAC. A
catalog profile that declares a `cloud` (or `launch`) table is refused at load,
and the error names the table to delete from `~/.satelle/agents.toml`.

## Migrating

Nothing is required. With no catalog present, every existing repo — including
one relying on `~/.satelle/config.toml [agent] cli` — resolves exactly as
before.

```bash
satelle agent migrate      # seed ~/.satelle/agents.toml from the selected CLI
```

`migrate` is opt-in and non-destructive: it never overwrites an existing catalog
and never writes into a repository. The catalog it seeds leaves `[roles]`
commented out, so it changes nothing until a repo writes `profile = "…"`.

## Personal backup (operator runtime, not project sync)

The catalog is machine-wide operator runtime. Project `satelle sync` never
includes or requires it. To rehydrate a tuned catalog onto a new machine:

```bash
satelle login
satelle agent profiles push      # upload (requires login); [vars] is NEVER uploaded
satelle agent profiles restore   # download onto a clean home; refuses overwrite
satelle agent profiles restore --force   # replace existing (keeps agents.toml.bak)
```

**`[vars]` is never uploaded** — it is this machine's secret KV. After restore,
re-enter secrets under `[vars]` (or in `satelle.local.toml`). Unauthenticated
push/restore fail closed and name `satelle login` — they never silently no-op.

The personal store path is `PUT|GET /api/v1/me/files/agents.toml` (not project
config, not documents, not team publish). The hosted server must implement that
route; the CLI ships the client and the OpenAPI contract.

See also: `satelle help agent-dispatch` (how a dispatched step runs) and
`satelle help workflows` (where process lives).
