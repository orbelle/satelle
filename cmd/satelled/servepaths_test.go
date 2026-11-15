package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

const gateScript = "scripts/check-serve-version.sh"

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			if _, err := os.ReadFile(filepath.Join(d, gateScript)); err != nil {
				t.Fatalf("cannot read %s: %v", gateScript, err)
			}
			return d
		}
		if filepath.Dir(d) == d {
			t.Fatal("no go.mod above this package")
		}
	}
}

func serveDeps(t *testing.T, root string) []string {
	t.Helper()
	mod := run(t, root, "go", "list", "-m")
	deps := run(t, root, "go", "list", "-deps", "./cmd/satelled")
	var out []string
	for _, ln := range strings.Split(deps, "\n") {
		ln = strings.TrimSpace(ln)
		if rest, ok := strings.CutPrefix(ln, mod+"/"); ok {
			out = append(out, rest)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no in-repo dependencies resolved for the serve binary (module %q)", mod)
	}
	return out
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func classifyCheckPath(code int, out string) ([]string, error) {
	switch code {
	case 0:
		return nil, nil
	case 1:
		var paths []string
		for _, ln := range strings.Split(out, "\n") {
			if ln = strings.TrimSpace(ln); ln != "" {
				paths = append(paths, ln)
			}
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("the gate exited 1 but named no unwatched path — it failed rather than answered")
		}
		return paths, nil
	default:
		return nil, fmt.Errorf("the gate could not answer (exit %d, want 0 watched / 1 unwatched): %s",
			code, strings.TrimSpace(out))
	}
}

func checkPaths(t *testing.T, root string, paths []string) []string {
	t.Helper()
	cmd := exec.Command("bash", append([]string{gateScript, "--check-path"}, paths...)...)
	cmd.Dir = root
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("--check-path: %v\n%s", err, stderr.String())
		}
		code = ee.ExitCode()
	}
	out := stdout.String()
	if code != 0 && code != 1 {
		out += stderr.String()
	}
	missed, err := classifyCheckPath(code, out)
	if err != nil {
		t.Fatalf("--check-path: %v\nstderr:\n%s", err, stderr.String())
	}
	return missed
}

func watches(t *testing.T, root, path string) bool {
	t.Helper()
	return len(checkPaths(t, root, []string{path})) == 0
}

func TestServeGateWatchesEveryServeDependency(t *testing.T) {
	root := repoRoot(t)
	deps := serveDeps(t, root)
	probes := make([]string, len(deps))
	for i, pkg := range deps {
		probes[i] = pkg + "/anyfile.go"
	}
	if missed := checkPaths(t, root, probes); len(missed) > 0 {
		t.Errorf("the serve binary links these packages but the version gate does not watch them:\n  %s\n"+
			"A change confined to one of them would release with no satelled.version bump, and the "+
			"running service would silently keep the old code.", strings.Join(missed, "\n  "))
	}
	if len(deps) < 5 {
		t.Errorf("only %d in-repo serve dependencies resolved — the derivation looks broken, "+
			"and a gate that watches almost nothing passes this test for the wrong reason", len(deps))
	}
}

func TestServeGateWatchesInternalServe(t *testing.T) {
	root := repoRoot(t)
	if !watches(t, root, "internal/serve/reconcile.go") {
		t.Error("internal/serve is unwatched — it runs ONLY inside satelled, " +
			"so a change there that ships without a serve bump never reaches a running service")
	}
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	gittest.Git(t, dir, "commit", "-a", "-m", msg)
}

