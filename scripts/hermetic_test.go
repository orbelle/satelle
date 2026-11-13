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

	t.Run("shell write", func(t *testing.T) {
		code, out := run(t, `mkdir -p "$HOME/.satelle" && echo a > "$HOME/.satelle/config.toml"`)
		caught(t, code, 1, out, "~/.satelle/config.toml")
	})
	t.Run("satelle binary with no home registers a project", func(t *testing.T) {
		project := filepath.Join(t.TempDir(), "003")
		if err := os.Mkdir(project, 0o755); err != nil {
			t.Fatal(err)
		}
		code, out := run(t, `SATELLE_SERVER_ENDPOINT=none "$1" project add "$2"`, bin, project)
		caught(t, code, 1, out, "~/.satelle/config.toml")
		if b, err := os.ReadFile(filepath.Join(caller, "config.toml")); err != nil || string(b) != "a = 1\n" {
			t.Errorf("caller's config.toml = %q, %v; want it untouched", b, err)
		}
	})
	t.Run("write at depth", func(t *testing.T) {
		code, out := run(t, `mkdir -p "$HOME/.satelle/serve" && echo w > "$HOME/.satelle/serve/mirror.db"`)
		caught(t, code, 1, out, "~/.satelle/serve/mirror.db")
	})
	t.Run("the command's failure status is kept", func(t *testing.T) {
		code, out := run(t, `mkdir -p "$HOME/.satelle" && echo a > "$HOME/.satelle/config.toml"; exit 3`)
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
