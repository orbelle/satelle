#!/usr/bin/env bash
set -uo pipefail

usage() {
  echo "usage: release-targets.sh list <release.yml> | build <dir>" >&2
  exit 2
}

list() {
  [ $# -eq 1 ] && [ -n "$1" ] || usage
  local yml=$1
  [ -f "$yml" ] || { echo "release.yml is missing" >&2; exit 1; }
  local entries
  entries=$(awk '
    /^[[:space:]]*include:[[:space:]]*$/ { on = 1; next }
    on && /^[[:space:]]*(#.*)?$/ { next }
    on && /^[[:space:]]*-[[:space:]]*\{.*\}[[:space:]]*$/ { print; next }
    on { on = 0 }' "$yml")
  if [ -z "$entries" ]; then
    echo "release.yml has no matrix include entry this script reads (- { pkg: ..., goos: ..., goarch: ... })" >&2
    exit 1
  fi
  local e p o a
  field() { printf '%s\n' "$1" | sed -n "s/.*[{,][[:space:]]*$2:[[:space:]]*\\([^,}[:space:]]*\\).*/\\1/p"; }
  while IFS= read -r e; do
    p=$(field "$e" pkg); o=$(field "$e" goos); a=$(field "$e" goarch)
    if [ -z "$p" ] || [ -z "$o" ] || [ -z "$a" ]; then
      echo "a release.yml include entry lacks pkg, goos or goarch: $(printf '%s' "$e" | sed 's/^[[:space:]]*//')" >&2
      exit 1
    fi
    printf '%s %s %s\n' "$p" "$o" "$a"
  done <<<"$entries" | awk '!seen[$0]++'
}

build() {
  [ $# -eq 1 ] && [ -n "$1" ] || usage
  local dir=$1 yml=$1/.github/workflows/release.yml
  local targets cgo
  targets=$(list "$yml") || exit 1
  cgo=$(sed -n "s/^[[:space:]]*CGO_ENABLED:[[:space:]]*['\"]\\{0,1\\}\\([0-9]\\)['\"]\\{0,1\\}[[:space:]]*\$/\\1/p" "$yml" | head -1)
  [ -n "$cgo" ] || { echo "release.yml sets no CGO_ENABLED this script reads" >&2; exit 1; }
  local p o a out failed=0
  while read -r p o a; do
    if out=$(cd "$dir" && CGO_ENABLED=$cgo GOOS=$o GOARCH=$a go build -o /dev/null "$p" 2>&1); then
      echo "release target $o/$a builds $p"
    else
      echo "release target $o/$a does not build $p:"
      printf '%s\n' "$out" | sed 's/^/  /'
      failed=1
    fi
  done <<<"$targets"
  exit "$failed"
}

case "${1:-}" in
  list) shift; list "$@" ;;
  build) shift; build "$@" ;;
  *) usage ;;
esac
