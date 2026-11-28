//go:build unix

package gitfacts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

func fakeGit(t *testing.T) (bin, log string) {
	t.Helper()
	bin = t.TempDir()
	log = filepath.Join(t.TempDir(), "env")
	script := "#!/bin/sh\nenv > '" + log + "'\necho /top\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestRepoEnvIsTheWholeEnvironment(t *testing.T) {
	bin, log := fakeGit(t)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_PROBE_VALUE", "s3cret")
	env := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=/nowhere"}
	if top, err := (Repo{Dir: t.TempDir(), Env: env}).TopLevel(); err != nil || top != "/top" {
		t.Fatalf("TopLevel = %q, %v", top, err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if name, _, _ := strings.Cut(line, "="); name != "PWD" && name != "SHLVL" && name != "_" && name != "OLDPWD" {
			got = append(got, line)
		}
	}
	want := append(slices.Clone(env), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("git ran with %q, want %q", got, want)
	}
}

func shimGit(t *testing.T, pre string) (log string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\n" + pre + "exec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func shimLines(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func marker(t *testing.T, dir, name string) (script, mark string) {
	t.Helper()
	mark = filepath.Join(dir, name+".ran")
	script = filepath.Join(dir, name+".sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+mark+"'\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, mark
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

const untrustedOpts = "-c core.fsmonitor=false -c credential.helper= -c core.attributesFile= -c diff.submodule=short -c submodule.recurse=false -c status.submoduleSummary=false"

func twoCommits(t *testing.T) (dir, base, head string) {
	t.Helper()
	dir = t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	gittest.Git(t, dir, "remote", "add", "origin", "https://github.com/o/web")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "one")
	base = gittest.Git(t, dir, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar X = 1\n"), 0o644)
	gittest.Git(t, dir, "commit", "-qam", "two")
	head = gittest.Git(t, dir, "rev-parse", "HEAD")
	return dir, base, head
}

func TestVersionCachedPerBinary(t *testing.T) {
	versionShim := func(banner string, code int) (count string) {
		bin := t.TempDir()
		count = filepath.Join(t.TempDir(), "count")
		script := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = version ] && { echo x >> '" + count + "'; echo '" + banner + "'; exit " + strconv.Itoa(code) + "; }; done\nexit 1\n"
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		return count
	}
	runs := func(count string) int { return len(shimLines(t, count)) }
	r := Repo{Dir: t.TempDir(), Untrusted: true}
	c1 := versionShim("git version 2.50.0", 0)
	for range 2 {
		if v, err := r.Version(); err != nil || v.Raw != "2.50.0" {
			t.Fatalf("Version = %+v, %v", v, err)
		}
	}
	if n := runs(c1); n != 1 {
		t.Fatalf("the first binary ran version %d times, want 1", n)
	}
	versionShim("git version 2.43.0", 0)
	if v, err := r.Version(); err != nil || v.Raw != "2.43.0" {
		t.Fatalf("a second binary: Version = %+v, %v", v, err)
	}
	c3 := versionShim("broken", 1)
	for range 2 {
		if _, err := r.Version(); err == nil {
			t.Fatal("a failing version run gave no error")
		}
	}
	if n := runs(c3); n != 2 {
		t.Fatalf("a failing binary ran version %d times, want 2 (errors are not cached)", n)
	}
}

func TestPartialCloneUnreadable(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 128\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if got, err := (Repo{Dir: t.TempDir(), Untrusted: true}).PartialClone(); err == nil {
		t.Fatalf("PartialClone = %v with git exiting 128, want an error", got)
	}
}

func TestUntrustedRepoArgv(t *testing.T) {
	dir, base, head := twoCommits(t)
	tree := "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	log := shimGit(t, "")
	u := Repo{Dir: dir, Untrusted: true, AttrSource: tree}
	p := "-C " + dir + " --attr-source=" + tree + " " + untrustedOpts + " "
	diffs := []func() error{
		func() error { _, err := u.ChangedPaths(base); return err },
		func() error { _, err := u.Diff(base, nil); return err },
		func() error { _, err := u.ChangedBetween(base, head); return err },
		func() error { _, err := u.DiffBetween(base, head, nil); return err },
		func() error { _, err := u.StatPaths(base); return err },
		func() error { _, err := u.StatBetween(base, head); return err },
	}
	for i, f := range diffs {
		if err := f(); err != nil {
			t.Fatalf("diff %d: %v", i, err)
		}
	}
	for _, l := range shimLines(t, log) {
		if !strings.HasPrefix(l, p+"diff --no-ext-diff --no-textconv --ignore-submodules=dirty --submodule=short ") {
			t.Errorf("untrusted diff argv %q", l)
		}
	}
	os.Remove(log)
	if _, err := u.Dirty(); err != nil {
		t.Fatal(err)
	}
	if l := shimLines(t, log); len(l) != 1 || !strings.HasPrefix(l[0], p+"status --ignore-submodules=dirty ") {
		t.Errorf("untrusted status argv %q", l)
	}
	os.Remove(log)
	others := []func() error{
		func() error { _, err := u.TopLevel(); return err },
		func() error { _, err := u.Head(); return err },
		func() error { _, err := u.Untracked(); return err },
		func() error { _, err := u.Resolve("main"); return err },
		func() error { _, err := u.IsAncestor(base, head); return err },
		func() error { _, err := u.MergeBase(base, head); return err },
		func() error { _, err := u.RemoteURL("origin"); return err },
		func() error { _, err := u.Branch(); return err },
		func() error { _, err := u.IgnoredBy("x"); return err },
		func() error { _, err := u.TrackedFiles(""); return err },
		func() error { _, err := u.ObjectFormat(); return err },
	}
	for i, f := range others {
		if err := f(); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	lines := shimLines(t, log)
	if len(lines) < len(others) {
		t.Fatalf("%d records for %d reads: %q", len(lines), len(others), lines)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, p) {
			t.Errorf("untrusted argv %q lacks %q", l, p)
		}
	}
	os.Remove(log)
	bare := Repo{Dir: dir, Untrusted: true}
	for i, f := range []func() error{
		func() error { _, err := bare.RemoteURL("origin"); return err },
		func() error { _, err := bare.TopLevel(); return err },
		func() error { _, err := bare.ObjectFormat(); return err },
		func() error { _, err := bare.PartialClone(); return err },
	} {
		if err := f(); err != nil {
			t.Fatalf("pre-format read %d: %v", i, err)
		}
	}
	for _, l := range shimLines(t, log) {
		if !strings.HasPrefix(l, "-C "+dir+" "+untrustedOpts+" ") {
			t.Errorf("pre-format argv %q", l)
		}
	}
	os.Remove(log)
	for i, f := range []func() error{
		func() error { _, err := bare.Diff(base, nil); return err },
		func() error { _, err := bare.ChangedPaths(base); return err },
		func() error { _, err := bare.StatPaths(base); return err },
		func() error { _, err := bare.Dirty(); return err },
		func() error { _, err := bare.Untracked(); return err },
		func() error { _, err := bare.IsAncestor(base, head); return err },
		func() error { _, err := bare.Branch(); return err },
		func() error { _, err := bare.IgnoredBy("x"); return err },
	} {
		if err := f(); !errors.Is(err, ErrNoAttrSource) {
			t.Errorf("read %d without an attribute source: %v, want ErrNoAttrSource", i, err)
		}
	}
	if l := shimLines(t, log); len(l) != 0 {
		t.Errorf("reads without an attribute source started git: %q", l)
	}
	trusted := Repo{Dir: dir}
	if _, err := trusted.Diff(base, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := trusted.TopLevel(); err != nil {
		t.Fatal(err)
	}
	got := shimLines(t, log)
	if len(got) != 2 || !strings.HasPrefix(got[0], "-C "+dir+" diff --no-color ") || got[1] != "-C "+dir+" rev-parse --show-toplevel" {
		t.Errorf("trusted argv %q", got)
	}
	for _, l := range got {
		for _, s := range []string{" -c ", "--attr-source", "--no-textconv", "--ignore-submodules", "--submodule="} {
			if strings.Contains(l, s) {
				t.Errorf("trusted argv %q carries %s", l, s)
			}
		}
	}
}

func filterRepo(t *testing.T, attrs string, cfg map[string]string) (r Repo, base string, marks []string) {
	t.Helper()
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte(attrs), 0o644)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "one")
	base = gittest.Git(t, dir, "rev-parse", "HEAD")
	scripts := t.TempDir()
	for key, name := range cfg {
		s, m := marker(t, scripts, name)
		gittest.Git(t, dir, "config", key, s)
		marks = append(marks, m)
	}
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar X = 1\n"), 0o644)
	return Repo{Dir: dir}, base, marks
}

func TestUntrustedRepoRunsNoTextconv(t *testing.T) {
	cfg := map[string]string{"diff.mark.textconv": "textconv", "core.fsmonitor": "fsmonitor", "filter.mark.clean": "clean", "filter.mark.process": "process"}
	r, base, marks := filterRepo(t, "*.go diff=mark filter=mark\n", cfg)
	u := Repo{Dir: r.Dir, Untrusted: true, AttrSource: "4b825dc642cb6eb9a060e54bf8d69288fbee4904"}
	d, err := u.Diff(base, nil)
	if err != nil || !strings.Contains(d, "a.go") {
		t.Fatalf("untrusted Diff = %q, %v", d, err)
	}
	if st, err := u.StatPaths(base); err != nil || len(st) != 1 || st[0].Path != "a.go" {
		t.Fatalf("untrusted StatPaths = %v, %v", st, err)
	}
	if p, err := u.ChangedPaths(base); err != nil || !slices.Equal(p, []string{"a.go"}) {
		t.Fatalf("untrusted ChangedPaths = %v, %v", p, err)
	}
	for _, m := range marks {
		if exists(m) {
			t.Errorf("an untrusted read ran %s", filepath.Base(m))
		}
	}
	if _, err := r.Diff(base, nil); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, m := range marks {
		if exists(m) && (strings.HasPrefix(filepath.Base(m), "textconv") || strings.HasPrefix(filepath.Base(m), "process") || strings.HasPrefix(filepath.Base(m), "clean")) {
			ran++
		}
	}
	if ran < 2 {
		t.Errorf("the trusted diff ran %d of the textconv and filter markers, want the textconv and a filter", ran)
	}
	r2, base2, marks2 := filterRepo(t, "*.go filter=mark\n", map[string]string{"filter.mark.clean": "clean"})
	if _, err := (Repo{Dir: r2.Dir, Untrusted: true, AttrSource: "4b825dc642cb6eb9a060e54bf8d69288fbee4904"}).StatPaths(base2); err != nil {
		t.Fatal(err)
	}
	if exists(marks2[0]) {
		t.Fatal("an untrusted stat ran the clean filter")
	}
	if _, err := r2.StatPaths(base2); err != nil {
		t.Fatal(err)
	}
	if !exists(marks2[0]) {
		t.Fatal("the trusted stat did not run the clean filter, so the fixture proves nothing")
	}
}

func TestUntrustedRepoIgnoresBuiltinAlias(t *testing.T) {
	dir, base, _ := twoCommits(t)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar Y = 2\n"), 0o644)
	mark := filepath.Join(t.TempDir(), "alias.ran")
	for _, a := range []string{"diff", "config", "rev-parse", "version"} {
		gittest.Git(t, dir, "config", "alias."+a, "!touch '"+mark+"'")
	}
	u := Repo{Dir: dir, Untrusted: true, AttrSource: "4b825dc642cb6eb9a060e54bf8d69288fbee4904"}
	if d, err := u.Diff(base, nil); err != nil || !strings.Contains(d, "+var Y = 2") {
		t.Fatalf("Diff = %q, %v", d, err)
	}
	if got, err := u.RemoteURL("origin"); err != nil || got != "https://github.com/o/web" {
		t.Fatalf("RemoteURL = %q, %v", got, err)
	}
	if top, err := u.TopLevel(); err != nil || top != dir {
		t.Fatalf("TopLevel = %q, %v", top, err)
	}
	if v, err := u.Version(); err != nil || v.Major < 2 {
		t.Fatalf("Version = %+v, %v", v, err)
	}
	if exists(mark) {
		t.Fatal("a checkout's alias replaced a builtin")
	}
}

func TestUntrustedRepoSkipsSubmoduleWorkTree(t *testing.T) {
	setup := func(t *testing.T, moved bool) (sup, base string, env []string, marks []string, trace string) {
		t.Helper()
		sub := t.TempDir()
		gittest.Git(t, sub, "init", "-q", "-b", "main")
		os.WriteFile(filepath.Join(sub, ".gitattributes"), []byte("*.txt filter=evil\n"), 0o644)
		os.WriteFile(filepath.Join(sub, "a.txt"), []byte("a\n"), 0o644)
		gittest.Git(t, sub, "add", ".")
		gittest.Git(t, sub, "commit", "-qm", "sub")
		sup = t.TempDir()
		gittest.Git(t, sup, "init", "-q", "-b", "main")
		gittest.Git(t, sup, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "s")
		gittest.Git(t, sup, "commit", "-qm", "add s")
		base = gittest.Git(t, sup, "rev-parse", "HEAD")
		s := filepath.Join(sup, "s")
		if moved {
			os.WriteFile(filepath.Join(s, "b.txt"), []byte("b\n"), 0o644)
			gittest.Git(t, s, "add", ".")
			gittest.Git(t, s, "commit", "-qm", "move")
		}
		home := t.TempDir()
		scripts := t.TempDir()
		clean, m1 := marker(t, scripts, "subclean")
		proc, m2 := marker(t, scripts, "subprocess")
		hclean, m3 := marker(t, scripts, "homeclean")
		hproc, m4 := marker(t, scripts, "homeprocess")
		gittest.Git(t, s, "config", "filter.evil.clean", clean)
		gittest.Git(t, s, "config", "filter.evil.process", proc)
		os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[filter \"evil\"]\n\tclean = "+hclean+"\n\tprocess = "+hproc+"\n[diff]\n\tsubmodule = diff\n"), 0o644)
		os.WriteFile(filepath.Join(s, "a.txt"), []byte("changed\n"), 0o644)
		trace = filepath.Join(t.TempDir(), "trace")
		shimGit(t, "GIT_TRACE='"+trace+"'; export GIT_TRACE\n")
		env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
		return sup, base, env, []string{m1, m2, m3, m4}, trace
	}
	noSpawn := func(t *testing.T, marks []string, trace string) {
		t.Helper()
		for _, m := range marks {
			if exists(m) {
				t.Errorf("%s ran", filepath.Base(m))
			}
		}
		b, _ := os.ReadFile(trace)
		if strings.Contains(string(b), "run_command:") {
			t.Errorf("a child process was started:\n%s", b)
		}
	}
	tree := "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	t.Run("dirty", func(t *testing.T) {
		sup, base, env, marks, trace := setup(t, false)
		u := Repo{Dir: sup, Env: env, Untrusted: true, AttrSource: tree}
		if p, err := u.ChangedPaths(base); err != nil || len(p) != 0 {
			t.Fatalf("ChangedPaths = %v, %v", p, err)
		}
		if st, err := u.StatPaths(base); err != nil || len(st) != 0 {
			t.Fatalf("StatPaths = %v, %v", st, err)
		}
		if d, err := u.Diff(base, nil); err != nil || d != "" {
			t.Fatalf("Diff = %q, %v", d, err)
		}
		if dirty, err := u.Dirty(); err != nil || dirty {
			t.Fatalf("Dirty = %v, %v", dirty, err)
		}
		noSpawn(t, marks, trace)
		if p, err := (Repo{Dir: sup, Env: env}).ChangedPaths(base); err != nil || !slices.Equal(p, []string{"s"}) {
			t.Fatalf("trusted ChangedPaths = %v, %v; the fixture's submodule work tree is not inspected", p, err)
		}
	})
	t.Run("moved", func(t *testing.T) {
		sup, base, env, marks, trace := setup(t, true)
		u := Repo{Dir: sup, Env: env, Untrusted: true, AttrSource: tree}
		if p, err := u.ChangedPaths(base); err != nil || !slices.Equal(p, []string{"s"}) {
			t.Fatalf("ChangedPaths = %v, %v", p, err)
		}
		if st, err := u.StatPaths(base); err != nil || len(st) != 1 || st[0].Path != "s" {
			t.Fatalf("StatPaths = %v, %v", st, err)
		}
		if d, err := u.Diff(base, nil); err != nil || !strings.Contains(d, "-Subproject commit") || !strings.Contains(d, "+Subproject commit") {
			t.Fatalf("Diff = %q, %v", d, err)
		}
		if dirty, err := u.Dirty(); err != nil || !dirty {
			t.Fatalf("Dirty = %v, %v", dirty, err)
		}
		noSpawn(t, marks, trace)
	})
}
