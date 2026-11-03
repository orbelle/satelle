//go:build clislow

package cli

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// AC1 + AC4: a skill edited in the working tree after engagement, with no
// change_record row yet, is refused in each state that is not on the hosted
// copy — naming the area, the state, the path and the command that moves it on.
func TestSlowSyncStatusStoryRefusesSubstrateNotOnHostedCopy(t *testing.T) {
	const touched = ".satelle/skills/x.md"

	t.Run("unpushed", func(t *testing.T) {
		ts, _ := newSnapServer(t)
		seedCred(t, ts.URL)
		a, e := storyMachine(t, ts.URL)
		editAfterEngage(t, a.repo, touched, "edited\n")
		out, err := a.storyStatus(e.id)
		wantRefusal(t, out, err, "skills", "unpushed", touched, "satelle sync push")
		if strings.Contains(out, "workflows") || strings.Contains(out, "documents") {
			t.Errorf("only the areas the story touched are judged:\n%s", out)
		}
	})

	t.Run("never synced", func(t *testing.T) {
		ts, _ := newSnapServer(t)
		seedCred(t, ts.URL)
		a := newSnapMachine(t, ts.URL)
		gitTree(t, a.repo, ".satelle/")
		a.use()
		e := engageSyncStory(t, a.repo)
		e.engage()
		editAfterEngage(t, a.repo, touched, "x\n")
		out, err := a.storyStatus(e.id)
		wantRefusal(t, out, err, "skills", "never synced", touched, "satelle sync push")
	})

	t.Run("diverged", func(t *testing.T) {
		ts, _ := newSnapServer(t)
		seedCred(t, ts.URL)
		a, e := storyMachine(t, ts.URL)
		b := newSnapMachine(t, ts.URL)
		b.deploy()
		b.write("skills/x.md", "from B\n")
		b.pushConfig()
		editAfterEngage(t, a.repo, touched, "from A\n")
		out, err := a.storyStatus(e.id)
		wantRefusal(t, out, err, "skills", "diverged", touched, "satelle sync reconcile, then satelle sync push")
	})

	t.Run("unmerged", func(t *testing.T) {
		ts, _ := newSnapServer(t)
		seedCred(t, ts.URL)
		a := publishedMachine(t, ts.URL)
		b := newSnapMachine(t, ts.URL)
		b.deploy()
		a.write("skills/x.md", "from A\n")
		a.pushConfig()
		gitTree(t, b.repo, ".satelle/")
		b.use()
		e := engageSyncStory(t, b.repo)
		e.engage()
		editAfterEngage(t, b.repo, touched, "from B\n")
		b.deploy() // conflict: both sides changed x.md
		out, err := b.storyStatus(e.id)
		wantRefusal(t, out, err, "skills", "unmerged", touched, "satelle sync reconcile")
	})

	t.Run("unavailable server", func(t *testing.T) {
		ts, _ := newSnapServer(t)
		seedCred(t, ts.URL)
		a, e := storyMachine(t, ts.URL)
		editAfterEngage(t, a.repo, touched, "edited\n")
		const down = "http://127.0.0.1:1" // signed in to a server that is not answering
		seedCred(t, down)
		out, err := a.storyStatusAt(down, e.id)
		wantRefusal(t, out, err, "skills", "unavailable", touched, "check the server, then satelle sync status")
	})

}

// AC2: substrate the story touched that is in step, or behind a hosted copy that
// holds the story's change, lets the story finish — and the output names the
// state of each touched area.
func TestSlowSyncStatusStoryAcceptsInStepAndBehind(t *testing.T) {
	const touched = ".satelle/skills/x.md"

	t.Run("in step", func(t *testing.T) {
		ts, _ := newSnapServer(t)
		seedCred(t, ts.URL)
		a, e := storyMachine(t, ts.URL)
		editAfterEngage(t, a.repo, touched, "edited\n")
		a.pushConfig()
		out, err := a.storyStatus(e.id)
		if err != nil {
			t.Fatalf("pushed substrate must exit zero: %v\n%s", err, out)
		}
		wantState(t, out, "skills", "in step")
		if !strings.Contains(out, touched) {
			t.Errorf("the touched path must be named:\n%s", out)
		}
	})

	t.Run("behind", func(t *testing.T) {
		ts, _ := newSnapServer(t)
		seedCred(t, ts.URL)
		a, e := storyMachine(t, ts.URL)
		editAfterEngage(t, a.repo, touched, "edited\n")
		a.pushConfig()
		b := newSnapMachine(t, ts.URL)
		b.deploy()
		b.write("skills/y.md", "from B\n")
		b.pushConfig() // the hosted copy moves on past A, A's change in its history
		out, err := a.storyStatus(e.id)
		if err != nil {
			t.Fatalf("a hosted copy that holds the story's change must exit zero: %v\n%s", err, out)
		}
		wantState(t, out, "skills", "behind")
	})
}

