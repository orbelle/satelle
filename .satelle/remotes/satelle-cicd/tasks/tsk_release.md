---
id: tsk_release
type: task
status: done
priority: high
category: substrate
tags: cicd
inputs_decl: story_id:string:required
created: 2026-11-04T00:00:00Z
updated: 2026-11-04T00:00:00Z
---

# Release one finished satelle story through its PR

## Action
Release the story named by the story_id input (`$SATELLE_INPUT_STORY_ID`): its `epic/<id>` or `feat/<id>` branch is pushed to origin, passed its review gates locally, and carries its CHANGELOG entry under `## [Unreleased]`. Run exactly this in your shell, as one command with no timeout, or one of at least 3600 seconds (the release waits on two CI workflows), with `<exe id>` replaced by this execution's exe_ id (the one satelle names when it delivers this step's inputs), and change nothing else:

```sh
N="$PWD"; E=<exe id>; W="$(mktemp -d)"
git -c credential.helper= -c credential.helper='!f(){ echo username=x-access-token; echo "password=$GH_TOKEN"; }; f' clone -q https://github.com/orbelle/satelle.git "$W/repo" &&
cd "$W/repo" && git config user.name "satelle cicd remote" && git config user.email "cicd@satelle.dev" &&
cp .satelle/remotes/satelle-cicd/release.sh "$W/release.sh" &&
SATELLE_NODE_DIR="$N" SATELLE_RELEASE_EXECUTION="$E" bash "$W/release.sh" "$SATELLE_INPUT_STORY_ID" >"$W/out.txt" 2>&1; echo "exit=$?" >>"$W/out.txt"; cat "$W/out.txt"
```

The script runs every satelle command (the PR body, `satelle pr approve`, the live-evidence attach) from the node's own directory, where the node holds the story (satelle 0.0.891); `satelle pr approve` records its verdict on this execution, and your record below replaces it with the whole output. Then record the command's full output (it ends with `exit=<n>`) as this execution's run-output doc with satelle itself: pipe it to `satelle execution record <this execution's exe_ id> --outcome <performed when exit=0, else reject>`. The exe_ id is the one satelle names when it delivers this step's inputs. Never print GH_TOKEN or any secret value, and never run git push, gh, or edit a file yourself: the script does the release.

## Verification
The run-output doc records the script's output and `exit=0` with outcome `performed`, including the live-evidence lines (version, branch, PR, approval, merge commit, test and release runs, tag). Any other exit records outcome `reject` with that output; a non-zero exit is deterministic, so it is not retried.

## Acceptance Criteria
1. The story's branch is released through its PR by .satelle/remotes/satelle-cicd/release.sh, or refused with its reason and nothing merged.
2. The PR is merged only when its satelle/pr-approval status is success on the head being merged.
3. The run-output carries the script's live-evidence text.
4. Nothing outside this execution, the story's branch, its PR and the release itself is written.
