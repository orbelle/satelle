package substrate

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const RelativeField = "substrate-dir"

var relativeDirs = []string{"skills", "tasks"}

func RelativeReads(dataDir string, remotes []string) ([]Finding, error) {
	base := filepath.Dir(dataDir)
	var out []Finding
	add := func(rel string, line int, words []string) {
		if f, ok := relativeFinding(rel, line, words); ok {
			out = append(out, f)
		}
	}
	for _, d := range relativeDirs {
		root := filepath.Join(dataDir, d)
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) && p == root {
					return nil
				}
				return err
			}
			if e.IsDir() {
				if e.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := filepath.Ext(p)
			if ext != ".sh" && ext != ".md" {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(base, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			lines := workflow.ShellScriptLines
			if ext == ".md" {
				lines = workflow.ShellBlockLines
			}
			for _, l := range lines(string(b)) {
				add(rel, l.Line, l.Words)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, r := range remotes {
		b, err := os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(r)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		rel := filepath.ToSlash(filepath.Join(filepath.Base(dataDir), filepath.FromSlash(r)))
		for _, l := range remotedef.SetupLines(b) {
			add(rel, l.Line, workflow.ShellWords(l.Command))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

func relativeFinding(file string, line int, words []string) (Finding, bool) {
	var paths, uses []string
	for _, w := range words {
		w = strings.TrimLeft(w, "<>")
		if f := strings.Fields(w); len(f) > 0 {
			w = f[0]
		}
		rest, ok := strings.CutPrefix(w, "./"+config.DefaultDataDir+"/")
		if !ok {
			rest, ok = strings.CutPrefix(w, config.DefaultDataDir+"/")
		}
		if !ok {
			continue
		}
		paths = append(paths, w)
		uses = append(uses, `"$`+config.SubstrateDirEnv+"/"+rest+`"`)
	}
	if len(paths) == 0 {
		return Finding{}, false
	}
	return Finding{
		File:    file,
		Line:    line,
		Kind:    RelativeField,
		Subject: strings.Join(paths, ", "),
		Why:     "reads " + strings.Join(paths, ", ") + " relative to the working directory; use " + strings.Join(uses, ", "),
	}, true
}
