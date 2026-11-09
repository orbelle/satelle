#!/usr/bin/env bash
# drift-fix — raise one drift-fix item per release listing every finding of
# that release's drift review (.satelle/skills/drift-review.md, steps 2 to 6),
# and close the older drift items it carries. Drift is fixed like an upgrade:
# one item per release, not one story per finding. It never fixes anything.
#
#   drift-fix.sh <version> < findings
#   drift-fix.sh --keys < findings
#   drift-fix.sh --open-findings
#   drift-fix.sh --close <fix id|->
#
# Each stdin line is a finding in drift-check.sh's form,
# `<file>:<line>: <kind>: <subject> — <why>`. Kinds command, flag, topic,
# link, skill, config (the substrate against the binary) and constitution (a
# release's binary change against the constitution) are findings; kind prose
# (a principle's statement the source does not implement, the drift-claims
# judge's WARN lines) is a warning, which never blocks. Any other kind, or a
# line that is not a finding, exits 2 before anything is written. A finding's
# key is `<kind>:<file>:<subject>` (no line number: lines shift between
# releases); this is the one place a finding maps to its key.
#
# <version> creates, or on a rerun rewrites the body of, the one open story
# tagged drift-fix and release:v<version>: title `Drift fix: v<version>`, tags
# drift, drift-fix and release:v<version>, category chore, no parent (the
# satelle.toml drift rule puts it on the drift workflow). Its body lists one
# `drift-key: <key>` line per distinct finding under `## Findings` and one
# `drift-warning: <key>` line per distinct warning under `## Warnings`, each
# followed by `> <the finding>`; the first line for a key wins. Its one
# criterion says every drift-key is fixed and warnings never hold it open. A
# rerun replaces the body with this input's findings and leaves the criterion.
# Empty input prints nothing, creates nothing and exits 0. More than one open
# fix item for the version exits 2. Output, one line:
#   <id>\tnew|updated\t<n> findings\t<m> warnings
#
# --keys prints the key of each distinct finding on stdin, one per line, and
# writes nothing; the drift workflow's done gate
# (.satelle/skills/satelle-drift-resolved-check.md) reads findings through it.
#
# --open-findings prints, for every open story tagged drift (older
# per-finding drift stories and fix items alike), one
# `<id>\t<finding line>` per `drift-key:` or `drift-warning:` body line: the
# first later body line (a `> ` quote stripped) whose key is that key, else a
# line rebuilt as `<file>:1: <kind>: <subject> — carried from <id>` (the same
# key: keys carry no line number). An open drift story with no such line
# yields `<id>:1: prose: <title> — carried from <id>`, a warning whose key
# names the item. Fed to `drift-fix.sh <version>`, it carries every open
# finding into that release's fix item.
#
# --close <fix id> acts on every open story tagged drift except the fix item.
# One not on the drift workflow is first reselected onto it (satelle story set
# --reselect drift). When its workflow has moved or judged it (the reselect
# exits 2) it keeps that route, its owner: its drift tag is swapped for
# drift-carried-by:<fix id>. Every other story gets the tag
# drift-carried-by:<fix id> and is presented done; the drift workflow's
# drift-resolved gate accepts a key the fix item carries, or one the
# in-progress drift run no longer finds. A refused story loses the tag again.
# `-` for the fix id closes only on the drift run's evidence and carries
# nothing. No story is cancelled. Output, one line per story:
#   <id>\tclosed
#   <id>\tcarried\ton <workflow>/<state>
#   <id>\trefused\t<the refusal's first line>
# Exit 0 when every story was closed or carried, 1 when any was refused, 2
# when a tool, a list or a write fails.
set -uo pipefail

bin=${SATELLE_BIN:-satelle}

tmp=$(mktemp -d) || exit 2
trap 'rm -rf "$tmp"' EXIT

# parse_findings: every stdin finding, parsed once into $tmp/findings, one
# line each: key, class (finding|warning), the finding. Returns 1 (naming the
# line) on a line that is not a finding or carries an unknown kind.
parse_findings() {
  local line file rest num kind subject class
  : > "$tmp/findings"
  while IFS= read -r line || [ -n "$line" ]; do
    [ -n "$line" ] || continue
    file=${line%%:*}
    rest=${line#*:}
    num=${rest%%:*}
    rest=${rest#*: }
    kind=${rest%%: *}
    rest=${rest#*: }
    subject=${rest%% — *}
    case $num in '' | *[!0-9]*) echo "drift-fix: not a finding: $line" >&2; return 1 ;; esac
    case $kind in
      command | flag | topic | link | skill | config | constitution) class=finding ;;
      prose) class=warning ;;
      *) echo "drift-fix: unknown kind $kind in: $line" >&2; return 1 ;;
    esac
    printf '%s\t%s\t%s\n' "$kind:$file:$subject" "$class" "$line" >> "$tmp/findings"
  done
}

