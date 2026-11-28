---
name: cross-repo-split
type: skill
description: The split-state performer of a cross-repo story, bound to the interactive session. It creates one child story per line of the plan's "## Split" section, with that repo, the parent link, the line's section as its body and depends-on tags in the plan's order, records them in the parent's split attachment, and presents underway. Creates items only; edits no tree.
---

# Cross-repository split (split performer)

You are the performer of `split` in the `cross-repo` workflow
(`.satelle/workflows/cross-repo.toml`), bound to the interactive session
(`executor`). The plan state left a `plan` attachment whose `## Split`
section the `satelle-cross-repo-plan-check` gate accepted. You create its
children; nothing in a tree changes.

## 1. Read the split

```bash
satelle story attachment <id> plan --session <session>
satelle story list --parent <id> --session <session>
```

On a return to `split` after a rejected round, the `children-split` gate's
notes name what is missing or wrong. Fix that child (`satelle story set
<child> ... --session <session>`, or cancel a wrong one) rather than creating a
second child for the same line.

## 2. Create each child, in order

For each split line `- <n> | <repo> | after <a,...|-> | <title>`, from 1 up,
create one child with `satelle story create --session <session>`
(`satelle story create --help --session <session>` names each flag):

- the line's title, `--repo <repo>` and `--parent <id>`;
- as its body, the line's `### Child <n>` section as written, its
  `Criteria:` lines included as draft criteria. The child's own plan state
  authors its criteria from them, as it does for any raiser's drafts;
- `--tag split:<n>`, and one `--tag depends-on:<child id>` for each child the
  line is `after`, using the ids the earlier creates printed;
- the parent's `epic:` and `sprint:` tags
  ([[satelle-group-related-stories]]). The child is
placed on its repository's workflow (`satelle help workflows --session
<session>`); do not pass `--workflow`.

## 3. Record the children on the parent

Attach a `split` attachment to the parent with one line per child,
`- <n> <child id> <repo>`, in split order:

```bash
satelle story attach <id> --name split --file <file> --session <session>
```

## 4. Present underway

```bash
satelle story set <id> --status underway --session <session>
```

The `children-split` gate checks every child against the plan and the
`split` attachment. After the round cap the story is held at `blocked`:
follow `.satelle/session.md`.
