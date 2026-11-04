//go:build integration

// Package tests holds satelle's black-box integration tests: they build the
// real binary and drive it end-to-end through the dogfood flow (init → story →
// index → status → serve), asserting on actual process output. Gated behind the
// `integration` build tag so the default `go test ./...` stays fast and
// hermetic; run with:
//
//	go test -tags integration ./tests/...
package tests

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/hostguard"
)

// testBin is the satelle binary under test, resolved once by TestMain.
var testBin string

// testHomes maps t.Name() → isolated SATELLE_HOME for that test. Each test gets
// one empty global registry reused across its run() calls so multi-step flows
// (init then reindex, second init idempotency, etc.) stay coherent, while never
// touching the host ~/.satelle (sty_ee7f40c6).
var testHomes sync.Map

// TestMain resolves the binary the suite drives. If SATELLE_BIN points at an
// existing binary it is used as-is — so the suite can run against a prebuilt or
// installed binary without a rebuild:
//
//	cd tests && SATELLE_BIN=$(command -v satelle) go test -tags integration .
//
// Otherwise satelle is built once into a temp dir shared across all tests.
//
// Before any test runs, SATELLE_HOME is forced to an isolated temp dir so nothing
// in this package can resolve to the host ~/.satelle (sty_ee7f40c6). After the
// suite, the FULL host production surface is re-snapshotted and the process exits
// non-zero if anything changed (sty_6d824d6a): host SATELLE_HOME tree,
// ~/.config/satelle credentials, and ~/.local/bin/satelle. Production listen
// port 8787 is off-limits for test serves (see never-bind-8787 assertions).
//
// Host credentials are guarded by server identity, not bytes (sty_d9677380): the
// operator's own hosted session rotates tokens in credentials.toml
// (hosted.Client.persistRotated) at any moment, independent of the suite, so a
// byte/mtime hash false-fails a clean run. A suite that adds, removes or
// re-identifies a server, or creates, deletes or corrupts the file, still fails.
// XDG_CONFIG_HOME is sandboxed for every test process so the suite cannot reach
// the host file through the environment.
// scrubAmbientHarnessEnv unsets the markers that make this process look like it
// runs inside a harness session or a dispatched step, so children inherit a bare
// shell whatever launched the suite.
func scrubAmbientHarnessEnv() {
	markers := agentcli.SessionMarkerEnvNames()
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		match := k == config.ScratchEnv || k == config.SessionEnv ||
			k == config.DispatchAgentEnv || k == config.DispatchStepEnv ||
			k == config.DispatchItemEnv || k == config.SpawnEnv
		for _, m := range markers {
			if k == m || strings.HasPrefix(k, m) {
				match = true
				break
			}
		}
		if match {
			_ = os.Unsetenv(k)
		}
	}
}

func TestMain(m *testing.M) {
	// Resolve host roots BEFORE isolating SATELLE_HOME, then snapshot those fixed
	// paths before and after the suite. Re-resolving via getenv after Setenv would
	// measure the sandbox and false-trip (or hide real host pollution).
	hostRootsAtStart = newHostRoots()
	beforeSurface := captureHostSurfaceAt(hostRootsAtStart)

	backstop, err := os.MkdirTemp("", "satelle-itest-home-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdtemp home:", err)
		os.Exit(1)
	}
	if err := os.Setenv("SATELLE_HOME", backstop); err != nil {
		fmt.Fprintln(os.Stderr, "setenv SATELLE_HOME:", err)
		_ = os.RemoveAll(backstop)
		os.Exit(1)
	}
	// Suite-wide off-switch (sty_5aa08259 / sty_21a7d16d). Machine
	// ResolveEndpoint now defaults to http://127.0.0.1:8787, so without this
	// every CLI drain would POST to the operator's live satelled. Tests that
	// seed a test serve override SATELLE_SERVER_ENDPOINT to that URL.
	if err := os.Setenv("SATELLE_SERVER_ENDPOINT", "none"); err != nil {
		fmt.Fprintln(os.Stderr, "setenv SATELLE_SERVER_ENDPOINT:", err)
		_ = os.RemoveAll(backstop)
		os.Exit(1)
	}

	// The suite drives the binary as a bare shell. A run started from inside a
	// harness or a dispatched step (the build-unit-check gate runs the suite as a
	// child of an engaged `satelle story set`) would otherwise hand every child
	// process the driver's session and dispatch identity, and the hook paths under
	// test would judge themselves dispatched. Tests that need one set it themselves.
	scrubAmbientHarnessEnv()

	// os.Exit skips defers; clean up and enforce the host-surface guard explicitly.
	// Also fingerprint host mirror partition keys (sty_5aa08259 AC1): hermetic
	// runs must not seed the operator's push-fed mirror even when a live unit
	// answers on :8787. File-level hash of serve/ is skipped (WAL noise); keys only.
	//
	// Serve process inventory (sty_bf797fa9 AC1): snapshot live serve pids BEFORE
	// the suite so the operator's own serve on :8787 is excluded by identity;
	// after m.Run, any new suite-owned serve is a leak — kill it and fail the suite.
	beforeParts := captureMirrorPartitionKeys(hostRootsAtStart.SatelleHome)
	beforeServes := liveServePIDs()
	var xdgBackstop, cwdBackstop string
	exit := func(code int) {
		// Always print coverage so a green run still says full vs PARTIAL
		// (sty_948a2d42) — silence must not read as verified clean.
		leaks, msg := serveLeakReport(beforeServes)
		if msg != "" {
			fmt.Fprint(os.Stderr, msg)
		}
		if len(leaks) > 0 {
			killLeakedServes(leaks)
			code = 1
		}
		afterSurface := captureHostSurfaceAt(hostRootsAtStart)
		diffs := diffHostSurface(beforeSurface, afterSurface)
		// The host-mutation probe's own artefacts go only after the guard has
		// compared, so the probe fails the suite and leaves the host as it found it.
		for _, p := range probePaths {
			_ = os.RemoveAll(p)
		}
		if len(diffs) > 0 {
			fmt.Fprintf(os.Stderr, "FATAL: host production surface changed during the integration suite (isolation failed).\n")
			fmt.Fprintf(os.Stderr, "  Production port 8787 is off-limits; host ~/.satelle, ~/.config/satelle, and ~/.local/bin/{satelle,satelled} must be untouched.\n")
			for _, d := range diffs {
				fmt.Fprintf(os.Stderr, "  - %s\n", d)
			}
			code = 1
		}
		afterParts := captureMirrorPartitionKeys(hostRootsAtStart.SatelleHome)
		if diffs := diffMirrorPartitionKeys(beforeParts, afterParts); len(diffs) > 0 {
			fmt.Fprintf(os.Stderr, "FATAL: host push-fed mirror partitions changed during the integration suite (sty_5aa08259 — hermetic seed isolation failed).\n")
			fmt.Fprintf(os.Stderr, "  Live :8787 mirror must stay byte-identical in partition keys; tests must not auto-probe/seed it.\n")
			for _, d := range diffs {
				fmt.Fprintf(os.Stderr, "  - %s\n", d)
			}
			code = 1
		}
		_ = os.RemoveAll(backstop)
		if xdgBackstop != "" {
			_ = os.RemoveAll(xdgBackstop)
		}
		if cwdBackstop != "" {
			_ = os.RemoveAll(cwdBackstop)
		}
		os.Exit(code)
	}

	if env := os.Getenv("SATELLE_BIN"); env != "" {
		abs, err := filepath.Abs(env)
		if err != nil || !fileExists(abs) {
			fmt.Fprintf(os.Stderr, "SATELLE_BIN=%q not usable: %v\n", env, err)
			exit(1)
		}
		// SATELLE_BIN may or may not carry a release stamp. Tests that need
		// writeDeployedVersion / deployed.version (TestDeployedVersionStampAndBreakingDrift,
		// TestScaffoldDriftSurfacesAndHeal, and isReleaseTestBin branches) skip
		// loudly when the binary is a dev sentinel. Prefer a stamped binary
		// (e.g. make build && SATELLE_BIN=./satelle go test …).
		testBin = abs
		if xdgBackstop, err = isolateXDGConfig(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			exit(1)
		}
		if cwdBackstop, err = isolateCwd(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			exit(1)
		}
		exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "satelle-itest")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdtemp:", err)
		exit(1)
	}
	testBin = filepath.Join(dir, "satelle")
	// The test runs from tests/, so the module root is one level up.
	//
	// Version stamp (sty_4c986ed8): stamp the same ldflags `make build` uses so
	// writeDeployedVersion is live under test. A bare `go build` leaves
	// Version=="dev", and writeDeployedVersion correctly refuses to stamp a dev
	// sentinel — permanently red-ing TestDeployedVersionStampAndBreakingDrift and
	// TestScaffoldDriftSurfacesAndHeal. Removing these ldflags re-breaks those
	// paths. isReleaseTestBin remains the guard when SATELLE_BIN is a dev build.
	ldflags, lerr := releaseTestBinLdflags()
	if lerr != nil {
		fmt.Fprintf(os.Stderr, "resolve version ldflags for testBin: %v\n", lerr)
		_ = os.RemoveAll(dir)
		exit(1)
	}
	build := exec.Command("go", "build", "-ldflags", ldflags, "-o", testBin, "./cmd/satelle")
	build.Dir = ".."
	if out, berr := build.CombinedOutput(); berr != nil {
		fmt.Fprintf(os.Stderr, "build satelle: %v\n%s", berr, out)
		_ = os.RemoveAll(dir)
		exit(1)
	}
	// Sandbox XDG_CONFIG_HOME only now: the go build above (and any setup-time
	// git) legitimately reads the operator's ~/.config/go and ~/.config/git. The
	// host roots were resolved and snapshotted before this point.
	if xdgBackstop, err = isolateXDGConfig(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(dir)
		exit(1)
	}
	// The build above ran from the module root (build.Dir = ".."); only now does
	// the suite leave the checkout.
	if cwdBackstop, err = isolateCwd(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(dir)
		exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	exit(code)
}

