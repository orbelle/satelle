#!/bin/sh
set -eu
. "$(dirname "$0")/name.sh"
n=$(sprite_name "$1")
sprite create --skip-console "$n" </dev/null >&2
echo "sprite $n"
