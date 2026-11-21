package cli

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

func visibilityFixture(t *testing.T) *fakeserver.Server {
	t.Helper()
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	bundle, _ := fakeserver.PackBundle("review", "1.2.3", []fakeserver.PackFile{{Path: "rev/SKILL.md", Body: skillBody("rev")}})
	srv.SeedPack("ws_1/review", "1.2.3", bundle)
	srv.SetPackAdmins("ws_1", testToken)
	return srv
}

func setVisibility(t *testing.T, srv *fakeserver.Server, ref string, flags ...string) (string, error) {
	t.Helper()
	return run(t, "", append([]string{"pack", "visibility", ref, "--server", srv.URL}, flags...)...)
}

func readPack(t *testing.T, srv *fakeserver.Server) *satellepb.Pack {
	t.Helper()
	c, err := hosted.Dial(srv.URL, hosted.NodeTokenStore{Server: srv.URL, Token: testToken}, hosted.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got, err := c.Packs.GetPack(context.Background(), &satellepb.GetPackRequest{Ref: "ws_1/review", Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	return got.GetPack()
}

func lastVisibilityRequest(t *testing.T, srv *fakeserver.Server) *satellepb.SetPackVisibilityRequest {
	t.Helper()
	reqs := srv.VisibilityRequests()
	if len(reqs) == 0 {
		t.Fatal("no SetPackVisibility request reached the server")
	}
	return reqs[len(reqs)-1]
}

func TestPackVisibilitySetsPublic(t *testing.T) {
	srv := visibilityFixture(t)

	out, err := setVisibility(t, srv, "ws_1/review", "--visibility", "public")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ws_1/review is now public"; strings.TrimSpace(out) != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
	reqs := srv.VisibilityRequests()
	if len(reqs) != 1 {
		t.Fatalf("sent %d request(s), want 1", len(reqs))
	}
	if r := reqs[0]; r.GetRef() != "ws_1/review" || r.GetVisibility() != satellepb.PackVisibility_PACK_VISIBILITY_PUBLIC || len(r.GetSharedWith()) != 0 {
		t.Fatalf("request = %v, want ws_1/review public with none shared", r)
	}
	if p := readPack(t, srv); p.GetVisibility() != satellepb.PackVisibility_PACK_VISIBILITY_PUBLIC {
		t.Fatalf("server holds visibility %v, want public", p.GetVisibility())
	}

	out, err = setVisibility(t, srv, "ws_1/review", "--visibility", "public", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	shared, ok := got["shared_with"].([]any)
	if got["ref"] != "ws_1/review" || got["owner_workspace_id"] != "ws_1" || got["visibility"] != "public" || !ok || len(shared) != 0 {
		t.Fatalf("json = %v, want ref ws_1/review, owner ws_1, public and an empty shared_with array", got)
	}
}

func TestPackVisibilitySendsSharedWith(t *testing.T) {
	srv := visibilityFixture(t)

	for _, c := range []struct {
		flags  []string
		vis    satellepb.PackVisibility
		shared []string
		out    string
	}{
		{[]string{"--visibility", "shared", "--share-with", "ws_2", "--share-with", "ws_3"}, satellepb.PackVisibility_PACK_VISIBILITY_SHARED, []string{"ws_2", "ws_3"}, "ws_1/review is now shared (shared with ws_2, ws_3)"},
		{[]string{"--visibility", "shared", "--share-with", "ws_2"}, satellepb.PackVisibility_PACK_VISIBILITY_SHARED, []string{"ws_2"}, "ws_1/review is now shared (shared with ws_2)"},
		{[]string{"--visibility", "workspace"}, satellepb.PackVisibility_PACK_VISIBILITY_WORKSPACE, nil, "ws_1/review is now workspace"},
	} {
		out, err := setVisibility(t, srv, "ws_1/review", c.flags...)
		if err != nil {
			t.Fatalf("%v: %v", c.flags, err)
		}
		if strings.TrimSpace(out) != c.out {
			t.Errorf("%v: output = %q, want %q", c.flags, out, c.out)
		}
		req := lastVisibilityRequest(t, srv)
		if req.GetVisibility() != c.vis || !slices.Equal(req.GetSharedWith(), c.shared) {
			t.Errorf("%v: request visibility %v shared with %v, want %v %v", c.flags, req.GetVisibility(), req.GetSharedWith(), c.vis, c.shared)
		}
		if p := readPack(t, srv); p.GetVisibility() != c.vis || !slices.Equal(p.GetSharedWith(), c.shared) {
			t.Errorf("%v: server holds %v %v, want %v %v", c.flags, p.GetVisibility(), p.GetSharedWith(), c.vis, c.shared)
		}
	}
}

func TestPackVisibilityRefusesFlags(t *testing.T) {
	srv := visibilityFixture(t)

	for _, c := range []struct {
		ref   string
		flags []string
		names string
	}{
		{"ws_1/review", nil, "--visibility"},
		{"ws_1/review", []string{"--visibility", "private"}, "--visibility"},
		{"ws_1/review", []string{"--visibility", "public", "--share-with", "ws_2"}, "--share-with"},
		{"ws_1/review", []string{"--visibility", "workspace", "--share-with", "ws_2"}, "--share-with"},
		{"ws_1/review", []string{"--visibility", "shared"}, "--share-with"},
		{"ws_1/review", []string{"--visibility", "shared", "--share-with", " "}, "--share-with"},
		{"review", []string{"--visibility", "public"}, "<owner workspace id>/<name>"},
		{"ws_1/", []string{"--visibility", "public"}, "<owner workspace id>/<name>"},
	} {
		_, err := setVisibility(t, srv, c.ref, c.flags...)
		if err == nil || !strings.Contains(err.Error(), c.names) {
			t.Errorf("%s %v: err = %v, want it to name %s", c.ref, c.flags, err, c.names)
		}
	}
	if n := len(srv.VisibilityRequests()); n != 0 {
		t.Errorf("refused changes reached the server: %d request(s)", n)
	}
}

func TestPackVisibilityRefusals(t *testing.T) {
	t.Run("permission denied", func(t *testing.T) {
		srv := visibilityFixture(t)
		srv.SetPackAdmins("ws_1", "someone-else")
		_, err := setVisibility(t, srv, "ws_1/review", "--visibility", "public")
		wantErr(t, err, "only admins of workspace ws_1", "ask an admin of ws_1", "was not changed")
		if n := len(srv.VisibilityRequests()); n != 1 {
			t.Errorf("sent %d request(s), want 1", n)
		}
		if p := readPack(t, srv); p.GetVisibility() != satellepb.PackVisibility_PACK_VISIBILITY_UNSPECIFIED {
			t.Errorf("refused change altered the visibility: %v", p.GetVisibility())
		}
	})
	t.Run("not found", func(t *testing.T) {
		srv := visibilityFixture(t)
		_, err := setVisibility(t, srv, "ws_9/missing", "--visibility", "public")
		wantErr(t, err, "ws_9/missing was not found", "member of its owner workspace ws_9", "satelle whoami", "was not changed")
	})
	t.Run("unauthenticated", func(t *testing.T) {
		srv := visibilityFixture(t)
		srv.RevokeToken(testToken)
		_, err := setVisibility(t, srv, "ws_1/review", "--visibility", "public")
		wantErr(t, err, "pack visibility:", "session token refused", "mint a new one", "was not changed")
		if !errors.Is(err, hosted.ErrLoginRequired) {
			t.Errorf("err = %v, want it to wrap hosted.ErrLoginRequired", err)
		}
	})
}

func wantErr(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want an error naming %q", parts)
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("err = %q, want it to contain %q", err, p)
		}
	}
}
