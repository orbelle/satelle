// `satelle pr` reads and approves a story's pull request through the forge seam
// (internal/forge). `pr body` renders the story's criteria block for the PR body,
// the one way a story's criteria reach a remote node that holds no story; `pr
// view` and `pr diff` are what the cold reviewer reads with; `pr approve` is the
// action of the embedded task tsk_pr-approve.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/forge"
	_ "github.com/bobmcallan/satelle/internal/forge/github" // the github provider registers itself
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/reviewscore"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// Seams a test replaces; production leaves them alone.
var (
	// forgeAPIURL, when set, replaces the API base the provider derives from the
	// repo's host.
	forgeAPIURL string
	// forgeHTTP is the provider's HTTP client; nil is its default.
	forgeHTTP *http.Client
	// prReview, when set, replaces the cold review dispatch.
	prReview func(ctx context.Context, cmd *cobra.Command, binding string, req forge.ReviewRequest) (forge.Verdict, error)
)

const (
	// prReviewSkill is the embedded rubric the cold review runs under.
	prReviewSkill = "satelle-pr-review"
	// defaultTokenEnv is the variable the forge token is read from when the
	// node's definition declares no github-app override.
	defaultTokenEnv = "GH_TOKEN"
)

func init() {
	pr := &cobra.Command{
		Use:   "pr",
		Short: "Read and approve a story's pull request through the forge",
		Long: `Read and approve a story's pull request through the configured forge
([forge] provider in satelle.toml; github by default).

On a remote node the repo is the definition's single [[repo]] and the token is
read from the variable its github-app override names; elsewhere pass --repo, and
the token is read from GH_TOKEN.`,
	}
	var repo string
	pr.PersistentFlags().StringVar(&repo, "repo", "", "owner/name of the repo (default: the node definition's single [[repo]])")

	body := &cobra.Command{
		Use:   "body <story_id>",
		Short: "Print the story's criteria block for its PR body",
		Long: `Print the marked block carrying the story's acceptance criteria and their
sha256. Put it in the story's PR body: a remote reviewer reads the criteria
from there with ` + "`satelle pr view <repo>#<n> --criteria`" + `.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			var it workitem.Item
			if err := dispatchInto(cmd.Context(), "story-get", map[string]any{"id": args[0]}, &it); err != nil {
				return err
			}
			if strings.TrimSpace(it.AcceptanceCriteria) == "" {
				return fmt.Errorf("pr body: %s has no acceptance criteria", it.ID)
			}
			fmt.Fprint(cmd.OutOrStdout(), forge.RenderCriteria(it.ID, it.Title, it.AcceptanceCriteria))
			return nil
		},
	}

	var criteria bool
	var story string
	view := &cobra.Command{
		Use:   "view <owner/name>#<n>",
		Short: "Print a PR: its head, or with --criteria the story criteria on its body",
		Long: `Print a pull request's head branch, head commit, URL and body. With
--criteria print only the acceptance criteria carried in the body's satelle
criteria block (written by ` + "`satelle pr body`" + `), with their hash and the PR's
head; a missing, foreign (--story) or tampered block is an error. This is how a
remote reviewer, which holds no story, reads the criteria it judges against.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, target, n, err := openPRForge(args[0])
			if err != nil {
				return err
			}
			p, err := f.PullRequest(cmd.Context(), target, n)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if criteria {
				c, err := forge.ParseCriteria(p.Body, story)
				if err != nil {
					return fmt.Errorf("pr view %s: %w", p.Ref(), err)
				}
				fmt.Fprintf(out, "story: %s\ncriteria: %s\nhead: %s\n\n%s\n", c.StoryID, c.Hash, p.HeadSHA, c.Text)
				return nil
			}
			fmt.Fprintf(out, "pr: %s\nurl: %s\nhead: %s at %s\n\n%s\n", p.Ref(), p.URL, p.HeadRef, p.HeadSHA, p.Body)
			return nil
		},
	}
	view.Flags().BoolVar(&criteria, "criteria", false, "print only the story's acceptance criteria from the PR body's criteria block")
	view.Flags().StringVar(&story, "story", "", "with --criteria, refuse a block that names another story")

	diff := &cobra.Command{
		Use:   "diff <owner/name>#<n>",
		Short: "Print a PR's diff",
		Long: `Print a pull request's diff as the forge renders it: the change a remote
reviewer judges, read through the configured forge with the run's token.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, target, n, err := openPRForge(args[0])
			if err != nil {
				return err
			}
			d, err := f.Diff(cmd.Context(), target, n)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), d)
			return nil
		},
	}

	var approveStory, execution, binding string
	approve := &cobra.Command{
		Use:   "approve",
		Short: "Cold-review a story's PR and, on accept, approve its exact head",
		Long: `Find the open PR whose head is epic/<story>, check its body carries the
story's criteria block and that the repo's constitution (principle
repo-constitution) is installed, have the satelle-pr-review skill judge it cold from
pointers alone, and only on accept post an APPROVE review on the exact head it
judged plus a commit status ([forge.github] status_context). A head that moved
since the review is not approved. The outcome and findings are recorded on
--execution's run-output doc.`,
		Args:        cobra.NoArgs,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPRApprove(cmd, repo, approveStory, execution, binding)
		},
	}
	approve.Flags().StringVar(&approveStory, "story", "", "the story whose PR (head epic/<story>) is approved")
	approve.Flags().StringVar(&execution, "execution", "", "the execution the outcome and findings are recorded on")
	approve.Flags().StringVar(&binding, "binding", "reviewer", "the agents.toml binding the cold review runs under")
	_ = approve.MarkFlagRequired("story")
	_ = approve.MarkFlagRequired("execution")

	pr.AddCommand(body, view, diff, approve)
	register(pr)
}

