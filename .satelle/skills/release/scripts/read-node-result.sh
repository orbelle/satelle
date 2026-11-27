session=<session>
task=tas_42301d36
exe=$(satelle execution list --parent "$task" --status in_progress --json --session "$session" | jq -r 'if length == 1 then .[0].id else empty end')
[ -n "$exe" ] || { echo "no single in-progress run of $task" >&2; exit 1; }
rd="${TMPDIR:-/tmp}/satelle-release-$exe"; wt="$rd/candidate"
sub=${SATELLE_SUBSTRATE_DIR:-$(git rev-parse --show-toplevel)/.satelle}
ntask=$(sed -n 's/^task = "\(tas_[0-9a-f]*\)"$/\1/p' "$sub/remotes/release-sprite/remote.toml")
st=$(satelle remote status release-sprite "$ntask" --json --session "$session") || exit 1
node=$(printf '%s' "$st" | jq -r '.node // empty')
verdict=$(printf '%s' "$st" | jq -r '.verdict // empty')
line=$(printf '%s' "$st" | jq -r '"state \(.state) node \(.node) verdict \(.verdict) \(.reason // "")"')
back="$rd/node-out/$node/back"
iso="$rd.isolate"
shaped() {
  local p f=()
  for p in "$@"; do [ -e "$p" ] && f+=("$p"); done
  [ "${#f[@]}" -gt 0 ] || return 1
  grep -rlE 'gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}' "${f[@]}"
  case $? in
    0) echo "a token-shaped string is in the files above: nothing is attached" >&2; return 0 ;;
    1) return 1 ;;
    *) echo "the token scan could not read ${f[*]}: nothing is attached" >&2; return 0 ;;
  esac
}
v=$(bash scripts/release-node-verify.sh "$rd" "$node" "$session" 2>&1); vcode=$?
printf '%s\n' "$v"
if [ "$verdict" = accept ] && [ "$vcode" = 0 ] && ! grep -q '^failed step ' "$back/result"; then
  { cat "$back/result"; echo "$line"; printf '%s\n' "$v"; } >"$rd/release-node"
  shaped "$back" "$rd/release-node" && exit 1
  satelle execution attach "$exe" --name release-binary --type text/plain --file "$back/release-binary" --session "$session" || exit 1
  satelle execution attach "$exe" --name release-node --type text/plain --file "$rd/release-node" --session "$session" || exit 1
  ma=$(sed -n 's/^main-after //p' "$back/result")
  if [ -s "$iso/out" ]; then
    failing=$(paste -sd ' ' "$iso/failing" 2>/dev/null)
    left=$(paste -sd ' ' "$iso/out")
    echo "trial $(cat "$iso/trial"): left out $left: passed" >>"$iso/log"
    while read -r id <&3; do
      [ -n "$id" ] || continue
      if [ "$(satelle story get "$id" --json --session "$session" | jq -r .status)" != approved ]; then echo "$id: not approved, not moved"; continue; fi
      printf 'the release candidate of %s failed its suite (%s); leaving out %s made it pass, and the rest released; merge origin/main into the branch (git fetch origin && git merge origin/main; never rebase, so the push needs no force), fix the failing tests and come through integration again\n' "$exe" "${failing:-see release-isolation}" "$left" \
        | satelle story attach "$id" --name release-sendback --file - --session "$session" || exit 1
      satelle story set "$id" --status in_progress --session "$session" || exit 1
      echo "$id: sent back: leaving it out made the suite pass"
    done 3<"$iso/out"
    { echo "the suite failed on the first candidate of $exe; failing:"; cat "$iso/failing" 2>/dev/null; cat "$iso/log"; } >"$iso/record"
    shaped "$iso/record" && exit 1
    satelle execution attach "$exe" --name release-isolation --type text/plain --file "$iso/record" --session "$session" || exit 1
  fi
  rm -rf "$iso"
  git worktree remove --force "$wt"
  rm -rf "$rd"
  echo "the node's part landed: main is $ma"
  exit 0
