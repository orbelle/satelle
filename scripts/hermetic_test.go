package scripts_test

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestHermeticWrapperEnvironment runs hermetic.sh over `env` with a caller
// environment carrying a real-looking HOME, GOPROXY=off, SATELLE_HOME=/x and an
// unlisted variable, and asserts what the wrapped command actually sees.
func TestHermeticWrapperEnvironment(t *testing.T) {
	callerHome := t.TempDir()
	callerEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + callerHome,
		"GOPROXY=off",
		"SATELLE_HOME=/x",
		"SATELLE_TEST_PROBE_X=1",
		"UNLISTED=1",
	}
	// The caller's module cache, as go resolves it in the caller's environment.
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
	if got["GOMODCACHE"] != callerModCache {
		t.Errorf("GOMODCACHE = %q, want the caller's %q", got["GOMODCACHE"], callerModCache)
	}
	if got["GOPROXY"] != "off" {
		t.Errorf("GOPROXY = %q, want the forwarded off", got["GOPROXY"])
	}
	if v, ok := got["SATELLE_HOME"]; ok {
		t.Errorf("SATELLE_HOME = %q leaked into the wrapped command", v)
	}
	// No path of the caller's home reaches the command (sto_e3c6bfb6): the
	// only ~/.satelle a test can reach by HOME is the wrapper's own.
	callerPaths := []string{callerHome, filepath.Join(callerHome, ".satelle"), filepath.Join(callerHome, ".config"), "/x"}
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

// TestHermeticWrapperPinsReleaseEndpoints proves the wrapped command sees the
// three release-API overrides as loopback URLs whatever the caller exports, so a
// release check a test starts can never reach GitHub (sty_02579374).
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

// TestHermeticWrapperCleansUpAndKeepsStatus proves the temp dir is removed and
// the wrapped command's exit status survives.
func TestHermeticWrapperCleansUpAndKeepsStatus(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "home")
	cmd := exec.Command("sh", "hermetic.sh", "--", "sh", "-c", `printf %s "$HOME" > "$1"; exit 7`, "sh", marker)
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

// readTree is every file under dir (relative path -> contents), with each
// directory recorded by its relative path and an empty body.
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

// TestHermeticFailsRunThatWritesSatelleHome proves the guard of make test and
// make integration (sto_e3c6bfb6): a run that writes ~/.satelle -- by shell, at
// any depth, or by a real satelle binary started with no home of its own, the
// route of the 2026-10 '003' leak -- fails, naming the files, while the
// caller's own .satelle (a fake host home standing in for the operator's) is
// left byte-identical. A run that writes no satelle home, or whose satelle has
// its own SATELLE_HOME, passes.
func TestHermeticFailsRunThatWritesSatelleHome(t *testing.T) {
	home, env := fakeHost(t)
	env = append(env, "PATH="+os.Getenv("PATH"))
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

	// refused: the write fails where it happens (the guard is read-only), so
	// the run fails and nothing lands under the wrapper's ~/.satelle.
	refused := func(t *testing.T, code int, out string) {
		t.Helper()
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
		if code != 0 || strings.Contains(out, "hermetic:") {
			t.Fatalf("exit %d, output %q; want 0 and no hermetic line", code, out)
		}
	})
	t.Run("a test that sets its own home passes", func(t *testing.T) {
		own, project := t.TempDir(), t.TempDir()
		code, out := run(t, `SATELLE_HOME="$2" SATELLE_SERVER_ENDPOINT=none "$1" project add "$3"`, bin, own, project)
		if code != 0 || strings.Contains(out, "hermetic:") {
			t.Fatalf("exit %d, output %q; want 0 and no hermetic line", code, out)
		}
		if _, err := os.Stat(filepath.Join(own, "config.toml")); err != nil {
			t.Errorf("the satelle with its own home did not write %s: %v", filepath.Join(own, "config.toml"), err)
		}
	})
}

