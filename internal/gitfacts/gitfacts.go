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

// CommonDir returns the absolute git common directory: the main repository's
// .git for every linked worktree of it.
func (r Repo) CommonDir() (string, error) {
	out, err := r.git("rev-parse", "--path-format=absolute", "--git-common-dir")
	return strings.TrimSpace(out), err
}

// IsLinked reports whether the tree is a linked worktree (its git dir is not
// the common dir).
func (r Repo) IsLinked() bool {
	out, err := r.git("rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return false
	}
	f := strings.Fields(out)
	return len(f) == 2 && f[0] != f[1]
}

// Resolve returns the commit ref names: the ref itself, else the branch of
// that name on origin. An error when neither names a commit.
func (r Repo) Resolve(ref string) (string, error) {
	var last error
	for _, cand := range []string{ref, "refs/remotes/origin/" + ref} {
		out, err := r.git("rev-parse", "--verify", "--quiet", "--end-of-options", cand+"^{commit}")
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("no commit")
	}
	return "", fmt.Errorf("resolve %s: %w", ref, last)
}

// MergeBase returns the best common ancestor of a and b.
func (r Repo) MergeBase(a, b string) (string, error) {
	out, err := r.git("merge-base", a, b)
	return strings.TrimSpace(out), err
}

// ChangedBetween lists the paths that differ between two commits, sorted.
func (r Repo) ChangedBetween(base, head string) ([]string, error) {
	out, err := r.git("diff", "--name-only", "--no-renames", base, head, "--")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// DiffBetween returns `git diff base head -- paths` (every path when paths is
// empty).
func (r Repo) DiffBetween(base, head string, paths []string) (string, error) {
	return r.git(append([]string{"diff", "--no-color", "--no-ext-diff", base, head, "--"}, paths...)...)
}
