// edit_exempt_managed_test.go — the seeded edit-gate exemption list and the
// binary's own deployed footprint must be ONE answer (sty_926cfcdc). satelle
// writes files into a repo unasked — the managed .gitignore block, and harness
// hook scaffolds installed lazily mid-session with no story engaged. If those
// paths are not seeded exempt, the binary's own write trips its own stop gate
// and the session is refused for something it did not do.
//
// These tests pin the three surfaces that have to agree: what init SEEDS, what
// the GATE classifies as exempt, and what the embedded substrate-only check
// ALLOWS. Product paths staying gated is pinned alongside, so widening the list
// is always a deliberate edit under review rather than drift.
package cli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// TestManagedEditExemptEntriesAreDeployedFootprint pins the managed set to
// exactly the paths the binary deploys. A new entry here means the binary
// started writing somewhere new; anything else belongs in the operator's own
// list, not the seeded default (AC3).
func TestManagedEditExemptEntriesAreDeployedFootprint(t *testing.T) {
	want := []string{".gitignore", ".claude/", ".grok/"}
	if strings.Join(managedEditExemptEntries, ",") != strings.Join(want, ",") {
		t.Fatalf("managedEditExemptEntries = %v, want %v — widening the seeded default is a deliberate change, not a drift",
			managedEditExemptEntries, want)
	}
	if strings.Join(managedDraftExemptPrefixes, ",") != "/tmp/" {
		t.Fatalf("managedDraftExemptPrefixes = %v, want [/tmp/]", managedDraftExemptPrefixes)
	}
	if got := defaultEditExemptTOML(); got != `[".satelle/", ".gitignore", ".claude/", ".grok/", "/tmp/"]` {
		t.Errorf("defaultEditExemptTOML() = %s", got)
	}
	// The scaffold seeds from the helper, so the two cannot drift apart.
	if !strings.Contains(scaffoldToml, "\n[gate]\nedit_exempt_paths = "+defaultEditExemptTOML()+"\n") {
		t.Error("scaffoldToml must seed [gate] edit_exempt_paths from defaultEditExemptTOML")
	}
}

// TestManagedEditExemptGlobsAreNarrow pins the seeded dump-name set. Any
// matching filename bypasses the edit gate, so widening this list is a
// deliberate reviewed edit, not drift (sty_fefc88cd).
func TestManagedEditExemptGlobsAreNarrow(t *testing.T) {
	want := []string{"sty_*_body.md", "sty_*_ac.md"}
	if strings.Join(managedEditExemptGlobs, ",") != strings.Join(want, ",") {
		t.Fatalf("managedEditExemptGlobs = %v, want %v — widening the seeded dump names is a deliberate change",
			managedEditExemptGlobs, want)
	}
	if got := defaultEditExemptGlobsTOML(); got != `["sty_*_body.md", "sty_*_ac.md"]` {
		t.Errorf("defaultEditExemptGlobsTOML() = %s", got)
	}
	if !strings.Contains(scaffoldToml, "edit_exempt_globs = "+defaultEditExemptGlobsTOML()) {
		t.Error("scaffoldToml must seed [gate] edit_exempt_globs from defaultEditExemptGlobsTOML")
	}
	need := gitignoreNeeded("", t.TempDir())
	for _, g := range want {
		var found bool
		for _, n := range need {
			found = found || n == g
		}
		if !found {
			t.Errorf("needed .gitignore set missing dump pattern %q: %v", g, need)
		}
	}
}

// TestSeededExemptionCoversLazyHarnessWrite is the AC2 regression: a repo whose
// only dirty path is satelle's own lazily-installed harness wiring must not be
// reported as an ungated edit. It asks the GATE's own classifier (the one
// dirtyGatedPaths uses), not a parallel prefix match, and covers both the
// directory and file forms git porcelain can report.
func TestSeededExemptionCoversLazyHarnessWrite(t *testing.T) {
	repo := t.TempDir()
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	cfg, _, err := config.Load(filepath.Join(repo, config.DefaultDataDir, config.ConfigName))
	if err != nil {
		t.Fatalf("load seeded config: %v", err)
	}
	roots := cfg.ResolveEditExemptPaths(repo)

	exempt := []string{
		".claude/", ".claude/settings.json", ".claude/hooks/satelle.sh",
		".grok/", ".grok/settings.json",
		".gitignore", ".satelle/satelle.toml",
	}
	for _, rel := range exempt {
		if !editExempt(roots, repo, resolveAbsTarget(repo, rel)) {
			t.Errorf("%q must be edit-gate exempt — satelle writes it itself, with no story engaged", rel)
		}
	}

	// AC3: product paths stay gated. The seeded list buys satelle's own
	// footprint an exemption and nothing else.
	for _, rel := range []string{"main.go", "internal/cli/cmd_init.go", "Makefile", ".github/workflows/ci.yml"} {
		if editExempt(roots, repo, resolveAbsTarget(repo, rel)) {
			t.Errorf("%q must stay gated — only satelle's own deployed footprint is exempt", rel)
		}
	}
}

// TestSeededExemptionSurvivesStopcheck runs the AC2 case through the stop gate's
// own path: a tree whose only uncommitted change is satelle's harness wiring
// reports no ungated paths, so the session is not refused for a write the binary
// made on its own initiative.
func TestSeededExemptionSurvivesStopcheck(t *testing.T) {
	repo := t.TempDir()
	if err := runInitTest(t, io.Discard, repo); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	gitInitCommit(t, repo)

	// satelle's lazy harness write: no story engaged, no operator action.
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// exemptTarget resolves config from the working directory, so run there.
	t.Chdir(repo)
	gated, err := dirtyGatedPaths(repo)
	if err != nil {
		t.Fatalf("dirtyGatedPaths: %v", err)
	}
	if len(gated) != 0 {
		t.Fatalf("stop gate would refuse satelle's own write: %v\n%s", gated, stopcheckReason(gated))
	}

	// A product edit in the same tree is still ungated work.
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gated, err = dirtyGatedPaths(repo)
	if err != nil {
		t.Fatalf("dirtyGatedPaths: %v", err)
	}
	if len(gated) != 1 || gated[0] != "main.go" {
		t.Fatalf("product edit must stay gated, got %v", gated)
	}
}

// gitInitCommit makes repo a git repo with one commit, so `git status
// --porcelain` reports only what the test dirties afterwards.
func gitInitCommit(t *testing.T, repo string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "test"},
		{"add", "-A"},
		{"commit", "-qm", "seed"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}
