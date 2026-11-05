#!/usr/bin/env bash
# Release one finished satelle story through its PR (sty_8094a272), from a
# writable clone of orbelle/satelle on the satelle-cicd remote's node. The story
# passed its gates locally and its epic/<id> (or feat/<id>) branch is pushed,
# contains origin/main, and carries its CHANGELOG entry under "## [Unreleased]".
#
#   SATELLE_NODE_DIR=<node dir> SATELLE_RELEASE_EXECUTION=<exe id> release.sh <story id>
#
# It stamps the release on the story branch (.version patch bump, the satelled
# bump scripts/check-serve-version.sh asks for, the CHANGELOG heading and its
# embedded copy), pushes the branch, opens the story's PR, has `satelle pr
# approve` cold-review it and post the satelle/pr-approval status under GH_TOKEN
# (the operator: the PR's author), merges it only when that status is success on
# the head being merged, waits for the test and release workflows and the
# v<version> tag, and attaches live-evidence to the story. Dogfood is the
# operator's machine's, after the tag.
#
# A re-run after a refusal resumes: when the branch tip is already this
# script's `release: <version> (<id>)` commit and v<version> is not tagged, it
# keeps that stamp and the open PR instead of stamping again.
#
# Every satelle command runs from SATELLE_NODE_DIR, where the node holds the
# story; git and gh run in the clone. Git and forge actions live here, in the
# substrate, never in the binary (satelle-mechanism-vs-substrate).
#
# Seams (environment):
#   GH_TOKEN                  read by git through a credential helper and by gh
#   SATELLE_NODE_DIR          the node's satelle directory (required)
#   SATELLE_RELEASE_EXECUTION the run's exe_ id, which `pr approve` records on (required)
#   SATELLE_RELEASE_REPO      owner/name (orbelle/satelle)
#   SATELLE_RELEASE_CI_WAIT   seconds to wait for the test run to be listed (900)
#   SATELLE_RELEASE_DISPATCH  seconds before a missing push run is dispatched (180)
#   SATELLE_RELEASE_TAG_WAIT  seconds to wait for the v<version> release (1800)
#   SATELLE_RELEASE_POLL      seconds between polls (10)
#
# Exit: 0 released; 2 usage, dirty clone, no pushed branch, no Unreleased entry,
# constitution drift or serve check unverifiable; 3 main moved (re-integrate
# locally); 4 push refused; 5 PR not opened; 8 not approved; 9 merge refused;
# 6 test run missing or red; 7 release run red or no tag.
set -euo pipefail
set +x

id="${1:-}"
node_dir="${SATELLE_NODE_DIR:-}"
execution="${SATELLE_RELEASE_EXECUTION:-}"
if [ -z "$id" ] || [ -z "$node_dir" ] || ! [[ "$execution" =~ ^exe_[0-9a-f]+$ ]]; then
  echo "usage: SATELLE_NODE_DIR=<node dir> SATELLE_RELEASE_EXECUTION=<exe id> $0 <story id>" >&2
  exit 2
fi

repo="${SATELLE_RELEASE_REPO:-orbelle/satelle}"
ci_wait="${SATELLE_RELEASE_CI_WAIT:-900}"
dispatch_after="${SATELLE_RELEASE_DISPATCH:-180}"
tag_wait="${SATELLE_RELEASE_TAG_WAIT:-1800}"
poll="${SATELLE_RELEASE_POLL:-10}"
status_context="satelle/pr-approval"

cd "$(git rev-parse --show-toplevel)"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/satelle-release.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

on_node() { (cd "$node_dir" && satelle "$@"); }

# The value is single-quoted so git config stores the literal $GH_TOKEN, never
# the token. The empty entry resets any inherited helper list so this one wins.
if [ -n "${GH_TOKEN:-}" ]; then
  git config --unset-all credential.helper || true
  git config --add credential.helper ''
  git config --add credential.helper '!f(){ echo username=x-access-token; echo "password=$GH_TOKEN"; }; f'
fi

if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "clone has local changes; refusing to release" >&2
  exit 2
fi

git fetch -q --prune --tags origin

if git rev-parse -q --verify "refs/remotes/origin/epic/$id" >/dev/null; then
  branch="epic/$id"
elif git rev-parse -q --verify "refs/remotes/origin/feat/$id" >/dev/null; then
  branch="feat/$id"
else
  echo "no pushed branch for $id (epic/$id or feat/$id)" >&2
  exit 2
fi

if ! git merge-base --is-ancestor origin/main "origin/$branch"; then
  echo "re-integrate locally: origin/main has commits $branch does not contain" >&2
  exit 3
fi

git checkout -q -B "$branch" "origin/$branch"

