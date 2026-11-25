#!/usr/bin/env bash
set -uo pipefail

tok=${GH_TOKEN:-}
unset GH_TOKEN

R=${SATELLE_RELEASE_ROOT:-/release}
IN=$R/in
WORK=$R/work
SUITE=$R/suite
OUT=$R/out
BACK=$R/back
LOG=$OUT/log
SLUG=orbelle/satelle
SAME=${SATELLE_RELEASE_TEST_SAME_UID:-}
POLL=${SATELLE_RELEASE_POLL:-5}
WAIT=${SATELLE_RELEASE_WAIT:-360}
GITC=(-c core.hooksPath=/dev/null -c core.fsmonitor=)
CRED=(-c credential.helper= -c 'credential.https://github.com.helper=!f(){ echo username=x-access-token; echo "password=$GH_TOKEN"; }; f')

usage() {
  echo "usage: release-node.sh run | input | clone | fetch | test | decide | bump | guard | publish | push | release | notes <ver> | dry-input <dir> <sha> <base>" >&2
  exit 2
}

say() { printf '%s\n' "$*" >>"$LOG"; }
STEP_AT=0
mark() { STEP_AT=$(stat -c %s "$LOG" 2>/dev/null || echo 0); }
rec() { printf '%s\n' "$*" >>"$OUT/result"; }
get() { [ -f "$OUT/result" ] && awk -v k="$1" '$1 == k { sub("^[^ ]+ ", ""); v = $0 } END { if (v != "") print v }' "$OUT/result"; }
in_get() { awk -v k="$1:" '$1 == k { sub("^[^:]+: *", ""); print; exit }' "$IN/run"; }

fail() {
  local step=$1
  shift
  local line="failed step $step: $*"
  if [ -d "$OUT" ]; then
    rec "$line"
    case $step in
      test | secret-scan) ;;
      *) [ -f "$LOG" ] && tail -c +"$((STEP_AT + 1))" "$LOG" | tail -n 40 >"$OUT/step-log" ;;
    esac
    if [ "$step" != secret-scan ] && scan_back; then
      cat "$BACK/result"
      exit 1
    fi
  fi
  printf '%s\n' "$line"
  exit 1
}

origin_ok() {
  case $1 in
    https://github.com/$SLUG | https://github.com/$SLUG.git) return 0 ;;
  esac
  return 1
}

g() { git -C "$WORK" "${GITC[@]}" "$@"; }
gnet() { GH_TOKEN="$tok" git -C "$WORK" "${GITC[@]}" "${CRED[@]}" "$@"; }
ghx() { GH_TOKEN="$tok" gh "$@" --repo "$SLUG"; }

check_origin() {
  local raw
  raw=$(g config --get remote.origin.url) || fail "$1" "origin is not https://github.com/$SLUG"
  origin_ok "$raw" || fail "$1" "origin is not https://github.com/$SLUG"
}

head_is() { [ "$(g rev-parse HEAD 2>/dev/null)" = "$1" ]; }

scan_back() {
  local t f
  t=$(mktemp -d "$OUT/back.XXXXXX") || return 1
  for f in result release-binary failing step-log; do
    [ -f "$OUT/$f" ] && cp "$OUT/$f" "$t/$f"
  done
  if [ -n "$tok" ]; then
    if grep -rqaF -f /dev/fd/3 "$t" 3< <(printf '%s\n' "$tok"); then
      rm -rf "$t"
      rec "failed step secret-scan: a record holds the token"
      printf '%s\n' "failed step secret-scan: a record holds the token"
      exit 1
    fi
    if grep -lqaF -f /dev/fd/3 /proc/[0-9]*/cmdline 3< <(printf '%s\n' "$tok") 2>/dev/null; then
      rm -rf "$t"
      rec "failed step secret-scan: a process's argv holds the token"
      printf '%s\n' "failed step secret-scan: a process's argv holds the token"
      exit 1
    fi
  fi
  echo "secret-scan clean" >>"$t/result"
  rm -rf "$BACK"
  mv "$t" "$BACK" && chmod 700 "$BACK"
}