// TestHermeticCatchesWriteToCallerRealSatelleHome proves the second half of
// the guard (sto_e3c6bfb6): a test that reaches the caller's REAL satelle home
// by a path of its own, not through HOME, fails the run, naming what changed.
// The route is the one the wrapper cannot close without moving the Go caches:
// GOMODCACHE stays the caller's (here $HOME/go/pkg/mod of a fake caller HOME),
// so a test can strip /go/pkg/mod and write <home>/.satelle. The fake caller
// stands in for the operator; the operator's home is never touched. The paths
// the host's own satelle rewrites while a suite runs (serve/, sessions/,
// builds.json, the per-repo runtime dirs) pass.
func TestHermeticCatchesWriteToCallerRealSatelleHome(t *testing.T) {
	// derive is the reviewer's route: the caller's home from GOMODCACHE.
	const derive = `h=${GOMODCACHE%/go/pkg/mod}; [ -d "$h/.satelle" ] || { echo "no caller home at $h" >&2; exit 9; }; `
	const changed = "changed the caller's real satelle home"
	run := func(t *testing.T, extraEnv []string, script string, args ...string) (home string, code int, out string) {
		t.Helper()
		home, env := fakeHost(t)
		env = append(env, "PATH="+os.Getenv("PATH"), "GOMODCACHE="+filepath.Join(home, "go", "pkg", "mod"))
		env = append(env, extraEnv...)
		cmd := exec.Command("sh", append([]string{"hermetic.sh", "--", "sh", "-c", derive + script, "sh"}, args...)...)
		cmd.Env = env
		b, err := cmd.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("hermetic.sh: %v", err)
		}
		return home, code, string(b)
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

	t.Run("a write by a path derived from GOMODCACHE is caught", func(t *testing.T) {
		home, code, out := run(t, nil, `echo leak >> "$h/.satelle/config.toml"`)
		caller := filepath.Join(home, ".satelle")
		caught(t, code, 1, out, filepath.Join(caller, "config.toml"))
		// The write really reached the caller's home: the route is live and
		// the guard, not the wrapper's HOME, is what caught it.
		if b, err := os.ReadFile(filepath.Join(caller, "config.toml")); err != nil || !strings.Contains(string(b), "leak") {
			t.Fatalf("caller's config.toml = %q, %v; the fixture's write did not land", b, err)
		}
	})
	t.Run("a new project registered at depth is caught", func(t *testing.T) {
		home, code, out := run(t, nil, `mkdir -p "$h/.satelle/projects/003" && echo x > "$h/.satelle/projects/003/repo.toml"`)
		caller := filepath.Join(home, ".satelle")
		caught(t, code, 1, out, filepath.Join(caller, "projects"), filepath.Join(caller, "projects", "003", "repo.toml"))
	})
	t.Run("a removed file is caught", func(t *testing.T) {
		home, code, out := run(t, nil, `rm "$h/.satelle/document-sync-state.json"`)
		caught(t, code, 1, out, filepath.Join(home, ".satelle", "document-sync-state.json"))
	})
	t.Run("a rewrite with the same bytes is caught", func(t *testing.T) {
		home, code, out := run(t, nil, `cat "$h/.satelle/config.toml" > "$h/.satelle/config.toml.new" && cat "$h/.satelle/config.toml.new" > "$h/.satelle/config.toml" && rm "$h/.satelle/config.toml.new"`)
		caught(t, code, 1, out, filepath.Join(home, ".satelle", "config.toml"))
	})
	t.Run("the home removed is caught", func(t *testing.T) {
		home, code, out := run(t, nil, `rm -r "$h/.satelle"`)
		caught(t, code, 1, out, filepath.Join(home, ".satelle"))
	})
	t.Run("the caller's SATELLE_HOME is watched too", func(t *testing.T) {
		own := t.TempDir()
		if err := os.WriteFile(filepath.Join(own, "config.toml"), []byte("b = 2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, code, out := run(t, []string{"SATELLE_HOME=" + own}, `echo leak >> "$1/config.toml"`, own)
		caught(t, code, 1, out, filepath.Join(own, "config.toml"))
	})
	t.Run("the command's failure status is kept", func(t *testing.T) {
		home, code, out := run(t, nil, `echo leak >> "$h/.satelle/config.toml"; exit 3`)
		caught(t, code, 3, out, filepath.Join(home, ".satelle", "config.toml"))
	})
	t.Run("the host's own satelle writes pass", func(t *testing.T) {
		_, code, out := run(t, nil, `echo w2 > "$h/.satelle/serve/mirror.db-wal" && echo l > "$h/.satelle/serve/server.log" && `+
			`echo w2 > "$h/.satelle/repo-0123abcd/db-wal" && mkdir -p "$h/.satelle/other-89abcdef" "$h/.satelle/sessions" && `+
			`echo p > "$h/.satelle/other-89abcdef/repo.path" && echo s > "$h/.satelle/sessions/s.json" && echo b > "$h/.satelle/builds.json"`)
		if code != 0 || strings.Contains(out, "hermetic:") {
			t.Fatalf("exit %d, output %q; want 0 and no hermetic line", code, out)
		}
	})
}
