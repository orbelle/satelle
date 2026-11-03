//go:build clislow

package cli

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted"
)

// AC3: the team leg applies the personal leg's rules.
func TestSlowAttachmentTeamLegRules(t *testing.T) {
	t.Run("binary never leaves", func(t *testing.T) {
		url, _, personal, team := teamServer(t)
		workstateRepo(t, teamToml)
		id := createStoryID(t, "binary team")
		attachDoc(t, id, "plan", "plan", "markdown sibling")
		writePNG(t, id)
		out := runPush(t, url)
		if _, ok := team.file("stories/" + id + "/plan.md"); !ok {
			t.Fatal("the markdown sibling should reach the workspace route (control)")
		}
		for _, docs := range []*fakeDocs{personal, team} {
			for _, p := range docs.reqs {
				if strings.Contains(p, "shot.png") {
					t.Errorf("binary attachment requested at %q", p)
				}
			}
		}
		if !strings.Contains(out, "attachments (team Acme): 1 uploaded, 0 unchanged, 1 binary not mirrored") {
			t.Errorf("the team line should count the binary as not mirrored: %q", out)
		}
	})

	t.Run("held then released", func(t *testing.T) {
		personal, team := newFakeDocs(), newTeamDocs()
		useScope(t, kindTeam)
		w := newHoldWorldWith(t, teamToml, func(mux *http.ServeMux, f *fakeWorkstateServer) {
			personal.routes(mux, f)
			team.routes(mux, f)
		})
		attachDoc(t, w.unpushed, "plan", "plan", "unpushed plan")
		attachDoc(t, w.ok, "plan", "plan", "pushed plan")
		unpushedPath, okPath := "stories/"+w.unpushed+"/plan.md", "stories/"+w.ok+"/plan.md"

		out := w.push(t)
		if _, ok := team.file(okPath); !ok {
			t.Fatal("the released story's plan should reach the workspace route (control)")
		}
		if !strings.Contains(out, "attachments (team Acme):") || !strings.Contains(out, "held with their stories") {
			t.Errorf("the team line should say files were held: %q", out)
		}
		for label, docs := range map[string]*fakeDocs{"project": personal, "team workspace": team} {
			if _, ok := docs.file(unpushedPath); ok {
				t.Errorf("a held story's file reached the %s collection", label)
			}
			for _, p := range docs.reqs {
				if strings.Contains(p, w.unpushed) {
					t.Errorf("a request named the held story on the %s route: %q", label, p)
				}
			}
		}

		gitIn(t, w.unpushedTree, "push", "origin", "HEAD:refs/heads/side")
		w.push(t)
		if _, ok := team.file(unpushedPath); !ok {
			t.Error("once the hold clears the file should reach the workspace route")
		}
		if _, ok := personal.file(unpushedPath); !ok {
			t.Error("once the hold clears the file should reach the project route")
		}
	})

	t.Run("unchanged files are skipped by sha", func(t *testing.T) {
		url, _, _, team := teamServer(t)
		workstateRepo(t, teamToml)
		id := createStoryID(t, "sha skip team")
		attachDoc(t, id, "plan", "plan", "p")
		attachDoc(t, id, "summary", "step-summary", "s")

		runPush(t, url)
		if team.putCount() != 2 {
			t.Fatalf("first push workspace PUTs = %d, want 2 (control)", team.putCount())
		}
		recorded, err := hosted.LoadUploadedAttachments(url, teamStateID, attachmentRepoRoot(t))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"plan", "summary"} {
			body, _ := team.file("stories/" + id + "/" + name + ".md")
			if got := recorded[id+"/"+name+".md"]; got == "" || got != sha256Hex(body) {
				t.Errorf("%s recorded under %s as %q, want the sha of what was sent", name, teamStateID, got)
			}
		}

		out := runPush(t, url)
		if team.putCount() != 2 {
			t.Errorf("second push workspace PUTs = %d, want none more", team.putCount()-2)
		}
		if !strings.Contains(out, "attachments (team Acme): 0 uploaded, 2 unchanged") {
			t.Errorf("the team line should report them unchanged: %q", out)
		}
	})
}

// AC5: pull and snapshot read the team collection for local stories with their own
// cursor, keep a differing local file unless forced, and under --force the team
// copy beats the personal one.
func TestSlowAttachmentTeamPull(t *testing.T) {
	// setup: machine A's story reaches machine B as a row, with no attachments yet.
	setup := func(t *testing.T) (url string, personal, team *fakeDocs, b cliMachine, id string) {
		t.Helper()
		url, _, personal, team = teamServer(t)
		a := newCLIMachine(t, teamToml)
		b = newCLIMachine(t, teamToml)
		a.use(t)
		id = createStoryID(t, "team pull")
		runPush(t, url)
		b.use(t)
		syncWorkstateCLI(t, url, "pull")
		return url, personal, team, b, id
	}

	for _, verbName := range []string{"pull", "snapshot"} {
		t.Run(verbName+" reads the team collection", func(t *testing.T) {
			url, _, team, _, id := setup(t)
			team.seed("stories/"+id+"/plan.md", []byte("plan from the team"))
			team.seed("stories/sty_notheld/plan.md", []byte("a story this machine does not hold"))
			out := syncWorkstateCLI(t, url, verbName)
			if !strings.Contains(out, "attachments (team Acme): 1 written") {
				t.Errorf("%s should report the team write: %q", verbName, out)
			}
			if got := mustDocBody(t, id, "plan"); !strings.Contains(got, "plan from the team") {
				t.Errorf("plan in the attachment store = %q", got)
			}
			if _, err := storyDocBody(t, "sty_notheld", "plan"); err == nil {
				t.Error("an attachment of a story this machine does not hold must not be written")
			}
			cursor, err := hosted.LoadAttachmentCursor(url, teamStateID, attachmentRepoRoot(t))
			if err != nil || cursor == "" {
				t.Errorf("the team collection should have its own cursor, got %q (%v)", cursor, err)
			}
		})
	}

	t.Run("a differing local file is kept without force", func(t *testing.T) {
		url, _, team, _, id := setup(t)
		attachDoc(t, id, "plan", "plan", "local plan")
		want := mustDocBody(t, id, "plan")
		team.seed("stories/"+id+"/plan.md", []byte("plan from the team"))
		out := syncWorkstateCLI(t, url, "pull")
		if !strings.Contains(out, "kept local stories/"+id+"/plan.md") || !strings.Contains(out, "the team copy") {
			t.Errorf("a differing local file should be named as kept: %q", out)
		}
		if got := mustDocBody(t, id, "plan"); got != want {
			t.Errorf("without --force the local plan changed to %q, want %q", got, want)
		}
	})

	t.Run("force takes the team copy over the personal one", func(t *testing.T) {
		url, personal, team, _, id := setup(t)
		attachDoc(t, id, "plan", "plan", "local plan")
		personal.seed("stories/"+id+"/plan.md", []byte("plan from the personal collection"))
		team.seed("stories/"+id+"/plan.md", []byte("plan from the team"))
		syncWorkstateCLI(t, url, "pull", "--force")
		got := mustDocBody(t, id, "plan")
		if !strings.Contains(got, "plan from the team") || strings.Contains(got, "personal collection") {
			t.Errorf("with --force the team copy should win: plan = %q", got)
		}
	})
}
