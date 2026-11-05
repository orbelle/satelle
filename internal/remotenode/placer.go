package remotenode

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/remotechan"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// DefaultReadyTimeout bounds how long a starter waits for a performer node to
// set up and report ready.
const DefaultReadyTimeout = 30 * time.Minute

// Placer is the starter's side of placement "remote:<id>" (a
// kernel.RemotePerformer): it asks satelle-server for a node
// (RemoteService.StartNode), pins the key the node publishes
// (NodeService.GetNode), opens the sealed channel through the relay and sends
// the step as an AgentJob.
type Placer struct {
	Client  *hosted.Client
	Project string
	// ReadyTimeout bounds the wait for the node's ready report; zero is
	// DefaultReadyTimeout.
	ReadyTimeout time.Duration
	ChanOpts     []remotechan.Option
}

// StartNode asks for a node of remote to perform binding for item.
func (p Placer) StartNode(ctx context.Context, remote, itemID, binding string) (string, error) {
	if p.Client == nil || p.Project == "" {
		return "", errors.New("a remote placement needs a bound project (satelle project bind)")
	}
	r, err := p.Client.Remotes.StartNode(ctx, &satellepb.StartNodeRequest{RemoteId: remote, Project: p.Project, ItemId: itemID, Binding: binding})
	if err != nil {
		return "", hosted.Describe("RemoteService.StartNode", err)
	}
	if r.GetNodeId() == "" {
		return "", errors.New("RemoteService.StartNode answered no node")
	}
	return r.GetNodeId(), nil
}

// PerformOn sends req to node over the sealed channel and returns its answer.
func (p Placer) PerformOn(ctx context.Context, node string, req agent.Request) (agent.Result, error) {
	key := func(ctx context.Context) ([]byte, error) {
		n, err := p.Client.Nodes.GetNode(ctx, &satellepb.GetNodeRequest{NodeId: node})
		if err != nil {
			return nil, hosted.Describe("NodeService.GetNode", err)
		}
		return n.GetChannelPublicKey(), nil
	}
	sess, err := remotechan.Attach(ctx, p.Client.Relay, node, key, p.ChanOpts...)
	if err != nil {
		return agent.Result{}, fmt.Errorf("channel to node %s: %w", node, err)
	}
	ready := p.ReadyTimeout
	if ready <= 0 {
		ready = DefaultReadyTimeout
	}
	run, err := AttachRun(ctx, sess, ready)
	if err != nil {
		return agent.Result{}, fmt.Errorf("node %s: %w", node, err)
	}
	defer run.End()
	start := time.Now()
	body, err := run.Request(ctx, AgentJob, AgentRequest{Text: req.Prompt, Binding: req.Binding.Name, SystemPrompt: req.System})
	if err != nil {
		return agent.Result{}, fmt.Errorf("node %s: %w", node, err)
	}
	reply, err := DecodeAgentReply(body)
	if err != nil {
		return agent.Result{}, fmt.Errorf("node %s: malformed reply: %w", node, err)
	}
	return agent.Result{Text: reply.Text, Model: reply.Model, TokensIn: reply.TokensIn, TokensOut: reply.TokensOut, Duration: time.Since(start)}, nil
}