fi
if [ "$verdict" = accept ] || grep -q '^main-after ' "$back/result" 2>/dev/null; then
  echo "the node concluded accept but verify refused it (above) or its records did not come back whole; main may have moved: cancel the run (satelle execution set $exe --status cancelled --session <session>) and fire again; the next run carries the landed items as contained" >&2
  exit 1
fi
if [ "$vcode" = 0 ] && [ "$(sed -n 's/^failed step \([a-z-]*\): .*/\1/p' "$back/result" 2>/dev/null | head -n1)" = test ]; then
  if [ ! -d "$iso" ]; then
    mkdir -p "$iso" || exit 1
    ff=$(cat "$rd/ff")
    tac "$rd/kept" | while read -r id head; do git merge-base --is-ancestor "$head" "$ff" || echo "$id"; done >"$iso/suspects"
    if [ -f "$back/failing" ]; then cp "$back/failing" "$iso/failing"; else : >"$iso/failing"; fi
    echo 0 >"$iso/trial"
  elif [ -s "$iso/out" ]; then
    echo "trial $(cat "$iso/trial"): left out $(paste -sd ' ' "$iso/out"): the suite failed" >>"$iso/log"
  fi
  n=$(grep -c . "$iso/suspects")
  t=$(( $(cat "$iso/trial") + 1 ))
  if [ "$n" -gt 0 ] && [ "$t" -le $(( 2 * n - 1 )) ]; then
    if [ "$t" -le "$n" ]; then sed -n "${t}p" "$iso/suspects" >"$iso/out"; else head -n $(( t - n + 1 )) "$iso/suspects" >"$iso/out"; fi
    echo "$t" >"$iso/trial"
    if [ -n "$(git ls-remote origin "refs/heads/release/$exe")" ]; then git push -q origin --delete "release/$exe"; fi
    git worktree remove --force "$wt"
    rm -rf "$rd"
    echo "isolate: the suite failed; trial $t of $(( 2 * n - 1 )) leaves out $(paste -sd ' ' "$iso/out"): build the candidate again (step 2a) and fire" >&2
    exit 3
  fi
fi
{
  echo "the node's part of $exe did not land: nothing is pushed to main and no item moved"
  echo "$line"
  [ -f "$back/result" ] && cat "$back/result"
  printf '%s\n' "$v"
  [ -f "$back/failing" ] && { echo "failing:"; cat "$back/failing"; }
  [ -f "$back/step-log" ] && { echo "step-log (the failed step's last lines on the node):"; cat "$back/step-log"; }
  [ -f "$iso/log" ] && { echo "isolation (no candidate without the suspects passed):"; echo "first failing:"; cat "$iso/failing"; cat "$iso/log"; }
  ff=$(cat "$rd/ff")
  while read -r id head; do
    git merge-base --is-ancestor "$head" "$ff" && continue
    echo "suspect $id: printf '%s\n' 'the release candidate $(cat "$rd/drift-checked") failed in run $exe (release-evidence); merge origin/main into the branch (git fetch origin && git merge origin/main; never rebase, so the push needs no force) and come through integration again' | satelle story attach $id --name release-sendback --file - --session <session> && satelle story set $id --status in_progress --session <session>"
  done <"$rd/kept"
} >"$rd/release-evidence"
shaped "$rd/release-evidence" "$back" && exit 1
satelle execution attach "$exe" --name release-evidence --type text/plain --file "$rd/release-evidence" --session "$session" || exit 1
if [ -n "$(git ls-remote origin "refs/heads/release/$exe")" ]; then git push -q origin --delete "release/$exe"; fi
satelle execution set "$exe" --status cancelled --session "$session" || exit 1
cat "$rd/release-evidence"
git worktree remove --force "$wt"
rm -rf "$rd" "$iso"
echo "the node's part was rejected: $exe is cancelled and its items stay approved" >&2
exit 1
