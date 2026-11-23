#!/bin/sh
. "$(dirname "$0")/name.sh"
n=$(sprite_name "$1")
sprite destroy --force "$n" </dev/null >&2 && exit 0
sprite list --prefix "$n" 2>/dev/null | grep -qx "$n" && { echo "sprite-stop: $n is still listed" >&2; exit 1; }
exit 0
