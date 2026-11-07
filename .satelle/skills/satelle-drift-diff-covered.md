---
name: satelle-drift-diff-covered
type: check
description: After check of the drift-constitution gate. When the run's diff over the binary's sources is longer than the gate prompt's 200000-byte cut, it turns the judge's accept into a reject unless the notes carry a READ <path> line for every changed file the cut hid. Any other result is kept. Deterministic, no agent.
---

# Drift diff covered

The gate prompt cuts a diff at 200000 bytes (`internal/gate/gate.go`, `const
max`). A judge that accepted a cut diff must have read each hidden file in the
work tree itself, and says so with one `READ <path>` line per file. The two
values below restate the binary's cut and the gate's paths in
`.satelle/workflows/runbook.toml`; `tests/drift_gate_unix_test.go` fails the
suite when either drifts.

```sh
set -u
limit=200000
paths='cmd/** internal/** go.mod go.sum'
[ "${SATELLE_GATE_RESULT:-}" = accept ] || exit 0
notes=$(jq -r '.notes // ""')
tmp=$(mktemp) || exit 2
trap 'rm -f "$tmp"' EXIT
set -f
# shellcheck disable=SC2086
git diff --no-color --no-ext-diff "$SATELLE_BASE" -- $paths > "$tmp" || { echo "git diff from $SATELLE_BASE failed"; exit 2; }
set +f
size=$(wc -c < "$tmp")
[ "$size" -le "$limit" ] && exit 0
hidden=$(LC_ALL=C awk -v cut=$((limit - 1000)) '
  function close_section() { if (p != "" && off > cut) print p }
  /^diff --git a\// { close_section(); p = $3; sub(/^a\//, "", p) }
  { off += length($0) + 1 }
  END { close_section() }' "$tmp")
missing=""
for p in $hidden; do
  printf '%s\n' "$notes" | grep -qxF "READ $p" || missing="$missing $p"
done
if [ -n "$missing" ]; then
  echo "diff truncated at $limit bytes; the judge did not read:$missing"
  exit 1
fi
echo "diff truncated at $limit bytes; the judge read every hidden file"
exit 0
```
