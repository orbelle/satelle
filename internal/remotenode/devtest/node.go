// Package devtest is a stand-in for the routes satelle.dev serves to a remote
// node, for tests: GET /api/v1/remote-nodes/{id}/definition, GET .../run,
// PUT .../status, PUT .../channel-key, POST .../secrets/{key}, GET
// .../stories/{story}/chain, POST .../workstate (which judges each ledger row's
// base against its story's chain) and POST .../end, all keyed by the node's id and taking
// the node token as a bearer (satelle-server sty_c9013137). A call under any other
// id is a 404; a bad token, or one an end call or Revoke has ended, is a 401. It
// records every request. It is for tests only: nothing in the product imports it.
package devtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// SecretValue is a value for a test to grant: one that must appear nowhere but the
// agent's environment.
const SecretValue = "sk-ant-oat01-NEVER-WRITTEN-ANYWHERE"

// Call is one request the fake received.
type Call struct {
	Method, Path, Auth string
	// Body is the request body as the fake received it, "" when it carried none.
	Body string
	// Status is what the fake answered.
	Status int
}

// DefaultProject is the team project the fake records a pushed batch in.
const DefaultProject = "team-project"

// Batch is one POST .../workstate the fake received: the rows it carried and the
// credential it carried them under.
type Batch struct {
	Auth    string            `json:"-"`
	Project string            `json:"-"` // the project the fake answered it recorded the batch in
	Items   []json.RawMessage `json:"items"`
	Ledger  []json.RawMessage `json:"ledger"`
	Outputs []Output          `json:"outputs"`
}

// Output is one run-output document of a Batch.
type Output struct {
	Execution string `json:"execution"`
	Name      string `json:"name"`
	Body      string `json:"body"`
}

// Refusal is a server refusal of one secret read: its error code and reason.
type Refusal struct{ Code, Reason string }

// Node is the fake. Create it with NewNode.
type Node struct {
	// URL is the base URL to hand to the node as its satelle.dev.
	URL string
	// Secrets are the values the node is granted, by key. A key not in it is
	// refused: not declared, or not granted.
	Secrets map[string]string
	// ETag, when set, replaces the definition's real ETag, and OmitVersion leaves
	// the version header off: a server that does not keep its contract.
	ETag        string
	OmitVersion bool
	// Run is what GET .../run answers, as the JSON of the server's run descriptor;
	// nil answers {"mode":"node"}, a node started by hand.
	Run any
	// LostAfterSeconds is the window every PUT .../status reply reports; 0 reports
	// none.
	LostAfterSeconds int
	// Project is the team project POST .../workstate answers that it recorded the
	// batch in; empty answers DefaultProject.
	Project string
	// RefuseFindings answers 400 to a PUT .../status that carries findings: a
	// server that decodes the heartbeat strictly and predates the field.
	RefuseFindings bool
	// SecretExpiry is the expires_at a granted key's read answers with, by key,
	// as satelle.dev answers for a value it minted for the run; a key not in it
	// answers none.
	SecretExpiry map[string]time.Time
	// Refuse answers a read of a key with 403 and the server's own code and reason,
	// before Secrets is consulted.
	Refuse map[string]Refusal
	// OnEnd, when set, runs as POST .../end is answered, before the token is
	// ended: what else the server ends with the run (a minted installation token).
	OnEnd func()
	// Chains is each story's hosted chain, by story id, oldest first: ledger rows
	// in the workstate push encoding (id, story_id, base_id, …). A test seeds it
	// before the node asks. GET .../stories/{id}/chain serves it, and POST
	// .../workstate judges each ledger row's base_id against its story's head here
	// and appends the rows it accepts. A story not in it has an empty chain.
	Chains map[string][]json.RawMessage
	// NoChainRoute answers GET .../stories/{id}/chain with 404: a server that
	// predates the route.
	NoChainRoute bool
	// OnAccept, when set, is called with each ledger row a POST .../workstate
	// accepted, in batch order, after it is answered: where else the server
	// records it (a test's hosted chain the local machine pulls from).
	OnAccept func(story string, row json.RawMessage)

	srv     *httptest.Server
	id      string
	bundle  []byte
	version string

	mu      sync.Mutex
	token   string
	revoked bool
	calls   []Call
	status  []map[string]string
	pushed  []Batch

	channelKey []byte
}

// NewNode starts a fake for node id whose token is token, serving bundle as the
// stored definition at version, for the life of t.
func NewNode(t testing.TB, id, token string, bundle []byte, version string) *Node {
	t.Helper()
	n := &Node{id: id, token: token, bundle: bundle, version: version, Secrets: map[string]string{}}
	n.srv = httptest.NewServer(http.HandlerFunc(n.serve))
	n.URL = n.srv.URL
	t.Cleanup(n.srv.Close)
	return n
}

// Client is an HTTP client for the fake's own server.
func (n *Node) Client() *http.Client { return n.srv.Client() }

// ID is the node id the fake serves.
func (n *Node) ID() string { return n.id }

// Token is the node token the fake accepts.
func (n *Node) Token() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.token
}

