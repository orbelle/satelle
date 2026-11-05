# Configuration

Everything satelle runs lives in `.satelle/`, in git, owned by you. An unknown
key refuses to run; `satelle validate` lists every problem.

- `satelle.toml` — settings:

      [workflows]
      default = "default"
      [[workflows.rule]]          # first match wins
      workflow = "..."
      category = "..."            # and/or tag = "...", kind = "..."

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

- `agents.toml` — the agents catalog (see `satelle help workflows`).
- `workflows/<name>.toml` — one workflow per file.
- `skills/<name>.md` — the skills performers, gates and checks follow.

Runtime state — the ledger, attachment blobs, the sqlite cache and logs — lives
under `~/.satelle/<repo-key>/` (`satelle runtime path`), shared by every
worktree of the repository. `~/.satelle/config.toml` holds machine settings:
satelled's port and address, the workspace registry and the hosted server.
