---
name: release
scope: project
type: skill
tags: [solo-dev, executor, release]
description: In-loop executor skill for a merged release step. Stages the slice, bumps the version file (example: .version), updates the changelog (example: CHANGELOG.md), commits without AI attribution, pushes to trunk, installs the published binary locally under a persistent supervisor (dogfood), and records CI/release evidence. The release-review gate is authority on CI-green and recorded install evidence.
---

# Release (in-loop executor step)

You are the **executor** for the merged `release` step (in-loop on the driving session when the workflow assigns `agent=executor`). Prior gates accepted the slice. **Commit with a version bump, push, install locally, prove the release, and record evidence.** You never self-enact `release → done` — the gate does that.

**Engage before any `git commit` / `git push`.** Engagement must already be true in a **prior** tool call (`plan` or `in_progress` per the governing workflow). commitgate is PreToolUse — it evaluates the whole Bash payload before any line runs — so fusing `satelle story set … --status …` with `git commit`/`git push` in one command cannot pass. See [[satelle-edits-require-a-story]] and `session-trace-workflow-review-followups` (repo principle/doc).

**Dogfood is part of the release, not optional cleanup.** It means the machine you released from runs the new binary as the live web service — installed via `satelle update --session <session>` (the **published** asset a user gets, sudo-free), which also restarts the service. The pass criterion is mechanism-AGNOSTIC: the **running** web service reports the new version, reached through a **persistent supervisor** (a system unit, or a linger-backed user manager that survives session loss), never an ephemeral `nohup`/`setsid` relaunch that dies with your shell. The *restart mechanism* is not the test: if the systemd `--user` bus is unreachable (a headless / non-login WSL session — no `/run/user/<uid>/systemd`) yet `satelle update --session <session>` installed the binary and the live version matches under a persistent supervisor, that is **not** a failed release. The release HAS failed only when the binary did not install, or the running service still reports the OLD version (or runs under no persistent supervisor). Fix the persistence under this story and re-verify; do not attach a success summary or proceed to done until the live version matches persistently.

## 1. Stage and commit (bump is mandatory)

0. **Serve-path version discipline** (<story-id>) — if this slice touches
   `cmd/<serve-binary>/`, `internal/<web-or-mirror packages>/` (example paths), `internal/buildinfo/`, run before commit:
   ```bash
   make check-serve-version  # example: optional dual-binary version discipline check
   ```
   Exit non-zero means bump `satelled.version` (and the serve-v changelog
   entry) before releasing. The check is configuration (`scripts/check-serve-version.sh`);
   the release step is the release-path gate that runs it.
1. **Format, then stage the STORY'S SLICE** — only the files this story changed (read the story body/acceptance criteria and `git status --short`):
   ```bash
   gofmt -s -w internal/ cmd/ tests/ 2>/dev/null; git add <the story's files>
   ```
   Do NOT `git add -A`: the tree may carry another session's in-flight changes. Confirm the staged set is exactly the slice (`git diff --cached --stat`).
2. **Bump `.version`** — MANDATORY. Single source of truth for the release tag (`v<satelle.version>`) and the baked build identity; `release` cuts a tag ONLY when `.version` changed, so a missed bump strands the released binary.
   - Increment the **patch** of `satelle.version` (`0.0.11` → `0.0.12`).
   - When the slice is serve-path work, also advance `satelled.version` as required by `make check-serve-version  # example: optional dual-binary version discipline check`.
   - Set `satelle.build` to `date -u +"%Y-%m-%d-%H-%M-%S"`. `git add .version`.
3. **Update `CHANGELOG.md`** — MANDATORY (<story-id>). The `release → done` gate
   `satelle-changelog-entry-check` fails closed when the version on HEAD has no entry.
   - Add a level-2 header `## [<new ver>] - <YYYY-MM-DD>` at the **top** of the file
     (newest first).
   - Put any breaking change under a `### Breaking` subsection — that is the marker
     require-init and post-upgrade heal key on.
   - Summarise Added / Changed / Fixed bullets ending with the story id in parens.
   - **Sync the embed** (<story-id>): `cp CHANGELOG.md <embedded-changelog-path>  # example: only if your binary embeds the changelog`
     so the consumer channel ships with the binary. Root file is build input; the embed
     is what `satelle changelog` and require-init read.
   - `git add CHANGELOG.md <embedded-changelog-path>  # example` with the slice.
