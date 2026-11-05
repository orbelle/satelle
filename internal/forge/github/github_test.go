package github_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/forge"
	"github.com/bobmcallan/satelle/internal/forge/github"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
)

const (
	repo  = "orbelle/satelle"
	token = "ghs_minted-for-the-run"
	head  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// open is the provider through the seam, as a node opens it: the fake's API, the
// minted token.
func open(t *testing.T, gh *devtest.GitHub) forge.Forge {
	t.Helper()
	f, err := forge.Open(github.Name, forge.Config{Host: "github.com", APIURL: gh.URL, Token: token, HTTP: gh.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func fakeWithPR(t *testing.T) *devtest.GitHub {
	t.Helper()
	gh := devtest.NewGitHub(t)
	gh.Mint(token, repo)
	gh.AddPR(devtest.PR{Repo: repo, Number: 7, HeadRef: "epic/sty_x", HeadSHA: head, Body: "b", Diff: "diff --git a/x b/x\n"})
	return gh
}

// AC1: the provider finds the PR by its epic head, approves on the exact head
// sha with the minted token, and posts one status there.
func TestGitHubApprovesOnTheExactHeadWithTheMintedToken(t *testing.T) {
	gh := fakeWithPR(t)
	f := open(t, gh)
	ctx := context.Background()

	pr, err := f.PullRequestForHead(ctx, repo, forge.EpicHead("sty_x"))
	if err != nil || pr.Number != 7 || pr.HeadSHA != head {
		t.Fatalf("PullRequestForHead = %+v, %v", pr, err)
	}
	if d, err := f.Diff(ctx, repo, 7); err != nil || d != "diff --git a/x b/x\n" {
		t.Errorf("Diff = %q, %v", d, err)
	}
	rv, err := f.Approve(ctx, repo, 7, pr.HeadSHA, "approved")
	if err != nil || rv.ID == 0 || rv.Author != gh.ReviewerLogin || rv.CommitID != head {
		t.Fatalf("Approve = %+v, %v", rv, err)
	}
	if err := f.SetStatus(ctx, repo, head, forge.Status{Context: "satelle/pr-approval", State: forge.StatusSuccess}); err != nil {
		t.Fatal(err)
	}
	reviews, statuses := gh.Reviews(), gh.Statuses()
	if len(reviews) != 1 || reviews[0].CommitID != head || reviews[0].Event != "APPROVE" || reviews[0].Token != token {
		t.Errorf("reviews = %+v, want one APPROVE on %s with the minted token", reviews, head)
	}
	if len(statuses) != 1 || statuses[0].SHA != head || statuses[0].Context != "satelle/pr-approval" || statuses[0].Token != token {
		t.Errorf("statuses = %+v, want one on %s", statuses, head)
	}
}

// AC3: Approve refuses a head that moved from the reviewed sha and posts nothing.
func TestGitHubRefusesToApproveAMovedHead(t *testing.T) {
	gh := fakeWithPR(t)
	f := open(t, gh)
	gh.SetHead(repo, 7, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	_, err := f.Approve(context.Background(), repo, 7, head, "approved")
	var moved *forge.HeadMovedError
	if !errors.As(err, &moved) || moved.Reviewed != head {
		t.Fatalf("Approve on a moved head = %v, want a HeadMovedError", err)
	}
	if gh.Posts() != 0 {
		t.Errorf("posts = %d, want none", gh.Posts())
	}
}

// GitHub's 422 refusing the PR's own author is ErrSelfApproval and records
// nothing; Comment then records a COMMENT review on the exact head.
func TestGitHubSelfApprovalIsErrSelfApprovalAndCommentStillPosts(t *testing.T) {
	gh := devtest.NewGitHub(t)
	gh.Mint(token, repo)
	gh.AddPR(devtest.PR{Repo: repo, Number: 7, HeadRef: "feat/sty_x", HeadSHA: head, Author: gh.ReviewerLogin})
	f := open(t, gh)
	ctx := context.Background()

	_, err := f.Approve(ctx, repo, 7, head, "approved")
	if !errors.Is(err, forge.ErrSelfApproval) || !strings.Contains(err.Error(), "Can not approve your own pull request") {
		t.Fatalf("Approve on an own PR = %v, want ErrSelfApproval carrying GitHub's text", err)
	}
	if gh.Posts() != 0 {
		t.Fatalf("posts after the refused approval = %d, want none", gh.Posts())
	}
	rv, err := f.Comment(ctx, repo, 7, head, "approved by status")
	if err != nil || rv.CommitID != head {
		t.Fatalf("Comment = %+v, %v", rv, err)
	}
	if r := gh.Reviews(); len(r) != 1 || r[0].Event != "COMMENT" || r[0].CommitID != head || r[0].Body != "approved by status" {
		t.Errorf("reviews = %+v, want one COMMENT on %s", r, head)
	}
	gh.SetHead(repo, 7, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	var moved *forge.HeadMovedError
	if _, err := f.Comment(ctx, repo, 7, head, "x"); !errors.As(err, &moved) || len(gh.Reviews()) != 1 {
		t.Errorf("Comment on a moved head = %v (reviews %d), want a HeadMovedError and no post", err, len(gh.Reviews()))
	}
}

// A 422 for any other reason is not ErrSelfApproval; GitHub's object-form
// errors entries reach the message.
func TestGitHubOtherUnprocessableIsNotSelfApproval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"number":7,"head":{"ref":"epic/sty_x","sha":"` + head + `"}}`))
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"Pull request review thread is outdated"}]}`))
	}))
	t.Cleanup(srv.Close)
	f, err := forge.Open(github.Name, forge.Config{APIURL: srv.URL, Token: token, HTTP: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Approve(context.Background(), repo, 7, head, "approved")
	var api *github.APIError
	if errors.Is(err, forge.ErrSelfApproval) || !errors.As(err, &api) || api.Status != 422 ||
		!strings.Contains(api.Message, "Pull request review thread is outdated") {
		t.Fatalf("Approve on another 422 = %v, want a plain 422 APIError carrying its errors entry", err)
	}
}

// No open PR on the head is ErrNoPR; a token not minted for the repo is the
// API's 404, never a silent empty answer.
func TestGitHubPRLookupRefusals(t *testing.T) {
	gh := fakeWithPR(t)
	f := open(t, gh)
	if _, err := f.PullRequestForHead(context.Background(), repo, "epic/sty_none"); !errors.Is(err, forge.ErrNoPR) {
		t.Errorf("no PR: %v, want ErrNoPR", err)
	}
	var api *github.APIError
	if _, err := f.PullRequestForHead(context.Background(), "acme/other", "epic/sty_x"); !errors.As(err, &api) || api.Status != 404 {
		t.Errorf("unscoped repo: %v, want a 404 APIError", err)
	}
}

// A provider name no provider registered is ErrNoProvider, which the caller
// turns into a warning and no posts.
func TestOpenUnknownProviderIsNoProvider(t *testing.T) {
	for _, name := range []string{"none", "", "gitea"} {
		if _, err := forge.Open(name, forge.Config{Token: token}); !errors.Is(err, forge.ErrNoProvider) {
			t.Errorf("Open(%q) = %v, want ErrNoProvider", name, err)
		}
	}
	if got := github.APIURL("github.com"); got != "https://api.github.com" {
		t.Errorf("APIURL(github.com) = %s", got)
	}
	if got := github.APIURL("ghe.example.com"); got != "https://ghe.example.com/api/v3" {
		t.Errorf("APIURL(ghe) = %s", got)
	}
}
