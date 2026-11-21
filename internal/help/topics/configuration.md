# Configuration

Everything satelle runs lives in `.satelle/`, in git, owned by you. Process-control
files and tables (`workflows/*.toml`, `queues/*.toml`, `agents.toml`,
`agents.local.toml`, and `satelle.toml`'s `[workflows]`, `[gate]` and `[review]`
tables, as well as any top-level key or table `satelle.toml` does not read) are
decoded strictly and refuse an unknown key. Lenient files (`doctor.toml`,
`ui.toml`, `init.toml`, `volatile.toml`, `harness.toml`,
`skills/<name>/pack.toml`) and the known advisory
tables of `satelle.toml` (`[hook]`, `[items]`, `[service]`, `[sync]`, `[ledger]`)
load and ignore an unknown key, while `satelle validate --session <session>`
(under `unknown_keys`) and `satelle doctor --session <session>` (under
`unknown-keys`) report each one with its file, table and value; missing required
keys and refused values still fail configuration loading. `satelle validate --session <session>`
lists every problem.

## The substrate check

`satelle validate --session <session>` also checks the substrate against the running binary: every
`satelle` command, subcommand, flag and `satelle help --session <session>` topic named in code in
the files of `skills/`, `principles/`, `tasks/`, `workflows/` and `queues/`
(backtick spans and fenced or indented blocks of `.md` files, backtick spans of
`.toml` files, every line of `.sh` files; not under `testdata/`), and every
`[[name]]` link outside code, which must name `principles/<name>.md`,
`skills/<name>.md` or `skills/<name>/SKILL.md`. A mention whose next word is a placeholder (`<...>`,
`$...`) is not checked. Each miss is one line,
`<file>:<line>: <kind>: <subject> — <why>`, of kind command, flag, topic or
link; with `--json` they are the `substrate` list beside `problems`. Any miss
fails `satelle validate --session <session>` and is a `substrate` issue of `satelle doctor --session <session>`, but
the kernel still opens: only a configuration problem stops it.

A principle or skill keys itself to the commands it governs with a `commands`
line in its frontmatter: a comma-separated list of command paths, written
without the binary's name.

    commands: story create, story seat

