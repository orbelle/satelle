#!/bin/sh
# hermetic.sh -- run a command in a clean room: an empty test-owned HOME and XDG
# dirs and a minimal PATH, so a test verdict does not depend on the operator's
# real machine (sty_ec30f859).
#
# Usage: hermetic.sh -- <cmd> [args...]     (test flags go after the cmd, e.g.
#        hermetic.sh -- go test -run X ./...; in make, through TESTFLAGS)
#
# The command runs under `env -i`. It gets:
#   - HOME and XDG_{CONFIG,DATA,CACHE,STATE}_HOME as fresh empty dirs inside one
#     mktemp dir, removed on exit; the command's exit status is kept;
#   - PATH=$GOROOT/bin:/usr/local/bin:/usr/bin:/bin -- the Go toolchain and the
#     system bin dirs, no version-manager shims;
#   - GOROOT, GOMODCACHE, GOCACHE and GOPATH read from `go env` BEFORE HOME is
#     replaced, so the wrapped run uses the real toolchain and the real module
#     and build caches (no network, a warm local cache still hits);
#   - no SATELLE_HOME and no path of the caller's home, .satelle dir, XDG config
#     dir or SATELLE_HOME, so the only ~/.satelle a test can reach by HOME is the
#     wrapper's own (sto_e3c6bfb6);
#   - no value that names the caller's HOME at all (sto_e3c6bfb6): a path the
#     command needs that lies under it (GOROOT, the Go caches, GOPATH,
#     SATELLE_BIN, the starting directory and so PWD, ...) is handed over as a
#     symlink in /tmp/satelle-hermetic-<uid> (mode 0700, owned by the caller),
#     named by a checksum of the target and kept between runs, so the Go build
#     cache's keys stay stable and nothing is re-downloaded or rebuilt. Any other passed-through value containing the
#     caller's HOME is dropped with a warning. A TMPDIR under the caller's HOME
#     is not linked (tests compare t.TempDir() paths with the physical working
#     directory a subprocess reports): the run gets a fresh TMPDIR in its own
#     temp dir under /tmp instead. The suite is thus given no path from which to
#     form the caller's ~/.satelle;
#   - SATELLE_RELEASE_API, SATELLE_RELEASE_LIST_API and SATELLE_RELEASE_BASE fixed
#     to a closed loopback endpoint, whatever the caller exports, so a release
#     check started by a session-start hook in a test never reaches GitHub or
#     spends its rate limit (sty_02579374). A test that needs a release server
#     appends its own values to the command's environment;
#   - of the caller's other variables, ONLY the allowlist below plus every
#     SATELLE_TEST_PROBE_* variable. Anything else is dropped.
# The wrapper does not change which tests exist; operator-config checks stay
# behind the `operatorconfig` build tag (make operator-check).
#
# The wrapper's own ~/.satelle is an empty read-only directory (sto_e3c6bfb6),
# so a suite process that resolved the home with no SATELLE_HOME of its own
# fails at the moment it writes there. After the command the wrapper fails the
# run unless the guard is still that empty directory with the same inode, mode
# and change time (written into, removed, replaced or chmodded all count, and
# ctime cannot be set back), so a write cleaned up before exit, even with the
# mode restored, is caught too. No path is exempt. It names what it found on stderr and
# exits 1 when the command passed, else keeps the command's status.
#
# As detection on top of that, the caller's real satelle home is snapshotted
# (sto_e3c6bfb6): $SATELLE_HOME and $HOME/.satelle as the caller has them. Every
# path under it is recorded (type, size, mtime, ctime, inode) before the command
# and again after, and the run fails, by the same exit rule, naming each path
# added, removed or changed. The guard catches any change EXCEPT to the paths
# the host's own satelle rewrites on its own schedule while a suite runs (the
# sync timer every 30 s, satelled, every satelle command of a parallel session),
# which it leaves out: serve/, sessions/, builds.json (and its .tmp) and the
# per-repo runtime dirs <name>-<8 hex>/. The project registry config.toml,
# machine-id and every other path, at any depth, are compared.

# SATELLE_CHROME points the browser e2e tests at a non-default Chrome path (sty_53c3f311).
ALLOW="TERM TMPDIR SATELLE_BIN SATELLE_CHROME GOFLAGS GOPROXY GOTOOLCHAIN GONOSUMDB GOPRIVATE GONOPROXY GOSUMDB"

[ "$1" = "--" ] && shift
if [ "$#" -eq 0 ]; then
	echo "usage: hermetic.sh -- <cmd> [args...]" >&2
	exit 2
fi

if ! goroot=$(go env GOROOT) || [ -z "$goroot" ]; then
	echo "hermetic: go not found on PATH; failing closed" >&2
	exit 2
