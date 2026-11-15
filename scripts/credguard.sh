#!/bin/sh

[ "$1" = "--" ] && shift
if [ "$#" -eq 0 ]; then
	echo "usage: credguard.sh -- <cmd> [args...]" >&2
	exit 2
fi

root=$(cd "$(dirname "$0")/.." && pwd) || exit 2
cred="${XDG_CONFIG_HOME:-$HOME/.config}/satelle/credentials.toml"

work=$(mktemp -d "${TMPDIR:-/tmp}/credguard.XXXXXX") || exit 2
trap 'rm -rf "$work"' EXIT HUP INT TERM

if ! (cd "$root" && go build -o "$work/credfp" ./scripts/credfp); then
	echo "credguard: cannot build scripts/credfp; failing closed" >&2
	exit 2
fi
if ! "$work/credfp" snapshot "$cred" >"$work/before"; then
	echo "credguard: cannot snapshot host credentials: $cred" >&2
	exit 2
fi

"$@"
status=$?

if ! "$work/credfp" snapshot "$cred" >"$work/after"; then
	echo "credguard: cannot snapshot host credentials: $cred" >&2
	[ "$status" -eq 0 ] && status=2
else
	reasons=$("$work/credfp" diff "$work/before" "$work/after")
	rc=$?
	if [ "$rc" -ne 0 ]; then
		if [ "$rc" -eq 1 ]; then
			echo "credguard: host credentials file changed by tests: $cred" >&2
			printf '%s\n' "$reasons" | sed 's/^/credguard:   /' >&2
			[ "$status" -eq 0 ] && status=1
		else
			echo "credguard: credfp diff failed (exit $rc); failing closed" >&2
			[ "$status" -eq 0 ] && status=2
		fi
	fi
fi

exit "$status"
