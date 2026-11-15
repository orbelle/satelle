package remotenode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

var ErrDefinitionDigest = errors.New("the definition does not match its digest")

type TokenRefusedError struct{ Route string }

func (e *TokenRefusedError) Error() string {
	return "the node token was refused or revoked by satelle.dev (" + e.Route + ")"
}

type SecretRefusal struct {
	Key, Code, Reason string
}

func (e *SecretRefusal) Error() string {
	return fmt.Sprintf("secret %s refused: %s: %s", e.Key, e.Code, e.Reason)
}

type NodeClient struct {
	nodeID string
	client *hosted.Client

	mu      sync.Mutex
	refused bool
	endOnce sync.Once
	endErr  error
}

func NewNodeClient(server, nodeID, token string) (*NodeClient, error) {
	c, err := hosted.Dial(server, hosted.NodeTokenStore{Server: server, Token: token}, hosted.Options{})
	if err != nil {
		return nil, err
	}
	return &NodeClient{nodeID: nodeID, client: c}, nil
}

func (c *NodeClient) Close() error { return c.client.Close() }

func (c *NodeClient) Hosted() *hosted.Client { return c.client }

func (c *NodeClient) NodeID() string { return c.nodeID }

func (c *NodeClient) call(route string, fn func() error) error {
	c.mu.Lock()
	refused := c.refused
	c.mu.Unlock()
	if refused {
		return &TokenRefusedError{Route: route}
	}
	err := fn()
	if errors.Is(err, hosted.ErrNodeTokenRefused) || errors.Is(err, hosted.ErrLoginRequired) {
		c.mu.Lock()
		c.refused = true
		c.mu.Unlock()
		return &TokenRefusedError{Route: route}
	}
	if err != nil {
		return hosted.Describe(route, err)
	}
	return nil
}

func (c *NodeClient) Definition(ctx context.Context) (bundle []byte, version string, err error) {
	var d *satellepb.Definition
	err = c.call("NodeService.GetDefinition", func() (err error) {
		d, err = c.client.Nodes.GetDefinition(ctx, &satellepb.GetDefinitionRequest{NodeId: c.nodeID})
		return err
	})
	if err != nil {
		return nil, "", fmt.Errorf("definition: %w", err)
	}
	sum := sha256.Sum256(d.GetBundle())
	if d.GetDigest() == "" || d.GetDigest() != hex.EncodeToString(sum[:]) {
		return nil, "", fmt.Errorf("definition: %w", ErrDefinitionDigest)
	}
	if d.GetVersion() == "" {
		return nil, "", errors.New("definition: the answer has no version")
	}
	return d.GetBundle(), d.GetVersion(), nil
}

func (c *NodeClient) Secret(ctx context.Context, key string) (string, error) {
	v, _, err := c.SecretWithExpiry(ctx, key)
	return v, err
}

func (c *NodeClient) SecretWithExpiry(ctx context.Context, key string) (string, time.Time, error) {
	var sec *satellepb.Secret
	err := c.call("NodeService.ReadSecret", func() (err error) {
		sec, err = c.client.Nodes.ReadSecret(ctx, &satellepb.ReadSecretRequest{NodeId: c.nodeID, Key: key})
		return err
	})
	if err != nil {
		var tr *TokenRefusedError
		if errors.As(err, &tr) {
			return "", time.Time{}, err
		}
		return "", time.Time{}, fmt.Errorf("secret %s: the read failed", key)
	}
	if sec.GetRefusal() != "" {
		return "", time.Time{}, &SecretRefusal{Key: key, Code: sec.GetRefusal(), Reason: sec.GetRefusalReason()}
	}
	var expires time.Time
	if sec.GetExpiresAt() != nil {
		expires = sec.GetExpiresAt().AsTime()
	}
	return sec.GetValue(), expires, nil
}

type RunDescriptor struct {
	Mode          string
	Project       string
	ItemID        string
	Binding       string
	Trigger       string
	TriggerSource string
}

func (c *NodeClient) Run(ctx context.Context) (RunDescriptor, error) {
	var d *satellepb.RunDescriptor
	err := c.call("NodeService.GetRun", func() (err error) {
		d, err = c.client.Nodes.GetRun(ctx, &satellepb.GetRunRequest{NodeId: c.nodeID})
		return err
	})
	if err != nil {
		return RunDescriptor{}, fmt.Errorf("run: %w", err)
	}
	return RunDescriptor{Mode: d.GetMode(), Project: d.GetProject(), ItemID: d.GetItemId(), Binding: d.GetBinding(),
		Trigger: d.GetTrigger(), TriggerSource: d.GetTriggerSource()}, nil
}

const (
	ModeItem      = "item"
	ModeTask      = "task"
	ModePerformer = "performer"
)

func (c *NodeClient) PublishChannelKey(ctx context.Context, pub []byte) error {
	err := c.call("NodeService.PublishChannelKey", func() error {
		_, err := c.client.Nodes.PublishChannelKey(ctx, &satellepb.PublishChannelKeyRequest{NodeId: c.nodeID, PublicKey: pub})
		return err
	})
	if err != nil {
		return fmt.Errorf("channel-key: %w", err)
	}
	return nil
}

const (
	StateRunning   = "running"
	StateConcluded = "concluded"
)

type NodeStatus struct {
	State    string
	ItemID   string
	Step     string
	RunState string
	Progress string
	SetupLog string
	Verdict  string
	Notes    string
}

type StatusReply struct {
	Stop             bool
	LostAfterSeconds int
}

func (c *NodeClient) Status(ctx context.Context, s NodeStatus) (StatusReply, error) {
	var r *satellepb.ReportStatusResponse
	err := c.call("NodeService.ReportStatus", func() (err error) {
		r, err = c.client.Nodes.ReportStatus(ctx, &satellepb.ReportStatusRequest{
			NodeId: c.nodeID, State: s.State, ItemId: s.ItemID, Step: s.Step, RunState: s.RunState,
			Progress: s.Progress, SetupLog: s.SetupLog, Verdict: s.Verdict, Notes: s.Notes,
		})
		return err
	})
	if err != nil {
		return StatusReply{}, fmt.Errorf("status: %w", err)
	}
	return StatusReply{Stop: r.GetStop(), LostAfterSeconds: int(r.GetLostAfterSeconds())}, nil
}

func (c *NodeClient) End(ctx context.Context) error {
	c.endOnce.Do(func() {
		_, err := c.client.Nodes.EndNode(ctx, &satellepb.EndNodeRequest{NodeId: c.nodeID})
		if err != nil && !errors.Is(err, hosted.ErrNodeTokenRefused) {
			c.endErr = hosted.Describe("end", err)
		}
	})
	return c.endErr
}
