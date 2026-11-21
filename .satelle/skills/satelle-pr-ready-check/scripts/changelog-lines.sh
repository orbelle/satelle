#!/usr/bin/env bash
set -uo pipefail

sections="Added Changed Deprecated Removed Fixed Security Breaking"

if [ $# -ne 1 ] || [ -z "$1" ]; then
  echo "usage: changelog-lines.sh <id>" >&2
  exit 2
fi

awk -v id="$1" -v sections="$sections" '
  function refuse(msg) { printf "changelog attachment line %d: %s: %s\n", NR, msg, $0 > "/dev/stderr"; bad = 1; exit 1 }
  BEGIN { n = split(sections, S, " "); for (i = 1; i <= n; i++) ok[S[i]] = 1; suffix = id ")" }
  { sub(/\r$/, "") }
  /^[[:space:]]*$/ { next }
  /^### / {
    s = substr($0, 5); sub(/[[:space:]]+$/, "", s)
    if (!(s in ok)) refuse("unknown section (want ### " sections ")")
    cur = s; next
  }
  /^-([[:space:]]|$)/ {
    if (cur == "") refuse("an entry before any ### heading")
    text = substr($0, 2); sub(/^[[:space:]]+/, "", text); sub(/[[:space:]]+$/, "", text)
    if (text == "") refuse("an empty entry")
    if (length(text) < length(suffix) || substr(text, length(text) - length(suffix) + 1) != suffix) text = text " (" id ")"
    got[++entries] = cur "\t- " text; next
  }
  { refuse("not a ### heading or a single - entry line") }
  END {
    if (bad) exit 1
    if (entries == 0) { print "changelog attachment has no entries: want ### <Section> then one - line per entry" > "/dev/stderr"; exit 1 }
    for (i = 1; i <= entries; i++) print got[i]
  }'