fi
gomodcache=$(go env GOMODCACHE) || exit 2
gocache=$(go env GOCACHE) || exit 2
gopath=$(go env GOPATH) || exit 2

# hide prints $1 unchanged unless it names the caller's HOME; then it prints a
# symlink to it in $hidebase, so the command is handed no path of the caller's
# home (sto_e3c6bfb6). The link's name is a checksum of the target (plus the
# target's base name), so the same target always gets the same link: links are
# never replaced, concurrent runs agree, and the Go build cache, whose keys
# include source directories, keeps hitting across runs.
hidebase="/tmp/satelle-hermetic-$(id -u)"
mkdir -m 0700 "$hidebase" 2>/dev/null
if [ -L "$hidebase" ] || [ ! -d "$hidebase" ] || [ ! -O "$hidebase" ] || ! chmod 0700 "$hidebase"; then
	echo "hermetic: $hidebase is not a directory owned by this user; failing closed" >&2
	exit 2
fi
for l in $(find "$hidebase" -mindepth 1 -maxdepth 1 -type l -mmin +60 2>/dev/null); do
	[ -e "$l" ] || rm -f "$l"
done
# underhome reports whether $1 is the caller's HOME or under it, as given or
# with HOME's symlinks resolved.
home=${HOME:-/nonexistent-home}
homephys=$(cd "$home" 2>/dev/null && pwd -P) || homephys=$home
underhome() {
	case "$1" in
	"$home" | "$home"/* | "$homephys" | "$homephys"/*) return 0 ;;
	esac
	return 1
}
hide() {
	underhome "$1" || {
		printf '%s\n' "$1"
		return 0
	}
	name=$(printf %s "$1" | cksum | cut -d' ' -f1)
	[ "$1" = "$HOME" ] || name="$name-$(basename "$1")"
	link="$hidebase/$name"
	[ -L "$link" ] || ln -s "$1" "$link" 2>/dev/null
	if [ "$(readlink "$link")" != "$1" ]; then
		echo "hermetic: $link does not point at the expected target; failing closed" >&2
		return 1
	fi
	printf '%s\n' "$link"
}
goroot=$(hide "$goroot") || exit 2
gomodcache=$(hide "$gomodcache") || exit 2
gocache=$(hide "$gocache") || exit 2
gopath=$(hide "$gopath") || exit 2

# The caller's real satelle homes, before HOME is replaced (sto_e3c6bfb6).
real1=${SATELLE_HOME:-}
real2=
[ -n "${HOME:-}" ] && [ "$HOME/.satelle" != "$real1" ] && real2="$HOME/.satelle"

# hostsnap prints one line per path under $1 (relative path, type, size, mtime,
# ctime, inode), sorted, leaving out the paths the host's own satelle writes
# while a suite runs; "absent" when $1 does not exist.
live='^(serve|sessions|builds[.]json|builds[.]json[.][^/|]*[.]tmp|[^/|]*-[0-9a-f]{8})[/|]'
hostsnap() {
	[ -n "$1" ] || return 0
	if [ ! -e "$1" ] && [ ! -L "$1" ]; then
		echo "absent"
		return 0
	fi
	if stat -c %n . >/dev/null 2>&1; then
		fmt="-c"
		spec='%n|%F|%s|%.9Y|%.9Z|%i'
	else
		fmt="-f"
		spec='%N|%HT|%z|%Fm|%Fc|%i'
	fi
	echo ".|$(stat -L $fmt "$(echo "$spec" | cut -d'|' -f2)" "$1" 2>/dev/null)"
	(cd "$1" && find . -mindepth 1 -exec stat $fmt "$spec" {} + 2>/dev/null) |
		sed 's|^[.]/||' | grep -Ev "$live" | LC_ALL=C sort
}
before1=$(hostsnap "$real1")
before2=$(hostsnap "$real2")

# A TMPDIR under the caller's HOME is replaced by $work/tmp, with $work in /tmp.
workroot=${TMPDIR:-/tmp}
ownTMP=
if underhome "$workroot"; then
	workroot=/tmp
	ownTMP=1
fi
work=$(mktemp -d "$workroot/hermetic.XXXXXX") || exit 2
trap 'rm -rf "$work"' HUP INT TERM
mkdir -p "$work/home" "$work/xdg/config" "$work/xdg/data" "$work/xdg/cache" "$work/xdg/state" "$work/tmp" || {
	rm -rf "$work"
	exit 2
}

# Assignments are prepended to the command, so "$@" ends up as
# NAME=value ... <cmd> [args...] for `env -i`.
names=$(env | sed -n 's/^\(SATELLE_TEST_PROBE_[A-Za-z0-9_]*\)=.*/\1/p')
for name in $ALLOW $names; do
	eval "isset=\${$name+x}"
	[ -n "$isset" ] || continue
	eval "val=\${$name}"
	[ "$name" = TMPDIR ] && [ -n "$ownTMP" ] && val="$work/tmp"
	val=$(hide "$val") || { rm -rf "$work"; exit 2; }
	case "$val" in
	*"$home"* | *"$homephys"*)
		echo "hermetic: $name names the caller's HOME and is not a path under it; dropped" >&2
		continue
		;;
	esac
	set -- "$name=$val" "$@"
