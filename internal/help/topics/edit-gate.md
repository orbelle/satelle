# The edit gate and blocks

Harness hooks (`satelle agents install claude|pi|grok|cursor`) send every edit
and shell command a session makes to `satelle hook gate`. The gate allows an edit only
when **the editing session holds a seat, at this worktree, on an item whose
current state has a performer**:

    satelle story seat <id> --session <session>    # location defaults to local:<worktree>
    satelle story release <id> --session <session>

The session id comes from the harness's hook payload, or from
`SATELLE_SESSION`; the context hook prints it at session start.

A performer satelle dispatches here (`satelle <kind> perform`, or a drive) is
seated for its run as session `perform_<hex>` at `local:<worktree>`. It
receives the session as `SATELLE_SESSION` and the worktree as
`SATELLE_PROJECT_DIR`, and the seat is released when the run ends, also on
failure.

Every seat records the machine it was bound on (an id kept in
`~/.satelle/machine-id`, folded with the host's machine id), its host name and
the satelle user who bound it: the signed-in identity (the email `satelle
whoami` names, else its display name), else `<OS user>@<host>`. The users
holding a seat are the item's assignees beside any set with `--assignee`:
`story get`, `story list --json` and satelled list them once each, marking
the seated ones working, and a released or gone seat drops its user.
Whether its session still runs is known only on that machine, from
`~/.satelle/sessions/<session>.json`: the SessionStart hook records its hook
processes, each hook call (session start, prompt, seat command) settles the
session's owner from the process it shares with an earlier call (the
harness), and a dispatched run records its own process. A live owner is kept
when SessionStart fires again in the same session. `satelle doctor` and
satelled class each seat as **here** (this machine, owner running),
**elsewhere** (another machine, or a remote or cloud location), **gone** (this
machine, its settled owner exited) or **unverified** (no machine recorded, no
session record, no settled owner even when its hook processes have exited, or
no proof either way). satelled marks the story ◆ here for a seat here and
◇ here for an unverified one, with the reason on hover, and shows each seat
elsewhere on the row at its user and place.
`satelle doctor --resolve` releases a gone one without asking, and asks about
a seat here whose session never settled an owner, never releasing it unasked.

Always and never:

- `[gate] edit_exempt_paths` (globs relative to the worktree) are always allowed.
- `[gate] locked_paths` are never allowed, whatever the seat.
- Temp directories, and paths in no git working tree, are outside the gate.
- Another repository's working tree is refused (the fence); a linked worktree of
  this repository is judged by the seat at that worktree.
- While the configuration is invalid, only `.satelle/` itself may be edited.
- A seat on an item in an operator state (`performer = { operator = "<type>" }`)
  allows no edits, and the operator's record verb (`satelle <kind> record`) is
  refused for every session. The verb is refused as a command word behind any
  wrapper, and in text a shell or interpreter runs: `-c` programs, heredocs and
  here-strings fed to one, `script -c`, a command substitution, and text piped
  into a command that is not data. Prose given as data to `echo`, `printf`,
  `grep`, `cat`/`head`/`tail`/`wc`, a git message or read command
  (`git commit`/`tag`/`log`), or satelle's own flags is not scanned. Other
  commands still are, so pass such prose with `-F <file>`. These checks run as
  the agent's own OS user: they stop an agent session, not a deliberate
  forgery of the runtime ledger.

A shell command is judged by the same rule, applied to every path it would
write (redirects, `tee`, `cp`/`mv`/`rm`/`sed -i` and similar, `git -C`) and to
the working directory of any interpreter running inline program text.

## Cursor sessions

`satelle agents install cursor` writes satelle's entries into the repo's
`.cursor/hooks.json` (version 1), keeping every other entry; `satelle agents
remove cursor` strips only satelle's. Cursor (the IDE and `cursor-agent`) runs:

- `preToolUse` (matcher `Write|Delete|Shell`) → `satelle hook gate --harness
  cursor`. A refusal answers `permission: deny` with the same reason the claude
  gate gives, before the file is written or the command runs. The session's
  repo comes from `CLAUDE_PROJECT_DIR`, else `CURSOR_PROJECT_DIR` (Cursor sets
  both), else the payload's project root; the fence applies as above.
- `sessionStart` → `satelle hook context`: the session id (Cursor's
  `session_id`, the same as its `conversation_id`), its seats, the doctor block
  and the `[hook] inject` files, as `additional_context`.
- `beforeSubmitPrompt` → `satelle hook prompt`. It settles the session's owner
  and answers `{"continue":true}`. Cursor's prompt event has no context
  channel, so a Cursor session gets no per-prompt seat reminder; the seats are
  in its session-start context and in `satelle status`.
- `stop` → `satelle hook stopcheck`, which answers `{}`: no `followup_message`,
  so Cursor ends the turn.

`cursor-agent -p` (print mode) fires sessionStart and preToolUse but not
beforeSubmitPrompt or stop.

Cursor also runs a repo's `.claude/settings.json` hooks. Once Cursor's own are
installed, a claude entry called by Cursor (its payload carries
`cursor_version`) answers nothing, so no edit is judged and no context injected
twice. The environment does not count: a claude session started from Cursor's
terminal keeps its gate.

`[hook] allow` in `satelle.toml` lists command prefixes (plain words, e.g.
`"satelle story set"`), empty by default. A Cursor shell call made of only such
commands (unquoted plain words, joined by `;`, `&&`, `||` or newlines) that the
gate allows is answered `permission: allow`. A dispatched run
(`SATELLE_SESSION` set) never is, and the gate's refusals come first, so an
entry cannot allow `satelle <kind> record`. `cursor-agent` 2026.11.05 in print
mode does not lift its own shell refusal for a hook's allow: there only
Cursor's permission rules (`permissions.allow`, e.g.
`Shell(satelle story set)`) do.

A **block** is a deny with its reason: the harness shows it to the agent and
the tool call does not run. A rejected transition (`satelle story set --status`,
exit 2) is the other block: the item stays where it is, and the rejecting gates'
notes say what to change before presenting it again.
