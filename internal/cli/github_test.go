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

func TestGitHubReviewPublishesThroughTheServer(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "acme", "--bind", "--server", srv.URL)

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

func TestGitHubStatusPublishesThroughTheServer(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "acme", "--bind", "--server", srv.URL)

	_, err := run(t, "", "github", "status", "--sha", "abc123", "--context", "ci/example", "--state", "success")
	if err == nil || !strings.Contains(err.Error(), "FailedPrecondition") || !strings.Contains(err.Error(), "no GitHub binding") || !strings.Contains(err.Error(), "install the satelle GitHub app") {
		t.Fatalf("unbound project: %v", err)
	}

	srv.BindGitHub("acme", "orbelle/acme")
	out := must(t, "github", "status", "--sha", "abc123", "--context", "ci/example", "--state", "success", "--description", "fine", "--target-url", "https://example.test/1")
	if want := "published status 1 on orbelle/acme@abc123: ci/example success"; strings.TrimSpace(out) != want {
		t.Fatalf("status = %q, want %q", out, want)
	}
	long := strings.Repeat("d", 300)
	out, err = run(t, "", "github", "status", "--sha", "def456", "--context", "ci/example", "--state", "SOMETHING_OPAQUE", "--description", long, "--json")
	if err != nil {
		t.Fatalf("json status: %v\n%s", err, out)
	}
	var got struct {
		StatusID   int64  `json:"status_id"`
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.StatusID != 2 || got.Repository != "orbelle/acme" {
		t.Fatalf("json = %+v (%v): %s", got, err, out)
	}
	statuses := srv.PublishedStatuses("acme")
	if len(statuses) != 2 {
		t.Fatalf("published %d statuses", len(statuses))
	}
	project := statuses[0].GetProject()
	want := []*satellepb.PublishStatusRequest{
		{Project: project, CommitSha: "abc123", Context: "ci/example", State: "success", Description: "fine", TargetUrl: "https://example.test/1"},
		{Project: project, CommitSha: "def456", Context: "ci/example", State: "SOMETHING_OPAQUE", Description: long},
	}
	for i := range want {
		if !proto.Equal(statuses[i], want[i]) {
			t.Errorf("status %d = %v, want %v", i, statuses[i], want[i])
		}
	}
	for _, args := range [][]string{
		{"--context", "c", "--state", "success"},
		{"--sha", "a", "--state", "success"},
		{"--sha", "a", "--context", "c"},
	} {
		if _, err := run(t, "", append([]string{"github", "status"}, args...)...); err == nil || !strings.Contains(err.Error(), "is required") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if n := len(srv.PublishedStatuses("acme")); n != 2 {
		t.Fatalf("a refused command published: %d", n)
	}
}

func TestGitHubStatusNeedsABoundRepo(t *testing.T) {
	newMachine(t)
	if _, err := run(t, "", "github", "status", "--sha", "a", "--context", "c", "--state", "success"); !errors.Is(err, errNotBound) {
		t.Fatalf("unbound repo: %v", err)
	}
}
