#!/usr/bin/env bash
set -uo pipefail

if [ $# -ne 2 ] || [ -z "$1" ] || [ -z "$2" ]; then
  echo "usage: notes-paths.sh <base> <head>" >&2
  exit 2
fi
base=$1 head=$2

diff=$(git diff --no-renames --name-status "$base...$head" -- CHANGELOG.md docs/releases) || { echo "git diff $base...$head failed" >&2; exit 1; }
cur=$(git show "$base:.version" 2>/dev/null | awk '$1 == "satelle.version:" { print $2; exit }')
while IFS=$'\t' read -r st p; do
  [ -n "$p" ] || continue
  case "$st:$p" in
    D:CHANGELOG.md) continue ;;
    A:docs/releases/v*.md)
      v=${p#docs/releases/v}
      v=${v%.md}
      if [ -n "$cur" ] && [[ $v =~ ^[0-9]+(\.[0-9]+)*$ ]] && [ "$v" != "$cur" ] &&
        [ "$(printf '%s\n%s\n' "$v" "$cur" | sort -V | head -n 1)" = "$v" ] &&
        ! git cat-file -e "$base:$p" 2>/dev/null; then
        continue
      fi
      ;;
  esac
  printf '%s\n' "$p"
done <<<"$diff"
