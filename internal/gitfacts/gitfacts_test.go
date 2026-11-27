package gitfacts

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

func TestFacts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n"), 0o644)
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "one")
	r := Repo{Dir: dir}
	head, err := r.Head()
	if err != nil || len(head) != 40 {
		t.Fatalf("head %q %v", head, err)
	}
	top, err := r.TopLevel()
	if err != nil {
		t.Fatalf("toplevel: %v", err)
	}
	if want, _ := filepath.EvalSymlinks(dir); top != want {
		t.Fatalf("toplevel %q want %q", top, want)
	}
	if dirty, _ := r.Dirty(); dirty {
		t.Fatal("clean tree reported dirty")
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("2\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "d"), 0o755)
	os.WriteFile(filepath.Join(dir, "d", "b.go"), []byte("x"), 0o644)
	gittest.Git(t, dir, "add", "--", "d/b.go")
	os.MkdirAll(filepath.Join(dir, "u"), 0o755)
	os.WriteFile(filepath.Join(dir, "u", "scratch.txt"), []byte("s"), 0o644)
	os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("*.log\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "build.log"), []byte("l"), 0o644)
	paths, err := r.ChangedPaths(head)
	if err != nil || strings.Join(paths, ",") != "a.txt,d/b.go" {
		t.Fatalf("changed %v %v; want the modified and the staged file only", paths, err)
	}
	untracked, err := r.Untracked()
	if err != nil || strings.Join(untracked, ",") != "u/scratch.txt" {
		t.Fatalf("untracked %v %v; want the untracked file only", untracked, err)
	}
	diff, _ := r.Diff(head, []string{"a.txt"})
	if !strings.Contains(diff, "+2") {
		t.Fatalf("diff %q", diff)
	}
	if dirty, _ := r.Dirty(); !dirty {
		t.Fatal("dirty tree reported clean")
	}
}

func TestIsAncestor(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	commit := func(name string) string {
		os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644)
		gittest.Git(t, dir, "add", ".")
		gittest.Git(t, dir, "commit", "-qm", name)
		h, _ := Repo{Dir: dir}.Head()
		return h
	}
	r := Repo{Dir: dir}
	root := commit("a")
	gittest.Git(t, dir, "checkout", "-q", "-b", "side")
	side := commit("s")
	gittest.Git(t, dir, "checkout", "-q", "main")
	gittest.Git(t, dir, "checkout", "-q", "-b", "feat")
	feat := commit("f")
	for _, c := range []struct {
		a, b string
		want bool
	}{{root, feat, true}, {feat, feat, true}, {side, feat, false}} {
		if got, err := r.IsAncestor(c.a, c.b); err != nil || got != c.want {
			t.Errorf("IsAncestor(%.7s, %.7s) = %v, %v; want %v", c.a, c.b, got, err, c.want)
		}
	}
	gittest.Git(t, dir, "rebase", "-q", "side")
	head, _ := r.Head()
	if in, err := r.IsAncestor(feat, head); err != nil || in {
		t.Errorf("the pre-rebase commit is in the rebased history: %v %v", in, err)
	}
	if _, err := r.IsAncestor(strings.Repeat("0", 40), head); err == nil {
		t.Error("an unknown commit is not an error")
	}
}

func TestBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "trunk")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "one")
	r := Repo{Dir: dir}
	if b, err := r.Branch(); err != nil || b != "trunk" {
		t.Fatalf("on a branch: %q %v", b, err)
	}
	head, _ := r.Head()
	gittest.Git(t, dir, "checkout", "-q", "--detach", head)
	if b, err := r.Branch(); err != nil || b != "" {
		t.Fatalf("detached: %q %v", b, err)
	}
}

func TestCleanTreeDiffEqualsDiffBetweenHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n"), 0o644)
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "one")
	r := Repo{Dir: dir}
	base, _ := r.Head()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("2\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "d"), 0o755)
	os.WriteFile(filepath.Join(dir, "d", "b.go"), []byte("package d\n"), 0o644)
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "two")
	head, _ := r.Head()
	for _, paths := range [][]string{nil, {"d/b.go"}} {
		tree, err := r.Diff(base, paths)
		if err != nil {
			t.Fatal(err)
		}
		between, err := r.DiffBetween(base, head, paths)
		if err != nil {
			t.Fatal(err)
		}
		if tree == "" || tree != between {
			t.Fatalf("paths %v: tree diff\n%s\n!= diff between base and HEAD\n%s", paths, tree, between)
		}
	}
}

