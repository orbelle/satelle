#!/bin/sh
set -eu
[ -n "${SATELLE_RELEASE_IN:-}" ] && [ -d "$SATELLE_RELEASE_IN" ] || {
  echo "release-start: SATELLE_RELEASE_IN is not the node's input directory: write it with .satelle/skills/release/SKILL.md ### The node's input" >&2
  exit 1
}
n="satelle-$(printf '%s' "$1" | tr '_' '-')"
sprite create --skip-console "$n" </dev/null >&2
echo "sprite $n"
base=$(basename "$SATELLE_RELEASE_IN")
tar -C "$(dirname "$SATELLE_RELEASE_IN")" -cf - "$base" | sprite exec -s "$n" -- sudo sh -c 't=$(mktemp -d) && tar -xf - -C "$t" && mkdir -p "$(dirname "$1")" && rm -rf "$1" && mv "$t/$2" "$1" && rmdir "$t"' sh /release/in "$base" >&2
