---
name: satelle-version-stamp-check
type: check
description: CI/CD gate on approved → done. On a binary-changed release origin/main's .version bumps satelle.version past the latest other v* tag and satelled.version past the latest other serve-v* tag, v<satelle.version> is not already published at another commit, and satelle.build is an RFC 3339 UTC datetime. On a binary-unchanged release (the run's release-binary record, release.md ### Decide) origin/main is the recorded candidate, .version is as v<satelle.version> published it, and the record's other paths are exactly origin/main's changes since the published tags. Deterministic, no agent.
---

# Version stamp

Every binary-changed release bumps the version once (the release run does it).
A binary-unchanged release bumps nothing: main fast-forwards to the tested
candidate, and the run's `release-binary` record (written and attached by
release.md, read through `scripts/release-binary.sh of`) says so. A run with
no record (`binary: unrecorded`) gets the binary-changed checks.

On a binary-changed release this check reads `origin/main:.version` and
compares each version with the latest published tag other than its own, as
bare versions: the `v` / `serve-v` prefix is stripped before `sort -V`, which
orders digits before letters, so an unstripped bare version would sort below
every `v`-prefixed tag. A `v<satelle.version>` tag that already points at
another commit means the bump was skipped.

On a binary-unchanged release it checks the record against origin/main: main
is the recorded candidate, `.version` is unchanged since `v<satelle.version>`,
the record has no `build` and no `reason:` line, and its `other` paths are
exactly the paths origin/main changed since the recorded tags.

```sh
set -u
task=tas_42301d36
git fetch -q origin --tags || { echo "git fetch origin failed"; exit 1; }
if ! rec=$(bash scripts/release-binary.sh of "$task" "$SATELLE_ITEM" 2>&1); then
  echo "cannot read the release run's binary decision for $SATELLE_ITEM: $rec"
  exit 1
fi
key() { git show origin/main:.version 2>/dev/null | awk -v k="$1:" '$1==k {print $2}'; }
ver=$(key satelle.version); sver=$(key satelled.version); BUILD=$(key satelle.build)
if [ -z "$ver" ] || [ -z "$sver" ] || [ -z "$BUILD" ]; then
  echo "origin/main .version lacks satelle.version, satelled.version or satelle.build"
  exit 1
fi
if ! printf '%s' "$BUILD" | grep -qE '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$'; then
  echo "satelle.build '$BUILD' is not date -u +%Y-%m-%dT%H:%M:%SZ"
  exit 1
fi
main=$(git rev-parse origin/main)
if printf '%s\n' "$rec" | grep -qx 'binary: unchanged'; then
  run=$(printf '%s\n' "$rec" | sed -n 's/^run: //p')
  cand=$(printf '%s\n' "$rec" | sed -n 's/^candidate: //p')
  [ "$cand" = "$main" ] || { echo "run $run decided binary unchanged on ${cand:0:12}, but origin/main is ${main:0:12}: a commit is on top of the recorded candidate"; exit 1; }
  git diff --quiet "v$ver" origin/main -- .version 2>/dev/null || { echo "binary unchanged, but origin/main's .version differs from v$ver's (or v$ver is not published)"; exit 1; }
  if printf '%s\n' "$rec" | grep -qE '^(build |reason: )'; then
    echo "run $run's record says binary unchanged but carries a build or reason line:"; printf '%s\n' "$rec"; exit 1
  fi
  bases=$(printf '%s\n' "$rec" | sed -n 's/^base: [^ ]* //p')
  want=$(printf '%s\n' "$rec" | sed -n 's/^other //p' | sort -u)
  got=$(for b in $bases; do git diff --name-only --no-renames "$b" origin/main || echo "(git diff from $b failed)"; done | sort -u)
  if [ -z "$bases" ] || [ "$got" != "$want" ]; then
    echo "run $run's record does not list exactly origin/main's changes since its tags:"
    echo "recorded: $(printf '%s' "$want" | tr '\n' ' ')"
    echo "changed:  $(printf '%s' "$got" | tr '\n' ' ')"
    exit 1
  fi
  echo "binary unchanged since v$ver (run $run): origin/main is the tested candidate and .version is as v$ver published it"
  exit 0
fi
pub=$(git rev-parse -q --verify "v$ver^{commit}")
if [ -n "$pub" ] && [ "$pub" != "$main" ]; then
  echo "v$ver is already published at ${pub:0:12}, not origin/main: the bump was skipped"
  exit 1
fi
above() { # $1 version, $2 prefix: is $1 above every other tag with the prefix?
  latest=$(git tag -l "$2[0-9]*" | grep -vx "$2$1" | sed "s/^$2//" | sort -V | tail -1)
  [ -z "$latest" ] && return 0
  [ "$1" != "$latest" ] && [ "$(printf '%s\n%s\n' "$latest" "$1" | sort -V | tail -1)" = "$1" ]
}
above "$ver" v || { echo "satelle.version $ver is not above the latest v* tag; bump it"; exit 1; }
above "$sver" serve-v || { echo "satelled.version $sver is not above the latest serve-v* tag; bump it"; exit 1; }
echo "origin/main bumps satelle $ver and satelled $sver past the latest other tags; build $BUILD"
```