// SetToken replaces the token the fake accepts, for a test whose token is minted
// after the fake starts.
func (n *Node) SetToken(token string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.token = token
}

// Revoke ends the node's token, as the server does when it ends the node or an
// admin revokes it: every later call is a 401.
func (n *Node) Revoke() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.revoked = true
}

// Calls is every request received, in order.
func (n *Node) Calls() []Call {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Call(nil), n.calls...)
}

// ChannelKey is the key the node published with PUT .../channel-key, nil until it
// does; it is what the starter's GET of the node would report as public_key.
func (n *Node) ChannelKey() []byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]byte(nil), n.channelKey...)
}

// Statuses is the body of every PUT .../status received, in order.
func (n *Node) Statuses() []map[string]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]map[string]string(nil), n.status...)
}

// Pushed is every batch received on POST .../workstate, in order.
func (n *Node) Pushed() []Batch {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Batch(nil), n.pushed...)
}

// SeedChain appends rows to the hosted chain of story, as events another machine
// pushed there.
func (n *Node) SeedChain(story string, rows ...json.RawMessage) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.Chains == nil {
		n.Chains = map[string][]json.RawMessage{}
	}
	n.Chains[story] = append(n.Chains[story], rows...)
}

// Chain is the hosted chain of story now: what was seeded and what pushes added.
func (n *Node) Chain(story string) []json.RawMessage {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]json.RawMessage(nil), n.Chains[story]...)
}

// chainRow is what the fake reads of a ledger row to keep its chain.
type chainRow struct {
	ID           string `json:"id"`
	StoryID      string `json:"story_id"`
	BaseID       string `json:"base_id"`
	WorkflowHash string `json:"workflow_hash"`
}

// headOf is the latest id of a chain, "" when it is empty.
func headOf(chain []json.RawMessage) string {
	if len(chain) == 0 {
		return ""
	}
	var r chainRow
	_ = json.Unmarshal(chain[len(chain)-1], &r)
	return r.ID
}

// serveChain answers GET .../stories/{id}/chain from Chains.
func (n *Node) serveChain(w http.ResponseWriter, story string) int {
	if n.NoChainRoute {
		return reply(w, http.StatusNotFound, `{"error":"not_found"}`)
	}
	chain := n.Chain(story)
	type event struct {
		ID              string          `json:"id"`
		BaseID          string          `json:"base_id"`
		WorkflowVersion string          `json:"workflow_version,omitempty"`
		Payload         json.RawMessage `json:"payload"`
	}
	events := make([]event, 0, len(chain))
	for _, raw := range chain {
		var r chainRow
		_ = json.Unmarshal(raw, &r)
		events = append(events, event{ID: r.ID, BaseID: r.BaseID, WorkflowVersion: r.WorkflowHash, Payload: raw})
	}
	out, _ := json.Marshal(map[string]any{"events": events, "latest": headOf(chain)})
	return reply(w, http.StatusOK, string(out))
}

// acceptLedger judges a batch's ledger rows in order against their stories'
// heads, as satelle.dev does: a row whose base is not its story's head (the
// chain's latest event, or a row earlier in the batch) refuses the whole batch
// with the server's stale-base text. A row the chain already holds is taken as
// it is. It appends the batch's rows when every one is accepted and returns them.
func (n *Node) acceptLedger(rows []json.RawMessage) (accepted []chainRow, raws []json.RawMessage, refusal string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	staged := map[string][]json.RawMessage{}
	chainOf := func(story string) []json.RawMessage {
		if c, ok := staged[story]; ok {
			return c
		}
		return n.Chains[story]
	}
	for _, raw := range rows {
		var r chainRow
		if json.Unmarshal(raw, &r) != nil || r.StoryID == "" {
			continue
		}
		chain := chainOf(r.StoryID)
		if holds(chain, r.ID) {
			continue
		}
		if head := headOf(chain); r.BaseID != head {
			if head == "" {
				head = "empty (no event yet)"
			}
			return nil, nil, "stale base for " + r.StoryID + ": head is " + head
		}
		staged[r.StoryID] = append(append([]json.RawMessage(nil), chain...), raw)
		accepted, raws = append(accepted, r), append(raws, raw)
	}
	if n.Chains == nil {
		n.Chains = map[string][]json.RawMessage{}
	}
	for story, chain := range staged {
		n.Chains[story] = chain
	}
	return accepted, raws, ""
}

// holds reports whether chain has a row with id.
func holds(chain []json.RawMessage, id string) bool {
	for _, raw := range chain {
		var r chainRow
		if json.Unmarshal(raw, &r) == nil && r.ID == id {
			return true
		}
	}
	return false
}

// Count is how many requests ended with tail (for example "/end" or
// "/secrets/KEY") under the node's own id.
func (n *Node) Count(method, tail string) int {
	count := 0
	for _, c := range n.Calls() {
		if c.Method == method && c.Path == "/api/v1/remote-nodes/"+n.id+tail {
			count++
		}
	}
	return count
}

func (n *Node) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	status := n.answer(w, r)
	n.mu.Lock()
	n.calls = append(n.calls, Call{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: string(body), Status: status})
	n.mu.Unlock()
}

