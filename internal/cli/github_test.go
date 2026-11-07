package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// github review publishes exactly {project, pull_number, commit_sha, event,
// body} over GitHubService.PublishReview: the event is passed through opaque,
// the body comes from a file or stdin, and no item id or title is sent.
func TestGitHubReviewPublishesThroughTheServer(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "acme", "--bind", "--server", srv.URL)

	// No GitHub binding: the server's FAILED_PRECONDITION surfaces.
	_, err := run(t, "", "github", "review", "--pr", "7", "--sha", "abc123", "--event", "APPROVE")
	if err == nil || !strings.Contains(err.Error(), "FailedPrecondition") || !strings.Contains(err.Error(), "no GitHub binding") {
		t.Fatalf("unbound project: %v", err)
	}

	srv.BindGitHub("acme", "orbelle/acme")
	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte("satelle · gate pr-review: approve\n\nfine"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := must(t, "github", "review", "--pr", "7", "--sha", "abc123", "--event", "APPROVE", "--body-file", body)
	if want := "published review 1 on orbelle/acme#7: https://github.com/orbelle/acme/pull/7#pullrequestreview-1"; strings.TrimSpace(out) != want {
		t.Fatalf("review = %q, want %q", out, want)
	}
	out, err = run(t, "satelle · gate pr-review: request changes\n\nbad", "github", "review", "--pr", "7", "--sha", "def456", "--event", "SOMETHING_OPAQUE", "--body-file", "-", "--json")
	if err != nil {
		t.Fatalf("stdin review: %v\n%s", err, out)
	}
	var got struct {
		ReviewID   int64  `json:"review_id"`
		HTMLURL    string `json:"html_url"`
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got.ReviewID != 2 || got.Repository != "orbelle/acme" || got.HTMLURL != "https://github.com/orbelle/acme/pull/7#pullrequestreview-2" {
		t.Fatalf("json = %+v", got)
	}

	reviews := srv.PublishedReviews("acme")
	if len(reviews) != 2 {
		t.Fatalf("published %d reviews", len(reviews))
	}
	project := reviews[0].GetProject()
	if project == "" {
		t.Fatal("no project on the request")
	}
	want := []*satellepb.PublishReviewRequest{
		{Project: project, PullNumber: 7, CommitSha: "abc123", Event: "APPROVE", Body: "satelle · gate pr-review: approve\n\nfine"},
		{Project: project, PullNumber: 7, CommitSha: "def456", Event: "SOMETHING_OPAQUE", Body: "satelle · gate pr-review: request changes\n\nbad"},
	}
	for i := range want {
		if !proto.Equal(reviews[i], want[i]) {
			t.Errorf("review %d = %v, want %v", i, reviews[i], want[i])
		}
	}

	for _, args := range [][]string{
		{"--sha", "a", "--event", "APPROVE"},
		{"--pr", "7", "--event", "APPROVE"},
		{"--pr", "7", "--sha", "a"},
	} {
		if _, err := run(t, "", append([]string{"github", "review"}, args...)...); err == nil || !strings.Contains(err.Error(), "is required") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if n := len(srv.PublishedReviews("acme")); n != 2 {
		t.Fatalf("a refused command published: %d", n)
	}
}

func TestGitHubReviewNeedsABoundRepo(t *testing.T) {
	newMachine(t)
	_, err := run(t, "", "github", "review", "--pr", "7", "--sha", "abc", "--event", "APPROVE")
	if !errors.Is(err, errNotBound) {
		t.Fatalf("unbound repo: %v", err)
	}
}
