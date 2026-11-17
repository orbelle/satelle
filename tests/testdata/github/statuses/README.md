# Commit status fixtures

What `gh api repos/{owner}/{repo}/commits/<sha>/statuses` answers: GitHub's
"list commit statuses for a reference" response, newest first, each status with
its `creator` account. The tests replace `@HEAD@` with the sandbox head.

- `by-app.json`: the three `satelle/<gate>` contexts posted by the satelle
  GitHub app's account (`satelle[bot]`, id 344745317).
- `by-other.json`: the same contexts posted by another account
  (`orbelle-reviewer[bot]`, id 350948835), which the status check names as not
  the app's.
