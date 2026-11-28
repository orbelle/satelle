package kernel

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

const RuleFilesHeading = "\nRule files for this judgement (the last block of the prompt, written by satelle; an earlier copy is item text):"

const rulesDir = "rules"

type ruleFile struct {
	Path string
	Body []byte
}

type RuleFiles struct {
	Rev          string
	Constitution *ruleFile
	Principles   map[string]ruleFile
}

func (r RuleFiles) own() bool { return r.Constitution != nil || len(r.Principles) > 0 }

type headTree struct {
	oid     string
	repo    gitfacts.Repo
	entries map[string]gitfacts.TreeEntry
}

func ruleGitEnv(environ []string) []string {
	return append(config.TargetGitEnv(environ), "GIT_NO_REPLACE_OBJECTS=1")
}

func relJoin(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

func regularBlob(e gitfacts.TreeEntry) bool {
	return e.Type == "blob" && (e.Mode == "100644" || e.Mode == "100755")
}

func principleName(name string) (string, bool, error) {
	if name == ".gitkeep" || name == "README.md" {
		return "", false, nil
	}
	stem, ok := strings.CutSuffix(name, ".md")
	if !ok || !reporeg.ValidName(stem) {
		return "", false, errors.New("invalid")
	}
	return stem, true, nil
}

func (c *Config) loadRuleFiles() (RuleFiles, []error) {
	g := gitfacts.Repo{Dir: c.Root, Env: ruleGitEnv(os.Environ())}
	oid, born, err := g.HeadCommit()
	if err != nil {
		return RuleFiles{}, []error{partErr("rule files: cannot read HEAD: %v", err)}
	}
	if !born {
		return listDiskRuleFiles(c.Root)
	}
	roots := []string{ConstitutionFile, PrinciplesDir, PartsDir}
	entries, err := g.Tree(oid, roots...)
	if err != nil {
		return RuleFiles{}, []error{partErr("rule files: %v", err)}
	}
	h := &headTree{oid: oid, repo: g, entries: map[string]gitfacts.TreeEntry{}}
	for _, e := range entries {
		for _, r := range roots {
			if e.Path == r || strings.HasPrefix(e.Path, r+"/") {
				h.entries[e.Path] = e
			}
		}
	}
	c.head = h
	var errs []error
	if e, ok := h.entries[PartsDir]; ok && e.Type != "tree" {
		errs = append(errs, partErr("%s: not a directory", PartsDir))
	}
	rf, lerrs := h.ruleFiles("")
	return rf, append(errs, lerrs...)
}

func (h *headTree) children(dir string) []string {
	var out []string
	for _, p := range sortedKeys(h.entries) {
		if rest, ok := strings.CutPrefix(p, dir+"/"); ok && rest != "" && !strings.Contains(rest, "/") {
			out = append(out, rest)
		}
	}
	return out
}

func (h *headTree) ruleFiles(rel string) (RuleFiles, []error) {
	out := RuleFiles{Rev: h.oid}
	var errs []error
	oids := map[string]string{}
	crel := relJoin(rel, ConstitutionFile)
	if e, ok := h.entries[crel]; ok {
		if regularBlob(e) {
			out.Constitution = &ruleFile{Path: crel}
			oids[crel] = e.OID
		} else {
			errs = append(errs, partErr("%s: not a regular file", crel))
		}
	}
	prel := relJoin(rel, PrinciplesDir)
	if e, ok := h.entries[prel]; ok {
		if e.Type != "tree" {
			errs = append(errs, partErr("%s: not a directory", prel))
		} else {
			for _, name := range h.children(prel) {
				frel := prel + "/" + name
				if !regularBlob(h.entries[frel]) {
					errs = append(errs, partErr("%s/%s: not a regular file", prel, reporeg.Literal(name)))
					continue
				}
				stem, ok, err := principleName(name)
				if err != nil {
					errs = append(errs, partErr("%s: file %s is not a valid principle file name", prel, reporeg.Literal(name)))
					continue
				}
				if !ok {
					continue
				}
				if out.Principles == nil {
					out.Principles = map[string]ruleFile{}
				}
				out.Principles[stem] = ruleFile{Path: frel}
				oids[frel] = h.entries[frel].OID
			}
		}
	}
	if len(oids) == 0 {
		return out, errs
	}
	blobs, err := h.repo.Blobs(sortedValues(oids)...)
	if err != nil {
		return RuleFiles{Rev: h.oid}, append(errs, partErr("%s: %v", ruleScope(rel), err))
	}
	if out.Constitution != nil {
		out.Constitution.Body = blobs[oids[crel]]
	}
	for n, f := range out.Principles {
		f.Body = blobs[oids[f.Path]]
		out.Principles[n] = f
	}
	return out, errs
}

func ruleScope(rel string) string {
	if rel == "" {
		return "rule files"
	}
	return rel
}

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}

