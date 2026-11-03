package remotenode

// A node talks to satelle.dev with one credential, the node token the launcher
// gave it, and about one thing: the node it is. The definition it runs from, each
// secret it is granted, the hosted chain of a story it writes to, the record of
// its run (workstate) and the end of its run are all routes under
// /api/v1/remote-nodes/{id}, where {id} is the --session value the node was
// started with (satelle-server sty_c9013137). A value a secret route returns is
// handed to the caller in memory and goes nowhere else: not to disk, the ledger,
// a log or an error.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
)

// DefinitionVersionHeader carries the version of the definition a node fetched.
const DefinitionVersionHeader = "X-Satelle-Definition-Version"

// ErrDefinitionDigest: the definition's bytes do not hash to the ETag the server
// sent with them, so they are not the stored bundle.
var ErrDefinitionDigest = errors.New("the definition does not match its ETag")

// TokenRefusedError: satelle.dev answered a node route with 401, so the node token
// is bad or revoked. Route names the request, and nothing else is asked after it.
type TokenRefusedError struct{ Route string }

func (e *TokenRefusedError) Error() string {
	return "the node token was refused or revoked by satelle.dev (" + e.Route + ")"
}

// SecretRefusal is satelle.dev's refusal of one secret's read, with the server's
// own code and reason (a key the definition does not declare, or one that was not
// granted). The agent is not started on one.
type SecretRefusal struct {
	Key string
	Err *hosted.APIError
}

func (e *SecretRefusal) Error() string { return fmt.Sprintf("secret %s refused: %s", e.Key, e.Err) }
func (e *SecretRefusal) Unwrap() error { return e.Err }

// NodeClient is a node's calls to satelle.dev for the node whose id it carries.
type NodeClient struct {
	nodeID, token string
	client        *hosted.Client

	mu      sync.Mutex
	refused bool
	endOnce sync.Once
	endErr  error
}

// NewNodeClient returns the client for node nodeID at server, authenticated by
// token. A nil hc uses a 30 second timeout.
func NewNodeClient(server, nodeID, token string, hc *http.Client) *NodeClient {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &NodeClient{nodeID: nodeID, token: token, client: hosted.NewClient(server, nil, hc)}
}

func (c *NodeClient) path(tail string) string {
	return "/api/v1/remote-nodes/" + url.PathEscape(c.nodeID) + tail
}

// do sends one request with the node token. A 401 is the token's refusal: it is
// remembered, so call asks nothing further.
func (c *NodeClient) do(ctx context.Context, method, tail string, reqBody any) ([]byte, http.Header, error) {
	route := method + " " + c.path(tail)
	body, h, err := c.client.SignedDo(ctx, hosted.SignedRequest{Method: method, Path: c.path(tail), Bearer: c.token, Body: reqBody})
	var ae *hosted.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
		c.mu.Lock()
		c.refused = true
		c.mu.Unlock()
		return nil, nil, &TokenRefusedError{Route: route}
	}
	return body, h, err
}

// call is do unless the token has already been refused, in which case it makes no
// request. A non-nil reqBody is sent as JSON.
func (c *NodeClient) call(ctx context.Context, method, tail string, reqBody any) ([]byte, http.Header, error) {
	c.mu.Lock()
	refused := c.refused
	c.mu.Unlock()
	if refused {
		return nil, nil, &TokenRefusedError{Route: method + " " + c.path(tail)}
	}
	return c.do(ctx, method, tail, reqBody)
}

