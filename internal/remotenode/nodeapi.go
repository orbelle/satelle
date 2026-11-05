package remotenode

// A node talks to satelle.dev with one credential, the node token the launcher
// gave it. About the node it is (the definition it runs from, each secret it is
// granted, its run and the end of its run) it calls satelle.v1.NodeService
// with the node id it was started with (the --session value). A value a secret
// read returns is handed to the caller in memory and goes nowhere else: not to
// disk, the ledger, a log or an error.

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

// ErrDefinitionDigest: the definition's bytes do not hash to the digest the
// server sent with them, so they are not the stored bundle.
var ErrDefinitionDigest = errors.New("the definition does not match its digest")

// TokenRefusedError: satelle.dev answered a node call with UNAUTHENTICATED, so
// the node token is bad or revoked. Route names the call, and nothing else is
// asked after it.
type TokenRefusedError struct{ Route string }

func (e *TokenRefusedError) Error() string {
	return "the node token was refused or revoked by satelle.dev (" + e.Route + ")"
}

// SecretRefusal is satelle.dev's refusal of one secret's read, with the server's
// own code and reason (a key the definition does not declare, or one that was not
// granted). The agent is not started on one.
type SecretRefusal struct {
	Key, Code, Reason string
}

func (e *SecretRefusal) Error() string {
	return fmt.Sprintf("secret %s refused: %s: %s", e.Key, e.Code, e.Reason)
}

// NodeClient is a node's calls to satelle.dev for the node whose id it carries.
type NodeClient struct {
	nodeID string
	client *hosted.Client

	mu      sync.Mutex
	refused bool
	endOnce sync.Once
	endErr  error
}

// NewNodeClient returns the client for node nodeID at server, authenticated by
// token. Nothing is sent until the first call.
func NewNodeClient(server, nodeID, token string) (*NodeClient, error) {
	c, err := hosted.Dial(server, hosted.NodeTokenStore{Server: server, Token: token}, hosted.Options{})
	if err != nil {
		return nil, err
	}
	return &NodeClient{nodeID: nodeID, client: c}, nil
}

// Close closes the connection.
func (c *NodeClient) Close() error { return c.client.Close() }

// call runs one NodeService call unless the token has already been refused. A
// refusal of the token is remembered, so nothing further is asked.
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

// Definition fetches the stored bundle of the definition the node runs and the
// version it was stored as. The bytes must hash to the digest the server sent.
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

// SecretRefusedGitHubAppOwnerRun is satelle.dev's code for a github-app secret
// read by a local owner run: an installation token is minted only for a woken
// remote run, so the owner's own run is refused it explicitly.
const SecretRefusedGitHubAppOwnerRun = "github_app_owner_run"

// Secret reads one secret's value. A key satelle.dev refuses (not declared by the
// definition, or not granted to it) is a *SecretRefusal.
func (c *NodeClient) Secret(ctx context.Context, key string) (string, error) {
	v, _, err := c.SecretWithExpiry(ctx, key)
	return v, err
}

// SecretWithExpiry is Secret with the time the value expires, which satelle.dev
// sends for a value it minted for the run (a github-app installation token); it is
// the zero time for a value that does not expire. An expiry that is not RFC 3339
// is a malformed answer.
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

// RunDescriptor is what NodeService.GetRun tells a node about its run. StoryID
// is the item the node was woken for, Trigger the declared trigger that woke it
// and TriggerSource how (mcp, api, status or schedule); a node started by hand
// carries none of the three. Project is the node's project, the one its work
// state is synced to.
type RunDescriptor struct {
	Mode          string
	Project       string
	StoryID       string
	Binding       string
	Trigger       string
	TriggerSource string
}

// Run reads the node's run descriptor.
func (c *NodeClient) Run(ctx context.Context) (RunDescriptor, error) {
	var d *satellepb.RunDescriptor
	err := c.call("NodeService.GetRun", func() (err error) {
		d, err = c.client.Nodes.GetRun(ctx, &satellepb.GetRunRequest{NodeId: c.nodeID})
		return err
	})
	if err != nil {
		return RunDescriptor{}, fmt.Errorf("run: %w", err)
	}
	return RunDescriptor{Mode: d.GetMode(), Project: d.GetProject(), StoryID: d.GetItemId(), Binding: d.GetBinding(),
		Trigger: d.GetTrigger(), TriggerSource: d.GetTriggerSource()}, nil
}

// ModePerformer is the run mode of a node a starter asked satelle.dev for to
// perform one step of its item over the paired channel.
const ModePerformer = "performer"

// PublishChannelKey publishes the node's channel public key so the starter that
// asked for this node can pin it before it trusts a message. The key is
// write-once: a second write is refused by the server and returned here.
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

// StateRunning is the heartbeat state a node is reported under.
const StateRunning = "running"

// NodeStatus is the node's heartbeat (NodeService.ReportStatus). Outcome and
// Findings go with RunState concluded, as the verdict and its notes.
type NodeStatus struct {
	State    string `json:"state"`
	StoryID  string `json:"story_id,omitempty"`
	Step     string `json:"step,omitempty"`
	RunState string `json:"run_state,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	Findings string `json:"findings,omitempty"`
}

// StatusReply is what the server says back to a heartbeat that the node uses:
// LostAfterSeconds is how long it may go unheard before the server lists it lost.
type StatusReply struct {
	LostAfterSeconds int
}

// Status sends one heartbeat.
func (c *NodeClient) Status(ctx context.Context, s NodeStatus) (StatusReply, error) {
	var r *satellepb.ReportStatusResponse
	err := c.call("NodeService.ReportStatus", func() (err error) {
		r, err = c.client.Nodes.ReportStatus(ctx, &satellepb.ReportStatusRequest{
			NodeId: c.nodeID, State: s.State, ItemId: s.StoryID, Step: s.Step, RunState: s.RunState,
			Verdict: s.Outcome, Notes: s.Findings,
		})
		return err
	})
	if err != nil {
		return StatusReply{}, fmt.Errorf("status: %w", err)
	}
	return StatusReply{LostAfterSeconds: int(r.GetLostAfterSeconds())}, nil
}

// End tells satelle.dev the node's run is over, which revokes the node token. It
// sends the call once however often it is called; a refused token is already
// gone, which is what End wanted.
func (c *NodeClient) End(ctx context.Context) error {
	c.endOnce.Do(func() {
		_, err := c.client.Nodes.EndNode(ctx, &satellepb.EndNodeRequest{NodeId: c.nodeID})
		if err != nil && !errors.Is(err, hosted.ErrNodeTokenRefused) {
			c.endErr = hosted.Describe("end", err)
		}
	})
	return c.endErr
}
