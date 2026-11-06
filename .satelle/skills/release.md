---
name: release
type: skill
description: The release procedure a release task's run follows: fast-forward main to the approved branch, push, dispatch release.yml, confirm the tags and assets, install the published build here and attach the evidence. Local landing; GitHub runs no tests. Never advances status.
---

# Release

A release is a run of the release task (`satelle task run <release task>`). The
executor seated on the run does the steps below and attaches the evidence; the
run's close gate judges it. Landing is local, GitHub holds the code and runs no
tests, and there is no staging: local testing is the staging.

## 0. Preconditions

- The branch to release (`claude/satelle-rebuild` until the CI/CD task replaces
  it) is pushed, and every item on it is done.
- `.version` names the version to release; `satelle.build` is the release
  time (`date -u +%Y-%m-%dT%H:%M:%SZ`). When it changes, commit it on the
  branch (`chore(release): v<version> (<execution id>)`) and push before step 1.
- The full suite passes locally on the branch head:
  `go build ./... && go test -count=1 ./...`.

## 1. Land on main

Fast-forward only; never merge, rebase or force onto main.

```sh
git fetch origin
git merge-base --is-ancestor origin/main origin/<branch>   # must hold
git push origin origin/<branch>:refs/heads/main
```

If main is not an ancestor of the branch, stop and report: someone landed on
main since, and the branch must take it in through a story first.

## 2. Publish

```sh
gh workflow run release.yml --ref main
gh run list --workflow release.yml --limit 1 --json databaseId,status,conclusion,headSha,url
gh run watch <id> --exit-status
gh release view v<satelle.version> --json tagName,targetCommitish,assets
gh release view serve-v<satelled.version> --json tagName,assets
```

The run's `headSha` must be main's head. A failed run or a missing tag means
the release is not done: report it and stop.

## 3. Install the published build here

```sh
satelle update --version <satelle.version>
satelle service restart
satelle version
satelle service status
```

`satelle version` must name the released version and main's commit.

## 4. Attach the evidence

Attach one markdown document named `release-evidence` to the run
(`satelle execution attach <run> --name release-evidence --file -`) with:
main's head before and after, the release.yml run URL, its head sha and
conclusion, both tags and their asset lists, and the `satelle version` and
`satelle service status` lines.
