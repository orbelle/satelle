package remotenode

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledgerrepl"
	"github.com/bobmcallan/satelle/internal/remotechan"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

const DefaultReadyTimeout = 30 * time.Minute

type Placer struct {
	Client       *hosted.Client
	Project      string
	ReadyTimeout time.Duration
	ChanOpts     []remotechan.Option
	Repl         *ledgerrepl.Replicator
}

func (p Placer) StartNode(ctx context.Context, remote, itemID, binding string) (string, error) {
	if p.Client == nil || p.Project == "" {
		return "", errors.New("a remote placement needs a bound project (satelle project bind)")
	}
	if p.Repl == nil {
		return "", errors.New("a remote placement pushes the item first; no ledger to push")
	}
	res, err := p.Repl.Push(ctx, itemID)
	if err != nil {
		return "", fmt.Errorf("pushing %s before its node starts: %w", itemID, err)
	}
	if res.Failed() {
		return "", fmt.Errorf("pushing %s before its node starts: %s diverged from the server; run satelle ledger pull %s", itemID, itemID, itemID)
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

func (p Placer) Attach(ctx context.Context, node string) (*Run, error) {
	key := func(ctx context.Context) ([]byte, error) {
		n, err := p.Client.Nodes.GetNode(ctx, &satellepb.GetNodeRequest{NodeId: node})
		if err != nil {
			return nil, hosted.Describe("NodeService.GetNode", err)
		}
		return n.GetChannelPublicKey(), nil
	}
	sess, err := remotechan.Attach(ctx, p.Client.Relay, node, key, p.ChanOpts...)
	if err != nil {
		return nil, fmt.Errorf("channel to node %s: %w", node, err)
	}
	ready := p.ReadyTimeout
	if ready <= 0 {
		ready = DefaultReadyTimeout
	}
	run, err := AttachRun(ctx, sess, ready)
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", node, err)
	}
	return run, nil
}

func (p Placer) PerformOn(ctx context.Context, node string, req agent.Request) (agent.Result, error) {
	run, err := p.Attach(ctx, node)
	if err != nil {
		return agent.Result{}, err
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
	return agent.Result{Text: reply.Text, Model: reply.Model, Command: reply.Command, TokensIn: reply.TokensIn, TokensOut: reply.TokensOut, Duration: time.Since(start)}, nil
}
