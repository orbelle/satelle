//go:build !windows

package scripts_test

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHermeticWrapperEnvironment(t *testing.T) {
	callerHome := t.TempDir()
	callerSatelleHome := filepath.Join(t.TempDir(), "x")
	callerEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + callerHome,
		"GOPROXY=off",
		"SATELLE_HOME=" + callerSatelleHome,
		"SATELLE_TEST_PROBE_X=1",
		"UNLISTED=1",
	}
	callerEnv = append(callerEnv, optIn()...)
	goEnv := exec.Command("go", "env", "GOMODCACHE")
	goEnv.Env = callerEnv
	modOut, err := goEnv.Output()
	if err != nil {
		t.Fatalf("go env GOMODCACHE: %v", err)
	}
	callerModCache := strings.TrimSpace(string(modOut))

	cmd := exec.Command("sh", "hermetic.sh", "--", "env")
	cmd.Env = callerEnv
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hermetic.sh: %v", err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}

	if got["HOME"] == "" || got["HOME"] == callerHome {
		t.Errorf("HOME = %q, want a fresh dir different from the caller's %q", got["HOME"], callerHome)
	}
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		if got[k] == "" || strings.HasPrefix(got[k], callerHome) {
			t.Errorf("%s = %q, want a fresh dir outside the caller's HOME", k, got[k])
		}
	}
	for _, p := range filepath.SplitList(got["PATH"]) {
		if strings.Contains(p, "shims") || strings.HasPrefix(p, callerHome) {
			t.Errorf("PATH entry %q is a shim dir or under the caller's HOME", p)
		}
	}
	if link, _ := os.Readlink(got["GOMODCACHE"]); got["GOMODCACHE"] != callerModCache && link != callerModCache {
		t.Errorf("GOMODCACHE = %q, want the caller's %q or a link to it", got["GOMODCACHE"], callerModCache)
	}
	if got["GOPROXY"] != "off" {
		t.Errorf("GOPROXY = %q, want the forwarded off", got["GOPROXY"])
	}
	if v, ok := got["SATELLE_HOME"]; ok {
		t.Errorf("SATELLE_HOME = %q leaked into the wrapped command", v)
	}
	callerPaths := []string{callerHome, filepath.Join(callerHome, ".satelle"), filepath.Join(callerHome, ".config"), callerSatelleHome}
	for k, v := range got {
		if strings.HasPrefix(k, "SATELLE_TEST_HOST_") {
			t.Errorf("%s = %q passed to the wrapped command", k, v)
		}
		if slices.Contains(callerPaths, v) {
			t.Errorf("%s = %q is a path of the caller's home", k, v)
		}
	}
	if got["SATELLE_TEST_PROBE_X"] != "1" {
		t.Errorf("SATELLE_TEST_PROBE_X = %q, want it forwarded", got["SATELLE_TEST_PROBE_X"])
	}
	if v, ok := got["UNLISTED"]; ok {
		t.Errorf("UNLISTED = %q, want it dropped", v)
	}
}

func TestHermeticWrapperPinsReleaseEndpoints(t *testing.T) {
	names := []string{"SATELLE_RELEASE_API", "SATELLE_RELEASE_LIST_API", "SATELLE_RELEASE_BASE"}
	cmd := exec.Command("sh", "hermetic.sh", "--", "env")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"SATELLE_RELEASE_API=https://api.github.com/x",
		"SATELLE_RELEASE_LIST_API=https://api.github.com/y",
		"SATELLE_RELEASE_BASE=https://github.com/z",
	}
	cmd.Env = append(cmd.Env, optIn()...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hermetic.sh: %v", err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	for _, name := range names {
		if !strings.HasPrefix(got[name], "http://127.0.0.1:") {
			t.Errorf("%s = %q, want a loopback URL whatever the caller exports", name, got[name])
		}
	}
	if strings.Contains(string(out), "github.com") {
		t.Errorf("the wrapped environment still names github.com:\n%s", out)
	}
}

