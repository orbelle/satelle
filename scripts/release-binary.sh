#!/usr/bin/env bash
# release-binary — the sole owner of a release run's binary decision
# (sto_da9775c1): does the tested candidate change the shipped satelle or
# satelled binaries, the record of that decision, and how a gate reads it.
# The release run (.satelle/skills/release.md, ### Decide: did the binary
# change) writes the record and attaches it to the run as `release-binary`;
# on `binary: unchanged` main fast-forwards to the tested candidate with no
# bump, tag, release.yml dispatch or install. The approved -> done gates
# (version-stamp, changelog-entry, ci-published, release-installed) read the
# record through `of`; release-run finds the run through `run`.
#
#   decide <dir>                  print the record for the candidate checked
#                                 out at <dir> (its HEAD)
#   run <task> <item>             print the in-progress run of <task> whose
#                                 task.run inputs list <item>
#   of <task> <item>              print `run: <exe>` and that run's
#                                 release-binary record
#   carried <task> <dir> <tested-sha>
#                                 print the items of binary-unchanged runs a
#                                 binary-changed run writes before its own,
#                                 oldest first
#
# decide. Exit 0 with a record once <dir> is a git work tree; exit 2 only on
# a usage error (no <dir>, or <dir> is not a git work tree). It has no
# cannot-decide exit: whatever it cannot establish is `binary: changed` with a
# `reason:` line (a needless version is acceptable, a missed one is not).
#   1. .version's satelle.version and satelled.version; either missing:
#      reason, `build version .version`.
#   2. The bases are the tags v<ver> and serve-v<sver>; either not a commit,
#      or not an ancestor of HEAD: reason, `build version .version`.
#   3. The changed paths: the union of `git diff --name-only --no-renames
#      <tag> HEAD` over both tags (a rename lists both paths).
#   4. The build configuration, read from <dir>/.github/workflows/release.yml:
#      the matrix.include entries (flow style, `- { ... }`) give the distinct
#      `pkg` values and `goos goarch` pairs, the build step's CGO_ENABLED and
#      setup-go's go-version. A missing file, no entry, an entry without pkg,
#      goos or goarch, no CGO_ENABLED or go-version, or a build setting this
#      decision does not reproduce (-tags, -mod=, -modfile, -overlay, GOFLAGS,
#      GOEXPERIMENT, GOAMD64, GOARM, GOARM64, GO386, GOWORK): reason,
#      `build workflow .github/workflows/release.yml`.
#   5. Every go call runs in the shipped build's clean environment:
#      `env -i PATH HOME TMPDIR GOENV=off` plus the module and cache locations
#      read once from the caller (`go env GOPATH GOMODCACHE GOCACHE GOPROXY
#      GONOPROXY GOPRIVATE GONOSUMDB GOSUMDB`) and GOOS, GOARCH and
#      CGO_ENABLED from release.yml. GOFLAGS, build tags and `go env -w`
#      settings never reach it.
#   6. That environment's GOVERSION major.minor must be release.yml's
#      go-version major.minor, else reason, `build workflow ...`.
#   7. For each target, scripts/check-serve-version.sh --files <pkgs> (the
#      owner of what a binary is built from). A path is built when any target
#      builds it, else excluded when any target excludes it; the `pattern`
#      lines of all targets are the embed areas. Any failure: reason, one
#      `build underived <path>` line per changed path.
#   8. Each changed path gets exactly one rule. A path is present (in HEAD's
#      tree) or absent. It is in an embed area when it equals a pattern, lies
#      under one, or matches one as a shell glob. Tried in order:
#        built     built for some target (always present)
#        excluded  excluded by a build constraint (always present)
#        deleted   absent, and in an embed area whatever its name, or not a
#                  _test.go file and under the directory of a built or
#                  excluded file (subdirectories included)
#        embed     present, in an embed area, on no built line (a `_` or `.`
#                  name a directory embed skips)
#        module    go.mod, go.sum, go.work, go.work.sum, vendor/**
#        version   .version
#        workflow  .github/workflows/release.yml
#        other     anything else (_test.go outside an embed area, .satelle/**,
#                  scripts/**, docs, Makefile)
#      deleted takes only absent paths and embed only present ones. The embed
#      areas are the candidate's own patterns: a pattern only the base had was
#      removed by editing a //go:embed line (a built file) or by removing its
#      directory (go list fails: underived).
#   9. `binary: changed` when any path matched rules 1 to 7, else
#      `binary: unchanged` (printed only when steps 1 to 7 all succeeded).
#
# The record (text/plain):
#   binary: changed|unchanged
#   candidate: <sha>
#   base: v<ver> <sha>
#   base: serve-v<sver> <sha>
#   go: <GOVERSION>
#   targets: <goos>/<goarch> ...
#   reason: <text>                only when a step could not be completed
#   build <rule> <path>
#   other <path>
# Without a reason line every changed path is on exactly one build or other
# line. A record with a reason carries the deciding lines that step names and
# omits the header lines it could not establish.
#
# run: exit 1 printing nothing when no in-progress run lists the item; exit 2
# when a satelle or jq call fails.
#
# of: `run: <exe>` then the run's latest release-binary attachment; a run
# without one prints `binary: unrecorded`, and no run prints `run: none` and
# `binary: unrecorded` (both exit 0: the gates apply their full checks). A
# failed read exits 1 with the reason. Presence is read from the attachment
# list, never inferred from a failed read.
#
# carried: the published base is the highest v[0-9]* tag merged into
# <tested-sha> (.version is never read, so step 3's bump cannot move it). It
# walks the task's done and cancelled runs newest first and stops at the first
# done run whose record says binary: changed, or that has release-kept and no
# release-binary (a full release from before this decision existed). Of each
# binary-unchanged run before the stop it keeps each release-kept item whose
# head is an ancestor of <tested-sha> and not of the published base. Output is
# oldest run first, each run's items in landing order, deduplicated. A satelle,
# jq or git failure exits 2 and prints nothing.
set -uo pipefail

