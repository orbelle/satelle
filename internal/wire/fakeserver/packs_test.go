package fakeserver_test

import (
	"context"
	"slices"
	"strings"
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

	srv.SetPackAdmins("ws_1", "tok-ada")
	made, _ := fakeserver.PackBundle("made", "1.0.0", []fakeserver.PackFile{{Path: "made/SKILL.md", Body: []byte("made")}})
	req := &satellepb.PublishPackRequest{OwnerWorkspaceId: "ws_1", Manifest: made.GetVersion().GetManifest(), Files: made.GetFiles(),
		Visibility: satellepb.PackVisibility_PACK_VISIBILITY_PUBLIC}
	if _, err := other.Packs.PublishPack(ctx, req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-admin publish: got %v, want PermissionDenied", err)
	}
	pv, err := ada.Packs.PublishPack(ctx, req)
	if err != nil {
		t.Fatalf("admin publish: %v", err)
	}
	if pv.GetPack().GetRef() != "ws_1/made" || pv.GetPack().GetOwnerWorkspaceId() != "ws_1" {
		t.Errorf("published pack = %v", pv.GetPack())
	}
	if _, err := ada.Packs.PublishPack(ctx, req); err != nil {
		t.Errorf("same digest republish: %v", err)
	}
	got, err = other.Packs.FetchPack(ctx, &satellepb.GetPackRequest{Ref: "ws_1/made", Version: "1.0.0"})
	if err != nil {
		t.Fatalf("fetch published: %v", err)
	}
	if !proto.Equal(got.GetVersion().GetManifest(), made.GetVersion().GetManifest()) || !proto.Equal(got.GetFiles()[0], made.GetFiles()[0]) {
		t.Errorf("fetched %v, want %v", got, made)
	}
	changed, _ := fakeserver.PackBundle("made", "1.0.0", []fakeserver.PackFile{{Path: "made/SKILL.md", Body: []byte("changed")}})
	_, err = ada.Packs.PublishPack(ctx, &satellepb.PublishPackRequest{OwnerWorkspaceId: "ws_1", Manifest: changed.GetVersion().GetManifest(), Files: changed.GetFiles(),
		Visibility: satellepb.PackVisibility_PACK_VISIBILITY_PUBLIC})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("changed version: got %v, want AlreadyExists", err)
	}
	if n := len(srv.Published()); n != 4 {
		t.Errorf("recorded %d publish request(s), want 4", n)
	}
}

func TestPublishPackRefusesUnspecifiedVisibility(t *testing.T) {
	ctx := context.Background()
	srv := fakeserver.Start(t, "tok-ada")
	ada, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: "tok-ada"}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ada.Close() })
	srv.SetPackAdmins("ws_1", "tok-ada")

	made, _ := fakeserver.PackBundle("made", "1.0.0", []fakeserver.PackFile{{Path: "made/SKILL.md", Body: []byte("made")}})
	_, err = ada.Packs.PublishPack(ctx, &satellepb.PublishPackRequest{OwnerWorkspaceId: "ws_1", Manifest: made.GetVersion().GetManifest(), Files: made.GetFiles()})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unspecified visibility: got %v, want InvalidArgument", err)
	}
	if want := `visibility "" is not public, workspace or shared`; !strings.Contains(status.Convert(err).Message(), want) {
		t.Errorf("message = %q, want it to contain %q", status.Convert(err).Message(), want)
	}
	if _, err := ada.Packs.FetchPack(ctx, &satellepb.GetPackRequest{Ref: "ws_1/made", Version: "1.0.0"}); status.Code(err) != codes.NotFound {
		t.Errorf("fetch after refused publish: got %v, want NotFound", err)
	}
}

