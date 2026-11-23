#!/bin/sh
set -eu
. "$(dirname "$0")/name.sh"
src=$1
n=$(sprite_name "$2")
dst=$3
base=$(basename "$src")
tar -C "$(dirname "$src")" -cf - "$base" | sprite exec -s "$n" -- sudo sh -c 't=$(mktemp -d) && tar -xf - -C "$t" && mkdir -p "$(dirname "$1")" && rm -rf "$1" && mv "$t/$2" "$1" && rmdir "$t"' sh "$dst" "$base" >&2
