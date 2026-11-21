package pack

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/workflow"
)

type Names struct {
	Skills      string
	Record      string
	StagePrefix string
	MergeDir    string
}

type Result struct {
	Ref              string   `json:"ref"`
	Name             string   `json:"name"`
	Version          string   `json:"version"`
	Digest           string   `json:"digest"`
	Skills           []string `json:"skills"`
	Files            int      `json:"files"`
	AlreadyInstalled bool     `json:"already_installed,omitempty"`
}

func Layout(v Verified) (string, []string, error) {
	tops := map[string]bool{}
	var entries []string
	for _, f := range v.files {
		top, rest, nested := strings.Cut(f.path, "/")
		if !nested {
			return "", nil, fmt.Errorf("pack file %q is at the bundle root; a pack's files sit in skill directories (<skill>/%s)", f.path, workflow.SkillFile)
		}
		tops[top] = true
		if rest == workflow.PackFile {
			entries = append(entries, top)
		}
	}
	switch len(entries) {
	case 0:
		return "", nil, fmt.Errorf("pack has no <entry>/%s", workflow.PackFile)
	case 1:
	default:
		return "", nil, fmt.Errorf("pack has more than one %s: %s/%s and %s/%s", workflow.PackFile, entries[0], workflow.PackFile, entries[1], workflow.PackFile)
	}
	entry := entries[0]
	body := map[string][]byte{}
	for _, f := range v.files {
		body[f.path] = f.body
	}
	if _, ok := body[entry+"/"+workflow.SkillFile]; !ok {
		return "", nil, fmt.Errorf("pack entry directory %s has no %s", entry, workflow.SkillFile)
	}
	packFile := entry + "/" + workflow.PackFile
	pk, err := workflow.ParsePack(packFile, body[packFile])
	if err != nil {
		return "", nil, err
	}
	lookup := func(name string) (workflow.Skill, bool) {
		if _, ok := body[name+"/"+workflow.SkillFile]; ok {
			return workflow.Skill{Name: name, Dir: name}, true
		}
		return workflow.Skill{}, false
	}
	if errs := pk.Validate(lookup); len(errs) > 0 {
		return "", nil, errors.Join(errs...)
	}
	dirs := []string{entry}
	for _, m := range pk.Skills {
		if m != entry {
			dirs = append(dirs, m)
		}
	}
	slices.Sort(dirs[1:])
	for top := range tops {
		if !slices.Contains(dirs, top) {
			return "", nil, fmt.Errorf("pack directory %q is neither the entry %q nor a member listed in %s", top, entry, packFile)
		}
	}
	return entry, dirs, nil
}

func Install(dataDir, ref string, v Verified, names Names, check func(abs string) error) (Result, error) {
	_, dirs, err := Layout(v)
	if err != nil {
		return Result{}, err
	}
	skillsDir := filepath.Join(dataDir, names.Skills)
	recordPath := filepath.Join(dataDir, names.Record)
	targets := []string{skillsDir}
	for _, d := range dirs {
		targets = append(targets, filepath.Join(skillsDir, d))
	}
	for _, f := range v.files {
		targets = append(targets, filepath.Join(skillsDir, filepath.FromSlash(f.path)))
	}
	targets = append(targets, filepath.Join(dataDir, names.StagePrefix), recordPath)
	for _, p := range targets {
		if err := check(p); err != nil {
			return Result{}, err
		}
	}

	rec, err := LoadRecord(recordPath)
	if err != nil {
		return Result{}, err
	}
	res := Result{Ref: ref, Name: v.name, Version: v.version, Digest: v.digest, Skills: dirs, Files: len(v.files)}
	if in, ok := rec.Find(ref); ok {
		if in.Version == v.version && in.Digest == v.digest {
			res.AlreadyInstalled = true
			return res, nil
		}
		return Result{}, fmt.Errorf("pack %s is already installed at version %s (digest %.12s); install does not change an installed pack; use satelle pack update to change it", ref, in.Version, in.Digest)
	}
	if err := refuseForeign(skillsDir, dataDir, ref, rec, dirs, names); err != nil {
		return Result{}, err
	}

	files := make([]InstalledFile, len(v.files))
	for i, f := range v.files {
		files[i] = InstalledFile{Path: f.path, SHA256: f.sha256}
	}
	next := rec.With(Installed{Ref: ref, Name: v.name, Version: v.version, Digest: v.digest, Skills: dirs, Files: files})
	if err := commit(dataDir, skillsDir, recordPath, names.StagePrefix, dirs, v, next); err != nil {
		return Result{}, err
	}
	return res, nil
}

func refuseForeign(skillsDir, dataDir, ref string, rec Record, dirs []string, names Names) error {
	show := func(name string) string { return path.Join(filepath.Base(dataDir), names.Skills, name) }
	for _, d := range dirs {
		for _, name := range []string{d, d + ".md"} {
			if _, err := os.Lstat(filepath.Join(skillsDir, name)); err == nil {
				return fmt.Errorf("pack %s: %s already exists and is not recorded as installed by %s in %s", ref, show(name), ref, path.Join(filepath.Base(dataDir), names.Record))
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("pack %s: %s: %w", ref, show(name), err)
			}
		}
		if owner, ok := rec.Owner(d); ok {
			return fmt.Errorf("pack %s: %s is recorded as installed by %s in %s", ref, show(d), owner, path.Join(filepath.Base(dataDir), names.Record))
		}
	}
	return nil
}

func presentAs(p string, wantDir bool) (bool, error) {
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return true, fmt.Errorf("%s: symlink is not allowed", p)
	case wantDir && !info.IsDir():
		return true, fmt.Errorf("%s: is not a directory", p)
	case !wantDir && !info.Mode().IsRegular():
		return true, fmt.Errorf("%s: is not a regular file", p)
	}
	return true, nil
}

func commit(dataDir, skillsDir, recordPath, prefix string, dirs []string, v Verified, rec Record) error {
	skillsExisted, err := presentAs(skillsDir, true)
	if err != nil {
		return err
	}
	if _, err := presentAs(recordPath, false); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dataDir, prefix+"-*")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := stageFiles(stage, v); err != nil {
		return err
	}
	if !skillsExisted {
		if err := os.Mkdir(skillsDir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", skillsDir, err)
		}
	}
	var moved []string
	undo := func(err error) error {
		for _, m := range moved {
			_ = os.RemoveAll(m)
		}
		if !skillsExisted {
			_ = os.Remove(skillsDir)
		}
		return err
	}
	for _, d := range dirs {
		dst := filepath.Join(skillsDir, d)
		if err := os.Rename(filepath.Join(stage, d), dst); err != nil {
			return undo(fmt.Errorf("commit %s: %w", dst, err))
		}
		moved = append(moved, dst)
	}
	if err := writeRecord(stage, recordPath, rec); err != nil {
		return undo(err)
	}
	return nil
}
