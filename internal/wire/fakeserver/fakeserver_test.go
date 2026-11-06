package fakeserver_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wire"
	"github.com/bobmcallan/satelle/internal/wire/chainpb"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

func TestChainAppendIsCompareAndAppend(t *testing.T) {
	srv := fakeserver.Start(t, "tok")
	conn, err := grpc.NewClient(srv.URL[len("http://"):], grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer tok")
	if _, err := satellepb.NewProjectServiceClient(conn).CreateProject(ctx, &satellepb.CreateProjectRequest{Slug: "p"}); err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e1, _ := led.Append("it_1", "item.created", "ada", map[string]string{"title": "t"})
	e2, _ := led.Append("it_1", "item.updated", "ada", nil)
	chain := chainpb.NewChainClient(conn)

	bad := wire.ToProto(e1)
	bad.Kind = "tampered"
	_, err = chain.Append(ctx, &chainpb.AppendRequest{Project: "p", ItemId: "it_1", Events: []*chainpb.Event{bad}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a tampered event = %v, want InvalidArgument", err)
	}
	if _, err := chain.Append(ctx, &chainpb.AppendRequest{Project: "p", ItemId: "it_1", Events: wire.ToProtos([]ledger.Event{e1, e2})}); err != nil {
		t.Fatal(err)
	}
	// A second append from the empty head is stale and carries what is missing.
	_, err = chain.Append(ctx, &chainpb.AppendRequest{Project: "p", ItemId: "it_1", Events: wire.ToProtos([]ledger.Event{e1})})
	st := status.Convert(err)
	if st.Code() != codes.FailedPrecondition || len(st.Details()) != 1 {
		t.Fatalf("stale append = %v", err)
	}
	sh := st.Details()[0].(*chainpb.StaleHead)
	if sh.GetHeadHash() != e2.Hash || sh.GetMissingCount() != 2 || len(sh.GetMissing()) != 2 {
		t.Errorf("StaleHead = %v", sh)
	}
	heads, err := chain.Heads(ctx, &chainpb.HeadsRequest{Project: "p", PageSize: 1})
	if err != nil || len(heads.GetHeads()) != 1 || heads.GetHeads()[0].GetHeadHash() != e2.Hash || heads.GetHeads()[0].GetSeq() != 2 {
		t.Errorf("Heads = %v, %v", heads, err)
	}
	read, err := chain.Read(ctx, &chainpb.ReadRequest{Project: "p", ItemId: "it_1", AfterHash: e1.Hash})
	if err != nil || len(read.GetEvents()) != 1 || wire.FromProto(read.GetEvents()[0]).Hash != e2.Hash {
		t.Errorf("Read after e1 = %v, %v", read, err)
	}
	if err := ledger.VerifyChain("it_1", wire.FromProtos(sh.GetMissing())); err != nil {
		t.Errorf("the round-tripped chain does not verify: %v", err)
	}

	unauth := context.Background()
	if _, err := chain.Heads(unauth, &chainpb.HeadsRequest{Project: "p"}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("no bearer = %v, want Unauthenticated", err)
	}
}

// A seeded item is a verifiable server-born chain: one item.created with no
// workflow or status, ids in the server's forms, listed in Heads.
func TestSeedItem(t *testing.T) {
	srv := fakeserver.Start(t, "tok")
	c, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: "tok"}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p, err := c.Projects.CreateProject(context.Background(), &satellepb.CreateProjectRequest{Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	id := srv.SeedItem("acme", map[string]any{"kind": "story", "title": "born on the server"})
	if !regexp.MustCompile(`^sto_[0-9a-f]{8}$`).MatchString(id) {
		t.Fatalf("item id %q", id)
	}
	evs := wire.FromProtos(srv.Events(p.GetId(), id))
	if len(evs) != 1 || !regexp.MustCompile(`^evt_[0-9a-f]{16}$`).MatchString(evs[0].ID) || evs[0].Kind != "item.created" {
		t.Fatalf("events %+v", evs)
	}
	if err := ledger.VerifyChain(id, evs); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(evs[0].Payload), "workflow") || strings.Contains(string(evs[0].Payload), "status") {
		t.Fatalf("payload names process: %s", evs[0].Payload)
	}
	heads, err := c.Chain.Heads(context.Background(), &chainpb.HeadsRequest{Project: p.GetId()})
	if err != nil || len(heads.GetHeads()) == 0 {
		t.Fatalf("heads %v %v", heads, err)
	}
}