The `--help` of a keyed command, and `satelle help --session <session>` for it, end
with each keyed file's name, description and path. When the command succeeds,
one `see <name>: <path>` line per file is written to stderr after its own
output, so stdout (a `--json` document, an attachment's body) is as it was.
A command that fails names no file. An entry keys exactly the command it
names, not its subcommands. Only the top-level `.md` files of `principles/`
and `skills/`, and `skills/<name>/SKILL.md`, are read, and satelle prints what
the frontmatter names: it knows no file by name. An entry that is not a command
of this binary is a substrate finding of kind command, on the file and line of
its `commands` key.

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
      inject = [".satelle/skills/satelle-workflow-standard.md"]   # files the session needs once, printed at session start
      refresh = true              # false: update and the sync pass leave the harness hook files

      [review]                    # optional: a gate's reviewers by the item's tier
      tier_tag = "risk"           # the tag key whose value is the item's tier (risk:low)
      [review.panels.code-ac]     # one table per gate name
      low = ["reviewer-a"]
      high = ["reviewer-a", "reviewer-b"]

      [items]
      prefix = { story = "sty" }  # id prefix per kind (default: its first three letters)

      [service]                   # satelled
      [sync]

      [ledger]                    # optional; see satelle help ledger-store
      store = { command = "...", args = ["..."] }   # or "satelle.dev" / "local"; unset: satelle.dev signed in, else local

  `[hook] refresh = false` opts the repo out of the harness hook refresh that
  `satelle update --session <session>` and the sync pass run (see `satelle help agents --session <session>`).
  `[hook]` is a lenient advisory table, so unknown keys inside it are ignored
  and reported by `satelle validate --session <session>` and `satelle doctor --session <session>`.

  `[review]` picks a gate's reviewers from the item's tier. The tier is the
  value of the item's tag whose key is `tier_tag`, set by whatever the repo
  uses to assess it; satelle only reads it. A gate named under
  `[review.panels]` dispatches the panel listed for the item's tier, with its
  own skill, combine (`all` when it has only an agent), carry and after list,
  and its gate.run records the tier. An item with no tier tag, a tier with no
  panel and a gate not named run the workflow's own judges, so a missing
  assessment never lightens review. An item carrying two tiers is refused at a
  named gate. A carried accept is carried only for the same tier. `satelle
  validate --session <session>` refuses a panel agent missing from `agents.toml`, a gate no
  workflow has, a check gate, and a blank or `:`-holding `tier_tag`; panel
  agents get the same judge checks as a gate's own. `[review]`, `[workflows]`,
  `[gate]` and unknown top-level keys or tables of `satelle.toml` are decoded
  strictly, so a misspelt key or table there fails configuration loading.

- `session.md` — the session guidance: how to pass `--session`, the edit
  gate and the drive rule. The context hook (at session start), the prompt
  hook and a blocked stop's reason print it whole, read on every call, so an
  edit takes effect with no rebuild. A first `satelle init --session <session>` writes it and the
  repo edits it; a repo without it gets a line naming the missing file, and
  `satelle doctor --session <session>` reports it until a substrate story adds it (see init.toml below).
- `auto-mode.md` — the repo's prose auto-mode guidance, one entry per non-empty
  line. `satelle agents auto-mode <harness> --session <session>` formats it into
  the harness's auto-mode block on stdout and names where it belongs on stderr
  without writing any file (see `satelle help agents --session <session>`). A
  first `satelle init --session <session>` writes the starter copy and the repo
  edits it.
- `agents.toml` — the agents catalog (see `satelle help workflows --session <session>`).
- `agents.local.toml` — this machine's overrides of agents.toml, field by field; ignored by git (see `satelle help agents --session <session>`).
- `workflows/<name>.toml` — one workflow per file.
- `skills/<name>.md` or `skills/<name>/SKILL.md` — the skills performers, gates
  and checks follow. A skill may be a single file (`skills/<name>.md`) or a
  self-contained directory in the Agent Skills layout (`skills/<name>/SKILL.md`
  beside the `scripts/` and `assets/` it owns). Defining both `skills/<name>.md`
  and `skills/<name>/SKILL.md` is a configuration problem reported by
  `satelle validate --session <session>`. For a skill directory,
  `satelle validate --session <session>` checks that every `scripts/<file>` and
  `assets/<file>` path named in backticks or a fenced shell block exists under
  that skill's directory, and refuses paths under `.satelle/scripts/`, paths
  under another skill's directory (except prose backtick references to another
  skill's `SKILL.md`), and `../` escapes. A skill is named by a workflow (a
  performer skill, a gate skill, trigger or after), a doctor.toml check or a
  `[hook] inject` entry, is shipped by `satelle init --session <session>`, or
  is reached by its `skills/<name>.md` or `skills/<name>/SKILL.md` path in the
  body of a skill so named. `satelle validate --session <session>` lists every
  other skill under `skill_warnings` without failing the check. A `README.md`
  in `skills/` and subdirectories without `SKILL.md` (including `testdata/`)
  are not skills.
- `skills/<name>/pack.toml` — optional: defines a skill pack in its entry
  skill's directory (`skills/<name>/SKILL.md` is the wiring guide) (below).
- `packs.toml` — written by `satelle pack install`, `satelle pack update` and
  `satelle pack remove`:
  each installed pack's ref, version, digest, skills and each file's sha256 as
  published (below, under pack.toml).
- `pack-merge/` — written by `satelle pack update`: the installed-version and
  new-version copies of each locally edited pack file, for the agent to merge
  and then remove (below, under pack.toml).
- `skills/satelle-pack.md` — written by `satelle init --session <session>`:
  the agent's instructions for installing a pack with `satelle pack install`,
  reading its entry skill's guide, wiring its gates into the repo's own
  workflow with the user, and saying when the landing route has no security
  reviewer.
- `skills/satelle-workflow-standard.md` — written by `satelle init --session <session>`:
  the agent's instructions for creating, changing or checking a workflow
  against the repo's workflow standard. The repo edits its `Standard:` line to
  name where the standard is. The template `satelle.toml` injects it at
  session start; an existing repo adds the path to `[hook] inject`.