if [ "${1:-}" = --keys ]; then
  parse_findings || exit 2
  cut -f1 "$tmp/findings" | awk '!seen[$0]++'
  exit 0
fi

for tool in "$bin" jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "drift-fix: $tool is not on PATH" >&2; exit 2; }
done

# keyof <line>: the key of one finding line, or nothing.
keyof() {
  local file rest kind
  file=${1%%:*}
  rest=${1#*:}
  case ${rest%%:*} in '' | *[!0-9]*) return 0 ;; esac
  rest=${rest#*: }
  kind=${rest%%: *}
  rest=${rest#*: }
  printf '%s:%s:%s' "$kind" "$file" "${rest%% — *}"
}

if [ "${1:-}" = --open-findings ]; then
  "$bin" story list --tag drift --open --json > "$tmp/open" || { echo "drift-fix: satelle story list --tag drift failed" >&2; exit 2; }
  jq -r '.[].id' "$tmp/open" > "$tmp/ids"
  while IFS= read -r id; do
    jq -r --arg id "$id" '.[] | select(.id == $id) | .body // ""' "$tmp/open" > "$tmp/body"
    n=0
    while IFS= read -r l; do
      case $l in
        'drift-key: '*) key=${l#drift-key: } ;;
        'drift-warning: '*) key=${l#drift-warning: } ;;
        *) continue ;;
      esac
      n=$((n + 1))
      at=$(grep -nxF -- "$l" "$tmp/body" | head -1 | cut -d: -f1)
      found=""
      while IFS= read -r c; do
        c=${c#> }
        if [ "$(keyof "$c")" = "$key" ]; then found=$c; break; fi
      done < <(tail -n +"$((at + 1))" "$tmp/body")
      if [ -z "$found" ]; then
        kind=${key%%:*}
        rest=${key#*:}
        found="${rest%%:*}:1: $kind: ${rest#*:} — carried from $id"
      fi
      printf '%s\t%s\n' "$id" "$found"
    done < "$tmp/body"
    if [ "$n" -eq 0 ]; then
      title=$(jq -r --arg id "$id" '.[] | select(.id == $id) | .title' "$tmp/open" | tr '\t' ' ')
      printf '%s\t%s:1: prose: %s — carried from %s\n' "$id" "$id" "$title" "$id"
    fi
  done < "$tmp/ids"
  exit 0
fi

if [ "${1:-}" = --close ]; then
  fix=${2:-}
  [ -n "$fix" ] || { echo "usage: drift-fix.sh --close <fix id|->" >&2; exit 2; }
  "$bin" story list --tag drift --open --json > "$tmp/open" || { echo "drift-fix: satelle story list --tag drift failed" >&2; exit 2; }
  jq -r --arg fix "$fix" '.[] | select(.id != $fix) | [.id, .workflow] | @tsv' "$tmp/open" > "$tmp/ids"
  refused=0
  # Pre-pass: put each story onto the drift workflow, or leave it on the route
  # that moved or judged it, carried.
  : > "$tmp/close"
  while IFS=$'\t' read -r id wf <&3; do
    if [ "$wf" = drift ]; then
      printf '%s\n' "$id" >> "$tmp/close"
      continue
    fi
    "$bin" story set "$id" --reselect drift > /dev/null 2> "$tmp/err"
    code=$?
    if [ "$code" -eq 0 ]; then
      printf '%s\n' "$id" >> "$tmp/close"
      continue
    fi
    [ "$code" -eq 2 ] || { echo "drift-fix: reselecting $id failed: $(head -n1 "$tmp/err")" >&2; exit 2; }
    state=$(jq -r --arg id "$id" '.[] | select(.id == $id) | .workflow + "/" + .status' "$tmp/open")
    if [ "$fix" = - ]; then
      printf '%s\trefused\ton %s, which moved or judged it; no fix item carries it\n' "$id" "$state"
      refused=1
      continue
    fi
    "$bin" story set "$id" --remove-tag drift --add-tag "drift-carried-by:$fix" > /dev/null \
      || { echo "drift-fix: retagging $id drift-carried-by:$fix failed" >&2; exit 2; }
    printf '%s\tcarried\ton %s\n' "$id" "$state"
  done 3< "$tmp/ids"
  while IFS= read -r id <&3; do
    if [ "$fix" != - ]; then
      "$bin" story set "$id" --add-tag "drift-carried-by:$fix" > /dev/null \
        || { echo "drift-fix: tagging $id drift-carried-by:$fix failed" >&2; exit 2; }
    fi
    if out=$("$bin" story set "$id" --status done 2>&1); then
      printf '%s\tclosed\n' "$id"
      continue
    fi
    refused=1
    if [ "$fix" != - ]; then
      "$bin" story set "$id" --remove-tag "drift-carried-by:$fix" > /dev/null \
        || { echo "drift-fix: untagging $id drift-carried-by:$fix failed" >&2; exit 2; }
    fi
    printf '%s\trefused\t%s\n' "$id" "$(printf '%s' "$out" | grep -v '^$' | head -n1 | tr '\t' ' ')"
  done 3< "$tmp/close"
  exit "$refused"
fi

ver=${1:-}
case $ver in '' | -*) echo "usage: drift-fix.sh <version> < findings | drift-fix.sh --keys < findings | drift-fix.sh --open-findings | drift-fix.sh --close <fix id|->" >&2; exit 2 ;; esac
ver=${ver#v}

parse_findings || exit 2
[ -s "$tmp/findings" ] || exit 0
awk -F'\t' '!seen[$1]++' "$tmp/findings" > "$tmp/distinct"

"$bin" story list --tag "release:v$ver" --open --json > "$tmp/open" || { echo "drift-fix: satelle story list --tag release:v$ver failed" >&2; exit 2; }
jq -r '.[] | select(any(.tags[]?; . == "drift-fix")) | .id' "$tmp/open" > "$tmp/fix"
nfix=$(grep -c . "$tmp/fix")
[ "$nfix" -le 1 ] || { echo "drift-fix: $nfix open items carry drift-fix and release:v$ver; want at most one: $(tr '\n' ' ' < "$tmp/fix")" >&2; exit 2; }

# section <class> <line prefix>: the body lines of one section, or none.
section() {
  awk -F'\t' -v c="$1" -v p="$2" '$2 == c { printf "%s%s\n> %s\n\n", p, $1, $3; n++ } END { if (!n) print "none\n" }' "$tmp/distinct"
}
{
  printf 'Drift fix for v%s: every finding the drift review of v%s made, fixed like an upgrade.\n\n' "$ver" "$ver"
  printf 'Who is affected: the agent or operator following the substrate a finding names, and the maintainer of the Go file a constitution finding names.\n\n'
  printf '## Findings\n\n'
  section finding 'drift-key: '
  printf '## Warnings\n\n'
  section warning 'drift-warning: '
  printf 'raised by the drift review of v%s\n' "$ver"
} > "$tmp/body"
nf=$(awk -F'\t' '$2 == "finding"' "$tmp/distinct" | grep -c .)
nw=$(awk -F'\t' '$2 == "warning"' "$tmp/distinct" | grep -c .)

if [ "$nfix" -eq 1 ]; then
  id=$(cat "$tmp/fix")
  "$bin" story set "$id" --body - < "$tmp/body" > /dev/null || { echo "drift-fix: rewriting $id failed" >&2; exit 2; }
  printf '%s\tupdated\t%s findings\t%s warnings\n' "$id" "$nf" "$nw"
  exit 0
fi
criterion='Every drift-key under ## Findings is fixed: a later drift run lists it under No longer found and re-checks it (drift-check no longer prints a substrate key; the drift-constitution judge states RESOLVED for a constitution key), or a later fix item carries it; drift-warning lines never hold this item open'
id=$("$bin" story create --title "Drift fix: v$ver" --tag drift --tag drift-fix --tag "release:v$ver" --category chore \
  --criteria "$criterion" --body - --json < "$tmp/body" | jq -r '.id // empty')
[ -n "$id" ] || { echo "drift-fix: creating the fix item for v$ver failed" >&2; exit 2; }
printf '%s\tnew\t%s findings\t%s warnings\n' "$id" "$nf" "$nw"
exit 0
