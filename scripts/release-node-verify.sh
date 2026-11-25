#!/usr/bin/env bash
set -uo pipefail

SAME=${SATELLE_RELEASE_TEST_SAME_UID:-}

usage() {
  echo "usage: release-node-verify.sh <rd> <node> <session>" >&2
  exit 2
}

verify() {
  [ $# -eq 3 ] || usage
  local rd=$1 node=$2 session=$3 back res bad=0 run cand base exe ver sver
  back="$rd/node-out/$node/back"
  res="$back/result"
  [ -f "$res" ] && [ ! -L "$res" ] || { echo "verify: no result at $res"; return 1; }
  v() { awk -v k="$1" '$1 == k { sub("^[^ ]+ ", ""); x = $0 } END { if (x != "") print x }' "$res"; }
  no() { echo "verify: $*"; bad=1; }
  run=$(v run)
  exe=$(basename "$rd" | sed -n 's/^satelle-release-//p')
  cand=$(cat "$rd/drift-checked" 2>/dev/null)
  base=$(sed -n 's/^base: //p' "$rd/node-in/run")
  local mainnow t d
  mainnow=$(git ls-remote origin refs/heads/main | cut -f1)
  if grep -q '^failed step ' "$res"; then
    echo "verdict reject"
    grep '^failed step ' "$res"
    if grep -q '^failed step release: ' "$res" && grep -q '^to-publish ' "$res" && [ -n "$(v main-after)" ]; then
      [ "$mainnow" = "$(v main-after)" ] || no "origin/main is $mainnow, not main-after $(v main-after)"
      for t in $(awk '$1 == "to-publish" { print $2 }' "$res"); do
        echo "to-publish $t: main holds $(v main-after) and $t is still a draft; fire again to publish it"
      done
    else
      [ "$mainnow" = "$base" ] || no "origin/main is $mainnow, not the run's base $base"
    fi
  else
    echo "verdict accept"
    [ "$run" = "$exe" ] || no "run is $run, not $exe"
    [ "$(v tested)" = "$cand" ] || no "tested is $(v tested), not the candidate $cand"
    [ "$mainnow" = "$(v main-after)" ] || no "origin/main is $mainnow, not main-after $(v main-after)"
    git fetch -q origin main 2>/dev/null
    ver=$(v version)
    sver=$(v sver)
    if git merge-base --is-ancestor "$cand" "$mainnow" 2>/dev/null; then
      if [ "$(v binary)" = unchanged ]; then
        [ "$mainnow" = "$cand" ] || no "a binary-unchanged run left main at $mainnow, not the candidate"
      elif ! printf '%s\n' "$ver" | grep -qxE '[0-9]+\.[0-9]+\.[0-9]+'; then
        no "version is not a plain x.y.z version"
      elif [ "$ver" != "$(git show "$mainnow:.version" 2>/dev/null | sed -n 's/^satelle\.version: *//p')" ]; then
        no "version $ver is not the satelle.version on origin/main $mainnow"
      elif ! git diff --quiet "$cand" "$mainnow" -- . ':!.version' ":!docs/releases/v$ver.md"; then
        no "main differs from the candidate outside .version and docs/releases/v$ver.md"
      fi
    else
      no "the candidate $cand is not under origin/main $mainnow"
    fi
    [ -z "$(git ls-remote origin "refs/heads/release/$exe")" ] || no "release/$exe is still on origin"
    for t in ${ver:+"v$ver"} ${sver:+"serve-v$sver"}; do
      if ! d=$(gh release view "$t" --json isDraft --jq .isDraft 2>/dev/null); then
        [ "$(v binary)" = unchanged ] || no "release $t cannot be read"
      elif [ "$d" != false ]; then
        no "release $t is still a draft"
      fi
    done
    [ "$(v secret-scan)" = clean ] || no "the secret scan is not clean"
  fi
  case $(v privilege) in
    release) ;;
    same-uid-test) [ "$SAME" = 1 ] || no "the node ran the suite as the same user (a test result)" ;;
    *) grep -q '^failed step input' "$res" || no "no privilege line" ;;
  esac
  local ne
  ne=$(v node-execution)
  if [ -n "$ne" ] && ! printf '%s\n' "$ne" | grep -qxE 'exe_[0-9a-f]+'; then
    no "node-execution $ne is not an execution id"
  elif [ -n "$ne" ]; then
    satelle ledger list "$ne" --json --session "$session" 2>/dev/null | jq -e --arg a "node:$node" 'any(.[]; .actor == $a)' >/dev/null ||
      no "the node execution $ne carries no event by node:$node"
  else
    no "no node-execution line"
  fi
  local sn
  sn="satelle-$(printf '%s' "$node" | tr '_' '-')"
  if ! command -v sprite >/dev/null 2>&1; then
    no "the sprite CLI is not on PATH, so $sn cannot be checked"
  elif ! sl=$(sprite list --prefix "$sn" 2>/dev/null); then
    no "sprite list failed, so $sn cannot be checked"
  elif printf '%s\n' "$sl" | grep -qx "$sn"; then
    no "the sprite $sn is still listed"
  else
    echo "sprite $sn is not listed"
  fi
  [ "$bad" = 0 ] && echo "verify: ok"
  return "$bad"
}

verify "$@"
