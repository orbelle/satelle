// Package fakeserver is an in-memory satelle-server for tests: every service in
// api/satelle over a loopback listener. Chain.Append is compare-and-append on
// prev_hash with a StaleHead detail and checks every hash with internal/ledger.
// Nothing in the product imports it.
package fakeserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wire"
	"github.com/bobmcallan/satelle/internal/wire/chainpb"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// MaxMissing bounds the events a StaleHead detail carries.
const MaxMissing = 100

// Server is the fake. Create it with Start.
type Server struct {
	// URL is the server to hand the client ("http://127.0.0.1:<port>").
	URL string

	mu       sync.Mutex
	tokens   map[string]bool
	me       *satellepb.GetMeResponse
	calls    []Call
	projects []*satellepb.Project
	spaces   []*satellepb.Workspace
	chains   map[string]map[string][]*chainpb.Event // project id → item → events
	subs     map[*subscriber]bool
	items    map[string]map[string]*satellepb.Item // project id → item id → summary
	atts     []*satellepb.Attachment
	runs     map[string]*satellepb.ItemRun // remote|item → run
	nodes    map[string]*Node
	relays   map[string]*relayPair
	streams  map[*relayStream]bool
	gh       map[string]*github // project id → GitHub binding
	nextID   int
	grpc     *grpc.Server
}

// Call is one RPC the fake received.
type Call struct {
	Method, Auth string
}

// Start serves a fake on a loopback port for the life of t. It accepts the
// bearer tokens given, and any added later with AllowToken.
func Start(t testing.TB, tokens ...string) *Server {
	t.Helper()
	s := &Server{
		tokens:  map[string]bool{},
		me:      &satellepb.GetMeResponse{Id: "usr_1", Email: "ada@example.com", DisplayName: "Ada", Role: "member"},
		spaces:  []*satellepb.Workspace{{Id: "wsp_personal", Kind: "personal", Name: "personal"}},
		chains:  map[string]map[string][]*chainpb.Event{},
		subs:    map[*subscriber]bool{},
		items:   map[string]map[string]*satellepb.Item{},
		runs:    map[string]*satellepb.ItemRun{},
		nodes:   map[string]*Node{},
		relays:  map[string]*relayPair{},
		streams: map[*relayStream]bool{},
	}
	for _, tok := range tokens {
		s.tokens[tok] = true
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer(grpc.UnaryInterceptor(s.unaryAuth), grpc.StreamInterceptor(s.streamAuth))
	satellepb.RegisterAccountServiceServer(g, account{s: s})
	satellepb.RegisterProjectServiceServer(g, projects{s: s})
	satellepb.RegisterWorkspaceServiceServer(g, workspaces{s: s})
	satellepb.RegisterLocationServiceServer(g, locations{s: s})
	satellepb.RegisterItemServiceServer(g, itemsSvc{s: s})
	satellepb.RegisterAttachmentServiceServer(g, attachments{s: s})
	satellepb.RegisterRemoteServiceServer(g, remotes{s: s})
	satellepb.RegisterNodeServiceServer(g, nodes{s: s})
	satellepb.RegisterRelayServiceServer(g, relay{s: s})
	satellepb.RegisterGitHubServiceServer(g, githubSvc{s: s})
	chainpb.RegisterChainServer(g, chain{s: s})
	s.grpc = g
	go func() { _ = g.Serve(ln) }()
	t.Cleanup(g.Stop)
	s.URL = "http://" + ln.Addr().String()
	return s
}

// Stop stops serving: every later call fails as unreachable.
func (s *Server) Stop() { s.grpc.Stop() }

// AllowToken makes tok an accepted bearer.
func (s *Server) AllowToken(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[tok] = true
}

// RevokeToken stops accepting tok.
func (s *Server) RevokeToken(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, tok)
}

// SetMe replaces the principal GetMe answers.
func (s *Server) SetMe(me *satellepb.GetMeResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.me = me
}

