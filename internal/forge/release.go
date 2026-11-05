package forge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// ReleaseApproval is the mechanical approval of a batch release PR. It runs no
// cold review, since each story the release merges was cold-reviewed on its own
// PR. It approves the release PR's exact head only when every story the PR
// body lists was approved on its own PR's head, each of those heads is an
// ancestor of the release head, and beyond them the release head changes only
// release files.
type ReleaseApproval struct {
	// Forge is the provider; nil is no provider, which warns and posts nothing.
	Forge Forge
	// OpenErr, when set, is why the selected provider could not be opened: the
	// approval is rejected naming it.
	OpenErr error
	// Repo is owner/name of the repo the release PR is on; Number is the PR.
	Repo   string
	Number int
	// ReleaseHeads are the branch prefixes a release PR is opened from ([forge]
	// release_heads); a PR whose head has none of them is refused.
	ReleaseHeads []string
	// PRHeads are the branch prefixes a story's PR is opened from ([forge]
	// pr_heads); empty is EpicHead alone.
	PRHeads []string
	// ReleaseFiles are the path globs (path.Match) the release may change beyond
	// its stories ([forge] release_files).
	ReleaseFiles []string
	// StatusContext names the commit status each story PR's head must carry as
	// success, and the one an accept posts on the release head.
	StatusContext string
	// Warn receives the warning a run with no forge provider gives; nil discards.
	Warn io.Writer
}

// releaseStory is one story the release merges and the PR it was approved on.
type releaseStory struct {
	ID string
	PR PR
	// commits are the story head's commits beyond the release base, and files
	// the paths they change.
	commits map[string]bool
	files   map[string]bool
}

func (a ReleaseApproval) run(ctx context.Context) Result {
	if res, stop := unopened(a.Forge, a.OpenErr, a.Warn); stop {
		return res
	}
	pr, err := a.Forge.PullRequest(ctx, a.Repo, a.Number)
	if err != nil {
		return reject(Result{}, fmt.Sprintf("the release PR %s#%d could not be read: %v", a.Repo, a.Number, err))
	}
	res := Result{PR: pr}
	if !hasAnyPrefix(pr.HeadRef, a.ReleaseHeads) {
		return reject(res, fmt.Sprintf("%s has head %s, which matches no release head (%s): it is not a release PR",
			pr.Ref(), pr.HeadRef, strings.Join(a.ReleaseHeads, ", ")))
	}
	listed, err := ParseReleaseStories(pr.Body)
	if err != nil {
		return reject(res, fmt.Sprintf("%s: %v", pr.Ref(), err))
	}
	res.Stories = listed.Stories
	stories, why := a.storyPRs(ctx, listed)
	if why == "" {
		why = a.unapproved(ctx, stories)
	}
	if why == "" {
		why = a.notAncestors(ctx, pr.HeadSHA, stories)
	}
	if why == "" {
		why = a.strayChanges(ctx, pr, stories)
	}
	if why != "" {
		return reject(res, why)
	}
	ids := strings.Join(listed.Stories, " ")
	var findings strings.Builder
	fmt.Fprintf(&findings, "mechanical release approval of %s at %s: stories %s were each approved on their own heads, each head is an ancestor of the release head, and beyond them only release files changed", pr.Ref(), pr.HeadSHA, ids)
	for _, s := range stories {
		fmt.Fprintf(&findings, "\n- %s: %s at %s", s.ID, s.PR.Ref(), s.PR.HeadSHA)
	}
	body := fmt.Sprintf("satelle: a mechanical release approval: stories %s each approved on their own heads, each an ancestor of %s, and beyond them only release files changed.", ids, pr.HeadSHA)
	status := Status{Context: a.StatusContext, State: StatusSuccess, Description: "release approved: " + ids}
	return post(ctx, a.Forge, a.Repo, res, body, status, findings.String())
}

