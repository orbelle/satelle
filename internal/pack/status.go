package pack

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

type PackStatus struct {
	Ref         string   `json:"ref"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Edited      []string `json:"edited"`
	Missing     []string `json:"missing"`
	Newest      string   `json:"newest"`
	NewestError string   `json:"newest_error"`
}

func Status(dataDir string, names Names) ([]PackStatus, error) {
	recordPath := filepath.Join(dataDir, names.Record)
	shown := path.Join(filepath.Base(dataDir), names.Record)
	present, err := presentAs(recordPath, false)
	if err != nil {
		return nil, err
	}
	out := []PackStatus{}
	if !present {
		return out, nil
	}
	rec, err := LoadRecord(recordPath)
	if err != nil {
		return nil, err
	}
	skillsDir := filepath.Join(dataDir, names.Skills)
	if _, err := presentAs(skillsDir, true); err != nil {
		return nil, err
	}
	packs := slices.Clone(rec.Packs)
	slices.SortFunc(packs, func(a, b Installed) int { return strings.Compare(a.Ref, b.Ref) })
	for _, in := range packs {
		st, err := installedStatus(skillsDir, in)
		if err != nil {
			ref := in.Ref
			if ValidatePath(ref) != nil || ValidateToken(ref) != nil {
				ref = "(invalid ref)"
			}
			return nil, fmt.Errorf("%s: pack %s: %w", shown, ref, err)
		}
		out = append(out, st)
	}
	return out, nil
}

func installedStatus(skillsDir string, in Installed) (PackStatus, error) {
	if err := ValidatePath(in.Ref); err != nil {
		return PackStatus{}, fmt.Errorf("ref %w", errNotPlain)
	}
	if err := ValidateToken(in.Ref); err != nil {
		return PackStatus{}, fmt.Errorf("ref %w", err)
	}
	if err := ValidateToken(in.Version); err != nil {
		return PackStatus{}, fmt.Errorf("version %w", err)
	}
	if err := in.Validate(); err != nil {
		return PackStatus{}, err
	}
	for _, d := range in.Skills {
		p, err := under(skillsDir, d)
		if err != nil {
			return PackStatus{}, err
		}
		if _, err := presentAs(p, true); err != nil {
			return PackStatus{}, err
		}
	}
	st := PackStatus{Ref: in.Ref, Name: in.Name, Version: in.Version, Edited: []string{}, Missing: []string{}}
	files := slices.Clone(in.Files)
	slices.SortFunc(files, func(a, b InstalledFile) int { return strings.Compare(a.Path, b.Path) })
	for _, f := range files {
		state, err := recordedState(skillsDir, f.Path, f.SHA256)
		if err != nil {
			return PackStatus{}, err
		}
		switch state {
		case stateEdited:
			st.Edited = append(st.Edited, f.Path)
		case stateMissing:
			st.Missing = append(st.Missing, f.Path)
		}
	}
	return st, nil
}