func TestHermeticWrapperCleansUpAndKeepsStatus(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "home")
	cmd := exec.Command("sh", "hermetic.sh", "--", "sh", "-c", `printf %s "$HOME" > "$1"; exit 7`, "sh", marker)
	cmd.Env = append(os.Environ(), optIn()...)
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 7 {
		t.Fatalf("exit = %v, want status 7 kept", err)
	}
	home, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if _, serr := os.Stat(string(home)); !os.IsNotExist(serr) {
		t.Errorf("sandbox HOME %s still exists after exit (stat err %v)", home, serr)
	}
}

func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			tree[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(p)
		tree[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestHermeticFailsRunThatWritesSatelleHome(t *testing.T) {
	home, env := fakeHost(t)
	env = append(env, "PATH="+os.Getenv("PATH"))
	env = append(env, optIn()...)
	caller := filepath.Join(home, ".satelle")
	bin := filepath.Join(t.TempDir(), "satelle")
	build := exec.Command("go", "build", "-o", bin, "../cmd/satelle")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build satelle: %v\n%s", err, out)
	}
	const wrote = "wrote to ~/.satelle"
	run := func(t *testing.T, script string, args ...string) (int, string) {
		t.Helper()
		before := readTree(t, caller)
		cmd := exec.Command("sh", append([]string{"hermetic.sh", "--", "sh", "-c", script, "sh"}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("hermetic.sh: %v", err)
		}
		if after := readTree(t, caller); !maps.Equal(before, after) {
			t.Errorf("the run changed the caller's .satelle:\n%v\n->\n%v", before, after)
		}
		return code, string(out)
	}
	caught := func(t *testing.T, code, want int, out string, files ...string) {
		t.Helper()
		if code != want || !strings.Contains(out, wrote) {
			t.Fatalf("exit %d, want %d with %q; output:\n%s", code, want, wrote, out)
		}
		for _, f := range files {
			if !strings.Contains(out, f) {
				t.Errorf("output does not name %s:\n%s", f, out)
			}
		}
	}

	refused := func(t *testing.T, code int, out string) {
		t.Helper()
		if os.Geteuid() == 0 {
			if code == 0 || !strings.Contains(out, wrote) {
				t.Fatalf("as root: exit %d, want non-zero with %q; output:\n%s", code, wrote, out)
			}
			return
		}
		if code == 0 || !strings.Contains(strings.ToLower(out), "permission denied") {
			t.Fatalf("exit %d, want non-zero with a permission-denied write; output:\n%s", code, out)
		}
	}
	t.Run("shell write is refused at the write", func(t *testing.T) {
		code, out := run(t, `echo a > "$HOME/.satelle/config.toml"`)
		refused(t, code, out)
	})
	t.Run("satelle binary with no home cannot register a project", func(t *testing.T) {
		project := filepath.Join(t.TempDir(), "003")
		if err := os.Mkdir(project, 0o755); err != nil {
			t.Fatal(err)
		}
		code, out := run(t, `SATELLE_SERVER_ENDPOINT=none "$1" project add "$2"`, bin, project)
		refused(t, code, out)
		if b, err := os.ReadFile(filepath.Join(caller, "config.toml")); err != nil || string(b) != "a = 1\n" {
			t.Errorf("caller's config.toml = %q, %v; want it untouched", b, err)
		}
	})
	t.Run("a write that unlocks the guard is caught", func(t *testing.T) {
		code, out := run(t, `chmod u+w "$HOME/.satelle" && mkdir -p "$HOME/.satelle/serve" && echo w > "$HOME/.satelle/serve/mirror.db"`)
		caught(t, code, 1, out, "~/.satelle/serve/mirror.db")
	})
	t.Run("a write removed before exit is caught", func(t *testing.T) {
		code, out := run(t, `chmod u+w "$HOME/.satelle" && echo a > "$HOME/.satelle/config.toml" && rm -rf "$HOME/.satelle"`)
		caught(t, code, 1, out, "guard directory was removed")
	})
	t.Run("a write cleaned out of the guard before exit is caught", func(t *testing.T) {
		code, out := run(t, `chmod u+w "$HOME/.satelle" && echo a > "$HOME/.satelle/config.toml" && rm -f "$HOME/.satelle/config.toml"`)
		caught(t, code, 1, out, "made writable")
	})
	t.Run("a write cleaned up with the guard's mode restored is caught", func(t *testing.T) {
		code, out := run(t, `chmod u+w "$HOME/.satelle" && echo a > "$HOME/.satelle/config.toml" && rm -f "$HOME/.satelle/config.toml" && chmod 0555 "$HOME/.satelle"`)
		caught(t, code, 1, out, "written into")
	})
	t.Run("the command's failure status is kept", func(t *testing.T) {
		code, out := run(t, `chmod u+w "$HOME/.satelle" && echo a > "$HOME/.satelle/config.toml"; exit 3`)
		caught(t, code, 3, out, "~/.satelle/config.toml")
	})
	t.Run("a run that writes no satelle home passes", func(t *testing.T) {
		code, out := run(t, "true")
		if code != 0 || guardLine(out) {
			t.Fatalf("exit %d, output %q; want 0 and no hermetic line", code, out)
		}
	})
	t.Run("a test that sets its own home passes", func(t *testing.T) {
		own, project := t.TempDir(), t.TempDir()
		code, out := run(t, `SATELLE_HOME="$2" SATELLE_SERVER_ENDPOINT=none "$1" project add "$3"`, bin, own, project)
		if code != 0 || guardLine(out) {
			t.Fatalf("exit %d, output %q; want 0 and no hermetic line", code, out)
		}
		if _, err := os.Stat(filepath.Join(own, "config.toml")); err != nil {
			t.Errorf("the satelle with its own home did not write %s: %v", filepath.Join(own, "config.toml"), err)
		}
	})
}

func callerWithGoUnderHome(t *testing.T) (home string, env []string, dir string) {
	t.Helper()
	home, env = fakeHost(t)
	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatalf("go env GOROOT: %v", err)
	}
	if err := os.Symlink(strings.TrimSpace(string(goroot)), filepath.Join(home, "goroot")); err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(home, "project")
	for _, d := range []string{"go/pkg/mod", "go-build", "tmp", "bin", "project"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env = append(env,
		"PATH="+filepath.Join(home, "goroot", "bin")+":/usr/local/bin:/usr/bin:/bin",
		"GOROOT="+filepath.Join(home, "goroot"),
		"GOPATH="+filepath.Join(home, "go"),
		"GOMODCACHE="+filepath.Join(home, "go", "pkg", "mod"),
		"GOCACHE="+filepath.Join(home, "go-build"),
		"TMPDIR="+filepath.Join(home, "tmp"),
		"SATELLE_BIN="+filepath.Join(home, "bin", "satelle"),
	)
	env = append(env, optIn()...)
	return home, env, dir
}

func TestHermeticGivesNoPathOfCallerHome(t *testing.T) {
	home, env, dir := callerWithGoUnderHome(t)
	env = append(env, "GOFLAGS=-modfile="+filepath.Join(home, "x.mod")+" -mod=mod")
	script, err := filepath.Abs("hermetic.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script, "--", "sh", "-c", `env; echo "pwd=$(pwd)"; for v in GOROOT GOMODCACHE GOCACHE GOPATH; do echo "goenv $v=$(go env $v)"; done`)
	cmd.Env = env
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("hermetic.sh: %v\n%s", err, b)
	}
	out := string(b)
	phys, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, home) || strings.Contains(line, phys) {
			t.Errorf("the wrapped command was given a path of the caller's HOME: %s", line)
		}
	}
	if !strings.Contains(out, "goenv GOROOT=") || strings.Contains(out, "GOFLAGS=") {
		t.Errorf("want go env answered and GOFLAGS dropped; output:\n%s", out)
	}
	want := map[string]string{
		"GOROOT":     filepath.Join(home, "goroot"),
		"GOMODCACHE": filepath.Join(home, "go", "pkg", "mod"),
		"GOCACHE":    filepath.Join(home, "go-build"),
		"GOPATH":     filepath.Join(home, "go"),
		"pwd":        dir,
	}
	got := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "goenv "), "=")
		if _, wanted := want[k]; (ok && wanted) || k == "TMPDIR" {
			got[k] = v
		}
	}
	for k, target := range want {
		link, err := os.Readlink(got[k])
		same := err == nil && link == target
		if !same && err == nil {
			a, aerr := filepath.EvalSymlinks(link)
			b, berr := filepath.EvalSymlinks(target)
			same = aerr == nil && berr == nil && a == b
		}
		if !same {
			t.Errorf("%s = %q links to %q (%v), want a link to the caller's %s", k, got[k], link, err, target)
		}
	}
	if !strings.HasPrefix(got["TMPDIR"], "/tmp/hermetic.") {
		t.Errorf("TMPDIR = %q, want a fresh dir under /tmp", got["TMPDIR"])
	}
}