usage() {
  echo "usage: release-binary.sh decide <dir> | run <task> <item> | of <task> <item> | carried <task> <dir> <tested-sha>" >&2
  exit 2
}

# ---- decide ----------------------------------------------------------------

rec_head=()  # header lines after binary:
rec_reason=
rec_lines=() # build/other lines

finish() { # $1 changed|unchanged
  echo "binary: $1"
  printf '%s\n' "${rec_head[@]}"
  [ -n "$rec_reason" ] && echo "reason: $rec_reason"
  [ ${#rec_lines[@]} -gt 0 ] && printf '%s\n' "${rec_lines[@]}"
  exit 0
}

# doubt <reason> <line>...: a step that could not be completed is a change.
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

  # 1. .version
  local ver sver
  ver=$(awk '$1 == "satelle.version:" { print $2; exit }' "$dir/.version" 2>/dev/null)
  sver=$(awk '$1 == "satelled.version:" { print $2; exit }' "$dir/.version" 2>/dev/null)
  if [ -z "$ver" ] || [ -z "$sver" ]; then
    doubt ".version names no satelle.version or satelled.version" "build version .version"
  fi

  # 2. the published tags under the candidate
  local tag sha bases=()
  for tag in "v$ver" "serve-v$sver"; do
    sha=$(git -C "$dir" rev-parse -q --verify "refs/tags/$tag^{commit}" 2>/dev/null)
    if [ -z "$sha" ] || ! git -C "$dir" merge-base --is-ancestor "$sha" HEAD 2>/dev/null; then
      doubt "$tag is not a published tag under the candidate" "build version .version"
    fi
    rec_head+=("base: $tag $sha")
    bases+=("$sha")
  done

  # 3. the changed paths
  local changed
  changed=$(for sha in "${bases[@]}"; do git -C "$dir" diff --name-only --no-renames "$sha" HEAD || echo "$(printf '\001')"; done | sort -u)
  case $changed in *$'\001'*) doubt "git diff from the tags failed" "build version .version" ;; esac
  local paths=()
  [ -n "$changed" ] && mapfile -t paths <<<"$changed"

  # 4. release.yml
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
    case " ${pkgs[*]} " in *" $p "*) ;; *) pkgs+=("$p") ;; esac
    case " ${targets[*]} " in *" $o/$a "*) ;; *) targets+=("$o/$a") ;; esac
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

  # 5. the clean environment
  local underived=() q
  for q in "${paths[@]}"; do underived+=("build underived $q"); done
  local goenv
  err=$(mktemp) || doubt "the build set could not be derived: mktemp failed" "${underived[@]}"
  trap 'rm -f "$err"' EXIT
  goenv=$(go env GOPATH GOMODCACHE GOCACHE GOPROXY GONOPROXY GOPRIVATE GONOSUMDB GOSUMDB 2>"$err") ||
    doubt "the build set could not be derived: go env: $(tr '\n' ' ' <"$err")" "${underived[@]}"
  local gv=()
  mapfile -t gv <<<"$goenv"
  local clean=(env -i "PATH=$PATH" "HOME=${HOME:-}" "TMPDIR=${TMPDIR:-/tmp}" GOENV=off
    "GOPATH=${gv[0]:-}" "GOMODCACHE=${gv[1]:-}" "GOCACHE=${gv[2]:-}" "GOPROXY=${gv[3]:-}"
    "GONOPROXY=${gv[4]:-}" "GOPRIVATE=${gv[5]:-}" "GONOSUMDB=${gv[6]:-}" "GOSUMDB=${gv[7]:-}"
    "CGO_ENABLED=$cgo")

  # 6. the toolchain
  local first=${targets[0]} goversion mm want
  goversion=$("${clean[@]}" "GOOS=${first%/*}" "GOARCH=${first#*/}" go env GOVERSION 2>"$err") ||
    doubt "the build set could not be derived: go env GOVERSION: $(tr '\n' ' ' <"$err")" "${underived[@]}"
  rec_head+=("go: $goversion" "targets: ${targets[*]}")
  mm=$(printf '%s' "$goversion" | sed -n 's/^go\([0-9]*\.[0-9]*\).*/\1/p')
  want=$(printf '%s' "$gover" | sed -n 's/^\([0-9]*\.[0-9]*\).*/\1/p')
  [ -n "$want" ] || want=$gover
  [ -n "$mm" ] && [ "$mm" = "$want" ] ||
    doubt "release.yml builds with go $gover; this machine lists with ${mm:-$goversion}" "$wfline"

  # 7. the build set, per target
  local t files all=
  for t in "${targets[@]}"; do
    files=$(cd "$dir" && "${clean[@]}" "GOOS=${t%/*}" "GOARCH=${t#*/}" bash "$dir/scripts/check-serve-version.sh" --files "${pkgs[@]}" 2>"$err") ||
      doubt "the build set could not be derived: $t: $(tr '\n' ' ' <"$err" | sed 's/[[:space:]]*$//')" "${underived[@]}"
    all="$all$files"$'\n'
  done
  local built excl pats
  built=$(printf '%s' "$all" | awk -F'\t' '$1 == "built" { print $2 }' | sort -u)
  excl=$(printf '%s' "$all" | awk -F'\t' '$1 == "excluded" { print $2 }' | sort -u)
  pats=$(printf '%s' "$all" | awk -F'\t' '$1 == "pattern" { print $2 }' | sort -u)
  local dirs
  dirs=$(printf '%s\n%s\n' "$built" "$excl" | awk 'NF { n = split($0, s, "/"); d = "."; if (n > 1) { d = s[1]; for (i = 2; i < n; i++) d = d "/" s[i] } print d }' | sort -u)

  # 8. one rule per path
  local path rule present inarea d pat builds=() others=()
  for path in "${paths[@]}"; do
    present=0
    git -C "$dir" cat-file -e "HEAD:$path" 2>/dev/null && present=1
    inarea=0
    while IFS= read -r pat; do
      [ -n "$pat" ] || continue
      # shellcheck disable=SC2053 # a pattern is matched as a glob on purpose
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
  rec_lines=("${builds[@]}" "${others[@]}")

  # 9.
  if [ ${#builds[@]} -gt 0 ]; then finish changed; fi
  finish unchanged
}

# ---- run, of ---------------------------------------------------------------

# run_of <task> <item>: the in-progress run listing item; return 1 when none,
# 2 when a read fails.
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

# ---- carried ---------------------------------------------------------------

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
