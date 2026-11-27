//go:build !windows

package remotenode

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestStageSkillRefusesSymlinksAndSkipsLocal(t *testing.T) {
	outside := t.TempDir()
	writeSkill(t, outside, map[string]string{"secret.txt": "OUTSIDE-BYTES", "sub/x.txt": "OUTSIDE-BYTES"})

	stagedFiles := func(t *testing.T, dst string) []string {
		t.Helper()
		var got []string
		_ = filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				got = append(got, p)
			}
			return nil
		})
		return got
	}
	refused := func(t *testing.T, src, name string) {
		t.Helper()
		dst := filepath.Join(t.TempDir(), "skill")
		err := stageSkill(src, dst)
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("stageSkill = %v, want a refusal naming %s", err, name)
		}
		if got := stagedFiles(t, dst); len(got) != 0 {
			t.Fatalf("a refused stage left files: %q", got)
		}
	}

	t.Run("file symlink", func(t *testing.T) {
		src := t.TempDir()
		writeSkill(t, src, map[string]string{"scripts/run.sh": "exit 0\n"})
		if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(src, "scripts", "leak")); err != nil {
			t.Fatal(err)
		}
		refused(t, src, "scripts/leak")
	})
	t.Run("directory symlink", func(t *testing.T) {
		src := t.TempDir()
		writeSkill(t, src, map[string]string{"scripts/run.sh": "exit 0\n"})
		if err := os.MkdirAll(filepath.Join(src, "assets"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "sub"), filepath.Join(src, "assets", "dir")); err != nil {
			t.Fatal(err)
		}
		refused(t, src, "assets/dir")
	})
	t.Run("the skill directory itself a symlink", func(t *testing.T) {
		real := t.TempDir()
		writeSkill(t, real, map[string]string{"scripts/run.sh": "exit 0\n"})
		link := filepath.Join(t.TempDir(), "skill")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		refused(t, link, link)
	})
	t.Run("fifo", func(t *testing.T) {
		src := t.TempDir()
		writeSkill(t, src, map[string]string{"scripts/run.sh": "exit 0\n"})
		if err := syscall.Mkfifo(filepath.Join(src, "scripts", "pipe"), 0o644); err != nil {
			t.Fatal(err)
		}
		refused(t, src, "scripts/pipe")
	})
	t.Run("local-only paths are left out and modes carried", func(t *testing.T) {
		src := t.TempDir()
		writeSkill(t, src, map[string]string{
			"scripts/run.sh":     "exit 0\n",
			"config.local.toml":  "secret\n",
			"assets/x.local.md":  "secret\n",
			"x.local/inside.txt": "secret\n",
		})
		if err := os.Chmod(filepath.Join(src, "scripts", "run.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(t.TempDir(), "skill")
		if err := stageSkill(src, dst); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(dst, "scripts", "run.sh")
		if got := stagedFiles(t, dst); len(got) != 1 || got[0] != want {
			t.Fatalf("staged %q, want only %s", got, want)
		}
		fi, err := os.Stat(want)
		if err != nil || fi.Mode().Perm() != 0o755 {
			t.Fatalf("staged run.sh mode = %v, %v; want 0755", fi.Mode(), err)
		}
	})
	t.Run("empty skill", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "skill")
		if err := stageSkill(t.TempDir(), dst); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Stat(dst); err != nil || !fi.IsDir() {
			t.Fatalf("an empty skill staged no directory: %v", err)
		}
	})
}
