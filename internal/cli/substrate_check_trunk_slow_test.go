//go:build clislow

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/testutil"
)

// The id4me sty_d9904629 shape (sty_659c4133): a substrate story engaged from a main
// tree that fast-forwards over another story's merge, whose own work is only the
// gitignored .satelle/. The embedded satelle-substrate-only-check runs as the gate
// runs it — a script that shells out to `satelle story diff` — against a satelle
// binary built from this tree, so the change set it reads is the one under test.
func TestSlowSubstrateOnlyCheckAfterFastForwardOverForeignCommit(t *testing.T) {
	script := embeddedCheckScript(t, "satelle-substrate-only-check")
	bin := buildSatelle(t)

	foreignPaths := []string{
		".github/workflows/ci.yml", "Makefile", "README.md",
		"scripts/ci-proven-tree.sh", "scripts/ci-proven-tree_test.sh", ".version",
	}
	// setup engages a story, lands a commit touching foreignPaths under the subject
	// the case gives it, fast-forwards the tree over it, and has the story edit only
	// the gitignored .satelle/.
	setup := func(t *testing.T, foreignSubject func(id string) string) (repo, id string) {
		repo, r, id := trunkEngageRepo(t, "")
		if out, err := runRoot(t, "story", "set", id, "--status", "in_progress"); err != nil {
			t.Fatalf("engage: %v\n%s", err, out)
		}
		r.Git(t, r.Pusher, "pull", "--quiet", "--ff-only")
		for _, p := range foreignPaths {
			abs := filepath.Join(r.Pusher, p)
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte(p+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		r.Git(t, r.Pusher, "add", "-A")
		r.Git(t, r.Pusher, "commit", "--quiet", "-m", foreignSubject(id))
		r.Git(t, r.Pusher, "push", "--quiet", "origin", "main")
		r.Git(t, repo, "pull", "--quiet", "--ff-only")

		skill := filepath.Join(repo, ".satelle", "skills", "x.md")
		if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(skill, []byte("a lane path\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return repo, id
	}

	t.Run("another story's commit is not this story's change", func(t *testing.T) {
		repo, id := setup(t, func(string) string { return "feat: proven tree (sty_other)" })
		out, code := runCheck(t, script, bin, repo, id)
		if code != 0 {
			t.Fatalf("a substrate story whose only own change is .satelle/ was refused (exit %d):\n%s", code, out)
		}
		if !strings.Contains(out, ".satelle/skills/x.md") {
			t.Errorf("the check must have seen the story's own lane path:\n%s", out)
		}
	})

	t.Run("a commit naming the story is its change", func(t *testing.T) {
		repo, id := setup(t, func(id string) string { return "feat: proven tree (" + id + ")" })
		out, code := runCheck(t, script, bin, repo, id)
		if code == 0 || !strings.Contains(out, "Makefile") {
			t.Fatalf("a non-lane commit naming the story must be refused naming Makefile (exit %d):\n%s", code, out)
		}
	})

	t.Run("the story's own non-lane commit is its change", func(t *testing.T) {
		repo, id := setup(t, func(string) string { return "feat: proven tree (sty_other)" })
		r := testutil.TrunkRepos{}
		r.Git(t, repo, "commit", "--quiet", "--allow-empty", "-m", "noop")
		if err := os.WriteFile(filepath.Join(repo, "mine.go"), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r.Git(t, repo, "add", "mine.go")
		r.Git(t, repo, "commit", "--quiet", "-m", "fix: mine ("+id+")")
		out, code := runCheck(t, script, bin, repo, id)
		if code == 0 || !strings.Contains(out, "mine.go") || strings.Contains(out, "Makefile") {
			t.Fatalf("only the story's own non-lane file may be named (exit %d):\n%s", code, out)
		}
	})
}
