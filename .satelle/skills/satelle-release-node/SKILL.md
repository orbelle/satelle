---
name: satelle-release-node
type: skill
description: The node's part of this repo's release on a fly.io sprite - the release-sprite remote's start and stop wrappers, the node's pinned tools, and release-node.sh, which tests the drift-checked candidate once as user release, decides the binary, bumps, publishes release.yml from release/<run> and fast-forwards main, then brings back only its scanned result.
---

# The release's node part

The release task (`.satelle/skills/release.md`) builds and drift-gates the
candidate here, then fires `.satelle/remotes/release-sprite` (`### Fire the
node's part`). The remote reaches a fly.io sprite through
`satelle-node-sprite`; these scripts add only what the release needs.

- `scripts/start.sh <node>` starts the sprite and copies
  `$SATELLE_RELEASE_IN` (written by `### The node's input`) to `/release/in`.
  It refuses when the variable names no directory.
- `scripts/stop.sh <node>` copies `/release/back` and nothing else into
  `$SATELLE_RELEASE_OUT/<node>/back` when that variable is set: the archive is
  written to a file, listed and refused unless every member is a regular file
  or a directory under `back` with no absolute path and no `..`, extracted
  owner-less into a fresh temp dir and checked again. It always ends by
  destroying the sprite.
- `scripts/tools.sh` installs make, bubblewrap, curl, go and gh at the
  versions it pins (go at release.yml's `go-version`), and the user `release`. Each tarball's SHA-256 is pinned in the
  script and checked with `sha256sum -c` before it is extracted; a mismatch
  exits 1. Upgrading a tool edits its version and digest together.
- `scripts/release-node.sh run` is the node's entry, run by the check
  `satelle-release-node-check` on the `release-node` workflow. Its steps, each
  of which fails as `failed step <name>: <reason>`: `input`, `clone`, `fetch`,
  `test`, `decide`, `bump`, `guard`, `publish`, `push`, `secret-scan`. It
  takes `GH_TOKEN` at entry into an unexported variable and gives it only to
  root's `git clone|fetch|push` against `https://github.com/orbelle/satelle`
  (a helper scoped to `https://github.com`, after `credential.helper=`) and to
  `gh --repo orbelle/satelle`. `make test` runs once as user `release` from
  `env -i`, in a throwaway copy `/release/suite`; `/release/work` and
  `/release/out` stay root's. Publishing runs from `release/<run>` before main
  moves, so a failed step before `push` leaves main where it was. Its stdout
  is only the compact `result`; `/release/out/log` never leaves the node.
- `scripts/release-node.sh verify <rd> <node> <session>` runs here after the
  fire and reads only `$rd/node-out/<node>/back`.
- `scripts/release-node.sh dry-input <dir> <sha> <base>` writes the input of a
  dry run (`.satelle/tasks/release-node.md`), which stops after `decide`.
