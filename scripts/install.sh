#!/bin/sh
set -eu

REPO="bobmcallan/satelle"
INSTALL_DIR="${SATELLE_INSTALL_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) echo "satelle install: unsupported arch '$arch'" >&2; exit 1 ;;
esac
case "$os" in
	linux | darwin) ;;
	*) echo "satelle install: unsupported OS '$os' (use the .exe asset on Windows)" >&2; exit 1 ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

base="${SATELLE_RELEASE_BASE:-https://github.com/$REPO/releases/download}"
version=""
no_service=""
for arg in "$@"; do
	case "$arg" in
		--no-service) no_service="--no-service" ;;
		-*) echo "satelle install: unknown option '$arg' (the only option is --no-service)" >&2; exit 1 ;;
		*) version="$arg" ;;
	esac
done
if [ -z "$no_service" ] && [ -n "${SATELLE_NO_SERVICE:-}" ] && [ "${SATELLE_NO_SERVICE}" != "0" ]; then
	no_service="SATELLE_NO_SERVICE=$SATELLE_NO_SERVICE"
fi

if [ -n "$version" ]; then
	given=$version
	version="${version#v}"
	case "$version" in
		"" | *[!0-9.]*) echo "satelle install: version '$given' is not MAJOR.MINOR.PATCH" >&2; exit 1 ;;
	esac
	tag="v$version"
else
	curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" -o "$tmp/latest.json"
	tag=$(grep -m1 '"tag_name"' "$tmp/latest.json" | cut -d'"' -f4)
	[ -n "$tag" ] || { echo "satelle install: could not resolve latest release" >&2; exit 1; }
fi

name="satelle-$tag-$os-$arch"
url="$base/$tag/$name"

echo "satelle install: fetching $name ..."
curl -fsSL "$url" -o "$tmp/satelle"
curl -fsSL "$url.sha256" -o "$tmp/satelle.sha256"

want=$(cut -d' ' -f1 "$tmp/satelle.sha256")
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/satelle" | cut -d' ' -f1)
else
	got=$(shasum -a 256 "$tmp/satelle" | cut -d' ' -f1)
fi
[ "$want" = "$got" ] || { echo "satelle install: sha256 mismatch (want $want, got $got)" >&2; exit 1; }

chmod +x "$tmp/satelle"
mkdir -p "$INSTALL_DIR"
mv "$tmp/satelle" "$INSTALL_DIR/satelle"
echo "satelle install: installed $INSTALL_DIR/satelle ($tag)"

install_daemon_asset() {
	_tag=$1
	_base=$2
	if [ "$_tag" = "${_tag#serve-}" ]; then
		_name="${_base}-$_tag-$os-$arch"
	else
		_name="${_base}-${_tag#serve-}-$os-$arch"
	fi
	_url="$base/$_tag/$_name"
	if curl -fsSL "$_url" -o "$tmp/satelled" 2>/dev/null \
		&& curl -fsSL "$_url.sha256" -o "$tmp/satelled.sha256" 2>/dev/null; then
		swant=$(cut -d' ' -f1 "$tmp/satelled.sha256")
		if command -v sha256sum >/dev/null 2>&1; then
			sgot=$(sha256sum "$tmp/satelled" | cut -d' ' -f1)
		else
			sgot=$(shasum -a 256 "$tmp/satelled" | cut -d' ' -f1)
		fi
		if [ "$swant" = "$sgot" ]; then
			chmod +x "$tmp/satelled"
			mv "$tmp/satelled" "$INSTALL_DIR/satelled"
			echo "satelle install: installed $INSTALL_DIR/satelled ($_tag)"
			return 0
		fi
		echo "satelle install: satelled sha256 mismatch — skipped (use satelle serve fallback)" >&2
		return 1
	fi
	return 1
}

if [ -n "$version" ]; then
	echo "satelle install: pinned $tag — satelled not installed (run without a version for the daemon)"
else
	curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=30" -o "$tmp/releases.json" || true
	serve_tag=$(grep -oE '"tag_name": "serve-v[^"]+"' "$tmp/releases.json" 2>/dev/null \
		| head -1 | cut -d'"' -f4 || true)
	if [ -n "$serve_tag" ]; then
		if ! install_daemon_asset "$serve_tag" "satelled"; then
			if ! install_daemon_asset "$serve_tag" "satelle-serve"; then
				echo "satelle install: could not fetch satelled (or legacy satelle-serve) for $serve_tag — service install will fall back to satelle serve"
			fi
		fi
	else
		if ! install_daemon_asset "$tag" "satelled"; then
			if ! install_daemon_asset "$tag" "satelle-serve"; then
				echo "satelle install: no satelled release yet — service install will fall back to satelle serve"
			fi
		fi
	fi
fi

case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*) echo "satelle install: add $INSTALL_DIR to your PATH (e.g. export PATH=\"$INSTALL_DIR:\$PATH\")" ;;
esac

echo
if [ -n "$no_service" ]; then
	echo "satelle install: service: not installed or changed ($no_service); 'satelle service install' installs it later"
elif ! "$INSTALL_DIR/satelle" help service 2>/dev/null | grep -q '^  refresh '; then
	echo "satelle install: this satelle has no 'service refresh'; run 'satelle service install' to install the service and sync timer"
elif ! "$INSTALL_DIR/satelle" service refresh; then
	echo "satelle install: the binaries are installed, but the service is not (above); fix it and run 'satelle service refresh', or rerun with --no-service" >&2
	exit 1
fi

echo
echo "The service and sync timer were installed and refreshed by this script, unless"
echo "skipped (--no-service / SATELLE_NO_SERVICE=1): every machine syncs by default."
echo
echo "Next (a new repo):"
echo "  cd <your-repo>"
echo "  satelle init             # scaffold .satelle/ (config, db, authored dirs)"
echo "  satelle agents install <harness>   # claude | pi | grok | cursor: wire your agent's sessions to satelle"
echo "  satelle doctor           # read-only: what is wrong here, each with its fix"
echo
echo "Repos you already have (if this was an upgrade):"
echo "  satelle doctor            # read-only: what is wrong here, each with its fix"
echo "  satelle init              # lists what this release ships that the repo lacks; writes nothing"
