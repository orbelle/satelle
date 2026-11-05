#!/bin/sh
# credguard.sh -- run a command and fail if it changed the operator's host
# credentials file in a way a test could have (sty_18403814, sty_5ba68e2c).
#
# Usage: credguard.sh -- <cmd> [args...]
#
# The credentials file is ${XDG_CONFIG_HOME:-$HOME/.config}/satelle/credentials.toml, the
# per-user store `satelle login` writes. A test suite must leave it untouched.
# The guard compares the file's IDENTITY before and after the command, not its
# bytes: a satelle process outside the suite may refresh a hosted token during
# the run, which rewrites tokens and timestamps but keeps every credential's
# server_url, display_name, email and principal_id. That passes. Adding or
# removing a credential, changing an identity, creating the file from absent,
# deleting it, or leaving it unparseable exits 1 with a message.
#
# The fingerprint and diff are hosted.CaptureIdentity / hosted.DiffIdentity (via
# scripts/credfp), the same code the integration suite's host-surface guard
# uses, so the two guards agree. When the command itself fails, its exit status
# is kept, so a red suite stays red and the guard only adds a failure of its own.
# If the helper cannot be built or run the guard fails closed (exit 2).

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

# Credentials block.
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
