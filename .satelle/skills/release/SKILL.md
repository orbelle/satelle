---
name: release
type: skill
description: CI/CD — the procedure a run of the release task (tas_42301d36), fired from the release queue, follows over the approved items it took: build one release-local candidate from their tested pull-request heads in queue order (fast-forward a head that contains it, merge one that does not, send back only a head whose merge conflicts; carry, as contained, an item a stopped run already landed, whose merged pull request's head is on origin/main), check the candidate's substrate with a satelle built from it (scripts/drift-check.sh) and fix any miss in the candidate before the suite, so substrate drift cannot ship, then fire the node's part on a fly.io sprite (the command remote release-sprite, .satelle/skills/satelle-release-node/), which runs make test once on that candidate, decides whether it changes the shipped binaries (scripts/release-binary.sh, recorded as release-binary), and when it does bumps the version once on top, dispatches release.yml from release/<run> and confirms both releases before it fast-forwards main, then publishes either release still a draft (step 6a), so no release is served before main holds its commit, and read its result back; then back up and install the published build here, check it (when it does not, the node fast-forwards main to the tested candidate with no bump, tag, release.yml or install, and the run checks the published build is installed); a failed node part pushes nothing to main, cancels the run and moves no item; only then move each item to done, and finally clear the finished worktrees, then runs the drift review (step 11). Landing is local; GitHub runs no tests. main only ever fast-forwards to a tree the release tested; never forces or rebases, and merges only in the candidate.
---

# Release (CI/CD)

DEV's work reaches `approved` (operator, 2026-11-06; modelled on
satelle-server's release skill), and the session that drove a story there
fires this run, at any time (`satelle queue fire release --session <session>`,
step 0), and drives it until the story is done: an open, auto-approved pull request whose head contains
`origin/main` and passed the full local suite on exactly that tree (the
integration check). Items approved from the same main no longer fast-forward
one another, so a run lands them together: it builds one release-local
candidate from the run's items in queue order, starting at `origin/main`. A
head that contains the candidate so far is fast-forwarded to; a head that does
not is merged onto it (a merge commit in the candidate only, never a rebase or
a force); a head whose merge conflicts is dropped and sent back alone, naming
the conflicting paths. The run then runs `make test` once on the final
candidate, the combined tree. main only ever fast-forwards to a tree the
release tested: the tested candidate, plus the bump commit, which changes only
the files step 3 commits. The suite, the binary decision, the bump, publishing
and the push to main are the node's part, run on a fly.io sprite
(`### Fire the node's part`); main moves after `release.yml` built the releases
from the run's `release/<exe>` branch, and the last step publishes any of them
`release.yml` left as a draft (step 6a), so no release is served to
`satelle update --session <session>` before main holds its commit: the
operator's ruling *Published only from main* (2026-11-25,
[[satelle-operator-rulings]]) is the order every step below keeps. After the suite passes the node decides whether the
candidate changes the shipped binaries (the repository's scripts/release-binary.sh decide):
when it does not, main fast-forwards to the tested candidate and the run does
not bump, tag, dispatch `release.yml` or install (steps 3, 4, 5 and 7 are
skipped), and the items still reach done on the recorded decision. A sent-back item goes back through the loop (merge
origin/main into the branch with `git fetch origin && git merge origin/main`,
never a rebase, so its pushed head is never rewritten and the push needs no
force; resolve any conflict in the real work, push, and come through
integration again: coding gates, the full suite, a pull request), with the
reason recorded on the item; every send-back reason below says so. A
pushed-back item reopens; it never becomes a new story, and its worktree stays
(step 10 keeps the worktree of an item that is not in a terminal state).

Items never edit a file under `docs/releases/`, beyond
backfilling the notes of versions already released (step 2a): each records
its lines as an attachment named `changelog` (format in
`satelle-pr-ready-check/SKILL.md`, owned by the repository's scripts/changelog-items.sh), and
this run writes the version's notes to `docs/releases/v<version>.md` from the
items it lands (step 3). Every released version has its own notes file there
(`docs/releases/v0.md` holds the 0.0.x and serve-v0.0.x notes), and no release
edits an earlier version's file.

The run's items reach `done` only through it: the `release-run` gate on
`approved → done` refuses any item no in-progress run lists. Every step below
that fails stops the run: record the reason in the run's `release-evidence`,
leave the items as they are and end the turn. Nothing installed is touched
before step 7.

Never: run any suite but the node's one run (step 2d); rebase; force-push; merge on main
itself (merges exist only in the candidate); push to main anything but the
tested candidate and its bump commit (the tested candidate may carry the run's
own drift-fix commits, made before the suite); push a candidate the drift gate
has not passed; bump, tag, dispatch `release.yml` or install on a
binary-unchanged run; take an item not in the run's inputs; or close an item
before step 8 passes.

Shell variables do not survive between the session's separate commands, so
from step 2 on the run keeps its state in one run-local directory,
`rd="${TMPDIR:-/tmp}/satelle-release-$exe"`: the candidate worktree
`$rd/candidate` (`$wt` below), the candidate's binary `bin/satelle` and the
files `kept`, `released`, `sendback`, `ff`, `drift.txt`, `drift-fixed`,
`drift-checked` and `release-drift`, the node's input `node-in/` and what the
node brought back, `node-out/<node>/back/` (`result`, `release-binary`, on
a red suite `failing`, and on any other failed step `step-log`). Each block finds the in-progress run and `rd` again
the same way. The durable record stays on the items (`release-sendback`) and
the run (`release-kept` and `release-drift`, `### The node's input`;
`release-binary` and `release-node`, `### Read the node's result`; and
`release-evidence`).

Each step's script is a file under `.satelle/skills/release/scripts/`, named
by its full path at its `Run` line. At step 0, while the session's tree is
still its own and not detached, read every step script from
`.satelle/skills/release/scripts/`. For each step, fill that step's `<...>`
placeholders in the text read at step 0 and give the filled text to bash
(`bash -s` or a heredoc) with the repository root as the working directory.
Never run a step script by a path in a tree detached at the candidate: the
candidate's tree must not supply a step's text. A `scripts/` path inside a
script is the repository's own, not this skill's.

## 0. Start and read the run

Run `.satelle/skills/release/scripts/start-run.sh`.

The after-review (`satelle-task-validate-after-review`) reads only the run's
record folder, so the run carries its parent task as the attachment `task`
(task-run.md step 2); without it the run is refused at `done`. The task's
body must name the run's `release-binary` record (the binary decision): step 0
refuses to fire against a body that does not, so no run is judged against a
task that still says every run bumps, tags, publishes and installs.

The printed base must be origin/main's head (`git rev-parse origin/main`):
`--base main` would resolve a local `main` branch first, which may be stale.

The release run is fired from the release queue, defined in
`.satelle/queues/release.toml` (filter: approved stories; trigger: the release
task, base `origin/main`, min size 0). The delivery and chore workflows enqueue
into it by name on their `approved` state, so a story reaching approved joins it
with the transition as the cause. Nothing fires on enqueue: this step is the
fire, run by the session whose items are approved, at any time, and the file's
`base` is the run's base, unless this machine's sync pass woke the queue
(`[[wake]]` in `~/.satelle/config.toml`, off unless listed; `satelle help queues --session <session>`):
then this step takes the run the wake started, while it is still at backlog,
cancels and refires it when its base is not the fetched `origin/main`, and
otherwise proceeds the same. It takes or cancels that run only when the run's
own `item.created` and `task.run` show the sync pass wrote them (actor
`satelle-sync.service`, origin role `sync`) under the release task, for this
queue, over members the queue still holds: a `queue.fired` payload is
appendable by any session, those origins are not, and the wake writes them only
from a sync pass that proved the whole ledger in sync with the store. A run it
cannot prove that of exits 2 naming it and the missing proof, untouched, and
nothing is fired. A run a session fired is taken as before.
`satelle queue show release --session <session>`
shows its members and last fire. A fire refused because the last run is still
open exits 2 naming that run: finish or cancel it first.

For each input item find its pull request: the PR against main whose title
ends `(<id>)` (`gh pr list --state open --base main --json number,headRefOid,title`).
Record `<id> PR #<n> head <sha>`. An item with no open pull request goes on the
send-back list, unless it has a merged pull request (title ending `(<id>)`,
`gh pr list --state merged`) whose head is an ancestor of `origin/main`: a run
that did not close it already landed that head. It is carried, kept as
contained (step 2a), and reaches done in this run (step 9); it is never sent
back, which would reopen landed work.

### Reviews on the heads

Then read the satelle GitHub app's review on each item's PR head, once per
input item (`id=` each in turn). The app's latest decision on the head
(APPROVED, CHANGES_REQUESTED or DISMISSED; a comment decides nothing) is
matched by its account's numeric id, read from the approval check, never by
login. When it requested changes, the item reopens here: the review body is
attached as `pr-review`, the item moves approved → in_progress, and it is
dropped from the run and recorded as sent back with "the satelle app requested
changes on head <sha>". The block calls gh and satelle's local commands only,
never satelle.dev, and changes nothing until the app reviews this repo's pull
requests. A carried item (no open pull request, a merged one whose head is on
`origin/main`) has no review to read: the run that landed it read the app's
review on that head, and a review of a landed head sends nothing back.

Run `.satelle/skills/release/scripts/review-heads.sh`.

Record `sha256sum` of `$(command -v satelle)` and of the
satelled binary in the `installed` line of `satelle service status --session <session>`; the same
sums just before step 7 prove nothing installed was touched.

## 1. Order

The run takes its items in queue order: the order of the `task.run` inputs
step 0 printed (explicit members in enqueue order, then filter candidates).
Nothing is sorted. A stacked head whose base comes later in the order still
lands: its base is merged with it, and when the base's turn comes its head is
already in the candidate.

## 2. Build the candidate, test it once

a. Judge each item's changelog, build the candidate, then pass the drift gate
   on it: a substrate miss the candidate's own binary finds is fixed in the
   candidate, in this run, before the suite. The tested candidate may carry
   those drift-fix commits.

### Each kept item's changelog

Once per input item still `approved` after step 0, in queue order (`id=` each
in turn, with its `head` from step 0, a carried item's merged head included,
and `tip=origin/main`). Its verdict is
its output, never its exit status: every item outcome prints exactly one line
and exits 0, so a send-back never stops the run. It exits 1 only when it
cannot judge at all (git, satelle or bash is not on PATH, or git cannot diff
the head, named on stderr):
that is a failed step, which stops the run. The head's own
scripts/changelog-items.sh validates the attachment, so a legacy head
without the script is sent back too. The release-notes test (every file under
`docs/releases/`, and any `CHANGELOG.md`) reads the head's own commits only
(`$tip...$head`, from their merge base), so a head cut from an older main is
not mistaken for one that edits them. Backfilling the notes of released
versions is the one change it keeps: adding `docs/releases/v<ver>.md` (or
`docs/releases/v0.md`) that `$tip` lacks for a `<ver>` below `$tip`'s
`satelle.version`, a version already released, and deleting a `CHANGELOG.md`
`$tip` still has (the rule is read from `$tip`,
never the head, so a head cannot relax it; a tip without a `.version` sends
back every path); every other change is sent back naming the path.

Run `.satelle/skills/release/scripts/changelog-verdict.sh`.

### Build the candidate

Once, after the changelog verdicts, with every `<id>: send back: <reason>`
line they printed pasted into `held` (nothing when none was). It recreates
`$rd`, adds the candidate worktree detached at `origin/main` and walks the
run's inputs in queue order. It skips an item no longer `approved` (step 0's
review sent it back) or held by its changelog. For the rest, with the
candidate's current HEAD as the tip:

- the head is already in the candidate (`git merge-base --is-ancestor <head> <tip>`; a resumed run's head already on main too, and a carried head): kept, contained; a head a `v*` release tag already contains prints `contained, released in v<X>` (the earliest such tag), and step 3 writes no lines for it;
- the tip is an ancestor of the head: kept, the candidate fast-forwards to it;
- else the head is merged onto the tip in the candidate (`--no-ff`, a merge commit; the head itself is never rewritten): kept, merged. The tip before the first merge is recorded in `$rd/ff` (the fast-forward prefix);
- a merge that conflicts is aborted and the item is dropped from the candidate. When its head merges cleanly onto `origin/main` alone (`git merge-tree --write-tree origin/main <head>`), it conflicts only with items kept before it in this run: it stays `approved`, prints `left approved`, and gets a `release-deferred` attachment naming the conflicting paths and those items, so it merges main once they land and the next run takes it. Otherwise it conflicts with main too and goes on the send-back list naming the conflicting paths: "head <sha> conflicts with the release candidate after <kept ids> in: <paths>; merge origin/main into the branch (git fetch origin && git merge origin/main; never rebase, so the push needs no force), resolve, and come through integration again".

An item with no open pull request goes on the send-back list too, unless its
merged pull request's head is an ancestor of `origin/main` (step 0): that head
is carried and lands contained, since the candidate starts at `origin/main`.
It is never sent back for the missing open pull request. Each kept
item is recorded `<id> <head>` in `$rd/kept`, in queue order (the landing
order). Each item prints one line, `<id>: <fast-forward|merged|contained|sent
back|skipped|left out> ...`; the block ends `candidate <sha> at <wt>`, or exits 1 with
"nothing lands" when no item is kept (send back, step 2c, and stop). While a
failed suite is being isolated (`### Isolate a failing suite`), it leaves out
the items the current trial names and does not send them back.

Run `.satelle/skills/release/scripts/build-candidate.sh`.

### Drift gate on the candidate

Once the candidate is built, and again after each drift-fix commit, before the
suite runs. It builds the candidate's own satelle into `$rd/bin/satelle` and
runs the candidate's own scripts/drift-check.sh with it in the candidate
worktree, with `SATELLE_HOME` set to an empty `$rd/drift-home` that it removes
afterwards, so a candidate that migrates the cache or ledger changes only that
home, never this machine's satelle state: the rules are that binary's `satelle validate --session <session>` substrate check, so a
release whose substrate names a command, flag, help topic, skill or link the
candidate binary lacks is refused here, before anything is tested or pushed.
On exit 0 it records the candidate's sha in `$rd/drift-checked` and writes
`$rd/release-drift`, which `### The node's input` attaches to the run;
`### The node's input` and the node's `fetch` and `guard` steps refuse a
candidate whose sha is not in `$rd/drift-checked`.

Run `.satelle/skills/release/scripts/drift-gate.sh`.

The release fixes a miss in the same run, before the suite:

1. Seat the run at the candidate, so the edit gate judges the edits there:
   `satelle execution seat "$exe" --location "local:$wt" --base origin/main --session <session>`.
2. For each `<file>:<line>` the block printed, edit that line in `$wt` so it
   names what the candidate binary has: read it from
   `"$rd/bin/satelle" <command> --help` and `"$rd/bin/satelle" help`.
3. Commit only those files in `$wt` as
   `chore(release): substrate drift fix (<exe>)`, and run this block again
   until it exits 0. Each miss it printed is listed in `release-drift` as
   fixed in this run.
4. A miss that needs a binary change (the substrate is right and the binary
   lost what it names) stops the run: record it in `release-evidence`, push
   nothing, and raise a story for it.

b. The tree that presents the moves below must carry the config being
   released: git diff --quiet HEAD "$tip" -- .satelle/workflows .satelle/skills scripts/changelog-items.sh scripts/release-binary.sh scripts/check-serve-version.sh,
   with `tip` the candidate's HEAD, else stop with "check out the run's tip
   first: git checkout --detach $tip".

c. Send back each item on the list. It reopens, and the coder's next run
   reads the reason from the item's record.

### Send back

Runs over `$rd/sendback` and empties it, so a second pass sends back only
what was added since. An item no longer `approved` is not moved again.

Run `.satelle/skills/release/scripts/send-back.sh`.

   When the installed satelle refuses the tip's workflows (an unknown key, such
   as a gate's `after`, from a satelle older than the build origin/main
   carries), build the tip in a temporary worktree
   (`bt=$(mktemp -d); git worktree add --detach "$bt" "$tip"; go build -C "$bt"
   -o "$bt/satelle" ./cmd/satelle`) and present the send-back with that build
   first on `PATH`.

d. Hand the candidate to the node's part, fire it on a sprite, and read what
   comes back. The suite, the binary decision, the bump, publishing and the
   push to main run on a fly.io sprite, the `release-sprite` remote
   (`.satelle/remotes/release-sprite/`), reached by the command remote through
   `.satelle/skills/satelle-node-sprite/`. The node's part starts here, after
   the drift gate and the send-back, and ends when main has fast-forwarded;
   steps 7 to 11 stay on this machine. Its steps and what each refuses are
   `.satelle/skills/satelle-release-node/SKILL.md`; each is
   its scripts/release-node.sh <step>, run as root on the node, with `make
   test` run once as user `release` from `env -i`.

### The node's input

Once, after the drift gate passed on the candidate and the send-back ran. It
refuses a candidate whose sha is not in `$rd/drift-checked`. It lists the
items earlier binary-unchanged runs carried (`### Carried from
binary-unchanged runs`, step 3) against the drift-checked candidate, which is
the commit the node tests, and writes `$rd/node-in`: `run` (one `key: value`
per line: `run`, `candidate`, `drift-checked`, `branch`, `ff`, `base`, `repo`,
`author`, `email`, `mode`), `kept`, `notes-ids` (step 3's `ids`) and
`changelog/<id>`, each notes id's `changelog` attachment. A missing attachment
stops a binary-changed run at the node's bump; a binary-unchanged run writes no
notes, and changelog-entry refuses that item's done until it has one. `repo` is origin's URL with `git@github.com:` written as
`https://github.com/`; `author` and `email` are git's author identity. Every file of the input, `$rd/kept` and
`$rd/release-drift` is scanned for a GitHub token's shape first. Then it
attaches `release-kept` and `release-drift` to the run (step 6) and pushes the
candidate to `release/<exe>`, removing a stale one from an earlier attempt of
this run first (`git push --delete`, never a force).

Run `.satelle/skills/release/scripts/node-input.sh`.

### Fire the node's part

Once, from the session's tree detached at the drift-checked candidate (`git
checkout --detach <candidate>`): the fire bundles that tree's tracked
`.satelle` and reads `release-sprite` from it, so a release ships with the node
part it carries. Run it attached (a secret-bearing fire refuses `--detach`), as
a background command whose timeout is above the release-node gate's timeout,
with margin for setup, the suite and `release.yml`; read that timeout with
`satelle workflow show release-node --session <session>`. The fire reads
`GH_TOKEN` from the firing shell or the repo's untracked `.satelle/.env` and
gives it to the node on the exec command's stdin only; this block never reads
either. The task is the remote's `[drive] task`.

Run `.satelle/skills/release/scripts/fire-node.sh`.

### Read the node's result

Once, after the fire returned, whatever it printed and whatever its exit
status (a rejected node part exits non-zero). It reads `satelle remote
status release-sprite <task> --json --session <session>`, then the repository's scripts/release-node-verify.sh, which
reads only `$rd/node-out/<node>/back`. On accept, verify checks the result is
this run's, that it tested the candidate, that origin/main is `main-after`
and differs from the candidate only in `.version` and the notes (nothing on a
`binary: unchanged` run), that `release/<exe>` is gone, that the node
execution's chain has an event by `node:<node>`, that the sprite is no longer
listed, and that the node ran the suite as `release` and its secret scan was
clean. On a reject or a stop it checks origin/main is still the run's base.
Every file it attaches is scanned for a GitHub token's shape first, and a hit
refuses the attach.

- Accepted: it attaches `release-binary` (the node's decision, which the
  version-stamp, changelog-entry, ci-published and release-installed gates
  read) and `release-node` (the result, the status line and the sprite check),
  then removes the candidate worktree and `$rd`. Step 7 follows.
- Rejected at `test`, the suite, with at least one suspect: the run is not
  cancelled. It exits 3, printing `isolate:` and the items the next trial
  leaves out; follow `### Isolate a failing suite`.
- Rejected or stopped otherwise: it attaches `release-evidence` (the failed step, the
  status line, the failing tests the node named, the failed step's last log
  lines the node brought back as `step-log`, and every item that entered
  the candidate by merge as a suspect, with the commands that send it back
  after this run), deletes `release/<exe>`, cancels the run and moves no item.
  The items stay `approved`; the next fire takes them.
- Stopped at `release` (step 6a): main already holds the run's commit and
  the result names each `to-publish <tag>`. Cancel the run and fire again;
  the next run carries the landed items as contained (step 2a) and publishes
  the drafts.
- Accepted but the records did not come back: the node already pushed main.
  It stops; cancel the run and fire again, and the next run carries the landed
  items as contained (step 2a).

Run `.satelle/skills/release/scripts/read-node-result.sh`.

An item in the fast-forward prefix is never a suspect: its head is exactly the
tree that passed its own integration. When the run is cancelled, a suspect is
sent back only on the session's reading of the failure (a red suite names its
tests in `failing`), by the line `release-evidence` gives it.

### Isolate a failing suite

When `### Read the node's result` exits 3 (`isolate:`), the suite failed on
a candidate with merged suspects, and the run looks for the story that breaks
it instead of cancelling. The suspects are the kept items that entered the
candidate by merge, newest first. Each trial builds a candidate that leaves
some of them out:

1. one suspect at a time, each alone, newest first;
2. then cumulatively, the newest two, the newest three, and so on, down to the
   fast-forward prefix alone.

For each trial, run again, in order, with the same `held` lines as the first
build: `### Build the candidate` (it prints `left out` for this trial's
items), `### Send back`, `### Drift gate on the candidate`,
`### The node's input`, `### Fire the node's part` and
`### Read the node's result`. The trial's node part is a real one: a
candidate whose suite passes goes on to bump, publish and push main, so the
first passing trial is the release of every story it kept.

- A trial that passes: the read sends back only the items that trial left
  out. Their `release-sendback` names the failing tests of the first
  candidate and says that leaving them out made the suite pass. It attaches
  `release-isolation`: the first failing tests and each trial's outcome.
  Step 7 follows.
- A trial whose suite fails again exits 3 with the next trial.
- A trial that fails at another step, or the last trial failing: the read
  cancels the run as today. `release-evidence` then carries each trial's
  outcome, and every item stays `approved`.

The trials' state is `${TMPDIR:-/tmp}/satelle-release-<exe>.isolate`, beside
`$rd`. Rebuilding the candidate keeps it, and an accepted or cancelled run
removes it.

## 3. Bump once

Binary changed only, on the node (`release-node.sh bump`): a `binary:
unchanged` run writes no release notes and bumps nothing. Its items keep their
lines on their `changelog` attachments, and the next binary-changed run writes
them into its notes, first.

In the node's clone of the candidate, on top of the tested candidate.

For `satelle.version` against the `v*` tags and `satelled.version` against the
`serve-v*` tags (prefix stripped, `sort -V`): keep the current value when it
is at or above every tag other than its own and its own tag is absent or points
at the candidate (a resumed run already published it); otherwise set it to the
patch + 1 of the latest tag of all, its own included; with no tag, the current
value's patch + 1. Set
`satelle.build=$(date -u +%Y-%m-%dT%H:%M:%SZ)` when either changed. The rule's
one implementation is `release-node.sh bump`.

### Carried from binary-unchanged runs

The items earlier binary-unchanged runs landed since the published version
have no release notes yet. The candidate's scripts/release-binary.sh carried lists
them, oldest first: it walks this task's finished runs back to the last
binary-changed one, and keeps each landed item whose head is under the
candidate and not under the published `v*` tag (read from the tags merged into
the candidate). It reads this ledger, so `### The node's input` runs it here,
against the drift-checked candidate, and a non-zero exit stops the run. The
ids it prints go first in `ids` below.

### Changelog from the items

The node writes the version's notes to `docs/releases/v<version>.md` from the
`changelog` attachments `### The node's input` carried (`changelog/<id>`), in
the order of `notes-ids`: the carried items first, oldest first, then the kept
items in landing order (`$rd/kept`, queue order; an id already carried is not
listed again), except each whose build line (step 2a) says `contained,
released in v<X>` (`$rd/released`): that release already wrote its lines, and
changelog-entry accepts its done from that release's notes. When no item is
left, no notes file is written. The file holds one section, `## [<ver>] -
<date>` (UTC date) with each item's lines under their `### <Section>` in fixed
order (Added, Changed, Deprecated, Removed, Fixed, Security, Breaking), each
line ending with its item id, and ends in exactly one newline; nothing is
staged above it. The repository's scripts/changelog-items.sh write renders the section before
it creates the file, so a malformed attachment writes nothing, and it refuses
a file that exists. The lines are copied verbatim; the run never edits an
item's content. A resumed run whose `docs/releases/v<ver>.md` exists changes
nothing.

Commit only `.version` and `docs/releases/v<ver>.md` (`git add` the new
file), so the tested tree is otherwise unchanged: `chore(release): v<ver>, satelled <sver> (<exe>: <ids>)`. Skip the
commit when nothing changed (a resumed run). Step 6's guard excludes exactly
these two files; a file added to the bump is added to both lists.

## 4. Dispatch

Binary changed only, on the node (`release-node.sh publish`), before main
moves: it pushes the bump to `release/<exe>`, runs `gh workflow run
release.yml --ref release/<exe>` and watches that run to its conclusion. The
release's tags come from the dispatched ref's `.version`, at the bump, which
the push then makes main's head, so ci-published finds the run at main.

## 5. Confirm both releases

Binary changed only, on the node (`release-node.sh publish`): `gh release
view` of `v<ver>` and `serve-v<sver>` shows each with its assets, and `v<ver>`
is at the bump. The result carries the `release.yml` run and both tags.

## 6. Push main once

On the node (`release-node.sh guard`, then `push` after steps 4 and 5). The
guard stops with "the candidate changed after the suite ran" when the suite
has not passed on this candidate; with "the run has not decided on the tested
candidate" when the decision is missing or names another candidate; on
`binary: unchanged`, with "a binary-unchanged run pushes the tested candidate
and nothing on top" when HEAD is not exactly the tested commit; on `binary:
changed`, with "the candidate changed after the suite ran" when the tested
commit is not under HEAD or HEAD differs from it outside `.version` and
`docs/releases/v<ver>.md`; and with "drift-check has not passed on the tested
candidate" when the drift-checked sha is not the tested one. The push is
`git push origin HEAD:refs/heads/main`, never forced, after a fetch shows
origin/main is still the run's base; a step that fails before it leaves main
where it was, and after it the node only publishes the drafts (step 6a). It then deletes
`release/<exe>`. `release-kept` and `release-drift` were attached before the
fire (`### The node's input`), so a push refused after them leaves a run that
must be resumed or cancelled, never landed items without a record; the
runbook's `release-kept` gate refuses the run's done while any of them is not
in a terminal state (step 9). `release-binary`, the run's decision, which the
version-stamp, changelog-entry, ci-published and release-installed gates
read, is attached by `### Read the node's result`.

GitHub marks each pull request merged, because main now contains its head.

## 6a. Publish the drafts

On the node (`release-node.sh release`), after step 6 moved main, on every
run, binary-unchanged included: for `v<ver>` and `serve-v<sver>` of HEAD's
`.version`, a release that is still a draft is published (`gh release edit
--draft=false`, `--latest` for `v<ver>` and `--latest=false` for
`serve-v<sver>`, which keeps `releases/latest` on the CLI), retried before it
gives up; a published release is left as it is. A draft is not served by
`releases/latest`, so until this step a version `release.yml` built is not
installable; its tag is already at the bump. The result records `published
<tag>` for each it published. When one stays a draft the step fails with
`to-publish <tag>` for each: main already holds the run's commit, and the next
fire, whose candidate is that commit, keeps the version (step 3's rule, its
own tag points at the candidate) and publishes the draft here.

## 7. Back up, then install here

Binary changed only.

Run `.satelle/skills/release/scripts/backup-install.sh`.

The restore line is printed, with real paths and no placeholder, before
anything is replaced. The backup is never deleted by the run. The second
update runs the binary just installed, so a fix to `satelle update --session <session>` itself
takes effect in the run that ships it; when the pair is already right it only
prints two `is current` lines.

## 8. Check

Binary changed: `satelle version --session <session>` names `<ver>` and main's commit; `satelle service status --session <session>`
shows satelled answering, `running` and `installed` at `<sver>` and main's
commit, and no `warning` line (the release-installed gate's patterns). A
mismatch stops the run and repeats the restore line.

Binary unchanged: `satelle version --session <session>` names `<ver>` at the
commit of `v<ver>`; `satelle service status --session <session>` shows
satelled answering, `running` and `installed` at `<sver>` and the commit of
`serve-v<sver>`, and no `warning` line. A mismatch stops the run, as above.
The run installs nothing on this path: the operator installs the published
`<ver>` outside the run, and the run resumes at step 8.

## 9. Close

For each released item: `satelle story set <id> --status done --session <session>`. Its five gates
(release-run, version-stamp, changelog-entry, ci-published, release-installed)
judge it while the run is still in progress, and the last four read the run's
`release-binary` decision: on `binary: unchanged` they accept main at the
recorded candidate with `.version` as published, the item's lines held on its
`changelog` attachment, the published releases, and the published build
installed. Present every move here with step
7's installed binary (on a binary-unchanged run, the installed published build) first on `PATH`: `command -v satelle` is step 0's path and
`satelle version --session <session>` names `<ver>`. Take any tip build step 2c put first on `PATH`
off it before this step, because release-installed reads `satelle version --session <session>`.

The run's done is refused by its `release-kept` gate until every item `### The node's input`
listed is terminal. When an item's done is refused, fix the cause and present
it again while the run is still in progress. A run that cannot finish may be
cancelled (`* -> cancelled`, no gates); the next fire carries its landed items
as contained (step 2a), and they reach done there. Then attach `release-evidence` to
the run (`satelle execution attach "$exe" --name release-evidence --file - --session <session>`):

- per item: id, PR number, head sha, and how it landed (fast-forward, merged or contained) or sent back with the reason
- the candidate sha and the `make test` result (`suite-exit`, `hermetic`)
- the node, the node execution and the remote verdict (`release-node`), and the line confirming the sprite was destroyed (`sprite <name> is not listed`)
- the `release-binary` record (the decision and the paths that decided it)
- main before and after, and the bump commit
- the release notes written: `docs/releases/v<ver>.md`, its heading and the ids it names
- the release.yml run URL, head sha and conclusion (the result's `release-yml` line)
- both tags with their asset names
- the step-0 and pre-step-7 binary sha256s, the backup dir listing and the restore line
- the `satelle version --session <session>` and `satelle service status --session <session>` lines
- each item's final status

On a binary-unchanged run the evidence is the record, main before and after
(after is the tested candidate), the step 8 lines and each item's final
status: there is no bump commit, release notes, `release.yml` run, tag,
backup or restore line.

Then present the run `in_progress → done` (validate-after) and release its
seat.

## 10. Clear the finished worktrees

Clearing worktrees is part of the workflow (operator, 2026-11-06): from the
main checkout (not a linked worktree, which the cleanup may remove), run the
cleanup task and follow its run to done. Step 6 moved origin/main, not the
main checkout, so the block first fast-forwards the checkout to origin/main
(`git merge --ff-only`, never a merge commit); otherwise the run's
`--base origin/main` is not in HEAD's history and its move to in_progress and
its seat are refused:

Run `.satelle/skills/release/scripts/clear-worktrees-run.sh`.

Then the task's ACTION, whose output is the run's `worktree-evidence`:

Run `.satelle/skills/release/scripts/clear-worktrees-action.sh`.

The cleanup (the repository's scripts/clear-worktrees.sh) removes each linked worktree that
has no uncommitted or untracked change, whose HEAD is on a remote branch, and
whose branch names no item or an item in a terminal state of its own workflow
(so every released item's worktree), deletes its local branch, keeps and
reports any other with its reason, and puts the main checkout on main at
origin/main. It never removes a worktree with work that is not pushed, nor the
worktree of an item still being worked: an item sent back to in_progress keeps
its worktree, reported `kept <path> (<branch>): item <id> is <state>, not
terminal`. The terminal states are read from `satelle workflow show --session <session>`, never
listed here.

## 11. Drift review

Every release ends with a drift review (operator, 2026-11-07): the substrate
checked against the binary just released, and the release's binary change
judged against the constitution. From the main checkout, after step 10, start
the drift review task with the release's tag range as its base and follow its
run to done. After a binary-changed release HEAD is the new tag `v<ver>` and
the range is the previous tag to it. After a binary-unchanged release no new
tag exists: HEAD is main (origin/main, ahead of `v<ver>`) and the range is
`v<ver>..main`, everything on main the published binary's review has not
covered. The block decides which from git alone; on the second it refuses a
drift task whose body does not state that head, before any run is created.

Run `.satelle/skills/release/scripts/drift-review-run.sh`.

Then follow `.satelle/skills/drift-review.md` to `done`. The seat carries the
same `--base` as the run: a seat without it records HEAD (the released tag
`v$ver`, or main after a binary-unchanged release), the latest seat's base is
the one the gates judge from, and the diff the `drift-constitution` gate
judges would be empty.

A drift finding never reopens the release: it goes into the release's one
drift-fix item (the repository's scripts/drift-fix.sh), and that item is the fix. The release
run is already done when this starts; its drift gate (step 2a) already refused
any substrate miss, so the review's findings are mostly the judges'.
