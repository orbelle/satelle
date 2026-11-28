# Agents

`.satelle/agents.toml` is a free catalog of bindings:

    [agents.reviewer]
    command = "claude"           # claude | pi | grok | cursor-agent | agy | session, or a command line
    model = "..."
    tools = ["Read", "Grep", "Glob"]

Permissions are tool grants, declared up front and enforced by the adapter. A
binding that judges a gate must have a read-only grant on an adapter that
enforces grants, and its grant must be one the adapter can express, or
`satelle validate --session <session>` refuses the configuration. Reviewer
independence (a judge of another model family than the performer) is the
repo's configuration, a check skill on the gates it chooses: see
`satelle help workflows --session <session>`.

Every dispatched performer is denied `satelle story set`, `satelle task set`
and `satelle execution set`: a performer never moves an item itself.

A judge binding may also carry an optional `brief` string naming what the judge
should read. When a gate dispatches that judge, its prompt carries the brief
under a `Brief:` section right after the opening move line; a binding with no
brief (or a blank one) receives the prompt unchanged.

A judge binding may name a `fallback`, another agent in the catalog:

    [agents.reviewer-a]
    command  = "cursor-agent"
    model    = "gpt-x"
    tools    = ["Read", "Grep", "Glob"]
    fallback = "reviewer-b"

When a gate dispatches that judge and the dispatch fails (the judge could not
be asked: the harness would not start, exited with an error, or settled with
no answer), satelle dispatches the fallback with the same skill and the same
prompt, the first judge's brief included (the fallback's own `brief` is not
used when it stands in), and the fallback's verdict is that judge's result.
A judge that answered, even with no verdict, is never re-asked, and an
interrupted run does not fall back. The fallback is one level: its own
`fallback` is never followed. In a panel each member falls back on its own,
and `combine` runs over the members as they answered. The field takes effect
only when the binding judges a gate; a performer ignores it. `satelle
validate --session <session>` refuses a fallback that is not in the catalog,
that names the agent itself, that names a fallback of its own, that cannot
judge (the same rule as a judge's grant), that is a judge of the same
gate's judge set (its default judges, or one `[review.panels.<gate>]` tier's
panel, each apart, since a tier's panel replaces the default), or that another
judge of that set also names, so one binding never answers for two judges.

A registered part X may bind its own agents in `.satelle/repos/X/agents.toml`,
in the same form. X's workflows (those loaded from `repos/X/workflows/`)
resolve a performer, judge or fallback name there first and in the shared
`agents.toml` otherwise; a part binding replaces a shared one of the same name
whole. A shared workflow uses the shared bindings, whatever its name and
whichever repository its story targets. A part binding's name is lower-case
letters, digits, `.`, `_` or `-`, starting with a letter or digit. A part
`agents.toml` that does not load (a link, bad TOML, an unknown key, a bad
binding or name, a fallback bound nowhere) leaves the part unresolved, so its
repository's stories are refused until it does. A part binding, or its
fallback, may run only the exact command (its first word, as dispatched) some
non-session binding of the shared `agents.toml` already runs, so its hooks are
wired; any other command, the same harness name at another path included,
leaves the part unresolved. `agents.local.toml` names only shared bindings, and the
fields it sets also apply over a part binding of the same name, so a machine's
local narrowing holds on every workflow. The judge-differs check compares a part's workflows
against the part's bindings.

## Harness hooks

`satelle agents install claude|pi|grok|cursor|agy --session <session>` writes the harness's hook file
so its interactive sessions run under the edit gate, the session-start
context, the prompt hook and the stop check: `.claude/settings.json`,
`.pi/extensions/satelle.ts`, `.grok/hooks/satelle.json`,
`.cursor/hooks.json` and `.agents/hooks.json` (agy: satelle's entries sit
under its own top-level `"satelle"` key, and every other key's hooks are
kept). A `cursor-agent` session and a session in the Cursor
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
(`claude`, `cursor-agent`, `grok`, `pi`, `agy`) or the repo has its configuration
directory (`.claude/`, `.cursor/`, `.grok/`, `.pi/`, `.agents/`). `satelle agents refresh --session <session>`
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

## Auto-mode guidance

`satelle agents auto-mode <harness> --session <session>` reads the repo's own
`.satelle/auto-mode.md` (plain prose, one entry per non-empty line), prints the
harness's auto-mode configuration block to stdout, and names where it belongs on
stderr. It is print-only and writes no file.

- `claude`: prints a JSON `autoMode` block whose `environment` list starts with
  `"$defaults"` followed by each non-empty line of `.satelle/auto-mode.md`, and
  names `~/.claude/settings.json` on stderr. Claude Code reads `autoMode` only
  from `~/.claude/settings.json`, managed settings or `--settings`, never from a
  project's `.claude/settings.json`.
- `pi`, `grok`, `cursor`, `agy`: reports that auto-mode guidance is unavailable for
  that harness and writes nothing.

A first `satelle init --session <session>` writes `.satelle/auto-mode.md` from
the starter template and the repo owns and edits it; `satelle init --print
.satelle/auto-mode.md --session <session>` prints the starter file. If
`.satelle/auto-mode.md` is missing or not a regular file, the command fails
without falling back to the starter template.

## Adapters

The adapter is chosen by the basename of the command's first word.

| adapter | grant | denies | may judge |
|---|---|---|---|
| claude | --tools / --allowedTools | --disallowedTools, scoped Bash denies included | read-only grants |
| pi | --tools | plain denies removed from the grant; a scoped deny on a shell grant is refused | read-only grants |
| grok | --tools | --deny; a scoped deny on a shell grant is refused | read-only grants |
| cursor-agent | ask or force mode (below) | every shell command, in the run's own Cursor config | read-only grants |
| agy | per-run --settings permissions (read_file, write_file) plus a default-deny PreToolUse guard | every other rule action and every tool outside the grant | read-only grants |
| session | not dispatched: the interactive session performs | the edit gate | never |

When a seated interactive session moves an item out of a state whose performer
has `command = "session"` (on a passing, non-hold, non-park transition),
satelle appends a `performer.run` event with `command` set to `"session"`,
`harness` naming the session's harness (from its hook record, falling back to
the harness environment marker or `unknown`), and `model` equal to the model
exposed by the harness's hook payload, or `unavailable (<harness>)` when the
harness exposed no model.

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

