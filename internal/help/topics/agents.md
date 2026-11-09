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

## Adapters

The adapter is chosen by the basename of the command's first word.

| adapter | grant | denies | may judge |
|---|---|---|---|
| claude | --tools / --allowedTools | --disallowedTools, scoped Bash denies included | read-only grants |
| pi | --tools | plain denies removed from the grant; a scoped deny on a shell grant is refused | read-only grants |
| grok | --tools | --deny; a scoped deny on a shell grant is refused | read-only grants |
| cursor-agent | ask mode or force mode (below) | Cursor's own permission rules, in the run's own config | read-only grants |
| session | not dispatched: the interactive session performs | the edit gate | never |

## cursor-agent

Cursor's own headless CLI, validated on cursor-agent 2026.10.22, signed in on
the machine (`cursor-agent login`) and billed to Cursor. A run is
`cursor-agent -p --output-format stream-json --trust --model <model>`; the
skill and the work go on stdin, never in argv, and the run's stream (every tool
call) is in its dispatch log.

- Every run gets its own Cursor config directory (`CURSOR_CONFIG_DIR`, a
  temporary directory removed when the run ends) holding the machine's sign-in
  (`auth.json`, linked) and a `cli-config.json` whose `permissions.deny` is the
  run's rules. Nothing is written to the working tree, runs in the same
  directory never share a config, and the user's own CLI settings are not
  carried. A project's own `.cursor/cli.json` still applies.
- A read-only grant (Read, Grep, Glob) runs `--mode ask` with every shell
  command denied: ask mode alone still runs commands Cursor deems read-only.
  Such a binding can judge.
- Any other grant runs `--force` with its denies.
- A grant without Bash is denied every shell command (`Shell(**)`). With Bash,
  each scoped deny becomes `Shell(**<first word>**)`, which matches any command
  whose text contains that word: the performer denies become
  `Shell(**satelle**)`, so a cursor performer granted Bash cannot run any shell
  command whose text contains satelle, wherever and however it appears. It
  reads its record through its read tools.
- Read, Grep and Glob are granted together, Edit and Write together, and Bash
  only with Edit and Write (Cursor's shell can write files). NotebookEdit and
  scoped Bash grants are refused.
- Effort goes in the model id (e.g. `claude-opus-5-5-high`); a binding with
  `effort` is refused.
- A run that fails names why (cursor-agent missing, not signed in, an error
  result, no answer) and is never a verdict.
