---
name: satelle-version-stamp-check
scope: project
type: check
tags: [type:skill, type:reviewer, type:functional-check]
description: Functional-check for release close (release → done), ordered before satelle-story-release-review — rejects unless `.version` on HEAD carries a satelle.version and a satelle.build stamp that both differ from the story's engagement baseline, with the stamp in the release skill's format. Skips an epic child whose parent owns the bump, unless the child is tagged lane:trunk. Deterministic, no LLM; self-contained.
---

# Version-stamp check (release close gate)

**Functional-check** on `release → done`. The release skill bumps
`satelle.version` and sets `satelle.build` to `date -u +"%Y-%m-%d-%H-%M-%S"`.
A missed stamp strands a released binary that reports a stale build. This check
judges that fact from the file, before the release review is dispatched.

The reference is the `.version` at the story's engagement baseline
(`satelle story diff <id> --json` → `engagement_sha`, the first-engagement HEAD
that a resume never moves; `baseline_sha` when the binary has no
`engagement_sha`). Without a baseline it is
the `.version` at the newest `v*` tag that is not HEAD's own version. Both keys
must differ from the reference.

A story whose own commit already shipped in an earlier `v*` tag passes, naming the
commit and tag.

```check
#!/usr/bin/env bash
set -uo pipefail
sd=.satelle
if [ ! -d "$sd" ]; then
  common=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null) && [ -d "$(dirname "$common")/.satelle" ] && sd="$(dirname "$common")/.satelle"
fi
payload=$(cat)
sid=$(printf '%s' "$payload" | grep -oE '"id":[[:space:]]*"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
if [ -z "$sid" ]; then
  echo "could not read the story id from the transition payload"
  exit 1
fi
# Epic child whose parent owns the merge (sty_2e9d0440): the parent release owns
# the version bump, so the child leaves .version untouched.
parent_id=$(satelle story get "$sid" --json 2>/dev/null | grep -oE '"parent_id"[[:space:]]*:[[:space:]]*"sty_[a-f0-9]+"' | head -1 | grep -oE 'sty_[a-f0-9]+')
# A lane:trunk child ships through its own full release (sty_cf79e443), so it is
# judged like a story outside an epic.
if [ -n "$parent_id" ] && ! satelle story get "$sid" --json 2>/dev/null | grep -q '"lane:trunk"'; then
  parent_cat=$(satelle story get "$parent_id" --json 2>/dev/null | grep -oE '"category"[[:space:]]*:[[:space:]]*"[^"]+"' | head -1 | grep -oE '"[^"]+"$' | tr -d '"')
  if [ "$parent_cat" = "epic-parent" ] || [ "$parent_cat" = "parent" ]; then
    if grep -qE '^after_children[[:space:]]*=' "$sd"/workflows/step.toml 2>/dev/null; then
      echo "skipped: epic child $sid of $parent_cat $parent_id; the parent release owns the version bump"
      exit 0
    fi
  fi
fi

key() { awk -v k="$1:" '$1==k {print $2}'; }
head_v=$(git show HEAD:.version 2>/dev/null)
CLI=$(printf '%s\n' "$head_v" | key satelle.version)
BUILD=$(printf '%s\n' "$head_v" | key satelle.build)
if [ -z "$CLI" ] || [ -z "$BUILD" ]; then
  echo "HEAD .version lacks satelle.version or satelle.build"
  exit 1
fi
if ! printf '%s' "$BUILD" | grep -qE '^[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9]{2}$'; then
  echo "satelle.build '$BUILD' is not in the release skill's format (date -u +%Y-%m-%d-%H-%M-%S)"
  exit 1
fi

# Already released (sty_068c5cd7): a story resumed at release after its commit
# shipped in an earlier v* tag has no bump of its own left to make.
for c in $(git log --reverse --format='%H %s' HEAD | grep -F "($sid)" | awk '{print $1}'); do
  shipped=$(git tag --contains "$c" --list 'v*' --sort=v:refname | head -1)
  if [ -n "$shipped" ] && [ "$shipped" != "v$CLI" ]; then
    echo "released: ${c:0:8} for $sid is in $shipped, before HEAD's v$CLI"
    exit 0
  fi
done

ref=""
refname=""
diffjson=$(satelle story diff "$sid" --json 2>/dev/null)
# engagement_sha is the first-engagement HEAD, which a resume never moves; older binaries have only baseline_sha.
base=$(printf '%s' "$diffjson" | grep -oE '"engagement_sha":[[:space:]]*"[a-f0-9]+"' | head -1 | grep -oE '[a-f0-9]{7,}')
[ -n "$base" ] || base=$(printf '%s' "$diffjson" | grep -oE '"baseline_sha":[[:space:]]*"[a-f0-9]+"' | head -1 | grep -oE '[a-f0-9]{7,}')
if [ -n "$base" ] && git cat-file -e "$base:.version" 2>/dev/null; then
  ref=$(git show "$base:.version"); refname="engagement baseline $base"
else
  tag=$(git tag --list 'v*' --sort=-v:refname | grep -vx "v$CLI" | head -1)
  if [ -n "$tag" ]; then
    ref=$(git show "$tag:.version" 2>/dev/null); refname="tag $tag"
  fi
fi
if [ -z "$ref" ]; then
  echo "no engagement baseline or earlier v* tag to compare .version against"
  exit 1
fi
ref_cli=$(printf '%s\n' "$ref" | key satelle.version)
ref_build=$(printf '%s\n' "$ref" | key satelle.build)
if [ "$CLI" = "$ref_cli" ]; then
  echo "satelle.version $CLI is unchanged since $refname — bump it in the release commit"
  exit 1
fi
if [ "$BUILD" = "$ref_build" ]; then
  echo "satelle.build $BUILD is unchanged since $refname — set it to date -u +\"%Y-%m-%d-%H-%M-%S\" in the release commit"
  exit 1
fi
echo "version $ref_cli -> $CLI, build stamp $ref_build -> $BUILD (against $refname)"
```