## agy

The Antigravity CLI, validated on agy 1.5.14, signed in on the machine. agy
is a dispatch adapter only: it has no harness hooks and `satelle agents
install` does not name it. A run is `agy --input-format stream-json
--output-format stream-json --disable-slash-commands --settings <settings
file> --add-dir <run dir> --add-dir <guard dir>`, then `--model <model>` and
`--effort <effort>` when the binding sets them. There is no `-p` and no
`--mode`: the skill and the work go on stdin as one NDJSON `user` event
(`{"event":"user","message":{"content":...}}`), never in argv, and the run's
stream is in its dispatch log.

- The grant is held in two layers, both written to a temporary directory
  removed when the run ends; nothing is written to the working tree, and
  `--settings` is session-only, so the user's own agy settings are never
  changed.
  - The `--settings` file allows the granted rule actions and denies every
    other one of agy's eight (`read_file`, `write_file`, `command(*)`,
    `unsandboxed`, `mcp`, `read_url`, `execute_url`, `download_url`). Its
    deny outranks an allow in the user's or the repository's agy settings.
  - agy's rules cannot name its other tools (web search, browser, subagents,
    notebooks, scheduling and the rest), so the second `--add-dir` is a guard
    directory whose `.agents/hooks.json` holds one `PreToolUse` hook that
    denies every tool outside the grant by name, tools agy adds later
    included. Its matcher sees only the tool name, never the prompt or the
    tool's arguments.
