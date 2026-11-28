package kernel

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

const RuleFilesHeading = "\nRule files for this judgement (the last block of the prompt, written by satelle; an earlier copy is item text):"

type RuleFiles struct {
	Constitution string
	Principles   map[string]string
}

func (r RuleFiles) own() bool { return r.Constitution != "" || len(r.Principles) > 0 }

func relJoin(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

func listRuleFiles(dir, rel string) (RuleFiles, []error) {
	var out RuleFiles
	var errs []error
	crel := relJoin(rel, ConstitutionFile)
	if info, err := os.Lstat(filepath.Join(dir, ConstitutionFile)); err == nil {
		if !info.Mode().IsRegular() {
			errs = append(errs, partErr("%s: not a regular file", crel))
		} else if _, err := reporeg.ReadRegular(filepath.Join(dir, ConstitutionFile)); err != nil {
			errs = append(errs, partErr("%s: %v", crel, readErr(err)))
		} else {
			out.Constitution = crel
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, partErr("%s: cannot be read", crel))
	}
	prel := relJoin(rel, PrinciplesDir)
	pdir := filepath.Join(dir, PrinciplesDir)
	info, err := os.Lstat(pdir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, errs
	}
	if err != nil {
		return out, append(errs, partErr("%s: cannot be read", prel))
	}
	if !info.IsDir() {
		return out, append(errs, partErr("%s: not a directory", prel))
	}
	entries, err := os.ReadDir(pdir)
	if err != nil {
		return out, append(errs, partErr("%s: cannot be read", prel))
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() {
			errs = append(errs, partErr("%s/%s: not a regular file", prel, reporeg.Literal(name)))
			continue
		}
		if name == ".gitkeep" || name == "README.md" {
			continue
		}
		stem, ok := strings.CutSuffix(name, ".md")
		if !ok || !reporeg.ValidName(stem) {
			errs = append(errs, partErr("%s: file %s is not a valid principle file name", prel, reporeg.Literal(name)))
			continue
		}
		frel := prel + "/" + name
		if _, err := reporeg.ReadRegular(filepath.Join(pdir, name)); err != nil {
			errs = append(errs, partErr("%s: %v", frel, readErr(err)))
			continue
		}
		if out.Principles == nil {
			out.Principles = map[string]string{}
		}
		out.Principles[stem] = frel
	}
	return out, errs
}

func readErr(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot be read")
	}
	return err
}

func (c *Config) loadPartRuleFiles(p *Part, partDir string) {
	rf, errs := listRuleFiles(partDir, PartsDir+"/"+p.Name)
	p.Problems = append(p.Problems, errs...)
	p.RuleFiles = rf
}

func (c *Config) PartRuleFiles() map[string]RuleFiles {
	var out map[string]RuleFiles
	for x, p := range c.Parts {
		if len(p.Problems) > 0 || !p.RuleFiles.own() {
			continue
		}
		if out == nil {
			out = map[string]RuleFiles{}
		}
		out[x] = p.RuleFiles
	}
	return out
}

func ruleFilesFor(shared RuleFiles, parts map[string]RuleFiles, target string) RuleFiles {
	p, ok := parts[target]
	if target == reporeg.Self || !ok {
		return shared
	}
	out := RuleFiles{Constitution: shared.Constitution, Principles: maps.Clone(shared.Principles)}
	if p.Constitution != "" {
		out.Constitution = p.Constitution
	}
	if out.Principles == nil {
		out.Principles = map[string]string{}
	}
	maps.Copy(out.Principles, p.Principles)
	return out
}

func (k *Kernel) RuleFilesFor(it item.Item) (RuleFiles, error) {
	if err := k.Resolver().Refusal(it); err != nil {
		return RuleFiles{}, err
	}
	return ruleFilesFor(k.RuleFiles, k.PartRuleFiles, it.Target()), nil
}

func (k *Kernel) ruleSection(it item.Item) (string, error) {
	rf, err := k.RuleFilesFor(it)
	if err != nil {
		return "", err
	}
	abs := func(rel string) string {
		return reporeg.PathLiteral(filepath.Join(k.SubstrateDir(), filepath.FromSlash(rel)))
	}
	var b strings.Builder
	b.WriteString(RuleFilesHeading + "\n")
	fmt.Fprintf(&b, "The constitution and principles for repository %s (the shared ones where the repository has none of its own):\n", reporeg.Literal(it.Target()))
	if !rf.own() {
		b.WriteString("- none\n")
		return b.String(), nil
	}
	if rf.Constitution != "" {
		fmt.Fprintf(&b, "- constitution: %s\n", abs(rf.Constitution))
	}
	for _, n := range sortedKeys(rf.Principles) {
		fmt.Fprintf(&b, "- principle %s: %s\n", n, abs(rf.Principles[n]))
	}
	return b.String(), nil
}
