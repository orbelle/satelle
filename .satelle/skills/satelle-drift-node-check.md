---
name: satelle-drift-node-check
type: check
description: The check of the drift-node workflow. Runs scripts/drift-check.sh in a docker-local node against a clean clone of the node's checked-out commit, with the node image's satelle, and exits with its code (0 clean, 1 drift, 2 could not check). Deterministic, no agent.
---

# Drift check on a node

The `docker-drift-check` remote (`.satelle/remotes/docker-drift-check/`) gives
a node the firing tree's HEAD commit, checked out at its work tree
(`[drive] checkout = true`), and drives one execution of its task on the
`drift-node` workflow. This check runs on that workflow's `backlog → done`
move, after `change-judged`.

The node's substrate is the firing tree's, unpacked at `/node/substrate`
apart from the work tree (`satelle help remotes --session <session>`). The
check clones the checked-out commit into a temp dir and runs that clone's own
`scripts/drift-check.sh` with the clone as its root: what it judges is the
commit alone. drift-check.sh is a bash script; the node image's
final stage installs bash (`Dockerfile`). An image with no bash fails here as
"could not check" (exit 2), never as drift.

The output is the gate's notes, in a fixed shape the drift review's step 1
reads (`.satelle/skills/drift-review.md`):

- first line: `satelle: <the satelle version line>`
- then every line drift-check.sh printed, stdout and stderr
- last line: `drift-check exit <N>`

The check exits with drift-check.sh's code.

```sh
set -u
echo "satelle: $(satelle version </dev/null 2>&1 | head -1)"
command -v bash >/dev/null 2>&1 || { echo "drift-check: bash is not on PATH, so scripts/drift-check.sh cannot run"; echo "drift-check exit 2"; exit 2; }
sha=$(git rev-parse HEAD 2>/dev/null) || sha=
t=$(mktemp -d) || { echo "drift-check: cannot make a temp dir"; echo "drift-check exit 2"; exit 2; }
if [ -z "$sha" ] || ! git clone -q --no-checkout . "$t/tree" 2>&1 || ! git -C "$t/tree" checkout -q --detach "$sha" 2>&1; then
  rm -rf "$t"
  echo "drift-check: cannot make a clean tree of ${sha:-HEAD}"
  echo "drift-check exit 2"
  exit 2
fi
bash "$t/tree/scripts/drift-check.sh" "$t/tree" </dev/null 2>&1
rc=$?
rm -rf "$t"
echo "drift-check exit $rc"
exit $rc
```