// storyPRs ties each listed story to the one listed PR on the release's repo
// whose head is one of the story's heads, or says which stories have none.
func (a ReleaseApproval) storyPRs(ctx context.Context, listed ReleaseStories) ([]releaseStory, string) {
	var prs []PR
	for _, ref := range listed.PRs {
		if ref.Repo != a.Repo {
			continue // a PR on another repo cannot be merged into this release
		}
		p, err := a.Forge.PullRequest(ctx, ref.Repo, ref.Number)
		if err != nil {
			return nil, fmt.Sprintf("the listed story PR %s could not be read: %v", ref, err)
		}
		prs = append(prs, p)
	}
	var stories []releaseStory
	var missing []string
	for _, id := range listed.Stories {
		heads := StoryHeads(id, a.PRHeads)
		var found []PR
		for _, p := range prs {
			for _, h := range heads {
				if p.HeadRef == h {
					found = append(found, p)
				}
			}
		}
		switch len(found) {
		case 1:
			stories = append(stories, releaseStory{ID: id, PR: found[0]})
		case 0:
			missing = append(missing, fmt.Sprintf("%s (no listed PR on %s has head %s)", id, a.Repo, strings.Join(heads, " or ")))
		default:
			refs := make([]string, len(found))
			for i, p := range found {
				refs[i] = p.Ref()
			}
			missing = append(missing, fmt.Sprintf("%s (listed PRs %s all have its heads; the PR it was approved on is ambiguous)", id, strings.Join(refs, ", ")))
		}
	}
	if len(missing) > 0 {
		return nil, "listed stories with no story PR on the release PR body:\n- " + strings.Join(missing, "\n- ")
	}
	return stories, ""
}

// unapproved names each story whose PR's head carries no success status under
// the approval's context.
func (a ReleaseApproval) unapproved(ctx context.Context, stories []releaseStory) string {
	var bad []string
	for _, s := range stories {
		st, err := a.Forge.CommitStatuses(ctx, a.Repo, s.PR.HeadSHA)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s (the statuses of %s at %s could not be read: %v)", s.ID, s.PR.Ref(), s.PR.HeadSHA, err))
			continue
		}
		if !hasSuccess(st, a.StatusContext) {
			bad = append(bad, fmt.Sprintf("%s (%s at %s has no %s %s status)", s.ID, s.PR.Ref(), s.PR.HeadSHA, StatusSuccess, a.StatusContext))
		}
	}
	if len(bad) > 0 {
		return "stories not approved on their own heads:\n- " + strings.Join(bad, "\n- ")
	}
	return ""
}

// notAncestors names each story whose PR's head is not an ancestor of the
// release head.
func (a ReleaseApproval) notAncestors(ctx context.Context, releaseHead string, stories []releaseStory) string {
	var bad []string
	for _, s := range stories {
		c, err := a.Forge.Compare(ctx, a.Repo, s.PR.HeadSHA, releaseHead)
		switch {
		case err != nil:
			bad = append(bad, fmt.Sprintf("%s (%s could not be compared with the release head: %v)", s.ID, s.PR.HeadSHA, err))
		case c.Status != CompareAhead && c.Status != CompareIdentical:
			bad = append(bad, fmt.Sprintf("%s (the release head is %s of its head %s, so that head is not an ancestor)", s.ID, c.Status, s.PR.HeadSHA))
		}
	}
	if len(bad) > 0 {
		return fmt.Sprintf("story heads not in the release head %s:\n- %s", releaseHead, strings.Join(bad, "\n- "))
	}
	return ""
}

