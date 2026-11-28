package fakeserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

type github struct {
	repo     string
	reviews  []*satellepb.PublishReviewRequest
	statuses []*satellepb.PublishStatusRequest
	events   []*satellepb.PullRequestEvent
	refuse   string
}

func (s *Server) RefuseStatuses(project, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.project(project)
	if p == nil || s.gh[p.Id] == nil {
		panic("fakeserver: RefuseStatuses on a project with no GitHub binding: " + project)
	}
	s.gh[p.Id].refuse = message
}

func (s *Server) BindGitHub(project, repo string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.project(project)
	if p == nil {
		panic("fakeserver: BindGitHub on unknown project " + project)
	}
	if s.gh == nil {
		s.gh = map[string]*github{}
	}
	s.gh[p.Id] = &github{repo: repo}
}

func (s *Server) SeedPullRequestEvent(project string, ev *satellepb.PullRequestEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.gh[s.project(project).GetId()]
	if g == nil {
		panic("fakeserver: SeedPullRequestEvent on a project with no GitHub binding")
	}
	e := proto.Clone(ev).(*satellepb.PullRequestEvent)
	if e.Repository == "" {
		e.Repository = g.repo
	}
	g.events = append(g.events, e)
}

func (s *Server) PublishedReviews(project string) []*satellepb.PublishReviewRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g := s.gh[s.project(project).GetId()]; g != nil {
		return append([]*satellepb.PublishReviewRequest(nil), g.reviews...)
	}
	return nil
}

func (s *Server) PublishedStatuses(project string) []*satellepb.PublishStatusRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g := s.gh[s.project(project).GetId()]; g != nil {
		return append([]*satellepb.PublishStatusRequest(nil), g.statuses...)
	}
	return nil
}

var errItemRequired = status.Error(codes.InvalidArgument, "item_id is required: the server resolves the repository from the item")

type githubSvc struct {
	satellepb.UnimplementedGitHubServiceServer
	s *Server
}

func (s *Server) bound(project string) (*github, error) {
	p, err := s.projectOrNotFound(project)
	if err != nil {
		return nil, err
	}
	g := s.gh[p.Id]
	if g == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "project %s has no GitHub binding: install the satelle GitHub app on its repository", p.Slug)
	}
	return g, nil
}

func (h githubSvc) PublishReview(_ context.Context, r *satellepb.PublishReviewRequest) (*satellepb.PublishReviewResponse, error) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if strings.TrimSpace(r.GetItemId()) == "" {
		return nil, errItemRequired
	}
	g, err := h.s.bound(r.GetProject())
	if err != nil {
		return nil, err
	}
	g.reviews = append(g.reviews, proto.Clone(r).(*satellepb.PublishReviewRequest))
	id := int64(len(g.reviews))
	return &satellepb.PublishReviewResponse{
		ReviewId:   id,
		HtmlUrl:    fmt.Sprintf("https://github.com/%s/pull/%d#pullrequestreview-%d", g.repo, r.GetPullNumber(), id),
		Repository: g.repo,
	}, nil
}

func (h githubSvc) PublishStatus(_ context.Context, r *satellepb.PublishStatusRequest) (*satellepb.PublishStatusResponse, error) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if strings.TrimSpace(r.GetItemId()) == "" {
		return nil, errItemRequired
	}
	g, err := h.s.bound(r.GetProject())
	if err != nil {
		return nil, err
	}
	if g.refuse != "" {
		return nil, status.Error(codes.FailedPrecondition, g.refuse)
	}
	g.statuses = append(g.statuses, proto.Clone(r).(*satellepb.PublishStatusRequest))
	return &satellepb.PublishStatusResponse{StatusId: int64(len(g.statuses)), Repository: g.repo}, nil
}

func (h githubSvc) ListPullRequestEvents(_ context.Context, r *satellepb.ListPullRequestEventsRequest) (*satellepb.ListPullRequestEventsResponse, error) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	g, err := h.s.bound(r.GetProject())
	if err != nil {
		return nil, err
	}
	from := 0
	if t := r.GetSinceToken(); t != "" {
		if from, err = strconv.Atoi(t); err != nil || from < 0 || from > len(g.events) {
			return nil, status.Errorf(codes.InvalidArgument, "bad since_token %q", t)
		}
	}
	size := int(r.GetPageSize())
	if size <= 0 {
		size = 100
	}
	to := min(from+size, len(g.events))
	out := &satellepb.ListPullRequestEventsResponse{NextToken: strconv.Itoa(to)}
	for _, e := range g.events[from:to] {
		out.Events = append(out.Events, proto.Clone(e).(*satellepb.PullRequestEvent))
	}
	return out, nil
}
