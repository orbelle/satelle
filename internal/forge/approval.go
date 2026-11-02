package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bobmcallan/satelle/internal/verb"
)

// Approval is one PR approval of a story: find the story's PR, check the
// reviewer can read the story's criteria and the repo's constitution, have the
// cold review judge it, and only on accept approve the exact head it judged and
// post a commit status there.
type Approval struct {
	// Forge is the provider; nil is no provider, which warns and posts nothing.
	Forge Forge
	// OpenErr, when set, is why the selected provider could not be opened (a
	// missing token, say): the approval is rejected naming it.
	OpenErr error
	// Repo is owner/name of the repo the PR is on.
	Repo string
	// StoryID is the story whose PR is judged: the open PR whose head is
	// EpicHead(StoryID).
	StoryID string
	// StatusContext names the commit status an accept posts.
	StatusContext string
	// Constitution reports whether the repo's constitution is installed where the
	// reviewer reads it; an error stops the approval before the review.
	Constitution func(ctx context.Context) error
	// Review is the cold review: it is handed pointers only and answers a verdict.
	Review func(ctx context.Context, req ReviewRequest) (Verdict, error)
	// Warn receives the warning a run with no forge provider gives; nil discards.
	Warn io.Writer
}

// ReviewRequest is everything the cold review is handed: which story, which PR
// and which commit. The criteria, the constitution and the diff are read by the
// reviewer itself.
type ReviewRequest struct {
	StoryID string
	PR      PR
}

// Verdict is the cold review's answer.
type Verdict struct {
	Accept   bool
	Findings string
}

// Result is how an approval ended.
type Result struct {
	// Outcome is verb.OutcomeAccept, verb.OutcomeReject or, with no forge
	// provider, verb.OutcomeNoOp.
	Outcome string
	// Findings say why: the reviewer's findings, or what stopped the approval.
	Findings string
	PR       PR
	// CriteriaHash is the hash of the criteria the review was judged against.
	CriteriaHash string
	// Review is the approving review, on accept.
	Review Review
}

// Record is the run-output doc text of r: the outcome's facts, then the
// findings.
func (r Result) Record() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", strings.TrimSpace(r.Findings))
	if r.PR.Number > 0 {
		fmt.Fprintf(&b, "\nPR: %s at %s\n", r.PR.Ref(), r.PR.HeadSHA)
	}
	if r.CriteriaHash != "" {
		fmt.Fprintf(&b, "Criteria: %s\n", r.CriteriaHash)
	}
	if r.Review.ID != 0 {
		fmt.Fprintf(&b, "Review: %d by %s on %s\n", r.Review.ID, r.Review.Author, r.Review.CommitID)
	}
	return strings.TrimSpace(b.String())
}

// RunApproval runs a and records its outcome and findings on execution execID's
// run-output doc (the record a task run's outcome is read from). An empty execID
// records nothing. The error is a record that could not be written; every other
// failure is the result's reject.
func RunApproval(ctx context.Context, a Approval, execID string) (Result, error) {
	res := a.run(ctx)
	if execID == "" {
		return res, nil
	}
	req, err := json.Marshal(map[string]any{"id": execID, "output": res.Record(), "outcome": res.Outcome})
	if err != nil {
		return res, err
	}
	if _, err := verb.Dispatch(ctx, "execution-record", req); err != nil {
		return res, fmt.Errorf("record the approval on %s: %w", execID, err)
	}
	return res, nil
}

func (a Approval) run(ctx context.Context) Result {
	reject := func(res Result, why string) Result {
		res.Outcome, res.Findings = verb.OutcomeReject, why
		return res
	}
	if a.OpenErr != nil {
		return reject(Result{}, fmt.Sprintf("the forge could not be opened: %v", a.OpenErr))
	}
	if a.Forge == nil {
		why := "no forge provider is configured: the PR was not read and nothing was posted"
		if a.Warn != nil {
			fmt.Fprintf(a.Warn, "warning: %s\n", why)
		}
		return Result{Outcome: verb.OutcomeNoOp, Findings: why}
	}
	head := EpicHead(a.StoryID)
	pr, err := a.Forge.PullRequestForHead(ctx, a.Repo, head)
	if errors.Is(err, ErrNoPR) {
		return reject(Result{}, fmt.Sprintf("no open PR on %s has head %s", a.Repo, head))
	}
	if err != nil {
		return reject(Result{}, fmt.Sprintf("the PR with head %s on %s could not be read: %v", head, a.Repo, err))
	}
	res := Result{PR: pr}
	crit, err := ParseCriteria(pr.Body, a.StoryID)
	if err != nil {
		return reject(res, fmt.Sprintf("criteria: %v; the review needs %s's criteria on the PR body (`satelle pr body %s`)", err, a.StoryID, a.StoryID))
	}
	res.CriteriaHash = crit.Hash
	if a.Constitution != nil {
		if err := a.Constitution(ctx); err != nil {
			return reject(res, fmt.Sprintf("constitution not installed: %v", err))
		}
	}
	v, err := a.Review(ctx, ReviewRequest{StoryID: a.StoryID, PR: pr})
	if err != nil {
		return reject(res, fmt.Sprintf("the review could not run: %v", err))
	}
	if !v.Accept {
		return reject(res, v.Findings)
	}
	rv, err := a.Forge.Approve(ctx, a.Repo, pr.Number, pr.HeadSHA, approvalBody(a.StoryID, crit.Hash))
	if err != nil {
		var moved *HeadMovedError
		if errors.As(err, &moved) {
			return reject(res, fmt.Sprintf("head moved: %v", err))
		}
		return reject(res, fmt.Sprintf("the approval could not be posted: %v", err))
	}
	res.Review = rv
	status := Status{Context: a.StatusContext, State: StatusSuccess, Description: "approved: " + a.StoryID + " meets its acceptance criteria"}
	if err := a.Forge.SetStatus(ctx, a.Repo, pr.HeadSHA, status); err != nil {
		return reject(res, fmt.Sprintf("approved as review %d, but the %s status could not be posted: %v", rv.ID, a.StatusContext, err))
	}
	res.Outcome, res.Findings = verb.OutcomeAccept, v.Findings
	if strings.TrimSpace(res.Findings) == "" {
		res.Findings = "the cold review accepted the PR"
	}
	return res
}

// RepoConstitution is the principle the cold review reads the repo's
// constitution from: on a remote node, the definition bundle's
// principles/repo-constitution.md.
const RepoConstitution = "repo-constitution"

// ConstitutionInstalled is Approval.Constitution as a node checks it: principle
// RepoConstitution resolves, the way `satelle doc get principles
// repo-constitution` resolves it for the reviewer.
func ConstitutionInstalled(ctx context.Context) error {
	req, _ := json.Marshal(map[string]string{"kind": "principles", "name": RepoConstitution})
	if _, err := verb.Dispatch(ctx, "doc-get", req); err != nil {
		return fmt.Errorf("principle %s does not resolve (the remote's definition bundle carries principles/%s.md): %w", RepoConstitution, RepoConstitution, err)
	}
	return nil
}

// approvalBody is the text of the approving review.
func approvalBody(storyID, hash string) string {
	return fmt.Sprintf("satelle: a cold review judged this head to meet the acceptance criteria of %s (%s) and the repo's constitution.", storyID, hash)
}
