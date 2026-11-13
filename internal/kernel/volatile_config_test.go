package kernel

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/substrate"
)

// initVolatile is a fresh satelle init: its .satelle dir and loaded config.
func initVolatile(t *testing.T) (string, *Config) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, c
}

// The init template ships the default volatile patterns in volatile.toml;
// they load, compile and validate, and a fresh init has nothing they match
// (sto_3de33a72 AC2).
func TestInitWritesDefaultVolatilePatterns(t *testing.T) {
	root, c := initVolatile(t)
	if _, err := os.Stat(filepath.Join(root, VolatileFile)); err != nil {
		t.Fatalf("init wrote no %s: %v", VolatileFile, err)
	}
	var names []string
	for _, p := range c.Volatile.Patterns {
		names = append(names, p.Name)
	}
	if want := []string{"version", "present-tense", "model-id", "current-item"}; !slices.Equal(names, want) {
		t.Fatalf("default patterns %v, want %v", names, want)
	}
	if _, err := substrate.CompileVolatile(c.Volatile); err != nil {
		t.Fatal(err)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("fresh init does not validate: %v", errs)
	}
	got, err := c.VolatileFindings()
	if err != nil || len(got) != 0 {
		t.Fatalf("fresh init: findings %v, err %v", got, err)
	}

	// A configured repo without the file is told init would add it.
	if err := os.Remove(filepath.Join(root, VolatileFile)); err != nil {
		t.Fatal(err)
	}
	plan, err := InitPlan(root)
	if err != nil || !slices.Contains(plan, VolatileFile) {
		t.Fatalf("InitPlan %v (%v) lacks %s", plan, err, VolatileFile)
	}
}

// Each default pattern matches the shape it is for and not its near misses.
// The test holds sample lines, never pattern text: the patterns are read from
// the template init writes.
func TestDefaultVolatilePatternsMatchTheirShapes(t *testing.T) {
	_, c := initVolatile(t)
	samples := map[string]struct{ match, miss []string }{
		"version": {
			match: []string{"uses v1.0.57 of the binary", "needs 2.14.0 or newer", "released v1.0.57."},
			miss:  []string{"listens on 10.0.0.1", "see section 3.2"},
		},
		"present-tense": {
			match: []string{"the latest release", "This is currently broken.", "At the moment it fails", "use the `current` build", "the limit now is 5"},
			miss:  []string{".../releases/latest/download/install.sh", "the current_state field", "pass --latest"},
		},
		"model-id": {
			match: []string{"runs on claude-opus-4-1", "gpt-4o is the reviewer", "gemini-2.5-pro", "grok-4"},
			miss:  []string{"claude-code", "reviewer-grok", "the gpt reviewer"},
		},
		"current-item": {
			match: []string{"the current epic is epi_1a2b3c4d", "currently in review: sto_0f76dd4e", "the active story sto_3846bea3"},
			miss:  []string{"see sto_0f76dd4e for the history", "the current epic has no id"},
		},
	}
	if len(c.Volatile.Patterns) != len(samples) {
		t.Fatalf("%d default patterns, %d with samples", len(c.Volatile.Patterns), len(samples))
	}
	for _, p := range c.Volatile.Patterns {
		s, ok := samples[p.Name]
		if !ok {
			t.Fatalf("default pattern %q has no samples", p.Name)
		}
		set, err := substrate.CompileVolatile(substrate.VolatileRules{Patterns: []substrate.VolatilePattern{p}})
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range s.match {
			if got := substrate.VolatileText("f.md", line, set); len(got) != 1 || got[0].Subject != p.Name {
				t.Errorf("%s: %q: want one finding, got %v", p.Name, line, got)
			}
		}
		for _, line := range s.miss {
			if got := substrate.VolatileText("f.md", line, set); len(got) != 0 {
				t.Errorf("%s: %q: want none, got %v", p.Name, line, got)
			}
		}
	}
}

// Each malformed volatile.toml is a validate problem naming the file.
func TestVolatileConfigProblems(t *testing.T) {
	root, _ := initVolatile(t)
	cases := map[string]string{
		"unknown key":  "bogus = 1\n",
		"nameless":     "[[pattern]]\nmatch = 'x'\n",
		"duplicate":    "[[pattern]]\nname = 'a'\nmatch = 'x'\n[[pattern]]\nname = 'a'\nmatch = 'y'\n",
		"bad match":    "[[pattern]]\nname = 'a'\nmatch = '('\n",
		"bad exclude":  "exclude = ['[']\n[[pattern]]\nname = 'a'\nmatch = 'x'\n",
		"dotdot paths": "paths = ['../outside/*.md']\n[[pattern]]\nname = 'a'\nmatch = 'x'\n",
		"absolute":     "paths = ['/etc/*.md']\n[[pattern]]\nname = 'a'\nmatch = 'x'\n",
		"inner dotdot": "exclude = ['a/../b/**']\n[[pattern]]\nname = 'a'\nmatch = 'x'\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			write(t, filepath.Join(root, VolatileFile), body)
			errs := Validate(root)
			if len(errs) == 0 {
				t.Fatal("validated")
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e.Error(), VolatileFile) {
					found = true
				}
			}
			if !found {
				t.Fatalf("no problem names %s: %v", VolatileFile, errs)
			}
		})
	}
}

// With no volatile.toml, or one with no pattern, nothing is reported; a repo
// file with patterns of its own replaces the defaults.
func TestVolatilePatternsAreOnlyConfiguration(t *testing.T) {
	root, _ := initVolatile(t)
	write(t, filepath.Join(root, SkillsDir, "stale.md"),
		"---\nname: stale\ntype: skill\ndescription: Stale.\n---\n\nUses v1.0.57, the latest build, on claude-opus-4-1; currently on sto_0f76dd4e.\nTODO: fix.\n")
	findings := func() []substrate.Finding {
		t.Helper()
		c, err := LoadConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.VolatileFindings()
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := findings(); len(got) != 4 {
		t.Fatalf("defaults: want the four patterns on line 7, got %v", got)
	}
	if err := os.Remove(filepath.Join(root, VolatileFile)); err != nil {
		t.Fatal(err)
	}
	if got := findings(); len(got) != 0 {
		t.Fatalf("no volatile.toml: got %v", got)
	}
	write(t, filepath.Join(root, VolatileFile), "exempt = 'volatile-ok'\nexclude = []\n")
	if got := findings(); len(got) != 0 {
		t.Fatalf("no [[pattern]]: got %v", got)
	}
	write(t, filepath.Join(root, VolatileFile), "[[pattern]]\nname = 'todo'\nmatch = 'TODO'\n")
	got := findings()
	if len(got) != 1 || got[0].Subject != "todo" || got[0].Line != 8 {
		t.Fatalf("repo pattern: want one todo finding, got %v", got)
	}
}

// No match, the exempt marker or an exclude entry of the defaults is written
// in the binary's Go source: the binary runs patterns and holds none.
func TestNoVolatilePatternIsAGoLiteral(t *testing.T) {
	_, c := initVolatile(t)
	var lits []string
	for _, p := range c.Volatile.Patterns {
		lits = append(lits, p.Match)
	}
	lits = append(lits, c.Volatile.Exempt)
	lits = append(lits, c.Volatile.Exclude...)
	if len(lits) < 6 {
		t.Fatalf("defaults too few to check: %v", lits)
	}
	module := filepath.Join("..", "..")
	err := filepath.WalkDir(module, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" || (n == ".satelle" && p != module) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, l := range lits {
			if l != "" && strings.Contains(string(b), l) {
				t.Errorf("%s holds the configured literal %q", p, l)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
