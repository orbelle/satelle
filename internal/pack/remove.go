package pack

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

type RemoveResult struct {
	Ref     string   `json:"ref"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Digest  string   `json:"digest"`
	Skills  []string `json:"skills"`
	Removed []string `json:"removed"`
	Edited  []string `json:"edited"`
	Missing []string `json:"missing"`
	Kept    []string `json:"kept"`
	Forced  bool     `json:"forced"`
}

func Remove(dataDir, ref string, force bool, names Names, check func(abs string) error) (RemoveResult, error) {
	if err := ValidatePath(ref); err != nil {
		return RemoveResult{}, fmt.Errorf("pack ref: %w", err)
	}
	skillsDir := filepath.Join(dataDir, names.Skills)
	recordPath := filepath.Join(dataDir, names.Record)
	show := func(parts ...string) string {
		return path.Join(append([]string{filepath.Base(dataDir)}, parts...)...)
	}

	rec, err := LoadRecord(recordPath)
	if err != nil {
		return RemoveResult{}, err
	}
	in, ok := rec.Find(ref)
	if !ok {
		return RemoveResult{}, fmt.Errorf("pack %s is not installed", ref)
	}
	if err := in.Validate(); err != nil {
		return RemoveResult{}, fmt.Errorf("%s: pack %s: %w", show(names.Record), ref, err)
	}
	res := RemoveResult{
		Ref: ref, Name: in.Name, Version: in.Version, Digest: in.Digest, Skills: slices.Clone(in.Skills),
		Removed: []string{}, Edited: []string{}, Missing: []string{}, Kept: []string{}, Forced: force,
	}

	if _, err := presentAs(skillsDir, true); err != nil {
		return RemoveResult{}, err
	}
	skillDirs := slices.Sorted(slices.Values(in.Skills))
	for _, d := range skillDirs {
		p, err := under(skillsDir, d)
		if err != nil {
			return RemoveResult{}, err
		}
		if _, err := presentAs(p, true); err != nil {
			return RemoveResult{}, err
		}
	}

	files := slices.Clone(in.Files)
	slices.SortFunc(files, func(a, b InstalledFile) int { return strings.Compare(a.Path, b.Path) })
	recorded := map[string]bool{}
	for _, f := range files {
		recorded[f.Path] = true
		if err := checkAncestors(skillsDir, f.Path); err != nil {
			return RemoveResult{}, err
		}
		cur, err := under(skillsDir, f.Path)
		if err != nil {
			return RemoveResult{}, err
		}
		present, err := presentAs(cur, false)
		if err != nil {
			return RemoveResult{}, err
		}
		if !present {
			res.Missing = append(res.Missing, f.Path)
			continue
		}
		got, err := fileSHA256(cur)
		if err != nil {
			return RemoveResult{}, err
		}
		if got != f.SHA256 {
			res.Edited = append(res.Edited, f.Path)
			continue
		}
		res.Removed = append(res.Removed, f.Path)
	}
	if len(res.Edited) > 0 && !force {
		shown := make([]string, len(res.Edited))
		for i, p := range res.Edited {
			shown[i] = show(names.Skills, p)
		}
		return RemoveResult{}, fmt.Errorf("pack %s has locally edited file(s): %s; nothing removed; satelle pack remove %s --force removes them too",
			ref, strings.Join(shown, ", "), ref)
	}
	res.Removed = slices.Sorted(slices.Values(slices.Concat(res.Removed, res.Edited)))

	for _, d := range skillDirs {
		root, _ := under(skillsDir, d)
		if ok, _ := presentAs(root, true); !ok {
			continue
		}
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(skillsDir, p)
			if err != nil {
				return err
			}
			if rel = filepath.ToSlash(rel); !recorded[rel] {
				res.Kept = append(res.Kept, rel)
			}
			return nil
		})
		if err != nil {
			return RemoveResult{}, err
		}
	}

	targets := []string{skillsDir}
	for _, d := range skillDirs {
		p, _ := under(skillsDir, d)
		targets = append(targets, p)
	}
	for _, p := range res.Removed {
		abs, _ := under(skillsDir, p)
		targets = append(targets, abs)
	}
	targets = append(targets, filepath.Join(dataDir, names.StagePrefix), recordPath)
	for _, p := range targets {
		if err := check(p); err != nil {
			return RemoveResult{}, err
		}
	}

	c := updateCommit{backup: res.Removed, removed: slices.Concat(res.Removed, res.Missing)}
	if err := commitUpdate(dataDir, skillsDir, recordPath, names.StagePrefix, c, rec.Without(ref)); err != nil {
		return RemoveResult{}, err
	}
	return res, nil
}
