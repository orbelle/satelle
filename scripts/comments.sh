#!/bin/sh
set -eu
case "${1:-}" in
strip | check) ;;
*)
	echo "usage: scripts/comments.sh strip|check [path...]" >&2
	exit 2
	;;
esac
root=$(cd "$(dirname "$0")/.." && pwd)
bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT
(cd "$root" && go build -o "$bin/comments" ./scripts/comments)
cd "$root"
status=0
"$bin/comments" "$@" || status=$?
exit "$status"
