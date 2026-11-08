# The edit gate and blocks

Harness hooks (`satelle agents install claude|pi|grok`) send every edit and shell
command a session makes to `satelle hook gate`. The gate allows an edit only
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

Always and never:

- `[gate] edit_exempt_paths` (globs relative to the worktree) are always allowed.
- `[gate] locked_paths` are never allowed, whatever the seat.
- Temp directories, and paths in no git working tree, are outside the gate.
- Another repository's working tree is refused (the fence); a linked worktree of
  this repository is judged by the seat at that worktree.
- While the configuration is invalid, only `.satelle/` itself may be edited.
- A seat on an item in an operator state (`performer = { operator = "<type>" }`)
  allows no edits, and the operator's record verb (`satelle <kind> record`) is
  refused for every session. The scan also refuses quoted or heredoc text that
  names the verb, and any shell call that names satelled's `operator-record`
  route (a curl or wget argument, `-c` program text, a heredoc), so reword such
  prose or pass it with `-F <file>`. These
  checks run as the agent's own OS user: they stop an agent session, not a
  deliberate forgery of the runtime ledger.

A shell command is judged by the same rule, applied to every path it would
write (redirects, `tee`, `cp`/`mv`/`rm`/`sed -i` and similar, `git -C`) and to
the working directory of any interpreter running inline program text.

A **block** is a deny with its reason: the harness shows it to the agent and
the tool call does not run. A rejected transition (`satelle story set --status`,
exit 2) is the other block: the item stays where it is, and the rejecting gates'
notes say what to change before presenting it again.
