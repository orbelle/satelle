# Recorded GitHub review fixtures (sto_ebcaa5aa)

`recorded.json` is a real response of GitHub's list-reviews endpoint, recorded
read-only on 2026-11-07 with

    gh api repos/orbelle/satelle/pulls/5/reviews

It holds the two reviews the orbelle-reviewer app (app id 5563015) left on
orbelle/satelle#5. Its bot account, `orbelle-reviewer[bot]`, has the numeric
id 350948835 (`gh api 'users/orbelle-reviewer[bot]' --jq .id`, same day): the
`user.id` every review by the app carries, and the `app_account` of
`.satelle/skills/satelle-pr-approval-check.md`.

Every other file is one case for the approval check, the publish check and the
release run's review read (`tests/pr_review_unix_test.go`). Each is a copy of
the first recorded review, field set, types and layout unchanged, with only
`id`, `html_url` (and `_links.html.href`), `state`, `commit_id`,
`submitted_at`, `body` and `user.id`/`user.login`/`user.html_url` set for the
case. The test replaces `@HEAD@` with the sandbox's HEAD and `@OLD@` with an
older commit.

| file | reviews | approval check |
|---|---|---|
| `approved-on-head.json` | the app approves HEAD | accepts |
| `approved-then-commented.json` | approves HEAD, then comments | accepts (a comment decides nothing) |
| `approved-older-head.json` | the app approves an older head | rejects |
| `approved-other-account.json` | `satelle[bot]` (id 344745317, a different app, 5379260) approves HEAD | rejects: the login is never compared |
| `approved-then-dismissed.json` | approves HEAD, then the approval is dismissed | rejects |
| `approved-then-changes-requested.json` | approves HEAD, then requests changes | rejects and reopens the item |
| `changes-requested.json` | the app requests changes on HEAD | rejects and reopens the item |
| `none.json` | no reviews | rejects, never reopens |
