# Configuration

Everything satelle runs lives in `.satelle/`, in git, owned by you. An unknown
key refuses to run; `satelle validate` lists every problem.

- `satelle.toml` — settings:

      [workflows]
      default = "default"
      [[workflows.rule]]          # first match wins
      workflow = "..."
      category = "..."            # and/or tag = "...", kind = "..." (e.g. kind = "queue")

      [gate]
      edit_exempt_paths = []      # globs always editable
      locked_paths = []           # globs never editable

      [hook]
      inject = []                 # files the context hook prints at session start

      [rules]
      reviewer_differs_from_performer = false

      [items]
      prefix = { story = "sty" }  # id prefix per kind (default: its first three letters)

      [service]                   # satelled
      [sync]

      [ledger]                    # optional; see satelle help ledger-store
      store = { command = "...", args = ["..."] }   # or "satelle.dev" / "local"; unset: satelle.dev signed in, else local

- `agents.toml` — the agents catalog (see `satelle help workflows`).
- `workflows/<name>.toml` — one workflow per file.
- `skills/<name>.md` — the skills performers, gates and checks follow.
- `doctor.toml` — optional: the repo's own checks and open-item lists
  `satelle doctor` runs beside its kernel checks (below).

## doctor.toml

`satelle doctor` reports what is wrong here, each issue with the command that
fixes it, and changes nothing; the session-start hook runs the same checks
within 2 s. Besides its kernel checks (config, init, versions, sync, seats) it
runs what `.satelle/doctor.toml` names:

    [[check]]
    skill = "<name>"            # a type: check skill under skills/
    fix = "<command or advice>" # shown with each issue the check raises

    [[items]]
    name = "<name>"             # unique; reported as open:<name>
    kind = "story"              # and/or tag, category, status (an item selection; one of kind, tag, category is required)
    tag = "<tag>"
    fix = "satelle story get {id}"  # {id} is replaced with the item's id

A check skill runs in the working tree with no item: exit 0 is clean, exit 1
is one issue per line it prints, any other exit (or a timeout) is one "could
not check" issue. A doctor check must change nothing. An `[[items]]` entry
reports each item it selects that is not in a terminal state of its workflow.
`doctor.toml` is its own file, not a `satelle.toml` table, so an older satelle
that does not know it ignores it.

Runtime state — the ledger, attachment blobs, the sqlite cache and logs — lives
under `~/.satelle/<repo-key>/` (`satelle runtime path`), shared by every
worktree of the repository. `~/.satelle/config.toml` holds machine settings:
satelled's port and address, this machine's projects (`satelle help projects`) and the hosted server.
