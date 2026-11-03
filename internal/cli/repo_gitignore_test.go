package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/health"
)

// TestRepoGitignoreNeedsNoConverge (sty_dbc0be85 AC7): this repo's committed
// .gitignore already settles everything satelle needs, so running `satelle
// init` here leaves no .gitignore diff.
func TestRepoGitignoreNeedsNoConverge(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Skipf("no repo .gitignore beside the sources: %v", err)
	}
	if _, added, changed := convergeGitignore(string(raw), root); changed {
		t.Errorf("the committed .gitignore lacks entries satelle needs: %v", added)
	}
}

// substrateRepo is a committed git repo holding an untracked .satelle/ process
// substrate and the given .gitignore, with git's global config isolated.
func substrateRepo(t *testing.T, gitignore, excludes string) (repo, excludesPath string) {
	t.Helper()
	excludesPath = isolateGitExcludes(t, excludes)
	repo = gitInitRepo(t, t.TempDir())
	writeFile(t, repo, ".satelle/satelle.toml", "# x\n")
	writeFile(t, repo, ".satelle/workflows/done.toml", "# x\n")
	if gitignore != "" {
		writeFile(t, repo, ".gitignore", gitignore)
	}
	return repo, excludesPath
}

// oneUntrackedFinding asserts fs is exactly one repo.substrate.untracked warning
// whose remediation names the block to adopt and the git add, and returns it.
func oneUntrackedFinding(t *testing.T, fs health.Findings) health.Finding {
	t.Helper()
	if len(fs) != 1 {
		t.Fatalf("want one finding, got %+v", fs)
	}
	f := fs[0]
	if f.ID != health.IDSubstrateUntracked || f.Severity != health.SeverityWarn {
		t.Errorf("want a %s warning, got %s %s", health.IDSubstrateUntracked, f.ID, f.Severity)
	}
	for _, want := range []string{"satelle init", "!.satelle/", "git add .satelle", "commit"} {
		if !strings.Contains(f.Remediation, want) {
			t.Errorf("remediation lacks %q: %s", want, f.Remediation)
		}
	}
	return f
}

// TestSubstrateTrackingFindings (sty_45bf867b AC2, AC3): doctor warns, with the
// remediation in the finding, for each way the process substrate is kept out
// of git, and says nothing once it is tracked.
func TestSubstrateTrackingFindings(t *testing.T) {
	block := gitignoreBlockWrap(satelleTrackedSubstrateIgnores)
	for _, own := range []string{"/.satelle", ".satelle/"} {
		t.Run("own line "+own, func(t *testing.T) {
			// The operator's line settles .satelle, so init's block holds no
			// re-include over it (gitignoreNeeded) and the line decides.
			repo, _ := substrateRepo(t, "node_modules/\n"+own+"\n"+gitignoreBlockWrap(satelleTrackedSubstrateIgnores[1:]), "")
			f := oneUntrackedFinding(t, substrateTrackingFindings(repo))
			want := fmt.Sprintf("remove the line %q from .gitignore", own)
			if !strings.Contains(f.Remediation, want) {
				t.Errorf("remediation must name the exact line (%s): %s", want, f.Remediation)
			}
			if !strings.Contains(f.Detail, fmt.Sprintf("%q", own)) {
				t.Errorf("detail must quote the line: %s", f.Detail)
			}
		})
	}
	t.Run("own line under a re-include", func(t *testing.T) {
		// A re-include after the line un-ignores the substrate, but the line is
		// still named while the substrate is untracked.
		repo, _ := substrateRepo(t, ".satelle/\n!.satelle/\n", "")
		f := oneUntrackedFinding(t, substrateTrackingFindings(repo))
		if !strings.Contains(f.Remediation, `remove the line ".satelle/" from .gitignore`) {
			t.Errorf("remediation must name the own line: %s", f.Remediation)
		}
	})
	t.Run("old managed block", func(t *testing.T) {
		repo, _ := substrateRepo(t, gitignoreMarker+"\n.satelle/\n"+gitignoreMarkerEnd+"\n", "")
		f := oneUntrackedFinding(t, substrateTrackingFindings(repo))
		if strings.Contains(f.Remediation, "remove the") {
			t.Errorf("init removes its own block's line; no manual removal is asked: %s", f.Remediation)
		}
	})
	t.Run("core.excludesFile", func(t *testing.T) {
		repo, excludes := substrateRepo(t, "", ".satelle/\n")
		f := oneUntrackedFinding(t, substrateTrackingFindings(repo))
		if !strings.Contains(f.Detail, excludes) {
			t.Errorf("detail must name the excludes file %s: %s", excludes, f.Detail)
		}
	})
	t.Run("present but untracked", func(t *testing.T) {
		repo, _ := substrateRepo(t, block, "")
		f := oneUntrackedFinding(t, substrateTrackingFindings(repo))
		if !strings.Contains(f.Detail, "no file under it is tracked") {
			t.Errorf("detail: %s", f.Detail)
		}
	})
	t.Run("tracked", func(t *testing.T) {
		repo, _ := substrateRepo(t, block, "")
		gitIn(t, repo, "add", ".gitignore", ".satelle")
		gitIn(t, repo, "commit", "-qm", "track the substrate")
		if fs := substrateTrackingFindings(repo); len(fs) != 0 {
			t.Errorf("a tracked substrate has no finding: %+v", fs)
		}
	})
	t.Run("not a git repo", func(t *testing.T) {
		isolateGitExcludes(t, "")
		repo := t.TempDir()
		writeFile(t, repo, ".satelle/satelle.toml", "# x\n")
		if fs := substrateTrackingFindings(repo); len(fs) != 0 {
			t.Errorf("no git repo, no finding: %+v", fs)
		}
	})
}