// runPRApprove is `pr approve`: it builds the approval from the repo's forge
// config and runs it (forge.RunApproval). No forge provider is the approval's
// no-op, with a warning naming what was selected; a provider that cannot be
// opened (no token, say) is its reject.
func runPRApprove(cmd *cobra.Command, repoFlag, story, execution, binding string) error {
	process, _, _, _, err := config.LoadInvokingProcess()
	if err != nil {
		return err
	}
	target, err := resolvePRTarget(repoFlag)
	if err != nil {
		return err
	}
	a := forge.Approval{
		Repo: target.repo, StoryID: story, StatusContext: process.Forge.ResolvedStatusContext(),
		Constitution: forge.ConstitutionInstalled,
		Review: func(ctx context.Context, req forge.ReviewRequest) (forge.Verdict, error) {
			if prReview != nil {
				return prReview(ctx, cmd, binding, req)
			}
			return coldReview(ctx, cmd, binding, req)
		},
	}
	switch f, ferr := target.open(process.Forge.ResolvedProvider()); {
	case ferr == nil:
		a.Forge = f
	case errors.Is(ferr, forge.ErrNoProvider):
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: pr approve: %v\n", ferr) // no forge: posts nothing
	default:
		a.OpenErr = ferr
	}
	res, err := forge.RunApproval(cmd.Context(), a, execution)
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(map[string]any{
		"outcome": res.Outcome, "findings": res.Findings, "pr": prRefOrEmpty(res.PR),
		"head": res.PR.HeadSHA, "criteria": res.CriteriaHash, "review_id": res.Review.ID, "review_author": res.Review.Author,
	}, "", "  ")
	fmt.Fprintln(cmd.OutOrStdout(), string(out))
	return nil
}

func prRefOrEmpty(p forge.PR) string {
	if p.Number == 0 {
		return ""
	}
	return p.Ref()
}

