#!/bin/sh
# satelle bootstrap installer — the `curl | sh` first-touch:
#
#   curl -fsSL https://github.com/bobmcallan/satelle/releases/latest/download/install.sh | sh
#
# (Published as a release asset on the CLI release, which remains GitHub "latest".)
#
# CLI and daemon carry independent versions (sty_19ff03f4 / sty_bd9de06d):
#   - CLI: tag v<X>, asset satelle-v<X>-<os>-<arch>, always latest
#   - daemon: tag serve-v<Y> (prefix kept so older CLIs still discover the
#     channel), asset satelled-v<Y>-…, not latest. Older tags publish
#     satelle-serve-v<Y>-… — compatibility fallback, not a current name.
#
# Pin a version (installs the CLI only; no GitHub API call is made):
#
#   curl -fsSL https://github.com/bobmcallan/satelle/releases/latest/download/install.sh | sh -s -- 0.0.643
#
# SATELLE_RELEASE_BASE overrides the download base exactly as `satelle update`
# reads it: the directory release assets are downloaded from, one subdirectory
# per tag (<base>/v<version>/<asset>).
#
# Installing is the service's upgrade path (operator's ruling, 2026-11-09,
# sto_1b6a7ed3): when satelle is installed the service is installed, and the
# default is to sync. After the binaries, this script runs
# `satelle service refresh`: it installs satelled's unit and satelle-sync.timer
# when missing, repoints a unit naming another binary, restarts satelled onto
# the new build, starts the sync timer and prints `satelle service status`;
# with no systemd user manager it prints the units and the autosync command.
# Provisioning, CI or a curl-to-shell run that must not touch the service opts
# out, the same way `satelle update` does:
#
#   ... | sh -s -- --no-service           # or SATELLE_NO_SERVICE=1 (any value but 0)
#
# Next: `satelle init` in a repo, then `satelle agents install <harness>`.
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
	# --- CLI (pinned: the tag is named, so nothing is looked up) ---
	given=$version
	version="${version#v}"
	case "$version" in
		"" | *[!0-9.]*) echo "satelle install: version '$given' is not MAJOR.MINOR.PATCH" >&2; exit 1 ;;
	esac
	tag="v$version"
else
	# --- CLI (always releases/latest) ---
	# Fetch the API body to a FILE, then grep the file. Never `curl | grep -m1`:
	# grep exits on its first match, curl takes EPIPE mid-body and prints
	# `curl: (23) Failure writing output to destination` — a cosmetic error that
	# reads as a failed install (sty_87b5d4bc). No `2>/dev/null` and no `|| true`
	# here: this lookup is HARD-fail, so `set -e` aborts and curl's own diagnostic
	# (`curl: (6) Could not resolve host`, a 404, …) still reaches the operator.
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

# --- daemon (independent serve-v* tags; soft-fail if none) ---
# Newest-first releases list; first serve-v* tag wins. Asset name uses v<Y>
# (strip serve- prefix), never the full tag. Tag prefix serve-v* is kept so
# older installed CLIs still discover the channel (sty_bd9de06d).
# De-piped for the same reason as the CLI lookup above, so `head -1` truncates a
# grep reading a local file rather than a live curl. Unlike that one this branch
# is deliberately SOFT-fail: `|| true` on both statements (and `2>/dev/null` on
# the grep, for the case where the fetch left no file) so a missing serve release
# falls through to the fallback branch below and still exits 0.
install_daemon_asset() {
	# $1 = tag, $2 = asset base name (satelled | satelle-serve). Always installs
	# as satelled. satelle-serve is a compatibility fallback for older tags.
	_tag=$1
	_base=$2
	# serve-vY → vY in the asset name; a combined-release CLI tag (vX) is used as-is.
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
	# A pinned install is the CLI alone: finding the daemon release is an API call.
	echo "satelle install: pinned $tag — satelled not installed (run without a version for the daemon)"
else
	curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=30" -o "$tmp/releases.json" || true
	serve_tag=$(grep -oE '"tag_name": "serve-v[^"]+"' "$tmp/releases.json" 2>/dev/null \
		| head -1 | cut -d'"' -f4 || true)
	if [ -n "$serve_tag" ]; then
		if ! install_daemon_asset "$serve_tag" "satelled"; then
			# Compatibility fallback — older serve-v* tags publish satelle-serve-… assets.
			if ! install_daemon_asset "$serve_tag" "satelle-serve"; then
				echo "satelle install: could not fetch satelled (or legacy satelle-serve) for $serve_tag — service install will fall back to satelle serve"
			fi
		fi
	else
		# Pre-split releases may still carry both under v*; try same tag once.
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

# --- service (installing is its upgrade path; sto_1b6a7ed3) ---
echo
if [ -n "$no_service" ]; then
	echo "satelle install: service: not installed or changed ($no_service); 'satelle service install' installs it later"
elif ! "$INSTALL_DIR/satelle" help service 2>/dev/null | grep -q '^  refresh '; then
	# A pinned older CLI has no refresh; it installs nothing here.
	echo "satelle install: this satelle has no 'service refresh'; run 'satelle service install' to install the service and sync timer"
elif ! "$INSTALL_DIR/satelle" service refresh; then
	echo "satelle install: the binaries are installed, but the service is not (above); fix it and run 'satelle service refresh', or rerun with --no-service" >&2
	exit 1
fi

# Installing and upgrading are the same command, and this script cannot reliably
# tell which one it just did. So it names BOTH cases rather than guessing
# (sty_0f471251): a fresh install ignores the second block, and an upgrade — which
# has just invalidated the scaffolding of every repo already on this machine —
# finally gets told.
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
echo "  satelle init              # adds what an older satelle did not scaffold; leaves existing files alone"
