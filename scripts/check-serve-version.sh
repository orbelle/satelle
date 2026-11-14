#!/usr/bin/env bash
# check-serve-version — fail when serve-path sources changed since the last
# serve-v* tag but satelled.version was not advanced (sty_4a5c6924).
# Exit 0 when no serve-path change, or when the serve version line advanced
# relative to the tagged commit.
#
# THE WATCHED SET IS DERIVED, NOT AUTHORED (sty_a8853e85). It is exactly what
# `cmd/satelled` transitively imports, computed here at run time.
#
# It used to be a literal four-entry array, and that array was a SECOND answer to
# a question the compiler already answers — so it drifted: 4 of the 16 in-repo
# packages the serve binary compiles in were watched. `internal/serve`, which
# runs ONLY inside the service, was not among them. The failure that makes this
# worth deriving is silent by construction: with no `satelled.version` bump,
# `satelle update` reports "already up to date", the operator sees a green
# release, and the running service keeps the old code.
#
# Configuration-over-code holds. The pass/fail RULE stays here, in configuration;
# `go list` is a mechanism this check invokes to enumerate a surface, which is
# exactly what a functional check is allowed to do. What is removed is the
# hand-maintained fact, not the decision.
#
# Baseline: the newest local serve-v* tag, but only after verifying it is not
# stale against the remote (sty_da6c3874). Serve tags are cut by the GitHub
# release workflow and exist only on the remote until fetched; answering from a
# lagging local list is the same class of defect as a shallow clone whose tag
# commit is missing — refuse rather than answer "fine". The gate never fetches;
# it prints the remedy. Override the remote name with SERVE_TAG_REMOTE (tests).
#
# Modes:
#   (no args)                 run the gate
#   --paths                   print the derived watch set, one per line, exit 0
#   --check-path <path>...    print each <path> NOT under the watch set, one per
#                             line; exit 0 if every path is watched, 1 if any is not
#   --files <pkg>...          print, sorted and unique, one `<kind><TAB><path>`
#                             line (module-relative) per file of every
#                             main-module package in `go list -deps <pkg>...`,
#                             for the GOOS/GOARCH/CGO_ENABLED of its environment
#                             (the caller owns that environment; this mode
#                             cleans nothing). Kinds: `built` (Go, cgo, C, C++,
#                             ObjC, header, Fortran, assembly, SWIG and syso
#                             sources, and every embedded file), `excluded`
#                             (sources a build constraint leaves out, except
#                             _test.go files), `pattern` (each //go:embed
#                             pattern, `all:` removed, joined to its package
#                             directory). Test-only files and embeds are not
#                             read: only `go test` compiles them. It owns "what
#                             a shipped binary is built from" for
#                             scripts/release-binary.sh (sto_da9775c1).
#
# Exit codes: 0 ok / watched; 1 gate failure or an unwatched path; 2 usage error;
# 3 (CANNOT_ANSWER) the watch set could not be derived, so there is NO answer.
# 3 is distinct from 1 on purpose (sto_54770db7): a failed `go list` used to exit
# 1, which `--check-path` callers read as "this path is unwatched" — a transient
# derivation failure under a parallel test suite reported one package as missing
# from a set it was in. The reason (go's own stderr) is printed with the exit.
set -euo pipefail

CANNOT_ANSWER=3

ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "check-serve-version: not a git repo" >&2
  exit "$CANNOT_ANSWER"
}
cd "$ROOT"

# go list's stderr is kept so a failure names its reason instead of "failed".
# Created in the parent: serve_paths runs in a $( ) subshell, whose EXIT trap
# would not be the parent's.
GO_ERR=$(mktemp)
trap 'rm -f "$GO_ERR"' EXIT

# go_reason prints the captured go stderr on one line.
go_reason() {
  tr '\n' ' ' <"$GO_ERR" | sed 's/[[:space:]]*$//'
}

