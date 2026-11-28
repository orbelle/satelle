package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

func githubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Publish to the bound project's GitHub repository through satelle.dev",
		Long: `The satelle GitHub app, installed on a repository, binds it to a satelle.dev
project. These commands ask satelle.dev to act as the app on the repository it
resolves for the item among the project's bound repositories; the repository
is never named on the command line. The item comes from --item, else from
SATELLE_ITEM, which a gate sets for every check. satelle.dev decides nothing:
what to publish (a review's event and body, a status's state, context and
description) is the caller's.`,
	}
	cmd.AddCommand(githubReviewCmd(), githubStatusCmd())
	return cmd
}

// githubItem resolves the item a publish is for: the --item flag when set,
// else the gate's SATELLE_ITEM. Only the id's shape is checked; satelle.dev
// resolves the item's repository.
func githubItem(flag string) (string, error) {
	id := strings.TrimSpace(flag)
	if id == "" {
		id = strings.TrimSpace(os.Getenv(gate.ItemEnv))
	}
	if id == "" {
		return "", errors.New("no item to publish for: pass --item ID or set " + gate.ItemEnv)
	}
	if !ledger.ValidItem(id) {
		return "", fmt.Errorf("invalid item id %q", id)
	}
	return id, nil
}

func githubItemFlag(cmd *cobra.Command, v *string) {
	cmd.Flags().StringVar(v, "item", "", "the item published for (default $"+gate.ItemEnv+"); satelle.dev resolves the repository from it")
}

func githubStatusCmd() *cobra.Command {
	var sha, context, state, description, targetURL, itemID string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "status --sha S --context C --state S [--description D] [--target-url U] [--item ID]",
		Short: "Publish a commit status as the satelle GitHub app (GitHubService.PublishStatus)",
		Long: `Publishes a commit status on commit S of the repository satelle.dev resolves
for the item among the project's bound repositories, as the satelle GitHub
app. The item comes from --item, else from SATELLE_ITEM. The context, the state (success, failure, error or
pending on GitHub), the description and the target url are passed through
unchanged; satelle.dev answers with the status's id and the repository. A
project with no GitHub binding is refused by the server (FAILED_PRECONDITION),
and a dry run (satelle gate run) publishes nothing.`,
		Args: cobra.NoArgs,
		RunE: openBound(func(cmd *cobra.Command, k *kernel.Kernel, s bound, _ []string) error {
			for _, f := range []struct{ name, v string }{{"sha", sha}, {"context", context}, {"state", state}} {
				if strings.TrimSpace(f.v) == "" {
					return fmt.Errorf("--%s is required", f.name)
				}
			}
			item, err := githubItem(itemID)
			if err != nil {
				return err
			}
			if kernel.UnderDryRun(k.Ledger.Dir()) {
				return errors.New("a dry run publishes no status")
			}
			r, err := s.Client.GitHub.PublishStatus(cmd.Context(), &satellepb.PublishStatusRequest{
				Project: s.Project, ItemId: item, CommitSha: sha, Context: context, State: state, Description: description, TargetUrl: targetURL,
			})
			if err != nil {
				return hosted.Describe("github status", err)
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"status_id": r.GetStatusId(), "repository": r.GetRepository()})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "published status %d on %s@%s: %s %s\n", r.GetStatusId(), r.GetRepository(), sha, context, state)
			return nil
		}),
	}
	cmd.Flags().StringVar(&sha, "sha", "", "the commit the status is on")
	cmd.Flags().StringVar(&context, "context", "", "the status context, passed through")
	cmd.Flags().StringVar(&state, "state", "", "the status state, passed through: success, failure, error or pending")
	cmd.Flags().StringVar(&description, "description", "", "a short description, passed through")
	cmd.Flags().StringVar(&targetURL, "target-url", "", "a url the status links to, passed through")
	githubItemFlag(cmd, &itemID)
	asJSON = jsonFlag(cmd)
	return cmd
}

func githubReviewCmd() *cobra.Command {
	var pr int64
	var sha, event, bodyFile, itemID string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "review --pr N --sha S --event E [--body-file F|-] [--item ID]",
		Short: "Publish a pull-request review as the satelle GitHub app (GitHubService.PublishReview)",
		Long: `Publishes a review on pull request N of the repository satelle.dev resolves
for the item among the project's bound repositories, on commit S, as the
satelle GitHub app. The item comes from --item, else from SATELLE_ITEM. The event (APPROVE, REQUEST_CHANGES,
COMMENT) and the body are passed through unchanged; satelle.dev answers with
the review's id and url. A project with no GitHub binding is refused by the
server (FAILED_PRECONDITION).`,
		Args: cobra.NoArgs,
		RunE: openBound(func(cmd *cobra.Command, _ *kernel.Kernel, s bound, _ []string) error {
			if pr <= 0 {
				return errors.New("--pr is required")
			}
			if strings.TrimSpace(sha) == "" {
				return errors.New("--sha is required")
			}
			if strings.TrimSpace(event) == "" {
				return errors.New("--event is required")
			}
			item, err := githubItem(itemID)
			if err != nil {
				return err
			}
			var body []byte
			switch bodyFile {
			case "":
			case "-":
				body, err = io.ReadAll(cmd.InOrStdin())
			default:
				body, err = os.ReadFile(bodyFile)
			}
			if err != nil {
				return err
			}
			r, err := s.Client.GitHub.PublishReview(cmd.Context(), &satellepb.PublishReviewRequest{
				Project: s.Project, ItemId: item, PullNumber: pr, CommitSha: sha, Event: event, Body: string(body),
			})
			if err != nil {
				return hosted.Describe("github review", err)
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"review_id": r.GetReviewId(), "html_url": r.GetHtmlUrl(), "repository": r.GetRepository()})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "published review %d on %s#%d: %s\n", r.GetReviewId(), r.GetRepository(), pr, r.GetHtmlUrl())
			return nil
		}),
	}
	cmd.Flags().Int64Var(&pr, "pr", 0, "pull request number")
	cmd.Flags().StringVar(&sha, "sha", "", "the commit the review is on (the PR head)")
	cmd.Flags().StringVar(&event, "event", "", "review event, passed through: APPROVE, REQUEST_CHANGES or COMMENT")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "file holding the review body; '-' reads stdin")
	githubItemFlag(cmd, &itemID)
	asJSON = jsonFlag(cmd)
	return cmd
}
