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
  local mainnow t d tags pub
  mainnow=$(git ls-remote origin refs/heads/main | cut -f1)
  git fetch -q origin main 2>/dev/null
  mainver() { git show "$mainnow:.version" 2>/dev/null | awk -v k="$1:" '$1 == k { print $2; exit }'; }
  plain() { printf '%s\n' "$1" | grep -qxE '[0-9]+\.[0-9]+\.[0-9]+'; }
  landed() {
    [ "$run" = "$exe" ] || no "run is $run, not $exe"
    [ "$(v tested)" = "$cand" ] || no "tested is $(v tested), not the candidate $cand"
    [ "$mainnow" = "$(v main-after)" ] || no "origin/main is $mainnow, not main-after $(v main-after)"
    ver=$(mainver satelle.version)
    sver=$(mainver satelled.version)
    if git merge-base --is-ancestor "$cand" "$mainnow" 2>/dev/null; then
      if [ "$(v binary)" = unchanged ]; then
        [ "$mainnow" = "$cand" ] || no "a binary-unchanged run left main at $mainnow, not the candidate"
      elif ! plain "$ver" || ! plain "$sver"; then
        no "the .version on origin/main $mainnow does not hold plain x.y.z versions"
      elif [ "$(v version)" != "$ver" ]; then
        no "version $(v version) is not the satelle.version $ver on origin/main $mainnow"
      elif ! git diff --quiet "$cand" "$mainnow" -- . ':!.version' ":!docs/releases/v$ver.md"; then
        no "main differs from the candidate outside .version and docs/releases/v$ver.md"
      fi
    else
      no "the candidate $cand is not under origin/main $mainnow"
    fi
    local rb
    if ! rb=$(git ls-remote origin "refs/heads/release/$exe"); then
      no "git ls-remote origin failed, so release/$exe cannot be checked"
    elif [ -n "$rb" ]; then
      no "release/$exe is still on origin"
    fi
    [ "$(v secret-scan)" = clean ] || no "the secret scan is not clean"
    tags=
    plain "$ver" && tags="v$ver"
    plain "$sver" && tags="$tags serve-v$sver"
  }
  draft() {
    local tr
    if [ "$(v binary)" = unchanged ]; then
      if ! tr=$(git ls-remote --tags origin "refs/tags/$1"); then
        echo unreadable
        return
      fi
      if [ -z "$tr" ]; then
        echo none
        return
      fi
    fi
    if ! d=$(gh release view "$1" --json isDraft --jq .isDraft 2>/dev/null); then
      echo unreadable
    else
      echo "$d"
    fi
  }
  if grep -q '^failed step ' "$res"; then
    echo "verdict reject"
    grep '^failed step ' "$res"
    if grep -q '^failed step release: ' "$res" && grep -q '^to-publish ' "$res" && [ -n "$(v main-after)" ]; then
      landed
      pub=$(awk '$1 == "to-publish" { print $2 }' "$res" | sort -u)
      for t in $tags; do
        d=$(draft "$t")
        case $d in
          true)
            printf '%s\n' "$pub" | grep -qxF -- "$t" || no "release $t is still a draft but the result does not name it to-publish"
            echo "to-publish $t: main holds $mainnow and $t is still a draft; fire again to publish it" ;;
          false) ;;
          none) ;;
          *) no "release $t cannot be read" ;;
        esac
      done
      printf '%s\n' "$pub" | while IFS= read -r t; do
        case " $tags " in *" $t "*) ;; *) echo "verify: to-publish names $t, which is not a release of origin/main $mainnow" ;; esac
      done | grep . && bad=1
    else
      [ "$mainnow" = "$base" ] || no "origin/main is $mainnow, not the run's base $base"
    fi
  else
    echo "verdict accept"
    landed
    for t in $tags; do
      d=$(draft "$t")
      case $d in
        false | none) ;;
        true) no "release $t is still a draft" ;;
        *) no "release $t cannot be read" ;;
      esac
    done
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
