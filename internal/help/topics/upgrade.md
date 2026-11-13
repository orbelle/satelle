# Upgrading a pre-1.0 repo

v1 is a clean rewrite: a pre-1.0 `.satelle` (its `satelle.toml` keys, its
`workflows/step.toml`, `done.toml` and `agents.toml`) does not load, and every
command names `satelle upgrade --archive`. While it does not load, the edit
gate allows edits under `.satelle/` only. The way across has two steps.

satelle names a pre-1.0 repo, each time with the same one line, `<path>:
needs upgrade: ...`, naming `satelle upgrade --archive` to run there and the
satelle-upgrade skill it writes: at session start in that repo (to the agent,
and to you where the harness has a user channel), in `satelle doctor` (an
`upgrade` issue in that repo, a note for each other registered one), in the
automatic sync pass (which skips it without failing), on satelled's project
page and landing (`upgrade needed`, with the command), and at the end of
`satelle update`, after it installs. The fix is always run in that repo.

## 1. Archive and start fresh

    satelle upgrade --archive --dry-run   # what it would move, and where
    satelle upgrade --archive

This moves the old `.satelle`, verified file by file (paths, modes, bytes,
symlink targets), to `<runtime>/archive/<UTC stamp>/.satelle` (`satelle
runtime path` names the runtime dir); nothing is deleted. It then writes a
fresh v1 `.satelle` (as `satelle init` does), which validates, so the edit gate
works again. Pre-1.0 hooks call commands v1 does not have: `satelle update`
and the sync pass refresh the untracked hook files of each harness in use in a
registered project; for a hook file git tracks, and in a repo that is not
registered, run `satelle agents install <harness>` (and commit the change).

The fresh `.satelle` holds one starter workflow and the upgrade skill. The
archive step writes `.satelle/skills/satelle-upgrade.md`: have your session
agent follow it. It ports the archived workflows, agent bindings and skills,
optimised and confirmed with you, and writes the status map.

## 2. Import the old items

The old store is the runtime directory that holds `satelle.db` (the archive
step prints it when it finds `satelle.db` in the runtime directory or in the
archived `.satelle`; otherwise name the directory the pre-1.0 binary used). Write a status map: every old status an item has, mapped to a
workflow and state of the new `.satelle`:

    [status]
    backlog = { workflow = "delivery", state = "backlog" }
    done = { workflow = "delivery", state = "done" }
    "task/active" = { workflow = "standing", state = "open" }

A `<kind>/<status>` key wins over a bare `<status>` key. Then:

    satelle upgrade --from <old store> --map <file> --dry-run
    satelle upgrade --from <old store> --map <file>

The dry run prints each mapping rule with its item count, the totals and the
map's sha256, and writes nothing. An old status the map does not name, a
target workflow or state the `.satelle` lacks, or an unreadable row refuses
the whole import before anything is written, listing each problem.

What is imported, for every old item, open and closed:

- the item, with its old id, kind, title, body, criteria (the old single text
  split at numbered or bulleted lines), tags, parent, category, assignee (as
  a one-entry assignees list) and created time, in the workflow and state the map gives;
- each old history row as an `upgrade.imported` event carrying the row
  unchanged (kind, actor, body, payload, time);
- each file of its old `stories/<id>/` directory as an attachment with the same
  bytes and sha256;
- an `upgrade.mapped` event, last, recording the map, its sha256, the rule
  used, the old status and the old priority and other columns.

Not imported: the old store's regenerated views (`stories/<id>.md`,
`index.md`, `log.md`), binary-file sidecars (`*.satelle.json`, read for their
media type), history rows that name no item, and task definitions under the
archived `.satelle/tasks/` (port them as tasks). A second run imports nothing
twice; an interrupted run resumes.

## Repos that start clean

`~/.satelle/config.toml` `[upgrade] start_clean = ["<repo root>", ...]` lists
repos that start clean on v1: `--archive` works, but `--from` refuses, naming
the setting.

The old store is opened read-only. If its directory is read-only, pass a copy:
sqlite needs to write its `-shm` file to read a store with a write-ahead log.
