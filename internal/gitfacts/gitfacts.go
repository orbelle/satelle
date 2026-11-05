// Package gitfacts is the kernel's one git adapter. It runs read-only git
// commands and reports facts; it never changes a repository.
package gitfacts

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// Repo is a working tree at Dir.
type Repo struct{ Dir string }

func (r Repo) git(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", r.Dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// TopLevel returns the root of the working tree.
func (r Repo) TopLevel() (string, error) {
	out, err := r.git("rev-parse", "--show-toplevel")
	return strings.TrimSpace(out), err
}

// Head returns the commit HEAD points at.
func (r Repo) Head() (string, error) {
	out, err := r.git("rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// ChangedPaths lists the paths that differ from base: committed and
// uncommitted changes to tracked files, and untracked files not ignored.
// Sorted, repo-relative, slash-separated.
func (r Repo) ChangedPaths(base string) ([]string, error) {
	diff, err := r.git("diff", "--name-only", "--no-renames", base, "--")
	if err != nil {
		return nil, err
	}
	untracked, err := r.git("ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(diff+untracked, "\n") {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Diff returns `git diff base -- paths` (every path when paths is empty).
func (r Repo) Diff(base string, paths []string) (string, error) {
	return r.git(append([]string{"diff", "--no-color", "--no-ext-diff", base, "--"}, paths...)...)
}

// Dirty reports whether the working tree has uncommitted changes or
// untracked files.
func (r Repo) Dirty() (bool, error) {
	out, err := r.git("status", "--porcelain")
	return strings.TrimSpace(out) != "", err
}