done

set -- \
	"HOME=$work/home" \
	"XDG_CONFIG_HOME=$work/xdg/config" \
	"XDG_DATA_HOME=$work/xdg/data" \
	"XDG_CACHE_HOME=$work/xdg/cache" \
	"XDG_STATE_HOME=$work/xdg/state" \
	"PATH=$goroot/bin:/usr/local/bin:/usr/bin:/bin" \
	"GOROOT=$goroot" \
	"GOMODCACHE=$gomodcache" \
	"GOCACHE=$gocache" \
	"GOPATH=$gopath" \
	"SATELLE_RELEASE_API=http://127.0.0.1:9/hermetic/releases/latest" \
	"SATELLE_RELEASE_LIST_API=http://127.0.0.1:9/hermetic/releases" \
	"SATELLE_RELEASE_BASE=http://127.0.0.1:9/hermetic/dl" \
	"$@"

# The wrapper's ~/.satelle is an empty read-only directory: a process that
# resolves the home and writes under it fails at that write (permission
# denied). Afterwards the guard must be the same inode with the same mode and
# the same change time (ctime): any chmod, and any entry created or removed in
# it, moves ctime, which no process can set back, so a write cleaned up before
# exit, even with the mode restored, cannot pass unseen.
leak="$work/home/.satelle"
snap() { stat -c '%i %a %z' "$1" 2>/dev/null || stat -f '%i %Lp %Fc' "$1" 2>/dev/null; }
mkdir "$leak" && chmod 0555 "$leak" && guard=$(snap "$leak") && [ -n "$guard" ] || {
	rm -rf "$work"
	exit 2
}

# The command starts in the same directory, reached through its link when it
# lies under the caller's HOME, so PWD names no path of that home either.
here=$(hide "$(pwd)") && cd "$here" || {
	rm -rf "$work"
	exit 2
}
set -- "PWD=$here" "$@"

env -i "$@"
status=$?
if [ -L "$leak" ] || [ ! -d "$leak" ] || [ "$(snap "$leak")" != "$guard" ] || [ -n "$(ls -A "$leak" 2>/dev/null)" ]; then
	echo "hermetic: the run wrote to ~/.satelle (a satelle process resolved the home with no SATELLE_HOME of its own):" >&2
	if [ -d "$leak" ] && [ -n "$(ls -A "$leak" 2>/dev/null)" ]; then
		find "$leak" -mindepth 1 | sed "s|^$work/home/|~/|" | awk -v max=20 'NR <= max { print "  " $0 } END { if (NR > max) print "  ... and " NR - max " more" }' >&2
	else
		echo "  ~/.satelle: the read-only guard directory was removed, replaced, made writable or written into" >&2
	fi
	echo "hermetic: a test sets SATELLE_HOME to its own temp dir for every satelle process it starts" >&2
	[ "$status" -eq 0 ] && status=1
fi
# hostcheck fails the run when the caller's real satelle home $1 changed
# between the snapshot $2 and now, naming every path added, removed or changed.
hostcheck() {
	[ -n "$1" ] || return 0
	after=$(hostsnap "$1")
	[ "$after" = "$2" ] && return 0
	echo "hermetic: the run changed the caller's real satelle home $1 (a test wrote there by a path of its own):" >&2
	printf '%s\n%s\n' "$2" "$after" | LC_ALL=C sort | uniq -u | cut -d'|' -f1 | LC_ALL=C sort -u |
		awk -v dir="$1" -v max=20 'NR <= max { print "  " ($0 == "." || $0 == "absent" ? dir : dir "/" $0) } END { if (NR > max) print "  ... and " NR - max " more" }' >&2
	echo "hermetic: a test never writes outside its own temp dirs; give every satelle process it starts SATELLE_HOME under t.TempDir()" >&2
	[ "$status" -eq 0 ] && status=1
}
hostcheck "$real1" "$before1"
hostcheck "$real2" "$before2"
chmod -R u+w "$work" 2>/dev/null
rm -rf "$work"
exit "$status"
