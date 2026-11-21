---
name: satelle-drift-ownership-check
type: check
description: Doctor check judging open drift items against open non-drift stories tagged drift-owns:<drift-key>. Exits 0 printing one note per owned key when every drift-key of every open drift item is owned by an open story; exits 1 naming each open drift item and its unowned keys (or no drift-key) otherwise; exits 2 when open stories cannot be read. Deterministic, no agent.
---

# Drift ownership (owned drift-keys report as notes; unowned keys raise issues)

An open story without the `drift` tag claims ownership of a carried drift
finding by carrying the tag `drift-owns:<drift-key>`, where `<drift-key>` is
the exact trimmed string after `drift-key: ` in the open drift item's body:

`satelle story set <id> --add-tag "drift-owns:<drift-key>" --session <session>`

Exit semantics for `satelle doctor --session <session>`:

- Exit `0`: every `drift-key: ` line of every open `drift` story is owned by at
  least one open non-drift story. Prints one line per owned key
  (`<key> (<drift-id>) owned by <owner-id> (<owner-status>)`, joining multiple
  owners with `, `), or nothing when no open drift item exists. Doctor prints
  each line as a note and raises no drift issue.
- Exit `1`: at least one open `drift` story has an unowned `drift-key: ` line
  or has no `drift-key: ` line at all. Prints one line per offending drift item
  naming the item and each unowned key (or `no drift-key`), omitting owned-key
  notes so doctor raises only issues.
- Exit `2`: `satelle` or `jq` is missing, or open stories could not be listed
  or parsed; doctor reports `could not check`.

`drift-warning: ` lines never block and produce neither notes nor issues.
Ownership suppresses only doctor issues and never closes a drift item; closure
is judged by `.satelle/skills/satelle-drift-resolved-check.md`. When an owning
story reaches a terminal state (`done`, `cancelled`, or another terminal state
of its workflow) while the key is still carried, the key becomes unowned again.

```sh
set -u
for tool in satelle jq; do
  command -v "$tool" >/dev/null 2>&1 || { printf '%s is not installed\n' "$tool"; exit 2; }
done
if ! stories=$(satelle story list --open --json </dev/null 2>&1); then
  printf 'cannot list open stories: %s\n' "$stories"
  exit 2
fi
if ! lines=$(printf '%s\n' "$stories" | jq -r '
  def uniq: reduce .[] as $x ([]; if any(.[]; . == $x) then . else . + [$x] end);
  def trim: sub("\\A[ \t\r\n]+"; "") | sub("[ \t\r\n]+\\z"; "");
  (. // [])
  | if type != "array" then error("expected JSON array") else . end
  | . as $all
  | [$all[] | select(any(.tags[]?; . == "drift"))] as $drift
  | [$all[] | select(all(.tags[]?; . != "drift"))] as $non_drift
  | (reduce $non_drift[] as $s (
      {};
      reduce (
        ($s.tags // [])
        | map(select(type == "string" and startswith("drift-owns:")) | ltrimstr("drift-owns:") | trim | select(length > 0))
        | uniq
        | .[]
      ) as $k (
        .;
        if any((.[$k] // [])[]; .id == $s.id) then .
        else .[$k] = ((.[$k] // []) + [{id: $s.id, status: $s.status}])
        end
      )
    )) as $owners
  | $drift[]
  | . as $d
  | (($d.title // "") | gsub("[\r\n]+"; " ") | trim) as $title
  | (($d.body // "")
      | split("\n")
      | map(sub("\r$"; "") | select(startswith("drift-key: ")) | ltrimstr("drift-key: ") | trim | select(length > 0))
      | uniq) as $keys
  | if ($keys | length) == 0 then
      "I\t" + ($d.id // "") + " is " + ($d.status // "") + ": " + $title + " (no drift-key)"
    else
      ([$keys[] | select((($owners[.] // []) | length) == 0)]) as $unowned
      | (
          ($keys[] | . as $k | ($owners[$k] // []) as $os | select(($os | length) > 0)
            | ($os | sort_by(.id) | map((.id // "") + " (" + (.status // "") + ")") | join(", ")) as $who
            | "N\t" + $k + " (" + ($d.id // "") + ") owned by " + $who),
          (if ($unowned | length) > 0 then
            "I\t" + ($d.id // "") + " is " + ($d.status // "") + ": " + $title + "; unowned: " + ($unowned | join(", "))
          else
            empty
          end)
        )
    end
' 2>&1); then
  printf 'cannot parse open stories: %s\n' "$lines"
  exit 2
fi
[ -n "$lines" ] || exit 0
tab=$(printf '\t')
issues=$(printf '%s\n' "$lines" | sed -n "s/^I${tab}//p")
if [ -n "$issues" ]; then
  printf '%s\n' "$issues"
  exit 1
fi
notes=$(printf '%s\n' "$lines" | sed -n "s/^N${tab}//p")
if [ -n "$notes" ]; then
  printf '%s\n' "$notes"
fi
exit 0
```
