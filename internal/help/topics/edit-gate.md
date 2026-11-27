# The edit gate and blocks

Harness hooks (`satelle agents install claude|pi|grok|cursor|agy --session <session>`) send every edit
and shell command a session makes to `satelle hook gate`. The gate allows an edit only
when **the editing session holds a seat, at this worktree, on an item whose
current state's performer may change the tree** (a state with an agent
performer and `edits` unset or `edits = "tree"`, see
`satelle help workflows --session <session>`):

    satelle story seat <id> --session <session>    # location defaults to local:<worktree>
    satelle story release <id> --session <session>

The session id comes from the harness's hook payload, and the context hook
prints it at session start, also while the configuration is invalid (without
seats), so the session can seat once `satelle validate --session <session>` passes. A session passes it as `--session <session>` on
every satelle command, each run on its own as one simple command, with
`satelle -C <worktree> ... --session <session>` for another worktree.

A performer satelle dispatches here (`satelle <kind> perform`, or a drive) is
seated for its run as session `perform_<hex>` at `local:<worktree>`. It
receives the session as `SATELLE_SESSION` and the worktree as
`SATELLE_PROJECT_DIR`, and the seat is released when the run ends, also on
failure.

Every seat records the machine it was bound on (an id kept in
`~/.satelle/machine-id`, folded with the host's machine id), its host name and
the satelle user who bound it: the signed-in identity (the email `satelle
whoami --session <session>` names, else its display name), else `<OS user>@<host>`. The users
holding a seat are the item's assignees beside any set with `--assignee`:
`story get`, `story list --json` and satelled list them once each, marking
the seated ones working, and a released or gone seat drops its user.
Whether its session still runs is known only on that machine, from
`~/.satelle/sessions/<session>.json`: the SessionStart hook records its hook
processes, each hook call (session start, prompt, seat command) settles the
session's owner from the process it shares with an earlier call (the
harness), hook calls record the session's harness and any model exposed by the
hook payload (for session `performer.run` attribution), and a dispatched run
records its own process. A live owner is kept
when SessionStart fires again in the same session. `satelle doctor --session <session>` and
satelled class each seat as **here** (this machine, owner running),
**elsewhere** (another machine, or a remote or cloud location), **gone** (this
machine, its settled owner exited) or **unverified** (no machine recorded, no
session record, no settled owner even when its hook processes have exited, or
no proof either way). satelled marks the story ◆ here for a seat here and
◇ here for an unverified one, with the reason on hover, and shows each seat
elsewhere on the row at its user and place.
`satelle doctor --resolve --session <session>` releases a gone one, and a seat on an item in a
terminal state wherever its session ran, without asking, and asks about a seat
here whose session never settled an owner, never releasing it unasked.

Always and never:

- `[gate] edit_exempt_paths` (globs relative to the worktree) are always allowed,
  except from an attachments-only seat (below).
- `[gate] locked_paths` are never allowed, whatever the seat; from an
  attachments-only seat they are refused with that seat's reason.
- A seat at this worktree on an item in a state that allows attachments only
  (`edits = "attachments"`, for example plan) refuses every path of the tree,
  exempt and locked paths included, also when the session holds another seat
  there on a state whose performer may change the tree: the refusal names the
  state and that its performer produces an attachment. Record that state's work
  with `satelle <kind> attach <id> --name <name> --file <file> --session <session>`
  (draft the file under a temp dir, or pass `--file -` for stdin); temp-dir
  writes and the attach command still work. To edit the tree, move the item on
  or release the seat (`satelle story release <id> --session <session>`). While
  the configuration is invalid, a seat's state is read leniently from its
  workflow file; a seat whose edit scope cannot be read counts as
  attachments-only, so repair the configuration from a session holding no seat
  at that worktree, or in the operator's own editor.
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

A git command that is not read-only, run after a `cd` to one literal path on
the same line, is judged at every directory the line could be in; `-C` values,
a glued `-C<dir>` included, are followed in order. The gate reads a line only
when every token is a plain literal word (letters, digits, `-_./,:+@^=%`, plain
single quotes, or double quotes without `$`, a backquote or `!`) or one of
`&&`, `||`, `;`, `|`. A segment's command may not be an assignment, or a
builtin or keyword other than `cd` (`echo`, `printf`, `true` and `pwd`
included), and a `cd` takes one literal path. A line holding anything else, a
redirection, a line continuation or `$(…)` included, is unread: under the
fail-closed rule it is refused when it holds the word git, whatever the git
subcommand and whatever the seat. Rewrite it in plain form (`git -C <dir>`, a
commit message with `-m '<text>'` or `-F <file>`, separate commands). The gate
does not read git run by another program (`xargs`, `env`, `bash -c`, a
script), or git options, configuration or environment that run programs: those
are level 0's concern, the operator's identity, credential and sandbox
controls.

