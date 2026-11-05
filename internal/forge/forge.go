// Package forge is the provider seam for the forge a repo is hosted on: its pull
// requests, their reviews and the commit statuses on their heads
// ([[satelle-local-first]] names "the forge for PR/merge/release" as a seam). A
// provider registers itself by name (Register) and is selected by the repo's
// [forge] provider (config.ForgeConfig); nothing outside a provider's own
// package names it. With no provider selected, Open says so and the caller
// posts nothing.
package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// PR is one pull request as the forge reports it.
type PR struct {
	// Repo is owner/name.
	Repo   string
	Number int
	// HeadRef is the branch the PR merges from; HeadSHA the commit at its tip.
	HeadRef string
	HeadSHA string
	// BaseSHA is the commit at the tip of the branch the PR merges into, as the
	// forge last computed it.
	BaseSHA string
	Body    string
	URL     string
}

// Ref is how a PR is named on the command line: <owner/name>#<n>.
func (p PR) Ref() string { return fmt.Sprintf("%s#%d", p.Repo, p.Number) }

// Review is a review the forge recorded.
type Review struct {
	ID     int64
	Author string
	// CommitID is the commit the review was recorded on.
	CommitID string
	State    string
}

// Status is one commit status. State is one of StatusSuccess, StatusFailure,
// StatusPending or StatusError.
type Status struct {
	Context     string
	State       string
	Description string
}

// The states a commit status may carry.
const (
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusPending = "pending"
	StatusError   = "error"
)

// Commit is one commit as the forge reports it.
type Commit struct {
	SHA string
	// Parents are the commit's parents, first parent first; more than one is a
	// merge.
	Parents []string
}

// Comparison is how a head commit compares with a base commit.
type Comparison struct {
	// Status is head relative to base: CompareAhead, CompareBehind,
	// CompareDiverged or CompareIdentical.
	Status string
	// Commits are the commits reachable from head and not from base.
	Commits []Commit
	// Files are the paths changed between base and head (both sides of a
	// rename).
	Files []string
	// Truncated says the forge did not list every commit or every file.
	Truncated bool
}

// The statuses a Comparison may carry.
const (
	CompareAhead     = "ahead"
	CompareBehind    = "behind"
	CompareDiverged  = "diverged"
	CompareIdentical = "identical"
)

// Forge is what a provider answers.
type Forge interface {
	// PullRequestForHead is the one open PR on repo whose head branch is head:
	// ErrNoPR when there is none, a *ManyPRsError when there are several.
	PullRequestForHead(ctx context.Context, repo, head string) (PR, error)
	// PullRequest is PR n of repo.
	PullRequest(ctx context.Context, repo string, n int) (PR, error)
	// Diff is PR n's diff, as the forge renders it.
	Diff(ctx context.Context, repo string, n int) (string, error)
	// Approve records an approving review of PR n on commit sha, and only on it:
	// when the PR's head is no longer sha it posts nothing and returns a
	// *HeadMovedError. When the forge refuses because the token's user authored
	// the PR, the error wraps ErrSelfApproval.
	Approve(ctx context.Context, repo string, n int, sha, body string) (Review, error)
	// Comment records a COMMENT review of PR n on commit sha, under the same head
	// guard as Approve.
	Comment(ctx context.Context, repo string, n int, sha, body string) (Review, error)
	// SetStatus posts s on commit sha of repo.
	SetStatus(ctx context.Context, repo, sha string, s Status) error
	// CommitStatuses are the statuses on commit sha of repo, the latest of each
	// context.
	CommitStatuses(ctx context.Context, repo, sha string) ([]Status, error)
	// Compare is how commit head compares with commit base on repo.
	Compare(ctx context.Context, repo, base, head string) (Comparison, error)
	// CommitFiles are the paths commit sha changes against its first parent
	// (both sides of a rename); truncated says the forge did not list them all.
	CommitFiles(ctx context.Context, repo, sha string) (files []string, truncated bool, err error)
}