// isolateXDGConfig points XDG_CONFIG_HOME at a fresh temp dir so every satelle
// process the suite starts resolves hosted.CredentialsPath inside the sandbox,
// never the operator's ~/.config/satelle (sty_d9677380). Tests that need a
// specific config home still append their own XDG_CONFIG_HOME; the later
// duplicate wins in exec. Returns the dir for cleanup.
func isolateXDGConfig() (string, error) {
	dir, err := os.MkdirTemp("", "satelle-itest-xdg-*")
	if err != nil {
		return "", fmt.Errorf("mkdtemp xdg: %w", err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("setenv XDG_CONFIG_HOME: %w", err)
	}
	return dir, nil
}

// isolateCwd moves the whole test process into a fresh temp dir that is outside
// any satelle repo, so a satelle child started without an explicit Dir inherits a
// non-repo working directory whichever checkout runs the suite (sty_5f3d3da6).
// Without it the child inherits tests/ inside the running checkout: a gate
// worktree or CI carries no .satelle (it is git-ignored) while the operator's
// checkout does, so a verb that behaves differently inside a repo passed the
// gates and failed at release. Tests that need a repo set cmd.Dir themselves.
// It fails closed when the dir is inside the checkout or config.FindDataDir —
// satelle's own repo discovery — finds a .satelle above it. Returns the dir for
// cleanup. Paths into the checkout are reached through moduleRoot(), never "..".
func isolateCwd() (string, error) {
	dir, err := os.MkdirTemp("", "satelle-itest-cwd-*")
	if err != nil {
		return "", fmt.Errorf("mkdtemp cwd: %w", err)
	}
	if reason := cwdInsideRepo(dir); reason != "" {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("working directory default is inside a satelle repo: %s", reason)
	}
	if err := os.Chdir(dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("chdir to %s: %w", dir, err)
	}
	return dir, nil
}

// cwdInsideRepo reports why dir counts as inside a satelle repo, or "" when it
// does not: it is the checkout or under it, or satelle's own discovery finds a
// .satelle directory at or above it.
func cwdInsideRepo(dir string) string {
	if root := moduleRoot(); dir == root || strings.HasPrefix(dir, root+string(filepath.Separator)) {
		return dir + " is inside the checkout " + root
	}
	if found, ok := config.FindDataDir(dir); ok {
		return dir + " has a satelle data dir at " + found
	}
	return ""
}

// hostSurface is a content-addressed snapshot of production host state the
// integration suite must not mutate (sty_6d824d6a). Missing roots yield empty
// maps / empty binary fingerprint — skip-safe on fresh CI with no install.
type hostSurface struct {
	// relpath → "dir" or "sha256:<hex>:<size>"
	satelleHome map[string]string
	xdgConfig   map[string]string // ~/.config/satelle, credentials.toml excluded
	// credentials is the identity fingerprint of ~/.config/satelle/credentials.toml
	// (sty_d9677380): token rotation is tolerated, server/identity changes are not.
	credentials hosted.Identity
	// bins is hostguard's fingerprint of the installed satelle and satelled
	// (sty_1b739a74); "" for a binary that is absent.
	bins map[string]string
	// resolved roots (for diagnostics only)
	satelleHomeRoot string
	xdgConfigRoot   string
	binDirRoot      string
}

// hostRoots are fixed host paths resolved once before SATELLE_HOME isolation.
// The ~/.satelle and ~/.local/bin roots come from hostguard.ResolveRoots; the
// config root stays here because only this suite's hashXDGConfig walks it.
type hostRoots struct {
	hostguard.Roots
	xdgConfig string
	// preExistingKeys: key-dir names already under SatelleHome before the suite
	// (sty_c36c211f). Live service may mutate those trees; NEW key dirs are
	// pollution and must fail the host-surface guard.
	preExistingKeys map[string]struct{}
}

// hostRootsAtStart is the operator's real host, resolved ONCE by TestMain before
// it sandboxes SATELLE_HOME / XDG_CONFIG_HOME (sty_ec30f859). Every check that
// must look at the real host (the host-surface and mirror-key guards, the
// isolation tests, the operator-config checks) reads this var; nothing
// re-resolves it after sandboxing begins, when the env describes the sandbox.
var hostRootsAtStart hostRoots

// probePaths are paths the host-mutation probe created under the real host; the
// TestMain exit hook removes them after the host-surface guard has compared.
var probePaths []string

// newHostRoots picks the real machine paths to guard; only TestMain calls it,
// before sandboxing. The ~/.satelle and bin roots come from
// hostguard.ResolveRoots, the single root-resolution rule (sty_1b739a74), which
// ignores SATELLE_HOME and honours SATELLE_TEST_HOST_HOME. The credentials root:
// under scripts/hermetic.sh (SATELLE_TEST_HOST_HOME set) the caller's real
// XDG_CONFIG_HOME arrives as SATELLE_TEST_HOST_XDG_CONFIG_HOME, because the
// wrapper's own XDG_CONFIG_HOME is a sandbox; otherwise a pre-existing
// XDG_CONFIG_HOME is honoured (caller sandboxed).
func newHostRoots() hostRoots {
	r := hostRoots{Roots: hostguard.ResolveRoots(), preExistingKeys: map[string]struct{}{}}
	xdgHome := os.Getenv("XDG_CONFIG_HOME")
	if os.Getenv("SATELLE_TEST_HOST_HOME") != "" {
		xdgHome = os.Getenv("SATELLE_TEST_HOST_XDG_CONFIG_HOME")
	}
	if v := strings.TrimSpace(xdgHome); v != "" {
		r.xdgConfig = filepath.Join(v, "satelle")
	} else if r.Home != "" {
		r.xdgConfig = filepath.Join(r.Home, ".config", "satelle")
	}
	snap, err := hostguard.Capture(r.Roots)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: cannot snapshot the host surface, so isolation cannot be verified: %v\n", err)
		os.Exit(1)
	}
	r.preExistingKeys = snap.KeyDirs()
	return r
}

