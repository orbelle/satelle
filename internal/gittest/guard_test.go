package gittest

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

var (
	bareGit     = regexp.MustCompile(`exec\.Command(Context)?\((ctx, )?"git"`)
	usesGitfact = regexp.MustCompile(`"github\.com/bobmcallan/satelle/internal/gitfacts"`)
	callsMain   = regexp.MustCompile(`gittest\.Main\(`)
)

var exempt = map[string]string{
	"internal/gittest":    "owns the isolation",
	"internal/remotenode": "git env passed explicitly",
	"tests":               "sandbox env",
}

func TestEveryTestGitCallGoesThroughGittest(t *testing.T) {
	root := moduleRoot(t)
	type pkg struct{ tests, runsGit, hasMain bool }
	pkgs := map[string]*pkg{}
	var bad []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "node_modules", ".satelle":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		dir := filepath.ToSlash(filepath.Dir(rel))
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		p := pkgs[dir]
		if p == nil {
			p = &pkg{}
			pkgs[dir] = p
		}
		src := string(body)
		if strings.HasSuffix(path, "_test.go") {
			p.tests = true
			if callsMain.MatchString(src) {
				p.hasMain = true
			}
			if !strings.HasPrefix(rel, "internal/gittest/") {
				for i, line := range strings.Split(src, "\n") {
					if bareGit.MatchString(line) {
						bad = append(bad, rel+":"+itoa(i+1)+": bare git in a test; use gittest.Git or gittest.Command")
					}
				}
			}
			return nil
		}
		if usesGitfact.MatchString(src) || bareGit.MatchString(src) {
			p.runsGit = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for dir, p := range pkgs {
		if p.tests && p.runsGit && !p.hasMain && exempt[dir] == "" {
			bad = append(bad, dir+": runs git and has tests but no TestMain calling gittest.Main")
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Error(b)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
