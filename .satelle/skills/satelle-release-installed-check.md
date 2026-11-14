---
name: satelle-release-installed-check
type: check
description: CI/CD's last gate on approved → done. This machine runs the released build: satelle version and satelle service status name origin/main's versions and commit, satelled answers, and nothing warns. Deterministic, no agent.
---

# Release installed

satelle's release ends with the published build installed here and satelled
restarted onto it. `satelle service status --session <session>` pads each label to ten columns, so
every label is matched with ` +`.

```sh
set -u
git fetch -q origin || { echo "git fetch origin failed"; exit 1; }
sha12=$(git rev-parse origin/main | cut -c1-12)
key() { git show origin/main:.version 2>/dev/null | awk -v k="$1:" '$1==k {print $2}'; }
ver=$(key satelle.version | sed 's/[.]/[.]/g'); sver=$(key satelled.version | sed 's/[.]/[.]/g')
satelle version | grep -qE "^satelle $ver [(]commit $sha12, built " || { echo "satelle version is not satelle $ver at $sha12: $(satelle version)"; exit 1; }
st=$(satelle service status 2>&1)
for pat in '^satelled +answering at .* [(]200 OK[)]$' "^running +satelled $sver [(]commit $sha12, built " "^installed +satelled $sver [(]commit $sha12, built "; do
  printf '%s\n' "$st" | grep -qE "$pat" || { echo "satelle service status lacks a line matching $pat"; printf '%s\n' "$st"; exit 1; }
done
if printf '%s\n' "$st" | grep -qE '^warning'; then
  echo "satelle service status warns:"; printf '%s\n' "$st"; exit 1
fi
echo "installed and running: satelle and satelled at $sha12"
```