func overlayGateScript(t *testing.T, root, dir string) {
	t.Helper()
	script, err := os.ReadFile(filepath.Join(root, gateScript))
	if err != nil {
		t.Fatalf("read %s: %v", gateScript, err)
	}
	dst := filepath.Join(dir, gateScript)
	writeFile(t, dst, string(script))
	if err := os.Chmod(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if gittest.Git(t, dir, "status", "--porcelain") != "" {
		commitAll(t, dir, "overlay the gate script")
	}
}

func privateClone(t *testing.T, src string) string {
	t.Helper()
	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	gittest.Git(t, parent, "clone", "--quiet", "--no-local", src, clone)
	overlayGateScript(t, src, clone)
	gittest.Git(t, clone, "tag", "serve-v999999.0.0")
	return clone
}

func probeServeGate(t *testing.T, src string, srcEnv []string) int {
	t.Helper()
	if gittest.Git(t, src, "tag", "-l", "serve-v*") == "" {
		t.Skip("no serve-v* tag in this clone — the gate has no baseline, so planting a file proves nothing")
	}
	if _, out := gateIn(t, src, srcEnv); strings.Contains(out, serveGateStaleBaseline) || strings.Contains(out, serveGateCannotVerify) {
		t.Skipf("serve-tag baseline not verified in this clone, so planting a file proves nothing:\n%s", out)
	}

	clone := privateClone(t, src)
	env := []string{"SERVE_TAG_REMOTE=" + clone}
	if code, out := gateIn(t, clone, env); code != 0 {
		t.Fatalf("the unplanted clone must pass the gate, exit %d:\n%s", code, out)
	}

	var asserted int
	for _, c := range []struct {
		rel      string
		pkg      string
		wantPass bool
		why      string
	}{
		{"internal/serve/zz_gate_probe.go", "serve", false,
			"a new source file in a serve package must demand a serve bump"},
		{"internal/serve/zz_gate_probe_test.go", "serve", true,
			"a test file is never compiled into the shipped binary"},
		{"internal/cli/zz_gate_probe.go", "cli", true,
			"a new file outside the serve build must not demand a serve release"},
	} {
		path := filepath.Join(clone, c.rel)
		writeFile(t, path, "package "+c.pkg+"\n")
		if planted, _ := filepath.Glob(filepath.Join(src, "internal", "*", "zz_gate_probe*")); len(planted) > 0 {
			t.Fatalf("probe files in the source tree %s: %v", src, planted)
		}
		code, out := gateIn(t, clone, env)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if ok := code == 0; ok != c.wantPass {
			t.Errorf("with %s present the gate %s — %s\n%s", c.rel,
				map[bool]string{true: "PASSED", false: "FAILED"}[ok], c.why, out)
		}
		asserted++
	}
	return asserted
}

func TestServeGateSeesNewFiles(t *testing.T) {
	requireGitAndGo(t)
	if n := probeServeGate(t, repoRoot(t), nil); n != 3 {
		t.Fatalf("the probe ran %d planted-file assertions, want 3", n)
	}
}

func TestServeGateProbeRunsWhenServeBumped(t *testing.T) {
	requireGitAndGo(t)
	root := repoRoot(t)
	parent := t.TempDir()
	src := filepath.Join(parent, "src")
	gittest.Git(t, parent, "clone", "--quiet", "--no-local", root, src)
	overlayGateScript(t, root, src)
	for _, tag := range strings.Fields(gittest.Git(t, src, "tag", "-l", "serve-v*")) {
		gittest.Git(t, src, "tag", "-d", tag)
	}
	gittest.Git(t, src, "tag", "serve-v0.0.1")

	versionPath := filepath.Join(src, ".version")
	body, err := os.ReadFile(versionPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	var bumped bool
	for i, ln := range lines {
		f := strings.Fields(ln)
		if len(f) != 2 || f[0] != "satelled.version:" {
			continue
		}
		parts := strings.Split(f[1], ".")
		patch, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil {
			t.Fatalf("satelled.version %q has no numeric patch: %v", f[1], err)
		}
		parts[len(parts)-1] = strconv.Itoa(patch + 1)
		lines[i] = f[0] + " " + strings.Join(parts, ".")
		bumped = true
	}
	if !bumped {
		t.Fatalf("no satelled.version line in %s", versionPath)
	}
	writeFile(t, versionPath, strings.Join(lines, "\n"))
	commitAll(t, src, "bump the serve version")

	env := []string{"SERVE_TAG_REMOTE=" + src}
	code, out := gateIn(t, src, env)
	if code != 0 || strings.Contains(out, serveGateStaleBaseline) || strings.Contains(out, serveGateCannotVerify) {
		t.Fatalf("the bumped source must have a verified baseline, exit %d:\n%s", code, out)
	}

	var sub *testing.T
	var n int
	t.Run("probe", func(st *testing.T) {
		sub = st
		n = probeServeGate(st, src, env)
	})
	if sub.Skipped() {
		t.Fatal("the probe skipped on a bumped, unreleased tree")
	}
	if n != 3 {
		t.Fatalf("the probe ran %d planted-file assertions on a bumped tree, want 3", n)
	}
}

func TestServeGateIgnoresCLIOnlyPaths(t *testing.T) {
	root := repoRoot(t)
	cliOnly := []string{
		"internal/cli/drift.go",
		"internal/verb/changelog.go",
		"internal/store/store.go",
		"internal/app/app.go",
		"internal/agentstep/engine.go",
	}
	missed := map[string]bool{}
	for _, p := range checkPaths(t, root, cliOnly) {
		missed[p] = true
	}
	for _, path := range cliOnly {
		if !missed[path] {
			t.Errorf("%s is not part of the serve build but the gate watches it — "+
				"a CLI-only release would be forced to cut a serve release", path)
		}
	}
}

func TestServeGateCannotAnswerIsNotUnwatched(t *testing.T) {
	requireGitAndGo(t)
	clone := privateClone(t, repoRoot(t))
	env := []string{"GOFLAGS=-mod=nonexistent", "SERVE_TAG_REMOTE=" + clone}
	for _, args := range [][]string{
		{"--check-path", "internal/serve/x.go"},
		nil,
	} {
		code, out := gateIn(t, clone, env, args...)
		if code != 3 {
			t.Errorf("%v: exit %d, want 3 (could not derive the watch set)\n%s", args, code, out)
		}
		if !strings.Contains(out, "go list") || !strings.Contains(out, "-mod") {
			t.Errorf("%v: output does not carry go's own reason\n%s", args, out)
		}
		if strings.Contains(out, "internal/serve/x.go") {
			t.Errorf("%v: a gate that could not answer echoed the path as unwatched\n%s", args, out)
		}
		if _, err := classifyCheckPath(code, out); err == nil {
			t.Errorf("%v: exit %d was classified as an answer\n%s", args, code, out)
		}
	}
}

func TestClassifyCheckPath(t *testing.T) {
	for _, c := range []struct {
		code    int
		out     string
		want    []string
		wantErr []string
	}{
		{code: 0, out: ""},
		{code: 1, out: "a/x.go\nb/y.go\n", want: []string{"a/x.go", "b/y.go"}},
		{code: 3, out: "reason text", wantErr: []string{"exit 3", "reason text"}},
		{code: 2, out: "usage", wantErr: []string{"exit 2"}},
		{code: 1, out: "\n", wantErr: []string{"named no unwatched path"}},
	} {
		got, err := classifyCheckPath(c.code, c.out)
		if c.wantErr != nil {
			if err == nil {
				t.Errorf("exit %d: no error, got paths %v", c.code, got)
				continue
			}
			for _, w := range c.wantErr {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("exit %d: error %q does not contain %q", c.code, err, w)
				}
			}
			if got != nil {
				t.Errorf("exit %d: paths %v alongside an error", c.code, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("exit %d: unexpected error %v", c.code, err)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("exit %d: paths %v, want %v", c.code, got, c.want)
		}
	}
}

func TestServeGateFilesListsEmbeddedFiles(t *testing.T) {
	requireGitAndGo(t)
	root := repoRoot(t)
	code, out := gateIn(t, root, nil, "--files", "./cmd/satelle", "./cmd/satelled")
	if code != 0 {
		t.Fatalf("--files: exit %d\n%s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	has := func(l string) bool {
		for _, x := range lines {
			if x == l {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"built\tinternal/help/topics/queues.md",
		"pattern\tinternal/help/topics",
		"pattern\tinternal/kernel/template",
	} {
		if !has(want) {
			t.Errorf("--files lacks %q", want)
		}
	}
	serve := false
	for _, l := range lines {
		kind, path, ok := strings.Cut(l, "\t")
		if !ok || (kind != "built" && kind != "excluded" && kind != "pattern") {
			t.Errorf("--files line %q is not <built|excluded|pattern><TAB><path>", l)
		}
		if strings.HasSuffix(path, "_test.go") && !strings.Contains(path, "/topics/") {
			t.Errorf("--files lists a test file: %q", l)
		}
		serve = serve || (kind == "built" && strings.HasPrefix(path, "internal/serve/"))
	}
	if !serve {
		t.Error("--files lists no internal/serve source")
	}
	if code, out := gateIn(t, root, nil, "--files", "./cmd/nosuchpkg"); code != 3 || !strings.Contains(out, "go list") {
		t.Errorf("--files on a missing package: exit %d, want 3 with go's reason\n%s", code, out)
	}
}
