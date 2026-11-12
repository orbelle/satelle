# satelle

satelle is a stateless, generic CLI for agent-driven work. You define your
process as configuration under `.satelle/` (workflows, the gates on their
transitions, the performers of their states and the agents that do the work),
and satelle runs it. Every event is appended to a hash-chained ledger. The
binary holds no process of its own: every state, gate, skill and agent name
comes from your configuration.

Two static, pure-Go binaries: `satelle` (the CLI) and `satelled` (a local web
UI). The CLI's own manual is `satelle help`; this file is the way in.

## Install

```sh
curl -fsSL https://github.com/bobmcallan/satelle/releases/latest/download/install.sh | sh
```

The script downloads the latest `satelle` and `satelled` for your platform,
checks each against its sha256, and installs them to `~/.local/bin` (override
with `SATELLE_INSTALL_DIR`). It then installs and starts the background
service (satelled and the sync timer) as `satelle service refresh` does. Pass
`--no-service` (`... | sh -s -- --no-service`) or set `SATELLE_NO_SERVICE=1` to
leave the service alone.

From source: `make install` builds both binaries and places them in
`~/.local/bin`.

Already installed: `satelle update` replaces both binaries with the latest
release and refreshes the service. `satelle version` prints the installed
version.

## Quick start

Install, then run the rest in the repo satelle is to govern. Each line is
followed by what it prints.

```sh
curl -fsSL https://github.com/bobmcallan/satelle/releases/latest/download/install.sh | sh
# one "installed <path> (<tag>)" line for each of the two binaries,
# the service status, then "Next (a new repo):"

cd <your-repo>
satelle init
# initialised <repo>/.satelle (configuration valid)

satelle agents install claude
# installed claude hooks: .claude/settings.json

satelle doctor
# no issues
```

- `satelle init` writes the starter configuration into `.satelle/`
  (`satelle.toml`, `agents.toml` and a two-state workflow, backlog to done, no
  gates), leaves any file that already exists alone, and validates the result.
  The configuration is yours to edit and is tracked in git.
- `satelle agents install <harness>` wires an agent harness to satelle, so its
  sessions are told about satelle at session start and their edits pass through
  the edit gate. The harnesses are `claude`, `pi`, `grok` and `cursor`; name
  each one you use. `satelle init --hooks` does both steps in one. Commit the
  file it writes so a clone is wired too.
- `satelle doctor` reports what is wrong here, each issue with the command that
  fixes it, and changes nothing. It exits 0 with `no issues`, or 1 with each
  issue as `<check>: <problem>` and a `fix:` line under it, then a count.
  `satelle status` shows the repo's configuration, item counts and the
  session's seats.

Then drive work:

```sh
satelle story create --title "Ship the thing"   # starts in the workflow's start state
satelle story set <id> --status <state>         # presents a transition; its gates judge it
satelle story route <id>                        # where the item is, and every gate result
satelle story seat <id>                         # bind this session here, for the edit gate
```

`satelle story set --status` exits 0 when the item moved and 2 when a gate
rejected the move, listing each rejecting gate and its notes.

## An agent asked to install or check satelle

- **Is it installed?** `satelle version`. If the command is missing, run the
  install line above.
- **Is this repo set up?** `satelle status` in the repo. No `.satelle/` means
  `satelle init` has not been run.
- **Is anything wrong?** `satelle doctor`, and `satelle validate` for the
  configuration alone.
- **Is this harness wired?** The harness's hook file is in the repo with
  satelle's entries: `.claude/settings.json`, `.pi/extensions/satelle.ts`,
  `.grok/hooks/satelle.json` or `.cursor/hooks.json`. If not,
  `satelle agents install <harness>`.
- **What can it do?** `satelle help`, then `satelle help workflows`,
  `satelle help edit-gate` and `satelle help agents`.

## What satelle keeps

- **Work items.** Stories, tasks and executions: id, title, body, acceptance
  criteria, status, tags, parent, category and the workflow the item runs
  under. Queues select items for a task.
- **The ledger.** Every event (an item created or updated, a transition, each
  gate's result, a seat bound or released, an attachment) is appended to a
  hash-chained ledger, one chain per item, under `~/.satelle/<repo-key>/`,
  outside the repo. The sqlite cache is rebuilt from it
  (`satelle ledger rebuild`).
- **Configuration**, in the repo under `.satelle/` and tracked in git:
  `satelle.toml`, `agents.toml`, `workflows/<name>.toml` and `skills/<name>.md`.
  An unknown key refuses to run; `satelle validate` lists every problem. See
  `satelle help configuration`.

## Workflows, gates and agents

A workflow is states and the transitions between them. A transition carries
gates; a state may name a performer. Presenting a transition
(`satelle story set <id> --status <state>`) runs its gates in their configured
order and records every result on the ledger; the item moves only when no gate
rejects. A gate is a skill: a deterministic check, or a rubric judged by an
agent with a read-only grant. Agents are bindings in `.satelle/agents.toml`,
each a command, a model and the tools it is granted.

Process is configuration: change a workflow or a skill and the process changes,
with no new binary. `satelle workflow show` prints the configured workflows.
See `satelle help workflows` and `satelle help agents`.

## The edit gate

With a harness wired, every edit and shell command a session makes is judged by
`satelle hook gate`. An edit is allowed only while the session holds a seat, at
this worktree, on an item whose current state has a performer:

```sh
satelle story seat <id> --session <session>
satelle story release <id> --session <session>
```

The session-start context names the session id. See `satelle help edit-gate`.

## The service and satelle.dev

`satelled` is a local, read-only web UI over this machine's projects, run as a
per-user service beside a timer that syncs each project's ledger.
`satelle service status` reports the unit and whether satelled answers;
`satelle service install`, `satelle service refresh` and
`satelle service restart` manage it. `satelle project add` adds a repo to
this machine's projects.

Signing in is optional: satelle runs locally without it. `satelle login` signs
in to satelle.dev, and `satelle project bind <project>` binds this repo to a
project there, so its ledger syncs between machines. See
`satelle help projects` and `satelle help ledger-store`.

A repo set up by a pre-1.0 satelle is moved with `satelle upgrade`; see
`satelle help upgrade`.

## Commands

| Commands | What they do |
|----------|--------------|
| `satelle init`, `satelle validate`, `satelle doctor`, `satelle status` | set a repo up, check its configuration, report what is wrong, show where it stands |
| `satelle story`, `satelle task`, `satelle execution`, `satelle queue` | work with items: create, show, list, update, present a transition, seat, attach, dispatch a performer |
| `satelle workflow show` | show the configured workflows |
| `satelle agents install`, `satelle agents remove` | install or remove harness hook files |
| `satelle ledger` | list events, rebuild the cache, and push or pull the ledger store |
| `satelle remote` | fire a remote for an item and follow its run |
| `satelle project`, `satelle workspace`, `satelle login`, `satelle logout`, `satelle whoami` | this machine's projects and satelle.dev |
| `satelle service`, `satelle settings`, `satelle logs`, `satelle runtime` | the background service, machine settings, logs and runtime dirs |
| `satelle update`, `satelle upgrade`, `satelle version` | replace the binaries, move a pre-1.0 repo to v1, print the version |

`satelle <command> --help` gives each command's usage, and `satelle help` lists
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