- `principles/satelle-reference-not-restate.md` — written by
  `satelle init --session <session>`: the principle that a page names the
  command, file or link answering a changing fact rather than writing the
  fact down. The template `satelle.toml` injects it at session start; an
  existing repo adds the path to `[hook] inject`. Its frontmatter has
  `framing: true`, so its description opens the session-start context: the
  description of each `[hook] inject` file with `type: principle` and
  `framing: true` is printed first, in inject order, read from the file. A
  repo with its own copy of the principle adds `framing: true` to get the
  line.
- `doctor.toml` — the repo's own checks and open-item lists
  `satelle doctor --session <session>` runs beside its kernel checks, and the
  `[resolve]` fix item; `satelle init --session <session>` writes it (below).
- `init.toml` — what init and doctor report about the files this release
  ships that the repo lacks (below).
- `volatile.toml` — optional: the volatile-literal patterns `satelle validate --session <session>`
  and `satelle doctor --session <session>` apply to the substrate (below).
- `ui.toml` — optional: the colours satelled gives tag chips (below).
- `harness.toml` — optional: each harness's session-start limit, over which
  `satelle hook context` lists the `[hook] inject` files instead of printing
  them (below).

## pack.toml

A pack is a set of skill directories under `.satelle/skills/` defined by
`.satelle/skills/<entry>/pack.toml` in its entry skill's directory (whose
`SKILL.md` is the wiring guide). A pack ships skills only, never workflows.

    name = "<pack-name>"            # required, unique across packs
    version = "<version>"           # required
    skills = ["<member-skill>"]     # required; each must exist as skills/<member>/SKILL.md

    [needs]                         # optional; declares what the pack needs
    scripts = ["<script>"]          # optional
    agents = ["<agent>"]            # optional
    network = ["<host>"]            # optional
    secrets = ["<secret>"]          # optional

`satelle validate --session <session>` refuses a `pack.toml` in a directory
without `SKILL.md`, a symlinked or non-regular `pack.toml`, a duplicate pack
`name`, a missing or blank `name`, `version` or `skills`, an invalid,
duplicate or missing member skill (every member must be a skill directory
`skills/<member>/SKILL.md`, not a single-file `skills/<member>.md`), or an
empty entry under `[needs]`. Because `pack.toml` defines no gate or
permission, it is decoded leniently: an unknown key is reported by
`satelle validate --session <session>` (under `unknown_keys`) and
`satelle doctor --session <session>` (under `unknown-keys`) without failing
validation. The kernel never refuses to load or run a skill for its content,
and loading or running edited pack member skills requires no
`.satelle/packs.toml`.

`satelle pack install <ref> --version <v> --session <session>` fetches a pack
version from the marketplace, verifies it as `satelle pack fetch` does, and
places it under `.satelle/skills/`. The published files are paths relative to
`.satelle/skills/`: one entry directory holding `SKILL.md` and `pack.toml`, and
the member skill directories its `skills` lists, as siblings; a file at the
bundle root, a second `pack.toml`, a directory that is neither entry nor
member, or a `pack.toml` validate would refuse is refused. Install needs a seat
at this worktree on a state whose performer may change the tree, whatever
`[gate] edit_exempt_paths` says, and `[gate] locked_paths` apply to every file
it writes. It refuses a `.satelle`, `skills` or `packs.toml` that resolves
outside the worktree, a skill directory (or single-file skill of the same
name) that already exists and is not recorded as that pack's, and the same pack
at another version; the same version and digest again writes nothing. When a
check, the verification or the read fails, nothing is written and
`.satelle/packs.toml` is unchanged. Installed skills are the repo's own to wire
and edit; install never merges or overwrites them. It records each pack in
`.satelle/packs.toml`, sorted by ref and path:

    [[pack]]
    ref = "<owner>/<name>"
    name = "<pack-name>"
    version = "<version>"
    digest = "<sha256 of the pack>"
    skills = ["<entry>", "<member-skill>"]   # the directories placed

      [[pack.file]]
      path = "<entry>/SKILL.md"            # relative to .satelle/skills/
      sha256 = "<sha256 as published>"