## The stop check

A session drives each item it holds a seat on to a terminal state of its
workflow. The session-start context, and each prompt where the harness takes
context, states this rule, in the wording of the repo's `.satelle/session.md`
(see `satelle help configuration --session <session>`). A turn may end with a seated item short of a
terminal state only at a park state, at a state that declares what it waits
on (`waits = "<on what>"`, see `satelle help workflows --session <session>`),
or at the operator's state. Otherwise `satelle hook stopcheck` blocks the
turn's end once, with one line per seated item naming the session's next move
as a command: the item's next transition, a queue fire
(`satelle queue fire <queue> --session <session>`) when the state enqueues
into a queue that can fire, or
the queue's open run to seat or drive. A run another session holds, or a
queue that cannot fire (terminal, no trigger, a base that names no commit),
names nothing. The stop straight after a blocked one is let through, so a
session is never trapped; a marker at `~/.satelle/sessions/<session>.stop`
records the block. The block's reason is the session line, the move lines
and the repo's `.satelle/session.md`; when that file is missing, a line
names it in its place. A dispatched run (its session comes from
`SATELLE_SESSION`) is never blocked, and every failure lets the stop through.

Each harness receives the block in its own envelope: claude, pi (its
extension sends the reason as a user message) and grok read
`{"decision":"block","reason":"<reason>"}`; cursor reads
`{"followup_message":"<reason>"}`; agy reads
`{"decision":"continue","reason":"<reason>"}`.

## grok sessions

