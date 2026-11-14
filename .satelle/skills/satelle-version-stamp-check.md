---
name: satelle-version-stamp-check
type: check
description: CI/CD gate on approved → done. origin/main's .version bumps satelle.version past the latest other v* tag and satelled.version past the latest other serve-v* tag, and stamps satelle.build as an RFC 3339 UTC datetime. Deterministic, no agent.
---

# Version stamp

Every release bumps the version once (the release run does it). This check reads
`origin/main:.version` and compares each version with the latest published tag
other than its own, as bare versions: the `v` / `serve-v` prefix is stripped
before `sort -V`, which orders digits before letters, so an unstripped bare
version would sort below every `v`-prefixed tag.

```sh
set -u
git fetch -q origin --tags || { echo "git fetch origin failed"; exit 1; }
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
above() { # $1 version, $2 prefix: is $1 above every other tag with the prefix?
  latest=$(git tag -l "$2[0-9]*" | grep -vx "$2$1" | sed "s/^$2//" | sort -V | tail -1)
  [ -z "$latest" ] && return 0
  [ "$1" != "$latest" ] && [ "$(printf '%s\n%s\n' "$latest" "$1" | sort -V | tail -1)" = "$1" ]
}
above "$ver" v || { echo "satelle.version $ver is not above the latest v* tag; bump it"; exit 1; }
above "$sver" serve-v || { echo "satelled.version $sver is not above the latest serve-v* tag; bump it"; exit 1; }
echo "origin/main bumps satelle $ver and satelled $sver past the latest other tags; build $BUILD"
```