// coldReview dispatches the satelle-pr-review skill on the isolated reviewer path a gate
// uses, handing it pointers only: the story id, the PR, the head it judges and
// the commands it reads the criteria, the constitution and the diff with. No
// diff, criteria or constitution is put in its prompt.
func coldReview(ctx context.Context, cmd *cobra.Command, binding string, req forge.ReviewRequest) (forge.Verdict, error) {
	a, err := appFrom(cmd)
	if err != nil {
		return forge.Verdict{}, err
	}
	opts, err := caseJudgeOptions(a, binding, nil)
	if err != nil {
		return forge.Verdict{}, err
	}
	opts.NoConstitution = true
	j, err := agentstep.NewCaseJudge(opts)
	if err != nil {
		return forge.Verdict{}, err
	}
	res, err := j.Judge(ctx, reviewscore.Case{
		ID: prReviewSkill + "-" + req.StoryID, Skill: prReviewSkill, StoryID: req.StoryID, From: "backlog", To: "plan",
		Payload: &reviewscore.ReplayPayload{
			Title:    "Review " + req.PR.Ref() + " for " + req.StoryID,
			Body:     reviewPointers(req),
			Category: "feature", NoPatch: true,
		},
	})
	if err != nil {
		return forge.Verdict{}, err
	}
	return forge.Verdict{Accept: res.Verdict == reviewscore.VerdictAccept, Findings: res.Notes}, nil
}

// reviewPointers is the whole of what the cold review is told.
func reviewPointers(req forge.ReviewRequest) string {
	ref := req.PR.Ref()
	return fmt.Sprintf(`Judge pull request %s at head %s for story %s under the satelle-pr-review rubric.
Read everything yourself, with these commands:
- the story's acceptance criteria: satelle pr view %s --criteria --story %s
- the repo's constitution: satelle doc get principles %s
- the change: satelle pr diff %s`, ref, req.PR.HeadSHA, req.StoryID, ref, req.StoryID, forge.RepoConstitution, ref)
}

// prTarget is the repo a `pr` command acts on and how it reaches the forge.
type prTarget struct {
	repo, host, tokenEnv string
}

// resolvePRTarget is the repo --repo names, else the node definition's single
// [[repo]], with the token variable its github-app override on that host names.
func resolvePRTarget(repoFlag string) (prTarget, error) {
	t := prTarget{repo: strings.TrimSpace(repoFlag), tokenEnv: defaultTokenEnv}
	raw, onNode := os.LookupEnv(remotedef.EnvDefinition)
	if !onNode {
		if t.repo == "" {
			return prTarget{}, fmt.Errorf("pr: pass --repo owner/name (no node definition in %s)", remotedef.EnvDefinition)
		}
		return t, nil
	}
	def, err := remotedef.FromEnvValue(raw)
	if err != nil {
		return prTarget{}, err
	}
	if t.repo == "" {
		if len(def.Repos) != 1 {
			return prTarget{}, fmt.Errorf("pr: the node definition declares %d [[repo]] entries; pass --repo", len(def.Repos))
		}
		host, slug, ok := remotedef.RepoSlug(def.Repos[0].URL)
		if !ok {
			return prTarget{}, fmt.Errorf("pr: the node definition's [[repo]] %q is not host/owner/name", def.Repos[0].URL)
		}
		t.repo, t.host = slug, host
	}
	if key, ok := def.GitHubAppKey(t.host); ok {
		t.tokenEnv, t.host = key, remotedef.DefaultGitHubHost
	}
	return t, nil
}

// open opens the provider with the target's token.
func (t prTarget) open(provider string) (forge.Forge, error) {
	return forge.Open(provider, forge.Config{Host: t.host, APIURL: forgeAPIURL, Token: os.Getenv(t.tokenEnv), HTTP: forgeHTTP})
}

// openPRForge parses a PR ref and opens the configured forge for its repo.
func openPRForge(ref string) (forge.Forge, string, int, error) {
	repo, n, err := forge.SplitRef(ref)
	if err != nil {
		return nil, "", 0, err
	}
	process, _, _, _, err := config.LoadInvokingProcess()
	if err != nil {
		return nil, "", 0, err
	}
	t, err := resolvePRTarget(repo)
	if err != nil {
		return nil, "", 0, err
	}
	f, err := t.open(process.Forge.ResolvedProvider())
	if err != nil {
		return nil, "", 0, err
	}
	return f, repo, n, nil
}
