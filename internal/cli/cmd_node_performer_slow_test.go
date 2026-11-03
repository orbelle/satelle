//go:build clislow && !windows

package cli

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/remotechan"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
)

// The leg end to end through the product's own join: the node publishes its key,
// and the starter — attached to the server-minted session with only what satelle.dev
// reports — is paired with it and gets the named binding's answer.
func TestSlowPerformerLegPairsThroughThePublishedKey(t *testing.T) {
	r := newNodeRig(t)
	r.fakeSetup = func(f *devtest.Node) { f.Run = map[string]string{"mode": remotenode.ModePerformer} }
	const session, token = "nod_perf1", "node-token-perf"
	fake := r.serve(session, token, r.encode())

	client := hosted.NewClient("https://satelle.test", &nodeTestStore{cred: hosted.Credential{ServerURL: "https://satelle.test", AccessToken: "access-1"}}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// The starter attaches first, as it does in life: it has the session id from
	// the nodes route before the node has even booted, and polls satelle.dev for the
	// key. (The stand-in relay drops a frame sent while its peer is absent.)
	type attached struct {
		s   *remotechan.Session
		err error
	}
	att := make(chan attached, 1)
	go func() {
		s, err := remotechan.AttachServerSession(ctx, client, r.relay.URL(), session,
			func(context.Context) ([]byte, error) { return fake.ChannelKey(), nil },
			remotechan.WithHTTPClient(r.relay.Client()))
		att <- attached{s, err}
	}()
	for deadline := time.Now().Add(10 * time.Second); r.relay.Upgrades() < 1; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the starter never reached the relay")
		}
	}

	joined := make(chan error, 1)
	go func() { joined <- r.join(io.Discard, r.relay.URL(), session, token) }()

	a := <-att
	if a.err != nil {
		t.Fatalf("AttachServerSession: %v\nnode log:\n%s", a.err, r.log)
	}
	run, err := remotenode.Attach(ctx, a.s, 60*time.Second)
	if err != nil {
		t.Fatalf("Attach: %v\nnode log:\n%s", err, r.log)
	}
	body, err := run.Request(ctx, remotenode.AgentJob, remotenode.AgentRequest{Binding: "worker", SystemPrompt: "SP", Text: "pair me"})
	if err != nil {
		t.Fatalf("agent job: %v\nnode log:\n%s", err, r.log)
	}
	if reply, derr := remotenode.DecodeAgentReply(body); derr != nil || !strings.Contains(reply.Text, "pair me") {
		t.Errorf("reply = %+v, %v", reply, derr)
	}
	_ = run.End()
	if err := waitJoin(t, joined); err != nil {
		t.Errorf("node join ended with %v\nnode log:\n%s", err, r.log)
	}
}
