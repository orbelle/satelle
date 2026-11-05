package fakeserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// Node is a remote node the fake knows. Set its fields before the node calls.
type Node struct {
	ID, Token string
	// Bundle is the stored definition, at Version.
	Bundle  []byte
	Version string
	// Secrets are the values granted, by key; SecretExpiry their expiry.
	Secrets      map[string]string
	SecretExpiry map[string]time.Time
	// Refuse answers a read of a key with this code and reason.
	Refuse map[string][2]string
	// Run is what GetRun answers.
	Run *satellepb.RunDescriptor
	// LostAfterSeconds is reported on every ReportStatus.
	LostAfterSeconds int32

	channelKey []byte
	statuses   []*satellepb.ReportStatusRequest
	ended      bool
}

// AddNode registers a node; its token is accepted on the node's own calls
// until EndNode or RevokeNode.
func (s *Server) AddNode(n *Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n.Secrets == nil {
		n.Secrets = map[string]string{}
	}
	s.nodes[n.ID] = n
}

// RevokeNode ends a node's token.
func (s *Server) RevokeNode(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.nodes[id]; n != nil {
		n.ended = true
	}
}

// NodeStatuses is every ReportStatus a node sent.
func (s *Server) NodeStatuses(id string) []*satellepb.ReportStatusRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.nodes[id]; n != nil {
		return append([]*satellepb.ReportStatusRequest(nil), n.statuses...)
	}
	return nil
}

// ChannelKey is the key a node published, nil until it does.
func (s *Server) ChannelKey(id string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.nodes[id]; n != nil {
		return append([]byte(nil), n.channelKey...)
	}
	return nil
}

// ---- remotes ----

type remotes struct {
	satellepb.UnimplementedRemoteServiceServer
	s *Server
}

func (r remotes) FireRemote(_ context.Context, req *satellepb.FireRemoteRequest) (*satellepb.FireRemoteResponse, error) {
	s := r.s
	s.mu.Lock()
	defer s.mu.Unlock()
	key := req.GetRemoteId() + "|" + req.GetItemId()
	if run := s.runs[key]; run != nil && run.State != "concluded" && run.State != "stopped" {
		return &satellepb.FireRemoteResponse{Pending: true, Position: 1, Outcome: "queued"}, nil
	}
	id := s.newID("run")
	s.runs[key] = &satellepb.ItemRun{State: "queued"}
	return &satellepb.FireRemoteResponse{RunId: id, Outcome: "started"}, nil
}

func (r remotes) GetItemRun(_ context.Context, req *satellepb.GetItemRunRequest) (*satellepb.ItemRun, error) {
	s := r.s
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[req.GetRemoteId()+"|"+req.GetItemId()]
	if run == nil {
		return nil, status.Error(codes.NotFound, "no run")
	}
	return proto.Clone(run).(*satellepb.ItemRun), nil
}

func (r remotes) StartNode(_ context.Context, req *satellepb.StartNodeRequest) (*satellepb.StartNodeResponse, error) {
	s := r.s
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newID("nod")
	s.nodes[id] = &Node{ID: id, Token: id + "-token", Secrets: map[string]string{},
		Run: &satellepb.RunDescriptor{Mode: "performer", Project: req.GetProject(), ItemId: req.GetItemId(), Binding: req.GetBinding()}}
	return &satellepb.StartNodeResponse{NodeId: id}, nil
}

// ---- the node contract ----

type nodes struct {
	satellepb.UnimplementedNodeServiceServer
	s *Server
}

// node returns the node id names when the caller's bearer is its token.
func (n nodes) node(ctx context.Context, id string) (*Node, error) {
	node := n.s.nodes[id]
	if node == nil {
		return nil, status.Errorf(codes.NotFound, "node %q not found", id)
	}
	if bearer(ctx) != node.Token || node.ended {
		return nil, status.Error(codes.Unauthenticated, "invalid_token")
	}
	return node, nil
}

func (n nodes) GetNode(_ context.Context, r *satellepb.GetNodeRequest) (*satellepb.Node, error) {
	n.s.mu.Lock()
	defer n.s.mu.Unlock()
	node := n.s.nodes[r.GetNodeId()]
	if node == nil {
		return nil, status.Errorf(codes.NotFound, "node %q not found", r.GetNodeId())
	}
	state := "running"
	if node.ended {
		state = "ended"
	}
	return &satellepb.Node{NodeId: node.ID, ChannelPublicKey: append([]byte(nil), node.channelKey...), State: state}, nil
}

