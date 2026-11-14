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
      inject = []                 # files the session needs once, printed at session start
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

- `session.md` — the session guidance: how to pass `--session`, the edit
  gate and the drive rule. The context hook (at session start), the prompt
  hook and a blocked stop's reason print it whole, read on every call, so an
  edit takes effect with no rebuild. `satelle init --session <session>` writes it and the
  repo edits it; a repo without it gets a line naming the missing file, and
  `satelle doctor --session <session>` reports it until `satelle init --session <session>` adds it.
- `agents.toml` — the agents catalog (see `satelle help workflows --session <session>`).
- `workflows/<name>.toml` — one workflow per file.
- `skills/<name>.md` — the skills performers, gates and checks follow.
- `doctor.toml` — the repo's own checks and open-item lists
  `satelle doctor --session <session>` runs beside its kernel checks, and the
  `[resolve]` fix item; `satelle init --session <session>` writes it (below).
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
names the item; `satelle init --session <session>` writes `doctor.toml` with it set:

    [resolve]
    kind = "story"              # required: the fix item's kind
    tag = "substrate-fix"       # required: the tag that finds and marks it
    category = "<category>"     # optional; routes the item by [[workflows.rule]]

Kind and tag are required for `--resolve` to open the item, and satelle holds
no default for either: with one unset, `--resolve` opens no item and lists
each finding, naming the `[resolve]` key to set. An item's status is its
workflow's to set, so there is no `status`. While the configuration is invalid no item can be recorded:
`--resolve` then lists the findings with `satelle validate --session <session>` first.

## volatile.toml

A volatile literal is text that goes stale on its own: a version string,
"currently", a model id, an item id presented as the current one.
`.satelle/volatile.toml` names the patterns that find them:

    paths   = ["<glob>"]        # extra files to read, of any extension
    exclude = ["<glob>"]        # files never read, whichever list found them
    exempt  = "<marker>"        # a line containing it is not reported; "" = no marker
    dated   = '<regexp>'        # a line it matches is not reported; "" = none exempt for a date

    [[pattern]]
    name  = "<name>"            # unique; named in each finding
    match = '<regexp>'          # Go regular expression (RE2), run on each line

    [[upstream]]
    files = ["<glob>"]          # files another repo or path owns
    owner = "<owner>"           # named in each of their findings
    fix   = "<command>"         # optional: brings the fixed files here

The files read are every `*.md` under `.satelle`, plus every regular file a
`paths` glob matches, minus every file an `exclude` glob matches; a file is
read once however many lists find it, and a symlink is never followed. Globs
are slash-separated and relative to the directory that holds `.satelle`; `**`
stands for any number of whole segments. A glob may not be empty or absolute
or hold an empty, `.` or `..` segment: that, a nameless or duplicate pattern,
or a `match` or `dated` that does not compile is a configuration problem. The shipped
`exclude` leaves out run records (`.satelle/runs`), testdata fixtures and
`.git`; the list is the repo's to change, and nothing else is left out.

A line is not reported when it matches `dated`, which the shipped file sets
to a YYYY-MM-DD date so dated history stays, or holds the `exempt` marker;
with `dated` unset no line is exempt for carrying a date. A satelle older than
the one that reads `dated` refuses the key. Also, and in a Markdown file no line of a fenced code block
(``` or ~~~, the fences too) is read: a script's variables are not prose. Each
other line gives one finding per pattern it
matches: `<file>:<line>: volatile: <pattern> — "<text>" matches <regexp>`.
`satelle validate --session <session>` lists them (the `volatile` list with `--json`) without
failing; `satelle doctor --session <session>` reports each as a `volatile` issue, and `--resolve`
puts them on the fix item. With no `volatile.toml`, or no
`[[pattern]]` in it, nothing is reported: satelle holds no patterns of its
own. `satelle init --session <session>` writes the default patterns when the file is absent, and
the repo edits its copy to change, add or remove them. The shipped file flags
version strings, the present-tense phrases "currently", "now is" and "at the
moment" (not bare "current" or "latest", which are mostly relational: "the
item's current state"), and item ids presented as current, and
holds a commented `model-id` pattern that a repo enables by uncommenting it
and listing the vendors whose models it uses: satelle ships no vendor list. It is its own file for
the reason `doctor.toml` is.

An `[[upstream]]` entry names files another repo or path owns, such as skills
vendored from a lane repo that must match it byte for byte. They are still
read, so a real hit is never dropped: each finding's text ends `owned by
<owner>`, and doctor's fix says to fix the line at the owner and bring the
file here (running `fix` when given), not to edit it in place. An entry with
no `files`, a bad glob or no `owner` is a configuration problem. A satelle that
predates `[[upstream]]` refuses the table, so add one only once every machine
runs a satelle that reads it.

When `satelle init --session <session>` adds `volatile.toml` to a repo it says so on one
line with the number of lines the check then flags, so a repo that gains the
check sees what it turned on.

Runtime state — the ledger, attachment blobs, the sqlite cache and logs — lives
under `~/.satelle/<repo-key>/` (`satelle runtime path --session <session>`), shared by every
worktree of the repository. `~/.satelle/config.toml` holds machine settings:
satelled's port and address, this machine's projects (`satelle help projects --session <session>`) and the hosted server.