step_input() {
  if [ "$(id -u)" != 0 ] && [ "$SAME" != 1 ]; then
    printf '%s\n' "failed step input: not root"
    exit 1
  fi
  mkdir -p "$R" && chmod 755 "$R" || { printf '%s\n' "failed step input: cannot make $R"; exit 1; }
  rm -rf "$OUT" "$BACK"
  mkdir -m 700 "$OUT" || { printf '%s\n' "failed step input: cannot make $OUT"; exit 1; }
  : >"$LOG"
  : >"$OUT/result"
  [ -n "$tok" ] || fail input "GH_TOKEN not given"
  [ -d "$IN" ] && [ ! -L "$IN" ] && [ -f "$IN/run" ] || fail input "no input at $IN/run"
  if [ "$SAME" != 1 ]; then
    chown -R -h root:root "$IN" || fail input "cannot take ownership of $IN"
  fi
  chmod -R go-rwx "$IN" || fail input "cannot close $IN"
  local f
  f=$(find "$IN" ! -type f ! -type d | head -n1)
  [ -z "$f" ] || fail input "$f is neither a file nor a directory"
  local run cand drift base branch repo mode author email
  run=$(in_get run)
  cand=$(in_get candidate)
  drift=$(in_get drift-checked)
  base=$(in_get base)
  branch=$(in_get branch)
  repo=$(in_get repo)
  mode=$(in_get mode)
  author=$(in_get author)
  email=$(in_get email)
  [[ $run =~ ^(exe_[0-9a-f]+|dry-[0-9a-f]+)$ ]] || fail input "run '$run' is not a run id"
  [[ $cand =~ ^[0-9a-f]{40}$ ]] || fail input "candidate '$cand' is not a commit"
  [[ $base =~ ^[0-9a-f]{40}$ ]] || fail input "base '$base' is not a commit"
  [ "$drift" = "$cand" ] || fail input "the drift gate has not passed on the candidate $cand"
  [ "$branch" = "release/$run" ] || fail input "branch '$branch' is not release/$run"
  origin_ok "$repo" || fail clone "origin is not https://github.com/$SLUG"
  case $mode in release | dry) ;; *) fail input "mode '$mode' is not release or dry" ;; esac
  [ -n "$author" ] && [ -n "$email" ] || fail input "no author or email"
  [ -f "$IN/kept" ] && [ -f "$IN/notes-ids" ] || fail input "no kept or notes-ids in $IN"
  local id
  for id in $(cat "$IN/notes-ids"); do
    [[ $id =~ ^[a-z]+_[0-9a-f]+$ ]] || fail input "notes id '$id' is not an item id"
  done
  while read -r id head; do
    [ -n "$id" ] || continue
    [[ $id =~ ^[a-z]+_[0-9a-f]+$ ]] && [[ $head =~ ^[0-9a-f]{40}$ ]] || fail input "kept line '$id $head' is not <id> <sha>"
  done <"$IN/kept"
  rec "run $run"
  rec "node-execution ${SATELLE_ITEM:-}"
  rec "candidate $cand"
  rec "mode $mode"
  if [ "$SAME" = 1 ]; then rec "privilege same-uid-test"; else rec "privilege release"; fi
}

step_clone() {
  local repo
  repo=$(in_get repo)
  origin_ok "$repo" || fail clone "origin is not https://github.com/$SLUG"
  rm -rf "$WORK" "$SUITE"
  mkdir -m 700 "$WORK" || fail clone "cannot make $WORK"
  GH_TOKEN="$tok" git "${GITC[@]}" "${CRED[@]}" clone -q --no-checkout "$repo" "$WORK" >>"$LOG" 2>&1 || fail clone "git clone $repo failed"
  chmod 700 "$WORK"
  check_origin clone
}