func diffRepo(t *testing.T, cfg ...string) (r Repo, base, head string) {
	t.Helper()
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	for i := 0; i+1 < len(cfg); i += 2 {
		gittest.Git(t, dir, "config", cfg[i], cfg[i+1])
	}
	r = Repo{Dir: dir}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\nb\nc\na\nb\nc\n"), 0o644)
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "base")
	base, _ = r.Head()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("c\nb\na\nb\nc\na\n"), 0o644)
	gittest.Git(t, dir, "commit", "-qam", "head")
	head, _ = r.Head()
	return r, base, head
}

func TestDiffTextIgnoresTheUsersDiffConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	plain, pb, ph := diffRepo(t, "diff.algorithm", "myers")
	want, err := plain.DiffBetween(pb, ph, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(want, "--- a/a.txt\n+++ b/a.txt\n") {
		t.Fatalf("plain diff has no a/ b/ prefixes:\n%s", want)
	}
	for _, cfg := range [][]string{
		{"diff.mnemonicPrefix", "true"},
		{"diff.noprefix", "true"},
		{"diff.srcPrefix", "x/", "diff.dstPrefix", "y/"},
		{"diff.algorithm", "histogram"},
		{"diff.algorithm", "patience", "diff.mnemonicPrefix", "true"},
	} {
		r, base, head := diffRepo(t, cfg...)
		tree, err := r.Diff(base, nil)
		if err != nil {
			t.Fatal(err)
		}
		between, err := r.DiffBetween(base, head, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tree != want || between != want {
			t.Errorf("config %v:\ntree diff\n%s\ndiff between commits\n%s\nwant\n%s", cfg, tree, between, want)
		}
	}
}

func TestIgnoredBy(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "hidden/\nkept/*\n!kept/back.json\ntracked/\n")
	for _, f := range []string{"hidden/a.json", "plain/a.json", "kept/back.json", "tracked/a.json"} {
		write(f, "{}\n")
	}
	gittest.Git(t, dir, "add", "-f", "tracked/a.json")
	got, err := Repo{Dir: dir}.IgnoredBy("hidden/a.json", "plain/a.json", "kept/back.json", "tracked/a.json", "absent/a.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["hidden/a.json"] != ".gitignore:1: hidden/" {
		t.Fatalf("ignored = %v, want only hidden/a.json by .gitignore:1: hidden/", got)
	}
	if got, err := (Repo{Dir: dir}).IgnoredBy(); err != nil || len(got) != 0 {
		t.Fatalf("no paths: %v %v", got, err)
	}
	if _, err := (Repo{Dir: t.TempDir()}).IgnoredBy("a"); err == nil {
		t.Fatal("outside a repository: want an error")
	}
}

func TestTracks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n"), 0o644)
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "one")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("2\n"), 0o644)
	r := Repo{Dir: dir}
	if ok, err := r.Tracks("a.txt"); !ok || err != nil {
		t.Errorf("Tracks(committed) = %v, %v; want true", ok, err)
	}
	if ok, err := r.Tracks("b.txt"); ok || err != nil {
		t.Errorf("Tracks(untracked) = %v, %v; want false", ok, err)
	}
	plain := t.TempDir()
	if ok, err := (Repo{Dir: plain}).Tracks("a.txt"); ok || err != nil {
		t.Errorf("Tracks(no repository) = %v, %v; want false, nil", ok, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Tracks("a.txt"); err == nil {
		t.Error("Tracks over a garbage index answered; want an error")
	}
}

func TestTopLevelOutsideARepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	top, err := Repo{Dir: t.TempDir()}.TopLevel()
	if err == nil || top != "" {
		t.Fatalf("toplevel %q %v; want an error and the empty string", top, err)
	}
}

