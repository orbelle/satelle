---
name: satelle-dogfood-check
scope: project
type: skill
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check gate on entry to done: the released version is actually installed, serving, and under a persistent supervisor, judged from the live process's exe identity. Never runs `satelle update` itself — the executor deploys, this gate confirms.
---

# Dogfood check (objective 4)

Re-reads the dogfood triad from the machine (exe identity, not version strings). Verifies only: never runs `satelle update`, install or restart.

## What it checks

| Named check | Assertion |
| --- | --- |
| `check_cli_version` | the installed `satelle` reports the version in `.version`, or a newer version whose build commit contains this release's commit (a release installed on top of it) |
| `check_installed_identity` | the installed CLI (and serve, when present) bytes match the published asset for the version they report — a matching version string is **not** sufficient proof |
| `check_live_footer` | the process holding the service port runs the INSTALLED binary (exe identity, not a version string that a release may not have touched) |
| `check_persistent_supervisor` | that process is supervised by something that survives session loss — never an ephemeral relaunch |

An epic child whose parent owns the merge (not `lane:trunk`) is skipped.

```check
#!/usr/bin/env bash
set -uo pipefail
# Process of record (sty_9c989241): a story driven in a linked worktree has no
# data dir of its own; read the main tree's, beside the git common dir.
sd=.satelle
if [ ! -d "$sd" ]; then
  common=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null) && [ -d "$(dirname "$common")/.satelle" ] && sd="$(dirname "$common")/.satelle"
fi
# Epic child whose parent owns the merge (sty_2e9d0440). The parent release
# owns the version bump, the main CI run, and dogfood. This check skips those
# assertions and says why. A story that is not such a child falls through.
payload=$(cat)
sid=$(printf '%s' "$payload" | grep -oE '"id":"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
if [ -n "$sid" ]; then
  parent_id=$(satelle story get "$sid" --json 2>/dev/null | grep -oE '"parent_id"[[:space:]]*:[[:space:]]*"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
  # A lane:trunk child ships through its own full release (sty_cf79e443), so it is
  # judged like a story outside an epic.
  if [ -n "$parent_id" ] && ! satelle story get "$sid" --json 2>/dev/null | grep -q '"lane:trunk"'; then
    parent_cat=$(satelle story get "$parent_id" --json 2>/dev/null | grep -oE '"category"[[:space:]]*:[[:space:]]*"[^"]+"' | head -1 | grep -oE '"[^"]+"$' | tr -d '"')
    if [ "$parent_cat" = "epic-parent" ] || [ "$parent_cat" = "parent" ]; then
      if grep -qE '^after_children[[:space:]]*=' "$sd"/workflows/step.toml 2>/dev/null; then
        echo "skipped: epic child $sid of $parent_cat $parent_id; a step sets after_children, so this check does not require a version bump, a main workflow run, or a dogfood install"
        exit 0
      fi
    fi
  fi
fi


ver=$(awk '$1=="satelle.version:"{print $2}' .version 2>/dev/null)
if [ -z "$ver" ]; then
  echo "OBJECTIVE 4 FAILED: could not read satelle.version from .version"
  exit 1
fi
fail=0

# --- check_cli_version: the INSTALLED cli, not this repo's build ------------
installed=$(command -v satelle || true)
if [ -z "$installed" ]; then
  echo "OBJECTIVE 4 FAILED (check_cli_version): no installed satelle on PATH"
  exit 1
fi
cli_line=$("$installed" version 2>/dev/null | head -1)
cli_ver=$(printf '%s' "$cli_line" | awk '{print $2}' | tr -d 'v')
if [ "$cli_ver" = "$ver" ]; then
  echo "  check_cli_version: $installed reports $cli_ver"
else
  # A newer release installed on top of this one (back-to-back releases,
  # sty_a2246048) still dogfoods it when its build contains this story's release
  # commit: the release commit is this tree's HEAD, the installed build's commit
  # is in `satelle version` ("(commit <sha>, ...").
  rel_commit=$(git rev-parse HEAD 2>/dev/null || true)
  inst_commit=$(printf '%s' "$cli_line" | grep -oE 'commit [0-9a-f]{7,40}' | head -1 | awk '{print $2}')
  newer=""
  if [ -n "$cli_ver" ] && [ "$(printf '%s\n%s\n' "$ver" "$cli_ver" | sort -V | tail -1)" = "$cli_ver" ]; then
    newer=1
  fi
  if [ -n "$inst_commit" ] && ! git cat-file -e "${inst_commit}^{commit}" 2>/dev/null; then
    git fetch -q origin 2>/dev/null || true
  fi
  if [ -n "$newer" ] && [ -n "$rel_commit" ] && [ -n "$inst_commit" ] && git merge-base --is-ancestor "$rel_commit" "$inst_commit" 2>/dev/null; then
    echo "  check_cli_version: $installed reports $cli_ver (newer than this release's $ver); its build ${inst_commit} contains the release commit ${rel_commit}"
  else
    echo "OBJECTIVE 4 FAILED (check_cli_version): installed satelle reports ${cli_ver:-<none>} (commit ${inst_commit:-<unknown>}), .version says $ver (release commit ${rel_commit:-<unknown>})"
    echo "  a newer installed release passes only when its build contains the release commit"
    echo "  run: satelle update"
    fail=1
  fi
fi

# --- check_installed_identity: version match ≠ byte match (sty_1cd2ff01) ---
# Consume `satelle update --check` rather than re-fetching .sha256 in bash.
# The binary owns the comparison; this gate only requires an honest report.
check_out=$("$installed" update --check 2>&1) || true
if echo "$check_out" | grep -qE 'identity NOT verified|installed build differs|update available:'; then
  echo "OBJECTIVE 4 FAILED (check_installed_identity): installed bytes do not match the published release (or identity could not be verified)"
  printf '%s\n' "$check_out" | sed 's/^/  /'
  echo "  run: satelle update   # or: satelle update --force"
  fail=1
elif echo "$check_out" | grep -qE 'sha256 .* matches published asset|already up to date'; then
  echo "  check_installed_identity: published asset checksum matches installed CLI"
else
  # Offline / unexpected output: do not fail closed on network alone here —
  # version and live-footer checks still guard the release. Surface the lines.
  echo "  check_installed_identity: could not confirm checksum from --check (non-fatal):"
  printf '%s\n' "$check_out" | sed 's/^/  /' | head -8
fi

# --- check_live_footer: the SERVING process runs the installed binary -------
# Compare exe identity against the installed service binary — satelled (its
# current name) or satelle-serve (the legacy name, kept as a fallback) beside
# the installed CLI, each resolved through readlink -f — rather than grepping
# a version string, so an unchanged version cannot masquerade as a restart.
# Match by PROCESS NAME (comm), never by command line (sty_4e3a7f2a): this
# script's own `bash -c` carries the service argv in its command line, so a
# `pgrep -f` pattern matched the check itself whenever its pid sorted below the
# service's. The unit runs satelle-serve (a symlink to satelled) or satelled.
svc_pid=$( { pgrep -x satelle-serve; pgrep -x satelled; } 2>/dev/null | head -1 || true)
if [ -z "$svc_pid" ]; then
  echo "OBJECTIVE 4 FAILED (check_live_footer): no satelle-serve process is running"
  fail=1
else
  live_exe=$(readlink -f "/proc/$svc_pid/exe" 2>/dev/null || true)
  want_exe=""
  for name in satelled satelle-serve; do
    cand=$(readlink -f "$(dirname "$installed")/$name" 2>/dev/null || true)
    if [ -n "$cand" ] && [ -e "$cand" ]; then
      [ -z "$want_exe" ] && want_exe=$cand
      if [ "$live_exe" = "$cand" ]; then want_exe=$cand; break; fi
    fi
  done
  if [ -z "$want_exe" ] || [ "$live_exe" != "$want_exe" ]; then
    echo "OBJECTIVE 4 FAILED (check_live_footer): the serving process is NOT the installed binary (stale)"
    echo "  live (pid $svc_pid): ${live_exe:-<unreadable>}"
    echo "  installed:           ${want_exe:-<missing>}"
    echo "  run: satelle update"
    fail=1
  else
    echo "  check_live_footer: pid $svc_pid runs the installed $want_exe"
  fi

  # --- check_persistent_supervisor -----------------------------------------
  # Kernel facts only: the parent must be a systemd manager (pid 1 = system
  # manager; otherwise a user manager, which only survives logout when the
  # account lingers). An ephemeral relaunch is NOT an acceptable final state —
  # it dies with the session and the fleet silently reverts.
  # Parse PPid from /proc/<pid>/stat anchored on the LAST ')': the comm field is
  # parenthesised and may itself contain spaces and parens, which would shift
  # every field if the whole line were split.
  ppid=$(sed 's/.*) //' "/proc/$svc_pid/stat" 2>/dev/null | awk '{print $2}')
  pname=$(cat "/proc/${ppid:-0}/comm" 2>/dev/null || true)
  if [ "$pname" != "systemd" ]; then
    echo "OBJECTIVE 4 FAILED (check_persistent_supervisor): pid $svc_pid's parent is '${pname:-unknown}' (pid ${ppid:-?}), not a systemd manager"
    echo "  an ephemeral process dies with the session — run: satelle service install --system"
    fail=1
  elif [ "$ppid" != "1" ] && [ ! -e "/var/lib/systemd/linger/$(id -un)" ]; then
    echo "OBJECTIVE 4 FAILED (check_persistent_supervisor): supervised by a systemd USER manager without lingering"
    echo "  it dies with the login session — run: satelle service install --system"
    fail=1
  else
    echo "  check_persistent_supervisor: parent pid $ppid ($pname)"
  fi
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "objective 4 PASS: installed cli $ver, serving process on the installed binary, persistent supervisor"
```
