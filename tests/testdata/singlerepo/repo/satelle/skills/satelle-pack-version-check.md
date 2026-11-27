---
name: satelle-pack-version-check
type: check
description: Doctor check reporting each installed pack that has a newer version on the marketplace. Runs satelle pack status --json. Exits 1 naming each such pack, its installed and newest version and the satelle pack update command; exits 0 printing a note for each pack whose newest version could not be read (an unreachable marketplace is never an issue) and nothing for a pack at its newest version; exits 2 when satelle or jq is missing or pack status fails. Deterministic, no agent.
---

# Pack versions (a newer version upstream raises an issue; an unreadable marketplace is a note)

Doctor reports a newer pack version; it changes nothing. The user decides when
to update, in a seat that may change the tree, with the command the issue names.
Locally edited and missing pack files are listed by
`satelle pack status --session <session>`, not by this check.

Configure it in `.satelle/doctor.toml`:

```toml
[[check]]
skill = "satelle-pack-version-check"
fix = "in a seat that may change the tree, run the update command the issue names, then merge each edited file it lists under .satelle/pack-merge"
```

Exit semantics for `satelle doctor --session <session>`:

- Exit `1`: at least one installed pack has a newest version that differs from
  its installed version. Prints one line per such pack:
  `<ref> is at <version>; <newest> is the newest: satelle pack update <ref> --version <newest> --session <session>`.
  Notes are omitted so doctor raises only issues.
- Exit `0`: no pack has a newer version. Prints one note per pack whose newest
  version could not be read (`<ref> <version>: the marketplace could not be read: ...`),
  and nothing for a pack at its newest version or when no pack is installed.
- Exit `2`: `satelle` or `jq` is missing, or pack status failed (for
  example a symlinked or malformed `.satelle/packs.toml`); doctor reports
  `could not check`.

Every ref and version the check names must be a plain name (letters, digits
and `. _ + @ / -`, not starting with `-`), so the update command it prints can
carry no flag or shell metacharacter.

```sh
set -u
for tool in satelle jq; do
  command -v "$tool" >/dev/null 2>&1 || { printf '%s is not installed\n' "$tool"; exit 2; }
done
if ! status=$(satelle pack status --json </dev/null 2>&1); then
  printf 'cannot read pack status\n'
  exit 2
fi
if ! lines=$(printf '%s\n' "$status" | jq -r '
  def plain: type == "string" and test("^[A-Za-z0-9][A-Za-z0-9._+@/-]{0,199}$");
  (.packs // [])
  | if type != "array" then error("expected packs array") else . end
  | .[]
  | if ((.ref | plain) and (.version | plain) and ((.newest // "") == "" or (.newest | plain))) | not then
      "N\tpack status listed a pack whose ref or version is not a plain name; run satelle pack status --session <session>"
    elif (.newest_error // "") != "" then
      "N\t" + .ref + " " + .version + ": " + (.newest_error | gsub("[[:cntrl:]\u2028\u2029]"; " "))
    elif (.newest // "") != "" and .newest != .version then
      "I\t" + .ref + "\t" + .version + "\t" + .newest
    else
      empty
    end
' 2>/dev/null); then
  printf 'cannot parse pack status\n'
  exit 2
fi
[ -n "$lines" ] || exit 0
tab=$(printf '\t')
issues=$(printf '%s\n' "$lines" | sed -n "s/^I${tab}//p")
if [ -n "$issues" ]; then
  printf '%s\n' "$issues" | while IFS="$tab" read -r ref version newest; do
    printf '%s is at %s; %s is the newest: satelle pack update %s --version %s --session <session>\n' "$ref" "$version" "$newest" "$ref" "$newest"
  done
  exit 1
fi
notes=$(printf '%s\n' "$lines" | sed -n "s/^N${tab}//p")
if [ -n "$notes" ]; then
  printf '%s\n' "$notes"
fi
exit 0
```
