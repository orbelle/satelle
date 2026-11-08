package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

func githubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Publish to the bound project's GitHub repository through satelle.dev",
		Long: `The satelle GitHub app, installed on a repository, binds it to a satelle.dev
project. These commands ask satelle.dev to act on that repository as the app;
the repository comes from the binding, never from the command line. satelle.dev
decides nothing: what to publish (the event, the body) is the caller's.`,
	}
	cmd.AddCommand(githubReviewCmd())
	return cmd
}

func githubReviewCmd() *cobra.Command {
	var pr int64
	var sha, event, bodyFile string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "review --pr N --sha S --event E [--body-file F|-]",
		Short: "Publish a pull-request review as the satelle GitHub app (GitHubService.PublishReview)",
		Long: `Publishes a review on pull request N of the bound project's repository, on
commit S, as the satelle GitHub app. The event (APPROVE, REQUEST_CHANGES,
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
			var body []byte
			var err error
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
				Project: s.Project, PullNumber: pr, CommitSha: sha, Event: event, Body: string(body),
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
	asJSON = jsonFlag(cmd)
	return cmd
}