// Calls is every RPC received, in order (refused ones included).
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// Events is the stored chain of item in project (id or slug).
func (s *Server) Events(project, item string) []*chainpb.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.project(project)
	if p == nil {
		return nil
	}
	return append([]*chainpb.Event(nil), s.chains[p.Id][item]...)
}

// SeedItem creates an item server-side the way satelle-server's MCP does: one
// item.created event carrying fields and no workflow or status (the server
// never decides process). The item id is <first three letters of kind>_<8
// hex> and the event id evt_<16 hex>, as the server makes them. It returns
// the item id.
func (s *Server) SeedItem(project string, fields map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.project(project)
	if p == nil {
		panic(fmt.Sprintf("fakeserver: no project %q", project))
	}
	kind, _ := fields["kind"].(string)
	if len(kind) < 3 {
		panic("fakeserver: SeedItem needs a kind of at least three letters")
	}
	id := strings.ToLower(kind[:3]) + "_" + randHex(4)
	payload, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	e := ledger.Event{ID: "evt_" + randHex(8), Item: id, Kind: "item.created", At: time.Now().UTC(), Actor: s.me.GetId(), Payload: payload}
	if e.Hash, err = ledger.HashOf(e); err != nil {
		panic(err)
	}
	pe := wire.ToProto(e)
	pe.Seq = 1
	if s.chains[p.Id] == nil {
		s.chains[p.Id] = map[string][]*chainpb.Event{}
	}
	s.chains[p.Id][id] = []*chainpb.Event{pe}
	for sub := range s.subs {
		if sub.project == p.Id {
			sub.send(pe)
		}
	}
	return id
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Items is the stored summaries of project (id or slug).
func (s *Server) Items(project string) []*satellepb.Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.project(project)
	if p == nil {
		return nil
	}
	var out []*satellepb.Item
	for _, it := range s.items[p.Id] {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out
}

// Attachments is every stored attachment, bodies included.
func (s *Server) Attachments() []*satellepb.Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*satellepb.Attachment(nil), s.atts...)
}

func bearer(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	for _, v := range md.Get("authorization") {
		if tok, ok := strings.CutPrefix(v, "Bearer "); ok {
			return tok
		}
	}
	return ""
}

// authorize records the call and checks its bearer: a user token or a live
// node's token.
func (s *Server) authorize(ctx context.Context, method string) error {
	tok := bearer(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Call{Method: method, Auth: tok})
	if tok != "" && s.tokens[tok] {
		return nil
	}
	for _, n := range s.nodes {
		if tok != "" && tok == n.Token && !n.ended {
			return nil
		}
	}
	return status.Error(codes.Unauthenticated, "invalid_token")
}

func (s *Server) unaryAuth(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	if err := s.authorize(ctx, info.FullMethod); err != nil {
		return nil, err
	}
	return h(ctx, req)
}

func (s *Server) streamAuth(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
	if err := s.authorize(ss.Context(), info.FullMethod); err != nil {
		return err
	}
	return h(srv, ss)
}

func (s *Server) newID(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s_%04d", prefix, s.nextID)
}

// project finds a project by id or slug; the caller holds mu.
func (s *Server) project(ref string) *satellepb.Project {
	for _, p := range s.projects {
		if p.Id == ref || p.Slug == ref {
			return p
		}
	}
	return nil
}

func (s *Server) projectOrNotFound(ref string) (*satellepb.Project, error) {
	p := s.project(ref)
	if p == nil {
		return nil, status.Errorf(codes.NotFound, "project %q not found", ref)
	}
	return p, nil
}

// ---- account, projects, workspaces, locations ----

type account struct {
	satellepb.UnimplementedAccountServiceServer
	s *Server
}

func (a account) GetMe(context.Context, *satellepb.GetMeRequest) (*satellepb.GetMeResponse, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	return proto.Clone(a.s.me).(*satellepb.GetMeResponse), nil
}

