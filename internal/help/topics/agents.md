# Agents

`.satelle/agents.toml` is a free catalog of bindings:

    [agents.reviewer]
    command = "claude"           # claude | pi | grok | cursor-agent | session, or a command line
    model = "..."
    tools = ["Read", "Grep", "Glob"]

Permissions are tool grants, declared up front and enforced by the adapter. A
binding that judges a gate must have a read-only grant on an adapter that
enforces grants, and its grant must be one the adapter can express, or
`satelle validate --session <session>` refuses the configuration. With
`[rules] reviewer_differs_from_performer = true`, a judge may not run the model
of the performer whose work it judges.

Every dispatched performer is denied `satelle story set`, `satelle task set`
and `satelle execution set`: a performer never moves an item itself.

## Harness hooks

`satelle agents install claude|pi|grok|cursor --session <session>` writes the harness's hook file
so its interactive sessions run under the edit gate, the session-start
context, the prompt hook and the stop check: `.claude/settings.json`,
`.pi/extensions/satelle.ts`, `.grok/hooks/satelle.json` and
`.cursor/hooks.json`. A `cursor-agent` session and a session in the Cursor
IDE's agent drive stories this way (see `satelle help edit-gate --session <session>`). It writes no `.cursor/cli.json`.

`satelle status --session <session>` shows each harness's wiring in the repo (`installed`,
`not installed`, or `installed but git-ignored` with the ignore rule), and
`satelle doctor --session <session>` has a `harness` check: the calling session's harness unwired
is an issue with its fix, the other unwired harnesses are a note, and an
installed hook file an ignore rule hides from git is a note, since a clone
would not be wired. The caller is told from the session marker its harness
sets in the environment; when it cannot be told, no harness wired at all is
the issue. `satelle init --session <session>` names the harnesses it left unwired, and writes
`.satelle/README.md`, which tells an agent with no hooks to run these.

satelle's entries in a hook file are the commands whose first word is
`satelle` (by basename): `satelle hook ...` entries are replaced by the current
set, and one naming a command this binary lacks (such as pre-v1's `reindex`)
is removed. Every other entry is kept, a satelle command the binary has
(`satelle doctor --session <session>`) included. Only the first word is read, never shell syntax:
a command that wraps satelle (`sh -c "..."`, `env`, `timeout`, `NAME=value`,
an if-block) is not satelle's and is left as it is, so edit a wrapped hook by
hand. A file whose hooks would not change is not written.

A harness is in use in a repo when `.satelle/agents.toml` runs its CLI
(`claude`, `cursor-agent`, `grok`, `pi`) or the repo has its configuration
directory (`.claude/`, `.cursor/`, `.grok/`, `.pi/`). `satelle agents refresh --session <session>`
applies the install rule to each harness in use in every registered project
(`satelle help projects --session <session>`); `satelle update --session <session>` runs it after replacing the
binaries, and the sync pass (`satelle ledger autosync --session <session>`) runs it per project.
These never write a file git tracks: they name it, `satelle doctor --session <session>` reports
it, and `satelle agents install <harness> --session <session>` there writes the working tree for
you to commit in that repo. They also leave a `.pi/extensions/satelle.ts` that
is not satelle's, and every file of a repo whose `.satelle/satelle.toml` sets
`[hook] refresh = false`. `satelle doctor --session <session>` reports each harness in use whose
file differs from the current set (absent, holding none, running a satelle
command this binary lacks, or an older set), with `satelle agents install
<harness> --session <session>` as the fix.

## Adapters

The adapter is chosen by the basename of the command's first word.

| adapter | grant | denies | may judge |
|---|---|---|---|
| claude | --tools / --allowedTools | --disallowedTools, scoped Bash denies included | read-only grants |
| pi | --tools | plain denies removed from the grant; a scoped deny on a shell grant is refused | read-only grants |
| grok | --tools | --deny; a scoped deny on a shell grant is refused | read-only grants |
| cursor-agent | ask or force mode (below) | every shell command, in the run's own Cursor config | read-only grants |
| session | not dispatched: the interactive session performs | the edit gate | never |

## cursor-agent

Cursor's own headless CLI, validated on cursor-agent 2026.10.22 and
2026.11.05, signed in on the machine (`cursor-agent login`) and billed to
Cursor. A run is `cursor-agent -p --output-format stream-json --trust --model
<model>`; the skill and the work go on stdin, never in argv, and the run's
stream, every tool call included, is in its dispatch log.

- Every run gets its own Cursor config directory (`CURSOR_CONFIG_DIR`, a
  temporary directory removed when the run ends) holding the machine's sign-in
  (`auth.json`, linked) and a `cli-config.json` that denies every shell
  command (`Shell(**)`). Nothing is written to the working tree, and the
  user's own CLI settings are not carried.
- A read-only grant (Read, Grep, Glob) runs `--mode ask`, and can judge. Ask
  mode alone still runs commands Cursor deems read-only; the shell deny holds
  there too.
- An Edit and Write grant runs `--force`, and is still denied every shell
  command.
- Bash and scoped Bash grants are refused, at `satelle validate --session <session>`. Cursor's
  Shell rules match the commands Cursor parses, not text a shell or program
  later runs: a heredoc or here-string body, or an editor variable such as
  `GIT_EDITOR`, escapes them, so they cannot hold a shell to the performer
  denies. A Cursor performer with a shell waits on Cursor running under
  satelle's own hooks.
- Read, Grep and Glob are granted together, Edit and Write together.
  NotebookEdit is refused.
- Effort goes in the model id (e.g. `claude-opus-5-5-high`); a binding with
  `effort` is refused.
- A `.cursor/cli.json` in the run's directory or any directory above it that
  allows tools (`permissions.allow`) or sets `approvalMode`, or that does not
  decode, makes the run fail, naming the file. A deny-only file is allowed.
- A run that fails names why (cursor-agent missing, not signed in, an error
  result, no answer, a project config) and is never a verdict.
