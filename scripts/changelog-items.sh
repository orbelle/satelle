#!/usr/bin/env bash
set -uo pipefail

sections="Added Changed Deprecated Removed Fixed Security Breaking"

usage() {
  echo "usage: changelog-items.sh lines <id> | render <ver> <date> <id>:<file>... | write <path> <ver> <date> <id>:<file>... | has <changelog> <ver> <id>:<file>" >&2
  exit 2
}

lines() {
  [ $# -eq 1 ] && [ -n "$1" ] || usage
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
}

item_lines() {
  local id=${1%%:*} f=${1#*:}
  [ "$id" != "$1" ] && [ -n "$id" ] || { echo "want <id>:<file>, got $1" >&2; return 1; }
  [ -r "$f" ] || { echo "$id: cannot read $f" >&2; return 1; }
  lines "$id" <"$f"
}

render() {
  [ $# -ge 3 ] || usage
  local ver=$1 date=$2 all="" out a s body
  shift 2
  for a in "$@"; do
    out=$(item_lines "$a") || return 1
    all+="$out"$'\n'
  done
  printf '## [%s] - %s\n\n' "$ver" "$date"
  for s in $sections; do
    body=$(printf '%s' "$all" | awk -F'\t' -v s="$s" '$1 == s { print substr($0, length(s) + 2) }')
    [ -n "$body" ] && printf '### %s\n%s\n\n' "$s" "$body"
  done
  return 0
}

write() {
  [ $# -ge 4 ] || usage
  local path=$1 out
  shift
  [ -e "$path" ] && { echo "$path exists: a release never rewrites another version's notes" >&2; return 1; }
  out=$(render "$@") || return 1
  mkdir -p "$(dirname "$path")" || return 1
  printf '%s\n' "$out" >"$path.tmp.$$" && mv "$path.tmp.$$" "$path" || { rm -f "$path.tmp.$$"; echo "cannot write $path" >&2; return 1; }
}

has() {
  [ $# -eq 3 ] || usage
  local log=$1 ver=$2 out
  [ -r "$log" ] || { echo "cannot read $log" >&2; return 1; }
  out=$(item_lines "$3") || return 1
  printf '%s\n' "$out" | awk -F'\t' -v ver="$ver" -v file="$log" '
    BEGIN {
      head = "## [" ver "] - "
      while ((getline l < file) > 0) {
        sub(/\r$/, "", l)
        if (substr(l, 1, 4) == "## [") { if (insec) break; insec = (substr(l, 1, length(head)) == head); found = found || insec; continue }
        if (!insec) continue
        if (substr(l, 1, 4) == "### ") { cur = substr(l, 5); sub(/[[:space:]]+$/, "", cur); continue }
        have[cur "\t" l] = 1
      }
      if (!found) { printf "%s has no \"%s<date>\" section\n", file, head > "/dev/stderr"; bad = 1; exit 1 }
    }
    !(($1 "\t" substr($0, length($1) + 2)) in have) {
      printf "the [%s] section has no \"%s\" under ### %s\n", ver, substr($0, length($1) + 2), $1 > "/dev/stderr"; bad = 1; exit 1
    }
    END { if (bad) exit 1 }'
}

cmd=${1:-}
[ $# -gt 0 ] && shift
case "$cmd" in
  lines) lines "$@" ;;
  render) render "$@" ;;
  write) write "$@" ;;
  has) has "$@" ;;
  *) usage ;;
esac
