#!/bin/sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
sprite=$here/../../satelle-node-sprite/scripts
[ -n "${SATELLE_RELEASE_IN:-}" ] && [ -d "$SATELLE_RELEASE_IN" ] || {
  echo "release-start: SATELLE_RELEASE_IN is not the node's input directory: write it with .satelle/skills/release.md ### The node's input" >&2
  exit 1
}
sh "$sprite/start.sh" "$1"
sh "$sprite/copy-in.sh" "$SATELLE_RELEASE_IN" "$1" /release/in
