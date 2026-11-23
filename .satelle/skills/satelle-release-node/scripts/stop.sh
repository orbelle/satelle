#!/bin/sh
# Brings back /release/back only, by the rules of provider.untar
# (internal/remote/provider/command.go): regular files and directories under
# back, no absolute path and no .. component, extracted owner-less into a fresh
# temp dir before anything reads it. Destroying the sprite never depends on it.
here=$(cd "$(dirname "$0")" && pwd)
sprite=$here/../../satelle-node-sprite/scripts
. "$sprite/name.sh"
node=$1
n=$(sprite_name "$node")

refuse() {
  echo "release-stop: $*; nothing of $node is brought back" >&2
  return 1
}

bring_back() {
  dst=$SATELLE_RELEASE_OUT/$node
  t=$(mktemp -d) || return 1
  trap 'rm -rf "$t"' EXIT
  sprite exec -s "$n" -- sudo tar -C /release -cf - back </dev/null 2>/dev/null | head -c 1048576 >"$t/back.tar"
  [ -s "$t/back.tar" ] || { refuse "the node sent no back archive"; return 1; }
  tar -tvf "$t/back.tar" >"$t/list" 2>/dev/null || { refuse "the back archive does not list"; return 1; }
  if cut -c1 "$t/list" | grep -qv '^[-d]$'; then refuse "the back archive holds a member that is not a file or directory"; return 1; fi
  tar -tf "$t/back.tar" >"$t/names" 2>/dev/null || { refuse "the back archive does not list"; return 1; }
  while IFS= read -r m; do
    case $m in
      /*) refuse "the back archive holds an absolute path"; return 1 ;;
      back | back/ | back/*) ;;
      *) refuse "the back archive holds a path outside back"; return 1 ;;
    esac
    case /$m/ in
      */../*) refuse "the back archive holds a .. path"; return 1 ;;
    esac
  done <"$t/names"
  mkdir "$t/x" || return 1
  tar -xf "$t/back.tar" -C "$t/x" --no-same-owner --no-same-permissions 2>/dev/null || { refuse "the back archive does not extract"; return 1; }
  [ -z "$(find "$t/x" ! -type f ! -type d | head -n1)" ] || { refuse "the extracted back holds a member that is not a file or directory"; return 1; }
  [ -d "$t/x/back" ] || { refuse "the back archive holds no back"; return 1; }
  rm -rf "$dst"
  mkdir -p "$dst" && mv "$t/x/back" "$dst/back"
}

if [ -n "${SATELLE_RELEASE_OUT:-}" ]; then
  (bring_back) || true
fi
exec sh "$sprite/stop.sh" "$node"
