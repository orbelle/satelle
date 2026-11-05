package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitPointerIsGitIgnored_TrackedSatelle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git absent")
	}
	repo := t.TempDir()
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatal(err)
	}
	// sty_dbc0be85: a fresh init ignores .satelle/ as a whole, which covers the
	// pointer; the check-ignore below is the proof.
	for _, args := range [][]string{{"init"}, {"check-ignore", ".satelle/logs"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil && args[0] == "check-ignore" {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		} else if err != nil && args[0] == "init" {
			t.Fatalf("git init: %v\n%s", err, out)
		}
	}
}

// A repo that tracks part of .satelle/ (a negation here) keeps its own setting;
// init adds only the pointer line, which is what keeps the symlink uncommitted.
func TestInitPointerListed_PartlyTrackedSatelle(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".satelle/*\n!.satelle/workflows/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatal(err)
	}
	gi, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if !strings.HasPrefix(string(gi), ".satelle/*\n!.satelle/workflows/\n") {
		t.Fatalf("the operator's lines must stay as written:\n%s", gi)
	}
	if !strings.Contains(string(gi), ".satelle/logs\n") {
		t.Fatalf("pointer line missing:\n%s", gi)
	}
}

func TestScaffoldTomlNamesLogsLocationAndPointer(t *testing.T) {
	if !strings.Contains(scaffoldToml, "~/.satelle/<repo-key>/logs") {
		t.Error("seed must name runtime location")
	}
	if !strings.Contains(scaffoldToml, ".satelle/logs") {
		t.Error("seed must name the pointer")
	}
}

func TestLogsCommentGuard(t *testing.T) {
	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			trim := strings.TrimSpace(line)
			if !strings.HasPrefix(trim, "//") && !strings.HasPrefix(trim, "*") {
				continue
			}
			if !strings.Contains(line, ".satelle/logs") {
				continue
			}
			if strings.Contains(line, "pointer") || strings.Contains(line, "symlink") {
				continue
			}
			t.Errorf("%s:%d storage claim without pointer: %s", path, i+1, trim)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
