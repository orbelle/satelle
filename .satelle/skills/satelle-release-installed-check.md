---
name: satelle-release-installed-check
type: check
description: CI/CD's last gate on approved → done. This machine runs the released build: satelle version and satelle service status name origin/main's versions and commit (on a binary-unchanged release, per the run's release-binary decision, the commits of the published v<satelle.version> and serve-v<satelled.version> tags), satelled answers, and nothing warns. Deterministic, no agent.
---

# Release installed

satelle's release ends with the published build installed here and satelled
restarted onto it. `satelle service status --session <session>` pads each label to ten columns, so
every label is matched with ` +`.

A binary-unchanged release installs nothing (the run's `release-binary`
decision, read through `scripts/release-binary.sh of`, says so): the build
that must be running is the published one, so
`satelle version --session <session>` must name the commit of `v<satelle.version>` and both satelled lines the commit of
`serve-v<satelled.version>`. A run with no decision gets the full check.

```sh
set -u
task=tas_42301d36
git fetch -q origin --tags || { echo "git fetch origin failed"; exit 1; }
if ! rec=$(bash scripts/release-binary.sh of "$task" "$SATELLE_ITEM" 2>&1); then
  echo "cannot read the release run's binary decision for $SATELLE_ITEM: $rec"
  exit 1
fi
sha12=$(git rev-parse origin/main | cut -c1-12)
key() { git show origin/main:.version 2>/dev/null | awk -v k="$1:" '$1==k {print $2}'; }
ver=$(key satelle.version | sed 's/[.]/[.]/g'); sver=$(key satelled.version | sed 's/[.]/[.]/g')
csha=$sha12; ssha=$sha12
if printf '%s\n' "$rec" | grep -qx 'binary: unchanged'; then
  csha=$(git rev-parse -q --verify "v$(key satelle.version)^{commit}" | cut -c1-12)
  ssha=$(git rev-parse -q --verify "serve-v$(key satelled.version)^{commit}" | cut -c1-12)
  [ -n "$csha" ] && [ -n "$ssha" ] || { echo "binary unchanged, but v$(key satelle.version) or serve-v$(key satelled.version) is not a published tag"; exit 1; }
fi
satelle version | grep -qE "^satelle $ver [(]commit $csha, built " || { echo "satelle version is not satelle $ver at $csha: $(satelle version)"; exit 1; }
st=$(satelle service status 2>&1)
for pat in '^satelled +answering at .* [(]200 OK[)]$' "^running +satelled $sver [(]commit $ssha, built " "^installed +satelled $sver [(]commit $ssha, built "; do
  printf '%s\n' "$st" | grep -qE "$pat" || { echo "satelle service status lacks a line matching $pat"; printf '%s\n' "$st"; exit 1; }
done
if printf '%s\n' "$st" | grep -qE '^warning'; then
  echo "satelle service status warns:"; printf '%s\n' "$st"; exit 1
fi
if [ "$csha" = "$sha12" ] && [ "$ssha" = "$sha12" ]; then
  echo "installed and running: satelle and satelled at $sha12"
else
  echo "binary unchanged: installed and running the published satelle at $csha and satelled at $ssha"
fi
```
