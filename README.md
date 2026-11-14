# satelle

satelle is a stateless, generic CLI for agent-driven work. You define your
process as configuration under `.satelle/` (workflows, the gates on their
transitions, the performers of their states and the agents that do the work),
and satelle runs it. Every event is appended to a hash-chained ledger. The
binary holds no process of its own: every state, gate, skill and agent name
comes from your configuration.

Two static, pure-Go binaries: `satelle` (the CLI) and `satelled` (a local web
UI). The CLI's own manual is `satelle help --session <session>`; this file is the way in.

## Install

```sh
curl -fsSL https://github.com/bobmcallan/satelle/releases/latest/download/install.sh | sh
```

The script downloads the latest `satelle` and `satelled` for your platform,
checks each against its sha256, and installs them to `~/.local/bin` (override
with `SATELLE_INSTALL_DIR`). It then installs and starts the background
service (satelled and the sync timer) as `satelle service refresh --session <session>` does. Pass
`--no-service` (`... | sh -s -- --no-service`) or set `SATELLE_NO_SERVICE=1` to
leave the service alone.

From source: `make install` builds both binaries and places them in
`~/.local/bin`.

Already installed: `satelle update --session <session>` replaces both binaries with the latest
release and refreshes the service. `satelle version --session <session>` prints the installed
version.

## Quick start

Install, then run the rest in the repo satelle is to govern. Each line is
followed by what it prints.

```sh
curl -fsSL https://github.com/bobmcallan/satelle/releases/latest/download/install.sh | sh
# one "installed <path> (<tag>)" line for each of the two binaries,
# the service status, then "Next (a new repo):"

cd <your-repo>
satelle init --session <session>
# initialised <repo>/.satelle (configuration valid)
# harness hooks not installed: claude, pi, grok, cursor
#   and the command that wires one

satelle agents install claude --session <session>
# installed claude hooks: .claude/settings.json

satelle doctor --session <session>
# no issues
```

- `satelle init --session <session>` writes the starter configuration into `.satelle/`
  (`satelle.toml`, `agents.toml` and a two-state workflow, backlog to done, no
  gates), leaves any file that already exists alone, and validates the result.
  The configuration is yours to edit and is tracked in git. It also writes
  `.satelle/README.md`, which tells an agent whose harness is not wired what
  satelle is and what to run, and it ends by naming the harnesses left unwired.
- `satelle agents install <harness> --session <session>` wires an agent harness to satelle, so its
  sessions are told about satelle at session start and their edits pass through
  the edit gate. The harnesses are `claude`, `pi`, `grok` and `cursor`; name
  each one you use. `satelle init --hooks --session <session>` does both steps in one. Commit the
  file it writes so a clone is wired too.
- `satelle doctor --session <session>` reports what is wrong here, each issue with the command that
  fixes it, and changes nothing. It exits 0 with `no issues`, or 1 with each
  issue as `<check>: <problem>` and a `fix:` line under it, then a count.
  An unwired harness is one of its checks. `satelle status --session <session>` shows the repo's
  configuration, each harness's wiring, item counts and the session's seats.

Then drive work:

```sh
satelle story create --title "Ship the thing" --session <session>   # starts in the workflow's start state
satelle story set <id> --status <state> --session <session>         # presents a transition; its gates judge it
satelle story route <id> --session <session>                        # where the item is, and every gate result
satelle story seat <id> --session <session>                         # bind this session here, for the edit gate
```

`<session>` is the id the session-start context line prints. At a terminal
with no agent session, leave `--session` off.

`satelle story set --status` exits 0 when the item moved and 2 when a gate
rejected the move, listing each rejecting gate and its notes.

## An agent asked to install or check satelle

- **Is it installed?** `satelle version --session <session>`. If the command is missing, run the
  install line above.
- **Is this repo set up?** `satelle status --session <session>` in the repo. No `.satelle/` means
  `satelle init --session <session>` has not been run.
- **Is anything wrong?** `satelle doctor --session <session>`, and `satelle validate --session <session>` for the
  configuration alone.
- **Is this harness wired?** `satelle status --session <session>` lists each harness as installed
  or not, and `satelle doctor --session <session>` reports the calling harness when it is not. The
  hook file is in the repo with satelle's entries: `.claude/settings.json`, `.pi/extensions/satelle.ts`,
  `.grok/hooks/satelle.json` or `.cursor/hooks.json`. If not,
  `satelle agents install <harness> --session <session>`.
- **What can it do?** `satelle help --session <session>`, then `satelle help workflows --session <session>`,
  `satelle help edit-gate --session <session>` and `satelle help agents --session <session>`.

## What satelle keeps

- **Work items.** Stories, tasks and executions: id, title, body, acceptance
  criteria, status, tags, parent, category and the workflow the item runs
  under. Queues select items for a task.
