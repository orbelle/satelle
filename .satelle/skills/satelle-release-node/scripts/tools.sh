#!/bin/sh
set -eu
GO_VERSION=1.27.0
GO_SHA256=675c26c449cbb18fc24b74650de1eabbae6e16f64326fd85a283fb3b58280685
GH_VERSION=2.103.3
GH_SHA256=4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d
P=${SATELLE_TOOLS_PREFIX:-/}
P=${P%/}

if [ -z "${SATELLE_TOOLS_SKIP_APT:-}" ]; then
  missing=
  for pkg in make bubblewrap curl; do
    dpkg -s "$pkg" >/dev/null 2>&1 || missing="$missing $pkg"
  done
  if [ -n "$missing" ]; then
    echo "tools: installing$missing"
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y --no-install-recommends $missing
  fi
  if [ "$(readlink -f /bin/sh)" != "$(readlink -f "$(command -v bash)")" ]; then
    ln -sfn bash /bin/sh || { echo "tools: cannot make sh bash" >&2; exit 1; }
    echo "tools: sh is bash, as on the machine the release has tested on"
  fi
fi

t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT

fetch() {
  curl -fsSL -o "$t/$2" "$1" || { echo "tools: cannot download $1" >&2; exit 1; }
  printf '%s  %s\n' "$3" "$2" | (cd "$t" && sha256sum -c - >/dev/null 2>&1) || { echo "tools: $4 tarball sha256 mismatch" >&2; exit 1; }
}

if [ "$("$P/usr/local/go/bin/go" env GOVERSION 2>/dev/null)" != "go$GO_VERSION" ]; then
  fetch "https://dl.google.com/go/go$GO_VERSION.linux-amd64.tar.gz" go.tgz "$GO_SHA256" go
  rm -rf "$P/usr/local/go"
  mkdir -p "$P/usr/local"
  tar -C "$P/usr/local" -xzf "$t/go.tgz"
fi

if ! "$P/usr/local/bin/gh" --version 2>/dev/null | grep -q "gh version $GH_VERSION "; then
  fetch "https://github.com/cli/cli/releases/download/v$GH_VERSION/gh_${GH_VERSION}_linux_amd64.tar.gz" gh.tgz "$GH_SHA256" gh
  mkdir -p "$t/gh" "$P/usr/local/bin"
  tar -C "$t/gh" -xzf "$t/gh.tgz"
  install -m 0755 "$t/gh/gh_${GH_VERSION}_linux_amd64/bin/gh" "$P/usr/local/bin/gh"
fi

if [ -z "${SATELLE_TOOLS_SKIP_USER:-}" ]; then
  id release >/dev/null 2>&1 || useradd -m -s /bin/sh release
fi

echo "tools: $("$P/usr/local/go/bin/go" version)"
echo "tools: $("$P/usr/local/bin/gh" --version | head -n1)"
echo "tools: done"