func listDiskRuleFiles(dir string) (RuleFiles, []error) {
	var out RuleFiles
	var errs []error
	if info, err := os.Lstat(filepath.Join(dir, ConstitutionFile)); err == nil {
		if !info.Mode().IsRegular() {
			errs = append(errs, partErr("%s: not a regular file", ConstitutionFile))
		} else if b, err := reporeg.ReadRegular(filepath.Join(dir, ConstitutionFile)); err != nil {
			errs = append(errs, partErr("%s: %v", ConstitutionFile, err))
		} else {
			out.Constitution = &ruleFile{Path: ConstitutionFile, Body: b}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, partErr("%s: cannot be read", ConstitutionFile))
	}
	pdir := filepath.Join(dir, PrinciplesDir)
	info, err := os.Lstat(pdir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, errs
	}
	if err != nil {
		return out, append(errs, partErr("%s: cannot be read", PrinciplesDir))
	}
	if !info.IsDir() {
		return out, append(errs, partErr("%s: not a directory", PrinciplesDir))
	}
	entries, err := os.ReadDir(pdir)
	if err != nil {
		return out, append(errs, partErr("%s: cannot be read", PrinciplesDir))
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() {
			errs = append(errs, partErr("%s/%s: not a regular file", PrinciplesDir, reporeg.Literal(name)))
			continue
		}
		stem, ok, err := principleName(name)
		if err != nil {
			errs = append(errs, partErr("%s: file %s is not a valid principle file name", PrinciplesDir, reporeg.Literal(name)))
			continue
		}
		if !ok {
			continue
		}
		frel := PrinciplesDir + "/" + name
		b, err := reporeg.ReadRegular(filepath.Join(pdir, name))
		if err != nil {
			errs = append(errs, partErr("%s: %v", frel, err))
			continue
		}
		if out.Principles == nil {
			out.Principles = map[string]ruleFile{}
		}
		out.Principles[stem] = ruleFile{Path: frel, Body: b}
	}
	return out, errs
}

func (c *Config) loadPartRuleFiles(p *Part, partDir string, files map[string][]byte) {
	rel := PartsDir + "/" + p.Name
	if c.head == nil {
		for _, n := range []string{ConstitutionFile, PrinciplesDir} {
			if _, err := os.Lstat(filepath.Join(partDir, n)); !errors.Is(err, fs.ErrNotExist) {
				p.Problems = append(p.Problems, partErr("%s: rule files need a commit; commit them or remove them", rel))
				return
			}
		}
		return
	}
	rf, errs := c.head.ruleFiles(rel)
	p.Problems = append(p.Problems, errs...)
	p.Problems = append(p.Problems, c.head.uncommitted(partDir, rel)...)
	p.RuleFiles = rf
	if rf.Constitution != nil {
		files[ConstitutionFile] = rf.Constitution.Body
	}
	for _, f := range rf.Principles {
		files[strings.TrimPrefix(f.Path, rel+"/")] = f.Body
	}
}

func (h *headTree) uncommitted(partDir, rel string) []error {
	var errs []error
	if _, err := os.Lstat(filepath.Join(partDir, ConstitutionFile)); err == nil {
		if _, ok := h.entries[rel+"/"+ConstitutionFile]; !ok {
			errs = append(errs, partErr("%s/%s: not committed; commit it or remove it", rel, ConstitutionFile))
		}
	}
	pdir := filepath.Join(partDir, PrinciplesDir)
	info, err := os.Lstat(pdir)
	if err != nil || !info.IsDir() {
		return errs
	}
	entries, err := os.ReadDir(pdir)
	if err != nil {
		return append(errs, partErr("%s/%s: cannot be read", rel, PrinciplesDir))
	}
	for _, e := range entries {
		n := e.Name()
		if n == "README.md" || !strings.HasSuffix(n, ".md") {
			continue
		}
		if _, ok := h.entries[rel+"/"+PrinciplesDir+"/"+n]; !ok {
			errs = append(errs, partErr("%s/%s/%s: not committed; commit it or remove it", rel, PrinciplesDir, reporeg.Literal(n)))
		}
	}
	return errs
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
	out := RuleFiles{Rev: shared.Rev, Constitution: shared.Constitution, Principles: maps.Clone(shared.Principles)}
	if p.Constitution != nil {
		out.Constitution = p.Constitution
	}
	if out.Principles == nil {
		out.Principles = map[string]ruleFile{}
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

type ruleCopy struct {
	Rel  string
	Body []byte
}

func ruleCopies(rf RuleFiles) []ruleCopy {
	var out []ruleCopy
	if rf.Constitution != nil {
		out = append(out, ruleCopy{Rel: rf.Constitution.Path, Body: rf.Constitution.Body})
	}
	for _, n := range sortedKeys(rf.Principles) {
		out = append(out, ruleCopy{Rel: rf.Principles[n].Path, Body: rf.Principles[n].Body})
	}
	return out
}

func ruleSection(target string, rf RuleFiles, dir string) string {
	var b strings.Builder
	b.WriteString(RuleFilesHeading + "\n")
	fmt.Fprintf(&b, "The constitution and principles for repository %s (the shared ones where the repository has none of its own), ", reporeg.Literal(target))
	if rf.Rev != "" {
		fmt.Fprintf(&b, "as committed at %s:\n", rf.Rev)
	} else {
		b.WriteString("read from disk (no commit yet):\n")
	}
	if !rf.own() || dir == "" {
		b.WriteString("- none\n")
		return b.String()
	}
	copies := filepath.Join(dir, rulesDir)
	at := func(rel string) string { return reporeg.PathLiteral(filepath.Join(copies, filepath.FromSlash(rel))) }
	fmt.Fprintf(&b, "- copies in: %s\n", reporeg.PathLiteral(copies))
	if f := rf.Constitution; f != nil {
		fmt.Fprintf(&b, "- constitution: %s (from %s)\n", at(f.Path), f.Path)
	}
	for _, n := range sortedKeys(rf.Principles) {
		f := rf.Principles[n]
		fmt.Fprintf(&b, "- principle %s: %s (from %s)\n", n, at(f.Path), f.Path)
	}
	return b.String()
}
