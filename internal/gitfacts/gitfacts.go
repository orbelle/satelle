// Package gitfacts is the kernel's one git adapter. It runs read-only git
// commands and reports facts; it never changes a repository.
package gitfacts

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"slices"
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

// diffText are the flags every diff whose text is hashed runs with: the text
// must not depend on the user's diff config (diff.mnemonicPrefix,
// diff.noprefix, diff.srcPrefix and diff.dstPrefix, diff.algorithm), so a
// change diffed in its own tree and between its commits, or on another
// machine, is the same text.
var diffText = []string{"diff", "--no-color", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/", "--diff-algorithm=myers"}

// Diff returns `git diff base -- paths` (every path when paths is empty), in
// the canonical text diffText fixes.
func (r Repo) Diff(base string, paths []string) (string, error) {
	return r.git(append(append(slices.Clone(diffText), base, "--"), paths...)...)
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

// IsAncestor reports whether commit a is in b's history (a commit is its own
// ancestor). An error when either names no commit here.
func (r Repo) IsAncestor(a, b string) (bool, error) {
	cmd := exec.Command("git", "-C", r.Dir, "merge-base", "--is-ancestor", a, b)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w: %s", a, b, err, strings.TrimSpace(errb.String()))
}

// Branch returns the checkout's current branch, "" on a detached HEAD.
func (r Repo) Branch() (string, error) {
	cmd := exec.Command("git", "-C", r.Dir, "symbolic-ref", "--short", "-q", "HEAD")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err == nil {
		return strings.TrimSpace(out.String()), nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return "", nil
	}
	return "", fmt.Errorf("git symbolic-ref HEAD: %w: %s", err, strings.TrimSpace(errb.String()))
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
// empty), in the canonical text diffText fixes.
func (r Repo) DiffBetween(base, head string, paths []string) (string, error) {
	return r.git(append(append(slices.Clone(diffText), base, head, "--"), paths...)...)
}

// TrackedFiles lists the files git tracks under path (relative to Dir; "" or
// "." is every file), repo-relative and slash-separated, sorted.
func (r Repo) TrackedFiles(path string) ([]string, error) {
	if path == "" {
		path = "."
	}
	out, err := r.git("ls-files", "-z", "--", path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	return files, nil
}

// Bundle writes a git bundle of ref (with its history) to file, outside the
// repository; the repository itself is not changed.
func (r Repo) Bundle(file, ref string) error {
	_, err := r.git("bundle", "create", "-q", file, ref)
	return err
}

// ShallowCommits lists the commits a shallow clone's history ends at (its
// shallow file, shared by every worktree of the repository); none for a
// full clone.
func (r Repo) ShallowCommits() ([]string, error) {
	p, err := r.git("rev-parse", "--path-format=absolute", "--git-path", "shallow")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(strings.TrimSpace(p))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(b)), nil
}
