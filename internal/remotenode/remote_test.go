package remotenode

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/remotechan"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// A performer node publishes its channel key, joins the relay over gRPC and is
// paired by that key; an AgentJob naming a binding, a system prompt and a
// payload reaches the node and is answered by it.
func TestPerformerNodeIsPairedByItsPublishedKey(t *testing.T) {
	srv, _, nc, _ := newFake(t)
	srv.AllowToken("user-tok")
	local, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: "user-tok"}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
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
	served := make(chan error, 1)
	go func() {
		sess, err := remotechan.Join(ctx, nc.Hosted().Relay, testNodeID, id)
		if err != nil {
			served <- err
			return
		}
		defer sess.Close()
		served <- node.Serve(ctx, sess, Report{Version: "v1"})
	}()
	sess, err := remotechan.Attach(ctx, local.Relay, testNodeID, func(context.Context) ([]byte, error) { return srv.ChannelKey(testNodeID), nil })
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	run, err := AttachRun(ctx, sess, 20*time.Second)
	if err != nil {
		t.Fatalf("AttachRun: %v", err)
	}
	if run.Report.Version != "v1" {
		t.Fatalf("report = %+v", run.Report)
	}
	body, err := run.Request(ctx, AgentJob, AgentRequest{Binding: "coder", SystemPrompt: "SYSTEM", Text: "PAYLOAD"})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	reply, err := DecodeAgentReply(body)
	if err != nil || reply.Text != "coder|SYSTEM|PAYLOAD" {
		t.Fatalf("reply = %+v, %v", reply, err)
	}
	if _, err := run.Request(ctx, "unknown-type", nil); err == nil {
		t.Fatal("an unregistered job type was answered")
	}
	_ = run.End()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the node kept serving after the run ended")
	}
	if _, err := run.Request(ctx, AgentJob, AgentRequest{}); !errors.Is(err, ErrJobLost) {
		t.Fatalf("a request after End = %v", err)
	}
}
