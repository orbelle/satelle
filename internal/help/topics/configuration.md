# Configuration

Everything satelle runs lives in `.satelle/`, in git, owned by you. An unknown
key refuses to run; `satelle validate --session <session>` lists every problem.

## The substrate check

`satelle validate --session <session>` also checks the substrate against the running binary: every
`satelle` command, subcommand, flag and `satelle help --session <session>` topic named in code in
the files of `skills/`, `principles/`, `tasks/`, `workflows/` and `queues/`
(backtick spans and fenced or indented blocks of `.md` files, backtick spans of
`.toml` files, every line of `.sh` files; not under `testdata/`), and every
`[[name]]` link outside code, which must name `principles/<name>.md` or
`skills/<name>.md`. A mention whose next word is a placeholder (`<...>`,
`$...`) is not checked. Each miss is one line,
`<file>:<line>: <kind>: <subject> — <why>`, of kind command, flag, topic or
link; with `--json` they are the `substrate` list beside `problems`. Any miss
fails `satelle validate --session <session>` and is a `substrate` issue of `satelle doctor --session <session>`, but
the kernel still opens: only a configuration problem stops it.

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
      refresh = true              # false: update and the sync pass leave the harness hook files

      [rules]
      reviewer_differs_from_performer = false

      [items]
      prefix = { story = "sty" }  # id prefix per kind (default: its first three letters)

      [service]                   # satelled
      [sync]

      [ledger]                    # optional; see satelle help ledger-store
      store = { command = "...", args = ["..."] }   # or "satelle.dev" / "local"; unset: satelle.dev signed in, else local

  `[hook] refresh = false` opts the repo out of the harness hook refresh that
  `satelle update --session <session>` and the sync pass run (see `satelle help agents --session <session>`).
  `satelle.toml` is decoded strictly, so a repo that sets `refresh` needs
  satelle at this version or later on every machine that opens it.

- `agents.toml` — the agents catalog (see `satelle help workflows --session <session>`).
- `workflows/<name>.toml` — one workflow per file.
- `skills/<name>.md` — the skills performers, gates and checks follow.
- `doctor.toml` — optional: the repo's own checks and open-item lists
  `satelle doctor --session <session>` runs beside its kernel checks (below).
- `volatile.toml` — optional: the volatile-literal patterns `satelle validate --session <session>`
  and `satelle doctor --session <session>` apply to the substrate (below).

## doctor.toml

`satelle doctor --session <session>` reports what is wrong here, each issue with the command that
fixes it, and changes nothing; the session-start hook runs the same checks
within 2 s. Besides its kernel checks (config, substrate, volatile, init, harness,
versions, sync, seats) it
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

`satelle doctor --resolve --session <session>` fixes what doctor can prove (the service, seats
whose session has ended on this machine or whose item is in a terminal state)
and asks about other orphaned seats.
The config, substrate, volatile, init and `[[check]]` findings it puts on one item, for the agent to
fix in one change through that item's workflow; it never edits or commits the
files itself, and opens no second item while that one is open. `[resolve]`
names the item:

    [resolve]
    kind = "story"              # default story
    tag = "substrate-fix"       # default substrate-fix
    category = "<category>"     # default none; routes the item by [[workflows.rule]]

Every key is optional; an item's status is its workflow's to set, so there is
no `status`. While the configuration is invalid no item can be recorded:
`--resolve` then lists the findings with `satelle validate --session <session>` first.

## volatile.toml

A volatile literal is text that goes stale on its own: a version string,
"currently", a model id, an item id presented as the current one.
`.satelle/volatile.toml` names the patterns that find them:

    paths   = ["<glob>"]        # extra files to read, of any extension
    exclude = ["<glob>"]        # files never read, whichever list found them
    exempt  = "<marker>"        # a line containing it is not reported; "" = no marker

    [[pattern]]
    name  = "<name>"            # unique; named in each finding
    match = '<regexp>'          # Go regular expression (RE2), run on each line

The files read are every `*.md` under `.satelle`, plus every regular file a
`paths` glob matches, minus every file an `exclude` glob matches; a file is
read once however many lists find it, and a symlink is never followed. Globs
are slash-separated and relative to the directory that holds `.satelle`; `**`
stands for any number of whole segments. A glob may not be empty or absolute
or hold an empty, `.` or `..` segment: that, a nameless or duplicate pattern,
or a `match` that does not compile is a configuration problem. The shipped
`exclude` leaves out run records (`.satelle/runs`), testdata fixtures and
`.git`; the list is the repo's to change, and nothing else is left out.

A line is not reported when it carries a YYYY-MM-DD date (dated history) or
the `exempt` marker. Each other line gives one finding per pattern it
matches: `<file>:<line>: volatile: <pattern> — "<text>" matches <regexp>`.
`satelle validate --session <session>` lists them (the `volatile` list with `--json`) without
failing; `satelle doctor --session <session>` reports each as a `volatile` issue, and `--resolve`
puts them on the substrate-fix item. With no `volatile.toml`, or no
`[[pattern]]` in it, nothing is reported: satelle holds no patterns of its
own. `satelle init --session <session>` writes the default patterns when the file is absent, and
the repo edits its copy to change, add or remove them. The shipped file flags
version strings, present-tense words and item ids presented as current, and
holds a commented `model-id` pattern that a repo enables by uncommenting it
and listing the vendors whose models it uses: satelle ships no vendor list. It is its own file for
the reason `doctor.toml` is.

Runtime state — the ledger, attachment blobs, the sqlite cache and logs — lives
under `~/.satelle/<repo-key>/` (`satelle runtime path --session <session>`), shared by every
worktree of the repository. `~/.satelle/config.toml` holds machine settings:
satelled's port and address, this machine's projects (`satelle help projects --session <session>`) and the hosted server.