// Definition fetches the stored bundle of the definition the node runs and the
// version it was stored as. The bytes are exactly the stored bundle, and they must
// hash to the ETag the server sent with them.
func (c *NodeClient) Definition(ctx context.Context) (bundle []byte, version string, err error) {
	body, h, err := c.call(ctx, http.MethodGet, "/definition", nil)
	if err != nil {
		return nil, "", fmt.Errorf("definition: %w", err)
	}
	sum := sha256.Sum256(body)
	etag := strings.TrimPrefix(strings.Trim(h.Get("ETag"), `"`), "W/")
	if etag == "" {
		return nil, "", fmt.Errorf("definition: %w: the answer has no ETag", ErrDefinitionDigest)
	}
	if !strings.EqualFold(etag, hex.EncodeToString(sum[:])) {
		return nil, "", fmt.Errorf("definition: %w", ErrDefinitionDigest)
	}
	version = h.Get(DefinitionVersionHeader)
	if version == "" {
		return nil, "", fmt.Errorf("definition: the answer has no %s", DefinitionVersionHeader)
	}
	return body, version, nil
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
	raw, _, err := c.call(ctx, http.MethodPost, "/secrets/"+url.PathEscape(key), nil)
	if err != nil {
		var ae *hosted.APIError
		if errors.As(err, &ae) {
			return "", time.Time{}, &SecretRefusal{Key: key, Err: ae}
		}
		var tr *TokenRefusedError
		if errors.As(err, &tr) {
			return "", time.Time{}, err
		}
		return "", time.Time{}, fmt.Errorf("secret %s: %w", key, err)
	}
	var v struct {
		Value     string `json:"value"`
		ExpiresAt string `json:"expires_at"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return "", time.Time{}, fmt.Errorf("secret %s: the answer is malformed", key)
	}
	var expires time.Time
	if v.ExpiresAt != "" {
		t, perr := time.Parse(time.RFC3339, v.ExpiresAt)
		if perr != nil {
			return "", time.Time{}, fmt.Errorf("secret %s: the answer's expires_at is not RFC 3339", key)
		}
		expires = t
	}
	return v.Value, expires, nil
}

// RunDescriptor is what GET .../run tells a node about its run. Only the wake's
// subject is read: StoryID is the story the node was woken for, Trigger the
// declared trigger that woke it and TriggerSource how (mcp, api, status or
// schedule). A node started by hand carries none of the three.
type RunDescriptor struct {
	Mode          string `json:"mode"`
	StoryID       string `json:"story_id,omitempty"`
	Trigger       string `json:"trigger,omitempty"`
	TriggerSource string `json:"trigger_source,omitempty"`
}

// Run reads the node's run descriptor.
func (c *NodeClient) Run(ctx context.Context) (RunDescriptor, error) {
	raw, _, err := c.call(ctx, http.MethodGet, "/run", nil)
	if err != nil {
		return RunDescriptor{}, fmt.Errorf("run: %w", err)
	}
	var d RunDescriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		return RunDescriptor{}, errors.New("run: the answer is malformed")
	}
	return d, nil
}

// ModePerformer is the run mode of a node a starter asked satelle.dev for to
// perform one step of its story over the paired channel (sty_7028d996).
const ModePerformer = "performer"

// PublishChannelKey publishes the node's channel public key (PUT .../channel-key,
// standard base64) so the starter that asked for this node can pin it before it
// trusts a message. The key is write-once: a second write is refused by the
// server and returned here.
func (c *NodeClient) PublishChannelKey(ctx context.Context, pub []byte) error {
	body := struct {
		PublicKey string `json:"public_key"`
	}{PublicKey: base64.StdEncoding.EncodeToString(pub)}
	if _, _, err := c.call(ctx, http.MethodPut, "/channel-key", body); err != nil {
		return fmt.Errorf("channel-key: %w", err)
	}
	return nil
}

// StateRunning is the heartbeat state a node is reported under (satelle-server
// remotes.NodeRunning). The run states it reports for its task run are
// runstatus.Queued, runstatus.Running and runstatus.Concluded.
const StateRunning = "running"

// NodeStatus is the node's heartbeat, PUT to .../status. RunState and Outcome are
// sent only when set, and Outcome and Findings only with runstatus.Concluded.
type NodeStatus struct {
	State    string `json:"state"`
	StoryID  string `json:"story_id,omitempty"`
	Step     string `json:"step,omitempty"`
	RunState string `json:"run_state,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	// Findings is what the run recorded behind its outcome, for the local driver
	// to relay to the step it hands the story to.
	Findings string `json:"findings,omitempty"`
}

// StatusReply is what the server says back to a heartbeat that the node uses:
// LostAfterSeconds is how long it may go unheard before the server lists it lost.
type StatusReply struct {
	LostAfterSeconds int `json:"lost_after_seconds"`
}

// Status sends one heartbeat. A report carrying findings that the server refuses
// with 400 is sent once more without them: a server that decodes the heartbeat
// strictly and predates the field would otherwise drop the concluded outcome too.
func (c *NodeClient) Status(ctx context.Context, s NodeStatus) (StatusReply, error) {
	raw, _, err := c.call(ctx, http.MethodPut, "/status", s)
	var ae *hosted.APIError
	if err != nil && s.Findings != "" && errors.As(err, &ae) && ae.Status == http.StatusBadRequest {
		s.Findings = ""
		raw, _, err = c.call(ctx, http.MethodPut, "/status", s)
	}
	if err != nil {
		return StatusReply{}, fmt.Errorf("status: %w", err)
	}
	var r StatusReply
	_ = json.Unmarshal(raw, &r) // a reply without the window falls back to the default cadence
	return r, nil
}

// RunOutput is one run-output document of an execution: its file name and text.
type RunOutput struct {
	Execution string `json:"execution"`
	Name      string `json:"name"`
	Body      string `json:"body"`
}

// NodeWorkstate is what a node pushes of its run, POSTed to .../workstate: the
// execution, its ledger rows, the subject's link row (the rows are the same
// encoding a personal `sync workstate push` sends) and the run-output documents.
// It names no project; satelle.dev records it in the node's team workspace.
type NodeWorkstate struct {
	Items   []json.RawMessage `json:"items"`
	Ledger  []json.RawMessage `json:"ledger"`
	Outputs []RunOutput       `json:"outputs"`
}

// PushWorkstateReply is what satelle.dev says back to a push: the team project the
// batch was recorded in and how many of each it took.
type PushWorkstateReply struct {
	Project string `json:"project"`
	Items   int    `json:"items"`
	Ledger  int    `json:"ledger"`
	Outputs int    `json:"outputs"`
}

// PushWorkstate sends the node's run to its team workspace under the node token.
func (c *NodeClient) PushWorkstate(ctx context.Context, w NodeWorkstate) (PushWorkstateReply, error) {
	raw, _, err := c.call(ctx, http.MethodPost, "/workstate", w)
	if err != nil {
		return PushWorkstateReply{}, fmt.Errorf("workstate: %w", err)
	}
	var r PushWorkstateReply
	_ = json.Unmarshal(raw, &r)
	return r, nil
}

// ErrChainRouteUnsupported: satelle.dev answered the node's story-chain read with
// 404, so it serves no chain to a node. The node writes on what its store holds,
// as it did before the route.
var ErrChainRouteUnsupported = errors.New("satelle.dev serves no story chain to a node")

// chainEvent is one event of a story's hosted chain as the node's chain route
// carries it: the hosted.ChainEvent fields, whose payload is the event's ledger
// row in the encoding a workstate push sends.
type chainEvent struct {
	ID              string          `json:"id"`
	BaseID          string          `json:"base_id"`
	WorkflowVersion string          `json:"workflow_version,omitempty"`
	Payload         json.RawMessage `json:"payload"`
}

// StoryChain reads the hosted chain of storyID (a story of the node's team
// workspace, or an execution the node made) under the node token, oldest first,
// and its latest event id: GET .../stories/{story}/chain answering
// {"events":[…],"latest":"…"}. A node writes onto that head, so its push builds on
// the chain rather than starting one (sty_0d621eaf).
func (c *NodeClient) StoryChain(ctx context.Context, storyID string) ([]hosted.ChainEvent, string, error) {
	raw, _, err := c.call(ctx, http.MethodGet, "/stories/"+url.PathEscape(storyID)+"/chain", nil)
	if err != nil {
		var ae *hosted.APIError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			return nil, "", fmt.Errorf("chain of %s: %w", storyID, ErrChainRouteUnsupported)
		}
		return nil, "", fmt.Errorf("chain of %s: %w", storyID, err)
	}
	var out struct {
		Events []chainEvent `json:"events"`
		Latest string       `json:"latest"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, "", fmt.Errorf("chain of %s: the answer is malformed", storyID)
	}
	events := make([]hosted.ChainEvent, len(out.Events))
	for i, e := range out.Events {
		events[i] = hosted.ChainEvent{ID: e.ID, BaseID: e.BaseID, WorkflowVersion: e.WorkflowVersion, Payload: e.Payload}
	}
	return events, out.Latest, nil
}

// End tells satelle.dev the node's run is over, which revokes the node token. It
// sends the request once however often it is called; a 401 means the token is
// already gone, which is what End wanted.
func (c *NodeClient) End(ctx context.Context) error {
	c.endOnce.Do(func() {
		_, _, err := c.do(ctx, http.MethodPost, "/end", nil)
		var tr *TokenRefusedError
		if err != nil && !errors.As(err, &tr) {
			c.endErr = fmt.Errorf("end: %w", err)
		}
	})
	return c.endErr
}
