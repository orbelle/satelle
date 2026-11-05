package remotenode

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remotechan"
)

// A performer-mode node (sty_7028d996) publishes its channel key to satelle.dev,
// and the starter pairs with it on the session the server minted, admitting only
// that key.

// The key goes out on PublishChannelKey under the node token and is
// write-once: a second write is refused and returned.
func TestPublishChannelKeyUsesTheNodeService(t *testing.T) {
	srv, _, client, _ := newFake(t)
	id, err := remotechan.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PublishChannelKey(context.Background(), id.Public()); err != nil {
		t.Fatalf("PublishChannelKey: %v", err)
	}
	calls := srv.Calls()
	if len(calls) != 1 || calls[0].Method != "/satelle.v1.NodeService/PublishChannelKey" || calls[0].Auth != testNodeToken {
		t.Fatalf("calls = %+v, want one PublishChannelKey under the node token", calls)
	}
	if !bytes.Equal(srv.ChannelKey(testNodeID), id.Public()) {
		t.Errorf("published key differs from the node's public key")
	}
	err = client.PublishChannelKey(context.Background(), id.Public())
	if err == nil || !strings.Contains(err.Error(), "already_published") {
		t.Errorf("second publish = %v, want the server's write-once refusal", err)
	}
}

// A revoked token is the token's refusal, as for every other node call.
func TestPublishChannelKeyWithARevokedToken(t *testing.T) {
	srv, _, client, _ := newFake(t)
	srv.RevokeNode(testNodeID)
	id, _ := remotechan.NewIdentity()
	err := client.PublishChannelKey(context.Background(), id.Public())
	if err == nil || !strings.Contains(err.Error(), "refused or revoked") {
		t.Fatalf("PublishChannelKey = %v, want the token's refusal", err)
	}
}

// The whole performer leg on the stand-in relay and server: the node publishes its
// key; the starter attaches to the server-minted session pinned to the published
// key; an AgentJob naming a binding, a system prompt and a payload reaches the
// node and is answered by it.
func TestPerformerNodeIsPairedByItsPublishedKey(t *testing.T) {
	r := newRig(t)
	srv, _, nc, _ := newFake(t)
	id, err := remotechan.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(t)

	node := NewNode()
	node.Handle(AgentJob, func(_ context.Context, body json.RawMessage) (json.RawMessage, error) {
		var req AgentRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return json.Marshal(AgentReply{Text: req.Binding + "|" + req.SystemPrompt + "|" + req.Text})
	})

	if err := nc.PublishChannelKey(ctx, id.Public()); err != nil {
		t.Fatal(err)
	}
	type attached struct {
		s   *remotechan.Session
		err error
	}
	att := make(chan attached, 1)
	go func() {
		s, err := remotechan.AttachServerSession(ctx, r.client, r.relay.URL(), testNodeID,
			func(context.Context) ([]byte, error) { return srv.ChannelKey(testNodeID), nil }, r.chanOpts...)
		att <- attached{s, err}
	}()
	eventually(t, "the starter on the relay", func() bool { return r.relay.Upgrades() >= 1 })
	go func() {
		sess, err := remotechan.Join(ctx, r.relay.URL(), testNodeID, id, "the-node-bearer",
			remotechan.WithHTTPClient(r.relay.Client()), remotechan.WithClock(r.clock))
		if err == nil {
			defer sess.Close()
			_ = node.Serve(ctx, sess, ready("v1"))
		}
	}()

	a := <-att
	if a.err != nil {
		t.Fatalf("AttachServerSession: %v", a.err)
	}
	run, err := Attach(ctx, a.s, 20*time.Second)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	t.Cleanup(func() { _ = run.End() })

	body, err := run.Request(ctx, AgentJob, AgentRequest{Binding: "coder", SystemPrompt: "SYSTEM", Text: "PAYLOAD"})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	reply, err := DecodeAgentReply(body)
	if err != nil || reply.Text != "coder|SYSTEM|PAYLOAD" {
		t.Fatalf("reply = %+v, %v; want the node's answer to the named binding, prompt and payload", reply, err)
	}
}

// An AgentRequest with no binding or prompt is the old message: the new fields do
// not appear on the wire.
func TestAgentRequestOmitsUnsetStepFields(t *testing.T) {
	raw, err := json.Marshal(AgentRequest{Text: "hello"})
	if err != nil || string(raw) != `{"text":"hello"}` {
		t.Fatalf("marshalled %s, %v; want only text", raw, err)
	}
}