// captureHostSurfaceAt hashes the given fixed host roots (not re-resolved from env).
func captureHostSurfaceAt(r hostRoots) hostSurface {
	snap, _ := hostguard.Capture(r.Roots)
	return hostSurface{
		satelleHome:     hashTree(r.SatelleHome, r.preExistingKeys),
		xdgConfig:       hashXDGConfig(r.xdgConfig),
		credentials:     hosted.CaptureIdentity(filepath.Join(r.xdgConfig, credentialsFileName)),
		bins:            snap.Bins,
		satelleHomeRoot: r.SatelleHome,
		xdgConfigRoot:   r.xdgConfig,
		binDirRoot:      r.BinDir,
	}
}

// diffHostSurface returns human-readable change lines; empty when identical.
func diffHostSurface(before, after hostSurface) []string {
	var diffs []string
	diffs = append(diffs, diffTreeMaps("host SATELLE_HOME ("+before.satelleHomeRoot+")", before.satelleHome, after.satelleHome)...)
	xdgLabel := "host ~/.config/satelle (" + before.xdgConfigRoot + ")"
	diffs = append(diffs, diffTreeMaps(xdgLabel, before.xdgConfig, after.xdgConfig)...)
	for _, reason := range hosted.DiffIdentity(before.credentials, after.credentials) {
		diffs = append(diffs, fmt.Sprintf("%s: changed %s (%s)", xdgLabel, credentialsFileName, reason))
	}
	binLabel := "installed binaries"
	if before.binDirRoot != "" {
		binLabel += " (" + before.binDirRoot + ")"
	}
	for _, d := range hostguard.DiffBins(before.bins, after.bins) {
		diffs = append(diffs, binLabel+": "+d)
	}
	return diffs
}

// credentialsFileName is the host credential store inside ~/.config/satelle.
const credentialsFileName = "credentials.toml"

// hashXDGConfig byte-guards every file under ~/.config/satelle except
// credentials.toml (and its atomic-write .tmp sibling), which
// hosted.CaptureIdentity guards by identity instead.
func hashXDGConfig(root string) map[string]string {
	m := hashTree(root, nil)
	delete(m, credentialsFileName)
	delete(m, credentialsFileName+".tmp")
	return m
}

// hashTree walks root and returns a map of relpath → fingerprint. Missing or
// unreadable roots return an empty map (skip-safe).
//
// Under ~/.satelle, project runtime dirs (<basename>-<8hex>/ from RepoKey) that
// were already present before the suite (preExistingKeys) are SKIPPED: a live
// satelle service legitimately mutates them. A NEW key dir is recorded as a
// leaf so diffHostSurface fails the suite (sty_c36c211f — pollution guard).
// The machine-wide push-fed plane (`serve/`) is also skipped when present —
// a live unit rewrites server.log and mirror.db WAL (sty_80233c10). Isolation
// still guards config.toml and other non-runtime host state.
func hashTree(root string, preExistingKeys map[string]struct{}) map[string]string {
	out := map[string]string{}
	if root == "" {
		return out
	}
	info, err := os.Stat(root)
	if err != nil {
		return out
	}
	if !info.IsDir() {
		out["."] = hostguard.FingerprintFile(root)
		return out
	}
	_ = filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil {
			return nil // skip unreadable leaves
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		if rel == "." {
			out["./"] = "dir"
			return nil
		}
		rel = filepath.ToSlash(rel)
		top := strings.SplitN(rel, "/", 2)[0]
		// Names a live satelle process rewrites on its own (the document-sync
		// cursor, the push-fed serve plane, the run/ healthz cache) —
		// hostguard.Ignored owns the list.
		if hostguard.Ignored(top) {
			if fi.IsDir() && rel == top {
				return filepath.SkipDir
			}
			return nil
		}
		// Runtime-key handling applies only to SATELLE_HOME (the caller passes a
		// non-nil key set) and only to key DIRS (sty_ec30f859). A key-shaped file,
		// or anything under another root such as ~/.config/satelle, is
		// fingerprinted like any other state.
		if preExistingKeys != nil && hostguard.IsRuntimeKey(top) && (fi.IsDir() || rel != top) {
			// Pre-existing: skip contents (live service may rewrite DB/logs).
			if _, ok := preExistingKeys[top]; ok {
				if fi.IsDir() && rel == top {
					return filepath.SkipDir
				}
				return nil
			}
			// New during suite: record the key dir itself as pollution evidence.
			if fi.IsDir() && rel == top {
				out[rel+"/"] = "dir"
				return filepath.SkipDir
			}
			return nil
		}
		if fi.IsDir() {
			out[rel+"/"] = "dir"
			return nil
		}
		out[rel] = hostguard.FingerprintFile(path)
		return nil
	})
	return out
}

// captureMirrorPartitionKeys returns the set of repo_key values in
// home/serve/mirror.db (operator push-fed plane). Missing DB → empty set.
// Used by TestMain to prove hermetic runs do not seed a live :8787 mirror
// (sty_5aa08259 AC1). Opens read-only; does not start serve.
func captureMirrorPartitionKeys(home string) map[string]struct{} {
	if home == "" {
		return map[string]struct{}{}
	}
	return queryMirrorPartitionKeys(filepath.Join(home, "serve", "mirror.db"))
}

// queryMirrorPartitionKeys is wired in mirror_guard.go (integration build).
var queryMirrorPartitionKeys = func(dbPath string) map[string]struct{} {
	return map[string]struct{}{}
}

