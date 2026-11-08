# Recorded GitHub review fixtures (sto_ebcaa5aa, sto_efe81afd)

`recorded.json` is a real response of GitHub's list-reviews endpoint, recorded
read-only on 2026-11-07 with

    gh api repos/orbelle/satelle/pulls/5/reviews

It holds the two reviews the orbelle-reviewer app (app id 5563015) left on
orbelle/satelle#5, by its bot account `orbelle-reviewer[bot]` (numeric id
350948835, `gh api 'users/orbelle-reviewer[bot]' --jq .id`, same day). It is
the field-shape source of every other file. orbelle-reviewer is internal only,
so its reviews never count.

The account the approval check matches is the user-facing satelle GitHub app's
(app id 5379260): `satelle[bot]`, numeric id 344745317
(`gh api 'users/satelle[bot]' --jq .id`, confirmed 2026-11-08), per the
operator ruling in `.satelle/principles/satelle-operator-rulings.md`. It is the
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
| `approved-other-account.json` | `orbelle-reviewer[bot]` (id 350948835, internal app 5563015) approves HEAD | rejects: the login is never compared |
| `approved-then-dismissed.json` | approves HEAD, then the approval is dismissed | rejects |
| `approved-then-changes-requested.json` | approves HEAD, then requests changes | rejects and reopens the item |
| `changes-requested.json` | the app requests changes on HEAD | rejects and reopens the item |
| `none.json` | no reviews | rejects, never reopens |
| `approved-repointed.json` | the app's approval given on an older head (the ledger records `@OLD@`) that GitHub reports on HEAD | rejects, naming both heads |

`approved-repointed.json` is the exception to the copy rule above: it is
review 5815187973 copied from `repointed-recorded.json`, every field as
recorded except `commit_id`, which is `@HEAD@`.

## GitHub re-points an older review's commit_id (sto_952d4435)

`repointed-recorded.json` is a real response of the list-reviews endpoint,
recorded read-only on 2026-11-08 with

    gh api repos/orbelle/satelle/pulls/41/reviews --paginate

and kept verbatim. It holds three reviews by `satelle[bot]` (user.id
344745317) on orbelle/satelle#41, the PR of sto_e809d7d6:

| review | state | submitted_at | given on (ledger) | commit_id GitHub reports |
|---|---|---|---|---|
| 5815183195 | CHANGES_REQUESTED | 2026-11-07T21:56:49Z | cd3e7ac3 | cd3e7ac3e246663c54d9f6fd9644a1e3a67f5370 |
| 5815187973 | APPROVED | 2026-11-07T21:58:23Z | 6109ffe2 | f93eef33d5814570f648a88df4f207b23e9f9996 |
| 5815417340 | APPROVED | 2026-11-07T22:44:10Z | f93eef33 | f93eef33d5814570f648a88df4f207b23e9f9996 |

The item's `github.review` ledger event for review 5815187973 records
`commit_sha` 6109ffe2f71d13665ce69edb01104e3599067a8d, the head the judge
approved. GitHub now reports that review on f93eef33, a head the judge never
saw.

When it happened: the PR head moved from 6109ffe2 to f93eef33 by an ordinary
push of a merge commit ("Merge remote-tracking branch 'origin/sto_efe81afd'
into sto_e809d7d6"). It was not a force-push: 6109ffe2 is still an ancestor of
f93eef33 (`git merge-base --is-ancestor 6109ffe2 f93eef33` succeeds), and
`gh api repos/orbelle/satelle/pulls/41/commits` still lists it. The approval
that was the latest review when the head moved was re-pointed. The earlier
CHANGES_REQUESTED review 5815183195, given on cd3e7ac3, was not: GitHub still
reports it on cd3e7ac3.

The consequence: a review's `commit_id` is not proof of the head it was given
for. The item's `github.review` event is, so the approval check accepts an
approval only when the ledger records its review_id as an APPROVE on HEAD.
