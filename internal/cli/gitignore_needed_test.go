package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitignoreHasSetting (sty_dbc0be85 AC2): a setting is a positive or
// negated rule targeting the path or a path under it.
func TestGitignoreHasSetting(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"exact", ".satelle", true},
		{"leading slash", "/.satelle", true},
		{"trailing slash", ".satelle/", true},
		{"slash star star", ".satelle/**", true},
		{"slash star", ".satelle/*", true},
		{"negation under it", "!.satelle/x", true},
		{"sub-path only", ".satelle/logs", true},
		{"comment", "# .satelle/", false},
		{"unrelated", "node_modules/", false},
		{"prefix is not a setting", ".satellex/", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitignoreHasSetting(tc.text, ".satelle"); got != tc.want {
				t.Errorf("gitignoreHasSetting(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestGitignoreHelpDescribesAddOnly (sty_dbc0be85 AC6): init and migrate help
// describe the add-only behaviour, not a rewrite of the block.
func TestGitignoreHelpDescribesAddOnly(t *testing.T) {
	for _, name := range []string{"init", "migrate"} {
		var long string
		for _, c := range NewRootCmd().Commands() {
			if c.Name() == name {
				long = c.Long
			}
		}
		if long == "" {
			t.Fatalf("%s command missing", name)
		}
		for _, banned := range []string{"rewritten between its markers", "rewrite the managed .gitignore block"} {
			if strings.Contains(long, banned) {
				t.Errorf("%s help still claims %q", name, banned)
			}
		}
		if !strings.Contains(long, "missing") {
			t.Errorf("%s help must say only missing entries are added:\n%s", name, long)
		}
	}
}

// wantTrackedSubstrateLines is AC1's tracked-substrate shape, spelled out so
// the init tests below pin the written block rather than read it back from
// the code under test.
var wantTrackedSubstrateLines = []string{
	"!.satelle/",
	".satelle/**/.env",
	".satelle/logs",
	".satelle/hooks",
	".satelle/deployed.version",
	".satelle/*.local.toml",
	".satelle/workflows/agents.local.toml",
	".satelle/satelle",
	".satelle/tasks/*/exe_*.md",
	".satelle/tasks/*/output-exe_*.md",
}

// gitIgnores reports whether git ignores rel in repo under its full rule set.
func gitIgnores(t *testing.T, repo, rel string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "check-ignore", "-q", "--no-index", "--", rel)
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false
	} else if err != nil {
		t.Fatalf("git check-ignore %s: %v", rel, err)
	}
	return true
}

// isolateGitExcludes points git's global config at a temp file that sets
// core.excludesFile to a file holding excludes (none when empty), so neither
// the operator's ~/.gitconfig nor ~/.config/git/ignore reaches the test.
func isolateGitExcludes(t *testing.T, excludes string) (excludesPath string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "gitconfig")
	body := ""
	if excludes != "" {
		excludesPath = filepath.Join(dir, "global-ignore")
		if err := os.WriteFile(excludesPath, []byte(excludes), 0o644); err != nil {
			t.Fatal(err)
		}
		body = "[core]\n\texcludesFile = " + excludesPath + "\n"
	}
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	return excludesPath
}
