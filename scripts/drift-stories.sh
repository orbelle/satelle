#!/usr/bin/env bash
# drift-stories — raise one story per drift finding (.satelle/skills/drift-review.md,
# step 2). It never fixes anything: the stories are the fix.
#
#   drift-stories.sh <version> < misses
#   drift-stories.sh --reshape
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
#
# A raised story is in the story shape (.satelle/principles/satelle-story-shape.md).
# Its body is the drift-key line, the role the finding affects (`Who is
# affected: ...`, by kind: the agent or operator following the substrate file;
# the reader of the principle for prose; the maintainer of the Go file for
# constitution), the finding quoted, and the release that raised it. Its one
# criterion quotes the finding and names the proof that it is fixed:
# drift-check.sh no longer prints the drift-key (a substrate kind), or the next
# drift run's judge, drift-claims (prose) or drift-constitution
# (constitution), no longer raises it.
#
# --reshape rewrites the open drift stories raised before that shape: each in
# backlog gets the new body and its criteria replaced. Editing the criteria of
# an item past backlog would send it back to plan, so those are left. Output,
# one line per open drift story:
#   <key>\t<id>\treshaped     body and criteria rewritten
#   <key>\t<id>\talready      it already carries the role line
#   <key>\t<id>\theld:<state> past backlog; not touched
# Exit 0 when every finding is carried, 2 when it cannot raise (a tool or an
# epic is missing, a line is not a finding, a create or set fails).
set -uo pipefail

bin=${SATELLE_BIN:-satelle}

# role <kind> <file>: who the finding affects.
role() {
  case $1 in
    prose) printf 'the reader of the principle %s, whose statement no longer matches the released source' "$2" ;;
    constitution) printf 'the maintainer of the Go file %s, whose change departs from the constitution' "$2" ;;
    *) printf 'the agent or operator following %s, which no longer matches the released binary' "$2" ;;
  esac
}

# proof <kind> <key> <finding>: the story's criterion, quoting the finding and
# naming the check that proves it fixed.
proof() {
  local check
  case $1 in
    prose) check="the next drift run's drift-claims judge no longer raises drift-key \`$2\`" ;;
    constitution) check="the next drift run's drift-constitution judge no longer raises drift-key \`$2\`" ;;
    *) check="scripts/drift-check.sh no longer prints drift-key \`$2\`" ;;
  esac
  printf 'The finding "%s" is fixed: %s' "$3" "$check"
}

# body <key> <finding> <raised-by line>: the story body. The drift-key line
# stays first and verbatim: dedup reads it.
body() {
  local kind=${1%%:*} rest=${1#*:}
  printf 'drift-key: %s\n\nWho is affected: %s.\n\nThe finding, as the drift review reported it:\n\n> %s\n\n%s\n' \
    "$1" "$(role "$kind" "${rest%%:*}")" "$2" "$3"
}

for tool in "$bin" jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "drift-stories: $tool is not on PATH" >&2; exit 2; }
done

tmp=$(mktemp -d) || exit 2
trap 'rm -rf "$tmp"' EXIT

if [ "${1:-}" = --reshape ]; then
  "$bin" story list --tag drift --open --json > "$tmp/open" || { echo "drift-stories: satelle story list --tag drift failed" >&2; exit 2; }
  jq -r '.[] | select((.body // "") | startswith("drift-key: ")) | .id' "$tmp/open" > "$tmp/ids"
  while IFS= read -r id; do
    jq -r --arg id "$id" '.[] | select(.id == $id) | .body' "$tmp/open" > "$tmp/body"
    key=$(sed -n '1s/^drift-key: //p' "$tmp/body")
    status=$(jq -r --arg id "$id" '.[] | select(.id == $id) | .status' "$tmp/open")
    if grep -q '^Who is affected: ' "$tmp/body"; then
      printf '%s\t%s\talready\n' "$key" "$id"
      continue
    fi
    if [ "$status" != backlog ]; then
      printf '%s\t%s\theld:%s\n' "$key" "$id" "$status"
      continue
    fi
    kind=${key%%:*}
    rest=${key#*:}
    file=${rest%%:*}
    line=$(awk -v p="$file:" -v k=": $kind: " 'index($0, p) == 1 && index($0, k) { print; exit }' "$tmp/body")
    raisedby=$(grep -m1 '^raised by the drift review of ' "$tmp/body")
    [ -n "$line" ] && [ -n "$raisedby" ] || { echo "drift-stories: $id: no finding or raised-by line in its body" >&2; exit 2; }
    body "$key" "$line" "$raisedby" | "$bin" story set "$id" --body - \
      --criteria "$(proof "$kind" "$key" "$line")" --replace > /dev/null \
      || { echo "drift-stories: reshaping $id failed" >&2; exit 2; }
    printf '%s\t%s\treshaped\n' "$key" "$id"
  done < "$tmp/ids"
  exit 0
fi

ver=${1:-}
[ -n "$ver" ] || { echo "usage: drift-stories.sh <version> < misses | drift-stories.sh --reshape" >&2; exit 2; }
ver=${ver#v}

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
  id=$(body "$key" "$line" "raised by the drift review of v$ver" | "$bin" story create --title "Drift: $subject ($file)" \
    --tag drift --tag "release:v$ver" --tag "$tag" --parent "$parent" "${cat[@]}" \
    --criteria "$(proof "$kind" "$key" "$line")" --body - --json | jq -r '.id // empty')
  [ -n "$id" ] || { echo "drift-stories: creating the story for $key failed" >&2; exit 2; }
  printf '%s\t%s\tnew\n' "$key" "$id"
done < "$tmp/findings"
exit 0