type projects struct {
	satellepb.UnimplementedProjectServiceServer
	s *Server
}

func (p projects) ListProjects(context.Context, *satellepb.ListProjectsRequest) (*satellepb.ListProjectsResponse, error) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	out := &satellepb.ListProjectsResponse{}
	for _, pr := range p.s.projects {
		out.Projects = append(out.Projects, proto.Clone(pr).(*satellepb.Project))
	}
	return out, nil
}

func (p projects) GetProject(_ context.Context, r *satellepb.GetProjectRequest) (*satellepb.Project, error) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	pr, err := p.s.projectOrNotFound(r.GetProject())
	if err != nil {
		return nil, err
	}
	return proto.Clone(pr).(*satellepb.Project), nil
}

func (p projects) CreateProject(_ context.Context, r *satellepb.CreateProjectRequest) (*satellepb.Project, error) {
	slug := strings.TrimSpace(r.GetSlug())
	if slug == "" {
		return nil, status.Error(codes.InvalidArgument, "a project needs a slug")
	}
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	if p.s.project(slug) != nil {
		return nil, status.Errorf(codes.AlreadyExists, "project slug %q already exists", slug)
	}
	name := r.GetName()
	if name == "" {
		name = slug
	}
	pr := &satellepb.Project{Id: p.s.newID("prj"), Slug: slug, Name: name, Role: "owner"}
	p.s.projects = append(p.s.projects, pr)
	return proto.Clone(pr).(*satellepb.Project), nil
}

type workspaces struct {
	satellepb.UnimplementedWorkspaceServiceServer
	s *Server
}

func (w workspaces) ListWorkspaces(context.Context, *satellepb.ListWorkspacesRequest) (*satellepb.ListWorkspacesResponse, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	out := &satellepb.ListWorkspacesResponse{}
	for _, ws := range w.s.spaces {
		out.Workspaces = append(out.Workspaces, proto.Clone(ws).(*satellepb.Workspace))
	}
	return out, nil
}

type locations struct {
	satellepb.UnimplementedLocationServiceServer
	s *Server
}

func (l locations) RegisterLocation(context.Context, *satellepb.RegisterLocationRequest) (*satellepb.RegisterLocationResponse, error) {
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	return &satellepb.RegisterLocationResponse{UserId: l.s.me.GetId()}, nil
}

// ---- chain ----

type chain struct {
	chainpb.UnimplementedChainServer
	s *Server
}

func head(events []*chainpb.Event) string {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].GetHash()
}

// after returns the events after hash ("" = all); ok is false when hash is not
// on the chain.
func after(events []*chainpb.Event, hash string) ([]*chainpb.Event, bool) {
	if hash == "" {
		return events, true
	}
	for i, e := range events {
		if e.GetHash() == hash {
			return events[i+1:], true
		}
	}
	return nil, false
}

func cloneEvents(in []*chainpb.Event) []*chainpb.Event {
	out := make([]*chainpb.Event, len(in))
	for i, e := range in {
		out[i] = proto.Clone(e).(*chainpb.Event)
	}
	return out
}

