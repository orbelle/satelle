package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// AC2 (sty_f141c77f): a repository that declares no absent-wiring policy gets
// fail-closed, and the embedded defaults declare wiring for the harnesses
// satelle deploys and none for unknown.
func TestAbsentWiringPolicyDefaultsToRefuse(t *testing.T) {
	if got := (Config{}).AbsentWiringPolicy(); got != AbsentWiringRefuse {
		t.Fatalf("undeclared policy = %q, want %q", got, AbsentWiringRefuse)
	}
	var c Config
	c.Worktree.AbsentWiring = AbsentWiringFailOpen
	if got := c.AbsentWiringPolicy(); got != AbsentWiringFailOpen {
		t.Fatalf("declared policy = %q", got)
	}
}

// AC2: an unknown policy value, an unknown key under [worktree] or
// [harness.<name>], or a malformed wiring entry is refused at Load naming the
// field, and nothing under the repo is rewritten.
func TestLoad_RefusesBadAbsentWiringDeclarations(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{"unknown policy", "[worktree]\nabsent_wiring = \"allow\"\n", []string{"[worktree] absent_wiring", `"allow"`}},
		{"policy wrong case", "[worktree]\nabsent_wiring = \"Fail-Open\"\n", []string{"[worktree] absent_wiring"}},
		{"unknown worktree key", "[worktree]\nabsent_wirng = \"refuse\"\n", []string{"unknown key", "[worktree] absent_wirng"}},
		{"unknown harness key", "[harness.claude]\ngate_wirng = [\".claude/settings.json\"]\n", []string{"unknown key", "[harness.claude] gate_wirng"}},
		{"empty entry", "[harness.claude]\ngate_wiring = [\"\"]\n", []string{"[harness.claude] gate_wiring", "empty"}},
		{"pattern entry", "[harness.grok]\ngate_wiring = [\".grok/*.json\"]\n", []string{"[harness.grok] gate_wiring", "pattern"}},
		{"absolute entry", "[harness.pi]\ngate_wiring = [\"/etc/passwd\"]\n", []string{"[harness.pi] gate_wiring", "relative"}},
		{"escaping entry", "[harness.pi]\ngate_wiring = [\"../x\"]\n", []string{"[harness.pi] gate_wiring", "outside the repository"}},
		{"git entry", "[harness.pi]\ngate_wiring = [\".git/config\"]\n", []string{"[harness.pi] gate_wiring", "git"}},
		{"whole repo", "[harness.pi]\ngate_wiring = [\".\"]\n", []string{"[harness.pi] gate_wiring", "whole repository"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeWorktreeRepo(t, tc.body)
			root := RepoRootFromConfigPath(p)
			before := treeHash(t, root)
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
			stBefore, _ := os.Stat(p)
			_, _, err := Load(p)
			if err == nil {
				t.Fatalf("Load accepted %q", tc.body)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
			if after := treeHash(t, root); after != before {
				t.Error("a refusal rewrote a file under the repo")
			}
			if stAfter, _ := os.Stat(p); !stAfter.ModTime().Equal(stBefore.ModTime()) {
				t.Error("a refusal touched satelle.toml")
			}
		})
	}
}

// The same strictness applies to the per-user satelle.local.toml overlay.
func TestLoad_RefusesUnknownKeyInLocalOverlay(t *testing.T) {
	p := writeWorktreeRepo(t, "[worktree]\nabsent_wiring = \"refuse\"\n")
	local := RepoRootFromConfigPath(p) + "/" + DefaultDataDir + "/" + LocalConfigName
	if err := os.WriteFile(local, []byte("[harness.claude]\nbogus = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "[harness.claude] bogus") {
		t.Fatalf("overlay unknown key: %v", err)
	}
}

// Valid declarations load, and unrelated tables stay lenient.
func TestLoad_AcceptsWiringDeclarations(t *testing.T) {
	p := writeWorktreeRepo(t, "[worktree]\nabsent_wiring = \"fail-open\"\n\n[harness.mybot]\ngate_wiring = [\".mybot/hooks.json\"]\ncontext_limit_bytes = 100\n\n[some_other_table]\nanything = 1\n")
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AbsentWiringPolicy() != AbsentWiringFailOpen {
		t.Errorf("policy = %q", cfg.AbsentWiringPolicy())
	}
	if got := cfg.GateWiring("mybot"); len(got) != 1 || got[0] != ".mybot/hooks.json" {
		t.Errorf("GateWiring(mybot) = %v", got)
	}
}

// sty_0dca3223: the configured-harness set is the embedded tables plus the
// repo's, sorted, without the unknown fallback; the budget set adds it last.
func TestHarnesses(t *testing.T) {
	got := Config{}.Harnesses()
	if !slices.IsSorted(got) || slices.Contains(got, unknownHarness) {
		t.Errorf("Harnesses() = %v, want sorted without %q", got, unknownHarness)
	}
	for name := range EmbeddedHarness() {
		if name != unknownHarness && !slices.Contains(got, name) {
			t.Errorf("Harnesses() = %v lacks embedded %q", got, name)
		}
	}
	c := Config{Harness: map[string]HarnessConfig{"mybot": {GateWiring: []string{".mybot/hooks.json"}}}}
	with := c.Harnesses()
	if !slices.Contains(with, "mybot") || len(with) != len(got)+1 || !slices.IsSorted(with) {
		t.Errorf("Harnesses() with mybot = %v", with)
	}
	budget := c.BudgetHarnesses()
	if len(budget) != len(with)+1 || budget[len(budget)-1] != unknownHarness {
		t.Errorf("BudgetHarnesses() = %v, want Harnesses() then %q", budget, unknownHarness)
	}
}

// sty_0dca3223: PresentGateWiring is the resolved gate_wiring that exists under
// root; a repo override replaces the embedded path.
func TestPresentGateWiring(t *testing.T) {
	root := t.TempDir()
	if got := (Config{}).PresentGateWiring(root, "claude"); len(got) != 0 {
		t.Errorf("empty root: %v", got)
	}
	for _, p := range []string{".claude/settings.json", ".claude/custom.json"} {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := (Config{}).PresentGateWiring(root, "claude"); !slices.Equal(got, []string{".claude/settings.json"}) {
		t.Errorf("embedded claude: %v", got)
	}
	if got := (Config{}).PresentGateWiring(root, "pi"); len(got) != 0 {
		t.Errorf("absent pi wiring: %v", got)
	}
	c := Config{Harness: map[string]HarnessConfig{"claude": {GateWiring: []string{".claude/custom.json", ".claude/gone.json"}}}}
	if got := c.PresentGateWiring(root, "claude"); !slices.Equal(got, []string{".claude/custom.json"}) {
		t.Errorf("overridden claude: %v", got)
	}
}
