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
	t.Setenv("SATELLE_ITEM", "")

	_, err := run(t, "", "github", "review", "--pr", "7", "--sha", "abc123", "--event", "APPROVE", "--item", "sto_aaaa1111")
	if err == nil || !strings.Contains(err.Error(), "FailedPrecondition") || !strings.Contains(err.Error(), "no GitHub binding") {
		t.Fatalf("unbound project: %v", err)
	}

	srv.BindGitHub("acme", "orbelle/acme")
	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte("satelle · gate pr-review: approve\n\nfine"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := must(t, "github", "review", "--pr", "7", "--sha", "abc123", "--event", "APPROVE", "--body-file", body, "--item", "sto_aaaa1111")
	if want := "published review 1 on orbelle/acme#7: https://github.com/orbelle/acme/pull/7#pullrequestreview-1"; strings.TrimSpace(out) != want {
		t.Fatalf("review = %q, want %q", out, want)
	}
	out, err = run(t, "satelle · gate pr-review: request changes\n\nbad", "github", "review", "--pr", "7", "--sha", "def456", "--event", "SOMETHING_OPAQUE", "--body-file", "-", "--json", "--item", "sto_aaaa1111")
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
		{Project: project, ItemId: "sto_aaaa1111", PullNumber: 7, CommitSha: "abc123", Event: "APPROVE", Body: "satelle · gate pr-review: approve\n\nfine"},
		{Project: project, ItemId: "sto_aaaa1111", PullNumber: 7, CommitSha: "def456", Event: "SOMETHING_OPAQUE", Body: "satelle · gate pr-review: request changes\n\nbad"},
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
	refuseItem(t, []string{"github", "review", "--pr", "7", "--sha", "a", "--event", "APPROVE"})
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
	t.Setenv("SATELLE_ITEM", "")

	_, err := run(t, "", "github", "status", "--sha", "abc123", "--context", "ci/example", "--state", "success", "--item", "sto_aaaa1111")
	if err == nil || !strings.Contains(err.Error(), "FailedPrecondition") || !strings.Contains(err.Error(), "no GitHub binding") || !strings.Contains(err.Error(), "install the satelle GitHub app") {
		t.Fatalf("unbound project: %v", err)
	}

	srv.BindGitHub("acme", "orbelle/acme")
	out := must(t, "github", "status", "--sha", "abc123", "--context", "ci/example", "--state", "success", "--description", "fine", "--target-url", "https://example.test/1", "--item", "sto_aaaa1111")
	if want := "published status 1 on orbelle/acme@abc123: ci/example success"; strings.TrimSpace(out) != want {
		t.Fatalf("status = %q, want %q", out, want)
	}
	long := strings.Repeat("d", 300)
	out, err = run(t, "", "github", "status", "--sha", "def456", "--context", "ci/example", "--state", "SOMETHING_OPAQUE", "--description", long, "--json", "--item", "sto_aaaa1111")
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
		{Project: project, ItemId: "sto_aaaa1111", CommitSha: "abc123", Context: "ci/example", State: "success", Description: "fine", TargetUrl: "https://example.test/1"},
		{Project: project, ItemId: "sto_aaaa1111", CommitSha: "def456", Context: "ci/example", State: "SOMETHING_OPAQUE", Description: long},
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
	refuseItem(t, []string{"github", "status", "--sha", "a", "--context", "c", "--state", "success"})
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

func refuseItem(t *testing.T, args []string) {
	t.Helper()
	if _, err := run(t, "", args...); err == nil || !strings.Contains(err.Error(), "--item") || !strings.Contains(err.Error(), "SATELLE_ITEM") {
		t.Errorf("%v with no item: %v", args, err)
	}
	if _, err := run(t, "", append(args, "--item", "bad id")...); err == nil || !strings.Contains(err.Error(), "invalid item id") {
		t.Errorf("%v with a malformed item: %v", args, err)
	}
}

func TestGitHubItemFromEnvironment(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "acme", "--bind", "--server", srv.URL)
	srv.BindGitHub("acme", "orbelle/acme")
	t.Setenv("SATELLE_ITEM", "sto_bbbb2222")

	review := []string{"github", "review", "--pr", "7", "--sha", "abc123", "--event", "APPROVE"}
	status := []string{"github", "status", "--sha", "abc123", "--context", "ci/example", "--state", "success"}
	must(t, review...)
	must(t, status...)
	must(t, append(review, "--item", "sto_aaaa1111")...)
	must(t, append(status, "--item", "sto_aaaa1111")...)

	reviews, statuses := srv.PublishedReviews("acme"), srv.PublishedStatuses("acme")
	if len(reviews) != 2 || len(statuses) != 2 {
		t.Fatalf("published %d reviews, %d statuses", len(reviews), len(statuses))
	}
	for i, want := range []string{"sto_bbbb2222", "sto_aaaa1111"} {
		if got := reviews[i].GetItemId(); got != want {
			t.Errorf("review %d item_id = %q, want %q", i, got, want)
		}
		if got := statuses[i].GetItemId(); got != want {
			t.Errorf("status %d item_id = %q, want %q", i, got, want)
		}
	}
}

func TestGitHubCommandsTakeNoRepository(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "acme", "--bind", "--server", srv.URL)
	srv.BindGitHub("acme", "orbelle/acme")
	t.Setenv("SATELLE_ITEM", "sto_aaaa1111")

	for _, cmd := range [][]string{
		{"github", "review", "--pr", "7", "--sha", "abc123", "--event", "APPROVE"},
		{"github", "status", "--sha", "abc123", "--context", "ci/example", "--state", "success"},
	} {
		for _, flag := range []string{"--repo", "--repository"} {
			if _, err := run(t, "", append(cmd, flag, "other/repo")...); err == nil || !strings.Contains(err.Error(), "unknown flag") {
				t.Errorf("%v %s: %v", cmd, flag, err)
			}
		}
	}
	if n, m := len(srv.PublishedReviews("acme")), len(srv.PublishedStatuses("acme")); n != 0 || m != 0 {
		t.Fatalf("a refused command published: %d reviews, %d statuses", n, m)
	}
}