func guardLine(out string) bool {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "hermetic:") && !strings.HasPrefix(l, "hermetic: mode ") {
			return true
		}
	}
	return false
}

func asCaller(t *testing.T, extraEnv []string, prep func(home string), script string, args ...string) (home string, code int, out string) {
	t.Helper()
	home, env, dir := callerWithGoUnderHome(t)
	env = append(env, extraEnv...)
	if prep != nil {
		prep(home)
	}
	path, err := filepath.Abs("hermetic.sh")
	if err != nil {
		t.Fatal(err)
	}
	args = append([]string{filepath.Join(home, ".satelle"), home}, args...)
	cmd := exec.Command("sh", append([]string{path, "--", "sh", "-c", script, "sh"}, args...)...)
	cmd.Env = env
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("hermetic.sh: %v", err)
	}
	return home, code, string(b)
}

func statWholeSeconds(t *testing.T) bool {
	t.Helper()
	out, err := exec.Command("stat", "-c", "%y", "/").Output()
	if err != nil {
		out, err = exec.Command("stat", "-f", "%Fm", "/").Output()
	}
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return !strings.Contains(string(out), ".")
}

func optIn() []string {
	var env []string
	for _, k := range []string{"SATELLE_HERMETIC_ALLOW_SNAPSHOT", "SATELLE_HERMETIC_FORCE_NO_BWRAP"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	if ok, _ := bwrapWorks(); !ok && os.Getenv("SATELLE_HERMETIC_ALLOW_SNAPSHOT") == "" {
		env = append(env, "SATELLE_HERMETIC_ALLOW_SNAPSHOT=1")
	}
	return env
}

func bwrapWorks() (bool, string) {
	if os.Getenv("SATELLE_HERMETIC_FORCE_NO_BWRAP") != "" {
		return false, "SATELLE_HERMETIC_FORCE_NO_BWRAP is set"
	}
	bw, err := exec.LookPath("bwrap")
	if err != nil {
		return false, "bwrap is not installed"
	}
	if out, err := exec.Command(bw, "--dev-bind", "/", "/", "true").CombinedOutput(); err != nil {
		return false, fmt.Sprintf("bwrap cannot run here: %v %s", err, out)
	}
	return true, ""
}

func TestHermeticBwrapMakesCallerSatelleHomeReadOnly(t *testing.T) {
	if ok, why := bwrapWorks(); !ok {
		t.Skip(why)
	}
	const erofs = "Read-only file system"
	const mode = "hermetic: mode bwrap:"
	refused := func(t *testing.T, code int, out string) {
		t.Helper()
		if code != 0 || !strings.Contains(out, mode) || !strings.Contains(out, erofs) || !strings.Contains(out, "write-refused") || guardLine(out) {
			t.Fatalf("exit %d; want 0 under bwrap with %q and write-refused; output:\n%s", code, erofs, out)
		}
	}
	unchanged := func(t *testing.T, home string) {
		t.Helper()
		if b, err := os.ReadFile(filepath.Join(home, ".satelle", "config.toml")); err != nil || string(b) != "a = 1\n" {
			t.Errorf("caller's config.toml = %q, %v; want it untouched", b, err)
		}
	}

	t.Run("a direct write fails", func(t *testing.T) {
		home, code, out := asCaller(t, nil, nil, `echo leak >> "$1/config.toml" || echo write-refused`)
		refused(t, code, out)
		unchanged(t, home)
	})
	t.Run("a write through readlink of the GOMODCACHE link fails", func(t *testing.T) {
		home, code, out := asCaller(t, nil, nil, `h=$(readlink "$GOMODCACHE"); h=${h%/go/pkg/mod}; `+
			`[ "$h" = "$2" ] || { echo "readlink gave $h, want the caller's home" >&2; exit 9; }; `+
			`echo leak >> "$h/.satelle/config.toml" || echo write-refused`)
		refused(t, code, out)
		unchanged(t, home)
	})
	t.Run("a new file and a removal fail", func(t *testing.T) {
		home, code, out := asCaller(t, nil, nil, `mkdir -p "$1/projects/003" && exit 1; rm "$1/config.toml" && exit 1; echo write-refused`)
		refused(t, code, out)
		unchanged(t, home)
		if _, err := os.Stat(filepath.Join(home, ".satelle", "projects")); !os.IsNotExist(err) {
			t.Errorf("projects/ was created in the caller's home (stat err %v)", err)
		}
	})
	t.Run("the caller's SATELLE_HOME is read-only too", func(t *testing.T) {
		own := t.TempDir()
		if err := os.WriteFile(filepath.Join(own, "config.toml"), []byte("b = 2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, code, out := asCaller(t, []string{"SATELLE_HOME=" + own}, nil, `echo leak >> "$3/config.toml" || echo write-refused`, own)
		refused(t, code, out)
		if b, _ := os.ReadFile(filepath.Join(own, "config.toml")); string(b) != "b = 2\n" {
			t.Errorf("caller's SATELLE_HOME config.toml = %q, want it untouched", b)
		}
	})
	t.Run("an absent home cannot be created and the rest of the home stays writable", func(t *testing.T) {
		gone := func(home string) {
			if err := os.RemoveAll(filepath.Join(home, ".satelle")); err != nil {
				t.Fatal(err)
			}
		}
		home, code, out := asCaller(t, nil, gone, `mkdir "$1" || echo write-refused; `+
			`echo ok > "$2/project/written" && echo ok > "$GOCACHE/written" && echo rest-writable`)
		refused(t, code, out)
		if !strings.Contains(out, "rest-writable") {
			t.Errorf("the rest of the caller's home was not writable:\n%s", out)
		}
		if _, err := os.Lstat(filepath.Join(home, ".satelle")); !os.IsNotExist(err) {
			t.Errorf("the run created the caller's absent .satelle (lstat err %v)", err)
		}
		for _, f := range []string{filepath.Join(home, "project", "written"), filepath.Join(home, "go-build", "written")} {
			if _, err := os.Stat(f); err != nil {
				t.Errorf("%s was not written: %v", f, err)
			}
		}
	})
}

func TestHermeticRefusesWithoutBwrap(t *testing.T) {
	const script = `touch "$3"`
	t.Run("no opt-in: refused, the command never runs", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "ran")
		_, code, out := asCaller(t, []string{"SATELLE_HERMETIC_FORCE_NO_BWRAP=1", "SATELLE_HERMETIC_ALLOW_SNAPSHOT="}, nil, script, marker)
		if code != 2 {
			t.Errorf("exit %d, want 2; output:\n%s", code, out)
		}
		for _, want := range []string{"hermetic: refusing to run: bwrap is missing", "install bubblewrap", "SATELLE_HERMETIC_ALLOW_SNAPSHOT=1", "detection-only snapshot mode"} {
			if !strings.Contains(out, want) {
				t.Errorf("output does not say %q:\n%s", want, out)
			}
		}
		if n := strings.Count(strings.TrimSpace(out), "\n"); n != 0 {
			t.Errorf("want one stderr line, got %d:\n%s", n+1, out)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("the command ran: %s exists (stat err %v)", marker, err)
		}
	})
	t.Run("opt-in: snapshot mode runs the command and says it only detects", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "ran")
		_, code, out := asCaller(t, []string{"SATELLE_HERMETIC_FORCE_NO_BWRAP=1", "SATELLE_HERMETIC_ALLOW_SNAPSHOT=1"}, nil, script, marker)
		if code != 0 || guardLine(out) {
			t.Fatalf("exit %d; want 0 and no guard line; output:\n%s", code, out)
		}
		for _, want := range []string{"hermetic: mode snapshot", "detection of any change", "no path exempt"} {
			if !strings.Contains(out, want) {
				t.Errorf("mode line does not say %q:\n%s", want, out)
			}
		}
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("the command did not run: %v", err)
		}
	})
}

