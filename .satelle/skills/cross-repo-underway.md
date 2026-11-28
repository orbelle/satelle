---
name: cross-repo-underway
type: skill
description: The underway performer of a cross-repo story, bound to the interactive session. It drives the children in their depends-on order, each through its own repository's workflow and release, then attaches combined-change (every done child's frozen change and its diff) and presents done. A further child the judge's findings need is created while the parent stays underway. Edits no tree of the parent.
---

# Cross-repository underway (underway performer)

You are the performer of `underway` in the `cross-repo` workflow
(`.satelle/workflows/cross-repo.toml`), bound to the interactive session
(`executor`). The parent carries the behaviour; its children carry the work.

## 1. Drive the children in order

```bash
satelle story attachment <id> split --session <session>
satelle story list --parent <id> --session <session>
```

The `split` attachment records the children and the list shows their states; and each
child's `depends-on:` tags give the order. Start a child only once every child
it depends on is `done`. Each child runs its own repository's workflow
(`satelle story route <child> --session <session>`) and releases through that
repository's queue. Drive it the way that workflow's skills say; this skill adds
no step to a child.

## 2. Attach the combined change

Once every child is terminal, write a `combined-change` attachment for the
parent. It starts with a `## Children` section, one line per `done` child:

```
- <child id> <repo> <base>..<head>
```

`<base>` and `<head>` are the child's frozen change, the `base` and `head` of
`satelle story diff <child> --json --session <session>`. List a `cancelled`
child as `- <child id> <repo> cancelled`. Then add one `## <child id>` section
per done child holding exactly the output of
`git -C <checkout> diff --no-ext-diff --no-textconv <base> <head>`, where
`<checkout>` is that repository's path in `satelle repo list --session
<session>`. `satelle-cross-repo-combined-check` recomputes that output and
compares it with the section, so write it unedited; it may sit inside one
```` ``` ```` fence.

```bash
satelle story attach <id> --name combined-change --file <file> --session <session>
```

## 3. Present done

```bash
satelle story set <id> --status done --session <session>
```

The gates refuse while a child is open, while `combined-change` lacks a
child's current frozen change, and when a judge finds that the children's
combined change does not meet the parent's criteria. A judge's reject names
the criterion or seam.

## 4. A further child, staying underway

The fix for a criterion or seam the children miss is a further child, and the
parent stays `underway`: the workflow has no move back to `split`. With the
operator's word, create it as `cross-repo-split` step 2 says, with the next
split number and `depends-on:` tags on the children it follows. Append its
`- <n> <child id> <repo>` line to a new version of the `split` attachment.
Drive it as in step 1, then write `combined-change` again and present done.
`children-closed` and `satelle-cross-repo-combined-check` read every child, the
further one included.

After the round cap the story is held at `blocked`: follow
`.satelle/session.md`.