# serve_paths prints the directories the serve binary is built from. FAILS CLOSED:
# a `go list` error or an empty set exits non-zero rather than yielding a gate
# that watches nothing. An inert gate is worse than the narrow one being replaced.
serve_paths() {
  local mod deps out rc
  mod=$(go list -m 2>"$GO_ERR") || {
    rc=$?
    echo "check-serve-version: go list -m failed (exit $rc) — cannot resolve the module path: $(go_reason)" >&2
    return 1
  }
  [ -n "$mod" ] || {
    echo "check-serve-version: go list -m returned nothing" >&2
    return 1
  }
  deps=$(go list -deps ./cmd/satelled 2>"$GO_ERR") || {
    rc=$?
    echo "check-serve-version: go list -deps ./cmd/satelled failed (exit $rc): $(go_reason)" >&2
    return 1
  }
  # In-repo packages only; a stdlib or vendored dep is not ours to version.
  # `cmd/satelled` itself is in this list and must stay watched.
  out=$(printf '%s\n' "$deps" | sed -n "s|^${mod}/||p" | sort -u | sed 's|$|/|')
  [ -n "$out" ] || {
    echo "check-serve-version: derived an EMPTY watch set" >&2
    return 1
  }
  printf '%s\n' "$out"
}

# --files is answered before the satelled derivation below: it names its own
# packages, and its failure is the same CANNOT_ANSWER with go's reason.
if [ "${1:-}" = --files ]; then
  shift
  [ $# -gt 0 ] && [ -n "$1" ] || { echo "check-serve-version: --files needs a package" >&2; exit 2; }
  moddir=$(go list -m -f '{{.Dir}}' 2>"$GO_ERR") || {
    echo "check-serve-version: go list -m failed — cannot resolve the module directory: $(go_reason)" >&2
    exit "$CANNOT_ANSWER"
  }
  # One <kind><TAB><package dir><TAB><file or pattern> line per entry.
  line='{{"\t"}}{{$d}}{{"\t"}}{{.}}{{"\n"}}'
  tmpl='{{if .Module}}{{if .Module.Main}}{{$d := .Dir}}'
  for f in GoFiles CgoFiles CFiles CXXFiles MFiles HFiles FFiles SFiles SwigFiles SwigCXXFiles SysoFiles EmbedFiles; do
    tmpl="$tmpl{{range .$f}}built$line{{end}}"
  done
  for f in IgnoredGoFiles IgnoredOtherFiles; do
    tmpl="$tmpl{{range .$f}}excluded$line{{end}}"
  done
  tmpl="$tmpl{{range .EmbedPatterns}}pattern$line{{end}}{{end}}{{end}}"
  if ! listed=$(go list -deps -f "$tmpl" "$@" 2>"$GO_ERR"); then
    echo "check-serve-version: go list -deps $* failed: $(go_reason)" >&2
    exit "$CANNOT_ANSWER"
  fi
  out=$(printf '%s\n' "$listed" | awk -F'\t' -v m="$moddir" '
    NF != 3 { next }
    $1 == "excluded" && $3 ~ /_test\.go$/ { next }
    {
      f = $3
      if ($1 == "pattern") sub(/^all:/, "", f)
      d = $2
      if (d == m) d = ""; else if (index(d, m "/") == 1) d = substr(d, length(m) + 2)
      print $1 "\t" (d == "" ? f : d "/" f)
    }' | sort -u)
  [ -n "$out" ] || {
    echo "check-serve-version: go list -deps $* listed no file of the main module" >&2
    exit "$CANNOT_ANSWER"
  }
  printf '%s\n' "$out"
  exit 0
fi

# Capture with $( ) and test the status in the PARENT. `mapfile < <(serve_paths)`
# reads better and is wrong: a process substitution is a subshell, so an `exit`
# inside it ends only that subshell. The parent carried on with an EMPTY array —
# and an empty array in `git diff -- "${SERVE_PATHS[@]}"` means NO pathspec, i.e.
# every file in the repo. So a broken derivation did not fail closed; it either
# passed green (go absent from PATH) or flagged unrelated files. Both observed
# before this was fixed, in the very story that exists to stop a silent gate.
if ! paths_raw=$(serve_paths); then
  echo "check-serve-version: refusing to run a gate whose watch set could not be derived" >&2
  exit "$CANNOT_ANSWER"
fi
mapfile -t SERVE_PATHS <<<"$paths_raw"
if [ "${#SERVE_PATHS[@]}" -eq 0 ] || [ -z "${SERVE_PATHS[0]}" ]; then
  echo "check-serve-version: derived an EMPTY watch set — refusing to run a gate that watches nothing" >&2
  exit "$CANNOT_ANSWER"
fi

case "${1:-}" in
--paths)
  printf '%s\n' "${SERVE_PATHS[@]}"
  exit 0
  ;;
--check-path)
  shift
  [ $# -gt 0 ] && [ -n "$1" ] || { echo "check-serve-version: --check-path needs a path" >&2; exit 2; }
  # One derivation answers every path: callers batch rather than re-derive per
  # path (sto_54770db7).
  miss=0
  for target in "$@"; do
    hit=0
    for p in "${SERVE_PATHS[@]}"; do
      case "$target" in "$p"*) hit=1; break ;; esac
    done
    if [ "$hit" -eq 0 ]; then
      printf '%s\n' "$target"
      miss=1
    fi
  done
  exit "$miss"
  ;;
