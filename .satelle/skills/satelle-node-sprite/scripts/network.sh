#!/bin/sh
set -eu
. "$(dirname "$0")/name.sh"
n=$(sprite_name "$1")
rules=$(grep -v '^[[:space:]]*$' | jq -R -s -c 'split("\n") | map(select(length > 0) | {domain: ., action: "allow"}) + [{domain: "*", action: "deny"}] | {rules: .}')
sprite api "/v1/sprites/$n/policy/network" -- -s -f -X POST -H 'Content-Type: application/json' -d "$rules" >&2
got=$(sprite api "/v1/sprites/$n/policy/network" -- -s -f 2>/dev/null | sed -n '/^{/,$p')
printf '%s\n' "$got" | jq -r '.rules[] | select(.action == "allow") | .domain'
printf '%s\n' "$got" | jq -e '[.rules[] | select(.domain == "*")] | length == 1 and .[0].action == "deny"' >/dev/null || { echo "sprite-network: $n does not deny every other host" >&2; exit 1; }