func (n *Node) answer(w http.ResponseWriter, r *http.Request) int {
	prefix := "/api/v1/remote-nodes/" + n.id + "/"
	tail, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok {
		return reply(w, http.StatusNotFound, `{"error":"not_found"}`)
	}
	n.mu.Lock()
	good := !n.revoked && r.Header.Get("Authorization") == "Bearer "+n.token
	n.mu.Unlock()
	if !good {
		return reply(w, http.StatusUnauthorized, `{"error":"invalid_token"}`)
	}
	switch {
	case r.Method == http.MethodGet && tail == "definition":
		sum := sha256.Sum256(n.bundle)
		etag := hex.EncodeToString(sum[:])
		if n.ETag != "" {
			etag = n.ETag
		}
		w.Header().Set("ETag", `"`+etag+`"`)
		if !n.OmitVersion {
			w.Header().Set("X-Satelle-Definition-Version", n.version)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(n.bundle)
		return http.StatusOK
	case r.Method == http.MethodPost && strings.HasPrefix(tail, "secrets/"):
		key := strings.TrimPrefix(tail, "secrets/")
		if ref, refused := n.Refuse[key]; refused {
			raw, _ := json.Marshal(map[string]string{"error": ref.Code, "reason": ref.Reason})
			return reply(w, http.StatusForbidden, string(raw))
		}
		value, granted := n.Secrets[key]
		if !granted {
			return reply(w, http.StatusForbidden, `{"error":"secret_refused","reason":"the key is not declared by this definition or not granted to it"}`)
		}
		out := map[string]string{"value": value}
		if exp, ok := n.SecretExpiry[key]; ok {
			out["expires_at"] = exp.UTC().Format(time.RFC3339)
		}
		raw, _ := json.Marshal(out)
		return reply(w, http.StatusOK, string(raw))
	case r.Method == http.MethodGet && tail == "run":
		run := n.Run
		if run == nil {
			run = map[string]string{"mode": "node"}
		}
		raw, _ := json.Marshal(run)
		return reply(w, http.StatusOK, string(raw))
	case r.Method == http.MethodPut && tail == "channel-key":
		// Write-once: the second write is refused, as the server does.
		var in struct {
			PublicKey string `json:"public_key"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			return reply(w, http.StatusBadRequest, `{"error":"invalid_body"}`)
		}
		key, derr := base64.StdEncoding.DecodeString(in.PublicKey)
		if derr != nil || len(key) == 0 {
			return reply(w, http.StatusBadRequest, `{"error":"invalid_public_key"}`)
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.channelKey != nil {
			return reply(w, http.StatusConflict, `{"error":"already_published"}`)
		}
		n.channelKey = key
		return reply(w, http.StatusNoContent, ``)
	case r.Method == http.MethodPut && tail == "status":
		var in map[string]string
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			return reply(w, http.StatusBadRequest, `{"error":"invalid_body"}`)
		}
		if n.RefuseFindings && in["findings"] != "" {
			return reply(w, http.StatusBadRequest, `{"error":"invalid_body","reason":"unknown field findings"}`)
		}
		n.mu.Lock()
		n.status = append(n.status, in)
		n.mu.Unlock()
		raw, _ := json.Marshal(map[string]int{"lost_after_seconds": n.LostAfterSeconds})
		return reply(w, http.StatusOK, string(raw))
	case r.Method == http.MethodPost && tail == "workstate":
		in := Batch{Auth: r.Header.Get("Authorization")}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			return reply(w, http.StatusBadRequest, `{"error":"invalid_body"}`)
		}
		accepted, rows, refusal := n.acceptLedger(in.Ledger)
		if refusal != "" {
			raw, _ := json.Marshal(map[string]string{"error": "stale_base", "reason": refusal})
			return reply(w, http.StatusConflict, string(raw))
		}
		project := n.Project
		if project == "" {
			project = DefaultProject
		}
		in.Project = project
		n.mu.Lock()
		n.pushed = append(n.pushed, in)
		n.mu.Unlock()
		raw, _ := json.Marshal(map[string]any{"project": project, "items": len(in.Items), "ledger": len(in.Ledger), "outputs": len(in.Outputs)})
		status := reply(w, http.StatusOK, string(raw))
		if n.OnAccept != nil {
			for i, r := range accepted {
				n.OnAccept(r.StoryID, rows[i])
			}
		}
		return status
	case r.Method == http.MethodGet && strings.HasPrefix(tail, "stories/") && strings.HasSuffix(tail, "/chain"):
		return n.serveChain(w, strings.TrimSuffix(strings.TrimPrefix(tail, "stories/"), "/chain"))
	case r.Method == http.MethodPost && tail == "end":
		if n.OnEnd != nil {
			n.OnEnd()
		}
		n.Revoke()
		w.WriteHeader(http.StatusNoContent)
		return http.StatusNoContent
	}
	return reply(w, http.StatusNotFound, `{"error":"not_found"}`)
}

func reply(w http.ResponseWriter, status int, body string) int {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
	return status
}
