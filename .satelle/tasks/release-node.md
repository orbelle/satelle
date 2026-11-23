# Release node part

The task the release fires on a fly.io sprite: `.satelle/skills/release.md`
`### Fire the node's part` runs `satelle remote fire release-sprite <task>
--session <session>`, and the node runs the release's steps from the suite to
the push to main (`.satelle/skills/satelle-release-node/SKILL.md`). The remote
is `.satelle/remotes/release-sprite/`: its `[connect]` reaches a sprite
through `satelle-node-sprite`, with start and stop wrappers that carry the
node's input in and its scanned result out; its `[secrets] names` declares
`GH_TOKEN`, which the fire reads from the firing shell or the repo's untracked
`.satelle/.env` and hands to the node on the exec command's stdin only; its
`[drive] task` names this task; and its overlay `satelle.toml` makes the
node's execution a `release-node` item (`satelle workflow show release-node
--session <session>`), whose `backlog → done` move runs
`satelle-change-judged-check` then the `satelle-release-node` check in the node.
No agent reads, prints or attaches the value of `GH_TOKEN`.

## The task

Tasks live on the ledger. Find this one by its title:

```sh
session=<session>
satelle task list --json --session "$session" | jq -r '.[] | select(.title == "Release node part") | .id'
```

When it prints nothing, create it once, from the main checkout, and put the
printed id into `.satelle/remotes/release-sprite/remote.toml`'s `[drive]
task`. The selection names no item, so the node is given no item chain and
changes no item.

```sh
session=<session>
satelle task create --title "Release node part" --input-kind story --input-tag release-node --body - --session "$session" <<'BODY'
The release's node part on a fly.io sprite (.satelle/tasks/release-node.md): test the drift-checked candidate once, decide the binary, bump, publish release.yml from release/<run> and fast-forward main.

ACTION: fired by .satelle/skills/release.md ### Fire the node's part as satelle remote fire release-sprite <this task> --session <session>, with SATELLE_RELEASE_IN and SATELLE_RELEASE_OUT set; the node creates one execution of this task and drives it on the release-node workflow, whose backlog -> done move runs the satelle-release-node check (release-node.sh run) in the node.
VERIFICATION: satelle remote status release-sprite <this task> --session <session> shows the run concluded; the execution's gate.run notes of release-node are the compact result (secret-scan clean, or failed step <name>); scripts/release-node-verify.sh reads the brought-back result and finds the sprite destroyed.
BODY
```

## Dry run

Proves the node's part on a real sprite without publishing or pushing main:
the node stops after `decide`. From the main checkout, with the sprite CLI
signed in and `GH_TOKEN` in the repo's `.satelle/.env`:

```sh
session=<session>
task=<the task id found above>
sha=$(git rev-parse HEAD)
base=$(git rev-parse origin/main)
d=$(mktemp -d)
branch=$(bash .satelle/skills/satelle-release-node/scripts/release-node.sh dry-input "$d/in" "$sha" "$base")
git push origin "$sha:refs/heads/$branch"
SATELLE_RELEASE_IN="$d/in" SATELLE_RELEASE_OUT="$d/node-out" satelle remote fire release-sprite "$task" --session "$session"
satelle remote status release-sprite "$task" --session "$session"
git push origin --delete "$branch"
```

The brought-back result is `$d/node-out/<node>/back/result`: `secret-scan
clean`, `privilege release`, `suite-exit 0` and the `binary` line. A dry run
whose `candidate` is not on its branch rejects with `failed step fetch`.
