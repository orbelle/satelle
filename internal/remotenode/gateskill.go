package remotenode

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/config"
)

const gateSkill = "skill"

type stagedSkillFile struct {
	rel  string
	info fs.FileInfo
}

// stageSkill copies a check skill's directory from the firing substrate into
// dst, the gate input a remote node runs the check from. It refuses a skill
// holding a symlink or special file before it copies anything, and leaves out
// every local-only path.
func stageSkill(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("skill directory %q: %w", src, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("skill directory %q is not a directory: a gate check ships no symlink or special file", src)
	}
	var dirs []string
	var files []stagedSkillFile
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == src {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if config.LocalOnlyPath(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.Type().IsDir():
			dirs = append(dirs, rel)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			files = append(files, stagedSkillFile{rel: rel, info: info})
		default:
			return fmt.Errorf("the skill's %s is not a regular file or directory: a gate check ships no symlink or special file", filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dst, d), 0o755); err != nil {
			return err
		}
	}
	for _, f := range files {
		if err := copySkillFile(filepath.Join(src, f.rel), filepath.Join(dst, f.rel), f); err != nil {
			return err
		}
	}
	return nil
}

func copySkillFile(from, to string, f stagedSkillFile) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	got, err := in.Stat()
	if err != nil {
		return err
	}
	if !got.Mode().IsRegular() || !os.SameFile(got, f.info) {
		return fmt.Errorf("the skill's %s changed while it was staged", filepath.ToSlash(f.rel))
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, f.info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
