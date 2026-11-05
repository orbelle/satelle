//go:build integration

package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sty_0f471251: upgrading the binary invalidates the deployed scaffolding of
// every OTHER registered repo, and healing them by hand is N × (cd + satelle
// init) — the chore an operator defers, which is how an estate ends up mostly
// stale. `satelle init --all` is the bulk heal, dry-run by default.

type estate struct {
	home  string
	env   []string
	clean string // registered, healthy
	stale string // registered, scaffolding corrupted
	gone  string // registered, then deleted
}

func (e estate) run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(testBin, args...)
	cmd.Dir = dir
	cmd.Env = e.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hookScript is the deployed harness wrapper whose drift is what
// DetectScaffoldDrift reports.
func hookScript(repo string) string {
	return filepath.Join(repo, ".satelle", "hooks", "satelle-hook.sh")
}

func newEstate(t *testing.T) estate {
	t.Helper()
	home := t.TempDir()
	e := estate{
		home: home,
		env:  append(os.Environ(), "SATELLE_HOME="+home, "SATELLE_SERVER_ENDPOINT=none"),
	}
	for _, spec := range []struct {
		name string
		dst  *string
	}{{"clean", &e.clean}, {"stale", &e.stale}, {"gone", &e.gone}} {
		dir := t.TempDir()
		*spec.dst = dir
		if out, err := e.run(t, dir, "init", "--harness", "claude"); err != nil {
			t.Fatalf("init %s: %v\n%s", spec.name, err, out)
		}
	}
	// Corrupt one repo's deployed wrapper so it reads as stale.
	if err := os.WriteFile(hookScript(e.stale), []byte("#!/bin/sh\n# junk from an older satelle\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// And delete one entirely, leaving its registry entry dangling.
	if err := os.RemoveAll(e.gone); err != nil {
		t.Fatal(err)
	}
	return e
}

// TestInitAllDryRunChangesNothing (AC4): the default is a report. It names the
// stale repo and its findings, tells the operator how to apply, and leaves every
// byte on disk untouched.
func TestInitAllDryRunChangesNothing(t *testing.T) {
	e := newEstate(t)
	before := sha256File(t, hookScript(e.stale))

	out, err := e.run(t, e.clean, "init", "--all")
	if err != nil {
		t.Fatalf("init --all (dry run) must not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, e.stale) {
		t.Errorf("dry run must name the stale repo:\n%s", out)
	}
	if !strings.Contains(out, "--yes") {
		t.Errorf("dry run must name the action required to apply:\n%s", out)
	}
	if after := sha256File(t, hookScript(e.stale)); after != before {
		t.Errorf("dry run must change nothing on disk (hook script was rewritten)")
	}
}

// TestInitAllYesHealsStaleRepos (AC3/AC4): --yes heals, and the healed repo
// really is healthy afterwards.
func TestInitAllYesHealsStaleRepos(t *testing.T) {
	e := newEstate(t)
	before := sha256File(t, hookScript(e.stale))

	out, err := e.run(t, e.clean, "init", "--all", "--yes")
	if err != nil {
		t.Fatalf("init --all --yes: %v\n%s", err, out)
	}
	if !strings.Contains(out, "HEALED") {
		t.Errorf("apply must report what it healed:\n%s", out)
	}
	if after := sha256File(t, hookScript(e.stale)); after == before {
		t.Errorf("the stale wrapper should have been rewritten")
	}
	// A second dry run should now find nothing stale.
	out2, err := e.run(t, e.clean, "init", "--all")
	if err != nil {
		t.Fatalf("second dry run: %v\n%s", err, out2)
	}
	if strings.Contains(out2, "STALE") {
		t.Errorf("nothing should remain stale after a heal:\n%s", out2)
	}
}

// TestInitAllSkipsDanglingPathsWithoutAborting (AC5): a registry entry whose
// repo is gone is reported and skipped — it must not abort the run over the
// remaining repos, because the registry legitimately carries entries for
// unmounted volumes and detached checkouts.
func TestInitAllSkipsDanglingPathsWithoutAborting(t *testing.T) {
	e := newEstate(t)

	out, err := e.run(t, e.clean, "init", "--all", "--yes")
	if err != nil {
		t.Fatalf("a dangling registry path must not fail the run: %v\n%s", err, out)
	}
	if !strings.Contains(out, e.gone) || !strings.Contains(out, "SKIP") {
		t.Errorf("the dangling path must be reported and skipped:\n%s", out)
	}
	// The real repo was still processed despite the dangling entry.
	if !strings.Contains(out, "HEALED") {
		t.Errorf("repos after the dangling entry must still be processed:\n%s", out)
	}
}

// TestInitAllPreservesAuthoredSubstrate (AC6) is the constitutional test: the
// bulk heal deploys canonical scaffolding and must never touch what the operator
// authored. The stale wrapper in the same run proves the test would catch a
// clobber rather than merely observing a no-op.
func TestInitAllPreservesAuthoredSubstrate(t *testing.T) {
	e := newEstate(t)

	// Structurally VALID authored substrate — init validates the deployed system
	// at the end, so an invalid fixture would fail the heal for reasons that have
	// nothing to do with preservation.
	authored := map[string]string{
		filepath.Join(e.stale, ".satelle", "documents", "mine.md"): "---\ntype: document\n" +
			"title: mine\ndescription: 'hand-authored document that must survive a bulk heal'\n" +
			"tags:\n- document\ntimestamp: '2026-07-31T00:00:00Z'\n---\n\n# mine\n\nHand written.\n",
		filepath.Join(e.stale, ".satelle", "workflows", "README.md"): "keep-file, edited by hand\n",
	}
	for path, body := range authored {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The operator's own config stanza.
	cfgPath := filepath.Join(e.stale, ".satelle", "satelle.toml")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg = append(cfg, []byte("\n# operator's own note\n")...)
	if err := os.WriteFile(cfgPath, cfg, 0o644); err != nil {
		t.Fatal(err)
	}

	sums := map[string]string{cfgPath: sha256File(t, cfgPath)}
	for path := range authored {
		sums[path] = sha256File(t, path)
	}
	staleBefore := sha256File(t, hookScript(e.stale))

	out, err := e.run(t, e.clean, "init", "--all", "--yes")
	if err != nil {
		t.Fatalf("init --all --yes: %v\n%s", err, out)
	}

	for path, want := range sums {
		if got := sha256File(t, path); got != want {
			t.Errorf("authored substrate must be preserved byte-for-byte, changed: %s", path)
		}
	}
	// Control: the run really did heal something, so the assertions above are not
	// vacuously passing on a no-op.
	if sha256File(t, hookScript(e.stale)) == staleBefore {
		t.Fatal("control failed: the run healed nothing, so substrate preservation proves nothing")
	}
}

// TestInitAllOnEmptyRegistryIsBenign (AC2 sibling): safe to run blind.
func TestInitAllOnEmptyRegistryIsBenign(t *testing.T) {
	home := t.TempDir()
	env := append(os.Environ(), "SATELLE_HOME="+home, "SATELLE_SERVER_ENDPOINT=none")
	dir := t.TempDir()
	cmd := exec.Command(testBin, "init", "--all")
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init --all on an empty registry must not error: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "no registered repositories") {
		t.Errorf("expected a sensible empty-registry message, got:\n%s", out)
	}
}

// TestYesWithoutAllIsRejected: --yes only means something with --all, and
// silently ignoring it would be dishonest about whether anything was applied.
func TestYesWithoutAllIsRejected(t *testing.T) {
	e := newEstate(t)
	out, err := e.run(t, e.clean, "init", "--yes")
	if err == nil {
		t.Fatalf("--yes without --all should be rejected:\n%s", out)
	}
	if !strings.Contains(out, "--all") {
		t.Errorf("the refusal should explain the flag pairing:\n%s", out)
	}
}

// TestDoctorAllRunsFromANonSatelleDirectory (AC2): the guidance printed after an
// upgrade names `satelle doctor --all`, and an operator who has just upgraded is
// very often NOT sitting in a satelle repo. It must be safe to run blind.
func TestDoctorAllRunsFromANonSatelleDirectory(t *testing.T) {
	e := newEstate(t)
	outside := t.TempDir() // no .satelle/

	out, err := e.run(t, outside, "doctor", "--all")
	// Non-zero is legitimate here (the estate contains a stale repo); what must
	// NOT happen is the bootstrap refusing because the cwd is ungoverned.
	if strings.Contains(out, "not a satelle repo") {
		t.Fatalf("doctor --all must run from any directory: %v\n%s", err, out)
	}
	if !strings.Contains(out, e.clean) {
		t.Errorf("doctor --all should report on registered repos:\n%s", out)
	}

	// The single-repo form still refuses there, and still names the remedy.
	single, serr := e.run(t, outside, "doctor")
	if serr == nil {
		t.Errorf("plain doctor in an ungoverned directory should still refuse:\n%s", single)
	}
	if !strings.Contains(single, "satelle init") {
		t.Errorf("the refusal must still name the remedy:\n%s", single)
	}
}

// testBinVersion is the release version testBin reports.
func testBinVersion(t *testing.T, e estate) string {
	t.Helper()
	out, err := e.run(t, t.TempDir(), "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}
	f := strings.Fields(out)
	if len(f) < 2 {
		t.Fatalf("unparseable version output: %q", out)
	}
	return f[1]
}

// releaseVersion matches a stamped release; an [Unreleased] entry carried by a
// branch until the release remote stamps it never does.
var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// newestNonBreakingStamp is the newest shipped release below binVer whose range
// up to binVer crosses no ### Breaking release, read from the embedded changelog.
func newestNonBreakingStamp(t *testing.T, e estate, binVer string) string {
	t.Helper()
	out, err := e.run(t, t.TempDir(), "changelog", "--json")
	if err != nil {
		t.Fatalf("changelog: %v\n%s", err, out)
	}
	var log struct {
		Entries []struct {
			Version  string `json:"version"`
			Breaking bool   `json:"breaking"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(out), &log); err != nil {
		t.Fatalf("changelog --json: %v\n%s", err, out)
	}
	for _, en := range log.Entries { // newest first
		if strings.HasPrefix(en.Version, "serve-") {
			continue // the service binary's own releases
		}
		if !releaseVersion.MatchString(en.Version) {
			continue
		}
		if en.Version != binVer {
			return en.Version
		}
		if en.Breaking {
			break
		}
	}
	t.Skipf("binary %s is itself a breaking release, so no stamp below it is non-breaking", binVer)
	return ""
}

func readStamp(t *testing.T, repo string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, ".satelle", "deployed.version"))
	if err != nil {
		t.Fatalf("read stamp: %v", err)
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "satelle.version:"))
}

func writeStamp(t *testing.T, repo, v string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "deployed.version"), []byte("satelle.version: "+v+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestInitAllHealsStampBehindRepos (sty_2e629e93 AC1, AC2): `init --all` judged
// only deployed scaffolding, so after an upgrade across an init-heals breaking
// release it reported every refusing repo "already current" and healed nothing.
// A repo whose stamp is behind the binary is stale whether or not its
// scaffolding changed, --yes heals it as a per-repo init would (the release's
// init-heals migration included), and afterwards no repo refuses a store-backed
// verb.
func TestInitAllHealsStampBehindRepos(t *testing.T) {
	if !isReleaseTestBin(t) {
		t.Skip("testBin is not release-stamped (dev sentinel) — a dev binary never stamps or gates")
	}
	home := t.TempDir()
	e := estate{home: home, env: append(os.Environ(), "SATELLE_HOME="+home, "SATELLE_SERVER_ENDPOINT=none")}
	binVer := testBinVersion(t, e)
	nonBreaking := newestNonBreakingStamp(t, e, binVer)

	breaking, quiet, current := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{breaking, quiet, current} {
		if out, err := e.run(t, dir, "init"); err != nil {
			t.Fatalf("init %s: %v\n%s", dir, err, out)
		}
	}
	writeStamp(t, breaking, "0.0.1")
	writeStamp(t, quiet, nonBreaking)
	// The retired [sync] key 0.0.761's init-heals migration removes.
	tomlPath := filepath.Join(breaking, ".satelle", "satelle.toml")
	raw, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	seeded := strings.Replace(string(raw), "[sync]\n", "[sync]\nhold_stale_after = \"1h\"\n", 1)
	if seeded == string(raw) {
		t.Fatal("fixture: scaffold [sync] table not found")
	}
	if err := os.WriteFile(tomlPath, []byte(seeded), 0o644); err != nil {
		t.Fatal(err)
	}

	// Guard: the breaking fixture really crosses an init-heals release.
	if out, err := e.run(t, breaking, "status"); err == nil || !strings.Contains(out, "across BREAKING") {
		t.Fatalf("guard: a stamp of 0.0.1 must refuse status across an init-heals breaking release: err=%v\n%s", err, out)
	}

	sums := map[string]string{tomlPath: sha256File(t, tomlPath)}
	for _, dir := range []string{breaking, quiet, current} {
		p := filepath.Join(dir, ".satelle", "deployed.version")
		sums[p] = sha256File(t, p)
	}
	dry, err := e.run(t, current, "init", "--all")
	if err != nil {
		t.Fatalf("init --all (dry run): %v\n%s", err, dry)
	}
	staleLine := func(out, repo string) string {
		for _, ln := range strings.Split(out, "\n") {
			if strings.Contains(ln, "STALE") && strings.Contains(ln, repo) {
				return ln
			}
		}
		return ""
	}
	if ln := staleLine(dry, breaking); !strings.Contains(ln, "stamp 0.0.1 behind binary "+binVer) || !strings.Contains(ln, "init heals") {
		t.Errorf("dry run must list the repo behind a breaking release as STALE, naming the gap:\n%s", dry)
	}
	if ln := staleLine(dry, quiet); !strings.Contains(ln, "stamp "+nonBreaking+" behind") || !strings.Contains(ln, "no breaking release") {
		t.Errorf("dry run must list the repo behind a non-breaking release as STALE:\n%s", dry)
	}
	if staleLine(dry, current) != "" || !strings.Contains(dry, "2 stale, 1 already current") {
		t.Errorf("the current repo must count as current:\n%s", dry)
	}
	for p, want := range sums {
		if sha256File(t, p) != want {
			t.Errorf("dry run must change nothing on disk: %s changed", p)
		}
	}

	out, err := e.run(t, current, "init", "--all", "--yes")
	if err != nil {
		t.Fatalf("init --all --yes: %v\n%s", err, out)
	}
	if !strings.Contains(out, "2 healed, 0 failed") {
		t.Errorf("--yes must heal both stamp-behind repos:\n%s", out)
	}
	for _, dir := range []string{breaking, quiet, current} {
		if got := readStamp(t, dir); got != binVer {
			t.Errorf("%s: stamp = %s after the heal, want %s", dir, got, binVer)
		}
		if st, serr := e.run(t, dir, "status"); serr != nil || strings.Contains(st, "across BREAKING") {
			t.Errorf("%s: a store-backed verb must run after the heal: err=%v\n%s", dir, serr, st)
		}
	}
	if healed, _ := os.ReadFile(tomlPath); strings.Contains(string(healed), "hold_stale_after") {
		t.Errorf("the heal must run the release's init-heals migration (retired [sync] key still present):\n%s", healed)
	}
}
