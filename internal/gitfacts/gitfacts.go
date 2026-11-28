package gitfacts

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Repo struct {
	Dir        string
	Env        []string
	Untrusted  bool
	AttrSource string
}

var ErrNoAttrSource = errors.New("untrusted git read has no attribute source")

var untrustedConfig = []string{
	"-c", "core.fsmonitor=false",
	"-c", "credential.helper=",
	"-c", "core.attributesFile=",
	"-c", "diff.submodule=short",
	"-c", "submodule.recurse=false",
	"-c", "status.submoduleSummary=false",
}

func (r Repo) command(args ...string) (*exec.Cmd, error) {
	argv := []string{"-C", r.Dir}
	if r.Untrusted {
		if r.AttrSource != "" {
			argv = append(argv, "--attr-source="+r.AttrSource)
		} else if len(args) == 0 || !slices.Contains([]string{"config", "rev-parse", "version"}, args[0]) {
			return nil, ErrNoAttrSource
		}
		argv = append(argv, untrustedConfig...)
		switch {
		case len(args) > 0 && args[0] == "diff":
			args = append([]string{"diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty", "--submodule=short"}, args[1:]...)
		case len(args) > 0 && args[0] == "status":
			args = append([]string{"status", "--ignore-submodules=dirty"}, args[1:]...)
		}
	}
	cmd := exec.Command("git", append(argv, args...)...)
	cmd.Env = r.environ()
	return cmd, nil
}

