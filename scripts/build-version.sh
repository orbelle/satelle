#!/usr/bin/env bash
set -euo pipefail

base="${1:-}"
if [ -z "$base" ]; then
  echo "usage: $0 <base-version> [tag-prefix]" >&2
  exit 2
fi
prefix="${2:-v}"
tag="${prefix}${base}"

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  printf '%s\n' "$base"
  exit 0
fi

sha=$(git rev-parse --short=12 HEAD 2>/dev/null || true)
if [ -z "$sha" ]; then
  printf '%s\n' "$base"
  exit 0
fi

dirty=""
if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
  dirty="-dirty"
fi

tag_commit=$(git rev-parse -q --verify "${tag}^{commit}" 2>/dev/null || true)
head_commit=$(git rev-parse HEAD 2>/dev/null || true)

if [ -n "$tag_commit" ] && [ "$tag_commit" = "$head_commit" ] && [ -z "$dirty" ]; then
  printf '%s\n' "$base"
  exit 0
fi

printf '%s+%s%s\n' "$base" "$sha" "$dirty"
