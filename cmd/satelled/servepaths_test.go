package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

// The serve-version gate (scripts/check-serve-version.sh) demands a
// satelled.version bump when a source file the daemon binary is built from
// changes. Its watch set used to be a hand-kept four-entry array while the
// binary compiled in sixteen in-repo packages, so a change to
// `internal/serve` — which runs ONLY inside the service — released with no
// bump, `satelle update` reported "already up to date", and the running service
// silently kept the old code (sty_a8853e85).
//
// These tests live HERE, beside deps_test.go, and not under tests/: every file
// in tests/ carries `//go:build integration`, which GitHub CI deliberately does
// not run. A guarantee against a silent gate belongs where every push checks
// it. They are the mirror of TestServeBinaryImportIsolation — that one asserts
// what must NOT link, these assert that everything which DOES link is watched.

// gateScript is the checked script, module-relative.
const gateScript = "scripts/check-serve-version.sh"

// repoRoot walks up to the directory holding go.mod.
//
// It also READS the gate script, which looks pointless and is not: these tests
// exercise the script through a subprocess, and `go test` keys its cache on the
// files the TEST PROCESS opens. Without this read, editing the script leaves a
// stale pass in the cache and the tests answer a question about the previous
// version. Verified: reverting the derivation by hand produced a cached "ok".
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

// serveDeps returns the in-repo package directories the serve binary links,
// module-relative (e.g. "internal/serve").
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

// watches reports whether the gate considers path part of the serve build.
// Exit 0 means watched, exit 1 unwatched; anything else is the script failing
// rather than answering, and must not be read as "unwatched".
func watches(t *testing.T, root, path string) bool {
	t.Helper()
	cmd := exec.Command("bash", gateScript, "--check-path", path)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("--check-path %s: %v\n%s", path, err, out)
	}
	if code := ee.ExitCode(); code != 1 {
		t.Fatalf("--check-path %s: exit %d (want 0 watched / 1 unwatched)\n%s", path, code, out)
	}
	return false
}

// TestServeGateWatchesEveryServeDependency is the enumeration, as a test rather
// than as a snapshot: it re-derives the serve binary's dependency set on every
// run and requires the gate to watch all of it. A new package joining the serve
// build fails here on the commit that adds it, so the drift this story fixes
// cannot silently recur.
func TestServeGateWatchesEveryServeDependency(t *testing.T) {
	root := repoRoot(t)
	deps := serveDeps(t, root)
	var missed []string
	for _, pkg := range deps {
		if !watches(t, root, pkg+"/anyfile.go") {
			missed = append(missed, pkg)
		}
	}
	if len(missed) > 0 {
		t.Errorf("the serve binary links these packages but the version gate does not watch them:\n  %s\n"+
			"A change confined to one of them would release with no satelled.version bump, and the "+
			"running service would silently keep the old code.", strings.Join(missed, "\n  "))
	}
	// A watch set that collapsed to nothing would pass the loop above vacuously.
	if len(deps) < 5 {
		t.Errorf("only %d in-repo serve dependencies resolved — the derivation looks broken, "+
			"and a gate that watches almost nothing passes this test for the wrong reason", len(deps))
	}
}

// TestServeGateWatchesInternalServe names the originating case explicitly.
// TestServeGateWatchesEveryServeDependency covers it, but a regression is
// easier to read when the story's own defect has an assertion of its own.
func TestServeGateWatchesInternalServe(t *testing.T) {
	root := repoRoot(t)
	if !watches(t, root, "internal/serve/reconcile.go") {
		t.Error("internal/serve is unwatched — it runs ONLY inside satelled, " +
			"so a change there that ships without a serve bump never reaches a running service")
	}
}

// commitAll commits every change in dir under the test identity.
func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	gittest.Git(t, dir, "commit", "-a", "-m", msg)
}

// overlayGateScript writes root's working-tree gate script into dir and commits
// it there when that changes anything, so the gate's own diff never counts it.
// The tests judge the script under edit, not the last committed one (see
// repoRoot).
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