"") ;;
*)
  echo "check-serve-version: unknown argument $1 (want --paths or --check-path <path>...)" >&2
  exit 2
  ;;
esac

# Remote-baseline verification (sty_da6c3874). Runs after --paths/--check-path
# so those modes stay offline, and before BASE is used so a stale or
# unreachable baseline never produces a verdict. Never fetches.
REMOTE="${SERVE_TAG_REMOTE:-origin}"
LOCAL=$(git tag -l 'serve-v*' --sort=-v:refname | head -1 || true)
set +e
REMOTE_LS=$(GIT_TERMINAL_PROMPT=0 git ls-remote --tags --refs "$REMOTE" 'serve-v*' 2>&1)
REMOTE_LS_STATUS=$?
set -e
if [ "$REMOTE_LS_STATUS" -ne 0 ]; then
  REMOTE_REASON=$(printf '%s' "$REMOTE_LS" | tr '\n' ' ' | sed 's/[[:space:]]*$//')
  [ -n "$REMOTE_REASON" ] || REMOTE_REASON="ls-remote exited $REMOTE_LS_STATUS"
  echo "check-serve-version: cannot verify the serve-tag baseline against $REMOTE ($REMOTE_REASON) — refusing to answer from the local tag list alone" >&2
  exit 1
fi
REMOTE_NEWEST=$(printf '%s\n' "$REMOTE_LS" | awk '{print $2}' | sed 's|^refs/tags/||' | sort -V | tail -1)
if [ -n "$REMOTE_NEWEST" ]; then
  if [ -z "$LOCAL" ]; then
    echo "check-serve-version: local newest serve tag is none but $REMOTE has $REMOTE_NEWEST — baseline is stale; run: git fetch --tags origin" >&2
    exit 1
  fi
  # Same sort for both sides. If the version-sort max is the remote tag and
  # the names differ, local is behind — refuse. If local is newer (unpushed
  # local tag), continue with the local tag: it is at least as fresh, and
  # refusing would block a legitimate pre-push state.
  NEWEST=$(printf '%s\n' "$LOCAL" "$REMOTE_NEWEST" | sort -V | tail -1)
  if [ "$NEWEST" = "$REMOTE_NEWEST" ] && [ "$LOCAL" != "$REMOTE_NEWEST" ]; then
    echo "check-serve-version: local newest serve tag is $LOCAL but $REMOTE has $REMOTE_NEWEST — baseline is stale; run: git fetch --tags origin" >&2
    exit 1
  fi
