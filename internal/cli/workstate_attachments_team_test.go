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
	"github.com/bobmcallan/satelle/internal/hosted"
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

// AC1, AC6: under a team scope with a team workspace set, every markdown
// attachment goes byte for byte to the project collection and to the workspace
// route, and no attachment body rides an Apply.
func TestPushStoryAttachmentsSharedTeam(t *testing.T) {
	const marker = "team-attachment-marker-sty_61872918"
	url, f, personal, team := teamServer(t)
	workstateRepo(t, teamToml)
	id := createStoryID(t, "shared attachments")
	attachDoc(t, id, "change-in_progress-done", "change", "diff --git a/x.go b/x.go\n+"+marker+"\n")
	attachDoc(t, id, "plan", "plan", "the plan")

	out := runPush(t, url)
	if !strings.Contains(out, "attachments: 2 uploaded") || !strings.Contains(out, "attachments (team Acme): 2 uploaded") {
		t.Errorf("push should report both collections: %q", out)
	}
	for _, name := range []string{"change-in_progress-done", "plan"} {
		want := mustDocBody(t, id, name)
		path := "stories/" + id + "/" + name + ".md"
		for label, docs := range map[string]*fakeDocs{"project": personal, "team workspace": team} {
			got, ok := docs.file(path)
			if !ok {
				t.Fatalf("%s not uploaded to the %s collection", name, label)
			}
			if string(got) != want {
				t.Errorf("%s bytes in the %s collection differ:\n got %q\nwant %q", name, label, got, want)
			}
		}
	}
	if personal.putCount() != 2 || team.putCount() != 2 {
		t.Fatalf("PUTs = %d project, %d team, want 2 each", personal.putCount(), team.putCount())
	}
	for _, p := range team.puts {
		if !strings.HasPrefix(p, teamWorkspaceRoot+"stories/"+id+"/") {
			t.Errorf("team PUT went to %q, want under %sstories/%s/", p, teamWorkspaceRoot, id)
		}
	}
	for _, p := range personal.puts {
		if !strings.HasPrefix(p, attachmentPrefix+"stories/"+id+"/") {
			t.Errorf("project PUT went to %q, want under %sstories/%s/", p, attachmentPrefix, id)
		}
	}
	// AC6: Apply carried rows, never a body.
	assertNoSecretInApply(t, f, marker)
	assertNoSecretInApply(t, f, "the plan")
}

// AC2: under a personal scope, or a team scope with no team workspace set, a story
// reaches no workspace documents route; the no-workspace case says why in one line.
func TestPushStoryAttachmentsTeamCopySkipped(t *testing.T) {
	t.Run("personal scope", func(t *testing.T) {
		url, _, personal, team := teamServer(t)
		useScope(t, kindPersonal)
		workstateRepo(t, teamToml)
		id := createStoryID(t, "personal only")
		attachDoc(t, id, "plan", "plan", "p")
		out := runPush(t, url)
		if personal.putCount() != 1 {
			t.Fatalf("the project collection PUTs = %d, want 1 (control)", personal.putCount())
		}
		if n := team.requestCount(); n != 0 {
			t.Errorf("workspace documents requests = %d, want 0", n)
		}
		if strings.Contains(out, "team copy skipped") {
			t.Errorf("a personal scope has no team copy to skip: %q", out)
		}
	})

	t.Run("team scope without a team workspace", func(t *testing.T) {
		url, _, personal, team := teamServer(t)
		workstateRepo(t, noTeamToml)
		id := createStoryID(t, "shared without team")
		attachDoc(t, id, "plan", "plan", "p")
		out := runPush(t, url)
		if personal.putCount() != 1 {
			t.Fatalf("the project collection PUTs = %d, want 1 (control)", personal.putCount())
		}
		if n := team.requestCount(); n != 0 {
			t.Errorf("workspace documents requests = %d, want 0", n)
		}
		if n := strings.Count(out, "team copy skipped"); n != 1 {
			t.Errorf("the skip line count = %d, want 1: %q", n, out)
		}
		if !strings.Contains(out, "no team workspace") {
			t.Errorf("the skip line should say why: %q", out)
		}
	})
}

// AC4: a refused workspace PUT is a failure, never an upload or a record, and the
// next push retries it.
func TestAttachmentTeamPushRefusal(t *testing.T) {
	url, _, personal, team := teamServer(t)
	workstateRepo(t, teamToml)
	id := createStoryID(t, "refused team")
	attachDoc(t, id, "plan", "plan", "p")
	rel := "stories/" + id + "/plan.md"
	team.refuse = http.StatusForbidden

	out := runPush(t, url) // the push itself still succeeds
	if personal.putCount() != 1 {
		t.Fatalf("the project collection PUTs = %d, want 1 (control)", personal.putCount())
	}
	if !strings.Contains(out, "attachment upload failed (team Acme) "+rel) {
		t.Errorf("the refusal should name the file: %q", out)
	}
	if !strings.Contains(out, teamWorkspaceRoot+rel) || !strings.Contains(out, "403") {
		t.Errorf("the refusal should name the route and the status: %q", out)
	}
	if !strings.Contains(out, "attachments (team Acme): 0 uploaded, 0 unchanged, 1 failed") {
		t.Errorf("the team line should count it failed: %q", out)
	}
	if _, ok := team.file(rel); ok {
		t.Fatal("a refused file must not be hosted")
	}
	recorded, err := hosted.LoadUploadedAttachments(url, teamStateID, attachmentRepoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := recorded[id+"/plan.md"]; ok {
		t.Error("a refused upload must not be recorded")
	}

	team.refuse = 0
	out = runPush(t, url)
	if _, ok := team.file(rel); !ok {
		t.Errorf("the next push should retry the refused file: %q", out)
	}
	recorded, _ = hosted.LoadUploadedAttachments(url, teamStateID, attachmentRepoRoot(t))
	if recorded[id+"/plan.md"] == "" {
		t.Error("the retried upload should be recorded")
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