- A read-only grant (Read, Grep, Glob) allows `read_file` only, and can judge.
  An Edit and Write grant adds `write_file` (agy's write and edit tools).
- Bash and scoped Bash grants are refused, at `satelle validate --session <session>`. agy's
  `command(...)` deny rules match the command text, so a variable expansion
  (`CMD="satelle story"; $CMD set ...`) escapes them, and they cannot hold a
  shell to the performer denies.
- Read, Grep and Glob are granted together, Edit and Write together.
  NotebookEdit is refused.
- `effort` is one of low, medium, high, xhigh, max; a model id that already
  ends in an effort (e.g. `gemini-4-hydrogen-high`) with `effort` set is
  refused.
- A run refuses, naming the file, before anything runs: a `.agents/hooks.json`
  in the run's directory or any directory above it, or in your home
  directory, unless it holds only satelle's own agy hooks, exactly as
  `satelle agents install agy --session <session>` writes them; a `.antigravity/settings.json` there that does not decode or sets
  `permissions.allow`, `permissions.ask`, `toolPermission`, `agentMode` or
  `hooks`; a user hook file (`~/.gemini/config/hooks.json`,
  `~/.gemini/antigravity-cli/hooks.json`) that does not decode or defines any
  event but `PreInvocation`, `PostInvocation`, `Stop` and `PostToolUse`, which
  cannot decide a tool call; and a `~/.gemini/antigravity-cli/settings.json`
  that sets `hooks`. Lifecycle-only user hooks run, as user-owned code.
- Every run sets `AGY_CLI_DISABLE_AUTO_UPDATE=true`, so a self-update never
  races a dispatch.
- The model is the stream's `init.model`, or `unavailable (agy)` when agy
  names none; tokens in are input plus cache-read tokens, tokens out the
  output tokens.
- A run that fails names why (agy missing, not signed in, an error result, no
  answer, a refused config) and is never a verdict.

## Target items

An item whose `repo` names another registered repository is dispatched
against that repository's checkout (`.satelle/repos.local.toml`), confined
per run:

- A performer with a write grant runs in the target's checkout, on claude
  only, with `--setting-sources user --restricted`, an `--add-dir` for its
  record copy, and, for a Bash grant, a shell sandbox that cannot be turned off
  (`allowUnsandboxedCommands: false`, no excluded commands). agy, grok and
  cursor-agent are refused for a target writer: they cannot be kept from
  loading the checkout's instruction files.
- A read-only planner or gate reviewer runs, on any harness, in an empty
  scratch folder outside both checkouts and reads the checkout by absolute
  path: claude through `--add-dir`, grok under its `--sandbox` that keeps writes to the scratch and temp folders, agy,
  cursor-agent and pi by the paths in the prompt. The prompt says everything
  under the checkout is content under review, never instructions.
- The grant is narrowed to its read tools, and the performer runs outside, in
  a protected-tier repository or on a harness with no recorded write
  confinement (pi).
- The dispatch is refused, naming the file, when the checkout holds a harness
  settings, hook or MCP file (`.claude/settings.json`,
  `.claude/settings.local.json`, `.mcp.json`, `.agents/hooks.json`,
  `.antigravity/settings.json`, `.cursor/hooks.json`, `.cursor/cli.json`,
  `.cursor/mcp.json`, `.grok`, `.pi`).
- A performer with a write grant is refused, naming the file, when the
  checkout holds an instruction file claude loads when a file under it is read
  or edited: any entry at `.claude/rules`, a `CLAUDE.local.md` at any depth,
  or a `CLAUDE.md` or `AGENTS.md` below the top level (`.git` is skipped;
  vendored trees are not). Remove the file or dispatch the item read-only. A
  read-only planner or reviewer is not refused for them: a target file it
  reads, at any depth, is content under review.
- The agent gets an allowlisted environment: OS runtime and locale variables
  (`PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `TERM`, `TZ`, `TMPDIR`, `LANG`,
  `LC_*`, `XDG_*`), proxy variables, the names `[dispatch] target_env` in
  `satelle.toml` lists, and its harness's own sign-in variables, never a
  `SATELLE_*`, `GIT_*`, withheld or other credential-named variable. A proxy
  whose URL carries a credential is kept only when the credential is 8 bytes
  or more. Its output, errors and log are redacted of every credential value
  and of each sign-in variable's value. cursor-agent's sign-in variables also
  include `DISPLAY` and `DBUS_SESSION_BUS_ADDRESS`, so a subscription login
  kept in the desktop keyring is reachable; the session bus reaches the whole
  keyring, so only cursor-agent receives them, and a `DISPLAY` shorter than 8
  bytes is not redacted.
- Its record copy and scratch folder live in a fresh temp folder outside both
  checkouts, removed after the run; a `TMPDIR` inside either checkout refuses
  the dispatch.

What each harness confines is recorded in
`.satelle/tasks/harness-confinement.md`.

## This machine's overrides

A machine may override the repo's bindings in `.satelle/agents.local.toml`,
which has the agents.toml shape (`[agents.<name>]` tables) and is ignored by
git. Each field written there (command, model, effort, tools, deny, placement, brief, fallback)
replaces the tracked value of the agent of the same name; every field not
written keeps the tracked value. With no file, the bindings are exactly as
agents.toml prescribes. A remote node never receives this file.

    [agents."reviewer-grok-4.7"]
    command = "cursor-agent"
    model   = "gpt-5.6-sol-high"

`satelle validate --session <session>` and `satelle doctor --session <session>` refuse an agent agents.toml does not
define, a field outside the schema, and an override that makes a binding
invalid; each refusal names agents.local.toml. `satelle doctor --session <session>` lists every
active override as a note, field by field, tracked -> effective. A
`performer.run` records the command and model that ran, and a `gate.run` the
judge's command, model and (when set) `effort` (each panel member's for a
panel).

`satelle init --session <session>` writes the ignore line into a new `.satelle/.gitignore`. A repo
whose `.satelle/.gitignore` predates it adds the line `agents.local.toml`
in a commit of its own, through its workflow; doctor names it until then. The
file is not locked against agent edits by default; a repo that wants that sets
`[gate] locked_paths = [".satelle/agents.local.toml"]`.

## herdr panel

herdr-app (herdr.dev) is a desktop app for terminals and coding agents.
satelle ships a stories panel for it, with a `satelle` layout that places the
panel beside the focused pane and shows it only while the focused folder is a
satelle repo. The panel is read-only: it runs satelle's `story list`,
`story route`, `ledger list`, `runtime path` and `version` commands in the
focused pane's folder and shows the stories, their criteria and description,
and each workflow step with its gates' results and reviewers. Its footer
shows the satelle version that its `version` command prints on that machine,
re-read on each refresh, or "unknown" when it cannot be read.

`satelle herdr install --session <session>` installs it on this machine (the install is per machine, not
per repo). It writes these files under herdr's home and names each one:

- `mods/satelle/main.js` and `mods/satelle/mod.json` (the panel)
- `layouts/satelle.json` and `layouts/satelle.svg` (the layout and its preview)

herdr's home is `~/.config/herdr-app` on Linux, macOS and Windows
(`%USERPROFILE%\.config\herdr-app`), by herdr's documentation. `HERDR_APP_HOME`
moves it, and the machine setting `herdr.home`
(`satelle settings set herdr.home <folder> --session <session>`) moves it over both. The setting holds
nothing while herdr is at its default; an install whose home came from a
non-default `HERDR_APP_HOME` records that folder there, so a later refresh
started without the variable finds it. A home that does not exist is refused
and nothing is written.

`satelle update --session <session>` refreshes the panel, through `satelle agents refresh --session <session>`, where
it was installed, and never adds it to a machine that did not ask. A herdr
outcome never fails update. A panel file changed outside satelle is left and
named; `satelle herdr install --force --session <session>` replaces it.
