package gittest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

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
				write(t, filepath.Join(host, ".gitconfig"), "[core]\n\texcludesFile = "+filepath.ToSlash(ignore)+"\n[user]\n\tname = host\n\temail = host@host\n")
			}
			t.Setenv("HOME", host)
			t.Setenv("XDG_CONFIG_HOME", xdg)
			for _, kv := range Env() {
				k, _, _ := strings.Cut(kv, "=")
				if !strings.HasPrefix(k, "GIT_CONFIG_") {
					continue
				}
				t.Setenv(k, "")
				os.Unsetenv(k)
			}

			ctl := t.TempDir()
			if out, err := exec.Command("git", "-C", ctl, "init", "-q").CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, out)
			}
			if err := exec.Command("git", "-C", ctl, "check-ignore", "-q", ".satelle/x").Run(); err != nil {
				t.Fatalf("the hostile host does not ignore .satelle/ (control failed): %v", err)
			}

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

func TestEnvTurnsOffAutomaticGcAndMaintenance(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	Git(t, dir, "init", "-q")
	if got := Git(t, dir, "config", "--get", "gc.auto"); got != "0" {
		t.Errorf("gc.auto = %q, want 0", got)
	}
	if got := Git(t, dir, "config", "--type=bool", "--get", "maintenance.auto"); got != "false" {
		t.Errorf("maintenance.auto = %q, want false", got)
	}
	Git(t, dir, "config", "gc.auto", "1")
	if got := Git(t, dir, "config", "--get", "gc.auto"); got != "0" {
		t.Errorf("gc.auto with a repo-local gc.auto=1 = %q, want 0", got)
	}
}

func TestEnvStartsNoAutomaticGc(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	var bare []string
	for _, kv := range Env() {
		if !strings.HasPrefix(kv, "GIT_CONFIG_COUNT=") && !strings.HasPrefix(kv, "GIT_CONFIG_KEY_") && !strings.HasPrefix(kv, "GIT_CONFIG_VALUE_") {
			bare = append(bare, kv)
		}
	}
	bare = append(bare, "GIT_CONFIG_COUNT=0")
	controlGit := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), bare...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	gittestGit := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		Git(t, dir, args...)
	}
	commitFixture := func(t *testing.T, git func(*testing.T, string, ...string)) string {
		t.Helper()
		dir := t.TempDir()
		git(t, dir, "init", "-q")
		git(t, dir, "config", "gc.auto", "1")
		git(t, dir, "config", "gc.autoDetach", "false")
		git(t, dir, "config", "maintenance.gc.enabled", "true")
		for i := 0; i < 2000; i++ {
			write(t, filepath.Join(dir, fmt.Sprintf("f%04d", i)), strconv.Itoa(i))
		}
		git(t, dir, "add", "-A")
		bucket, err := os.ReadDir(filepath.Join(dir, ".git", "objects", "17"))
		if err != nil || len(bucket) < 2 {
			t.Fatalf("fixture error: .git/objects/17 holds %d entries (%v), want at least 2", len(bucket), err)
		}
		git(t, dir, "commit", "-q", "-m", "c")
		return dir
	}
	packs := func(t *testing.T, dir string) []string {
		t.Helper()
		m, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.pack"))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	t.Run("control", func(t *testing.T) {
		if got := packs(t, commitFixture(t, controlGit)); len(got) == 0 {
			t.Fatal("git without the injected config made no pack on commit (control failed)")
		}
	})
	t.Run("gittest env", func(t *testing.T) {
		if got := packs(t, commitFixture(t, gittestGit)); len(got) != 0 {
			t.Fatalf("git under Env() ran automatic gc on commit: packs %v", got)
		}
	})
}

func mkdir(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}