step_fetch() {
  local cand branch base head main
  cand=$(in_get candidate)
  branch=$(in_get branch)
  base=$(in_get base)
  check_origin fetch
  gnet fetch -q --tags origin "+refs/heads/main:refs/remotes/origin/main" "+refs/heads/$branch:refs/remotes/origin/$branch" >>"$LOG" 2>&1 ||
    fail fetch "git fetch $branch failed"
  head=$(g rev-parse -q --verify "refs/remotes/origin/$branch^{commit}") || fail fetch "$branch is not on origin"
  [ "$head" = "$cand" ] || fail fetch "$branch is at $head, not the candidate $cand"
  [ "$(in_get drift-checked)" = "$cand" ] || fail fetch "the drift gate has not passed on the candidate $cand"
  main=$(g rev-parse refs/remotes/origin/main) || fail fetch "origin has no main"
  g merge-base --is-ancestor "$main" "$cand" || fail fetch "the candidate $cand does not contain origin/main $main"
  g checkout -q --detach "$cand" >>"$LOG" 2>&1 || fail fetch "cannot check out $cand"
  rec "main-before $main"
  [ "$main" = "$base" ] || say "origin/main $main is not the run's base $base"
}

step_test() {
  local cand code hermetic=bwrap
  cand=$(in_get candidate)
  head_is "$cand" || fail test "$WORK is not at the candidate $cand"
  rm -rf "$SUITE"
  git "${GITC[@]}" clone -q --no-hardlinks --no-checkout "$WORK" "$SUITE" >>"$LOG" 2>&1 || fail test "cannot copy the candidate"
  git -C "$SUITE" "${GITC[@]}" checkout -q --detach "$cand" >>"$LOG" 2>&1 || fail test "cannot check out the candidate's copy"
  [ "$(git -C "$SUITE" "${GITC[@]}" rev-parse HEAD)" = "$cand" ] || fail test "the suite's copy is not the candidate"
  local runas=() home=${SATELLE_RELEASE_SUITE_HOME:-/home/release}
  if [ "$SAME" != 1 ]; then
    chown -R -h release:release "$SUITE" || fail test "cannot give the suite's copy to user release"
    runas=("$(command -v runuser)" -u release --)
    [ -n "${runas[0]}" ] || fail test "runuser is not on PATH"
  fi
  local senv=(PATH="${SATELLE_RELEASE_SUITE_PATH:-/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin}" HOME="$home" USER=release LANG=C.UTF-8 GOTOOLCHAIN=local GOFLAGS=-mod=readonly)
  if ! env -i "${senv[@]}" ${runas[@]+"${runas[@]}"} bwrap --dev-bind / / true >>"$LOG" 2>&1; then
    hermetic=snapshot
    senv+=(SATELLE_HERMETIC_ALLOW_SNAPSHOT=1)
  fi
  rec "hermetic $hermetic"
  rm -f "$OUT/failing"
  env -i "${senv[@]}" ${runas[@]+"${runas[@]}"} make -C "$SUITE" test 2>&1 | head -c 52428800 >"$OUT/suite.out"
  code=${PIPESTATUS[0]}
  rec "suite-exit $code"
  if [ "$code" != 0 ]; then
    grep -ao '^--- FAIL: [A-Za-z0-9_/.-]*' "$OUT/suite.out" | head -n 40 >"$OUT/failing"
    [ -s "$OUT/failing" ] || rm -f "$OUT/failing"
    fail test "suite-exit $code"
  fi
  rec "tested $cand"
}

step_decide() {
  local tested
  tested=$(get tested)
  [ -n "$tested" ] && head_is "$tested" || fail decide "the suite has not passed on the candidate"
  env -u GH_TOKEN bash "$WORK/scripts/release-binary.sh" decide "$WORK" >"$OUT/release-binary" 2>>"$LOG" || {
    rm -f "$OUT/release-binary"
    fail decide "the binary decision could not run"
  }
  rec "binary $(sed -n 's/^binary: //p' "$OUT/release-binary" | head -n1)"
}

