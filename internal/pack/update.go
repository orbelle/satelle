package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

type EditedFile struct {
	Path      string `json:"path"`
	Installed string `json:"installed,omitempty"`
	New       string `json:"new,omitempty"`
}

type UpdateResult struct {
	Ref              string       `json:"ref"`
	Name             string       `json:"name"`
	FromVersion      string       `json:"from_version"`
	Version          string       `json:"version"`
	Digest           string       `json:"digest"`
	Skills           []string     `json:"skills"`
	Replaced         []string     `json:"replaced"`
	Added            []string     `json:"added"`
	Removed          []string     `json:"removed"`
	Edited           []EditedFile `json:"edited"`
	AlreadyInstalled bool         `json:"already_installed,omitempty"`
}

func under(base, rel string) (string, error) {
	p := filepath.Join(base, filepath.FromSlash(rel))
	r, err := filepath.Rel(base, p)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", fmt.Errorf("%q leaves %s", rel, base)
	}
	return p, nil
}

func checkAncestors(base, rel string) error {
	segs := strings.Split(rel, "/")
	cur := base
	for _, seg := range segs[:len(segs)-1] {
		cur = filepath.Join(cur, seg)
		ok, err := presentAs(cur, true)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	return nil
}

func (v Verified) subset(keep []string) Verified {
	out := Verified{name: v.name, version: v.version, digest: v.digest}
	for _, f := range v.files {
		if slices.Contains(keep, f.path) {
			out.files = append(out.files, f)
		}
	}
	return out
}

func fileSHA256(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

type fileState int

const (
	stateUnchanged fileState = iota
	stateEdited
	stateMissing
)

func locate(skillsDir, rel string) (string, bool, error) {
	if err := checkAncestors(skillsDir, rel); err != nil {
		return "", false, err
	}
	cur, err := under(skillsDir, rel)
	if err != nil {
		return "", false, err
	}
	present, err := presentAs(cur, false)
	return cur, present, err
}

func recordedState(skillsDir, rel, want string) (fileState, error) {
	cur, present, err := locate(skillsDir, rel)
	if err != nil {
		return 0, err
	}
	if !present {
		return stateMissing, nil
	}
	got, err := fileSHA256(cur)
	if err != nil {
		return 0, err
	}
	if got != want {
		return stateEdited, nil
	}
	return stateUnchanged, nil
}

func Update(dataDir, ref string, next Verified, installed func() (Verified, error), names Names, check func(abs string) error) (UpdateResult, error) {
	if err := ValidatePath(ref); err != nil {
		return UpdateResult{}, fmt.Errorf("pack ref: %w", err)
	}
	if err := ValidateSegment(next.version); err != nil {
		return UpdateResult{}, fmt.Errorf("pack %s version: %w", ref, err)
	}
	_, dirs, err := Layout(next)
	if err != nil {
		return UpdateResult{}, err
	}
	for _, d := range dirs {
		if err := ValidateSegment(d); err != nil {
			return UpdateResult{}, err
		}
	}
	skillsDir := filepath.Join(dataDir, names.Skills)
	recordPath := filepath.Join(dataDir, names.Record)
	show := func(parts ...string) string {
		return path.Join(append([]string{filepath.Base(dataDir)}, parts...)...)
	}

	rec, err := LoadRecord(recordPath)
	if err != nil {
		return UpdateResult{}, err
	}
	in, ok := rec.Find(ref)
	if !ok {
		return UpdateResult{}, fmt.Errorf("pack %s is not installed; use satelle pack install", ref)
	}
	if err := in.Validate(); err != nil {
		return UpdateResult{}, fmt.Errorf("%s: pack %s: %w", show(names.Record), ref, err)
	}
	res := UpdateResult{
		Ref: ref, Name: next.name, FromVersion: in.Version, Version: next.version, Digest: next.digest, Skills: dirs,
		Replaced: []string{}, Added: []string{}, Removed: []string{}, Edited: []EditedFile{},
	}
	if in.Version == next.version {
		if in.Digest == next.digest {
			res.AlreadyInstalled = true
			return res, nil
		}
		return UpdateResult{}, fmt.Errorf("pack %s version %s is recorded with digest %s, but the marketplace serves digest %s", ref, in.Version, in.Digest, next.digest)
	}

	if _, err := presentAs(skillsDir, true); err != nil {
		return UpdateResult{}, err
	}
	skillDirs := slices.Clone(in.Skills)
	for _, d := range dirs {
		if !slices.Contains(skillDirs, d) {
			skillDirs = append(skillDirs, d)
		}
	}
	slices.Sort(skillDirs)
	for _, d := range skillDirs {
		p, err := under(skillsDir, d)
		if err != nil {
			return UpdateResult{}, err
		}
		if _, err := presentAs(p, true); err != nil {
			return UpdateResult{}, err
		}
	}

	recorded := map[string]string{}
	for _, f := range in.Files {
		recorded[f.Path] = f.SHA256
	}
	published := map[string]bool{}
	for _, f := range next.files {
		published[f.path] = true
	}
	var all []string
	for p := range recorded {
		all = append(all, p)
	}
	for p := range published {
		if _, ok := recorded[p]; !ok {
			all = append(all, p)
		}
	}
	slices.Sort(all)

	var place, backup []string
	var edited []string
	for _, p := range all {
		want, wasRecorded := recorded[p]
		if wasRecorded {
			state, err := recordedState(skillsDir, p, want)
			if err != nil {
				return UpdateResult{}, err
			}
			switch {
			case state != stateUnchanged:
				edited = append(edited, p)
			case published[p]:
				res.Replaced = append(res.Replaced, p)
				place = append(place, p)
				backup = append(backup, p)
			default:
				res.Removed = append(res.Removed, p)
				backup = append(backup, p)
			}
			continue
		}
		_, present, err := locate(skillsDir, p)
		if err != nil {
			return UpdateResult{}, err
		}
		if present {
			edited = append(edited, p)
		} else {
			res.Added = append(res.Added, p)
			place = append(place, p)
		}
	}

	var added []string
	for _, d := range dirs {
		if !slices.Contains(in.Skills, d) {
			added = append(added, d)
		}
	}
	if err := refuseForeign(skillsDir, dataDir, ref, rec, added, names); err != nil {
		return UpdateResult{}, err
	}

	mergeRoot := filepath.Join(dataDir, names.MergeDir)
	mergeDir, err := under(mergeRoot, ref)
	if err != nil {
		return UpdateResult{}, err
	}
	var copies []Verified
	var copyPaths []string
	if len(edited) > 0 {
		if _, err := presentAs(mergeRoot, true); err != nil {
			return UpdateResult{}, err
		}
		if err := checkAncestors(mergeRoot, ref); err != nil {
			return UpdateResult{}, err
		}
		if present, err := presentAs(mergeDir, true); err != nil {
			return UpdateResult{}, err
		} else if present {
			return UpdateResult{}, fmt.Errorf("merge copies from an earlier update remain at %s; merge them and remove the directory first", show(names.MergeDir, ref))
		}
		var old Verified
		var oldPaths, newPaths []string
		for _, p := range edited {
			if _, ok := recorded[p]; ok {
				oldPaths = append(oldPaths, p)
			}
			if published[p] {
				newPaths = append(newPaths, p)
			}
		}
		if len(oldPaths) > 0 {
			if old, err = installed(); err != nil {
				return UpdateResult{}, err
			}
			if err := ValidateSegment(old.version); err != nil {
				return UpdateResult{}, fmt.Errorf("pack %s installed version: %w", ref, err)
			}
			if old.version != in.Version || old.digest != in.Digest {
				return UpdateResult{}, fmt.Errorf("pack %s installed version %s no longer verifies against the recorded digest %.12s", ref, in.Version, in.Digest)
			}
			old = old.subset(oldPaths)
			copies = append(copies, old)
		}
		if len(newPaths) > 0 {
			copies = append(copies, next.subset(newPaths))
		}
		for _, p := range edited {
			e := EditedFile{Path: p}
			for _, c := range copies {
				if slices.ContainsFunc(c.files, func(f verifiedFile) bool { return f.path == p }) {
					rel := c.version + "/" + p
					abs, err := under(mergeDir, rel)
					if err != nil {
						return UpdateResult{}, err
					}
					copyPaths = append(copyPaths, abs)
					if c.version == in.Version {
						e.Installed = show(names.MergeDir, ref, rel)
					} else {
						e.New = show(names.MergeDir, ref, rel)
					}
				}
			}
			res.Edited = append(res.Edited, e)
		}
	}

	targets := []string{skillsDir}
	for _, d := range skillDirs {
		p, _ := under(skillsDir, d)
		targets = append(targets, p)
	}
	for _, p := range slices.Concat(place, res.Removed) {
		abs, _ := under(skillsDir, p)
		targets = append(targets, abs)
	}
	if len(edited) > 0 {
		targets = append(targets, mergeRoot, mergeDir)
		targets = append(targets, copyPaths...)
	}
	targets = append(targets, filepath.Join(dataDir, names.StagePrefix), recordPath)
	for _, p := range targets {
		if err := check(p); err != nil {
			return UpdateResult{}, err
		}
	}

	apply := func(dst string) error {
		for _, p := range backup {
			abs, err := under(dst, p)
			if err != nil {
				return err
			}
			if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := stageInto(dst, next.subset(place), ""); err != nil {
			return err
		}
		for _, p := range res.Removed {
			if err := pruneEmptyDirs(dst, p, nil); err != nil {
				return err
			}
		}
		return nil
	}
	if err := prospect(dataDir, skillsDir, names.StagePrefix, ref, next.version, apply, names.Validate); err != nil {
		return UpdateResult{}, err
	}

	files := make([]InstalledFile, len(next.files))
	for i, f := range next.files {
		files[i] = InstalledFile{Path: f.path, SHA256: f.sha256}
	}
	nextRec := rec.With(Installed{Ref: ref, Name: next.name, Version: next.version, Digest: next.digest, Skills: dirs, Files: files})
	c := updateCommit{place: place, backup: backup, removed: res.Removed, newFiles: next.subset(place), copies: copies, mergeDir: mergeDir}
	if err := commitUpdate(dataDir, skillsDir, recordPath, names.StagePrefix, c, nextRec); err != nil {
		return UpdateResult{}, err
	}
	return res, nil
}

type updateCommit struct {
	place, backup, removed []string
	newFiles               Verified
	copies                 []Verified
	mergeDir               string
}

func makeDirs(base, rel string) ([]string, error) {
	var created []string
	if rel == "" || rel == "." {
		return nil, nil
	}
	cur := base
	for _, seg := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, seg)
		ok, err := presentAs(cur, true)
		if err != nil {
			return created, err
		}
		if !ok {
			if err := os.Mkdir(cur, 0o755); err != nil {
				return created, fmt.Errorf("create %s: %w", cur, err)
			}
			created = append(created, cur)
		}
	}
	return created, nil
}

func commitUpdate(dataDir, skillsDir, recordPath, prefix string, c updateCommit, rec Record) error {
	if _, err := presentAs(recordPath, false); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dataDir, prefix+"-*")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	stageNew := filepath.Join(stage, "new")
	stageBackup := filepath.Join(stage, "backup")
	stageMerge := filepath.Join(stage, "merge")
	if err := stageInto(stageNew, c.newFiles, ""); err != nil {
		return err
	}
	for _, v := range c.copies {
		if err := stageInto(stageMerge, v, v.version); err != nil {
			return err
		}
	}

	var undos []func()
	undo := func(err error) error {
		for i := len(undos) - 1; i >= 0; i-- {
			undos[i]()
		}
		return err
	}
	mkdirs := func(base, rel string) error {
		created, err := makeDirs(base, rel)
		for _, d := range created {
			undos = append(undos, func() { _ = os.Remove(d) })
		}
		return err
	}
	move := func(src, dst string) error {
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("commit %s: %w", dst, err)
		}
		return nil
	}

	for _, p := range c.backup {
		src, err := under(skillsDir, p)
		if err != nil {
			return undo(err)
		}
		dst, err := under(stageBackup, p)
		if err != nil {
			return undo(err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return undo(err)
		}
		if err := move(src, dst); err != nil {
			return undo(err)
		}
		undos = append(undos, func() { _ = os.Rename(dst, src) })
	}
	if err := mkdirs(filepath.Dir(skillsDir), filepath.Base(skillsDir)); err != nil {
		return undo(err)
	}
	for _, p := range c.place {
		src, err := under(stageNew, p)
		if err != nil {
			return undo(err)
		}
		dst, err := under(skillsDir, p)
		if err != nil {
			return undo(err)
		}
		if err := mkdirs(skillsDir, path.Dir(p)); err != nil {
			return undo(err)
		}
		if err := move(src, dst); err != nil {
			return undo(err)
		}
		undos = append(undos, func() { _ = os.Remove(dst) })
	}
	for _, p := range c.removed {
		if err := pruneEmptyDirs(skillsDir, p, func(abs string, mode os.FileMode) {
			undos = append(undos, func() { _ = os.Mkdir(abs, mode) })
		}); err != nil {
			return undo(err)
		}
	}
	if len(c.copies) > 0 {
		rel, err := filepath.Rel(dataDir, filepath.Dir(c.mergeDir))
		if err != nil {
			return undo(err)
		}
		if err := mkdirs(dataDir, filepath.ToSlash(rel)); err != nil {
			return undo(err)
		}
		if err := move(stageMerge, c.mergeDir); err != nil {
			return undo(err)
		}
		undos = append(undos, func() { _ = os.RemoveAll(c.mergeDir) })
	}
	if err := writeRecord(stage, recordPath, rec); err != nil {
		return undo(err)
	}
	return nil
}
