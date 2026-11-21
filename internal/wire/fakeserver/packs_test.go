package fakeserver_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

func TestPackService(t *testing.T) {
	ctx := context.Background()
	srv := fakeserver.Start(t, "tok-ada", "tok-other")

	ada, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: "tok-ada"}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ada.Close() })

	other, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: "tok-other"}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })

	pub, _ := fakeserver.PackBundle("pub", "1.0.0", []fakeserver.PackFile{{Path: "SKILL.md", Body: []byte("hello")}})
	srv.SeedPack("acme/pub", "1.0.0", pub)

	for name, client := range map[string]*hosted.Client{"ada": ada, "other": other} {
		got, err := client.Packs.FetchPack(ctx, &satellepb.GetPackRequest{Ref: "acme/pub", Version: "1.0.0"})
		if err != nil {
			t.Fatalf("%s fetch public pack: %v", name, err)
		}
		if !proto.Equal(got, pub) {
			t.Errorf("%s got %v, want %v", name, got, pub)
		}
	}

	priv, _ := fakeserver.PackBundle("priv", "2.0.0", []fakeserver.PackFile{{Path: "SKILL.md", Body: []byte("secret")}})
	srv.SeedPack("acme/priv", "2.0.0", priv, "tok-other")

	got, err := other.Packs.FetchPack(ctx, &satellepb.GetPackRequest{Ref: "acme/priv", Version: "2.0.0"})
	if err != nil {
		t.Fatalf("authorized reader fetch: %v", err)
	}
	if !proto.Equal(got, priv) {
		t.Errorf("authorized reader got %v, want %v", got, priv)
	}

	_, err = ada.Packs.FetchPack(ctx, &satellepb.GetPackRequest{Ref: "acme/priv", Version: "2.0.0"})
	if status.Code(err) != codes.NotFound || !hosted.IsNotFound(err) {
		t.Fatalf("unreadable pack: got %v, want NotFound", err)
	}

	_, err = ada.Packs.FetchPack(ctx, &satellepb.GetPackRequest{Ref: "acme/missing", Version: "1.0.0"})
	if status.Code(err) != codes.NotFound || !hosted.IsNotFound(err) {
		t.Fatalf("missing pack: got %v, want NotFound", err)
	}

	_, err = ada.Packs.FetchPack(ctx, &satellepb.GetPackRequest{Ref: "acme/pub", Version: "9.9.9"})
	if status.Code(err) != codes.NotFound || !hosted.IsNotFound(err) {
		t.Fatalf("missing version: got %v, want NotFound", err)
	}
}
