---
name: satelle-ci-published-check
type: check
description: CI/CD gate on approved → done. A release.yml run on origin/main's head completed with success, the v<satelle.version> and serve-v<satelled.version> releases exist with assets, and v<satelle.version> points at origin/main. GitHub runs no tests, so no test run is read. Deterministic, no agent.
---

# Published

The release run dispatches `release.yml` on main (`workflow_dispatch`); that
workflow builds and publishes both releases and runs no tests.

```sh
set -u
git fetch -q origin --tags || { echo "git fetch origin failed"; exit 1; }
main=$(git rev-parse origin/main)
key() { git show origin/main:.version 2>/dev/null | awk -v k="$1:" '$1==k {print $2}'; }
ver=$(key satelle.version); sver=$(key satelled.version)
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