func TestGetPackNamesLatestVersion(t *testing.T) {
	ctx := context.Background()
	srv := fakeserver.Start(t, "tok-ada", "tok-other")
	ada, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: "tok-ada"}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ada.Close() })

	for _, v := range []string{"1.0.0", "1.1.0", "1.0.0"} {
		b, _ := fakeserver.PackBundle("review", v, []fakeserver.PackFile{{Path: "SKILL.md", Body: []byte(v)}})
		srv.SeedPack("acme/review", v, b)
	}
	got, err := ada.Packs.GetPack(ctx, &satellepb.GetPackRequest{Ref: "acme/review", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	p := got.GetPack()
	if p.GetRef() != "acme/review" || p.GetName() != "review" || p.GetLatestVersion() != "1.1.0" ||
		!slices.Equal(p.GetVersions(), []string{"1.0.0", "1.1.0"}) || got.GetManifest().GetVersion() != "1.0.0" {
		t.Errorf("GetPack = %v", got)
	}

	priv, _ := fakeserver.PackBundle("priv", "2.0.0", []fakeserver.PackFile{{Path: "SKILL.md", Body: []byte("secret")}})
	srv.SeedPack("acme/priv", "2.0.0", priv, "tok-other")
	for _, r := range []*satellepb.GetPackRequest{
		{Ref: "acme/priv", Version: "2.0.0"},
		{Ref: "acme/review", Version: "9.9.9"},
		{Ref: "acme/missing", Version: "1.0.0"},
	} {
		if _, err := ada.Packs.GetPack(ctx, r); !hosted.IsNotFound(err) {
			t.Errorf("GetPack %v: %v, want NotFound", r, err)
		}
	}
}

func TestSetPackVisibility(t *testing.T) {
	ctx := context.Background()
	srv := fakeserver.Start(t, "tok-ada", "tok-other")
	dial := func(tok string) *hosted.Client {
		c, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: tok}, hosted.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	ada, other := dial("tok-ada"), dial("tok-other")
	b, _ := fakeserver.PackBundle("review", "1.0.0", []fakeserver.PackFile{{Path: "SKILL.md", Body: []byte("v1")}})
	srv.SeedPack("ws_1/review", "1.0.0", b)
	srv.SetPackAdmins("ws_1", "tok-ada")

	got, err := ada.Packs.SetPackVisibility(ctx, &satellepb.SetPackVisibilityRequest{
		Ref: "ws_1/review", Visibility: satellepb.PackVisibility_PACK_VISIBILITY_SHARED, SharedWith: []string{"ws_2", "ws_3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetRef() != "ws_1/review" || got.GetOwnerWorkspaceId() != "ws_1" || got.GetName() != "review" ||
		got.GetVisibility() != satellepb.PackVisibility_PACK_VISIBILITY_SHARED || !slices.Equal(got.GetSharedWith(), []string{"ws_2", "ws_3"}) ||
		got.GetLatestVersion() != "1.0.0" {
		t.Errorf("SetPackVisibility = %v", got)
	}
	read, err := ada.Packs.GetPack(ctx, &satellepb.GetPackRequest{Ref: "ws_1/review", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if p := read.GetPack(); p.GetVisibility() != satellepb.PackVisibility_PACK_VISIBILITY_SHARED || !slices.Equal(p.GetSharedWith(), []string{"ws_2", "ws_3"}) {
		t.Errorf("GetPack after change = %v", p)
	}

	for _, c := range []struct {
		client *hosted.Client
		req    *satellepb.SetPackVisibilityRequest
		code   codes.Code
	}{
		{ada, &satellepb.SetPackVisibilityRequest{Ref: "ws_9/missing", Visibility: satellepb.PackVisibility_PACK_VISIBILITY_PUBLIC}, codes.NotFound},
		{ada, &satellepb.SetPackVisibilityRequest{Ref: "noslash", Visibility: satellepb.PackVisibility_PACK_VISIBILITY_PUBLIC}, codes.NotFound},
		{other, &satellepb.SetPackVisibilityRequest{Ref: "ws_1/review", Visibility: satellepb.PackVisibility_PACK_VISIBILITY_PUBLIC}, codes.PermissionDenied},
		{ada, &satellepb.SetPackVisibilityRequest{Ref: "ws_1/review"}, codes.InvalidArgument},
	} {
		if _, err := c.client.Packs.SetPackVisibility(ctx, c.req); status.Code(err) != c.code {
			t.Errorf("%v: got %v, want %v", c.req, err, c.code)
		}
	}
	read, err = ada.Packs.GetPack(ctx, &satellepb.GetPackRequest{Ref: "ws_1/review", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if read.GetPack().GetVisibility() != satellepb.PackVisibility_PACK_VISIBILITY_SHARED {
		t.Errorf("refused change altered the visibility: %v", read.GetPack())
	}
	if n := len(srv.VisibilityRequests()); n != 5 {
		t.Errorf("recorded %d request(s), want 5", n)
	}
}
