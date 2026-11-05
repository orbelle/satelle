package remotenode

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

const (
	testNodeID    = "nod_a1b2"
	testNodeToken = "node-token-1"
	// secretValue must appear nowhere but the agent's environment.
	secretValue = "sk-ant-oat01-NEVER-WRITTEN-ANYWHERE"
)

func newFake(t *testing.T) (*fakeserver.Server, *fakeserver.Node, *NodeClient, []byte) {
	t.Helper()
	bundle := bundleOf(t, definitionFiles(t))
	srv := fakeserver.Start(t)
	node := &fakeserver.Node{ID: testNodeID, Token: testNodeToken, Bundle: bundle, Version: "v7"}
	srv.AddNode(node)
	return srv, node, nodeClient(t, srv.URL, testNodeID), bundle
}

func nodeClient(t *testing.T, server, id string) *NodeClient {
	t.Helper()
	c, err := NewNodeClient(server, id, testNodeToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func countCalls(srv *fakeserver.Server, method string) int {
	n := 0
	for _, c := range srv.Calls() {
		if strings.HasSuffix(c.Method, "/"+method) {
			n++
		}
	}
	return n
}

// The definition is fetched under the node token, and the bytes are exactly the
// stored bundle with its version.
func TestDefinitionIsFetchedWithTheNodeToken(t *testing.T) {
	srv, _, client, bundle := newFake(t)
	got, version, err := client.Definition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(bundle) || version != "v7" {
		t.Errorf("Definition = %d bytes at %q, want the stored %d bytes at v7", len(got), version, len(bundle))
	}
	calls := srv.Calls()
	if len(calls) != 1 || calls[0].Method != "/satelle.v1.NodeService/GetDefinition" || calls[0].Auth != testNodeToken {
		t.Errorf("calls = %+v", calls)
	}
}

// The run descriptor is read under the node token; a heartbeat carries the
// fields it sets and the reply's lost-after window comes back.
func TestRunAndStatusUseTheNodeService(t *testing.T) {
	srv, node, client, _ := newFake(t)
	node.Run = &satellepb.RunDescriptor{Mode: "node", Project: "acme-team", ItemId: "sty_1", Trigger: "nightly", TriggerSource: "schedule"}
	node.LostAfterSeconds = 90

	d, err := client.Run(context.Background())
	if err != nil || d != (RunDescriptor{Mode: "node", Project: "acme-team", StoryID: "sty_1", Trigger: "nightly", TriggerSource: "schedule"}) {
		t.Fatalf("Run = %+v, %v", d, err)
	}
	reply, err := client.Status(context.Background(), NodeStatus{State: StateRunning, StoryID: "sty_1", RunState: "queued"})
	if err != nil || reply.LostAfterSeconds != 90 {
		t.Fatalf("Status = %+v, %v", reply, err)
	}
	if _, err := client.Status(context.Background(), NodeStatus{State: StateRunning, RunState: "concluded", Outcome: "reject", Findings: "fix it"}); err != nil {
		t.Fatal(err)
	}
	st := srv.NodeStatuses(testNodeID)
	if len(st) != 2 || st[0].RunState != "queued" || st[0].ItemId != "sty_1" || st[0].Verdict != "" ||
		st[1].RunState != "concluded" || st[1].Verdict != "reject" || st[1].Notes != "fix it" {
		t.Errorf("statuses = %v", st)
	}
}

// A bundle that does not hash to its digest is not a definition.
func TestDefinitionRefusesWhatIsNotTheStoredBundle(t *testing.T) {
	_, node, client, _ := newFake(t)
	node.Version = ""
	if _, _, err := client.Definition(context.Background()); err == nil || !strings.Contains(err.Error(), "no version") {
		t.Fatalf("Definition without a version = %v", err)
	}
}

// A secret is read under the node token; a minted value carries its expiry and
// a refusal carries the server's code.
func TestSecretReads(t *testing.T) {
	_, node, client, _ := newFake(t)
	exp := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	node.Secrets["GH_TOKEN"] = "ghs_minted"
	node.Secrets["PLAIN"] = secretValue
	node.SecretExpiry = map[string]time.Time{"GH_TOKEN": exp}
	node.Refuse = map[string][2]string{"OWNER": {SecretRefusedGitHubAppOwnerRun, "owner run"}}

	v, got, err := client.SecretWithExpiry(context.Background(), "GH_TOKEN")
	if err != nil || v != "ghs_minted" || !got.Equal(exp) {
		t.Fatalf("SecretWithExpiry = %q, %v, %v", v, got, err)
	}
	if v, got, err := client.SecretWithExpiry(context.Background(), "PLAIN"); err != nil || v != secretValue || !got.IsZero() {
		t.Errorf("plain = %q, %v, %v", v, got, err)
	}
	_, _, err = client.SecretWithExpiry(context.Background(), "OWNER")
	var refusal *SecretRefusal
	if !errors.As(err, &refusal) || refusal.Code != SecretRefusedGitHubAppOwnerRun {
		t.Errorf("owner-run read = %v", err)
	}
}

// A value is returned in memory and goes nowhere else.
func TestReadingNeverWritesTheValueToDisk(t *testing.T) {
	_, node, client, _ := newFake(t)
	node.Secrets["K"] = secretValue
	dir := t.TempDir()
	t.Chdir(dir)
	if _, err := client.Secret(context.Background(), "K"); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("reading a secret wrote %v", entries)
	}
	if _, err := client.Secret(context.Background(), "NOT-GRANTED"); err == nil || strings.Contains(err.Error(), secretValue) {
		t.Errorf("err = %v", err)
	}
}

func isTokenRefused(err error) bool {
	var tr *TokenRefusedError
	return errors.As(err, &tr)
}

// End sends one call however often it is called, and it ends the token.
func TestEndSendsOneCall(t *testing.T) {
	srv, _, client, _ := newFake(t)
	for range 3 {
		if err := client.End(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := countCalls(srv, "EndNode"); n != 1 {
		t.Errorf("end calls = %d, want 1", n)
	}
	fresh := nodeClient(t, srv.URL, testNodeID)
	if _, _, err := fresh.Definition(context.Background()); !isTokenRefused(err) {
		t.Errorf("Definition after End = %v, want the token refused", err)
	}
}

// A refused token names the call, and nothing further is asked except End.
func TestARefusedTokenStopsEveryLaterCall(t *testing.T) {
	srv, node, client, _ := newFake(t)
	node.Secrets["K"] = "v"
	srv.RevokeNode(testNodeID)

	_, _, err := client.Definition(context.Background())
	var tr *TokenRefusedError
	if !errors.As(err, &tr) || tr.Route != "NodeService.GetDefinition" || !strings.Contains(err.Error(), "refused or revoked") {
		t.Fatalf("Definition = %v, want the token refused on GetDefinition", err)
	}
	if _, err := client.Secret(context.Background(), "K"); !isTokenRefused(err) {
		t.Errorf("Secret after a refusal = %v", err)
	}
	if err := client.End(context.Background()); err != nil {
		t.Errorf("End after a refusal = %v", err)
	}
	if n, m := countCalls(srv, "ReadSecret"), countCalls(srv, "GetDefinition"); n != 0 || m != 1 {
		t.Errorf("secret reads = %d, definition reads = %d; want 0 and 1", n, m)
	}
}

// A call under another node id is NOT_FOUND, not the token's refusal.
func TestACallUnderAnotherIDIsNotFound(t *testing.T) {
	srv, _, _, _ := newFake(t)
	other := nodeClient(t, srv.URL, "nod_other")
	_, _, err := other.Definition(context.Background())
	if err == nil || !strings.Contains(err.Error(), "NotFound") || isTokenRefused(err) {
		t.Fatalf("Definition under another id = %v, want NotFound", err)
	}
}
