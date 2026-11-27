#!/usr/bin/env bash
set -uo pipefail
cd "$(dirname "$0")/.."
names=$( {
  grep -ho '^\[states\.[a-z_-]*\]' .satelle/workflows/*.toml | sed 's/\[states\.\(.*\)\]/\1/'
  grep -ho '^\[gates\.[a-z_-]*\]' .satelle/workflows/*.toml | sed 's/\[gates\.\(.*\)\]/\1/'
  grep -ho '^\[agents\.[a-z_-]*\]' .satelle/agents.toml | sed 's/\[agents\.\(.*\)\]/\1/'
  grep -ho '^name = "[a-z_-]*"' .satelle/workflows/*.toml | cut -d'"' -f2
  grep -ho '^\(category\|tag\) = "[a-z_:-]*"' .satelle/satelle.toml | cut -d'"' -f2
  ls .satelle/skills/*.md | xargs -n1 basename | sed 's/\.md$//'
} | sort -u | grep -v '^README$')
pat=$(printf '"%s"|' $names); pat=${pat%|}
allow='^internal/integration/pi/pi\.go:.*"cancelled": true|^internal/hook/bashscan\.go:.*"done": true|^internal/remotechan/channel\.go:.*msgDone += "done"'
allow="$allow"'|^internal/substrate/substrate\.go:[0-9]+:const Field = "substrate"$|^internal/cli/cmd_item\.go:.*BoolVar\(&onlyOpen, "open",|^internal/hosted/browser\.go:.*name, args = "open",|^internal/web/detail\.go:[0-9]+:(// drew\. Outcome is "moved", "refused" or "open"|	outcomeOpen += "open"$)'
allow="$allow"'|^internal/cli/doctor_resolve\.go:[0-9]+:	answerRelease += "release"'
hits=$(find internal cmd -name '*.go' ! -name '*_test.go' -print0 | xargs -0 grep -nE "$pat" | grep -vE "$allow" || true)
echo "names checked: $(echo "$names" | wc -l)"
if [ -n "$hits" ]; then echo "$hits"; exit 1; fi
echo "no Go string literal names this repo's workflow"