func (c chain) Append(_ context.Context, r *chainpb.AppendRequest) (*chainpb.AppendResponse, error) {
	if len(r.GetEvents()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no events")
	}
	prev := r.GetPrevHash()
	for i, e := range r.GetEvents() {
		if e.GetItemId() != r.GetItemId() {
			return nil, status.Errorf(codes.InvalidArgument, "event %d names item %q, not %q", i, e.GetItemId(), r.GetItemId())
		}
		if e.GetPrevHash() != prev {
			return nil, status.Errorf(codes.InvalidArgument, "event %d prev_hash %q does not link to %q", i, e.GetPrevHash(), prev)
		}
		if !wire.VerifyHash(e) {
			return nil, status.Errorf(codes.InvalidArgument, "event %d hash does not match its content", i)
		}
		prev = e.GetHash()
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.projectOrNotFound(r.GetProject())
	if err != nil {
		return nil, err
	}
	if s.chains[p.Id] == nil {
		s.chains[p.Id] = map[string][]*chainpb.Event{}
	}
	stored := s.chains[p.Id][r.GetItemId()]
	if h := head(stored); h != r.GetPrevHash() {
		missing, ok := after(stored, r.GetPrevHash())
		if !ok {
			missing = stored
		}
		detail := &chainpb.StaleHead{HeadHash: h, MissingCount: int64(len(missing))}
		if len(missing) > MaxMissing {
			missing = missing[:MaxMissing]
		}
		detail.Missing = cloneEvents(missing)
		st, _ := status.New(codes.FailedPrecondition, "stale head").WithDetails(detail)
		return nil, st.Err()
	}
	var appended []*chainpb.Event
	for _, e := range r.GetEvents() {
		e = proto.Clone(e).(*chainpb.Event)
		e.Seq = int64(len(stored)) + 1
		stored = append(stored, e)
		appended = append(appended, e)
	}
	s.chains[p.Id][r.GetItemId()] = stored
	for sub := range s.subs {
		if sub.project == p.Id {
			for _, e := range appended {
				sub.send(e)
			}
		}
	}
	return &chainpb.AppendResponse{HeadHash: head(stored), Appended: cloneEvents(appended)}, nil
}

func (c chain) Read(_ context.Context, r *chainpb.ReadRequest) (*chainpb.ReadResponse, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.projectOrNotFound(r.GetProject())
	if err != nil {
		return nil, err
	}
	events, ok := after(s.chains[p.Id][r.GetItemId()], r.GetAfterHash())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "%s has no event %s", r.GetItemId(), r.GetAfterHash())
	}
	return &chainpb.ReadResponse{Events: cloneEvents(events)}, nil
}

func (c chain) Heads(_ context.Context, r *chainpb.HeadsRequest) (*chainpb.HeadsResponse, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.projectOrNotFound(r.GetProject())
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(s.chains[p.Id]))
	for id := range s.chains[p.Id] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	start := 0
	if r.GetPageToken() != "" {
		if start, err = strconv.Atoi(r.GetPageToken()); err != nil || start < 0 || start > len(ids) {
			return nil, status.Error(codes.InvalidArgument, "bad page token")
		}
	}
	size := int(r.GetPageSize())
	if size <= 0 {
		size = 100
	}
	end := min(start+size, len(ids))
	out := &chainpb.HeadsResponse{}
	for _, id := range ids[start:end] {
		ev := s.chains[p.Id][id]
		out.Heads = append(out.Heads, &chainpb.ItemHead{ItemId: id, HeadHash: head(ev), Seq: int64(len(ev))})
	}
	if end < len(ids) {
		out.NextPageToken = strconv.Itoa(end)
	}
	return out, nil
}

type subscriber struct {
	project string
	ch      chan *chainpb.Event
}

// send queues e without blocking; a subscriber that falls far behind loses
// events (the fake is for tests).
func (sub *subscriber) send(e *chainpb.Event) {
	select {
	case sub.ch <- proto.Clone(e).(*chainpb.Event):
	default:
	}
}

func (c chain) Subscribe(r *chainpb.SubscribeRequest, stream grpc.ServerStreamingServer[chainpb.SubscribeResponse]) error {
	s := c.s
	sub := &subscriber{ch: make(chan *chainpb.Event, 1024)}
	s.mu.Lock()
	p, err := s.projectOrNotFound(r.GetProject())
	if err != nil {
		s.mu.Unlock()
		return err
	}
	sub.project = p.Id
	for item, hash := range r.GetResume() {
		missing, _ := after(s.chains[p.Id][item], hash)
		for _, e := range missing {
			sub.send(e)
		}
	}
	s.subs[sub] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
	}()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case e := <-sub.ch:
			if err := stream.Send(&chainpb.SubscribeResponse{Event: e}); err != nil {
				return err
			}
		}
	}
}

