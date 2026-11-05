package cli

// Under a team scope a story's attachments also go to its team workspace's
// documents collection (sty_61872918): the only collection a team remote reads. Only the
// hosted server is faked, as in workstate_attachments_test.go; the personal
// collection and the team workspace's are two separate fakeDocs.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/app"
)

const (
	teamToml   = "[sync]\n\n[hosted]\nproject = \"probe\"\nworkspace = \"Acme\"\n"
	noTeamToml = "[sync]\n\n[hosted]\nproject = \"probe\"\n"

	teamWorkspaceRoot = "/api/v1/workspaces/ws-team/documents/"
	teamStateID       = "ws:ws-team" // the uploaded-state / cursor key of the team collection
)

func newTeamDocs() *fakeDocs {
	d := newFakeDocs()
	d.root = "/api/v1/workspaces/{ws}/documents"
	return d
}

// seed stores a file in the fake collection as another machine's push would.
func (d *fakeDocs) seed(path string, body []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.files[path] = body
	d.seq++
	d.ver[path] = d.seq
}

// teamServer is the fake workstate server with the personal documents collection
// and the Acme team workspace's (id ws-team). The account resolves the bound
// project to a team it belongs to.
func teamServer(t *testing.T) (url string, f *fakeWorkstateServer, personal, team *fakeDocs) {
	t.Helper()
	useScope(t, kindTeam)
	personal, team = newFakeDocs(), newTeamDocs()
	ts, f := newFakeWorkstateServerWith(t, func(mux *http.ServeMux, f *fakeWorkstateServer) {
		personal.routes(mux, f)
		team.routes(mux, f)
	})
	seedCred(t, ts.URL)
	return ts.URL, f, personal, team
}

// attachmentRepoRoot is the repo root the uploaded-state records are keyed on.
func attachmentRepoRoot(t *testing.T) string {
	t.Helper()
	a, err := app.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	return a.RepoRoot
}

func writePNG(t *testing.T, id string) {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "shot.png")
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89}
	if err := os.WriteFile(binPath, png, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runRoot(t, "story", "attach", id, "--name", "shot.png", "--type", "screenshot",
		"--binary-file", binPath, "--content-type", "image/png"); err != nil {
		t.Fatalf("binary attach: %v\n%s", err, out)
	}
}

// AC7: the help says a team scope also sends attachments to the team
// workspace's collection.
func TestSyncWorkstatePushHelpNamesTeamCollection(t *testing.T) {
	for _, args := range [][]string{{"sync", "workstate", "push", "--help"}, {"sync", "workstate", "--help"}} {
		out, err := runRoot(t, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		flat := strings.Join(strings.Fields(out), " ")
		if !strings.Contains(flat, "team workspace's documents collection") {
			t.Errorf("%v help should name the team workspace's documents collection:\n%s", args, out)
		}
	}
}
