---
name: release
scope: project
type: skill
tags: [type:skill]
description: In-loop executor skill for the `release` step — commit the slice with a .version bump and CHANGELOG entry, publish onto main with `satelle trunk publish`, dogfood the published release with `satelle update`, and attach the release summary the close gates judge. An epic child whose parent owns the merge commits to its branch only. Never self-advances status.
---

# Release (in-loop executor)

Prior gates accepted the slice. Commit it with a version bump, publish it, run the published release on this machine, and record the evidence. Engage in a prior tool call before any `git commit` or `git push` ([[satelle-edits-require-a-story]]). `release → done` is the gates' move.

## Epic child: branch only

Take this path only when all hold: a step in `.satelle/workflows/step.toml` sets `after_children`; this story's `parent_id` is set; that parent's category is `epic-parent` or `parent`; this story is not itself a container and is not tagged `lane:trunk`. Then commit the slice to `epic/<story-id>`, push that branch only, leave `.version` and `CHANGELOG.md` untouched, record the branch on the story, and stop. Otherwise do everything below.

## 1. Commit

1. `make check-serve-version`, on every release. Non-zero: bump `satelled.version` and add a `## [serve-v<x>]` CHANGELOG entry. "Baseline is stale": `git fetch --tags origin` and re-run. "Cannot verify": restore connectivity; never release on an unverified baseline. The script owns the serve-path set (`bash scripts/check-serve-version.sh --paths`); do not judge it from the diff.
2. `gofmt -s -w internal/ cmd/ tests/`, then stage only the story's files — never `git add -A`; confirm with `git diff --cached --stat`.
3. Bump `.version`: the patch of `satelle.version`; `satelled.version` when step 1 asked; `satelle.build` = `date -u +"%Y-%m-%d-%H-%M-%S"`.
4. Add `## [<new ver>] - <YYYY-MM-DD>` at the top of `CHANGELOG.md`, with Added / Changed / Fixed bullets ending in the story id.
   - A breaking change goes under `### Breaking`, written as the remediation: what broke and the exact command that heals it. The drift gate prints these bullets verbatim to repos stamped before this release.
   - Add `- init-heals: <what init does>` only when `satelle init` alone performs every migration the entry names. Never when any step is manual: the marker makes ordinary commands refuse until init runs.
   - Retiring an embedded substrate name adds a `RetiredSubstrate` row to `internal/config/retired.go` (Version = this release; Replacement = the successor, or `""` with a Note).
   - `cp CHANGELOG.md internal/verb/embedded/CHANGELOG.md`; stage both.
5. Commit: conventional subject ending with `(<story id>)`, no AI attribution (no `Co-Authored-By`, no "generated with"). Check `git show --stat HEAD`.

## 2. Publish and record CI

Publish with `satelle trunk publish`, never a bare `git push`. It merges origin/main in, proves the combined head, pushes, and retries a refused push. It never forces, and neither do you.

```bash
satelle trunk publish --story <id> --prove "go build ./..." --json
SHA=$(git rev-parse HEAD)
```

- The suites ran in the gates; the proof is a build. When the report shows `"merged": true`, run `go test -count=1 ./...` on the pushed head and record it. On the epic-parent / parent route, run `go test -count=1 ./...` and `make integration` on the merged head before publishing, and record both.
- When publish stops (conflict, failed proof, rounds spent), trunk is restored: fix under this story and publish again. A `.version` or `CHANGELOG.md` conflict means another machine released first: re-bump on top of theirs and publish again.
- Record the `trunk_publish` combined and pushed heads; CI evidence is for the pushed head.

Record CI and the tag; do not `gh run watch`:

```bash
for i in $(seq 1 8); do
  gh run list --commit "$SHA" --json name,status,conclusion,url
  gh release view "v$(awk '$1=="satelle.version:"{print $2}' .version)" 2>/dev/null && break
  sleep 15
done
```

Record the real `test` and `release` conclusions and the tag; never record success over a red or unconcluded run. `test` failed: fix under this story and release again. `release` failed or no tag: surface it and stop.