// ---- items and attachments ----

type itemsSvc struct {
	satellepb.UnimplementedItemServiceServer
	s *Server
}

func (i itemsSvc) PutItems(_ context.Context, r *satellepb.PutItemsRequest) (*satellepb.PutItemsResponse, error) {
	s := i.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.projectOrNotFound(r.GetProject())
	if err != nil {
		return nil, err
	}
	if s.items[p.Id] == nil {
		s.items[p.Id] = map[string]*satellepb.Item{}
	}
	for _, it := range r.GetItems() {
		s.items[p.Id][it.GetId()] = proto.Clone(it).(*satellepb.Item)
	}
	return &satellepb.PutItemsResponse{Stored: int32(len(r.GetItems()))}, nil
}

type attachments struct {
	satellepb.UnimplementedAttachmentServiceServer
	s *Server
}

func (a attachments) PutAttachment(_ context.Context, r *satellepb.PutAttachmentRequest) (*satellepb.PutAttachmentResponse, error) {
	in := r.GetAttachment()
	if in == nil || in.GetItemId() == "" || in.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "an attachment needs an item and a name")
	}
	sum := sha256.Sum256(in.GetBody())
	sha := hex.EncodeToString(sum[:])
	if in.GetSha256() != "" && in.GetSha256() != sha {
		return nil, status.Errorf(codes.InvalidArgument, "sha256 %s does not match the body (%s)", in.GetSha256(), sha)
	}
	s := a.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.projectOrNotFound(in.GetProject())
	if err != nil {
		return nil, err
	}
	for _, have := range s.atts {
		if have.Project == p.Id && have.ItemId == in.GetItemId() && have.Name == in.GetName() && have.Sha256 == sha {
			return &satellepb.PutAttachmentResponse{Created: false, Sha256: sha}, nil
		}
	}
	stored := proto.Clone(in).(*satellepb.Attachment)
	stored.Project, stored.Sha256, stored.Size = p.Id, sha, int64(len(in.GetBody()))
	stored.StoredAt = timestamppb.New(time.Now())
	s.atts = append(s.atts, stored)
	return &satellepb.PutAttachmentResponse{Created: true, Sha256: sha}, nil
}

func (a attachments) GetAttachment(_ context.Context, r *satellepb.GetAttachmentRequest) (*satellepb.Attachment, error) {
	s := a.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.projectOrNotFound(r.GetProject())
	if err != nil {
		return nil, err
	}
	for i := len(s.atts) - 1; i >= 0; i-- {
		have := s.atts[i]
		if have.Project == p.Id && have.ItemId == r.GetItemId() && have.Name == r.GetName() &&
			(r.GetSha256() == "" || r.GetSha256() == have.Sha256) {
			return proto.Clone(have).(*satellepb.Attachment), nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "no attachment %s on %s", r.GetName(), r.GetItemId())
}

func (a attachments) ListAttachmentChanges(_ context.Context, r *satellepb.ListAttachmentChangesRequest) (*satellepb.ListAttachmentChangesResponse, error) {
	s := a.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.projectOrNotFound(r.GetProject())
	if err != nil {
		return nil, err
	}
	start := 0
	if r.GetSinceToken() != "" {
		if start, err = strconv.Atoi(r.GetSinceToken()); err != nil || start < 0 || start > len(s.atts) {
			return nil, status.Error(codes.InvalidArgument, "bad since token")
		}
	}
	size := int(r.GetPageSize())
	if size <= 0 {
		size = 100
	}
	out := &satellepb.ListAttachmentChangesResponse{}
	i := start
	for ; i < len(s.atts) && len(out.Attachments) < size; i++ {
		if s.atts[i].Project == p.Id {
			c := proto.Clone(s.atts[i]).(*satellepb.Attachment)
			c.Body = nil
			out.Attachments = append(out.Attachments, c)
		}
	}
	out.NextToken = strconv.Itoa(i)
	return out, nil
}