// privateClone returns a throwaway clone of src with its own objects and tags,
// carrying the working-tree gate script, and with HEAD tagged serve-v999999.0.0.
// That tag sorts above every real one, so it is the gate's baseline and the
// clone starts with no serve-path change whatever src's tag or bump state is.
// Probe files are planted here and never in src: tests that walk the real source
// tree run in parallel with this package (sty_6b98ba1f).
func privateClone(t *testing.T, src string) string {
	t.Helper()
	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	gittest.Git(t, parent, "clone", "--quiet", "--no-local", src, clone)
	overlayGateScript(t, src, clone)
	gittest.Git(t, clone, "tag", "serve-v999999.0.0")
	return clone
}

// probeServeGate plants the probe files in a private clone of src and returns
// how many planted-file assertions it ran. It skips only when src has no
// verified serve-tag baseline, the two states in which a gate verdict says
// nothing. How far src's serve version leads its newest tag does not matter,
// because the clone judges against its own HEAD. srcEnv is the environment of
// the gate run that checks src's baseline (nil on the real tree, which uses
// origin).
func probeServeGate(t *testing.T, src string, srcEnv []string) int {
	t.Helper()
	// The gate compares against the latest serve-v* tag. With no tag it exits
	// early and answers ok to everything, so a planted file would "pass" and this
	// test would report a hole that is not there — or worse, miss one that is.
	// That is not hypothetical: it is how this test first failed CI, on a default
	// shallow, tagless checkout. Refuse to conclude anything instead.
	if gittest.Git(t, src, "tag", "-l", "serve-v*") == "" {
		t.Skip("no serve-v* tag in this clone — the gate has no baseline, so planting a file proves nothing")
	}
	// sty_da6c3874: a stale or unverifiable remote baseline fails the gate before
	// any planted file can matter.
	if _, out := gateIn(t, src, srcEnv); strings.Contains(out, serveGateStaleBaseline) || strings.Contains(out, serveGateCannotVerify) {
		t.Skipf("serve-tag baseline not verified in this clone, so planting a file proves nothing:\n%s", out)
	}

	clone := privateClone(t, src)
	env := []string{"SERVE_TAG_REMOTE=" + clone}
	if code, out := gateIn(t, clone, env); code != 0 {
		t.Fatalf("the unplanted clone must pass the gate, exit %d:\n%s", code, out)
	}

	// The probe must carry the package clause its directory already uses. A
	// mismatched one breaks `go list -deps`, and the gate then fails closed for
	// that reason instead of the one under test — which is correct behaviour and
	// a useless test.
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
		// While a probe is planted, none may exist in src: a source-walking test
		// elsewhere in the module would see it vanish under it.
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

// TestServeGateSeesNewFiles closes the hole a path-prefix check hides: `git
// diff` cannot see an UNTRACKED file, and the release path runs this gate before
// staging. So adding a brand-new source file to a serve package — as real a
// change as editing one — would have passed green.
//
// The _test.go leg is the matching carve-out: a test file is compiled into
// `go test`, never into the shipped binary, so it cannot make a running service
// stale and must not demand a serve release.
func TestServeGateSeesNewFiles(t *testing.T) {
	requireGitAndGo(t)
	if n := probeServeGate(t, repoRoot(t), nil); n != 3 {
		t.Fatalf("the probe ran %d planted-file assertions, want 3", n)
	}
}

// TestServeGateProbeRunsWhenServeBumped proves the probe does not skip on a tree
// whose serve version is ahead of its newest serve-v* tag — a serve bump pushed
// and not yet released, the normal state between that push and its release. It
// builds exactly that source: a serve-v* tag on the pre-bump commit and a
// committed satelled.version patch bump on top.
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

	// The source is ahead of its newest tag and its baseline verifies, so the
	// probe has no reason to skip.
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

// TestServeGateIgnoresCLIOnlyPaths is the no-false-positive half: a slice that
// touches nothing the serve binary links must not demand a serve release. The
// paths chosen are exactly the ones TestServeBinaryImportIsolation proves are
// NOT linked, so this test stays honest only while that isolation holds.
func TestServeGateIgnoresCLIOnlyPaths(t *testing.T) {
	root := repoRoot(t)
	for _, path := range []string{
		"internal/cli/drift.go",
		"internal/verb/changelog.go",
		"internal/store/store.go",
		"internal/app/app.go",
		"internal/agentstep/engine.go",
	} {
		if watches(t, root, path) {
			t.Errorf("%s is not part of the serve build but the gate watches it — "+
				"a CLI-only release would be forced to cut a serve release", path)
		}
	}
}