`satelle pack update <ref> --version <v> --session <session>` moves an
installed pack to another version, fetched and verified as install does; it
does not compare versions, and the installed version again writes nothing. A
file is edited when its sha256 differs from `.satelle/packs.toml` or it is
missing, and an unrecorded local file at a path the new version publishes is
edited too. Every unedited file is replaced by the new version's, removed when
the new version drops it (with any skill directory that leaves empty), and
each file the new version adds is placed. Each edited file is left exactly as
it is and named with its copies,
`.satelle/pack-merge/<ref>/<installed-version>/<path>` and
`.satelle/pack-merge/<ref>/<new-version>/<path>` (`(none)` for a version that
does not publish it); satelle merges nothing: the agent merges them into the
file and then removes `.satelle/pack-merge/<ref>`. The installed-version
copies are fetched from the marketplace and must match the recorded digest.
`.satelle/packs.toml` then records the new version and its published sha256s.
Update needs the same seat and honours the same `[gate] locked_paths` as
install. It refuses a pack that is not installed, a `packs.toml` entry whose
version, skills or file paths are not safe relative names (or whose files lie
outside its recorded skills), a version that is not a single safe name, a
symlinked skill directory or file, a new member directory that already exists
unrecorded, and merge copies an earlier update left at
`.satelle/pack-merge/<ref>`. When a check, the verification or a write fails,
nothing is written and `.satelle/packs.toml` is unchanged.

`satelle pack remove <ref> [--force] --session <session>` deletes each file
`.satelle/packs.toml` records for an installed pack whose sha256 still matches,
prunes each skill directory that leaves empty, and drops the pack's entry from
`.satelle/packs.toml` (the file stays, empty when no pack is left). It contacts
no marketplace. A recorded file whose sha256 differs is locally edited: remove
refuses, names each edited file and removes nothing, unless `--force`, which
deletes the edited recorded files too. A recorded file already gone is
reported missing and does not block. A file the pack did not record is never
deleted, even with `--force`: it and its directory are kept and named.
`.satelle/pack-merge/` is left alone. Remove needs the same seat and honours
the same `[gate] locked_paths` as install, refuses a `packs.toml` entry that is
not safe as update does, a symlinked skill directory or file, and a pack that
is not installed; when a check or a write fails, nothing is written and
`.satelle/packs.toml` is unchanged. Remove never edits a workflow: a workflow
that still names a removed skill is reported by
`satelle validate --session <session>`.

`satelle pack publish <entry> --session <session>` publishes the pack defined
by `.satelle/skills/<entry>/pack.toml` for its owner on satelle.dev, named by
the owner's id (see `satelle pack publish --help --session <session>`), at
`pack.toml`'s `version`. It sends the entry and member skill directories as
the working tree holds them (untracked files included), with paths relative to
`.satelle/skills/`, each file's sha256 and the digest `satelle pack fetch`
verifies. The entry and every member must be a plain directory name; a
single-skill pack lists only its entry in `skills`. Publish refuses a symlink
or non-regular file anywhere in those directories, and any layout install
would refuse. Only the owner's admins may publish, and the same version with
different files is refused; in both cases nothing is published. Publish writes nothing locally.

## doctor.toml

