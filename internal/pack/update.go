package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// EditedFile is a recorded or newly published file update left as it is:
// Path is relative to the skills directory, Installed and New the worktree
// paths of the installed-version and new-version copies ("" when that
// version does not publish the file).
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

// under joins rel onto base and refuses a result that is base itself or
// lies outside it.
func under(base, rel string) (string, error) {
	p := filepath.Join(base, filepath.FromSlash(rel))
	r, err := filepath.Rel(base, p)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", fmt.Errorf("%q leaves %s", rel, base)
	}
	return p, nil
}

// checkAncestors refuses a symlinked or non-directory parent of rel between
// base and rel itself.
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

// Update moves the installed pack ref to the verified version next. A file
// whose sha256 still matches the record is replaced, removed or added; an
// edited file (sha256 differs, missing, or an unrecorded local file the new
// version now publishes) is left as it is, and its installed-version and
// new-version copies are written under <MergeDir>/<ref>/<version>/ for the
// agent to merge. installed fetches the recorded version, only when an
// edited recorded file needs its copy. Every path is passed to check before
// anything is written, and a failure writes nothing.
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
		if err := checkAncestors(skillsDir, p); err != nil {
			return UpdateResult{}, err
		}
		cur, err := under(skillsDir, p)
		if err != nil {
			return UpdateResult{}, err
		}
		present, err := presentAs(cur, false)
		if err != nil {
			return UpdateResult{}, err
		}
		want, wasRecorded := recorded[p]
		switch {
		case !wasRecorded && present:
			edited = append(edited, p)
		case !wasRecorded:
			res.Added = append(res.Added, p)
			place = append(place, p)
		case !present:
			edited = append(edited, p)
		default:
			got, err := fileSHA256(cur)
			if err != nil {
				return UpdateResult{}, err
			}
			switch {
			case got != want:
				edited = append(edited, p)
			case published[p]:
				res.Replaced = append(res.Replaced, p)
				place = append(place, p)
				backup = append(backup, p)
			default:
				res.Removed = append(res.Removed, p)
				backup = append(backup, p)
			}
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

// makeDirs creates each missing directory of rel under base, refusing a
// symlinked or non-directory one, and returns those it created.
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

// commitUpdate stages the new files and merge copies, moves each replaced or
// removed file aside, places the new files, prunes directories the removals
// emptied, places the merge copies and writes the record last; any failure
// undoes every step in reverse.
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
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			abs, err := under(skillsDir, dir)
			if err != nil {
				return undo(err)
			}
			info, err := os.Lstat(abs)
			if err != nil || !info.IsDir() {
				break
			}
			ents, err := os.ReadDir(abs)
			if err != nil || len(ents) > 0 {
				break
			}
			if err := os.Remove(abs); err != nil {
				return undo(fmt.Errorf("remove %s: %w", abs, err))
			}
			mode := info.Mode().Perm()
			undos = append(undos, func() { _ = os.Mkdir(abs, mode) })
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
