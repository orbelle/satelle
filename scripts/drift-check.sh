#!/usr/bin/env bash
# drift-check — the substrate -> binary half of the drift review
# (.satelle/skills/drift-review.md, step 1). It checks the tracked substrate
# against the installed satelle and changes nothing.
#
#   drift-check.sh [root]        root defaults to the repo this runs in
#
# Files read: `git ls-files` of .satelle/skills, .satelle/principles,
# .satelle/tasks, .satelle/workflows, .satelle/queues and internal/help/topics,
# kept to *.md, *.toml and *.sh outside any testdata/ directory. Tracked files
# only, so machine-local state is never read; .satelle/remotes is not listed
# (satelle-cicd is reference only, satelle-operator-rulings).
#
# Checks:
#   command  every `satelle <command> [<subcommand>]` named in code — backtick
#            spans, fenced blocks and 4-space-indented lines of .md files,
#            backtick spans of .toml files, every line of .sh files — is a
#            command of the binary's help tree;
#   flag     every --long-flag of such a mention, up to the next | ; && ) or
#            the span's end, is a flag of the command it resolved to (its own
#            or a global one);
#   topic    every `satelle help <word>` names a help topic or a command;
#   link     every [[name]] outside code in those .md files names
#            .satelle/principles/<name>.md or .satelle/skills/<name>.md;
#   skill    every skill a workflow names exists (satelle validate --json);
#            any other validate problem is kind config.
# A mention whose next word is a placeholder (<...>, $..., quoted) is not
# checked. The binary's help tree is read once, by recursive --help, into a
# temp dir deleted on exit; no list of commands is kept in the repo.
#
# Output: one line per miss, `<file>:<line>: <kind>: <subject> — <why>`.
# Exit 0 when nothing is missed, 1 on any miss, 2 when it cannot check
# (satelle, git or jq is missing, or the root is not a git work tree).
set -uo pipefail

bin=${SATELLE_BIN:-satelle}
for tool in "$bin" git jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "drift-check: $tool is not on PATH, so the substrate cannot be checked" >&2; exit 2; }
done

root=${1:-$(git rev-parse --show-toplevel 2>/dev/null)}
[ -n "$root" ] && git -C "$root" rev-parse --is-inside-work-tree >/dev/null 2>&1 || { echo "drift-check: ${root:-.} is not a git work tree" >&2; exit 2; }
cd "$root" || exit 2

tmp=$(mktemp -d) || exit 2
trap 'rm -rf "$tmp"' EXIT

