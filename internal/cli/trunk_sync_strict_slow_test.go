//go:build clislow

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/testutil"
)

func TestSlowTrunkSyncStrictStopsOnTheBaseStopSet(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, repo string, r testutil.TrunkRepos)
		want  []string
	}{
		{"diverged", func(t *testing.T, repo string, r testutil.TrunkRepos) {
			r.Commit(t, repo, "mine.txt", "mine\n")
			r.PublishFromPusher(t, "theirs.txt")
		}, []string{"diverged: 1 ahead, 1 behind"}},
		{"ahead", func(t *testing.T, repo string, r testutil.TrunkRepos) {
			r.Commit(t, repo, "mine.txt", "mine\n")
		}, []string{"1 unpushed", "push"}},
		{"offline", func(t *testing.T, repo string, r testutil.TrunkRepos) {
			r.Git(t, repo, "remote", "set-url", "origin", filepath.Join(filepath.Dir(r.Remote), "gone.git"))
		}, []string{"fetch from origin failed"}},
		{"dirty", func(t *testing.T, repo string, r testutil.TrunkRepos) {
			if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			r.PublishFromPusher(t, "theirs.txt")
		}, []string{"dirty tree on main", "stash"}},
		{"behind and not moved", func(t *testing.T, repo string, r testutil.TrunkRepos) {
			r.Git(t, repo, "checkout", "--quiet", "-b", "elsewhere")
			r.Git(t, repo, "worktree", "add", "--quiet", filepath.Join(filepath.Dir(repo), "holder"), "main")
			r.PublishFromPusher(t, "theirs.txt")
		}, []string{"behind origin/main by 1, not moved", "holder"}},
		{"unresolved", func(t *testing.T, repo string, r testutil.TrunkRepos) {
			r.Git(t, repo, "remote", "set-head", "origin", "--delete")
			r.Git(t, r.Remote, "symbolic-ref", "HEAD", "refs/heads/gone")
		}, []string{"--trunk-branch", "git remote set-head origin --auto"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, r, _ := trunkEngageRepo(t, "")
			c.setup(t, repo, r)
			before := r.Git(t, repo, "rev-parse", "main")
			stdout, _, err := runRootSplit(t, "", "trunk", "sync", "--fast-forward", "--strict")
			if err == nil {
				t.Fatalf("strict sync exited 0, want a stop; stdout %q", stdout)
			}
			if !strings.HasPrefix(stdout, "satelle: trunk ") {
				t.Errorf("the report is printed before the stop, got %q", stdout)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
			if strings.Contains(err.Error(), " --branch") {
				t.Errorf("error tells the operator to pass --branch: %v", err)
			}
			if got := r.Git(t, repo, "rev-parse", "main"); got != before {
				t.Errorf("a stopped sync moved main: %s -> %s", before, got)
			}
			// Without --strict the same state is only reported.
			if _, _, err := runRootSplit(t, "", "trunk", "sync", "--fast-forward"); err != nil {
				t.Errorf("non-strict sync exited non-zero: %v", err)
			}
		})
	}
}

// A state listed out of the stop set proceeds with exit 0 and its line printed,
// whether the stop set comes from --refuse or from [trunk] base_refuse.
func TestSlowTrunkSyncStrictOverrideLetsAStoppingStateProceed(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, repo string, r testutil.TrunkRepos)
		line  string
	}{
		{"offline", func(t *testing.T, repo string, r testutil.TrunkRepos) {
			r.Git(t, repo, "remote", "set-url", "origin", filepath.Join(filepath.Dir(r.Remote), "gone.git"))
		}, "satelle: trunk fetch from origin failed"},
		{"diverged", func(t *testing.T, repo string, r testutil.TrunkRepos) {
			r.Commit(t, repo, "mine.txt", "mine\n")
			r.PublishFromPusher(t, "theirs.txt")
		}, "satelle: trunk diverged: 1 ahead, 1 behind"},
		{"ahead", func(t *testing.T, repo string, r testutil.TrunkRepos) {
			r.Commit(t, repo, "mine.txt", "mine\n")
		}, "satelle: trunk 1 unpushed"},
		{"unresolved", func(t *testing.T, repo string, r testutil.TrunkRepos) {
			r.Git(t, repo, "remote", "set-head", "origin", "--delete")
			r.Git(t, r.Remote, "symbolic-ref", "HEAD", "refs/heads/gone")
		}, "satelle: trunk unresolved:"},
	}
	via := []struct {
		name string
		toml string
		args []string
	}{
		{"refuse flag", "", []string{"--refuse", "dirty"}},
		{"base_refuse", "[trunk]\nbase_refuse = [\"dirty\"]\n", nil},
	}
	for _, v := range via {
		for _, c := range cases {
			t.Run(v.name+"/"+c.name, func(t *testing.T) {
				repo, r, _ := trunkEngageRepo(t, v.toml)
				c.setup(t, repo, r)
				before := r.Git(t, repo, "rev-parse", "main")
				args := append([]string{"trunk", "sync", "--fast-forward", "--strict"}, v.args...)
				stdout, stderr, err := runRootSplit(t, "", args...)
				if err != nil {
					t.Fatalf("overridden strict sync: %v\n%s", err, stderr)
				}
				if !strings.Contains(stdout, c.line) {
					t.Errorf("stdout %q lacks %q", stdout, c.line)
				}
				if got := r.Git(t, repo, "rev-parse", "main"); got != before {
					t.Errorf("main moved: %s -> %s", before, got)
				}
			})
		}
	}
}
