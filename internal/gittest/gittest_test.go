package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A host whose git ignores .satelle/ (through core.excludesFile in its global
// config, or through the XDG default ignore file alone) does not reach a test
// that runs git through this package.
func TestEnvNeutralisesAHostileHost(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for _, viaGlobal := range []bool{true, false} {
		name := "xdg-default-only"
		if viaGlobal {
			name = "global-excludesFile"
		}
		t.Run(name, func(t *testing.T) {
			host := t.TempDir()
			xdg := filepath.Join(host, "xdg")
			write(t, mkdir(t, filepath.Join(xdg, "git"), "ignore"), ".satelle/\n")
			if viaGlobal {
				ignore := filepath.Join(host, "global-ignore")
				write(t, ignore, ".satelle/\n")
				write(t, filepath.Join(host, ".gitconfig"), "[core]\n\texcludesFile = "+ignore+"\n[user]\n\tname = host\n\temail = host@host\n")
			}
			t.Setenv("HOME", host)
			t.Setenv("XDG_CONFIG_HOME", xdg)
			for _, k := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
				t.Setenv(k, "")
				os.Unsetenv(k)
			}

			// Control: the host really ignores .satelle/ for an unisolated git.
			ctl := t.TempDir()
			if out, err := exec.Command("git", "-C", ctl, "init", "-q").CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, out)
			}
			if err := exec.Command("git", "-C", ctl, "check-ignore", "-q", ".satelle/x").Run(); err != nil {
				t.Fatalf("the hostile host does not ignore .satelle/ (control failed): %v", err)
			}

			// Through the helper, .satelle/ is committed.
			dir := t.TempDir()
			Git(t, dir, "init", "-q")
			write(t, mkdir(t, filepath.Join(dir, ".satelle"), "x"), "x\n")
			Git(t, dir, "add", "-A")
			Git(t, dir, "commit", "-q", "-m", "c")
			if got := Git(t, dir, "ls-files"); !strings.Contains(got, ".satelle/x") {
				t.Fatalf("ls-files = %q, want .satelle/x committed", got)
			}
		})
	}
}

// mkdir makes dir and returns the path of name inside it.
func mkdir(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}
