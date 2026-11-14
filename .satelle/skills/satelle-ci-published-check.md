---
name: satelle-ci-published-check
type: check
description: CI/CD gate on approved → done. On a binary-unchanged release (the run's release-binary decision, release.md ### Decide) the v<satelle.version> and serve-v<satelled.version> releases exist with assets and both tags are under origin/main; no release.yml run is read. Otherwise a release.yml run on origin/main's head completed with success, the v<satelle.version> and serve-v<satelled.version> releases exist with assets, and v<satelle.version> points at origin/main. GitHub runs no tests, so no test run is read. Deterministic, no agent.
---

# Published

The release run dispatches `release.yml` on main (`workflow_dispatch`); that
workflow builds and publishes both releases and runs no tests. A
binary-unchanged release dispatches nothing (the run's `release-binary`
decision, read through `scripts/release-binary.sh of`, says so): the versions
on main are the ones already published, so the check reads those releases and
that their tags are under origin/main. A run with no decision gets the full
check.

```sh
set -u
task=tas_42301d36
git fetch -q origin --tags || { echo "git fetch origin failed"; exit 1; }
if ! rec=$(bash scripts/release-binary.sh of "$task" "$SATELLE_ITEM" 2>&1); then
  echo "cannot read the release run's binary decision for $SATELLE_ITEM: $rec"
  exit 1
fi
main=$(git rev-parse origin/main)
key() { git show origin/main:.version 2>/dev/null | awk -v k="$1:" '$1==k {print $2}'; }
ver=$(key satelle.version); sver=$(key satelled.version)
if printf '%s\n' "$rec" | grep -qx 'binary: unchanged'; then
  for tag in "v$ver" "serve-v$sver"; do
    n=$(gh release view "$tag" --json assets --jq '.assets | length' 2>/dev/null || echo 0)
    [ "${n:-0}" -gt 0 ] || { echo "binary unchanged, but release $tag is missing or has no assets"; exit 1; }
    git merge-base --is-ancestor "$tag" origin/main 2>/dev/null || { echo "binary unchanged, but tag $tag is not under origin/main ${main:0:12}"; exit 1; }
  done
  echo "binary unchanged: v$ver and serve-v$sver are published with assets and under origin/main ${main:0:12}; no release.yml run was dispatched"
  exit 0
fi
ok=$(gh run list --workflow release.yml --limit 20 --json headSha,status,conclusion,url |
  jq -r --arg h "$main" '[.[] | select(.headSha == $h and .status == "completed" and .conclusion == "success")][0].url // empty')
[ -n "$ok" ] || { echo "no successful release.yml run on origin/main ${main:0:12}"; exit 1; }
for tag in "v$ver" "serve-v$sver"; do
  n=$(gh release view "$tag" --json assets --jq '.assets | length' 2>/dev/null || echo 0)
  [ "${n:-0}" -gt 0 ] || { echo "release $tag is missing or has no assets"; exit 1; }
done
[ "$(git rev-parse "v$ver^{commit}" 2>/dev/null)" = "$main" ] || { echo "tag v$ver does not point at origin/main ${main:0:12}"; exit 1; }
echo "release.yml $ok succeeded on ${main:0:12}; v$ver and serve-v$sver published with assets"
```
