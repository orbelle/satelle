#!/bin/sh
set -eu
. "$(dirname "$0")/name.sh"
n=$(sprite_name "$1")
shift
[ "${1:-}" = "--" ] && shift
exec sprite exec -s "$n" -- sudo "$@"