# The bundle's constitution copy must say what the repo's does, or the cold
# review judges against a stale constitution.
body_of() { awk 'BEGIN{n=0} n>=2{print; next} /^---$/{n++; next}' "$1"; }
bundle_constitution="$node_dir/.satelle/principles/repo-constitution.md"
if [ ! -f "$bundle_constitution" ] || ! diff -q <(body_of .satelle/constitution.md) <(body_of "$bundle_constitution") >/dev/null; then
  echo "constitution drift: the satelle-cicd bundle's principles/repo-constitution.md body differs from .satelle/constitution.md on $branch; update the definition" >&2
  exit 2
fi

# A tip that is this script's own release commit for the story, not yet
# tagged, is a refused run's stamp: resume from it.
resumed=""
if [[ "$(git log -1 --format=%s)" =~ ^release:\ ([0-9]+\.[0-9]+\.[0-9]+)\ \("$id"\)$ ]]; then
  version="${BASH_REMATCH[1]}"
  if [ "$(sed -n 's/^satelle\.version: //p' .version)" = "$version" ] &&
     awk '/^## \[/{print; exit}' CHANGELOG.md | grep -qF "## [$version]" &&
     ! git rev-parse -q --verify "refs/tags/v$version" >/dev/null; then
    resumed=yes
    serve=""
    if git show --format= --name-only HEAD | grep -qx .version && git diff HEAD~1 HEAD -- .version | grep -q '^+satelled\.version: '; then
      serve="$(sed -n 's/^satelled\.version: //p' .version)"
    fi
    echo "resuming the release of $version from $(git rev-parse --short HEAD) on $branch"
  fi
fi

if [ -z "$resumed" ]; then
if ! awk '/^## \[/{print; exit}' CHANGELOG.md | grep -q '^## \[Unreleased\]'; then
  echo "CHANGELOG.md's first entry on $branch is not '## [Unreleased]'; the story's entry must be there" >&2
  exit 2
fi

