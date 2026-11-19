#!/usr/bin/env bash
set -uo pipefail

usage() {
  echo "usage: release-binary.sh decide <dir> | run <task> <item> | of <task> <item> | carried <task> <dir> <tested-sha>" >&2
  exit 2
}

rec_head=()
rec_reason=
rec_lines=()

finish() {
  echo "binary: $1"
  printf '%s\n' ${rec_head[@]+"${rec_head[@]}"}
  [ -n "$rec_reason" ] && echo "reason: $rec_reason"
  [ ${#rec_lines[@]} -gt 0 ] && printf '%s\n' ${rec_lines[@]+"${rec_lines[@]}"}
  exit 0
}

doubt() {
  rec_reason=$1
  shift
  rec_lines=("$@")
  finish changed
}

decide() {
  [ $# -eq 1 ] && [ -n "$1" ] || usage
  local dir=$1
  git -C "$dir" rev-parse --is-inside-work-tree >/dev/null 2>&1 || {
    echo "release-binary: $dir is not a git work tree" >&2
    exit 2
  }
  local cand
  cand=$(git -C "$dir" rev-parse HEAD 2>/dev/null) || {
    echo "release-binary: $dir has no HEAD commit" >&2
    exit 2
  }
  rec_head+=("candidate: $cand")

  local ver sver
  ver=$(awk '$1 == "satelle.version:" { print $2; exit }' "$dir/.version" 2>/dev/null)
  sver=$(awk '$1 == "satelled.version:" { print $2; exit }' "$dir/.version" 2>/dev/null)
  if [ -z "$ver" ] || [ -z "$sver" ]; then
    doubt ".version names no satelle.version or satelled.version" "build version .version"
  fi

  local tag sha bases=()
  for tag in "v$ver" "serve-v$sver"; do
    sha=$(git -C "$dir" rev-parse -q --verify "refs/tags/$tag^{commit}" 2>/dev/null)
    if [ -z "$sha" ] || ! git -C "$dir" merge-base --is-ancestor "$sha" HEAD 2>/dev/null; then
      doubt "$tag is not a published tag under the candidate" "build version .version"
    fi
    rec_head+=("base: $tag $sha")
    bases+=("$sha")
  done

  local changed
  changed=$(for sha in ${bases[@]+"${bases[@]}"}; do git -C "$dir" diff --name-only --no-renames "$sha" HEAD || echo "$(printf '\001')"; done | sort -u)
  case $changed in *$'\001'*) doubt "git diff from the tags failed" "build version .version" ;; esac
  local paths=() q
  if [ -n "$changed" ]; then
    while IFS= read -r q; do paths+=("$q"); done <<<"$changed"
  fi

  local wfline="build workflow .github/workflows/release.yml"
  local yml="$dir/.github/workflows/release.yml"
  [ -f "$yml" ] || doubt "release.yml is missing" "$wfline"
  local entries
  entries=$(awk '
    /^[[:space:]]*include:[[:space:]]*$/ { on = 1; next }
    on && /^[[:space:]]*(#.*)?$/ { next }
    on && /^[[:space:]]*-[[:space:]]*\{.*\}[[:space:]]*$/ { print; next }
    on { on = 0 }' "$yml")
  [ -n "$entries" ] || doubt "release.yml has no matrix include entry this decision reads (- { pkg: ..., goos: ..., goarch: ... })" "$wfline"
  local field e p o a pkgs=() targets=()
  field() { printf '%s\n' "$1" | sed -n "s/.*[{,][[:space:]]*$2:[[:space:]]*\\([^,}[:space:]]*\\).*/\\1/p"; }
  while IFS= read -r e; do
    p=$(field "$e" pkg); o=$(field "$e" goos); a=$(field "$e" goarch)
    if [ -z "$p" ] || [ -z "$o" ] || [ -z "$a" ]; then
      doubt "a release.yml include entry lacks pkg, goos or goarch: $(printf '%s' "$e" | sed 's/^[[:space:]]*//')" "$wfline"
    fi
    case " ${pkgs[*]:-} " in *" $p "*) ;; *) pkgs+=("$p") ;; esac
    case " ${targets[*]:-} " in *" $o/$a "*) ;; *) targets+=("$o/$a") ;; esac
  done <<<"$entries"
  local cgo gover
  cgo=$(sed -n "s/^[[:space:]]*CGO_ENABLED:[[:space:]]*['\"]\\{0,1\\}\\([0-9]\\)['\"]\\{0,1\\}[[:space:]]*\$/\\1/p" "$yml" | head -1)
  gover=$(sed -n "s/^[[:space:]]*go-version:[[:space:]]*['\"]\\{0,1\\}\\([0-9][0-9.]*\\)['\"]\\{0,1\\}[[:space:]]*\$/\\1/p" "$yml" | head -1)
  [ -n "$cgo" ] || doubt "release.yml sets no CGO_ENABLED this decision reads" "$wfline"
  [ -n "$gover" ] || doubt "release.yml sets no go-version this decision reads" "$wfline"
  local name
  for name in -tags -mod= -modfile -overlay GOFLAGS GOEXPERIMENT GOAMD64 GOARM GOARM64 GO386 GOWORK; do
    grep -qF -- "$name" "$yml" && doubt "release.yml sets $name, which the decision does not reproduce" "$wfline"
  done

  local underived=()
  for q in ${paths[@]+"${paths[@]}"}; do underived+=("build underived $q"); done
  local goenv
  err=$(mktemp) || doubt "the build set could not be derived: mktemp failed" ${underived[@]+"${underived[@]}"}
  trap 'rm -f "$err"' EXIT
  goenv=$(go env GOPATH GOMODCACHE GOCACHE GOPROXY GONOPROXY GOPRIVATE GONOSUMDB GOSUMDB 2>"$err") ||
    doubt "the build set could not be derived: go env: $(tr '\n' ' ' <"$err")" ${underived[@]+"${underived[@]}"}
  local gv=()
  while IFS= read -r q; do gv+=("$q"); done <<<"$goenv"
  local clean=(env -i "PATH=$PATH" "HOME=${HOME:-}" "TMPDIR=${TMPDIR:-/tmp}" GOENV=off
    "GOPATH=${gv[0]:-}" "GOMODCACHE=${gv[1]:-}" "GOCACHE=${gv[2]:-}" "GOPROXY=${gv[3]:-}"
    "GONOPROXY=${gv[4]:-}" "GOPRIVATE=${gv[5]:-}" "GONOSUMDB=${gv[6]:-}" "GOSUMDB=${gv[7]:-}"
    "CGO_ENABLED=$cgo")

  local first=${targets[0]} goversion mm want
  goversion=$(${clean[@]+"${clean[@]}"} "GOOS=${first%/*}" "GOARCH=${first#*/}" go env GOVERSION 2>"$err") ||
    doubt "the build set could not be derived: go env GOVERSION: $(tr '\n' ' ' <"$err")" ${underived[@]+"${underived[@]}"}
  rec_head+=("go: $goversion" "targets: ${targets[*]:-}")
  mm=$(printf '%s' "$goversion" | sed -n 's/^go\([0-9]*\.[0-9]*\).*/\1/p')
  want=$(printf '%s' "$gover" | sed -n 's/^\([0-9]*\.[0-9]*\).*/\1/p')
  [ -n "$want" ] || want=$gover
  [ -n "$mm" ] && [ "$mm" = "$want" ] ||
    doubt "release.yml builds with go $gover; this machine lists with ${mm:-$goversion}" "$wfline"

  local t files all=
  for t in ${targets[@]+"${targets[@]}"}; do
    files=$(cd "$dir" && ${clean[@]+"${clean[@]}"} "GOOS=${t%/*}" "GOARCH=${t#*/}" bash "$dir/scripts/check-serve-version.sh" --files ${pkgs[@]+"${pkgs[@]}"} 2>"$err") ||
      doubt "the build set could not be derived: $t: $(tr '\n' ' ' <"$err" | sed 's/[[:space:]]*$//')" ${underived[@]+"${underived[@]}"}
    all="$all$files"$'\n'
  done
  local built excl pats
  built=$(printf '%s' "$all" | awk -F'\t' '$1 == "built" { print $2 }' | sort -u)
  excl=$(printf '%s' "$all" | awk -F'\t' '$1 == "excluded" { print $2 }' | sort -u)
  pats=$(printf '%s' "$all" | awk -F'\t' '$1 == "pattern" { print $2 }' | sort -u)
  local dirs
  dirs=$(printf '%s\n%s\n' "$built" "$excl" | awk 'NF { n = split($0, s, "/"); d = "."; if (n > 1) { d = s[1]; for (i = 2; i < n; i++) d = d "/" s[i] } print d }' | sort -u)

  local path rule present inarea d pat builds=() others=()
  for path in ${paths[@]+"${paths[@]}"}; do
    present=0
    git -C "$dir" cat-file -e "HEAD:$path" 2>/dev/null && present=1
    inarea=0
    while IFS= read -r pat; do
      [ -n "$pat" ] || continue
      # shellcheck disable=SC2053
      if [ "$path" = "$pat" ] || [ "${path#"$pat"/}" != "$path" ] || [[ $path == $pat ]]; then inarea=1; break; fi
    done <<<"$pats"
    rule=
    if grep -qxF -- "$path" <<<"$built"; then
      rule=built
    elif grep -qxF -- "$path" <<<"$excl"; then
      rule=excluded
    elif [ "$present" = 0 ] && [ "$inarea" = 1 ]; then
      rule=deleted
    elif [ "$present" = 0 ] && [ "${path%_test.go}" = "$path" ]; then
      while IFS= read -r d; do
        [ -n "$d" ] || continue
        if [ "$d" = . ] || [ "${path#"$d"/}" != "$path" ]; then rule=deleted; break; fi
      done <<<"$dirs"
    fi
    if [ -z "$rule" ]; then
      if [ "$present" = 1 ] && [ "$inarea" = 1 ]; then
        rule=embed
      else
        case $path in
          go.mod | go.sum | go.work | go.work.sum | vendor/*) rule=module ;;
          .version) rule=version ;;
          .github/workflows/release.yml) rule=workflow ;;
        esac
      fi
    fi
    if [ -n "$rule" ]; then builds+=("build $rule $path"); else others+=("other $path"); fi
  done
  rec_lines=(${builds[@]+"${builds[@]}"} ${others[@]+"${others[@]}"})

  if [ ${#builds[@]} -gt 0 ]; then finish changed; fi
  finish unchanged
}

run_of() {
  local list exes exe led rc
  list=$(satelle execution list --parent "$1" --status in_progress --json 2>&1) || { echo "release-binary: satelle execution list --parent $1: $list" >&2; return 2; }
  exes=$(printf '%s' "$list" | jq -r '.[].id' 2>&1) || { echo "release-binary: cannot read the runs of $1: $exes" >&2; return 2; }
  for exe in $exes; do
    led=$(satelle ledger list "$exe" --kind task.run --json 2>&1) || { echo "release-binary: satelle ledger list $exe: $led" >&2; return 2; }
    printf '%s' "$led" | jq -e --arg id "$2" 'any(.[]; .payload.inputs | index($id))' >/dev/null 2>&1
    rc=$?
    [ "$rc" = 0 ] && { echo "$exe"; return 0; }
    [ "$rc" = 1 ] || { echo "release-binary: cannot read $exe's task.run inputs" >&2; return 2; }
  done
  return 1
}

run_mode() {
  [ $# -eq 2 ] && [ -n "$1" ] && [ -n "$2" ] || usage
  run_of "$1" "$2"
  exit $?
}

of_mode() {
  [ $# -eq 2 ] && [ -n "$1" ] && [ -n "$2" ] || usage
  local exe rc js body
  exe=$(run_of "$1" "$2" 2>&1)
  rc=$?
  case $rc in
    0) ;;
    1) printf 'run: none\nbinary: unrecorded\n'; exit 0 ;;
    *) echo "$exe"; exit 1 ;;
  esac
  js=$(satelle execution get "$exe" --json 2>&1) || { echo "cannot read $exe: $js"; exit 1; }
  if ! printf '%s' "$js" | jq -e 'any(.attachments[]?; .name == "release-binary")' >/dev/null 2>&1; then
    printf 'run: %s\nbinary: unrecorded\n' "$exe"
    exit 0
  fi
  body=$(satelle execution attachment "$exe" release-binary 2>&1) || { echo "cannot read $exe's release-binary attachment: $body"; exit 1; }
  printf 'run: %s\n%s\n' "$exe" "$body"
}

carried() {
  [ $# -eq 3 ] && [ -n "$1" ] && [ -n "$2" ] && [ -n "$3" ] || usage
  local task=$1 dir=$2 tested=$3
  fail() { echo "release-binary carried: $*" >&2; exit 2; }
  local tags pub=
  tags=$(git -C "$dir" tag -l 'v[0-9]*' --merged "$tested" 2>&1) || fail "git tag --merged $tested: $tags"
  pub=$(printf '%s\n' "$tags" | sed -n 's/^v//p' | sort -V | tail -1)
  local list runs
  list=$(satelle execution list --parent "$task" --json 2>&1) || fail "satelle execution list --parent $task: $list"
  runs=$(printf '%s' "$list" | jq -r '[.[] | select(.status == "done" or .status == "cancelled")] | sort_by(.created_at) | reverse | .[] | "\(.id) \(.status)"' 2>&1) ||
    fail "cannot read the runs of $task: $runs"
  local blocks=() exe st js names hasbin haskept rec kept id head block rc
  while read -r exe st; do
    [ -n "$exe" ] || continue
    js=$(satelle execution get "$exe" --json 2>&1) || fail "satelle execution get $exe: $js"
    names=$(printf '%s' "$js" | jq -r '.attachments[]?.name' 2>&1) || fail "cannot read $exe's attachments: $names"
    hasbin=0; haskept=0
    grep -qx release-binary <<<"$names" && hasbin=1
    grep -qx release-kept <<<"$names" && haskept=1
    rec=
    if [ "$hasbin" = 1 ]; then
      rec=$(satelle execution attachment "$exe" release-binary 2>&1) || fail "cannot read $exe's release-binary: $rec"
    fi
    if [ "$st" = done ]; then
      if grep -qx 'binary: changed' <<<"$rec" || { [ "$haskept" = 1 ] && [ "$hasbin" = 0 ]; }; then
        break
      fi
    fi
    grep -qx 'binary: unchanged' <<<"$rec" || continue
    [ "$haskept" = 1 ] || continue
    kept=$(satelle execution attachment "$exe" release-kept 2>&1) || fail "cannot read $exe's release-kept: $kept"
    block=
    while read -r id head; do
      [ -n "$id" ] && [ -n "$head" ] || continue
      git -C "$dir" merge-base --is-ancestor "$head" "$tested" 2>/dev/null
      rc=$?
      [ "$rc" = 0 ] || { [ "$rc" = 1 ] && continue; fail "git merge-base $head $tested failed"; }
      if [ -n "$pub" ]; then
        git -C "$dir" merge-base --is-ancestor "$head" "v$pub" 2>/dev/null
        rc=$?
        [ "$rc" = 1 ] || { [ "$rc" = 0 ] && continue; fail "git merge-base $head v$pub failed"; }
      fi
      block="$block$id"$'\n'
    done <<<"$kept"
    blocks+=("$block")
  done <<<"$runs"
  local i out=
  for ((i = ${#blocks[@]} - 1; i >= 0; i--)); do out="$out${blocks[$i]}"; done
  printf '%s' "$out" | awk 'NF && !seen[$0]++'
}

case "${1:-}" in
  decide) shift; decide "$@" ;;
  run) shift; run_mode "$@" ;;
  of) shift; of_mode "$@" ;;
  carried) shift; carried "$@" ;;
  *) usage ;;
esac
