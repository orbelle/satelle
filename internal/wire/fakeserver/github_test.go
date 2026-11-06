package fakeserver_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// GitHubService through the hosted client: a project with no binding is a
// failed precondition, another's project is not found, and a bound project
// publishes reviews and statuses (recorded unchanged) and pages its
// pull-request events.
func TestGitHubService(t *testing.T) {
	ctx := context.Background()
	srv := fakeserver.Start(t, "tok")
	c, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: "tok"}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	p, err := c.Projects.CreateProject(ctx, &satellepb.CreateProjectRequest{Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.GitHub.PublishReview(ctx, &satellepb.PublishReviewRequest{Project: p.GetId(), PullNumber: 1})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unbound project: %v", err)
	}
	_, err = c.GitHub.PublishStatus(ctx, &satellepb.PublishStatusRequest{Project: "prj_other"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("another's project: %v", err)
	}

	srv.BindGitHub("acme", "orbelle/acme")
	rev, err := c.GitHub.PublishReview(ctx, &satellepb.PublishReviewRequest{
		Project: "acme", PullNumber: 7, CommitSha: "abc123", Event: "REQUEST_CHANGES", Body: "team acme, gate code-ac: criterion 2 is not met",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rev.GetReviewId() != 1 || rev.GetRepository() != "orbelle/acme" || rev.GetHtmlUrl() != "https://github.com/orbelle/acme/pull/7#pullrequestreview-1" {
		t.Errorf("review response %+v", rev)
	}
	st, err := c.GitHub.PublishStatus(ctx, &satellepb.PublishStatusRequest{
		Project: "acme", CommitSha: "abc123", State: "success", Context: "satelle/code-ac", Description: "accept", TargetUrl: "https://example.com",
	})
	if err != nil || st.GetStatusId() != 1 || st.GetRepository() != "orbelle/acme" {
		t.Fatalf("status %+v %v", st, err)
	}
	if got := srv.PublishedReviews("acme"); len(got) != 1 || got[0].GetEvent() != "REQUEST_CHANGES" || got[0].GetBody() != "team acme, gate code-ac: criterion 2 is not met" {
		t.Errorf("recorded reviews %+v", got)
	}
	if got := srv.PublishedStatuses("acme"); len(got) != 1 || got[0].GetContext() != "satelle/code-ac" || got[0].GetState() != "success" {
		t.Errorf("recorded statuses %+v", got)
	}

	for _, n := range []int64{7, 8, 9} {
		srv.SeedPullRequestEvent("acme", &satellepb.PullRequestEvent{DeliveryId: "d", Action: "opened", PullNumber: n, HeadSha: "h", HeadRef: "sto_1", BaseRef: "main"})
	}
	page, err := c.GitHub.ListPullRequestEvents(ctx, &satellepb.ListPullRequestEventsRequest{Project: "acme", PageSize: 2})
	if err != nil || len(page.GetEvents()) != 2 || page.GetEvents()[0].GetPullNumber() != 7 || page.GetEvents()[0].GetRepository() != "orbelle/acme" {
		t.Fatalf("first page %+v %v", page, err)
	}
	rest, err := c.GitHub.ListPullRequestEvents(ctx, &satellepb.ListPullRequestEventsRequest{Project: "acme", SinceToken: page.GetNextToken()})
	if err != nil || len(rest.GetEvents()) != 1 || rest.GetEvents()[0].GetPullNumber() != 9 {
		t.Fatalf("second page %+v %v", rest, err)
	}
	none, err := c.GitHub.ListPullRequestEvents(ctx, &satellepb.ListPullRequestEventsRequest{Project: "acme", SinceToken: rest.GetNextToken()})
	if err != nil || len(none.GetEvents()) != 0 || none.GetNextToken() != rest.GetNextToken() {
		t.Fatalf("nothing new %+v %v", none, err)
	}
}
