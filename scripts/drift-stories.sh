#!/usr/bin/env bash
# drift-stories — raise one story per drift finding (.satelle/skills/drift-review.md,
# step 2). It never fixes anything: the stories are the fix.
#
#   drift-stories.sh <version> < misses
#
# Each stdin line is a finding in drift-check.sh's form,
# `<file>:<line>: <kind>: <subject> — <why>`. Kinds command, flag, topic,
# link, skill, config and prose (substrate that no longer matches the binary)
# belong to the open epic tagged epic:hygiene; kind constitution (a release's
# binary change against the constitution) to the open epic tagged
# epic:constitution. This is the one place that maps a kind to its epic. An
# epic is the open item that carries both its epic:<name> tag and the bare
# epic tag (its children carry epic:<name> too); unless exactly one resolves
# for every kind on stdin the script exits 2, naming the tag, before it
# creates any story.
#
# The finding's key is `drift-key: <kind>:<file>:<subject>` (no line number:
# lines shift between releases). A finding whose key is in the body of an open
# story tagged drift is not raised again. Output, one line per distinct key:
#   <key>\t<id>\texisting     an open drift story already carries it
#   <key>\t<id>\tnew          a story was raised: title `Drift: <subject> (<file>)`,
#                             tags drift, release:v<version> and the epic's tag,
#                             parent the epic, category chore for a finding in
#                             .satelle/ that is not kind constitution
# Exit 0 when every finding is carried, 2 when it cannot raise (a tool or an
# epic is missing, a line is not a finding, a create fails).
set -uo pipefail

ver=${1:-}
[ -n "$ver" ] || { echo "usage: drift-stories.sh <version> < misses" >&2; exit 2; }
ver=${ver#v}
bin=${SATELLE_BIN:-satelle}
for tool in "$bin" jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "drift-stories: $tool is not on PATH" >&2; exit 2; }
done

tmp=$(mktemp -d) || exit 2
trap 'rm -rf "$tmp"' EXIT

# Parse every finding first: kind, file, subject, the line itself.
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
  case $num in '' | *[!0-9]*) echo "drift-stories: not a finding: $line" >&2; exit 2 ;; esac
  case $kind in
    constitution) epic=epic:constitution ;;
    command | flag | topic | link | skill | config | prose) epic=epic:hygiene ;;
    *) echo "drift-stories: unknown kind $kind in: $line" >&2; exit 2 ;;
  esac
  printf '%s\t%s\t%s\t%s\t%s\n' "$kind:$file:$subject" "$epic" "$file" "$subject" "$line" >> "$tmp/findings"
done
[ -s "$tmp/findings" ] || exit 0

# Resolve each epic a finding needs, before anything is created.
: > "$tmp/epics"
for tag in $(cut -f2 "$tmp/findings" | sort -u); do
  ids=$("$bin" story list --tag "$tag" --open --json | jq -r '[.[] | select(any(.tags[]?; . == "epic"))] | .[].id') || { echo "drift-stories: satelle story list --tag $tag failed" >&2; exit 2; }
  n=$(printf '%s' "$ids" | grep -c . || true)
  if [ "$n" -ne 1 ]; then
    echo "drift-stories: $n open epics carry the tags epic and $tag; want exactly one (create it, or close the extra)" >&2
    exit 2
  fi
  printf '%s\t%s\n' "$tag" "$ids" >> "$tmp/epics"
done

"$bin" story list --tag drift --open --json > "$tmp/open" || { echo "drift-stories: satelle story list --tag drift failed" >&2; exit 2; }

: > "$tmp/seen"
while IFS=$'\t' read -r key tag file subject line; do
  grep -qxF "$key" "$tmp/seen" && continue
  printf '%s\n' "$key" >> "$tmp/seen"
  id=$(jq -r --arg k "drift-key: $key" '[.[] | select((.body // "") | split("\n") | any(. == $k))][0].id // empty' "$tmp/open")
  if [ -n "$id" ]; then
    printf '%s\t%s\texisting\n' "$key" "$id"
    continue
  fi
  parent=$(awk -F'\t' -v t="$tag" '$1 == t { print $2 }' "$tmp/epics")
  kind=${key%%:*}
  cat=()
  case $file in .satelle/*) [ "$kind" != constitution ] && cat=(--category chore) ;; esac
  body=$(printf 'drift-key: %s\n\n%s\n\nraised by the drift review of v%s\n' "$key" "$line" "$ver")
  id=$(printf '%s\n' "$body" | "$bin" story create --title "Drift: $subject ($file)" \
    --tag drift --tag "release:v$ver" --tag "$tag" --parent "$parent" "${cat[@]}" --body - --json | jq -r '.id // empty')
  [ -n "$id" ] || { echo "drift-stories: creating the story for $key failed" >&2; exit 2; }
  printf '%s\t%s\tnew\n' "$key" "$id"
done < "$tmp/findings"
exit 0