func TestHermeticCatchesWriteToCallerRealSatelleHome(t *testing.T) {
	const changed = "changed the caller's real satelle home"
	run := func(t *testing.T, extraEnv []string, script string, args ...string) (home string, code int, out string) {
		t.Helper()
		home, code, out = asCaller(t, append([]string{"SATELLE_HERMETIC_FORCE_NO_BWRAP=1", "SATELLE_HERMETIC_ALLOW_SNAPSHOT=1"}, extraEnv...), nil, script, args...)
		if !strings.Contains(out, "hermetic: mode snapshot (bwrap is missing (SATELLE_HERMETIC_FORCE_NO_BWRAP is set); SATELLE_HERMETIC_ALLOW_SNAPSHOT is set): detection of any change") {
			t.Fatalf("the run did not say it ran in snapshot mode:\n%s", out)
		}
		return home, code, out
	}
	caught := func(t *testing.T, code, want int, out string, paths ...string) {
		t.Helper()
		if code != want || !strings.Contains(out, changed) {
			t.Fatalf("exit %d, want %d with %q; output:\n%s", code, want, changed, out)
		}
		for _, p := range paths {
			if !strings.Contains(out, "  "+p+"\n") {
				t.Errorf("output does not name %s:\n%s", p, out)
			}
		}
	}

	t.Run("a path derived from GOMODCACHE no longer reaches the caller's home", func(t *testing.T) {
		home, code, out := run(t, nil, `h=${GOMODCACHE%/go/pkg/mod}; echo "derived=$h/.satelle"; `+
			`case "$h" in "$2"*) echo "derived path is in the caller's home" >&2; exit 9;; esac; `+
			`echo leak >> "$h/.satelle/config.toml" 2>/dev/null; exit 0`)
		if code != 0 || guardLine(out) {
			t.Fatalf("exit %d, want 0 and no guard line; output:\n%s", code, out)
		}
		if !strings.Contains(out, "derived=/") || strings.Contains(out, home) {
			t.Errorf("want a derived path outside the caller's home; output:\n%s", out)
		}
		if b, err := os.ReadFile(filepath.Join(home, ".satelle", "config.toml")); err != nil || string(b) != "a = 1\n" {
			t.Errorf("caller's config.toml = %q, %v; want it untouched", b, err)
		}
	})
	t.Run("a direct write to the caller's home is caught", func(t *testing.T) {
		home, code, out := run(t, nil, `echo leak >> "$1/config.toml"`)
		caller := filepath.Join(home, ".satelle")
		caught(t, code, 1, out, filepath.Join(caller, "config.toml"))
		if b, err := os.ReadFile(filepath.Join(caller, "config.toml")); err != nil || !strings.Contains(string(b), "leak") {
			t.Fatalf("caller's config.toml = %q, %v; the fixture's write did not land", b, err)
		}
	})
	t.Run("a new project registered at depth is caught", func(t *testing.T) {
		home, code, out := run(t, nil, `mkdir -p "$1/projects/003" && echo x > "$1/projects/003/repo.toml"`)
		caller := filepath.Join(home, ".satelle")
		caught(t, code, 1, out, filepath.Join(caller, "projects"), filepath.Join(caller, "projects", "003", "repo.toml"))
	})
	t.Run("a removed file is caught", func(t *testing.T) {
		home, code, out := run(t, nil, `rm "$1/document-sync-state.json"`)
		caught(t, code, 1, out, filepath.Join(home, ".satelle", "document-sync-state.json"))
	})
	t.Run("changed bytes of the same size are caught", func(t *testing.T) {
		home, code, out := run(t, nil, `printf 'a = 2\n' > "$1/config.toml"`)
		caught(t, code, 1, out, filepath.Join(home, ".satelle", "config.toml"))
	})
	t.Run("a rewrite with the same bytes is caught", func(t *testing.T) {
		wait := ""
		if statWholeSeconds(t) {
			wait = "sleep 1; "
		}
		home, code, out := run(t, nil, wait+`cat "$1/config.toml" > "$1/config.toml.new" && cat "$1/config.toml.new" > "$1/config.toml" && rm "$1/config.toml.new"`)
		caught(t, code, 1, out, filepath.Join(home, ".satelle", "config.toml"))
	})
	t.Run("the home removed is caught", func(t *testing.T) {
		home, code, out := run(t, nil, `rm -r "$1"`)
		caught(t, code, 1, out, filepath.Join(home, ".satelle"))
	})
	t.Run("the caller's SATELLE_HOME is watched too", func(t *testing.T) {
		own := t.TempDir()
		if err := os.WriteFile(filepath.Join(own, "config.toml"), []byte("b = 2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, code, out := run(t, []string{"SATELLE_HOME=" + own}, `echo leak >> "$3/config.toml"`, own)
		caught(t, code, 1, out, filepath.Join(own, "config.toml"))
	})
	t.Run("the command's failure status is kept", func(t *testing.T) {
		home, code, out := run(t, nil, `echo leak >> "$1/config.toml"; exit 3`)
		caught(t, code, 3, out, filepath.Join(home, ".satelle", "config.toml"))
	})
	for _, c := range []struct{ name, script, path string }{
		{"serve/", `echo x > "$1/serve/x"`, "serve/x"},
		{"sessions/", `mkdir -p "$1/sessions" && echo x > "$1/sessions/x"`, "sessions/x"},
		{"builds.json", `echo x > "$1/builds.json"`, "builds.json"},
		{"an existing runtime dir", `echo x > "$1/repo-0123abcd/x"`, "repo-0123abcd/x"},
		{"a new runtime dir", `mkdir "$1/other-89abcdef" && echo x > "$1/other-89abcdef/repo.path"`, "other-89abcdef/repo.path"},
	} {
		t.Run("a write to "+c.name+" is caught", func(t *testing.T) {
			home, code, out := run(t, nil, c.script)
			caught(t, code, 1, out, filepath.Join(home, ".satelle", c.path))
		})
	}
}

func TestHermeticNestedRunInheritsOptIn(t *testing.T) {
	script, err := filepath.Abs("hermetic.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script, "--", "sh", "-c",
		`env | grep '^SATELLE_HERMETIC_'; sh "$1" -- sh -c 'echo inner-ran'`, "sh", script)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"SATELLE_HERMETIC_FORCE_NO_BWRAP=1",
		"SATELLE_HERMETIC_ALLOW_SNAPSHOT=1",
	}
	b, err := cmd.CombinedOutput()
	out := string(b)
	if err != nil {
		t.Fatalf("hermetic.sh: %v\n%s", err, out)
	}
	for _, want := range []string{"SATELLE_HERMETIC_ALLOW_SNAPSHOT=1", "SATELLE_HERMETIC_FORCE_NO_BWRAP=1", "inner-ran"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "hermetic: mode snapshot"); n != 2 || strings.Contains(out, "refusing to run") {
		t.Errorf("want the outer and the nested run both in snapshot mode, got %d mode lines:\n%s", n, out)
	}
}