// diffMirrorPartitionKeys reports keys added or removed on the host mirror.
func diffMirrorPartitionKeys(before, after map[string]struct{}) []string {
	var diffs []string
	for k := range after {
		if _, ok := before[k]; !ok {
			diffs = append(diffs, "added partition "+k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			diffs = append(diffs, "removed partition "+k)
		}
	}
	sort.Strings(diffs)
	return diffs
}

func diffTreeMaps(label string, before, after map[string]string) []string {
	var diffs []string
	keys := map[string]struct{}{}
	for k := range before {
		keys[k] = struct{}{}
	}
	for k := range after {
		keys[k] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		b, bok := before[k]
		a, aok := after[k]
		switch {
		case bok && !aok:
			diffs = append(diffs, fmt.Sprintf("%s: removed %s", label, k))
		case !bok && aok:
			diffs = append(diffs, fmt.Sprintf("%s: added %s", label, k))
		case b != a:
			diffs = append(diffs, fmt.Sprintf("%s: changed %s", label, k))
		}
	}
	return diffs
}

func readFileOptional(path string) []byte {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return b
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// stubReviewerAccept points the repo's reviewer binding at a deterministic accept
// script, so the now-active baseline gates (materialised by init, sty_5b8bd8b2) do
// not invoke a real agent CLI in hermetic tests. Call after init, before any status
// transition. Gate CONTENT is covered separately (create_review, baseline_skills).
func stubReviewerAccept(t *testing.T, repo string) {
	t.Helper()
	// Outside the repo: a worktree-resident stub is an untracked non-substrate
	// path and would fail satelle-substrate-only-check once that gate unions
	// the live worktree (sty_6469025e).
	verdict := filepath.Join(t.TempDir(), "claude-verdict-accept.sh")
	if err := os.WriteFile(verdict, []byte("#!/bin/sh\necho '{\"decision\":\"accept\",\"notes\":\"\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "workflows", "agents.toml"),
		[]byte(fmt.Sprintf("[reviewer]\ncommand = \"%s {system} {tools} {model}\"\nisolation = \"operator-attested\"\n", verdict)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// isolatedHome returns the per-test SATELLE_HOME (created once via t.TempDir).
// Shared across run() calls in the same test so multi-step flows keep one
// empty-at-start global registry; never the host ~/.satelle.
//
// Keyed by the TOP-LEVEL test name (strip t.Run subtest suffixes) so a parent
// that started serve and a subtest that runs mustRun share one home-keyed DB
// (sty_4660bbe1). Without that, subtests opened empty ledgers and stories
// vanished mid-browser flow.
func isolatedHome(t *testing.T) string {
	t.Helper()
	name := t.Name()
	if i := strings.Index(name, "/"); i >= 0 {
		name = name[:i]
	}
	if v, ok := testHomes.Load(name); ok {
		return v.(string)
	}
	home := t.TempDir()
	if actual, loaded := testHomes.LoadOrStore(name, home); loaded {
		return actual.(string)
	}
	t.Cleanup(func() { testHomes.Delete(name) })
	return home
}

// runtimeRoot returns the home-keyed runtime dir for repo under this test's
// isolated SATELLE_HOME (~/.satelle/<repo-key>/ — sty_4660bbe1). Holds
// satelle.db, logs/, backups/, stories/. Uses Config.ResolveRuntimeDir so a
// tree that gained .git after init still resolves the original path-key dir.
func runtimeRoot(t *testing.T, repo string) string {
	t.Helper()
	// Isolate GlobalDir for this process (ResolveRuntimeDir reads SATELLE_HOME).
	prev, had := os.LookupEnv("SATELLE_HOME")
	_ = os.Setenv("SATELLE_HOME", isolatedHome(t))
	defer func() {
		if had {
			_ = os.Setenv("SATELLE_HOME", prev)
		} else {
			_ = os.Unsetenv("SATELLE_HOME")
		}
	}()
	return config.Config{}.ResolveRuntimeDir(repo).Dir
}

// isolatedEnv returns os.Environ with SATELLE_HOME pinned to this test's
// isolated home (overrides TestMain's suite backstop; last-wins for a key).
// SATELLE_SERVER_ENDPOINT=none disables endpoint auto-discovery/push so
// hermetic runs cannot seed the operator's live :8787 serve
// (sty_5aa08259 / epic:mirror-hygiene). Tests that intentionally seed set
// SATELLE_SERVER_ENDPOINT to their serve URL (overrides none; last wins).
func isolatedEnv(t *testing.T) []string {
	t.Helper()
	return append(os.Environ(),
		"SATELLE_HOME="+isolatedHome(t),
		"SATELLE_SERVER_ENDPOINT=none",
		// The suite drives the CLI as a script with no terminal, which is an
		// agent-facing call to a gate-running verb: it would return a verdict
		// block or a handle, not the record these tests read (sty_c4b92c9e).
		// Scripts run the gate in the foreground; the hand-off tests opt back in
		// through the extra env runEnv appends after this.
		"SATELLE_GATE_MODE=interactive",
	)
}

// materializeDefault materializes one embedded default onto disk via
// `satelle substrate edit` for tests that need an on-disk file (sty_29e5a9a5).
func materializeDefault(t *testing.T, repo, kind, name string) {
	t.Helper()
	mustRun(t, testBin, repo, "substrate", "edit", kind, name)
}

// materializeDefaultSolution lands the baseline workflows + gate skills used by
// many integration tests that predate virtual sparse defaults.
func materializeDefaultSolution(t *testing.T, repo string) {
	t.Helper()
	for _, wf := range []string{"done", "step"} {
		materializeDefault(t, repo, "workflows", wf)
	}
	for _, sk := range []string{
		"satelle-estimate-actual-review", "satelle-step-summary",
		"satelle-story-amend-review", "satelle-story-blocked-review",
		"satelle-story-cancel-review",
		"satelle-story-create-review", "satelle-story-done-review",
		"satelle-story-intent-review", "satelle-task-validate-before-review",
		"satelle-task-validate-after-review", "satelle-workflow-advisor",
		"satelle-reviewer-objective-audit", "satelle-context-audit",
		"satelle-substrate-only-check",
	} {
		materializeDefault(t, repo, "skills", sk)
	}
	for _, pr := range []string{
		"satelle-agent-goals", "satelle-agent-model", "satelle-edits-require-a-story",
		"satelle-done-is-last", "satelle-story-classification",
	} {
		materializeDefault(t, repo, "principles", pr)
	}
}

// run executes the binary in dir with args and returns combined output.
// The subprocess gets the test's isolated SATELLE_HOME so it never reads or
// writes the host machine-wide workspace registry (sty_ee7f40c6). Runtime
// state (DB/logs) is home-keyed under that home (sty_4660bbe1).
func run(t *testing.T, bin, dir string, args ...string) (string, error) {
	t.Helper()
	return runEnv(t, bin, dir, nil, args...)
}

// runEnv is run with extra env vars appended after isolatedEnv (last-wins).
func runEnv(t *testing.T, bin, dir string, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(isolatedEnv(t), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// mustRun fails the test if the command errors.
// After a successful `init`, hermetic tests opt OUT of create-gating via
// satelle.local.toml (gate_create=false): product init seeds gate_create=true
// (sty_83782ffb), but most suite tests create incomplete drafts to exercise
// other seams. Tests that need the create gate ON write local.toml themselves
// (see create_review_test.go).
func mustRun(t *testing.T, bin, dir string, args ...string) string {
	t.Helper()
	return mustRunEnv(t, bin, dir, nil, args...)
}

// mustRunEnv is mustRun with extra env (appended after isolatedEnv).
func mustRunEnv(t *testing.T, bin, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	out, err := runEnv(t, bin, dir, extraEnv, args...)
	if err != nil {
		t.Fatalf("satelle %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	if len(args) > 0 && args[0] == "init" {
		hermeticCreateGateOff(t, dir)
	}
	return out
}

// seedWorkspaceAdd runs `workspace add` with SATELLE_SERVER_ENDPOINT=endpoint so
// the suite-wide none is overridden and the snapshot hits the test serve
// (sty_5aa08259). endpoint must be the test serve base URL (e.g. http://127.0.0.1:PORT).
func seedWorkspaceAdd(t *testing.T, bin, repo, endpoint string) string {
	t.Helper()
	return mustRunEnv(t, bin, repo, []string{"SATELLE_SERVER_ENDPOINT=" + endpoint}, "workspace", "add")
}

// hermeticCreateGateOff flips the init-seeded scaffold's gate_create to false
// in the committed satelle.toml so black-box tests are not forced through
// create-review unless they opt in via satelle.local.toml. Also sets
// [categories] enforce = "off" so inventing a workflow-specific category for a
// fixture does not emit the warn notice (which breaks combined-out JSON
// parsers). Edits the scaffold file (not the local overlay) so tests that
// assert "no local.toml" still pass and tests that rewrite local.toml for
// [vars] do not wipe the opt-out.
func hermeticCreateGateOff(t *testing.T, repo string) {
	t.Helper()
	p := filepath.Join(repo, ".satelle", "satelle.toml")
	b, err := os.ReadFile(p)
	if err != nil {
		return // init may not have written config in exotic cases
	}
	body := string(b)
	if strings.Contains(body, "gate_create = true") {
		body = strings.Replace(body, "gate_create = true", "gate_create = false", 1)
	}
	if !strings.Contains(body, "[categories]") {
		body += "\n[categories]\nenforce = \"off\"\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("hermetic create-gate off: %v", err)
	}
}

// TestSubprocessHomeIsolated proves init via run() never writes the host global
// config, and that an explicit sandbox SATELLE_HOME receives the registry write
// instead (sty_ee7f40c6). The host path is the real SATELLE_HOME resolved before
// TestMain's process-wide backstop (hostRootsAtStart).
func TestSubprocessHomeIsolated(t *testing.T) {
	if hostRootsAtStart.SatelleHome == "" {
		t.Skip("no host SATELLE_HOME resolved; nothing to compare")
	}
	hostCfg := filepath.Join(hostRootsAtStart.SatelleHome, "config.toml")
	before := readFileOptional(hostCfg)

	repo := t.TempDir()
	mustRun(t, testBin, repo, "init")

	after := readFileOptional(hostCfg)
	if !bytes.Equal(before, after) {
		t.Fatalf("host %s changed after run()-spawned init (isolation failed)\nbefore %d bytes, after %d bytes",
			hostCfg, len(before), len(after))
	}
	if bytes.Contains(after, []byte(repo)) {
		t.Fatalf("host config contains test repo path %q", repo)
	}

	// Positive side: init with an explicit sandbox home must write config.toml
	// there (workspace registration), so we know isolation is not "init wrote nowhere".
	sandbox := t.TempDir()
	sandboxRepo := t.TempDir()
	cmd := exec.Command(testBin, "init")
	cmd.Dir = sandboxRepo
	cmd.Env = append(os.Environ(), "SATELLE_HOME="+sandbox)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox init: %v\n%s", err, out)
	}
	if !fileExists(filepath.Join(sandbox, "config.toml")) {
		t.Fatalf("sandbox SATELLE_HOME has no config.toml after init; init did not register:\n%s", out)
	}
	// Host still untouched.
	after2 := readFileOptional(hostCfg)
	if !bytes.Equal(before, after2) {
		t.Fatalf("host %s changed after sandbox init", hostCfg)
	}
}

// TestHostSurfaceGuardTrips proves the sty_6d824d6a snapshot/diff helpers detect
// a deliberate write into a protected tree, and stay quiet on missing roots
// (skip-safe for fresh CI with no host install).
func TestHostSurfaceGuardTrips(t *testing.T) {
	root := t.TempDir()
	before := hashTree(root, nil)
	if diffs := diffTreeMaps("t", before, hashTree(root, nil)); len(diffs) != 0 {
		t.Fatalf("equal trees must not trip: %v", diffs)
	}
	// Missing roots → empty map, no panic.
	if len(hashTree(filepath.Join(root, "does-not-exist"), nil)) != 0 {
		t.Fatal("missing root must hash to empty map")
	}
	if hostguard.FingerprintFile(filepath.Join(root, "does-not-exist")) != "" {
		t.Fatal("missing file fingerprint must be empty")
	}
	if err := os.WriteFile(filepath.Join(root, "pollute"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	after := hashTree(root, nil)
	if diffs := diffTreeMaps("t", before, after); len(diffs) == 0 {
		t.Fatal("expected guard to trip after deliberate write into protected tree")
	}
	// Full surface diff names the change.
	beforeS := hostSurface{satelleHome: before, xdgConfig: map[string]string{}}
	afterS := hostSurface{satelleHome: after, xdgConfig: map[string]string{}}
	if diffs := diffHostSurface(beforeS, afterS); len(diffs) == 0 {
		t.Fatal("diffHostSurface should report the deliberate write")
	}
	// Installed-binary fingerprint changes on rewrite, for satelled as well as
	// satelle, and ignores a no-op touch (sty_1b739a74).
	binDir := t.TempDir()
	for _, name := range hostguard.BinNames {
		bin := filepath.Join(binDir, name)
		if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
			t.Fatal(err)
		}
		fp1 := hostguard.FingerprintFile(bin)
		future := time.Now().Add(time.Hour)
		if err := os.Chtimes(bin, future, future); err != nil {
			t.Fatal(err)
		}
		if hostguard.FingerprintFile(bin) != fp1 {
			t.Fatalf("%s: a touch must not change the binary fingerprint", name)
		}
		if err := os.WriteFile(bin, []byte("v2"), 0o755); err != nil {
			t.Fatal(err)
		}
		fp2 := hostguard.FingerprintFile(bin)
		if fp1 == "" || fp2 == "" || fp1 == fp2 {
			t.Fatalf("%s: binary fingerprint must change on rewrite: %q → %q", name, fp1, fp2)
		}
		diffs := diffHostSurface(
			hostSurface{bins: map[string]string{name: fp1}, binDirRoot: binDir},
			hostSurface{bins: map[string]string{name: fp2}, binDirRoot: binDir})
		if len(diffs) != 1 || !strings.Contains(diffs[0], name) {
			t.Fatalf("expected the %s change to trip the guard and name it: %v", name, diffs)
		}
	}
}

// testCredential is a login-shaped credential for the host-surface guard tests.
func testCredential(url string) hosted.Credential {
	return hosted.Credential{
		ServerURL:    url,
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		TokenType:    "Bearer",
		Scope:        "mcp",
		DisplayName:  "Ada",
		Email:        "ada@example.com",
		PrincipalID:  "usr_1",
		ExpiresAt:    "2026-10-06T01:00:00Z",
		CreatedAt:    "2026-10-06T00:00:00Z",
	}
}

// credSurface captures a hostSurface for a temp config root holding the store.
func credSurface(root string) hostSurface {
	return captureHostSurfaceAt(hostRoots{xdgConfig: root})
}

func mustSave(t *testing.T, s hosted.FileStore, c hosted.Credential) {
	t.Helper()
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
}

// TestHostSurfaceToleratesCredentialRefresh proves a token refresh by a satelle
// process outside the suite (sty_d9677380 AC1) does not trip the guard. The
// rotated record is built the way hosted.Client.persistRotated builds it: new
// tokens, expiry and created_at, identity fields carried over from the original.
func TestHostSurfaceToleratesCredentialRefresh(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, credentialsFileName)
	store := hosted.FileStore{Path: path}
	orig := testCredential("http://127.0.0.1:9999")
	mustSave(t, store, orig)
	other := testCredential("https://other.example.com")
	other.PrincipalID = "usr_2"
	mustSave(t, store, other)
	before := credSurface(root)
	beforeBytes := readFileOptional(path)
	time.Sleep(5 * time.Millisecond) // time-subject: let mtime move on coarse filesystems, so the rotation is observable

	rotated := testCredential(orig.ServerURL)
	rotated.AccessToken = "access-2"
	rotated.RefreshToken = "refresh-2"
	rotated.ExpiresAt = "2026-10-06T02:00:00Z"
	rotated.CreatedAt = "2026-10-06T01:00:00Z"
	rotated.DisplayName, rotated.Email, rotated.PrincipalID = orig.DisplayName, orig.Email, orig.PrincipalID
	mustSave(t, store, rotated)
	after := credSurface(root)

	if bytes.Equal(beforeBytes, readFileOptional(path)) {
		t.Fatal("test setup: refresh must change the file bytes")
	}
	if diffs := diffHostSurface(before, after); len(diffs) != 0 {
		t.Fatalf("token refresh must not trip the guard: %v", diffs)
	}
}

// TestHostSurfaceCredentialWritesTrip proves a suite write to the host
// credentials file still fails the check and names the file (sty_d9677380 AC2).
func TestHostSurfaceCredentialWritesTrip(t *testing.T) {
	const keep = "https://keep.example.com"
	const drop = "https://drop.example.com"
	seed := func(t *testing.T, s hosted.FileStore, _ string) { mustSave(t, s, testCredential(keep)) }
	mutate := func(f func(*hosted.Credential)) func(*testing.T, hosted.FileStore, string) {
		return func(t *testing.T, s hosted.FileStore, _ string) {
			c := testCredential(keep)
			f(&c)
			mustSave(t, s, c)
		}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, store hosted.FileStore, path string)
		write func(t *testing.T, store hosted.FileStore, path string)
	}{
		{"server added", seed, func(t *testing.T, s hosted.FileStore, _ string) {
			mustSave(t, s, testCredential("http://127.0.0.1:41234"))
		}},
		{"server removed", func(t *testing.T, s hosted.FileStore, p string) {
			seed(t, s, p)
			mustSave(t, s, testCredential(drop))
		}, func(t *testing.T, s hosted.FileStore, _ string) {
			if err := s.Delete(drop); err != nil {
				t.Fatal(err)
			}
		}},
		{"display_name changed", seed, mutate(func(c *hosted.Credential) { c.DisplayName = "Grace" })},
		{"email changed", seed, mutate(func(c *hosted.Credential) { c.Email = "grace@example.com" })},
		{"principal_id changed", seed, mutate(func(c *hosted.Credential) { c.PrincipalID = "usr_9" })},
		{"file created", func(*testing.T, hosted.FileStore, string) {}, mutate(func(*hosted.Credential) {})},
		{"file deleted", seed, func(t *testing.T, _ hosted.FileStore, p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}},
		{"file unparseable", seed, func(t *testing.T, _ hosted.FileStore, p string) {
			if err := os.WriteFile(p, []byte("[[credential\nnot toml"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, credentialsFileName)
			store := hosted.FileStore{Path: path}
			tc.setup(t, store, path)
			before := credSurface(root)
			tc.write(t, store, path)
			diffs := diffHostSurface(before, credSurface(root))
			if len(diffs) == 0 {
				t.Fatal("suite write to host credentials must trip the guard")
			}
			if !strings.Contains(strings.Join(diffs, "\n"), credentialsFileName) {
				t.Fatalf("diff must name %s: %v", credentialsFileName, diffs)
			}
		})
	}
}

// TestHostSurfaceToleratesHealthzCache proves the guard skips the run/ healthz
// cache that any host satelle CLI rewrites during the suite (sty_9c6fedb3 AC1),
// while a suite write anywhere else under host SATELLE_HOME still trips it.
func TestHostSurfaceToleratesHealthzCache(t *testing.T) {
	const key = "satelle-16882c39"
	seed := func(t *testing.T, withRun bool) hostRoots {
		home := filepath.Join(t.TempDir(), ".satelle")
		mustWrite(t, filepath.Join(home, "config.toml"), "a = 1\n")
		mustWrite(t, filepath.Join(home, key, "db"), "d1")
		if withRun {
			mustWrite(t, filepath.Join(home, "run", "healthz.json"), `{"ok":true,"at":1}`)
		}
		return hostRoots{Roots: hostguard.Roots{SatelleHome: home}, preExistingKeys: map[string]struct{}{key: {}}}
	}
	refresh := func(t *testing.T, home string) {
		mustWrite(t, filepath.Join(home, "run", "healthz.json"), `{"ok":true,"at":2}`)
		mustWrite(t, filepath.Join(home, "run", ".healthz-123"), "tmp")
	}
	for _, tc := range []struct {
		name    string
		withRun bool
	}{{"cache rewritten", true}, {"cache created", false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := seed(t, tc.withRun)
			before := captureHostSurfaceAt(r)
			refresh(t, r.SatelleHome)
			if diffs := diffHostSurface(before, captureHostSurfaceAt(r)); len(diffs) != 0 {
				t.Fatalf("a host healthz cache refresh must not trip the guard: %v", diffs)
			}
		})
	}
	for _, tc := range []struct {
		name, want string
		write      func(t *testing.T, home string)
	}{
		{"stray top-level file", "stray.json", func(t *testing.T, home string) {
			mustWrite(t, filepath.Join(home, "stray.json"), "{}")
		}},
		{"config.toml modified", "config.toml", func(t *testing.T, home string) {
			mustWrite(t, filepath.Join(home, "config.toml"), "a = 2\n")
		}},
		{"new key dir", "x-0000abcd", func(t *testing.T, home string) {
			mustWrite(t, filepath.Join(home, "x-0000abcd", "db"), "d")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := seed(t, true)
			before := captureHostSurfaceAt(r)
			refresh(t, r.SatelleHome)
			tc.write(t, r.SatelleHome)
			diffs := diffHostSurface(before, captureHostSurfaceAt(r))
			if len(diffs) != 1 || !strings.Contains(diffs[0], tc.want) {
				t.Fatalf("a suite write to host SATELLE_HOME must trip the guard and name %s alone: %v", tc.want, diffs)
			}
		})
	}
}

// TestSuiteXDGConfigIsolated proves TestMain sandboxed XDG_CONFIG_HOME away from
// the operator's config root (sty_d9677380 AC3).
func TestSuiteXDGConfigIsolated(t *testing.T) {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		t.Fatal("XDG_CONFIG_HOME must be set for the suite")
	}
	if !strings.HasPrefix(filepath.Clean(xdg), filepath.Clean(os.TempDir())+string(filepath.Separator)) {
		t.Fatalf("XDG_CONFIG_HOME %q must be a temp dir under %q", xdg, os.TempDir())
	}
	host := hostRootsAtStart.xdgConfig
	if host == "" {
		t.Skip("no host XDG config root was resolvable at suite start")
	}
	if filepath.Join(xdg, "satelle") == host {
		t.Fatalf("suite XDG_CONFIG_HOME %q resolves to the host root %q", xdg, host)
	}
	path, err := hosted.CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if path == filepath.Join(host, credentialsFileName) {
		t.Fatalf("hosted.CredentialsPath %q reaches the host credentials", path)
	}
}

func TestDogfoodFlow(t *testing.T) {
	bin := testBin
	repo := t.TempDir()

	// init scaffolds the repo. DB is home-keyed (sty_4660bbe1), not under .satelle/.
	out := mustRun(t, bin, repo, "init")
	for _, want := range []string{".satelle/satelle.toml", "+ .gitignore"} {
		if !strings.Contains(out, want) {
			t.Errorf("init output missing %q:\n%s", want, out)
		}
	}
	homeDB := filepath.Join(runtimeRoot(t, repo), config.DefaultDBName)
	if !strings.Contains(out, homeDB) && !strings.Contains(out, runtimeRoot(t, repo)) {
		t.Errorf("init output missing home-keyed runtime path %s:\n%s", homeDB, out)
	}
	for _, rel := range []string{".satelle/satelle.toml", ".satelle/workflows/README.md"} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Errorf("init did not create %s", rel)
		}
	}
	if _, err := os.Stat(homeDB); err != nil {
		t.Errorf("init did not create home-keyed db %s: %v", homeDB, err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".satelle", config.DefaultDBName)); err == nil {
		t.Error("init must not write satelle.db under the repo")
	}

	// init is idempotent.
	out = mustRun(t, bin, repo, "init")
	if strings.Contains(out, "  + ") {
		t.Errorf("second init created something:\n%s", out)
	}

	// The materialised baseline gates are now active; stub the reviewer so the
	// status transitions below stay hermetic (sty_5b8bd8b2).
	stubReviewerAccept(t, repo)

	// Index the substrate init materialised (baseline workflow + step skill), as a
	// real session does at SessionStart, so a later authoring index is incremental.
	mustRun(t, bin, repo, "reindex")

	// Create a story and a task.
	out = mustRun(t, bin, repo, "story", "create", "--title", "Dogfood satelle", "--priority", "high", "--tags", "mvp,core")
	if !strings.Contains(out, `"sty_`) || !strings.Contains(out, "Dogfood satelle") {
		t.Fatalf("story create output:\n%s", out)
	}
	storyID := extractID(out, "sty_")
	mustRun(t, bin, repo, "task", "create", "--title", "write release notes")

	// The seeded default's CODED estimate gate enforces out of the box
	// (sty_f804caaa): begin-work without an estimate is rejected deterministically.
	if out, err := run(t, bin, repo, "story", "set", storyID, "--status", "in_progress"); err == nil || !strings.Contains(out, "no plan estimate recorded") {
		t.Fatalf("begin-work without an estimate should be rejected by the coded gate: err=%v\n%s", err, out)
	}
	mustRun(t, bin, repo, "story", "estimate", storyID, "--time", "10m")

	// Move the story along the seeded default workflow.
	out = mustRun(t, bin, repo, "story", "set", storyID, "--status", "in_progress")
	if !strings.Contains(out, `"status": "in_progress"`) {
		t.Errorf("story set status:\n%s", out)
	}

	// Lifecycle events landed in the ledger.
	out = mustRun(t, bin, repo, "ledger", "list", "--story", storyID)
	if !strings.Contains(out, "story_created") || !strings.Contains(out, "status_transition") {
		t.Errorf("ledger missing lifecycle events:\n%s", out)
	}

	// Author markdown and index it.
	docsDir := filepath.Join(repo, ".satelle", "documents")
	if err := os.WriteFile(filepath.Join(docsDir, "onboarding.md"), []byte("# Onboarding\n\nhi"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = mustRun(t, bin, repo, "reindex")
	if !strings.Contains(out, `"indexed": 1`) {
		t.Errorf("index output:\n%s", out)
	}
	out = mustRun(t, bin, repo, "doc", "get", "documents", "onboarding")
	if !strings.Contains(out, `"headline": "Onboarding"`) {
		t.Errorf("doc get headline:\n%s", out)
	}

	// status reflects the counts.
	out = mustRun(t, bin, repo, "status")
	for _, want := range []string{"stories", "tasks", "indexed documents   1"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
}

func TestServeServesProjectPage(t *testing.T) {
	bin := testBin
	repo := t.TempDir()
	mustRun(t, bin, repo, "init")
	mustRun(t, bin, repo, "story", "create",
		"--title", "Render me",
		"--body", "visible in push-fed UI",
		"--acceptance", "1. listed",
		"--category", "chore",
	)

	const port = "8791"
	base := "http://127.0.0.1:" + port
	// Point workspace add at this serve instance.
	localBody := fmt.Sprintf("[review]\ngate_create = false\n\n[server]\nendpoint = %q\n", base)
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "satelle.local.toml"), []byte(localBody), 0o644); err != nil {
		t.Fatal(err)
	}

	_ = StartServeHealthy(t, bin, repo, isolatedEnv(t), 5*time.Second, "--port", port)
	seedWorkspaceAdd(t, bin, repo, base)

	slug := filepath.Base(repo)

	// / is the push-fed workspace landing — lists partitions after workspace add.
	landing := httpGet(t, base+"/")
	for _, want := range []string{"workspace", `href="/r/` + slug + `/"`, "satelle"} {
		if !strings.Contains(landing, want) {
			t.Errorf("landing missing %q:\n%s", want, landing)
		}
	}

	// The project page is under /r/{slug}/.
	body := httpGet(t, base+"/r/"+slug+"/")
	for _, want := range []string{"Render me", "Stories", "Tasks", "satelle"} {
		if !strings.Contains(body, want) {
			t.Errorf("project page missing %q", want)
		}
	}
	if code := httpStatus(t, base+"/nope"); code != 404 {
		t.Errorf("unknown path = %d, want 404", code)
	}
}

// --- helpers ---

func extractID(out, prefix string) string {
	i := strings.Index(out, prefix)
	if i < 0 {
		return ""
	}
	rest := out[i:]
	end := strings.IndexAny(rest, `"`)
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func waitHealthy(t *testing.T, url string, timeout time.Duration) bool {
	t.Helper()
	// Per-request timeout so a blackholed/half-open port fails the poll quickly
	// instead of hanging DefaultClient until TCP timeout (~minutes) and blowing
	// past the overall deadline (browser tests on contended local ports).
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond) // poll tick
	}
	return false
}

// freeListenPort binds 127.0.0.1:0 and returns the OS-assigned free port string.
// Prefer this over hardcoded ports: leftover satelle processes and blackholed
// high ports make fixed-port healthz checks false-green or false-fail.
func freeListenPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeListenPort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return strconv.Itoa(port)
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		b.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	return b.String()
}

func httpStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestInitDeploysDefaultSolution asserts a fresh init lands the COMPLETE default
// solution virtually (sty_29e5a9a5): no unedited markdown on disk, validators
// green, execution category resolves to the task-execution workflow.
func TestInitDeploysDefaultSolution(t *testing.T) {
	bin := testBin
	repo := t.TempDir()
	mustRun(t, bin, repo, "init")

	// Virtual sparse defaults: no seed files for workflows/skills.
	for _, rel := range []string{
		".satelle/workflows/done.toml",
		".satelle/workflows/step.toml",
		".satelle/skills/satelle-step-summary.md",
	} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err == nil {
			t.Errorf("init must not seed %s", rel)
		}
	}

	// Effective process is listed and validators pass against the virtual set.
	mustRun(t, bin, repo, "reindex")
	out := mustRun(t, bin, repo, "substrate", "list", "--json")
	if !strings.Contains(out, `"name": "done"`) || !strings.Contains(out, `"provenance": "default"`) {
		t.Errorf("substrate list missing the virtual route source:\n%s", out)
	}
	mustRun(t, bin, repo, "workflow", "validate")
	mustRun(t, bin, repo, "skill", "validate")

	// An execution resolves to the task-execution workflow, not the wildcard: the
	// head (active) entry of the ordered list for the execution kind-category.
	out = mustRun(t, bin, repo, "workflow", "list", "--category", "execution")
	firstObj := out
	if i := strings.Index(out, "}"); i >= 0 {
		firstObj = out[:i]
	}
	if !strings.Contains(firstObj, `"name": "default"`) {
		t.Errorf("head lifecycle for an execution is not the shipped route:\n%s", out)
	}

	// And a run can be created against an authored task: init seeds no example
	// task (sty_04ec1fe6), so author one, then create a run against it.
	authored := "---\nid: tsk_run\ntype: task\nstatus: backlog\n---\n\n# Runnable\n\nACTION: do the thing. VERIFICATION: it is done.\n"
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "tasks", "tsk_run.md"), []byte(authored), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, bin, repo, "reindex")
	out = mustRun(t, bin, repo, "execution", "create", "--parent", "tsk_run", "--title", "run 1")
	if !strings.Contains(out, `"exe_`) {
		t.Fatalf("execution create output:\n%s", out)
	}
}