func TestStat(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	r := Repo{Dir: dir}
	put := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("a.txt", "1\n2\n3\n")
	put("gone.txt", "x\ny\n")
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "base")
	base, _ := r.Head()

	put("a.txt", "1\nTWO\n3\n4\n")
	put("new.txt", "n\n")
	put("logo.bin", "\x00\x01\x02\x00")
	os.Remove(filepath.Join(dir, "gone.txt"))
	gittest.Git(t, dir, "add", "new.txt", "logo.bin")

	want := []FileStat{
		{Path: "a.txt", Added: 2, Removed: 1},
		{Path: "gone.txt", Removed: 2},
		{Path: "logo.bin", Binary: true},
		{Path: "new.txt", Added: 1},
	}
	tree, err := r.StatPaths(base)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tree, want) {
		t.Fatalf("StatPaths = %+v, want %+v", tree, want)
	}
	paths, err := r.ChangedPaths(base)
	if err != nil {
		t.Fatal(err)
	}
	var statPaths []string
	for _, s := range tree {
		statPaths = append(statPaths, s.Path)
	}
	if !reflect.DeepEqual(statPaths, paths) {
		t.Fatalf("stat paths %v, changed paths %v", statPaths, paths)
	}

	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-qm", "head")
	head, _ := r.Head()
	between, err := r.StatBetween(base, head)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(between, want) {
		t.Fatalf("StatBetween = %+v, want %+v", between, want)
	}

	plain, pb, ph := diffRepo(t, "diff.algorithm", "myers")
	myers, err := plain.StatBetween(pb, ph)
	if err != nil {
		t.Fatal(err)
	}
	for _, alg := range []string{"histogram", "patience"} {
		cr, cb, ch := diffRepo(t, "diff.algorithm", alg)
		got, err := cr.StatBetween(cb, ch)
		if err != nil {
			t.Fatal(err)
		}
		tree, err := cr.StatPaths(cb)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, myers) || !reflect.DeepEqual(tree, myers) {
			t.Errorf("diff.algorithm=%s: between %+v, tree %+v, want %+v", alg, got, tree, myers)
		}
	}
}

func TestIsLinked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	tmp := t.TempDir()
	main := filepath.Join(tmp, "main repo")
	os.MkdirAll(main, 0o755)
	gittest.Git(t, main, "init", "-q")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("1\n"), 0o644)
	gittest.Git(t, main, "add", ".")
	gittest.Git(t, main, "commit", "-qm", "one")
	wt := filepath.Join(tmp, "linked wt")
	gittest.Git(t, main, "worktree", "add", "-q", wt, "-b", "wt")
	if linked, err := (Repo{Dir: main}).IsLinked(); linked || err != nil {
		t.Fatalf("main checkout: linked %v, %v", linked, err)
	}
	if linked, err := (Repo{Dir: wt}).IsLinked(); !linked || err != nil {
		t.Fatalf("linked worktree: linked %v, %v", linked, err)
	}
	if _, err := (Repo{Dir: t.TempDir()}).IsLinked(); err == nil {
		t.Fatal("a directory outside git gave no error")
	}
}

func TestLinkedFrom(t *testing.T) {
	for _, c := range []struct {
		out     string
		linked  bool
		wantErr bool
	}{
		{"/a b/.git\n/a b/.git\n", false, false},
		{"/a b/.git/worktrees/w x\n/a b/.git\n", true, false},
		{"/a b/.git\r\n/a b/.git\r\n", false, false},
		{"", false, true},
		{"/a b/.git\n", false, true},
		{"/a\n/b\n/c\n", false, true},
		{"/a b/.git\n\n", false, true},
	} {
		linked, err := linkedFrom(c.out)
		if linked != c.linked || (err != nil) != c.wantErr {
			t.Errorf("linkedFrom(%q) = %v, %v; want %v, error %v", c.out, linked, err, c.linked, c.wantErr)
		}
	}
}

func TestRemoteURL(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	r := Repo{Dir: dir}
	if got, err := r.RemoteURL("origin"); got != "" || err != ErrNoRemote {
		t.Fatalf("no origin: %q, %v; want ErrNoRemote", got, err)
	}
	gittest.Git(t, dir, "remote", "add", "origin", "git@github.com:o/r.git")
	if got, err := r.RemoteURL("origin"); err != nil || got != "git@github.com:o/r.git" {
		t.Fatalf("origin: %q, %v", got, err)
	}
}

func TestIgnoredByHonoursEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	excludes := filepath.Join(t.TempDir(), "excludes")
	if err := os.WriteFile(excludes, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.excludesFile", "GIT_CONFIG_VALUE_0="+excludes)
	got, err := Repo{Dir: dir, Env: env}.IgnoredBy("x")
	if err != nil || !strings.Contains(got["x"], "excludes") {
		t.Fatalf("IgnoredBy with the given env = %v, %v; want the excludes rule", got, err)
	}
	if got, err := (Repo{Dir: dir, Env: append(os.Environ(), "GIT_CONFIG_VALUE_0="+os.DevNull)}).IgnoredBy("x"); err != nil || got["x"] != "" {
		t.Fatalf("IgnoredBy without the rule = %v, %v", got, err)
	}
}
