package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

func publishFixture(t *testing.T, dir string) []fakeserver.PackFile {
	t.Helper()
	files := []fakeserver.PackFile{
		{Path: "rev/SKILL.md", Body: skillBody("rev")},
		{Path: "rev/pack.toml", Body: []byte("name = \"review\"\nversion = \"1.2.3\"\nskills = [\"rev-check\"]\n")},
		{Path: "rev/scripts/run.sh", Body: []byte("#!/bin/sh\necho ok\n"), Executable: true},
		{Path: "rev-check/SKILL.md", Body: skillBody("rev-check")},
	}
	skills := filepath.Join(dir, ".satelle", "skills")
	for _, f := range files {
		p := filepath.Join(skills, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if f.Executable {
			mode = 0o755
		}
		if err := os.WriteFile(p, f.Body, mode); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func publish(t *testing.T, srv *fakeserver.Server, entry, ws string, extra ...string) (string, error) {
	t.Helper()
	return run(t, "", append([]string{"pack", "publish", entry, "--workspace", ws, "--server", srv.URL, "--visibility", "workspace"}, extra...)...)
}

func TestPackPublishSendsManifestAndFiles(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	files := publishFixture(t, m.dir)
	srv.SetPackAdmins("ws_1", testToken)

	_, digest := fakeserver.PackBundle("review", "1.2.3", files)
	out, err := publish(t, srv, "rev", "ws_1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "published ws_1/review@1.2.3 (4 file(s), digest " + digest[:12] + ")"; strings.TrimSpace(out) != want {
		t.Fatalf("output = %q, want %q", out, want)
	}

	pubs := srv.Published()
	if len(pubs) != 1 {
		t.Fatalf("published %d request(s), want 1", len(pubs))
	}
	req := pubs[0]
	man := req.GetManifest()
	if req.GetVisibility() != satellepb.PackVisibility_PACK_VISIBILITY_WORKSPACE || len(req.GetSharedWith()) != 0 {
		t.Fatalf("request visibility = %v shared with %v, want workspace and none", req.GetVisibility(), req.GetSharedWith())
	}
	if req.GetOwnerWorkspaceId() != "ws_1" || man.GetName() != "review" || man.GetVersion() != "1.2.3" || man.GetDigest() != digest {
		t.Fatalf("request = %s %s@%s digest %s, want ws_1 review@1.2.3 digest %s", req.GetOwnerWorkspaceId(), man.GetName(), man.GetVersion(), man.GetDigest(), digest)
	}
	want, _ := fakeserver.PackBundle("review", "1.2.3", files)
	byPath := map[string]string{}
	for _, f := range req.GetFiles() {
		byPath[f.GetPath()] = string(f.GetBody())
	}
	refs := map[string]string{}
	for _, r := range man.GetFiles() {
		refs[r.GetPath()] = r.GetSha256()
	}
	for _, r := range want.GetVersion().GetManifest().GetFiles() {
		if refs[r.GetPath()] != r.GetSha256() {
			t.Errorf("%s sha256 = %q, want %q", r.GetPath(), refs[r.GetPath()], r.GetSha256())
		}
	}
	for _, f := range files {
		if byPath[f.Path] != string(f.Body) {
			t.Errorf("%s body = %q, want %q", f.Path, byPath[f.Path], f.Body)
		}
	}
	if len(byPath) != len(files) || len(refs) != len(files) {
		t.Errorf("sent %d file(s), %d ref(s), want %d", len(byPath), len(refs), len(files))
	}

	dest := filepath.Join(t.TempDir(), "fetched")
	must(t, "pack", "fetch", "ws_1/review", "--version", "1.2.3", "--dir", dest, "--server", srv.URL)
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(f.Path)))
		if err != nil || string(b) != string(f.Body) {
			t.Errorf("fetched %s = %q (%v), want %q", f.Path, b, err, f.Body)
		}
	}

	raw := must(t, "pack", "publish", "rev", "--workspace", "ws_1", "--server", srv.URL, "--visibility", "workspace", "--json")
	var got struct {
		Ref       string   `json:"ref"`
		Workspace string   `json:"owner_workspace_id"`
		Name      string   `json:"name"`
		Version   string   `json:"version"`
		Digest    string   `json:"digest"`
		Files     []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("json %v:\n%s", err, raw)
	}
	slices.Sort(got.Files)
	wantPaths := []string{"rev-check/SKILL.md", "rev/SKILL.md", "rev/pack.toml", "rev/scripts/run.sh"}
	if got.Ref != "ws_1/review" || got.Workspace != "ws_1" || got.Name != "review" || got.Version != "1.2.3" || got.Digest != digest || !slices.Equal(got.Files, wantPaths) {
		t.Fatalf("json = %+v", got)
	}

	if _, err := run(t, "", "pack", "publish", "rev", "--server", srv.URL, "--visibility", "workspace"); err == nil || !strings.Contains(err.Error(), "--workspace is required") {
		t.Errorf("missing --workspace: %v", err)
	}
	before := len(srv.Published())
	if _, err := publish(t, srv, "../x", "ws_1"); err == nil || !strings.Contains(err.Error(), "../x") {
		t.Errorf("unsafe entry: %v", err)
	}
	if n := len(srv.Published()); n != before {
		t.Errorf("unsafe entry reached the server: %d request(s), want %d", n, before)
	}
}

