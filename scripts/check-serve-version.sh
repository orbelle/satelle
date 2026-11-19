#!/usr/bin/env bash
set -euo pipefail

CANNOT_ANSWER=3

ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "check-serve-version: not a git repo" >&2
  exit "$CANNOT_ANSWER"
}
cd "$ROOT"

GO_ERR=$(mktemp)
trap 'rm -f "$GO_ERR"' EXIT

go_reason() {
  tr '\n' ' ' <"$GO_ERR" | sed 's/[[:space:]]*$//'
}

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
  out=$(printf '%s\n' "$deps" | sed -n "s|^${mod}/||p" | sort -u | sed 's|$|/|')
  [ -n "$out" ] || {
    echo "check-serve-version: derived an EMPTY watch set" >&2
    return 1
  }
  printf '%s\n' "$out"
}

if [ "${1:-}" = --files ]; then
  shift
  [ $# -gt 0 ] && [ -n "$1" ] || { echo "check-serve-version: --files needs a package" >&2; exit 2; }
  moddir=$(go list -m -f '{{.Dir}}' 2>"$GO_ERR") || {
    echo "check-serve-version: go list -m failed — cannot resolve the module directory: $(go_reason)" >&2
    exit "$CANNOT_ANSWER"
  }
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

if ! paths_raw=$(serve_paths); then
  echo "check-serve-version: refusing to run a gate whose watch set could not be derived" >&2
  exit "$CANNOT_ANSWER"
fi
SERVE_PATHS=()
while IFS= read -r p; do SERVE_PATHS+=("$p"); done <<<"$paths_raw"
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
  NEWEST=$(printf '%s\n' "$LOCAL" "$REMOTE_NEWEST" | sort -V | tail -1)
  if [ "$NEWEST" = "$REMOTE_NEWEST" ] && [ "$LOCAL" != "$REMOTE_NEWEST" ]; then
    echo "check-serve-version: local newest serve tag is $LOCAL but $REMOTE has $REMOTE_NEWEST — baseline is stale; run: git fetch --tags origin" >&2
    exit 1
  fi
fi

BASE=$(git tag -l 'serve-v*' --sort=-v:refname | head -1 || true)
if [ -z "$BASE" ]; then
  echo "check-serve-version: no serve-v* tag yet — ok (first serve release uses current .version)"
  exit 0
fi
if ! git cat-file -e "${BASE}^{commit}" 2>/dev/null; then
  echo "check-serve-version: tag $BASE exists but its commit is not in this clone (shallow checkout)" >&2
  echo "check-serve-version: refusing to report a verdict with no baseline — fetch full history (git fetch --unshallow --tags)" >&2
  exit 1
fi

changed=$(git diff --name-only "${BASE}..HEAD" -- "${SERVE_PATHS[@]}" 2>/dev/null || true)
wt=$(git diff --name-only HEAD -- "${SERVE_PATHS[@]}" 2>/dev/null || true)
cached=$(git diff --name-only --cached -- "${SERVE_PATHS[@]}" 2>/dev/null || true)
untracked=$(git ls-files --others --exclude-standard -- "${SERVE_PATHS[@]}" 2>/dev/null || true)
all=$(printf '%s\n%s\n%s\n%s\n' "$changed" "$wt" "$cached" "$untracked" |
  grep -v '^$' |
  grep -v '_test\.go$' |
  sort -u || true)

if [ -z "$all" ]; then
  echo "check-serve-version: no serve-path changes since $BASE"
  exit 0
fi

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