cur="$(sed -n 's/^satelle\.version: //p' .version)"
serve_cur="$(sed -n 's/^satelled\.version: //p' .version)"
if ! [[ "$cur" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || ! [[ "$serve_cur" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo ".version has no satelle.version / satelled.version patch versions (got '$cur' / '$serve_cur')" >&2
  exit 2
fi
version="${cur%.*}.$(( ${cur##*.} + 1 ))"

# The serve check owns the satelled watch set; its "bump" answer is the only one
# that moves satelled.version, and any other failure stops the release.
serve=""
if ! bash scripts/check-serve-version.sh >"$tmp/serve.txt" 2>&1; then
  if grep -q 'bump satelled.version' "$tmp/serve.txt"; then
    serve="${serve_cur%.*}.$(( ${serve_cur##*.} + 1 ))"
  else
    cat "$tmp/serve.txt" >&2
    echo "check-serve-version could not answer; refusing to release" >&2
    exit 2
  fi
fi

today="$(date -u +%Y-%m-%d)"
stamp="$(date -u +%Y-%m-%d-%H-%M-%S)"
sed -e "s/^satelle\.version: .*/satelle.version: $version/" \
    -e "s/^satelle\.build: .*/satelle.build: $stamp/" .version >"$tmp/version"
if [ -n "$serve" ]; then
  sed -i "s/^satelled\.version: .*/satelled.version: $serve/" "$tmp/version"
fi
cat "$tmp/version" >.version

awk -v v="$version" -v d="$today" -v sv="$serve" -v id="$id" '
  /^## \[/ {
    h++
    if (h == 1) { print "## [" v "] - " d; next }
    if (h == 2 && sv != "") {
      print "## [serve-v" sv "] - " d; print ""; print "### Changed"
      print "- The service binary carries the serve-path changes released in " v ". (" id ")"; print ""
    }
  }
  { print }' CHANGELOG.md >"$tmp/changelog"
cat "$tmp/changelog" >CHANGELOG.md
cp CHANGELOG.md internal/verb/embedded/CHANGELOG.md

if [ -n "$serve" ] && ! bash scripts/check-serve-version.sh >"$tmp/serve2.txt" 2>&1; then
  cat "$tmp/serve2.txt" >&2
  echo "check-serve-version still fails after the satelled bump; refusing to release" >&2
  exit 2
fi

git add .version CHANGELOG.md internal/verb/embedded/CHANGELOG.md
git commit -q -m "release: $version ($id)"

if ! git push -q origin "HEAD:refs/heads/$branch"; then
  echo "push of $branch refused; nothing further ran" >&2
  exit 4
fi
fi
sha="$(git rev-parse HEAD)"

entry="$(awk '/^## \[/{h++} h==1{print}' CHANGELOG.md)"
pr="$(gh pr list -R "$repo" --head "$branch" --state open --json number -q '.[0].number' || true)"
if [ -z "$pr" ] || [ "$pr" = null ]; then
  if ! on_node pr body "$id" >"$tmp/criteria.md" 2>"$tmp/criteria.err"; then
    cat "$tmp/criteria.err" >&2
    echo "satelle pr body $id failed on the node; no PR opened" >&2
    exit 5
  fi
  { cat "$tmp/criteria.md"; echo; echo "$entry"; } >"$tmp/pr-body.md"
  if ! gh pr create -R "$repo" --base main --head "$branch" --title "release: $version ($id)" --body-file "$tmp/pr-body.md" >"$tmp/pr.txt"; then
    echo "gh pr create refused for $branch" >&2
    exit 5
  fi
  pr="$(gh pr list -R "$repo" --head "$branch" --state open --json number -q '.[0].number')"
fi
pr_url="$(gh pr view "$pr" -R "$repo" --json url -q .url)"

# The approval: satelle pr approve cold-reviews the exact head and, on accept,
# posts the status under GH_TOKEN. The merge below trusts only that status.
if ! on_node pr approve --story "$id" --repo "$repo" --binding pr-reviewer --execution "$execution" >"$tmp/approve.txt" 2>&1; then
  cat "$tmp/approve.txt" >&2
  echo "satelle pr approve did not approve $pr_url at $sha; nothing merged" >&2
  exit 8
fi
cat "$tmp/approve.txt"
approval="$(gh api "repos/$repo/commits/$sha/status" -q ".statuses[] | select(.context==\"$status_context\") | .state" | head -n 1)"
if [ "$approval" != success ]; then
  echo "$status_context on $sha is '${approval:-absent}', not success; nothing merged" >&2
  exit 8
fi

git fetch -q origin
if ! git merge-base --is-ancestor origin/main "$sha"; then
  echo "re-integrate locally: main moved after approval; nothing merged" >&2
  exit 3
fi
prev="$(git rev-parse origin/main)"
if ! gh pr merge "$pr" -R "$repo" --merge --match-head-commit "$sha" >&2; then
  echo "gh pr merge refused $pr_url at $sha" >&2
  exit 9
fi
merge_sha="$(gh pr view "$pr" -R "$repo" --json mergeCommit -q .mergeCommit.oid)"

# The push to main starts the test workflow; when GitHub lists none for the
# merge commit, dispatch it once on main (it is main's head) and keep waiting.
start=$SECONDS
dispatched=""
run=""
while :; do
  run="$(gh run list -R "$repo" --commit "$merge_sha" --workflow test.yml --json databaseId -q '.[0].databaseId' 2>"$tmp/gh.err" || true)"
  if [ -n "$run" ] && [ "$run" != null ]; then
    break
  fi
  if [ -z "$dispatched" ] && [ $(( SECONDS - start )) -ge "$dispatch_after" ]; then
    gh workflow run test.yml -R "$repo" --ref main >&2 && dispatched=yes
  fi
  if [ $(( SECONDS - start )) -ge "$ci_wait" ]; then
    cat "$tmp/gh.err" >&2
    echo "no test run listed for $merge_sha within ${ci_wait}s" >&2
    exit 6
  fi
  sleep "$poll"
done
if ! gh run watch "$run" -R "$repo" --exit-status >/dev/null 2>&1; then
  echo "test run $run failed for $merge_sha" >&2
  exit 6
fi
test_url="$(gh run view "$run" -R "$repo" --json url -q .url)"

start=$SECONDS
while ! gh release view "v$version" -R "$repo" --json url -q .url >"$tmp/tag.txt" 2>/dev/null; do
  failed="$(gh run list -R "$repo" --commit "$merge_sha" --workflow release.yml --json conclusion -q '[.[] | select(.conclusion=="failure")] | length' 2>/dev/null || echo 0)"
  if [ "${failed:-0}" != 0 ]; then
    echo "release run failed for $merge_sha; no v$version tag" >&2
    exit 7
  fi
  if [ $(( SECONDS - start )) -ge "$tag_wait" ]; then
    echo "no v$version release within ${tag_wait}s" >&2
    exit 7
  fi
  sleep "$poll"
done
release_url="$(gh run list -R "$repo" --commit "$merge_sha" --workflow release.yml --json url,conclusion -q '[.[] | select(.conclusion=="success")][0].url' 2>/dev/null || true)"

{
  echo "shipping: yes"
  echo "version: $version"
  [ -n "$serve" ] && echo "satelled: $serve (serve-v$serve)"
  echo "branch: $branch at $sha"
  echo "pr: $pr_url"
  echo "approval: $status_context success on $sha (satelle pr approve under GH_TOKEN)"
  echo "merge: $merge_sha onto main (was $prev)"
  echo "shipped commits:"
  git log --oneline "$prev..$sha" | sed 's/^/  /'
  echo "test: $test_url success"
  echo "release: ${release_url:-listed} success"
  echo "tag: $(cat "$tmp/tag.txt")"
  echo "dogfood: pending on the operator's machine (satelle update against v$version)"
} >"$tmp/live-evidence.md"

# The release has landed: a failed attach is reported, never turned into a
# failed release that the run's gates would retry.
if ! on_node story attach "$id" --name live-evidence --type output --file "$tmp/live-evidence.md" >&2; then
  echo "warning: live-evidence was not attached to $id on the node; it is in this output" >&2
fi
cat "$tmp/live-evidence.md"