func TestPackPublishRefusesChangedVersion(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	files := publishFixture(t, m.dir)
	srv.SetPackAdmins("ws_1", testToken)
	if _, err := publish(t, srv, "rev", "ws_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := publish(t, srv, "rev", "ws_1"); err != nil {
		t.Errorf("republishing unchanged files: %v", err)
	}

	edited := filepath.Join(m.dir, ".satelle", "skills", "rev-check", "SKILL.md")
	if err := os.WriteFile(edited, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := publish(t, srv, "rev", "ws_1")
	if err == nil || !strings.Contains(err.Error(), "1.2.3") || !strings.Contains(err.Error(), "nothing was published") {
		t.Fatalf("changed version: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "fetched")
	must(t, "pack", "fetch", "ws_1/review", "--version", "1.2.3", "--dir", dest, "--server", srv.URL)
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(f.Path)))
		if err != nil || string(b) != string(f.Body) {
			t.Errorf("fetched %s = %q (%v), want the first publish's %q", f.Path, b, err, f.Body)
		}
	}
}

func TestPackPublishPermissionDenied(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	publishFixture(t, m.dir)
	srv.SetPackAdmins("ws_1", testToken)

	_, err := publish(t, srv, "rev", "ws_2")
	if err == nil || !strings.Contains(err.Error(), "may not publish") || !strings.Contains(err.Error(), "ws_2") || !strings.Contains(err.Error(), "nothing was published") {
		t.Fatalf("not an admin: %v", err)
	}
	if pubs := srv.Published(); len(pubs) != 1 || pubs[0].GetOwnerWorkspaceId() != "ws_2" {
		t.Fatalf("published requests = %v, want the refused ws_2 request", pubs)
	}
	if _, err := run(t, "", "pack", "fetch", "ws_2/review", "--version", "1.2.3", "--dir", filepath.Join(t.TempDir(), "x"), "--server", srv.URL); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("fetch of refused pack: %v", err)
	}
}

func TestPackPublishSendsVisibility(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	publishFixture(t, m.dir)
	srv.SetPackAdmins("ws_1", testToken)

	for _, c := range []struct {
		flags  []string
		vis    satellepb.PackVisibility
		shared []string
	}{
		{[]string{"--visibility", "public"}, satellepb.PackVisibility_PACK_VISIBILITY_PUBLIC, nil},
		{[]string{"--visibility", "workspace"}, satellepb.PackVisibility_PACK_VISIBILITY_WORKSPACE, nil},
		{[]string{"--visibility", "shared", "--share-with", "ws_2", "--share-with", "ws_3"}, satellepb.PackVisibility_PACK_VISIBILITY_SHARED, []string{"ws_2", "ws_3"}},
	} {
		args := append([]string{"pack", "publish", "rev", "--workspace", "ws_1", "--server", srv.URL}, c.flags...)
		if _, err := run(t, "", args...); err != nil {
			t.Fatalf("%v: %v", c.flags, err)
		}
		pubs := srv.Published()
		req := pubs[len(pubs)-1]
		if req.GetVisibility() != c.vis || !slices.Equal(req.GetSharedWith(), c.shared) {
			t.Errorf("%v: request visibility %v shared with %v, want %v %v", c.flags, req.GetVisibility(), req.GetSharedWith(), c.vis, c.shared)
		}
	}
}

func TestPackPublishRefusesVisibilityFlags(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	publishFixture(t, m.dir)
	srv.SetPackAdmins("ws_1", testToken)

	for _, c := range []struct {
		flags []string
		names string
	}{
		{nil, "--visibility"},
		{[]string{"--visibility", "private"}, "--visibility"},
		{[]string{"--visibility", "public", "--share-with", "ws_2"}, "--share-with"},
		{[]string{"--visibility", "workspace", "--share-with", "ws_2"}, "--share-with"},
		{[]string{"--visibility", "shared"}, "--share-with"},
		{[]string{"--visibility", "shared", "--share-with", " "}, "--share-with"},
	} {
		args := append([]string{"pack", "publish", "rev", "--workspace", "ws_1", "--server", srv.URL}, c.flags...)
		_, err := run(t, "", args...)
		if err == nil || !strings.Contains(err.Error(), c.names) {
			t.Errorf("%v: err = %v, want it to name %s", c.flags, err, c.names)
		}
	}
	if n := len(srv.Published()); n != 0 {
		t.Errorf("refused publishes reached the server: %d request(s)", n)
	}
}
