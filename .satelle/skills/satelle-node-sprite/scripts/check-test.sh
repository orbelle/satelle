#!/bin/sh
set -u
here=$(cd "$(dirname "$0")" && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
fail=0
stub() {
  printf '#!/bin/sh\n%s\n' "$1" >"$t/sprite"
  chmod +x "$t/sprite"
}
expect() {
  out=$(PATH="$t:/usr/bin:/bin" sh "$here/check.sh" 2>&1)
  code=$?
  if [ "$code" -ne "$1" ] || ! printf '%s\n' "$out" | grep -q "$2"; then
    echo "FAIL $3: exit $code, output: $out"
    fail=1
  else
    echo "ok   $3"
  fi
}
stub 'echo "Error: no organizations configured, run sprite login" >&2; exit 1'
expect 1 'sprite login' "a CLI with no organisation is refused"
stub 'echo "API endpoints:"; exit 0'
expect 1 'not signed in' "a CLI that selects no organisation is refused"
stub 'case "$1" in org) echo "Currently selected org: example";; create) echo created >"$0.created";; esac'
expect 0 'Currently selected org: example' "a signed-in CLI passes"
rm -f "$t/sprite"
expect 1 'not on PATH' "no sprite CLI is refused"
[ ! -e "$t/sprite.created" ] || { echo "FAIL check.sh created a sprite"; fail=1; }
exit $fail
