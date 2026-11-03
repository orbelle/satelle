//go:build clislow

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/testutil"
)

func TestSlowStorySetEngagePrintsTrunkCheck(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, repo string, r testutil.TrunkRepos)
		want    func(t *testing.T, r testutil.TrunkRepos, before string) string // the exact or prefix line
		refused bool
		hint    string
	}{
		{
			name: "behind is fast-forwarded",
			setup: func(t *testing.T, repo string, r testutil.TrunkRepos) {
				r.PublishFromPusher(t, "a.txt")
				r.PublishFromPusher(t, "b.txt")
			},
			want: func(t *testing.T, r testutil.TrunkRepos, before string) string {
				tip := r.Git(t, r.Remote, "rev-parse", "refs/heads/main")
				return "satelle: trunk fast-forwarded main by 2 commit(s) " + before[:8] + ".." + tip[:8]
			},
		},
		{
			name:  "ahead proceeds with a notice",
			setup: func(t *testing.T, repo string, r testutil.TrunkRepos) { r.Commit(t, repo, "mine.txt", "mine\n") },
			want: func(*testing.T, testutil.TrunkRepos, string) string {
				return "satelle: trunk 1 unpushed commit(s)"
			},
		},
		{
			name: "diverged refuses",
			setup: func(t *testing.T, repo string, r testutil.TrunkRepos) {
				r.Commit(t, repo, "mine.txt", "mine\n")
				r.PublishFromPusher(t, "a.txt")
			},
			want: func(*testing.T, testutil.TrunkRepos, string) string {
				return "satelle: trunk diverged: 1 ahead, 1 behind"
			},
			refused: true,
			hint:    "git pull --rebase",
		},
		{
			name: "dirty refuses",
			setup: func(t *testing.T, repo string, r testutil.TrunkRepos) {
				if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("edited\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: func(*testing.T, testutil.TrunkRepos, string) string {
				return "satelle: trunk dirty tree on main"
			},
			refused: true,
			hint:    "git stash",
		},
		{
			name: "offline proceeds with a warning",
			setup: func(t *testing.T, repo string, r testutil.TrunkRepos) {
				r.Git(t, repo, "remote", "set-url", "origin", filepath.Join(filepath.Dir(r.Remote), "gone.git"))
			},
			want: func(*testing.T, testutil.TrunkRepos, string) string {
				return "satelle: trunk fetch from origin failed (proceeding): "
			},
		},
		{
			name:  "level prints nothing",
			setup: func(*testing.T, string, testutil.TrunkRepos) {},
			want:  func(*testing.T, testutil.TrunkRepos, string) string { return "" },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, r, id := trunkEngageRepo(t, "")
			before := r.Head(t, repo)
			tc.setup(t, repo, r)
			want := tc.want(t, r, before)

			_, stderr, err := runRootSplit(t, "", "story", "set", id, "--status", "in_progress")

			lines := trunkLines(stderr)
			if want == "" {
				if len(lines) != 0 {
					t.Fatalf("a level trunk printed %q", lines)
				}
			} else if len(lines) != 1 || !strings.HasPrefix(lines[0], want) {
				t.Fatalf("trunk lines = %q, want one starting %q\nstderr:\n%s", lines, want, stderr)
			}
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), tc.hint) {
					t.Fatalf("err = %v, want a refusal naming %q", err, tc.hint)
				}
				if st := storyStatus(t, id); st != "backlog" {
					t.Fatalf("a refused engage left the story at %s", st)
				}
				return
			}
			if err != nil {
				t.Fatalf("engage: %v\n%s", err, stderr)
			}
			if st := storyStatus(t, id); st != "in_progress" {
				t.Fatalf("status = %s, want in_progress", st)
			}
			led, lerr := runRoot(t, "ledger", "list", "--story", id)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if got := strings.Contains(led, "trunk_check"); got != (want != "") {
				t.Fatalf("ledger carries trunk_check = %v, want %v:\n%s", got, want != "", led)
			}
		})
	}
}

// Outside a detached run the line prints to the command's stderr.
func TestSlowStorySetEngageTrunkLineStaysOnStderrOutsideAGateRun(t *testing.T) {
	repo, r, id := trunkEngageRepo(t, "")
	r.PublishFromPusher(t, "a.txt")
	before := r.Head(t, repo)
	tip := r.Git(t, r.Remote, "rev-parse", "refs/heads/main")

	_, stderr, err := runRootSplit(t, "", "story", "set", id, "--status", "in_progress")
	if err != nil {
		t.Fatalf("engage: %v\n%s", err, stderr)
	}
	want := "satelle: trunk fast-forwarded main by 1 commit(s) " + before[:8] + ".." + tip[:8]
	if lines := trunkLines(stderr); len(lines) != 1 || !strings.HasPrefix(lines[0], want) {
		t.Fatalf("trunk lines = %q, want one starting %q", lines, want)
	}
}