func (n nodes) GetDefinition(ctx context.Context, r *satellepb.GetDefinitionRequest) (*satellepb.Definition, error) {
	n.s.mu.Lock()
	defer n.s.mu.Unlock()
	node, err := n.node(ctx, r.GetNodeId())
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(node.Bundle)
	digest := hex.EncodeToString(sum[:])
	if r.GetIfNoneMatch() == digest {
		return &satellepb.Definition{Digest: digest, Version: node.Version, NotModified: true}, nil
	}
	return &satellepb.Definition{Digest: digest, Version: node.Version, Bundle: append([]byte(nil), node.Bundle...)}, nil
}

func (n nodes) ReadSecret(ctx context.Context, r *satellepb.ReadSecretRequest) (*satellepb.Secret, error) {
	n.s.mu.Lock()
	defer n.s.mu.Unlock()
	node, err := n.node(ctx, r.GetNodeId())
	if err != nil {
		return nil, err
	}
	if ref, refused := node.Refuse[r.GetKey()]; refused {
		return &satellepb.Secret{Refusal: ref[0], RefusalReason: ref[1]}, nil
	}
	v, ok := node.Secrets[r.GetKey()]
	if !ok {
		return &satellepb.Secret{Refusal: "secret_refused", RefusalReason: "the key is not declared by this definition or not granted to it"}, nil
	}
	out := &satellepb.Secret{Value: v}
	if exp, ok := node.SecretExpiry[r.GetKey()]; ok {
		out.ExpiresAt = timestamppb.New(exp)
	}
	return out, nil
}

func (n nodes) GetRun(ctx context.Context, r *satellepb.GetRunRequest) (*satellepb.RunDescriptor, error) {
	n.s.mu.Lock()
	defer n.s.mu.Unlock()
	node, err := n.node(ctx, r.GetNodeId())
	if err != nil {
		return nil, err
	}
	if node.Run == nil {
		return &satellepb.RunDescriptor{Mode: "node"}, nil
	}
	return proto.Clone(node.Run).(*satellepb.RunDescriptor), nil
}

func (n nodes) PublishChannelKey(ctx context.Context, r *satellepb.PublishChannelKeyRequest) (*satellepb.PublishChannelKeyResponse, error) {
	n.s.mu.Lock()
	defer n.s.mu.Unlock()
	node, err := n.node(ctx, r.GetNodeId())
	if err != nil {
		return nil, err
	}
	if len(r.GetPublicKey()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid_public_key")
	}
	if node.channelKey != nil {
		return nil, status.Error(codes.AlreadyExists, "already_published")
	}
	node.channelKey = append([]byte(nil), r.GetPublicKey()...)
	return &satellepb.PublishChannelKeyResponse{}, nil
}

func (n nodes) ReportStatus(ctx context.Context, r *satellepb.ReportStatusRequest) (*satellepb.ReportStatusResponse, error) {
	n.s.mu.Lock()
	defer n.s.mu.Unlock()
	node, err := n.node(ctx, r.GetNodeId())
	if err != nil {
		return nil, err
	}
	node.statuses = append(node.statuses, proto.Clone(r).(*satellepb.ReportStatusRequest))
	return &satellepb.ReportStatusResponse{LostAfterSeconds: node.LostAfterSeconds}, nil
}

func (n nodes) EndNode(ctx context.Context, r *satellepb.EndNodeRequest) (*satellepb.EndNodeResponse, error) {
	n.s.mu.Lock()
	defer n.s.mu.Unlock()
	node, err := n.node(ctx, r.GetNodeId())
	if err != nil {
		return nil, err
	}
	node.ended = true
	return &satellepb.EndNodeResponse{}, nil
}

// ---- relay ----

type relay struct {
	satellepb.UnimplementedRelayServiceServer
	s *Server
}

// relayPair joins the local and remote ends of one session.
type relayPair struct {
	ends map[string]chan []byte // role → frames for that role
}

func (s *Server) pair(session string) *relayPair {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.relays[session]
	if p == nil {
		p = &relayPair{ends: map[string]chan []byte{"local": make(chan []byte, 256), "remote": make(chan []byte, 256)}}
		s.relays[session] = p
	}
	return p
}

// Connect forwards each side's data frames to the other, in order.
func (r relay) Connect(stream grpc.BidiStreamingServer[satellepb.RelayFrame, satellepb.RelayFrame]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	role := first.GetRole()
	if first.GetSession() == "" || (role != "local" && role != "remote") {
		return status.Error(codes.InvalidArgument, "the first frame names the session and role")
	}
	other := "remote"
	if role == "remote" {
		other = "local"
	}
	p := r.s.pair(first.GetSession())
	ctx := stream.Context()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-p.ends[role]:
				if stream.Send(&satellepb.RelayFrame{Data: b}) != nil {
					return
				}
			}
		}
	}()
	if len(first.GetData()) > 0 {
		p.ends[other] <- first.GetData()
	}
	for {
		f, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case p.ends[other] <- f.GetData():
		case <-ctx.Done():
			return nil
		}
	}
}
