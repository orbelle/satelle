---
name: satelle-judge-differs-from-performer-check
type: check
description: Fails a gate where no judge is of another model family (claude, gpt, gemini, grok) than the performer of the state the move leaves; a single judge must differ, a panel passes when one member differs, and a session performer has no model in config and is not compared. Opt-in, deterministic, no agent; reads satelle status, satelle workflow show, agents.toml and the [review] tier panels of satelle.toml.
---

# Judge differs from performer (reviewer independence)

A model shares its author's blind spots, and a smaller model of the same
family shares most of them. This check makes sure every judged gate has at
least one judge of another family than the agent that did the work it judges.

**Opt-in.** satelle does not wire it. A repo names this skill as the `skill`
of a gate with no `agent` and no `panel` (a check gate), on the transitions
it chooses, typically the one that judges a change to its workflows or agents.
It judges the configuration in the gate's working directory, so it sees the
presented change to workflows or agents. It reads nothing of the item.

**The rule.** For every transition of every workflow, for each from-state
whose performer is a dispatched agent, and for each gate of that transition
with judges (an `agent`, or a `panel`), at least one judge is of another
family than the performer. A single judge must differ. A panel passes when
one member differs. A check gate has no judge and is skipped. A `*` from
stands for every state that is neither terminal nor park, except a state
another transition to the same target names explicitly (that transition
wins).

**Family** is the vendor the binding's `model` names: claude, gpt, gemini,
grok. The table is `FAMILIES` at the top of the script, one `family pattern`
pair per line, matched in order against the lower-cased name. It is this
repo's configuration: edit it in the repo's copy. Two names for one model
fall in one family, so they are not taken as differing.

- A binding with no model, or a model that names no family, takes its family
  from its command's basename (`claude`, `grok`, `codex`, `gemini`).
- A name that matches no family is `unknown`. An unknown judge does not count
  as differing, and an unknown performer fails its gates. Both lines say to
  add the name to `FAMILIES`.

**A session performer** (`command = "session"`) has no model in config and
is not compared: its harness's model is the author, and whoever binds the
judges keeps them alt to it. The check prints one note per such state.

**Read.** `satelle status --json --session <session>` (`root`, the `.satelle` directory, and
`workflows`), `satelle workflow show <w> --json --session <session>` for each workflow, and
`agents.toml`, then `agents.local.toml` when present, read field by field
over it, so the check sees what would dispatch on this machine. The agents
reader takes only `[agents.NAME]` tables and in them only `command` and
`model` with a plain double-quoted value; any other form of those keys
stops the check (exit 2) naming the file and line. `satelle validate
--session <session>` stays the judge of the file's validity.
The `[review.panels.*]` tables of `satelle.toml` are read too (see Tier
panels).

**Tier panels.** A `[review.panels.GATE]` table in satelle.toml replaces
that gate's judges with the panel of the item's tier. Each tier's panel is
judged like the gate's own judges, as one more judge set of that gate, and a
finding names the tier. The reader takes each tier as `tier = ["agent", ...]`
on one line of plain double-quoted names; any other form in a
`[review.panels.GATE]` table stops the check (exit 2) naming the line.

**Exit codes:** 0 every judged gate has a judge of another family; 1 a
finding, one line per gate and from-state; 2 the configuration could not be
read (or jq or awk is missing).