// strayChanges names each commit on the release head beyond its base and its
// stories' heads that changes more than it may: a non-merge commit any path
// that is not a release file, and a merge commit that joins no story head or
// changes a path outside its story's own changes and the release files.
func (a ReleaseApproval) strayChanges(ctx context.Context, pr PR, stories []releaseStory) string {
	if pr.BaseSHA == "" {
		return fmt.Sprintf("the forge reported no base commit for %s, so its changes cannot be judged", pr.Ref())
	}
	tooLarge := func(what string) string {
		return fmt.Sprintf("%s is too large to judge mechanically: the forge did not list all of it", what)
	}
	rel, err := a.Forge.Compare(ctx, a.Repo, pr.BaseSHA, pr.HeadSHA)
	if err != nil {
		return fmt.Sprintf("the release head %s could not be compared with its base %s: %v", pr.HeadSHA, pr.BaseSHA, err)
	}
	if rel.Truncated {
		return tooLarge(fmt.Sprintf("the release range %s...%s", pr.BaseSHA, pr.HeadSHA))
	}
	byHead := map[string]*releaseStory{}
	for i := range stories {
		s := &stories[i]
		c, err := a.Forge.Compare(ctx, a.Repo, pr.BaseSHA, s.PR.HeadSHA)
		if err != nil {
			return fmt.Sprintf("%s's head %s could not be compared with the release base %s: %v", s.ID, s.PR.HeadSHA, pr.BaseSHA, err)
		}
		if c.Truncated {
			return tooLarge(fmt.Sprintf("%s's range %s...%s", s.ID, pr.BaseSHA, s.PR.HeadSHA))
		}
		s.commits, s.files = map[string]bool{}, map[string]bool{}
		for _, cm := range c.Commits {
			s.commits[cm.SHA] = true
		}
		for _, f := range c.Files {
			s.files[f] = true
		}
		byHead[s.PR.HeadSHA] = s
	}
	var bad []string
	for _, cm := range rel.Commits {
		if inAnyStory(cm.SHA, stories) {
			continue
		}
		var joined *releaseStory
		if len(cm.Parents) > 1 {
			for _, p := range cm.Parents[1:] {
				if s := byHead[p]; s != nil {
					joined = s
					break
				}
			}
			if joined == nil {
				bad = append(bad, fmt.Sprintf("merge commit %s joins %s, none of them a story head", cm.SHA, strings.Join(cm.Parents[1:], ", ")))
				continue
			}
		}
		files, truncated, err := a.Forge.CommitFiles(ctx, a.Repo, cm.SHA)
		switch {
		case err != nil:
			bad = append(bad, fmt.Sprintf("commit %s: its files could not be read: %v", cm.SHA, err))
			continue
		case truncated:
			bad = append(bad, tooLarge("commit "+cm.SHA))
			continue
		}
		for _, f := range files {
			switch {
			case a.releaseFile(f):
			case joined == nil:
				bad = append(bad, fmt.Sprintf("commit %s changes %s, which is not a release file", cm.SHA, f))
			case !joined.files[f]:
				bad = append(bad, fmt.Sprintf("merge commit %s of %s changes %s, outside %s's own changes and the release files", cm.SHA, joined.ID, f, joined.ID))
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Sprintf("the release head changes more than its stories and the release files (%s):\n- %s", strings.Join(a.ReleaseFiles, ", "), strings.Join(bad, "\n- "))
	}
	return ""
}

// releaseFile reports whether p matches one of the release file globs.
func (a ReleaseApproval) releaseFile(p string) bool {
	for _, g := range a.ReleaseFiles {
		if ok, err := path.Match(g, p); err == nil && ok {
			return true
		}
	}
	return false
}

func inAnyStory(sha string, stories []releaseStory) bool {
	for _, s := range stories {
		if s.commits[sha] {
			return true
		}
	}
	return false
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func hasSuccess(statuses []Status, statusContext string) bool {
	for _, s := range statuses {
		if s.Context == statusContext && s.State == StatusSuccess {
			return true
		}
	}
	return false
}

// PRRef names one PR: <owner/name>#<n>.
type PRRef struct {
	Repo   string
	Number int
}

func (r PRRef) String() string { return fmt.Sprintf("%s#%d", r.Repo, r.Number) }

// ReleaseStories is what a release PR's body lists: a line `Stories: <id> <id>
// …`, then each story PR's URL on the lines after it.
type ReleaseStories struct {
	Stories []string
	// PRs are the PR URLs on the lines after the Stories line, in order.
	PRs []PRRef
}

// ErrNoStories is ParseReleaseStories' answer for a body with no Stories line,
// or one that lists no story.
var ErrNoStories = errors.New("the release PR body carries no `Stories: <id> <id> …` line naming its stories")

var urlPattern = regexp.MustCompile(`https?://\S+`)

// ParseReleaseStories reads the stories a release PR body lists and the PR
// URLs that follow them. URLs that are not PR URLs are ignored.
func ParseReleaseStories(body string) (ReleaseStories, error) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var out ReleaseStories
	for i, ln := range lines {
		list, ok := strings.CutPrefix(strings.TrimSpace(ln), "Stories:")
		if !ok {
			continue
		}
		seen := map[string]bool{}
		for _, id := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
			if !seen[id] {
				seen[id] = true
				out.Stories = append(out.Stories, id)
			}
		}
		if len(out.Stories) == 0 {
			return ReleaseStories{}, ErrNoStories
		}
		for _, rest := range lines[i+1:] {
			for _, u := range urlPattern.FindAllString(rest, -1) {
				if repo, n, err := ParsePRURL(strings.TrimRight(u, ").,;>]")); err == nil {
					out.PRs = append(out.PRs, PRRef{Repo: repo, Number: n})
				}
			}
		}
		return out, nil
	}
	return ReleaseStories{}, ErrNoStories
}

// ParsePRURL parses a PR's web URL, https://<host>/<owner>/<name>/pull/<n>.
func ParsePRURL(raw string) (repo string, n int, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", 0, fmt.Errorf("%q is not a PR URL", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" {
		return "", 0, fmt.Errorf("%q is not a PR URL", raw)
	}
	if n, err = strconv.Atoi(parts[3]); err != nil || n <= 0 || strconv.Itoa(n) != parts[3] {
		return "", 0, fmt.Errorf("%q is not a PR URL", raw)
	}
	return parts[0] + "/" + parts[1], n, nil
}