## 2b. Or release through the satelle-cicd remote

When the operator releases on the remote instead of steps 1–2 (sty_8094a272):

1. On the story branch, add the CHANGELOG entry under `## [Unreleased]` at the top (Added / Changed / Fixed bullets ending in the story id; no `.version` change, no embedded copy). Merge origin/main in so the branch contains it, commit, and push `epic/<id>` (or `feat/<id>`).
2. Wake the remote: `fire_remote_trigger` on the satelle-cicd remote with its `release` request trigger and the story id. Its node runs `.satelle/remotes/satelle-cicd/release.sh`: it stamps `.version`, the satelled bump and the CHANGELOG heading, opens the story's PR, approves it under the operator's gh token through `satelle pr approve`, merges it, waits for `test`, `release` and the tag, and attaches `live-evidence`.
3. Read the run once when it ends (`list_remote_nodes` with the remote and story), not on a timer. A `reject` names the script's exit and reason; fix under this story and wake it again.
4. Then do sections 3 and 5 here, quoting the node's `live-evidence` for the CI and tag lines.



Once the tag exists:

```bash
satelle update            # installs the published CLI + serve assets and cycles the service; non-zero = release not done
satelle version           # check_cli_version
satelle service status    # check_persistent_supervisor, check_live_footer
curl -fsS "http://127.0.0.1:${PORT:-8787}/" | grep -E "(satelled|satelle-serve) <satelled.version>|satelle <satelle.version>"
```

| Named check | Must be true |
| --- | --- |
| `check_cli_version` | `satelle version` reports `satelle.version` and the pushed SHA prefix |
| `check_live_footer` | the running service reports its unit's binary version: `satelled <satelled.version>` for satelled or the legacy satelle-serve link, `satelle <satelle.version>` for the deprecated `satelle serve` alias |
| `check_persistent_supervisor` | the service runs under a system unit or a linger-backed user manager, never a `nohup`/`setsid` relaunch |

- Quote `satelle update`'s exe-identity line (e.g. "restarted satelle.service (user unit, pid …) onto the new binary") and the `satelle service status` line as the evidence. A version grep alone is a fallback only when exe identity is unavailable; say so.
- `service status` passes on `active (user unit, pid …)`, `active (system unit, pid …)`, or `running (pid …) on the installed binary, but its supervisor is unreachable`. It fails on `not installed`, `installed, not running`, `start-limited`, or `NOT reported active by the installed unit — an ephemeral process`.
- `satelle update` exits non-zero: the release is not done. Read its message, fix, re-run. Never hand-restart the service or relaunch it with `nohup`/`setsid`.
- `no persistent supervisor owns it`: run `satelle service install --system` (or `satelle service install` with lingering) once, then `satelle update` again.
- Footer still shows the old version after a zero exit: `check_live_footer` failed. Raise the restart-path defect.
- `make install` is not dogfood. A global binary left on an unreleased build is recovered with `satelle update` (or `--force`).

## 4. Epic-parent only: clean up the children

After main is pushed, CI is green and dogfood passed, for each child (`satelle story list --tag epic:<theme>`, excluding the parent): delete `epic/<child-id>` on origin and locally only when `git merge-base --is-ancestor origin/epic/<child-id> origin/main`; remove its worktree only when `git status --porcelain` shows nothing but ignored entries. Never force. List each removal, and each kept branch or worktree with its reason, in the summary.

## 5. Attach the summary

```bash
satelle story attach <id> --name "release-summary-<id>" --type story-implementation-summary --body "…"
```

Name what shipped, the SHA and `trunk_publish` heads, the version and tag, the `test` and `release` URLs with conclusions, and each named dogfood check with its observed line. For a story the sync architecture governs ([[satelle-sync-architecture]] §6 rule 3), also attach the `satelle doctor --all` output and a session start in each registered repo, captured before and after `satelle update` and `satelle init --all --yes`. `satelle-story-release-review` judges it; the CI, dogfood and changelog checks re-verify the machine. See [[satelle-done-is-last]].
