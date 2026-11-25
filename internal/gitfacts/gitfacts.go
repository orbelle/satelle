package gitfacts

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

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

func (r Repo) TopLevel() (string, error) {
	out, err := r.git("rev-parse", "--show-toplevel")
	top := strings.TrimSpace(out)
	if err != nil || top == "" {
		return "", err
	}
	return filepath.Clean(filepath.FromSlash(top)), nil
}

func (r Repo) Head() (string, error) {
	out, err := r.git("rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

func (r Repo) ChangedPaths(base string) ([]string, error) {
	out, err := r.git("diff", "--name-only", "--no-renames", base, "--")
	if err != nil {
		return nil, err
	}
	return pathLines(out), nil
}

func (r Repo) Untracked() ([]string, error) {
	out, err := r.git("ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return pathLines(out), nil
}

func pathLines(out string) []string {
	var paths []string
	for _, p := range strings.Split(out, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

var diffText = []string{"diff", "--no-color", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/", "--diff-algorithm=myers"}

func (r Repo) Diff(base string, paths []string) (string, error) {
	return r.git(append(append(slices.Clone(diffText), base, "--"), paths...)...)
}

func (r Repo) Dirty() (bool, error) {
	out, err := r.git("status", "--porcelain")
	return strings.TrimSpace(out) != "", err
}

func (r Repo) CommonDir() (string, error) {
	out, err := r.git("rev-parse", "--path-format=absolute", "--git-common-dir")
	return strings.TrimSpace(out), err
}

func (r Repo) IsLinked() (bool, error) {
	out, err := r.git("rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return false, err
	}
	return linkedFrom(out)
}

func linkedFrom(out string) (bool, error) {
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return false, fmt.Errorf("git rev-parse --git-dir --git-common-dir: unreadable output %q", out)
	}
	return filepath.Clean(lines[0]) != filepath.Clean(lines[1]), nil
}

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

func (r Repo) IgnoredBy(paths ...string) (map[string]string, error) {
	out := map[string]string{}
	if len(paths) == 0 {
		return out, nil
	}
	cmd := exec.Command("git", append([]string{"-C", r.Dir, "check-ignore", "-v", "--"}, paths...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var stdout, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &errb
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			return nil, fmt.Errorf("git check-ignore: %w: %s", err, strings.TrimSpace(errb.String()))
		}
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		rule, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		i := strings.LastIndex(rule, ":")
		if i < 0 {
			continue
		}
		where, pattern := rule[:i], rule[i+1:]
		if strings.HasPrefix(pattern, "!") {
			continue
		}
		out[path] = where + ": " + pattern
	}
	return out, nil
}

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

func (r Repo) MergeBase(a, b string) (string, error) {
	out, err := r.git("merge-base", a, b)
	return strings.TrimSpace(out), err
}

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

func (r Repo) DiffBetween(base, head string, paths []string) (string, error) {
	return r.git(append(append(slices.Clone(diffText), base, head, "--"), paths...)...)
}

type FileStat struct {
	Path           string
	Added, Removed int
	Binary         bool
}

var numstatArgs = []string{"diff", "--no-color", "--no-ext-diff", "--diff-algorithm=myers", "--numstat", "--no-renames"}

func (r Repo) StatPaths(base string) ([]FileStat, error) {
	out, err := r.git(append(slices.Clone(numstatArgs), base, "--")...)
	if err != nil {
		return nil, err
	}
	return numstat(out)
}

func (r Repo) StatBetween(base, head string) ([]FileStat, error) {
	out, err := r.git(append(slices.Clone(numstatArgs), base, head, "--")...)
	if err != nil {
		return nil, err
	}
	return numstat(out)
}

func numstat(out string) ([]FileStat, error) {
	var stats []FileStat
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		added, rest, ok := strings.Cut(line, "\t")
		removed, path, ok2 := strings.Cut(rest, "\t")
		if !ok || !ok2 || path == "" {
			return nil, fmt.Errorf("git diff --numstat: unreadable line %q", line)
		}
		if added == "-" && removed == "-" {
			stats = append(stats, FileStat{Path: path, Binary: true})
			continue
		}
		a, aerr := strconv.Atoi(added)
		d, derr := strconv.Atoi(removed)
		if aerr != nil || derr != nil {
			return nil, fmt.Errorf("git diff --numstat: unreadable line %q", line)
		}
		stats = append(stats, FileStat{Path: path, Added: a, Removed: d})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Path < stats[j].Path })
	return stats, nil
}

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

func (r Repo) Tracks(path string) (bool, error) {
	files, err := r.TrackedFiles(path)
	if err == nil {
		return len(files) > 0, nil
	}
	dir, aerr := filepath.Abs(r.Dir)
	if aerr != nil {
		return false, err
	}
	for {
		if _, serr := os.Lstat(filepath.Join(dir, ".git")); serr == nil {
			return false, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		dir = parent
	}
}

func (r Repo) Bundle(file, ref string) error {
	_, err := r.git("bundle", "create", "-q", file, ref)
	return err
}

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