// TestRebaseResetsSubstrate asserts `satelle rebase` aborts without confirmation,
// and with --yes backs up the customized substrate to a timestamped dir, wipes
// it, and redeploys the complete default solution (sty_a7cbd6dd).
func TestRebaseResetsSubstrate(t *testing.T) {
	bin := testBin
	repo := t.TempDir()
	mustRun(t, bin, repo, "init")

	// Customize: drift a seeded skill, add an extra authored workflow.
	skill := filepath.Join(repo, ".satelle", "skills", "satelle-code-ac-review.md")
	if err := os.WriteFile(skill, []byte("# drifted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(repo, ".satelle", "workflows", "extra-workflow.md")
	if err := os.WriteFile(extra, []byte("# extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Authored task and document, each carrying a distinct marker, so the
	// untouched-class assertion below proves CONTENT survived rather than that a
	// same-named file happens to exist.
	authoredTask := filepath.Join(repo, ".satelle", "tasks", "tsk_authored-probe.md")
	if err := os.WriteFile(authoredTask,
		[]byte("---\nid: tsk_authored-probe\ntype: task\nstatus: done\n---\n\nACTION: MARKER-TASK. VERIFICATION: done.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	authoredDoc := filepath.Join(repo, ".satelle", "documents", "authored-probe.md")
	if err := os.WriteFile(authoredDoc,
		[]byte("---\nname: authored-probe\ntype: note\n---\n\nMARKER-DOCUMENT\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No confirmation (empty stdin) → abort, nothing changed.
	out, err := run(t, bin, repo, "rebase")
	if err != nil || !strings.Contains(out, "aborted") {
		t.Fatalf("unconfirmed rebase: err=%v\n%s", err, out)
	}
	if _, serr := os.Stat(extra); serr != nil {
		t.Error("unconfirmed rebase removed the extra workflow")
	}

	// Confirmed rebase: backup + wipe, and NO redeploy — the wipe IS the reset,
	// because the embedded defaults govern through the read-time overlay
	// (sty_cc550a88).
	out = mustRun(t, bin, repo, "rebase", "--yes")
	if !strings.Contains(out, "backed up") {
		t.Errorf("rebase report incomplete:\n%s", out)
	}
	if _, serr := os.Stat(skill); serr == nil {
		t.Error("rebase left the drifted skill on disk — the reset returns it to its virtual default")
	}
	if _, serr := os.Stat(extra); serr == nil {
		t.Error("rebase left the extra authored workflow in the live dir")
	}

	// AC1: the repo must RUN immediately after a rebase. Its absence is what let
	// the agents-layer wipe ship — every existing assertion inspected files, and
	// none asked the binary to do anything (sty_72ccafaa).
	mustRun(t, bin, repo, "status")

	// AC4: the files rebase must never touch, asserted by CONTENT so a file that
	// is recreated-but-emptied fails too. This is the class guard — the next time
	// an authored file moves into a wiped dir, this catches it.
	for _, tc := range []struct{ rel, marker string }{
		{".satelle/workflows/agents.toml", "["},
		{".satelle/constitution.md", "#"},
		{".satelle/satelle.toml", "="},
		{".satelle/tasks/tsk_authored-probe.md", "MARKER-TASK"},
		{".satelle/documents/authored-probe.md", "MARKER-DOCUMENT"},
	} {
		b, rerr := os.ReadFile(filepath.Join(repo, filepath.FromSlash(tc.rel)))
		if rerr != nil {
			t.Errorf("rebase removed %s — it is not part of the default substrate: %v", tc.rel, rerr)
			continue
		}
		if !strings.Contains(string(b), tc.marker) {
			t.Errorf("rebase emptied %s (no %q in it)", tc.rel, tc.marker)
		}
	}

	// The backup holds the pre-rebase files.
	backups := filepath.Join(runtimeRoot(t, repo), "backups")
	entries, rerr := os.ReadDir(backups)
	if rerr != nil || len(entries) != 1 {
		t.Fatalf("expected one timestamped backup dir: %v %v", entries, rerr)
	}
	backupDir := filepath.Join(backups, entries[0].Name())
	if _, serr := os.Stat(filepath.Join(backupDir, "workflows", "extra-workflow.md")); serr != nil {
		t.Error("backup missing the extra authored workflow")
	}
	if b, _ := os.ReadFile(filepath.Join(backupDir, "skills", "satelle-code-ac-review.md")); !strings.Contains(string(b), "# drifted") {
		t.Error("backup missing the drifted skill bytes")
	}
}

// TestStoryRestamp exercises the first-class re-stamp (sty_ed3386cf): a story
// stamped at create picks up a category-specific workflow authored later — the
// re-categorise → restamp flow — with the change ledgered; an invalid target is
// rejected without touching the stamp.
func TestStoryRestamp(t *testing.T) {
	bin := testBin
	repo := t.TempDir()
	mustRun(t, bin, repo, "init")
	mustRun(t, bin, repo, "reindex")

	// A feature story stamps the seeded wildcard base workflow at create.
	// governance is a fixture-only category later — silence default-warn notices.
	writeFile(t, filepath.Join(repo, ".satelle", "satelle.local.toml"),
		"[review]\ngate_create = false\n\n[categories]\nenforce = \"off\"\n")
	out := mustRun(t, bin, repo, "story", "create", "--title", "Assess the rollout", "--category", "feature")
	if !strings.Contains(out, `"workflow:default"`) {
		t.Fatalf("create did not stamp the shipped route:\n%s", out)
	}
	id := extractID(out, "sty_")

	// A category-specific governance workflow is authored AFTER create.
	gov := `---
name: gov-workflow
scope: project
type: workflow
tags: [type:workflow]
applies_to: ["governance"]
description: Governance lifecycle moving backlog → in_progress → done with a cancelled exit.
---

# governance workflow

` + "```dot\n" + `digraph w {
  backlog [shape=Mdiamond]
  in_progress [agent=executor]
  done [shape=Msquare, agent=reviewer, prompt="@skill:satelle-story-done-review"]
  cancelled [agent=reviewer, prompt="@skill:satelle-story-cancel-review"]
  backlog -> in_progress
  in_progress -> done
  backlog -> cancelled
  in_progress -> cancelled
}` + "\n```\n"
	writeFile(t, filepath.Join(repo, ".satelle", "workflows", "gov-workflow.md"), gov)
	mustRun(t, bin, repo, "reindex")

	// Re-categorise, then restamp re-resolves from the CURRENT category.
	mustRun(t, bin, repo, "story", "set", id, "--category", "governance")
	out = mustRun(t, bin, repo, "story", "restamp", id)
	if !strings.Contains(out, `"workflow:gov-workflow"`) || strings.Contains(out, `"workflow:default"`) {
		t.Fatalf("restamp did not swap the governing workflow:\n%s", out)
	}

	// The trail records old -> new. The ledger JSON escapes ">" (>), so
	// match the escaped body as printed.
	out = mustRun(t, bin, repo, "ledger", "list", "--story", id)
	if !strings.Contains(out, "re-stamped: default -\\u003e gov-workflow") {
		t.Errorf("ledger missing the re-stamp row:\n%s", out)
	}

	// An unknown target is rejected and the stamp is untouched.
	if out, err := run(t, bin, repo, "story", "restamp", id, "--workflow", "nope"); err == nil || !strings.Contains(out, "does not resolve") {
		t.Errorf("unknown-workflow restamp should fail: err=%v\n%s", err, out)
	}
	out = mustRun(t, bin, repo, "story", "get", id)
	if !strings.Contains(out, `"workflow:gov-workflow"`) {
		t.Errorf("stamp changed after a rejected restamp:\n%s", out)
	}
}

// TestInitAgentGuidance asserts real init output ends with the agent-facing note
// when the repo carries CLAUDE.md (sty_4c406061): fold satelle into the
// instruction file, preferring `satelle help` over duplicated docs.
func TestInitAgentGuidance(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "CLAUDE.md"), "# my instructions\n")
	out := mustRun(t, testBin, repo, "init")
	for _, want := range []string{"Agent note:", "CLAUDE.md", `"## satelle" section`, "satelle help"} {
		if !strings.Contains(out, want) {
			t.Errorf("init output missing %q:\n%s", want, out)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "CLAUDE.md")); string(got) != "# my instructions\n" {
		t.Errorf("init modified CLAUDE.md:\n%s", got)
	}
}