4. **Commit.** A conventional-commit subject ending with the story id in parens, e.g. `feat(web): add the X view (<story-id>)`. **No AI attribution** — no `Co-Authored-By`, no "generated with" trailer (the adopting repo's convention). Verify the commit captured the intended files (`git show --stat HEAD`).

## 2. Push, then install locally (dogfood), then capture CI evidence

Pushing to `main` triggers **`test`**. On success, the version-gated **`release`** workflow cuts tag `v<satelle.version>` and publishes assets. Push to main IS the publish path. **Do not `gh run watch`** — RECORD conclusions (<story-id>).

```bash
git push origin main
SHA=$(git rev-parse HEAD)
for i in $(seq 1 10); do TID=$(gh run list --commit "$SHA" --workflow test --limit 1 --json databaseId -q '.[0].databaseId'); [ -n "$TID" ] && break; sleep 3; done
```

### 2a. Dogfood the PUBLISHED release with `satelle update --session <session>`

Dogfood installs the **published release asset** — the exact artifact a user gets —
not a local build. So it runs **after the `release` workflow publishes the tag**
(see §2b); `satelle update --session <session>` resolves the latest release, so the tag must exist first.

**One-time setup (skip if already done):** a **persistent supervisor** must run the
web service, so a restart survives session loss and needs no sudo per release. Install
it once with `satelle service install --session <session>` (systemd **user** unit — sudo-free, when the
user manager is alive) or, on a box whose user manager is down (headless / non-login
WSL — no `/run/user/<uid>/systemd`), `satelle service install --system --session <session>` (a persistent
**system** unit, `Restart=always`, running as you; one sudo at install time only).

Once the release tag is published, dogfood. The **dogfood triad** is three **named
checks** the release summary must satisfy (the release-review gate rejects by these
names when a gap is present):

| Named check | What must be true |
| --- | --- |
| **`check_cli_version`** | `satelle version --session <session>` reports `$CLI_VER` (and the pushed commit SHA prefix) |
| **`check_live_footer`** | Live web footer matches the **running** service binary's version (<story-id>): if ExecStart is `satelled`, grep `$SERVE_VER`; if still the deprecated `satelle serve` alias, grep `$CLI_VER`. Prefer migrating the unit to satelled (`satelle service install --session <session>`). |
| **`check_persistent_supervisor`** | The live service is under a **persistent** supervisor (system unit or linger-backed user manager), never an ephemeral `nohup`/`setsid` relaunch |

```bash
CLI_VER=$(awk '$1=="satelle.version:"{print $2}' .version)
SERVE_VER=$(awk '$1=="satelled.version:"{print $2}' .version)
satelle update --session <session>                                   # pulls CLI + serve assets (sudo-free),
                                                 # restarts the supervisor onto the new binary
satelle version --session <session>                                  # check_cli_version: must report $CLI_VER + SHA prefix
# check_live_footer: version of the process that holds the port
curl -fsS "http://127.0.0.1:${PORT:-8787}/" | grep -E "satelled $SERVE_VER|satelle $CLI_VER"
# expect satelled $SERVE_VER when unit runs satelled; satelle $CLI_VER for deprecated CLI alias
# check_persistent_supervisor: confirm service is the installed unit, not a throwaway serve
```

`satelle update --session <session>` is sudo-free: it rewrites your `~/.local/bin` binary and restarts the
service — the user unit via `systemctl --user`, or a system unit by signalling its
process so `Restart=always` respawns it onto the new binary. The **running service
reporting `$VER`** is the pass criterion — not any particular restart command:

- `satelle update --session <session>` reports "already up to date" but the footer shows the OLD version →
  the restart no-op'd (dead user manager, or a non-`Restart=always` supervisor). Fix the
  supervisor (`satelle service install --system --session <session>` for a persistent one), re-run.
- Footer still shows the **previous** version → **`check_live_footer` failed** (stale process). Make
  the persistent supervisor run the new binary; re-verify. An ephemeral `nohup`/`setsid
  satelle serve …` relaunch is **NOT** an acceptable final state — it dies with the
  session and the fleet silently reverts; use it only as a throwaway probe
  (`check_persistent_supervisor` fails).
- `make install  # example: optional local install from the working tree` is fine for an immediate local CLI build, but the DOGFOOD is `satelle
  update --session <session>` (the real published artifact + the path a user hits) — do not substitute it.
- Do **not** treat "printed guidance" or a manual note as success — the dogfood is done
  only when **all three named checks** pass.

Draft the summary body (§3) once every triad check passes.

### 2b. CI + published tag (one consolidated look)

```bash
for i in $(seq 1 8); do
  gh run list --commit "$SHA" --json name,status,conclusion,url
  gh release view "v$(awk '$1=="satelle.version:"{print $2}' .version)" 2>/dev/null && break
  sleep 15
done
```

Record what you actually SEE — `test` and `release` run URLs with **real conclusions** and the published tag. **Do not auto-retry, amend, or force-push, and never record success over a red or unconcluded run.** If `test` failed, fix under this story and re-run release. If `release` failed or the tag is missing, surface it and stop.

## 3. Record the summary WITH the story

Write a short PR-style summary and attach it — evidence the release gate judges. **Must include the dogfood triad by name** (`check_cli_version`, `check_live_footer`, `check_persistent_supervisor`):

```bash
satelle story attach <story-id> --name "release-summary-<story-id>" \
  --type story-implementation-summary --body "…" --session <session>
```

Summary must cover: what shipped, SHA, version/tag, test + release URLs/conclusions, and **each named dogfood check** with its observed result (CLI version line, live footer/service at `$VER`, persistent supervisor — not ephemeral).

The `satelle-story-release-review` gate judges this recorded evidence and the ACs. See [[satelle-agent-model]], [[satelle-done-is-last]].
