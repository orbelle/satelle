package pack

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
)

func prospect(dataDir, skillsDir, prefix, ref, version string, apply func(dst string) error, validate func(string) error) error {
	if validate == nil {
		return fmt.Errorf("pack %s: no configuration validator was given; nothing was written", ref)
	}
	if _, err := presentAs(skillsDir, true); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(dataDir, prefix+"-*")
	if err != nil {
		return fmt.Errorf("create prospective skills directory: %w", err)
	}
	defer os.RemoveAll(tmp)
	dst := filepath.Join(tmp, filepath.Base(skillsDir))
	if err := os.Mkdir(dst, 0o755); err != nil {
		return fmt.Errorf("create prospective skills directory: %w", err)
	}
	if err := copySkillTree(skillsDir, dst); err != nil {
		return fmt.Errorf("copy %s: %w", skillsDir, err)
	}
	if err := apply(dst); err != nil {
		return err
	}
	if err := validate(dst); err != nil {
		return fmt.Errorf("pack %s: the configuration as it would stand with %s@%s written is invalid; nothing was written:\n%w", ref, ref, version, err)
	}
	return nil
}

func copySkillTree(src, dst string) error {
	if _, err := os.Lstat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	return copyDirContents(src, dst, []string{real})
}

func copyDirContents(src, dst string, descent []string) error {
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		info, err := os.Lstat(from)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if info, err = os.Stat(from); err != nil {
				continue
			}
		}
		switch {
		case info.IsDir():
			real, err := filepath.EvalSymlinks(from)
			if err != nil || slices.Contains(descent, real) {
				continue
			}
			if err := os.Mkdir(to, info.Mode().Perm()|0o700); err != nil {
				return err
			}
			if err := copyDirContents(from, to, append(slices.Clone(descent), real)); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := copyRegular(from, to, info.Mode().Perm()); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyRegular(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, wErr := io.Copy(out, in)
	if cErr := out.Close(); wErr == nil {
		wErr = cErr
	}
	return wErr
}

func pruneEmptyDirs(base, p string, pruned func(abs string, mode os.FileMode)) error {
	for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
		abs, err := under(base, dir)
		if err != nil {
			return err
		}
		info, err := os.Lstat(abs)
		if err != nil || !info.IsDir() {
			return nil
		}
		ents, err := os.ReadDir(abs)
		if err != nil || len(ents) > 0 {
			return nil
		}
		if err := os.Remove(abs); err != nil {
			return fmt.Errorf("remove %s: %w", abs, err)
		}
		if pruned != nil {
			pruned(abs, info.Mode().Perm())
		}
	}
	return nil
}