func (r Repo) environ() []string {
	env := r.Env
	if env == nil {
		env = os.Environ()
	}
	return append(slices.Clone(env), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
}

func (r Repo) git(args ...string) (string, error) {
	cmd, err := r.command(args...)
	if err != nil {
		return "", err
	}
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
	cmd, err := r.command("merge-base", "--is-ancestor", a, b)
	if err != nil {
		return false, err
	}
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err = cmd.Run()
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
	cmd, err := r.command(append([]string{"check-ignore", "-v", "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
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

var ErrNoRemote = errors.New("no such remote")

func (r Repo) RemoteURL(name string) (string, error) {
	out, err := r.git("config", "--local", "--get", "remote."+name+".url")
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", ErrNoRemote
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r Repo) Branch() (string, error) {
	cmd, err := r.command("symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return "", err
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
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
	if in, aerr := r.inWorkTree(); aerr != nil || in {
		return false, err
	}
	return false, nil
}

func (r Repo) inWorkTree() (bool, error) {
	dir, err := filepath.Abs(r.Dir)
	if err != nil {
		return false, err
	}
	for {
		if _, serr := os.Lstat(filepath.Join(dir, ".git")); serr == nil {
			return true, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		dir = parent
	}
}

var objectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

var ErrUnbornWithRefs = errors.New("HEAD has no commit but the repository has refs")

func (r Repo) HeadCommit() (string, bool, error) {
	out, err := r.git("rev-parse", "--verify", "--quiet", "--end-of-options", "HEAD^{commit}")
	if err == nil {
		oid := strings.TrimSpace(out)
		if !objectID.MatchString(oid) {
			return "", false, fmt.Errorf("git rev-parse HEAD: unreadable commit %q", oid)
		}
		return oid, true, nil
	}
	if _, gerr := r.git("rev-parse", "--git-dir"); gerr != nil {
		if in, aerr := r.inWorkTree(); aerr == nil && !in {
			return "", false, nil
		}
		return "", false, gerr
	}
	refs, rerr := r.git("for-each-ref", "--count=1", "--format=%(refname)")
	if rerr != nil {
		return "", false, rerr
	}
	if strings.TrimSpace(refs) == "" {
		return "", false, nil
	}
	return "", false, ErrUnbornWithRefs
}

type TreeEntry struct {
	Mode string
	Type string
	OID  string
	Path string
}

func (r Repo) Tree(rev string, paths ...string) ([]TreeEntry, error) {
	if !objectID.MatchString(rev) {
		return nil, fmt.Errorf("git ls-tree: %q is not an object id", rev)
	}
	out, err := r.git(append([]string{"ls-tree", "-r", "-t", "-z", rev, "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, path, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || path == "" || len(f) != 3 || !objectID.MatchString(f[2]) {
			return nil, fmt.Errorf("git ls-tree: unreadable record %q", rec)
		}
		entries = append(entries, TreeEntry{Mode: f[0], Type: f[1], OID: f[2], Path: path})
	}
	return entries, nil
}

func (r Repo) Blobs(oids ...string) (map[string][]byte, error) {
	out := map[string][]byte{}
	if len(oids) == 0 {
		return out, nil
	}
	for _, oid := range oids {
		if !objectID.MatchString(oid) {
			return nil, fmt.Errorf("git cat-file: %q is not an object id", oid)
		}
	}
	cmd, err := r.command("cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	var stdout, errb bytes.Buffer
	cmd.Stdin = strings.NewReader(strings.Join(oids, "\n") + "\n")
	cmd.Stdout, cmd.Stderr = &stdout, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git cat-file --batch: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	b := stdout.Bytes()
	for range oids {
		line, rest, ok := bytes.Cut(b, []byte("\n"))
		f := strings.Fields(string(line))
		if !ok || len(f) != 3 || f[1] != "blob" || !objectID.MatchString(f[0]) {
			return nil, fmt.Errorf("git cat-file --batch: unreadable header %q", line)
		}
		n, err := strconv.Atoi(f[2])
		if err != nil || n < 0 || len(rest) < n+1 || rest[n] != '\n' {
			return nil, fmt.Errorf("git cat-file --batch: unreadable object %s", f[0])
		}
		out[f[0]] = slices.Clone(rest[:n])
		b = rest[n+1:]
	}
	return out, nil
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

func EmptyTree(format string) (string, error) {
	switch format {
	case "sha1":
		return "4b825dc642cb6eb9a060e54bf8d69288fbee4904", nil
	case "sha256":
		return "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321", nil
	}
	return "", fmt.Errorf("object format %s is neither sha1 nor sha256", format)
}

func (r Repo) ObjectFormat() (string, error) {
	out, err := r.git("rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	f := strings.TrimSpace(out)
	if f == "" {
		return "", errors.New("git rev-parse --show-object-format: empty output")
	}
	return f, nil
}

type Version struct {
	Major, Minor, Patch int
	Raw                 string
}

func (v Version) AtLeast(major, minor int) bool {
	if v.Major != major {
		return v.Major > major
	}
	return v.Minor >= minor
}

func ParseVersion(out string) (Version, error) {
	bad := fmt.Errorf("unreadable git version %q", out)
	rest, ok := strings.CutPrefix(strings.TrimSpace(out), "git version ")
	fields := strings.Fields(rest)
	if !ok || len(fields) == 0 {
		return Version{}, bad
	}
	v := Version{Raw: fields[0]}
	parts := strings.Split(v.Raw, ".")
	for i, dst := range []*int{&v.Major, &v.Minor, &v.Patch} {
		if i >= len(parts) || !allDigits(parts[i]) {
			if i < 2 {
				return Version{}, bad
			}
			break
		}
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return Version{}, bad
		}
		*dst = n
	}
	return v, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

var (
	versionMu    sync.Mutex
	versionCache = map[string]Version{}
)

func (r Repo) Version() (Version, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return Version{}, err
	}
	versionMu.Lock()
	v, ok := versionCache[bin]
	versionMu.Unlock()
	if ok {
		return v, nil
	}
	out, err := r.git("version")
	if err != nil {
		return Version{}, err
	}
	if v, err = ParseVersion(out); err != nil {
		return Version{}, err
	}
	versionMu.Lock()
	versionCache[bin] = v
	versionMu.Unlock()
	return v, nil
}

func (r Repo) PartialClone() (bool, error) {
	out, err := r.git("config", "--includes", "--null", "--get-regexp", `^(extensions\.partialclone|remote\..+\.(promisor|partialclonefilter))$`)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		key, val, hasVal := strings.Cut(rec, "\n")
		switch {
		case key == "extensions.partialclone", strings.HasSuffix(key, ".partialclonefilter"):
			return true, nil
		case strings.HasSuffix(key, ".promisor") && (!hasVal || !gitFalse(val)):
			return true, nil
		}
	}
	return false, nil
}

func gitFalse(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "no", "off", "0", "":
		return true
	}
	return false
}