`satelle doctor --session <session>` reports what is wrong here, each issue with the command that
fixes it, and changes nothing; the session-start hook runs the same checks
within 2 s. Besides its kernel checks (config, unknown-keys, substrate, volatile, init, harness,
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

A check skill runs in the working tree with no item: exit 0 raises no issue
and prints each non-empty line of its output as a note naming the check
(printing nothing adds no note), exit 1 is one issue per line it prints with
the check's fix, any other exit (or a timeout) is one "could not check" issue.
A doctor check must change nothing. An `[[items]]` entry reports each item it
selects that is not in a terminal state of its workflow.
`doctor.toml` is its own file, not a `satelle.toml` table, so an older satelle
that does not know it ignores it.

`satelle doctor --resolve --session <session>` fixes what doctor can prove (the service, seats
whose session has ended on this machine or whose item is in a terminal state)
and asks about other orphaned seats.
A file this release ships that the repo lacks it lists with the command that
raises the story for it, the user's decision, and opens no item for it.
The config, substrate, volatile and `[[check]]` findings it puts on one item, for the agent to
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

## harness.toml

`.satelle/harness.toml` is optional. It holds one table per harness
(`claude`, `pi`, `grok`, `cursor`) with that harness's session-start limit:

    [claude]
    context_limit_bytes = <bytes>

The limit is the size of hook output the harness shows its model inline;
satelle holds no default for it. `satelle hook context` measures what it
would write to stdout for the calling harness with every `[hook] inject`
file printed in full, the harness's envelope included. When that is over the
harness's limit, it prints the session id, the seats, the session guidance
and the doctor block as before, then one line per inject file, its
frontmatter `description` (else its first line) and its path, under a line
saying to read them in full before acting. No file's body is printed, and
nothing is ever cut: the list is printed whole even when it is itself over
the limit. The framing lines come first in both forms and count toward the
measured size. A harness with no table is printed in full, as is a repo with no
`harness.toml`. `satelle validate --session <session>` rejects a table that
is not a harness and a limit that is not a positive number.
`satelle doctor --session <session>` reports a harness in use whose context as
the index is over its limit, naming the limit and the index size; notes a
harness in use whose context with every file inline is over its limit and
whose index fits, naming the limit and the inline size; and notes a harness
in use with no table as unbudgeted, with its size. It is its own file for the reason `doctor.toml` is.

## init.toml

`.satelle/init.toml` is the repo's own file for the files this release ships.
`satelle init --session <session>` in a repo that already has a `satelle.toml` writes
nothing under `.satelle/`: it lists each shipped file the repo lacks, and
`satelle doctor --session <session>` reports each one. Adding them changes the substrate,
so it is a substrate story, and raising it is the user's decision. That story's
session writes each file itself, from `satelle init --print <path> --session <session>`,
which prints one shipped file exactly.

    category = "substrate"      # the printed story command's category
    removed = ["skills/satelle-workflow-standard.md"]

`category` is the category the printed story command carries
(`satelle story create --category <category> ... --session <session>`); a new
repo's file names one. A repo that names none gets no command:
doctor reports the key and the categories its workflow rules select, and
naming it is itself a substrate story. `removed` records shipped files the repo
removed on purpose (paths relative to `.satelle/`); init and doctor then do not
report them. An entry that is not a file this release ships, or that names
`satelle.toml` or `init.toml`, is a configuration problem. It is its own file,
not a `satelle.toml` table, so an older satelle still opens the repo.

## ui.toml

`.satelle/ui.toml` is optional. Its `[tag_colours]` table maps a tag, or a
tag key, to one of the palette colours `fail`, `warn`, `ok`, `accent`,
`muted` and `note`:

    [tag_colours]
    "priority:high" = "warn"
    surface = "note"
    "team:core" = "accent"

The first entry colours one tag, the second every tag with the key
`surface`, the third a tag satelle gives no colour of its own. An entry for
a whole tag wins over one for its key. An entry overrides the
default colour satelle gives a conventional tag (`satelle help satelle --session <session>`)
or colours any other tag; a workflow chip takes a colour from its name until
an entry names it. satelled reads the file from the repo's next full push.
`satelle validate --session <session>` rejects an empty key and any colour
outside the palette. With no `ui.toml` the conventional tags keep their
defaults. It is its own file for the reason `doctor.toml` is.

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

When a first `satelle init --session <session>` writes `volatile.toml` it says so on one
line with the number of lines the check then flags, so a repo that gains the
check sees what it turned on.

Runtime state — the ledger, attachment blobs, the sqlite cache and logs — lives
under `~/.satelle/<repo-key>/` (`satelle runtime path --session <session>`), shared by every
worktree of the repository. `~/.satelle/config.toml` holds machine settings:
satelled's port and address, this machine's projects (`satelle help projects --session <session>`) and the hosted server.
