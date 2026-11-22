#!/bin/sh
set -eu
command -v apt-get >/dev/null 2>&1 || { echo "base-tools: apt-get not found; this script prepares a Debian base" >&2; exit 1; }
missing=
for pkg in git jq ca-certificates; do
  if dpkg -s "$pkg" >/dev/null 2>&1; then
    echo "base-tools: $pkg present"
  else
    missing="$missing $pkg"
  fi
done
if [ -n "$missing" ]; then
  echo "base-tools: installing$missing"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends $missing
fi
for tool in git jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "base-tools: $tool is not on PATH after install" >&2; exit 1; }
done
echo "base-tools: done"
