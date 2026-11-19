//go:build linux

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/kernel"
)

func wantIgnoreWarning(t *testing.T, out, path, rule string) {
	t.Helper()
	for _, want := range []string{path + ": git ignores this file", rule, "git add -f .claude/settings.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestAgentsInstallNamesTheIgnoreRuleThatHidesTheFile(t *testing.T) {
	t.Run("repo .gitignore", func(t *testing.T) {
		isolate(t)
		dir := harnessProject(t, false)
		t.Chdir(dir)
		putFile(t, filepath.Join(dir, ".gitignore"), ".claude/\n")
		out := must(t, "agents", "install", "claude")
		wantIgnoreWarning(t, out, filepath.Join(dir, ".claude", "settings.json"), ".gitignore:1: .claude/")

		fresh := isolate(t)
		putFile(t, filepath.Join(fresh, ".gitignore"), ".claude/\n")
		out = must(t, "init", "--hooks=claude")
		wantIgnoreWarning(t, out, filepath.Join(fresh, ".claude", "settings.json"), ".gitignore:1: .claude/")
	})
	t.Run("global core.excludesFile", func(t *testing.T) {
		isolate(t)
		dir := harnessProject(t, false)
		t.Chdir(dir)
		conf := t.TempDir()
		excludes := filepath.Join(conf, "ignore")
		putFile(t, excludes, ".claude/\n")
		gitconfig := filepath.Join(conf, "gitconfig")
		putFile(t, gitconfig, "[core]\n\texcludesFile = "+excludes+"\n")
		t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
		t.Setenv("GIT_CONFIG_COUNT", "0")
		rule := excludes + ":1: .claude/"
		control := gittest.Command(t, dir, "check-ignore", "-v", ".claude/settings.json")
		control.Env = os.Environ()
		got, err := control.Output()
		if err != nil || !strings.HasPrefix(string(got), excludes+":1:.claude/\t") {
			t.Fatalf("git check-ignore -v: %v %q; want the rule from %s", err, got, excludes)
		}
		out := must(t, "agents", "install", "claude")
		wantIgnoreWarning(t, out, filepath.Join(dir, ".claude", "settings.json"), rule)
	})
}

func TestAgentsInstallSaysNothingOfIgnoreWhenNoRuleHidesTheFile(t *testing.T) {
	quiet := func(t *testing.T, out string) {
		t.Helper()
		if !strings.Contains(out, "installed claude hooks: ") {
			t.Errorf("agents install wrote nothing:\n%s", out)
		}
		for _, gone := range []string{"git ignores this file", "git add -f"} {
			if strings.Contains(out, gone) {
				t.Errorf("output has %q:\n%s", gone, out)
			}
		}
	}
	t.Run("no .gitignore", func(t *testing.T) {
		isolate(t)
		t.Chdir(harnessProject(t, false))
		quiet(t, must(t, "agents", "install", "claude"))
	})
	t.Run("an unrelated rule", func(t *testing.T) {
		isolate(t)
		dir := harnessProject(t, false)
		t.Chdir(dir)
		putFile(t, filepath.Join(dir, ".gitignore"), ".grok/\n")
		quiet(t, must(t, "agents", "install", "claude"))
	})
	t.Run("tracked under an ignore rule", func(t *testing.T) {
		isolate(t)
		dir := harnessProject(t, false)
		t.Chdir(dir)
		putFile(t, filepath.Join(dir, ".gitignore"), ".claude/\n")
		putFile(t, filepath.Join(dir, ".claude", "settings.json"), "{}\n")
		gittest.Git(t, dir, "add", "-f", ".claude/settings.json")
		gittest.Git(t, dir, "commit", "-q", "-m", "stale settings")
		out := must(t, "agents", "install", "claude")
		quiet(t, out)
		if !strings.Contains(out, "git tracks this file") {
			t.Errorf("no tracked line:\n%s", out)
		}
	})
	t.Run("no git repository", func(t *testing.T) {
		isolate(t)
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := kernel.Init(filepath.Join(dir, config.DefaultDataDir)); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		quiet(t, must(t, "agents", "install", "claude"))
	})
}
