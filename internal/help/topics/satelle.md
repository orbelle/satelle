# What satelle is

satelle is a stateless, generic CLI. You define your process as configuration
under `.satelle/` — workflows, the gates on their transitions, the performers of
their states and the agents that do the work — and satelle runs it. The binary
holds no process of its own: every state, gate, skill and agent name comes from
your configuration.

What satelle keeps:

- **Work items.** Stories, tasks and executions: id, title, body, acceptance
  criteria, status, tags, parent, category and the workflow the item runs under.
- **The ledger.** Every event — an item created or updated, a transition, each
  gate's result, a seat bound or released, an attachment — is appended to a
  hash-chained ledger, one chain per item, under `~/.satelle/<repo-key>/`. The
  sqlite cache is rebuilt from it (`satelle ledger rebuild --session <session>`).

How you drive it:

    satelle init --session <session>                         # .satelle/ with a two-state workflow
    satelle story create --title "..." --session <session>   # starts in the workflow's start state
    satelle story set <id> --status <s> --session <session>  # presents a transition; gates judge it
    satelle story route <id> --session <session>             # where it is, and every gate result
    satelle story seat <id> --session <session>              # bind this session here, for the edit gate
    satelle story seat <id> --base <ref> --session <session> # judge the diff from <ref> (a stacked branch)
    satelle -C <worktree> story set <id> --status <s> --session <session>  # the same, run in another worktree

`<session>` is the id the session-start context line prints. At a terminal
with no agent session, leave `--session` off.

`satelle story set --status` exits 0 when the item moved, and 2 when a gate
rejected the move; it then lists each rejecting gate and its notes. Any other
failure exits 1.

A task is a repeating workflow over a selection of items: `satelle task run <id> --session <session>`
creates an execution recording which items it took, and prints the base its
diff is judged from (HEAD, or `satelle task run <id> --base <ref> --session <session>` for a run
worked on another branch, such as a release on main). satelle runs a task when
asked; repeating it is up to cron, a remote or a routine. An execution is
worked like any item in its workflow: `satelle execution set|attach|seat|route|perform <id>`.

Topics: `satelle help workflows --session <session>`, `satelle help edit-gate --session <session>`,
`satelle help configuration --session <session>`, `satelle help remotes --session <session>`, `satelle help projects --session <session>`,
`satelle help upgrade --session <session>`.
