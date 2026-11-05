package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAttachBody covers `story attach`'s body resolution (sty_97c53d72): --file
// reads the body from a file, --body passes through, and a missing file is a
// clear error (the --body/--file conflict is enforced by cobra's
// MarkFlagsMutuallyExclusive on the command).
func TestAttachBody(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "summary.md")
	if err := os.WriteFile(p, []byte("# from file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	empty := strings.NewReader("")
	if got, err := attachBody(empty, "inline", ""); err != nil || got != "inline" {
		t.Errorf("body passthrough = (%q, %v), want (inline, nil)", got, err)
	}
	if got, err := attachBody(empty, "", p); err != nil || got != "# from file\n" {
		t.Errorf("file read = (%q, %v), want file content", got, err)
	}
	if _, err := attachBody(empty, "", filepath.Join(dir, "absent.md")); err == nil || !strings.Contains(err.Error(), "read --file") {
		t.Errorf("missing file should error with path context, got %v", err)
	}
	// sty_ef8a896b: "-" reads the command's stdin instead of a file named "-".
	if got, err := attachBody(strings.NewReader("# piped\n"), "", "-"); err != nil || got != "# piped\n" {
		t.Errorf(`attachBody(--file -) = (%q, %v), want the piped body`, got, err)
	}
}

// untrackedFiles walks repo (excluding .satelle, satelle's own store/runtime
// dir) and returns the repo-relative file paths present — used to assert a
// CLI operation created no stray file in the tree. tempRepo fixtures are not
// git repos, so this is a plain filesystem snapshot, not `git ls-files`.
func untrackedFiles(t *testing.T, repo string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(repo, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(repo, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			if rel == ".satelle" {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	return files
}
