package fakeserver_test

import (
	"context"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/reporeg"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

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

	_, err = c.GitHub.PublishReview(ctx, &satellepb.PublishReviewRequest{Project: p.GetId(), ItemId: "sto_1", PullNumber: 1})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unbound project: %v", err)
	}
	_, err = c.GitHub.PublishStatus(ctx, &satellepb.PublishStatusRequest{Project: "prj_other", ItemId: "sto_1"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("another's project: %v", err)
	}

	srv.BindGitHub("acme", "orbelle/acme")
	if _, err := c.GitHub.PublishReview(ctx, &satellepb.PublishReviewRequest{Project: "acme", PullNumber: 7, CommitSha: "abc123", Event: "APPROVE"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("review with no item_id: %v", err)
	}
	if _, err := c.GitHub.PublishStatus(ctx, &satellepb.PublishStatusRequest{Project: "acme", ItemId: " ", CommitSha: "abc123", State: "success", Context: "ci/x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status with no item_id: %v", err)
	}
	if len(srv.PublishedReviews("acme")) != 0 || len(srv.PublishedStatuses("acme")) != 0 {
		t.Fatal("a request with no item_id was recorded")
	}
	rev, err := c.GitHub.PublishReview(ctx, &satellepb.PublishReviewRequest{
		Project: "acme", ItemId: "sto_1", PullNumber: 7, CommitSha: "abc123", Event: "REQUEST_CHANGES", Body: "team acme, gate code-ac: criterion 2 is not met",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rev.GetReviewId() != 1 || rev.GetRepository() != "orbelle/acme" || rev.GetHtmlUrl() != "https://github.com/orbelle/acme/pull/7#pullrequestreview-1" {
		t.Errorf("review response %+v", rev)
	}
	st, err := c.GitHub.PublishStatus(ctx, &satellepb.PublishStatusRequest{
		Project: "acme", ItemId: "sto_1", CommitSha: "abc123", State: "success", Context: "satelle/code-ac", Description: "accept", TargetUrl: "https://example.com",
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

	srv.RefuseStatuses("acme", "the app's installation lacks statuses: write")
	_, err = c.GitHub.PublishStatus(ctx, &satellepb.PublishStatusRequest{Project: "acme", ItemId: "sto_1", CommitSha: "abc123", State: "success", Context: "ci/x"})
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "the app's installation lacks statuses: write" {
		t.Fatalf("refused status: %v", err)
	}
	if got := srv.PublishedStatuses("acme"); len(got) != 1 {
		t.Errorf("a refused status was recorded: %+v", got)
	}
	srv.RefuseStatuses("acme", "")
	if _, err := c.GitHub.PublishStatus(ctx, &satellepb.PublishStatusRequest{Project: "acme", ItemId: "sto_1", CommitSha: "abc123", State: "success", Context: "ci/x"}); err != nil {
		t.Fatalf("lifted refusal: %v", err)
	}
	if got := srv.PublishedStatuses("acme"); len(got) != 2 {
		t.Errorf("statuses after the refusal lifted: %+v", got)
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

func TestProjectRepositories(t *testing.T) {
	ctx := context.Background()
	srv := fakeserver.Start(t, "tok")
	c, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: "tok"}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	for _, slug := range []string{"acme", "solo", "bare"} {
		if _, err := c.Projects.CreateProject(ctx, &satellepb.CreateProjectRequest{Slug: slug}); err != nil {
			t.Fatal(err)
		}
	}
	srv.BindGitHubVia("acme", "orbelle/acme", 11)
	srv.BindGitHubVia("acme", "orbelle/acme-docs", 12)
	srv.BindGitHubVia("acme", "orbelle/acme", 11)
	srv.BindGitHub("solo", "orbelle/solo")

	type binding struct {
		repo string
		inst int64
	}
	flat := func(p *satellepb.Project) []binding {
		var out []binding
		for _, b := range p.GetRepositories() {
			out = append(out, binding{b.GetRepository(), b.GetInstallationId()})
		}
		return out
	}
	wantAcme := []binding{{"orbelle/acme", 11}, {"orbelle/acme-docs", 12}}

	got, err := c.Projects.GetProject(ctx, &satellepb.GetProjectRequest{Project: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(flat(got), wantAcme) {
		t.Errorf("GetProject acme repositories = %v, want %v", flat(got), wantAcme)
	}

	list, err := c.Projects.ListProjects(ctx, &satellepb.ListProjectsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]binding{"acme": wantAcme, "solo": {{"orbelle/solo", 1}}, "bare": nil}
	if len(list.GetProjects()) != len(want) {
		t.Fatalf("ListProjects = %d projects, want %d", len(list.GetProjects()), len(want))
	}
	for _, p := range list.GetProjects() {
		if !slices.Equal(flat(p), want[p.GetSlug()]) {
			t.Errorf("ListProjects %s repositories = %v, want %v", p.GetSlug(), flat(p), want[p.GetSlug()])
		}
	}

	rev, err := c.GitHub.PublishReview(ctx, &satellepb.PublishReviewRequest{Project: "acme", ItemId: "sto_1", PullNumber: 1, CommitSha: "abc", Event: "APPROVE"})
	if err != nil || rev.GetRepository() != "orbelle/acme" {
		t.Errorf("publish target = %+v %v, want the first binding orbelle/acme", rev, err)
	}

	if n, err := reporeg.NormaliseOrigin("https://github.com/" + got.GetRepositories()[1].GetRepository()); err != nil || n != "github.com/orbelle/acme-docs" {
		t.Errorf("NormaliseOrigin of the second binding = %q %v", n, err)
	}
}
