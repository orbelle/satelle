#!/usr/bin/env bash
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

v=$("$bin" validate --json 2>/dev/null)
printf '%s' "$v" | jq -e 'has("problems") and (.substrate | type == "array")' >/dev/null 2>&1 || {
  echo "drift-check: $bin validate --json printed no problems or substrate list, so it cannot check the substrate" >&2
  printf '%s\n' "$v" >&2
  exit 2
}

: > "$tmp/misses"
printf '%s' "$v" | jq -r '.substrate[] | "\(.file):\(.line): \(.kind): \(.subject) — \(.why)"' >> "$tmp/misses"

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