- **The ledger.** Every event (an item created or updated, a transition, each
  gate's result, a seat bound or released, an attachment) is appended to a
  hash-chained ledger, one chain per item, under `~/.satelle/<repo-key>/`,
  outside the repo. The sqlite cache is rebuilt from it
  (`satelle ledger rebuild --session <session>`).
- **Configuration**, in the repo under `.satelle/` and tracked in git:
  `satelle.toml`, `agents.toml`, `workflows/<name>.toml` and `skills/<name>.md`.
  An unknown key refuses to run; `satelle validate --session <session>` lists every problem. See
  `satelle help configuration --session <session>`.

## Workflows, gates and agents

A workflow is states and the transitions between them. A transition carries
gates; a state may name a performer. Presenting a transition
(`satelle story set <id> --status <state> --session <session>`) runs its gates in their configured
order and records every result on the ledger; the item moves only when no gate
rejects. A gate is a skill: a deterministic check, or a rubric judged by an
agent with a read-only grant. Agents are bindings in `.satelle/agents.toml`,
each a command, a model and the tools it is granted.

Process is configuration: change a workflow or a skill and the process changes,
with no new binary. `satelle workflow show --session <session>` prints the configured workflows.
See `satelle help workflows --session <session>` and `satelle help agents --session <session>`.

## The edit gate

With a harness wired, every edit and shell command a session makes is judged by
`satelle hook gate`. An edit is allowed only while the session holds a seat, at
this worktree, on an item whose current state's performer may change the tree
(a state whose performer produces only attachments, such as plan, allows no
tree edits; record its work as an attachment):

```sh
satelle story seat <id> --session <session>
satelle story release <id> --session <session>
```

The session-start context names the session id. See `satelle help edit-gate --session <session>`.

## The service and satelle.dev

`satelled` is a local, read-only web UI over this machine's projects, run as a
per-user service beside a timer that syncs each project's ledger.
`satelle service status --session <session>` reports the unit and whether satelled answers;
`satelle service install --session <session>`, `satelle service refresh --session <session>` and
`satelle service restart --session <session>` manage it. `satelle project add --session <session>` adds a repo to
this machine's projects.

Signing in is optional: satelle runs locally without it. `satelle login --session <session>` signs
in to satelle.dev, and `satelle project bind <project> --session <session>` binds this repo to a
project there, so its ledger syncs between machines. See
`satelle help projects --session <session>` and `satelle help ledger-store --session <session>`.

A repo set up by a pre-1.0 satelle is moved with `satelle upgrade --session <session>`; see
`satelle help upgrade --session <session>`.

## Commands

| Commands | What they do |
|----------|--------------|
| `satelle init --session <session>`, `satelle validate --session <session>`, `satelle doctor --session <session>`, `satelle status --session <session>` | set a repo up, check its configuration, report what is wrong, show where it stands |
| `satelle story`, `satelle task`, `satelle execution`, `satelle queue` | work with items: create, show, list, update, present a transition, seat, attach, dispatch a performer |
| `satelle workflow show --session <session>` | show the configured workflows |
| `satelle agents install`, `satelle agents remove`, `satelle agents refresh --session <session>` | install, remove or refresh harness hook files |
| `satelle ledger` | list events, rebuild the cache, and push or pull the ledger store |
| `satelle remote` | fire a remote for an item and follow its run |
| `satelle project`, `satelle workspace --session <session>`, `satelle login --session <session>`, `satelle logout --session <session>`, `satelle whoami --session <session>` | this machine's projects and satelle.dev |
| `satelle service`, `satelle settings --session <session>`, `satelle logs --session <session>`, `satelle runtime` | the background service, machine settings, logs and runtime dirs |
| `satelle update --session <session>`, `satelle upgrade --session <session>`, `satelle version --session <session>` | replace the binaries, move a pre-1.0 repo to v1, print the version |

`satelle <command> --help` gives each command's usage, and `satelle help --session <session>` lists
the topics.

## Development

```sh
go test ./...        # the unit tests of every package
make test            # every package and the integration suite under tests/, in a hermetic home
make integration     # only the integration suite
make install         # build both binaries into ~/.local/bin
```

The integration suite builds `satelle` and `satelled` once and drives them as a
user would, each test in its own home and a fresh git repo.

GitHub runs no tests: they run locally, in this repo's own satelle gates.
satelle is built with satelle; its process is the configuration under
[`.satelle/`](./.satelle). A release is cut by that process, which dispatches
[`.github/workflows/release.yml`](./.github/workflows/release.yml); a push to
main publishes nothing. `.version` holds the version of each binary.

## License

MIT. See [LICENSE](./LICENSE).