# The help tree: S <path>\t<sub>, F <path>\t<--flag>, T\t<topic>.
helptree() {
  local out sub
  out=$("$bin" "$@" --help 2>/dev/null) || true
  printf '%s\n' "$out" | awk -v path="$*" '
    /^Available Commands:/ { sec = "S"; next }
    /^(Flags|Global Flags):/ { sec = "F"; next }
    /^[^ ]/ || /^$/ { sec = ""; next }
    sec == "S" && /^  [a-z]/ { print "S " path "\t" $1 }
    sec == "F" {
      for (i = 1; i <= NF; i++) if ($i ~ /^--[a-z]/) { f = $i; sub(/[=,].*/, "", f); print "F " path "\t" f; break }
    }' >> "$tmp/tree"
  for sub in $(printf '%s\n' "$out" | awk '/^Available Commands:/ { s = 1; next } /^[^ ]/ || /^$/ { s = 0 } s && /^  [a-z]/ { print $1 }'); do
    [ "$sub" = help ] && [ $# -eq 0 ] && continue   # `satelle help` is cobra's; its topics are read below
    helptree "$@" "$sub"
  done
}
: > "$tmp/tree"
helptree
grep -q '^S ' "$tmp/tree" || { echo "drift-check: $bin --help lists no commands" >&2; exit 2; }
printf 'S \thelp\n' >> "$tmp/tree"
"$bin" help 2>/dev/null | awk '/^Help topics/ { s = 1; next } /^[^ ]/ || /^$/ { s = 0 } s && /^  [a-z]/ { print "T\t" $1 }' >> "$tmp/tree"

# Link targets: every principle and skill name on disk.
for f in .satelle/principles/*.md .satelle/skills/*.md; do
  [ -f "$f" ] && printf 'L\t%s\n' "$(basename "$f" .md)"
done >> "$tmp/tree"

git ls-files -- .satelle/skills .satelle/principles .satelle/tasks .satelle/workflows .satelle/queues internal/help/topics |
  grep -E '\.(md|toml|sh)$' | grep -v '/testdata/' > "$tmp/files"

: > "$tmp/misses"
if [ -s "$tmp/files" ]; then
  # shellcheck disable=SC2046
  awk -v treefile="$tmp/tree" '
    BEGIN {
      FS = "\t"
      while ((getline l < treefile) > 0) {
        split(l, p, "\t")
        if (p[1] ~ /^S /) { path = substr(p[1], 3); subs[path, p[2]] = 1; hassubs[path] = 1 }
        else if (p[1] ~ /^F /) { path = substr(p[1], 3); flags[path, p[2]] = 1 }
        else if (p[1] == "T") topics[p[2]] = 1
        else if (p[1] == "L") links[p[2]] = 1
      }
      FS = " "
    }
    FNR == 1 { fence = 0; kind = FILENAME; sub(/.*\./, "", kind) }
    function miss(k, subj, why) { printf "%s:%d: %s: %s — %s\n", FILENAME, FNR, k, subj, why }
    # check walks one mention: rest is the text after "satelle"; quoted is
    # set when the mention sits inside a "..." string, where a first word
    # that is no command is prose (echo "the satelle app ..."), not a call.
    function check(rest, quoted,    n, tok, j, t, path, words, descend, f, k, known) {
      if (match(rest, /[|;)]|&&|[ \t]#/)) rest = substr(rest, 1, RSTART - 1)
      n = split(rest, tok, /[ \t]+/)
      path = ""; words = "satelle"; descend = 1
      for (j = 1; j <= n; j++) {
        t = tok[j]
        if (t == "") continue
        if (t ~ /^--[a-z]/) {
          match(t, /^--[a-z][a-z0-9-]*/); f = substr(t, 1, RLENGTH)
          if (f ~ /-$/) {                     # an elided flag (--input-…): any flag it begins
            sub(/-+$/, "", f); known = 0
            for (k in flags) { split(k, kp, SUBSEP); if (kp[1] == path && index(kp[2], f "-") == 1) known = 1 }
            if (!known) miss("flag", words " " f, f "-… begins no flag of satelle" (path == "" ? "" : " " path))
            continue
          }
          if (!((path, f) in flags)) miss("flag", words " " f, f " is not a flag of satelle" (path == "" ? "" : " " path))
          continue
        }
        if (t ~ /^-/ || !descend) continue
        sub(/[.,:]+$/, "", t)
        if (t !~ /^[a-z][a-z-]*$/) { descend = 0; continue }
        if (path == "help") {
          if (!(t in topics) && !(("", t) in subs)) miss("topic", "satelle help " t, t " is not a help topic or command of satelle")
          descend = 0; continue
        }
        if (!(path in hassubs)) { descend = 0; continue }
        if (!((path, t) in subs)) {
          if (path == "" && quoted) return
          miss("command", words " " t, t " is not a " (path == "" ? "command of satelle" : "subcommand of satelle " path))
          return
        }
        path = (path == "" ? t : path " " t); words = words " " t
      }
    }
    # mentions checks every mention in code text c, skipping a word list
    # (for tool in git satelle bash).
    function mentions(c,    i, before, q, pre) {
      q = 0
      while (match(c, /(^|[^A-Za-z0-9_.\/-])satelle[ \t]+[a-z]/)) {
        i = index(substr(c, RSTART), "satelle") + RSTART - 1
        before = substr(c, 1, i - 1)
        pre = before; q = (q + gsub(/"/, "", pre)) % 2
        if (before !~ /(^|[ \t;])for[ \t]+[A-Za-z_]+[ \t]+in[ \t][^;]*$/) check(substr(c, i + 7), q)
        c = substr(c, i + 7)
      }
    }
    # spans checks the mentions in each backtick span of s (check moves
    # RSTART, so the end of the span is kept first); strip returns s without them.
    function spans(s,    a, b) {
      while (match(s, /`[^`]+`/)) { a = RSTART; b = RLENGTH; mentions(substr(s, a + 1, b - 2)); s = substr(s, a + b) }
    }
    function strip(s) { gsub(/`[^`]*`/, "", s); return s }
    kind == "sh" { mentions($0); next }
    kind == "toml" { spans($0); next }
    kind == "md" {
      if ($0 ~ /^[ \t]*(```|~~~)/) { fence = !fence; next }
      if (fence) { mentions($0); next }
      if ($0 ~ /^    [ \t]*[^ \t]/ && $0 !~ /^[ \t]*([-*+]|[0-9]+\.)[ \t]/) { mentions($0); next }
      spans($0)
      s = strip($0)
      while (match(s, /\[\[[a-z0-9][a-z0-9-]*\]\]/)) {
        name = substr(s, RSTART + 2, RLENGTH - 4)
        if (!(name in links)) miss("link", name, "no .satelle/principles/" name ".md or .satelle/skills/" name ".md")
        s = substr(s, RSTART + RLENGTH)
      }
    }
  ' $(cat "$tmp/files") >> "$tmp/misses"
fi

# Skills a workflow names: satelle validate owns the rule.
v=$("$bin" validate --json 2>/dev/null)
printf '%s' "$v" | jq -e 'has("problems")' >/dev/null 2>&1 || { echo "drift-check: $bin validate --json printed no problems list" >&2; printf '%s\n' "$v" >&2; exit 2; }
printf '%s' "$v" | jq -r '.problems[]' | while IFS= read -r p; do
  wf=$(printf '%s' "$p" | sed -n 's/^workflow \([^:]*\):.*/\1/p')
  file=.satelle/satelle.toml
  [ -n "$wf" ] && [ -f ".satelle/workflows/$wf.toml" ] && file=".satelle/workflows/$wf.toml"
  case $p in
    *"is not in skills/"* | *"is not a check skill"*)
      k=skill
      subj=$(printf '%s' "$p" | sed -n 's/.*"\([^"]*\)" is not.*/\1/p')
      ;;
    *) k=config; subj=${p#workflow "$wf": } ;;
  esac
  line=1
  if [ "$k" = skill ] && [ -n "$subj" ]; then
    line=$(grep -nF "\"$subj\"" "$file" 2>/dev/null | head -1 | cut -d: -f1)
    line=${line:-1}
  fi
  printf '%s:%s: %s: %s — %s\n' "$file" "$line" "$k" "${subj:-$p}" "$p"
done >> "$tmp/misses"

if [ -s "$tmp/misses" ]; then
  cat "$tmp/misses"
  exit 1
fi
exit 0