// AC3: a story that touched no synced substrate is unaffected and the server is
// never contacted.
func TestSlowSyncStatusStoryTouchedNoSyncedSubstrate(t *testing.T) {
	for name, tc := range map[string]struct {
		toml string
		edit func(t *testing.T, repo string)
	}{
		"non-substrate path": {snapToml, func(t *testing.T, repo string) {
			writeRepoFile(t, repo, "a.go", "package a\n\nvar changed = true\n")
		}},
		"signed out": {"[sync]\n" + boundProjectToml, func(t *testing.T, repo string) {
			editAfterEngage(t, repo, ".satelle/skills/x.md", "x\n")
		}},
		"no sync table": {boundProjectToml, func(t *testing.T, repo string) {
			editAfterEngage(t, repo, ".satelle/skills/x.md", "x\n")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				http.Error(w, "unreachable by design", http.StatusInternalServerError)
			}))
			defer ts.Close()
			repo := syncConfigRepo(t, tc.toml)
			gitTree(t, repo, ".satelle/")
			pointAt(t, repo)
			e := engageSyncStory(t, repo)
			e.engage()
			tc.edit(t, repo)

			cmd, buf := testCmd()
			if err := runSyncStatusForStory(cmd, ts.URL, e.id); err != nil {
				t.Fatalf("a story that touched no synced substrate must exit zero: %v\n%s", err, buf.String())
			}
			if !strings.Contains(buf.String(), "touched no synced substrate") {
				t.Errorf("output = %q", buf.String())
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("the server was contacted %d time(s)", n)
			}
		})
	}
}

// AC8: a story engaged from a linked worktree makes its substrate edits in the
// main tree; they are mapped and probed against the process of record.
func TestSlowSyncStatusStoryFromLinkedWorktree(t *testing.T) {
	const touched = ".satelle/skills/z.md"
	setup := func(t *testing.T) (*snapMachine, *storyEnv, string) {
		ts, _ := newSnapServer(t)
		seedCred(t, ts.URL)
		a := publishedMachine(t, ts.URL)
		gitTree(t, a.repo, ".satelle/")
		wt := filepath.Join(t.TempDir(), "wt")
		if out, err := exec.Command("git", "-C", a.repo, "worktree", "add", "-b", "wt-branch", wt).CombinedOutput(); err != nil {
			t.Fatalf("git worktree add: %v\n%s", err, out)
		}
		a.use()
		e := engageSyncStory(t, wt)
		e.engage()
		editAfterEngage(t, a.repo, touched, "z\n")
		return a, e, wt
	}
	// status runs from the worktree with nothing naming the main tree's config:
	// the process of record is found the way every worktree command finds it.
	status := func(t *testing.T, a *snapMachine, id string) (string, error) {
		a.use()
		t.Setenv("SATELLE_CONFIG", "")
		cmd, buf := testCmd()
		err := runSyncStatusForStory(cmd, a.url, id)
		out := buf.String()
		if err != nil {
			out += err.Error()
		}
		return out, err
	}

	t.Run("unpushed edit is refused", func(t *testing.T) {
		a, e, _ := setup(t)
		out, err := status(t, a, e.id)
		wantRefusal(t, out, err, "skills", "unpushed", touched, "satelle sync push")
	})

	t.Run("pushed edit passes", func(t *testing.T) {
		a, e, _ := setup(t)
		a.pushConfig()
		out, err := status(t, a, e.id)
		if err != nil {
			t.Fatalf("a pushed worktree edit must exit zero: %v\n%s", err, out)
		}
		wantState(t, out, "skills", "in step")
	})
}
