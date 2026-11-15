#!/bin/sh

ALLOW="TERM TMPDIR SATELLE_BIN SATELLE_CHROME GOFLAGS GOPROXY GOTOOLCHAIN GONOSUMDB GOPRIVATE GONOPROXY GOSUMDB SATELLE_HERMETIC_ALLOW_SNAPSHOT SATELLE_HERMETIC_FORCE_NO_BWRAP"

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

hidebase="/tmp/satelle-hermetic-$(id -u)"
mkdir -m 0700 "$hidebase" 2>/dev/null
if [ -L "$hidebase" ] || [ ! -d "$hidebase" ] || [ ! -O "$hidebase" ] || ! chmod 0700 "$hidebase"; then
	echo "hermetic: $hidebase is not a directory owned by this user; failing closed" >&2
	exit 2
fi
for l in $(find "$hidebase" -mindepth 1 -maxdepth 1 -type l -mmin +60 2>/dev/null); do
	[ -e "$l" ] || rm -f "$l"
done
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

real1=${SATELLE_HOME:-}
real2=
[ -n "${HOME:-}" ] && [ "$HOME/.satelle" != "$real1" ] && real2="$HOME/.satelle"

if stat -c %n . >/dev/null 2>&1; then
	statfmt="-c"
	statspec='%n|%F|%s|%y|%z|%i'
	stattime='%y'
else
	statfmt="-f"
	statspec='%N|%HT|%z|%Fm|%Fc|%i'
	stattime='%Fm'
fi
case "$(stat $statfmt "$stattime" / 2>/dev/null)" in
*.*) statprec="nanosecond timestamps" ;;
*) statprec="whole-second timestamps" ;;
esac
if command -v sha256sum >/dev/null 2>&1; then
	hashcmd="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
	hashcmd="shasum -a 256"
else
	hashcmd="cksum"
fi
hostsnap() {
	[ -n "$1" ] || return 0
	if [ ! -e "$1" ] && [ ! -L "$1" ]; then
		echo "absent"
		return 0
	fi
	echo ".|$(stat -L $statfmt "$(echo "$statspec" | cut -d'|' -f2)" "$1" 2>/dev/null)"
	(cd "$1" && find . -mindepth 1 -exec stat $statfmt "$statspec" {} + 2>/dev/null) |
		sed 's|^[.]/||' | LC_ALL=C sort |
		while IFS= read -r line; do
			rest=${line#*|}
			case "${rest%%|*}" in
			"regular file" | "regular empty file" | "Regular File")
				sum=$($hashcmd <"$1/${line%%|*}" 2>/dev/null | cut -d' ' -f1)
				printf '%s|%s\n' "$line" "$sum"
				;;
			*) printf '%s\n' "$line" ;;
			esac
		done
}

mode=snapshot
why="bwrap is missing (SATELLE_HERMETIC_FORCE_NO_BWRAP is set)"
if [ -z "${SATELLE_HERMETIC_FORCE_NO_BWRAP:-}" ]; then
	why="bwrap is missing"
	if bwrap=$(command -v bwrap 2>/dev/null) && [ -n "$bwrap" ]; then
		why="bwrap cannot run here (bwrap --dev-bind / / true failed)"
		"$bwrap" --dev-bind / / true >/dev/null 2>&1 && mode=bwrap
	fi
fi

robind() {
	if [ -e "$1" ]; then
		p=$(cd "$1" 2>/dev/null && pwd -P) || p=$1
		printf '%s\n%s\n%s\n' --ro-bind "$p" "$p"
		return 0
	fi
	a=$(dirname "$1")
	while [ ! -e "$a" ]; do a=$(dirname "$a"); done
	a=$(cd "$a" && pwd -P) || return 1
	[ "$a" != / ] || return 1
	printf '%s\n%s\n%s\n' --ro-bind "$a" "$a"
	for e in "$a"/* "$a"/.[!.]* "$a"/..?*; do
		[ -e "$e" ] && [ ! -L "$e" ] || continue
		printf '%s\n%s\n%s\n' --bind "$e" "$e"
	done
}
binds=
if [ "$mode" = bwrap ]; then
	for h in "$real1" "$real2"; do
		[ -n "$h" ] || continue
		case "$h" in *"
"*) mode=snapshot why="a satelle home path holds a newline" ;; esac
		b=$(robind "$h") || {
			mode=snapshot
			why="$h is absent and its nearest existing parent is /"
			break
		}
		binds="$binds$b
"
	done
fi
if [ "$mode" = snapshot ] && [ -z "${SATELLE_HERMETIC_ALLOW_SNAPSHOT:-}" ]; then
	echo "hermetic: refusing to run: $why, so the caller's satelle home cannot be made read-only; install bubblewrap, or set SATELLE_HERMETIC_ALLOW_SNAPSHOT=1 to run in detection-only snapshot mode" >&2
	exit 2
fi
if [ "$mode" = bwrap ]; then
	echo "hermetic: mode bwrap: the caller's satelle home is read-only for the run ($(printf %s "$real1 $real2" | sed 's/^ //; s/ $//'))" >&2
else
	echo "hermetic: mode snapshot ($why; SATELLE_HERMETIC_ALLOW_SNAPSHOT is set): detection of any change to the caller's satelle home, writes are not prevented; every path is compared after the run ($statprec, content hashes), no path exempt, so a satelle running on this host can fail the run by writing its own home" >&2
	before1=$(hostsnap "$real1")
	before2=$(hostsnap "$real2")
fi

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

leak="$work/home/.satelle"
snap() { stat -c '%i %a %z' "$1" 2>/dev/null || stat -f '%i %Lp %Fc' "$1" 2>/dev/null; }
mkdir "$leak" && chmod 0555 "$leak" && guard=$(snap "$leak") && [ -n "$guard" ] || {
	rm -rf "$work"
	exit 2
}

here=$(hide "$(pwd)") && cd "$here" || {
	rm -rf "$work"
	exit 2
}
set -- "PWD=$here" "$@"

set -- env -i "$@"
if [ "$mode" = bwrap ]; then
	set -- -- "$@"
	rev=$(printf '%s' "$binds" | awk 'NF || length { l[n++] = $0 } END { for (i = n - 1; i >= 0; i--) print l[i] }')
	oldifs=$IFS
	IFS='
'
	set -f
	for a in $rev; do
		set -- "$a" "$@"
	done
	set +f
	IFS=$oldifs
	set -- "$bwrap" --dev-bind / / "$@"
fi
"$@"
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
if [ "$mode" = snapshot ]; then
	hostcheck "$real1" "$before1"
	hostcheck "$real2" "$before2"
fi
chmod -R u+w "$work" 2>/dev/null
rm -rf "$work"
exit "$status"