// ErrNoPR is PullRequestForHead's answer when no open PR has the head.
var ErrNoPR = errors.New("no open PR")

// ErrSelfApproval is what Approve's error wraps when the forge refuses an
// approving review because the token's user authored the PR.
var ErrSelfApproval = errors.New("the forge refuses an approving review from the PR's author")

// ManyPRsError is PullRequestForHead's answer when more than one open PR has the
// head, so the PR to judge is not known.
type ManyPRsError struct {
	Head    string
	Numbers []int
}

func (e *ManyPRsError) Error() string {
	return fmt.Sprintf("%d open PRs have head %s (%v); the PR to review is ambiguous", len(e.Numbers), e.Head, e.Numbers)
}

// HeadMovedError is Approve's refusal: the PR's head is not the commit that was
// reviewed.
type HeadMovedError struct {
	Reviewed, Now string
}

func (e *HeadMovedError) Error() string {
	return fmt.Sprintf("the PR head moved from the reviewed %s to %s; not approved", e.Reviewed, e.Now)
}

// Config is what a provider is opened with.
type Config struct {
	// Host is the forge's web host (github.com, or an enterprise host).
	Host string
	// APIURL, when set, replaces the API base the provider derives from Host.
	APIURL string
	// Token is the bearer the provider calls the API with. It is never logged.
	Token string
	// HTTP is the client; nil is a default with a timeout.
	HTTP *http.Client
}

// Provider opens a forge from cfg.
type Provider func(cfg Config) (Forge, error)

var (
	registryMu sync.Mutex
	registry   = map[string]Provider{}
)

// Register makes a provider selectable by name. A provider package calls it from
// init.
func Register(name string, p Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = p
}

// ErrNoProvider is Open's answer when no provider is selected or the selected
// one is not in this build: the caller warns and posts nothing.
var ErrNoProvider = errors.New("no forge provider")

// Open opens the provider name with cfg. An empty name or "none", or a name no
// provider registered, is ErrNoProvider wrapped with what was asked for.
func Open(name string, cfg Config) (Forge, error) {
	registryMu.Lock()
	p, ok := registry[strings.TrimSpace(name)]
	known := make([]string, 0, len(registry))
	for n := range registry {
		known = append(known, n)
	}
	registryMu.Unlock()
	if !ok {
		sort.Strings(known)
		return nil, fmt.Errorf("%w %q selected (this build has %s)", ErrNoProvider, name, strings.Join(known, ", "))
	}
	return p(cfg)
}

// SplitRef parses <owner/name>#<n>.
func SplitRef(ref string) (repo string, n int, err error) {
	repo, num, ok := strings.Cut(strings.TrimSpace(ref), "#")
	if !ok || strings.Count(repo, "/") != 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return "", 0, fmt.Errorf("PR %q is not <owner/name>#<number>", ref)
	}
	if _, err := fmt.Sscanf(num, "%d", &n); err != nil || n <= 0 || fmt.Sprint(n) != num {
		return "", 0, fmt.Errorf("PR %q is not <owner/name>#<number>", ref)
	}
	return repo, n, nil
}

// EpicHeadPrefix is the branch prefix a story's worktree is cut on.
const EpicHeadPrefix = "epic/"

// EpicHead is the branch a story's PR is opened from: epic/<story id>, the
// branch convention a story's worktree is cut on.
func EpicHead(storyID string) string { return EpicHeadPrefix + storyID }

// StoryHeads are the branches a story's PR may be opened from: each prefix
// followed by the story id, in order. No prefixes is EpicHead alone.
func StoryHeads(storyID string, prefixes []string) []string {
	if len(prefixes) == 0 {
		return []string{EpicHead(storyID)}
	}
	heads := make([]string, len(prefixes))
	for i, p := range prefixes {
		heads[i] = p + storyID
	}
	return heads
}