Grok reads only a top-level `systemMessage` from a SessionStart hook, so
`satelle hook context --harness grok` answers `{"systemMessage":"<context>"}`
with the whole session context (the `[hook] inject` files, or their list
when they are over grok's limit in `.satelle/harness.toml`). Grok discards an allowing prompt hook's
output, so `satelle hook prompt --harness grok` prints nothing; the hook
still settles the session's owner. The stop check blocks with
`decision: block`, which grok feeds back to the model as a user message; a
Stop grok fires at session end (`reason` `channel_closed` or `shutdown`) is
let through and leaves the marker as it was.

## agy sessions

`satelle agents install agy --session <session>` writes satelle's entries under the top-level
`"satelle"` key of the repo's `.agents/hooks.json`, keeping every other key's
hooks; `satelle agents remove agy --session <session>` strips only satelle's,
and deletes the file when nothing else is left. agy (Antigravity CLI) runs:

- `PreToolUse` (matcher `write_file|edit_file|write_to_file|replace_file_content|multi_replace_file_content|sed_file|notebook_edit|run_command`)
  → `satelle hook gate --harness agy`. A refusal answers
  `{"decision":"deny","reason":"<reason>"}`; an allowed call prints nothing,
  so agy's own permission checks still run. agy runs a hook in the repo's
  `.agents/` directory and sends the repo's path in the payload, which the
  gate reads.
- `SessionStart` → `satelle hook context --harness agy`, answered as
  `{"injectSteps":[{"systemMessage":{"systemMessage":"<context>"}}]}`, with a
  doctor notice as a second step (the `[hook] inject` files, or their list
  when they are over agy's limit in `.satelle/harness.toml`).
- `PreInvocation` → `satelle hook prompt --harness agy`. agy fires it before
  every model call; the reminder is injected only at a user turn's start
  (`invocationNum` 0), and the calls within a turn answer `{}`. Every call
  settles the session's owner.
- `Stop` → `satelle hook stopcheck --harness agy`. Only a turn's normal end
  (`terminationReason` `NO_TOOL_CALL` with no continuation active) is
  blocked, once, with `{"decision":"continue","reason":"<reason>"}`; any
  other stop answers `{}`.

## cursor-agent sessions

`satelle agents install cursor --session <session>` writes satelle's entries into the repo's
`.cursor/hooks.json` (version 1), keeping every other entry; `satelle agents
remove cursor --session <session>` strips only satelle's. `cursor-agent` runs:

- `preToolUse` (matcher `Write|Delete|Shell`) → `satelle hook gate --harness
  cursor`. A refusal answers `permission: deny` with the same reason the claude
  gate gives, before the file is written or the command runs. The session's
  repo comes from `CLAUDE_PROJECT_DIR`, else `CURSOR_PROJECT_DIR` (Cursor sets
  both), else the payload's project root; the fence applies as above.
- `sessionStart` → `satelle hook context`: the session id (Cursor's
  `session_id`, the same as its `conversation_id`) after any framing
  principle's description, its seats, the repo's
  `.satelle/session.md`, the doctor block and the `[hook] inject` files (or
  their list by description and path, when printing them in full is over
  cursor's limit in `.satelle/harness.toml`), as `additional_context`.
- `beforeSubmitPrompt` → `satelle hook prompt`. It settles the session's owner
  and answers `{"continue":true}`. Cursor's prompt event has no context
  channel, so a cursor-agent session gets no per-prompt seat reminder; the
  seats are in its session-start context and in `satelle status --session <session>`.
- `stop` → `satelle hook stopcheck`. A block (see The stop check, above)
  answers `{"followup_message":"<reason>"}`, which Cursor submits as the next
  prompt; otherwise it answers `{}`, so Cursor ends the turn.

`cursor-agent -p` (print mode) fires sessionStart and preToolUse but not
beforeSubmitPrompt or stop.

Cursor also runs a repo's `.claude/settings.json` hooks. Once Cursor's own are
installed, a claude entry called by Cursor (its payload carries
`cursor_version`) answers nothing, so no edit is judged and no context injected
twice. The environment does not count: a claude session started from Cursor's
terminal keeps its gate.

## Cursor IDE sessions

Validated on the Cursor IDE 3.28.7 (its payloads carry `cursor_version`
3.28.7), in the agent pane's default agent mode, by
`scripts/cursor-ide-probe.sh` (sto_bf491f0d). The IDE's agent runs the same
`.cursor/hooks.json` entries as cursor-agent, above: sessionStart,
beforeSubmitPrompt, preToolUse and stop all fire, each with one id
(`session_id` equals `conversation_id`), which is the session satelle uses.
The IDE's payloads carry fields cursor-agent's do not (`transcript_path`,
`user_email`, and on sessionStart, beforeSubmitPrompt and stop `model_id`,
`model_params` and `composer_mode`), and its sessionStart `generation_id` is
empty where cursor-agent's equals the session id; satelle reads `model_id` as
a fallback for `model` and ignores the rest. Its file writes arrive as
tool `Write` and its shell commands as tool `Shell` on preToolUse, so the gate
judges both: an edit or a shell redirect with no seat is refused with the
gate's reason, and the same edit passes once the session is seated. Cursor's
prompt event has no context channel, so the seats are in the session-start
context and in `satelle status --session <session>`, as for cursor-agent.

Where an IDE session differs:

- The IDE sets both `CLAUDE_PROJECT_DIR` and `CURSOR_PROJECT_DIR` to the open
  folder, which is the repo the gate judges. Only a single-folder window was
  validated; a window holding several folders was not. An IDE started from a shell that
  exports `SATELLE_SESSION` or `SATELLE_PROJECT_DIR` is judged by those, so
  start it without them.
- The agent's shell runs in Cursor's sandbox by default, which cannot write
  satelle's runtime under `~/.satelle`: a `satelle story set` there fails with
  `attempt to write a readonly database`. Run satelle commands outside the
  sandbox (the agent offers to rerun them), or allow them in Cursor's settings.
- The IDE's hook processes exit after each event, so `satelle doctor --session <session>` reports
  the session's seat as unverified rather than engaged; release it with
  `satelle story release <id> --session <session>` when the chat is done.
- A user-level `~/.cursor/hooks.json` entry that answers `allow` on preToolUse
  or beforeShellExecution does not override satelle's deny: the refused write
  and the refused shell redirect were not made.
- Cloud and background agents (`is_background_agent`) were not validated.

A repo wired only for claude (`satelle agents install claude --session <session>`, no
`.cursor/hooks.json`) is gated in the Cursor IDE too: the IDE runs its
`.claude/settings.json` hooks (SessionStart, UserPromptSubmit, PreToolUse,
Stop) and honours their answers, so the agent gets the session-start context
and an unseated edit or shell redirect is refused. `satelle status --session <session>` still
shows cursor as not installed there; `satelle agents install cursor --session <session>` gives the
IDE its own entries.

A **block** is a deny with its reason: the harness shows it to the agent and
the tool call does not run. A rejected transition (`satelle story set --status`,
exit 2) is the other block: the item stays where it is, and the rejecting gates'
notes say what to change before presenting it again.
