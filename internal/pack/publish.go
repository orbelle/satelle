package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/wire/satellepb"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func Collect(skillsDir, entry string) (*satellepb.PackBundle, error) {
	if err := ValidateSegment(entry); err != nil {
		return nil, fmt.Errorf("entry skill: %w", err)
	}
	if ok, err := presentAs(skillsDir, true); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s: no skills directory", skillsDir)
	}
	entryDir := filepath.Join(skillsDir, entry)
	if ok, err := presentAs(entryDir, true); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("entry skill %s: no directory %s", entry, entryDir)
	}
	for _, name := range []string{workflow.SkillFile, workflow.PackFile} {
		if ok, err := presentAs(filepath.Join(entryDir, name), false); err != nil {
			return nil, err
		} else if !ok {
			return nil, fmt.Errorf("%s: no %s", entry, name)
		}
	}
	packFile := entry + "/" + workflow.PackFile
	data, err := os.ReadFile(filepath.Join(entryDir, workflow.PackFile))
	if err != nil {
		return nil, err
	}
	pk, err := workflow.ParsePack(packFile, data)
	if err != nil {
		return nil, err
	}
	lookup := func(name string) (workflow.Skill, bool) {
		if ValidateSegment(name) != nil {
			return workflow.Skill{}, false
		}
		dir := filepath.Join(skillsDir, name)
		if ok, err := presentAs(dir, true); ok && err == nil {
			if ok, err := presentAs(filepath.Join(dir, workflow.SkillFile), false); ok && err == nil {
				return workflow.Skill{Name: name, Dir: name}, true
			}
		}
		if ok, err := presentAs(dir+".md", false); ok && err == nil {
			return workflow.Skill{Name: name, Path: name + ".md"}, true
		}
		return workflow.Skill{}, false
	}
	if errs := pk.Validate(lookup); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	dirs := []string{entry}
	for _, m := range pk.Skills {
		if m == entry {
			continue
		}
		if err := ValidateSegment(m); err != nil {
			return nil, fmt.Errorf("skill %s listed in %s: %w", m, packFile, err)
		}
		if ok, err := presentAs(filepath.Join(skillsDir, m), true); err != nil {
			return nil, err
		} else if !ok {
			return nil, fmt.Errorf("skill %s listed in %s is missing", m, packFile)
		}
		dirs = append(dirs, m)
	}

	var refs []*satellepb.PackFileRef
	var files []*satellepb.PackFile
	var entries []DigestEntry
	for _, d := range dirs {
		root := filepath.Join(skillsDir, d)
		err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == root {
				return nil
			}
			if _, err := presentAs(p, de.IsDir()); err != nil {
				return err
			}
			if de.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(skillsDir, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if err := ValidatePath(rel); err != nil {
				return err
			}
			info, err := de.Info()
			if err != nil {
				return err
			}
			body, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			raw := sha256.Sum256(body)
			sum := hex.EncodeToString(raw[:])
			refs = append(refs, &satellepb.PackFileRef{Path: rel, Sha256: sum, Size: int64(len(body)), Executable: info.Mode()&0o111 != 0})
			files = append(files, &satellepb.PackFile{Path: rel, Sha256: sum, Body: body})
			entries = append(entries, DigestEntry{Path: rel, SHA256: sum})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	bundle := &satellepb.PackBundle{
		Version: &satellepb.PackVersion{
			Manifest: &satellepb.PackManifest{
				Name:    pk.Name,
				Version: pk.Version,
				Files:   refs,
				Needs: &satellepb.PackNeeds{
					Scripts: pk.Needs.Scripts,
					Agents:  pk.Needs.Agents,
					Network: pk.Needs.Network,
					Secrets: pk.Needs.Secrets,
				},
				Digest: ComputeDigest(entries),
			},
		},
		Files: files,
	}
	verified, err := Verify(bundle)
	if err != nil {
		return nil, err
	}
	if _, _, err := Layout(verified); err != nil {
		return nil, err
	}
	return bundle, nil
}
