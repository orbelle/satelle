---
name: satelle-block-resolution-check
type: check
description: Refuses a block-breaker run's move to done unless its block-resolution is well formed (a held story id, and either an answerable ruling with a change and an answer for each dissent, or a blocked ruling with its reason), the run carries the held story's record, and the held story carries the same block-resolution. Deterministic, no agent; reads the ledger and attachments.
---

# Block resolution

A block-breaker run (`.satelle/tasks/block-breaker.md`) ends with a
`block-resolution` its breaker returned, attached to the run. Before the run
is done, the held story must carry it too, since the driving session acts on
the story, and the resolution must be in the form `block-breaker.md` gives:

- a `held: <id>` line naming the held story, which is an item on the ledger
  and whose own record the run carries as its `held-item` attachment (its
  first word is that id);
- `ruling: answerable`, with at least one `change:` line, each `plan`,
  `driver-note` or `story-amendment`, and an `answer:` line for each; or
- `ruling: blocked`, with a non-empty `reason:` line;
- the held story's newest `block-resolution` attachment is the same body as
  the run's (equal sha256): copy it with
  `satelle execution attachment <run> block-resolution --session <session>`
  piped to `satelle story attach <held> --name block-resolution --type
  text/markdown --file - --session <session>`.

```sh
set -u
for tool in jq satelle; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the resolution cannot be read"; exit 1; }
done
run=$SATELLE_ITEM
kindof() {
  satelle ledger list "$1" --json </dev/null 2>/dev/null | jq -r '[(. // [])[] | select(.kind == "item.created")][0].payload.kind // empty' 2>/dev/null
}
rkind=$(kindof "$run")
if [ -z "$rkind" ]; then
  echo "$run: no item.created event on its ledger"
  exit 1
fi
res=$(satelle "$rkind" attachment "$run" block-resolution </dev/null 2>/dev/null) || res=
if [ -z "$res" ]; then
  echo "$run: no block-resolution attachment; perform the run's breaker, which returns it"
  exit 1
fi
field() { printf '%s\n' "$res" | sed -n "s/^$1: *//p"; }
held=$(field held | head -n 1 | tr -d '[:space:]')
if [ -z "$held" ]; then
  echo "$run: the block-resolution has no 'held: <id>' line"
  exit 1
fi
hkind=$(kindof "$held")
if [ -z "$hkind" ]; then
  echo "$run: the block-resolution names $held, which is not an item on the ledger"
  exit 1
fi
first=$(satelle "$rkind" attachment "$run" held-item </dev/null 2>/dev/null | awk 'NF { print $1; exit }') || first=
if [ "$first" != "$held" ]; then
  echo "$run: its held-item attachment is not the record of $held (first word '${first:-none}'); attach the held story's record as held-item (.satelle/tasks/block-breaker.md)"
  exit 1
fi
ruling=$(field ruling | head -n 1 | tr -d '[:space:]')
case "$ruling" in
  answerable)
    changes=$(printf '%s\n' "$res" | grep -c '^change:' || true)
    answers=$(printf '%s\n' "$res" | grep -c '^answer: *[^[:space:]]' || true)
    bad=$(printf '%s\n' "$res" | grep '^change:' | grep -Ev '^change: *(plan|driver-note|story-amendment) *$' || true)
    if [ "$changes" -eq 0 ]; then
      echo "$run: ruling answerable names no change: give each dissent a 'change:' and an 'answer:' line"
      exit 1
    fi
    if [ -n "$bad" ]; then
      echo "$run: a change is none of plan, driver-note, story-amendment:"
      printf '%s\n' "$bad"
      exit 1
    fi
    if [ "$answers" -ne "$changes" ]; then
      echo "$run: $changes change line(s) but $answers non-empty answer line(s); each change needs its answer"
      exit 1
    fi
    ;;
  blocked)
    if [ -z "$(field reason | tr -d '[:space:]')" ]; then
      echo "$run: ruling blocked gives no 'reason:' why no change within the story answers the dissent"
      exit 1
    fi
    ;;
  *)
    echo "$run: the block-resolution's ruling is '${ruling:-none}', not answerable or blocked"
    exit 1
    ;;
esac
sha() {
  satelle "$1" attachments "$2" --json </dev/null 2>/dev/null | jq -r '[(. // [])[] | select(.name == "block-resolution")] | last | .sha256 // empty' 2>/dev/null
}
rsum=$(sha "$rkind" "$run")
hsum=$(sha "$hkind" "$held")
if [ -z "$hsum" ] || [ "$hsum" != "$rsum" ]; then
  echo "$run: $held does not carry this run's block-resolution; copy it: satelle $rkind attachment $run block-resolution | satelle $hkind attach $held --name block-resolution --type text/markdown --file -"
  exit 1
fi
echo "$run: block-resolution for $held, ruling $ruling, attached to $held"
```
