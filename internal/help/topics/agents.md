# Agents

`.satelle/agents.toml` is a free catalog of bindings:

    [agents.reviewer]
    command = "claude"           # claude | pi | grok | cursor-agent | session, or a command line
    model = "..."
    tools = ["Read", "Grep", "Glob"]

Permissions are tool grants, declared up front and enforced by the adapter. A
binding that judges a gate must have a read-only grant on an adapter that
enforces grants, and its grant must be one the adapter can express, or
`satelle validate` refuses the configuration. With
`[rules] reviewer_differs_from_performer = true`, a judge may not run the model
of the performer whose work it judges.

Every dispatched performer is denied `satelle story set`, `satelle task set`
and `satelle execution set`: a performer never moves an item itself.

## Harness hooks

`satelle agents install claude|pi|grok|cursor` writes the harness's hook file
so its interactive sessions run under the edit gate, the session-start
context, the prompt hook and the stop check: `.claude/settings.json`,
`.pi/extensions/satelle.ts`, `.grok/hooks/satelle.json` and
`.cursor/hooks.json`. A `cursor-agent` session drives stories this way (see
`satelle help edit-gate`). It writes no `.cursor/cli.json`.

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
- Bash and scoped Bash grants are refused, at `satelle validate`. Cursor's
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