nextver() {
  local p=$1 cur=$2 cand=$3 all others latest own
  all=$(g tag -l "$p*" | sed "s/^$p//" | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | sort -V)
  others=$(printf '%s\n' "$all" | grep -vxF "$cur" | tail -n1)
  own=$(g rev-parse -q --verify "refs/tags/$p$cur^{commit}" 2>/dev/null)
  if [ -n "$cur" ] && { [ -z "$others" ] || [ "$(printf '%s\n%s\n' "$others" "$cur" | sort -V | tail -n1)" = "$cur" ]; } &&
    { [ -z "$own" ] || [ "$own" = "$cand" ]; }; then
    echo "$cur"
    return 0
  fi
  latest=$(printf '%s\n' "$all" | tail -n1)
  [ -n "$latest" ] || latest=$cur
  [ -n "$latest" ] || return 1
  echo "${latest%.*}.$((${latest##*.} + 1))"
}

write_notes() {
  local ver=$1 notes="$WORK/docs/releases/v$1.md" args=() id ids
  ids=$(tr '\n' ' ' <"$IN/notes-ids")
  for id in $ids; do
    [ -f "$IN/changelog/$id" ] || { echo "no changelog attachment on $id: stop the run" >&2; return 1; }
    args+=("$id:$IN/changelog/$id")
  done
  if [ ${#args[@]} -eq 0 ]; then
    echo "every kept item was released before: no release notes are written"
  elif [ -e "$notes" ]; then
    echo "docs/releases/v$ver.md already exists (a resumed run)"
  else
    env -u GH_TOKEN bash "$WORK/scripts/changelog-items.sh" write "$notes" "$ver" "$(date -u +%Y-%m-%d)" "${args[@]}" || return 1
    echo "docs/releases/v$ver.md written from $ids"
  fi
}

vfield() { awk -v k="$1:" '$1 == k { print $2; exit }' "$WORK/.version"; }

step_bump() {
  local tested cur scur ver sver
  tested=$(get tested)
  [ -n "$tested" ] || fail bump "the suite has not passed on the candidate"
  cur=$(vfield satelle.version)
  scur=$(vfield satelled.version)
  if ! grep -qx 'binary: changed' "$OUT/release-binary" 2>/dev/null; then
    rec "version $cur"
    rec "sver $scur"
    return 0
  fi
  ver=$(nextver v "$cur" "$tested") || fail bump "no v* tag to bump from"
  sver=$(nextver serve-v "$scur" "$tested") || fail bump "no serve-v* tag to bump from"
  if [ "$ver" != "$cur" ] || [ "$sver" != "$scur" ]; then
    local stamp
    stamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    env -u GH_TOKEN awk -v v="$ver" -v s="$sver" -v b="$stamp" '
      $1 == "satelle.version:" { print "satelle.version: " v; next }
      $1 == "satelled.version:" { print "satelled.version: " s; next }
      $1 == "satelle.build:" { print "satelle.build: " b; next }
      { print }' "$WORK/.version" >"$OUT/version.new" && mv "$OUT/version.new" "$WORK/.version" || fail bump "cannot write .version"
  fi
  local notes="$WORK/docs/releases/v$ver.md"
  local wrote
  wrote=$(write_notes "$ver" 2>&1) || { say "$wrote"; fail bump "writing docs/releases/v$ver.md failed: $(printf '%s\n' "$wrote" | tail -n1)"; }
  say "$wrote"
  g add .version >>"$LOG" 2>&1 || fail bump "git add .version failed"
  [ -e "$notes" ] && { g add "docs/releases/v$ver.md" >>"$LOG" 2>&1 || fail bump "git add the notes failed"; }
  if ! g diff --cached --quiet; then
    local kept
    kept=$(awk 'NF { printf "%s%s", (n++ ? " " : ""), $1 }' "$IN/kept")
    env -u GH_TOKEN git -C "$WORK" "${GITC[@]}" -c user.name="$(in_get author)" -c user.email="$(in_get email)" \
      commit -q -m "chore(release): v$ver, satelled $sver ($(in_get run): $kept)" >>"$LOG" 2>&1 || fail bump "the bump commit failed"
  fi
  rec "version $ver"
  rec "sver $sver"
  rec "bump $(g rev-parse HEAD)"
}

step_guard() {
  local tested ver
  tested=$(get tested)
  [ -n "$tested" ] || fail guard "the candidate changed after the suite ran (or the suite has not passed on it)"
  [ "$(sed -n 's/^candidate: //p' "$OUT/release-binary" 2>/dev/null)" = "$tested" ] || fail guard "the run has not decided on the tested candidate $tested"
  if grep -qx 'binary: unchanged' "$OUT/release-binary"; then
    head_is "$tested" || fail guard "a binary-unchanged run pushes the tested candidate and nothing on top"
  else
    ver=$(vfield satelle.version)
    g merge-base --is-ancestor "$tested" HEAD && g diff --quiet "$tested" HEAD -- . ':!.version' ":!docs/releases/v$ver.md" ||
      fail guard "the candidate changed after the suite ran (or the suite has not passed on it)"
  fi
  [ "$(in_get drift-checked)" = "$tested" ] || fail guard "drift-check has not passed on the tested candidate $tested"
  g merge-base --is-ancestor "$(in_get base)" HEAD || fail guard "HEAD is not a fast-forward of the run's base"
}

main_now() {
  gnet fetch -q origin "+refs/heads/main:refs/remotes/origin/main" >>"$LOG" 2>&1 || return 1
  g rev-parse refs/remotes/origin/main
}

step_publish() {
  grep -qx 'binary: changed' "$OUT/release-binary" 2>/dev/null || return 0
  local base branch bump ver sver main run id i rid hsha conc url t sha n wcode
  base=$(in_get base)
  branch=$(in_get branch)
  bump=$(g rev-parse HEAD)
  ver=$(vfield satelle.version)
  sver=$(vfield satelled.version)
  check_origin publish
  main=$(main_now) || fail publish "git fetch main failed"
  [ "$main" = "$base" ] || fail publish "origin/main moved from $base to $main"
  gnet push -q origin "HEAD:refs/heads/$branch" >>"$LOG" 2>&1 || fail publish "git push $branch failed"
  ghx workflow run release.yml --ref "$branch" >>"$LOG" 2>&1 || fail publish "gh workflow run release.yml failed"
  id=
  for i in $(seq 1 60); do
    id=$(ghx run list --workflow release.yml --branch "$branch" --limit 10 --json databaseId,headSha 2>>"$LOG" |
      jq -r --arg h "$bump" '[.[] | select(.headSha == $h)][0].databaseId // empty')
    [ -n "$id" ] && break
    sleep "$POLL"
  done
  [ -n "$id" ] || fail publish "no release.yml run on $branch at $bump"
  ghx run watch "$id" --exit-status >>"$LOG" 2>&1
  wcode=$?
  conc=
  for i in $(seq 1 "$WAIT"); do
    rid=$(ghx run view "$id" --json url,headSha,status,conclusion 2>>"$LOG") || fail publish "gh run view $id failed"
    url=$(printf '%s' "$rid" | jq -r '.url // empty')
    hsha=$(printf '%s' "$rid" | jq -r '.headSha // empty')
    conc=$(printf '%s' "$rid" | jq -r '.conclusion // empty')
    [ "$(printf '%s' "$rid" | jq -r '.status // empty')" = completed ] && [ -n "$conc" ] && break
    conc=
    say "release.yml run $id has not concluded after the watch (exit $wcode); waiting"
    sleep "$POLL"
  done
  [ -n "$conc" ] || fail publish "the watch of release.yml run ${url:-$id} returned (exit $wcode) before the run concluded, and it had not concluded after $WAIT checks"
  rec "release-yml $url $hsha $conc"
  [ "$conc" = success ] && [ "$hsha" = "$bump" ] || fail publish "release.yml run $url concluded ${conc:-unknown} at ${hsha:-unknown}"
  gnet fetch -q --tags origin >>"$LOG" 2>&1 || fail publish "git fetch --tags failed"
  for t in "v$ver" "serve-v$sver"; do
    n=$(ghx release view "$t" --json assets --jq '.assets | length' 2>>"$LOG") || fail publish "no release $t"
    [ "${n:-0}" -gt 0 ] 2>/dev/null || fail publish "release $t has no assets"
    sha=$(g rev-parse -q --verify "refs/tags/$t^{commit}") || fail publish "no tag $t"
    rec "tag $t $sha"
  done
  [ "$(g rev-parse "refs/tags/v$ver^{commit}")" = "$bump" ] || fail publish "v$ver is not at the bump $bump"
}

step_push() {
  local base branch main
  base=$(in_get base)
  branch=$(in_get branch)
  check_origin push
  main=$(main_now) || fail push "git fetch main failed"
  [ "$main" = "$base" ] || fail push "origin/main moved from $base to $main"
  gnet push -q origin "HEAD:refs/heads/main" >>"$LOG" 2>&1 || fail push "git push main failed"
  rec "main-after $(g rev-parse HEAD)"
  if gnet push -q origin --delete "$branch" >>"$LOG" 2>&1; then rec "branch-deleted $branch"; else say "could not delete $branch"; fi
}

step_release() {
  local ver sver t d i ok latest pending=
  ver=$(vfield satelle.version)
  sver=$(vfield satelled.version)
  gnet fetch -q --tags origin >>"$LOG" 2>&1 || fail release "git fetch --tags failed"
  for t in "v$ver" "serve-v$sver"; do
    g rev-parse -q --verify "refs/tags/$t" >/dev/null || continue
    d=$(ghx release view "$t" --json isDraft --jq .isDraft 2>>"$LOG") || { pending="$pending $t"; continue; }
    [ "$d" = true ] || continue
    latest=--latest
    [ "$t" = "serve-v$sver" ] && latest=--latest=false
    ok=
    for i in 1 2 3; do
      if ghx release edit "$t" --draft=false "$latest" >>"$LOG" 2>&1; then
        ok=1
        break
      fi
      sleep "$POLL"
    done
    if [ -n "$ok" ]; then rec "published $t"; else pending="$pending $t"; fi
  done
  [ -z "$pending" ] && return 0
  for t in $pending; do rec "to-publish $t"; done
  fail release "main holds $(g rev-parse HEAD) and$pending is still a draft; a resumed run publishes it"
}

run_all() {
  mark
  step_input
  mark
  step_clone
  mark
  step_fetch
  mark
  step_test
  mark
  step_decide
  if [ "$(in_get mode)" = release ]; then
    mark
    step_bump
    mark
    step_guard
    mark
    step_publish
    mark
    step_push
    mark
    step_release
  fi
  scan_back
  cat "$BACK/result"
}

dry_input() {
  [ $# -eq 3 ] || usage
  local dir=$1 sha=$2 base=$3 run
  [[ $sha =~ ^[0-9a-f]{40}$ ]] && [[ $base =~ ^[0-9a-f]{40}$ ]] || usage
  run="dry-${sha:0:12}"
  rm -rf "$dir" && mkdir -p "$dir/changelog" || exit 1
  {
    echo "run: $run"
    echo "candidate: $sha"
    echo "drift-checked: $sha"
    echo "branch: release/$run"
    echo "ff: $sha"
    echo "base: $base"
    echo "repo: https://github.com/$SLUG.git"
    echo "author: satelle release"
    echo "email: release@satelle.invalid"
    echo "mode: dry"
  } >"$dir/run"
  : >"$dir/kept"
  : >"$dir/notes-ids"
  echo "release/$run"
}

case ${1:-} in
  run) run_all ;;
  input) mark; step_input ;;
  clone) mark; step_clone ;;
  fetch) mark; step_fetch ;;
  test) mark; step_test ;;
  decide) mark; step_decide ;;
  bump) mark; step_bump ;;
  guard) mark; step_guard ;;
  publish) mark; step_publish ;;
  push) mark; step_push ;;
  release) mark; step_release ;;
  notes) [ $# -eq 2 ] || usage; write_notes "$2" ;;
  dry-input) shift; dry_input "$@" ;;
  *) usage ;;
esac
