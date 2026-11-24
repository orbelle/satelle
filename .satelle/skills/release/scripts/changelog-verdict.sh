session=<session>
id=<id>
for tool in git satelle bash; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not on PATH, so the changelog of $id cannot be judged" >&2; exit 1; }
done
why=
if ! cl=$(satelle story attachment "$id" changelog --session "$session" 2>/dev/null); then
  why="no changelog attachment"
elif ! git cat-file -e "$head:scripts/changelog-items.sh" 2>/dev/null; then
  why="head has no scripts/changelog-items.sh"
elif ! out=$(printf '%s\n' "$cl" | bash <(git show "$head:scripts/changelog-items.sh") lines "$id" 2>&1 >/dev/null); then
  why=${out:-the attachment is refused}
fi
paths=
if [ -z "$why" ]; then
  diff=$(git diff --no-renames --name-status "$tip...$head" -- CHANGELOG.md docs/releases) || { echo "git diff $tip...$head failed, so the changelog of $id cannot be judged" >&2; exit 1; }
  cur=$(git show "$tip:.version" 2>/dev/null | awk '$1 == "satelle.version:" { print $2; exit }')
  while IFS=$'\t' read -r st p; do
    [ -n "$p" ] || continue
    case "$st:$p" in
      D:CHANGELOG.md) continue ;;
      A:docs/releases/v*.md)
        v=${p#docs/releases/v}
        v=${v%.md}
        if [ -n "$cur" ] && [[ $v =~ ^[0-9]+(\.[0-9]+)*$ ]] && [ "$v" != "$cur" ] &&
          [ "$(printf '%s\n%s\n' "$v" "$cur" | sort -V | head -n 1)" = "$v" ] &&
          ! git cat-file -e "$tip:$p" 2>/dev/null; then
          continue
        fi
        ;;
    esac
    paths="${paths:+$paths }$p"
  done <<<"$diff"
fi
if [ -n "$why" ]; then
  echo "$id: send back: no valid changelog attachment on head $head: merge origin/main into the branch (git fetch origin && git merge origin/main; never rebase, so the push needs no force) and record the lines with satelle story attach $id --name changelog --file - --session <session> ($why)"
elif [ -n "$paths" ]; then
  cmds=
  for p in $paths; do
    if git cat-file -e "$tip:$p" 2>/dev/null; then c="git checkout origin/main -- $p"; else c="git rm $p"; fi
    cmds="${cmds:+$cmds && }$c"
  done
  echo "$id: send back: head $head edits $paths; restore them ($cmds), move the lines to the changelog attachment, and merge origin/main into the branch (git fetch origin && git merge origin/main; never rebase, so the push needs no force)"
else
  echo "$id: keep head $head"
fi