```check
#!/usr/bin/env bash
set -uo pipefail

FAMILIES='claude claude|opus|sonnet|haiku|fable|mythos
gpt gpt|codex|(^|[^a-z])o[0-9]
gemini gemini
grok grok'

for tool in jq awk satelle; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed, so the bindings cannot be compared"; exit 2; }
done

if ! status=$(satelle status --json </dev/null 2>&1); then
  echo "the repo's status could not be read: $status"
  exit 2
fi
root=$(printf '%s' "$status" | jq -r '.root // empty' 2>/dev/null)
if [ -z "$root" ]; then
  echo "the repo's status names no root"
  exit 2
fi
workflows=$(printf '%s' "$status" | jq -r '(.workflows // [])[]' 2>/dev/null) || { echo "the repo's status has no workflows list"; exit 2; }

files=("$root/agents.toml")
[ -f "$root/agents.local.toml" ] && files+=("$root/agents.local.toml")
if [ ! -f "$root/agents.toml" ]; then
  echo "$root/agents.toml does not exist"
  exit 2
fi

rows=$(awk '
  function bad(why) { printf "%s:%d: %s; this check reads only [agents.NAME] tables with command and model as plain double-quoted strings\n", FILENAME, FNR, why; failed = 1; exit 2 }
  FNR == 1 { table = "" }
  {
    line = $0
    sub(/^[ \t]+/, "", line)
    sub(/[ \t\r]+$/, "", line)
    if (line == "" || substr(line, 1, 1) == "#") next
    if (substr(line, 1, 1) == "[") {
      hdr = line
      sub(/[ \t]*#.*$/, "", hdr)
      if (hdr ~ /^\[[ \t]*agents[ \t]*\.[ \t]*[A-Za-z0-9_-]+[ \t]*\]$/) {
        name = hdr
        sub(/^\[[ \t]*agents[ \t]*\.[ \t]*/, "", name)
        sub(/[ \t]*\]$/, "", name)
        table = name
      } else if (hdr ~ /^\[[ \t]*agents[ \t]*\.[ \t]*"[^"\\]+"[ \t]*\]$/) {
        name = hdr
        sub(/^\[[ \t]*agents[ \t]*\.[ \t]*"/, "", name)
        sub(/"[ \t]*\]$/, "", name)
        table = name
      } else if (hdr ~ /^\[+[ \t]*"?agents/) {
        bad("cannot read the table header " hdr)
      } else {
        table = ""
      }
      next
    }
    if (table == "" && line ~ /^"?agents"?[ \t]*[.=]/) bad("cannot read a dotted or inline agents key")
    if (table == "") next
    if (line ~ /^"?(command|model)"?[ \t]*\./) bad("cannot read a dotted key")
    if (line !~ /^"?(command|model)"?[ \t]*=/) next
    key = line
    sub(/[ \t]*=.*$/, "", key)
    gsub(/"/, "", key)
    val = line
    sub(/^[^=]*=[ \t]*/, "", val)
    if (val !~ /^"[^"\\]*"[ \t]*(#.*)?$/) bad("cannot read the " key " value " val)
    sub(/^"/, "", val)
    sub(/"[ \t]*(#.*)?$/, "", val)
    printf "%s\t%s\t%s\n", table, key, val
  }
' "${files[@]}")
if [ $? -ne 0 ]; then
  printf '%s\n' "$rows" | grep -v "$(printf '\t')"
  exit 2
fi

agents=$(printf '%s\n' "$rows" | jq -Rn --arg fam "$FAMILIES" '
  ($fam | split("\n") | map(select(length > 0) | capture("^(?<f>[^ ]+) +(?<p>.+)$"))) as $table
  | def family($s): ($s | ascii_downcase) as $n
      | ([$table[] | . as $e | select($n | test($e.p)) | .f] | first) // "";
  reduce (inputs | select(length > 0) | split("\t")) as $r ({}; .[$r[0]][$r[1]] = $r[2])
  | with_entries(
      .value |= (
        (.command // "") as $c
        | (.model // "") as $m
        | ($c | split(" ") | map(select(length > 0)) | (.[0] // "") | split("/") | last | ascii_downcase | sub("\\.exe$"; "")) as $bin
        | {command: $c, model: $m, session: ($bin == "session"),
           family: ((if $m != "" then family($m) else "" end) as $fm
                    | if $fm != "" then $fm else (family($bin) as $fc | if $fc != "" then $fc else "unknown" end) end)}
      ))
') || { echo "could not build the bindings from ${files[*]}"; exit 2; }

review=""
if [ -f "$root/satelle.toml" ]; then
  review=$(awk '
    function bad(why) { printf "%s:%d: %s; this check reads [review.panels.GATE] tables only as tier = [\"agent\", ...] on one line\n", FILENAME, FNR, why; exit 2 }
    {
      line = $0
      sub(/^[ \t]+/, "", line)
      sub(/[ \t\r]+$/, "", line)
      if (line == "" || substr(line, 1, 1) == "#") next
      if (substr(line, 1, 1) == "[") {
        hdr = line
        sub(/[ \t]*#.*$/, "", hdr)
        gate = ""
        inreview = 0
        if (hdr ~ /^\[[ \t]*review[ \t]*\.[ \t]*panels[ \t]*\.[ \t]*[A-Za-z0-9_-]+[ \t]*\]$/) {
          gate = hdr
          sub(/^\[[ \t]*review[ \t]*\.[ \t]*panels[ \t]*\.[ \t]*/, "", gate)
          sub(/[ \t]*\]$/, "", gate)
        } else if (hdr ~ /^\[[ \t]*review[ \t]*\]$/) {
          inreview = 1
        } else if (hdr ~ /^\[+[ \t]*"?review/) {
          bad("cannot read the table header " hdr)
        }
        next
      }
      if (inreview) {
        if (line ~ /^"?panels"?[ \t]*[.=]/) bad("cannot read a dotted or inline panels key")
        next
      }
      if (gate == "") {
        if (line ~ /^"?review"?[ \t]*[.=]/) bad("cannot read a dotted or inline review key")
        next
      }
      if (line !~ /^[A-Za-z0-9_-]+[ \t]*=[ \t]*\[[^]]*\][ \t]*(#.*)?$/) bad("cannot read the line " line)
      tier = line
      sub(/[ \t]*=.*$/, "", tier)
      list = line
      sub(/^[^[]*\[/, "", list)
      sub(/\][^]]*$/, "", list)
      n = split(list, parts, ",")
      for (i = 1; i <= n; i++) {
        a = parts[i]
        gsub(/^[ \t]+|[ \t]+$/, "", a)
        if (a == "") continue
        if (a !~ /^"[^"\\]*"$/) bad("cannot read the panel member " a)
        gsub(/"/, "", a)
        printf "%s\t%s\t%s\n", gate, tier, a
      }
    }
  ' "$root/satelle.toml")
  if [ $? -ne 0 ]; then
    printf '%s\n' "$review" | grep -v "$(printf '\t')"
    exit 2
  fi
fi
panels=$(printf '%s\n' "$review" | jq -Rn 'reduce (inputs | select(length > 0) | split("\t")) as $r ({}; .[$r[0]][$r[1]] += [$r[2]])') || { echo "could not build the [review] tier panels from $root/satelle.toml"; exit 2; }

nl='
'
tab=$(printf '\t')
findings=0
nw=0
nt=0
ng=0
for w in $workflows; do
  if ! show=$(satelle workflow show "$w" --json </dev/null 2>&1); then
    echo "workflow $w: it could not be shown: $show"
    exit 2
  fi
  if ! printf '%s' "$show" | jq -e '(.states | type == "object") and (.transitions | type == "array")' >/dev/null 2>&1; then
    echo "workflow $w: unexpected shape from workflow show --json (want .states object and .transitions array)"
    exit 2
  fi
  out=$(printf '%s' "$show" | jq -r --arg w "$w" --argjson agents "$agents" --argjson panels "$panels" '
    . as $wf
    | ($wf.states) as $S
    | ($wf.transitions) as $T
    | def judges($g): ($wf.gates[$g] // {}) as $gate
        | if (($gate.Panel // []) | length) > 0 then $gate.Panel
          elif ($gate.Agent // "") != "" then [$gate.Agent]
          else [] end;
      def explicit($to): [$T[] | select(.To == $to) | (.From // [])[] | select(. != "*")];
      def firstwild($to): ([$T | to_entries[] | select(.value.To == $to and ((.value.From // []) | index("*"))) | .key] | first);
      def describe($b): if ($b.model // "") != "" then "model \($b.model)" else "no model, command \($b.command // "")" end;
      [ $T | to_entries[] | .key as $i | .value as $t
        | ($t.From // [])[] as $f
        | (if $f == "*" then
             (if firstwild($t.To) == $i then
                ($S | to_entries[] | select((.value.Terminal | not) and (.value.Park | not)) | .key)
                | select(. as $s | explicit($t.To) | index($s) | not)
              else empty end)
           else $f end) as $from
        | ($S[$from].Performer) as $p
        | select($p != null and ($p.Agent // "") != "" and ($p.Operator // "") == "")
        | ($agents[$p.Agent] // {command: "", model: "", session: false, family: "unknown"}) as $pb
        | [ ($t.Groups // [])[] | (.Gates // [])[] ] | unique[] as $g
        | ([{tier: "", js: judges($g)}] + [($panels[$g] // {}) | to_entries[] | {tier: .key, js: .value}])[] as $set
        | select(($set.js | length) > 0)
        | $set.js as $js
        | (if $set.tier == "" then "gate \($g)" else "gate \($g) (tier \($set.tier))" end) as $gl
        | if $pb.session then
            {kind: "note", text: "workflow \($w): state \($from): performer \($p.Agent) is the session: it has no model in config and is not compared"}
          elif $pb.family == "unknown" then
            {kind: "find", text: "workflow \($w): \($from) -> \($t.To): \($gl): performer \($p.Agent) (\(describe($pb))) names no known family; add it to FAMILIES"}
          else
            ([ $js[] | . as $j | ($agents[$j].family // "unknown") as $jf | {j: $j, f: $jf} ]) as $jfs
            | if any($jfs[]; .f != "unknown" and .f != $pb.family) then {kind: "pass"}
              else {kind: "find", text: ("workflow \($w): \($from) -> \($t.To): \($gl): no judge is of another model family than performer \($p.Agent) (\($pb.family), \(describe($pb))): "
                     + ([ $jfs[] | "\(.j) (\(.f))" ] | join(", "))
                     + (if any($jfs[]; .f == "unknown") then "; add an unknown name to FAMILIES" else "" end))}
              end
          end
      ] as $rows
      | ($rows | map(select(.kind == "note") | .text) | unique[] | "NOTE\t" + .),
        ($rows[] | select(.kind == "find") | "FIND\t" + .text),
        "COUNT\t\($T | length)\t\([$rows[] | select(.kind != "note")] | length)"
  ') || { echo "workflow $w: could not walk its transitions"; exit 2; }
  nw=$((nw + 1))
  while IFS= read -r line; do
    case $line in
      NOTE*) echo "${line#NOTE?}" ;;
      FIND*) echo "${line#FIND?}"; findings=$((findings + 1)) ;;
      COUNT*)
        IFS="$tab" read -r _ t g <<< "$line"
        nt=$((nt + t))
        ng=$((ng + g))
        ;;
    esac
  done <<< "$out$nl"
done

if [ "$findings" -gt 0 ]; then
  echo "$findings gate(s) have no judge of another model family than their from-state performer"
  exit 1
fi
echo "every judged gate has a judge of another model family than its from-state performer ($nw workflows, $nt transitions, $ng gates compared)"
exit 0
```