fi

# Latest serve-v* tag (not latest CLI tag).
BASE=$(git tag -l 'serve-v*' --sort=-v:refname | head -1 || true)
if [ -z "$BASE" ]; then
  echo "check-serve-version: no serve-v* tag yet — ok (first serve release uses current .version)"
  exit 0
fi
# A tag whose commit is absent is a SHALLOW clone, not a first release. Without
# that commit there is no baseline: the diff yields nothing and the tagged
# .version cannot be read, so every check below would report ok having compared
# against nothing. Refuse instead — a gate that cannot establish a baseline must
# not answer "fine" (sty_a8853e85). CI fetches full history for this reason.
if ! git cat-file -e "${BASE}^{commit}" 2>/dev/null; then
  echo "check-serve-version: tag $BASE exists but its commit is not in this clone (shallow checkout)" >&2
  echo "check-serve-version: refusing to report a verdict with no baseline — fetch full history (git fetch --unshallow --tags)" >&2
  exit 1
fi

changed=$(git diff --name-only "${BASE}..HEAD" -- "${SERVE_PATHS[@]}" 2>/dev/null || true)
# Also count unstaged/staged worktree changes on serve paths (pre-commit style).
wt=$(git diff --name-only HEAD -- "${SERVE_PATHS[@]}" 2>/dev/null || true)
cached=$(git diff --name-only --cached -- "${SERVE_PATHS[@]}" 2>/dev/null || true)
# …and files that do not exist yet in git. `git diff` cannot see an untracked
# file, and the release path runs this check BEFORE staging, so a brand-new
# source file in a watched package would otherwise sail through — the same
# looks-covered-but-is-not hole as the old narrow path list (sty_a8853e85).
untracked=$(git ls-files --others --exclude-standard -- "${SERVE_PATHS[@]}" 2>/dev/null || true)
all=$(printf '%s\n%s\n%s\n%s\n' "$changed" "$wt" "$cached" "$untracked" |
  grep -v '^$' |
  # A _test.go file is compiled into `go test`, never into the shipped binary,
  # so it cannot make a running service stale. Demanding a serve release for one
  # is a false positive, and with untracked files now counted it would be a
  # frequent one. Only *_test.go — testdata can be embedded, so it stays watched.
  grep -v '_test\.go$' |
  sort -u || true)

if [ -z "$all" ]; then
  echo "check-serve-version: no serve-path changes since $BASE"
  exit 0
fi

# Version at BASE vs HEAD (and working tree .version).
# HEAD uses satelled.version. Old tags still carry satelle-serve.version —
# compatibility fallback, not a current name (sty_bd9de06d).
ver_field() {
  awk '
    $1=="satelled.version:" { print $2; found=1; exit }
    $1=="satelle-serve.version:" { legacy=$2 }
    END { if (!found && legacy != "") print legacy }
  ' "$@"
}
ver_at() {
  git show "$1:.version" 2>/dev/null | ver_field
}
base_ver=$(ver_at "$BASE")
head_ver=$(ver_field .version)
if [ -z "$head_ver" ]; then
  echo "check-serve-version: satelled.version missing from .version" >&2
  exit 1
fi
if [ -z "$base_ver" ]; then
  echo "check-serve-version: tag $BASE has no satelled.version (or legacy satelle-serve.version) — require current $head_ver present"
  exit 0
fi
if [ "$head_ver" = "$base_ver" ]; then
  echo "check-serve-version: serve-path changes since $BASE but satelled.version still $base_ver" >&2
  echo "changed:" >&2
  printf '  %s\n' $all >&2
  echo "bump satelled.version in .version (and changelog serve-v entry) before release" >&2
  exit 1
fi
echo "check-serve-version: ok — serve-path changed since $BASE; version $base_ver → $head_ver"
