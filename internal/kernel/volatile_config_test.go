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
	// The template ships no model-id pattern: which model vendors a repo uses
	// is the repo's own configuration (it is a commented example there).
	if want := []string{"version", "present-tense", "current-item"}; !slices.Equal(names, want) {
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

	// The dated exemption is the shipped dated key: a YYYY-MM-DD line is
	// exempt, another date shape is not, and with the key removed from the
	// written file the dated line is reported (sto_a5cdb024 AC1).
	if c.Volatile.Dated == "" {
		t.Fatal("init wrote no dated expression")
	}
	const dated, undated = "- 2026-11-08: released v1.0.57", "2026-1-8 v1.0.57"
	set, err := substrate.CompileVolatile(c.Volatile)
	if err != nil {
		t.Fatal(err)
	}
	if got := substrate.VolatileText("f.md", dated, set); len(got) != 0 {
		t.Errorf("shipped dated: %q reported: %v", dated, got)
	}
	if got := substrate.VolatileText("f.md", undated, set); len(got) != 1 {
		t.Errorf("shipped dated: %q want one finding, got %v", undated, got)
	}
	vb, err := os.ReadFile(filepath.Join(root, VolatileFile))
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, l := range strings.Split(string(vb), "\n") {
		if !strings.HasPrefix(l, "dated =") {
			kept = append(kept, l)
		}
	}
	write(t, filepath.Join(root, VolatileFile), strings.Join(kept, "\n"))
	noDated, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if noDated.Volatile.Dated != "" {
		t.Fatalf("dated line not removed: %q", noDated.Volatile.Dated)
	}
	set, err = substrate.CompileVolatile(noDated.Volatile)
	if err != nil {
		t.Fatal(err)
	}
	if got := substrate.VolatileText("f.md", dated, set); len(got) != 1 {
		t.Errorf("dated removed: %q want one finding, got %v", dated, got)
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
			match: []string{"This is currently broken.", "At the moment it fails", "the limit now is 5"},
			// Bare current and latest are relational, not "as of now" (sto_1ee018c8).
			miss: []string{".../releases/latest/download/install.sh", "the current_state field", "pass --latest",
				"the latest release", "use the `current` build", "the item's current state", "its latest run"},
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
		"unknown key":        "bogus = 1\n",
		"nameless":           "[[pattern]]\nmatch = 'x'\n",
		"duplicate":          "[[pattern]]\nname = 'a'\nmatch = 'x'\n[[pattern]]\nname = 'a'\nmatch = 'y'\n",
		"bad match":          "[[pattern]]\nname = 'a'\nmatch = '('\n",
		"bad dated":          "dated = '('\n[[pattern]]\nname = 'a'\nmatch = 'x'\n",
		"bad exclude":        "exclude = ['[']\n[[pattern]]\nname = 'a'\nmatch = 'x'\n",
		"dotdot paths":       "paths = ['../outside/*.md']\n[[pattern]]\nname = 'a'\nmatch = 'x'\n",
		"absolute":           "paths = ['/etc/*.md']\n[[pattern]]\nname = 'a'\nmatch = 'x'\n",
		"inner dotdot":       "exclude = ['a/../b/**']\n[[pattern]]\nname = 'a'\nmatch = 'x'\n",
		"ownerless upstream": "[[upstream]]\nfiles = ['a.md']\n[[pattern]]\nname = 'a'\nmatch = 'x'\n",
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
	// The three shipped defaults fire on line 7; the model id there is not
	// flagged, because the template ships no vendor list.
	if got := findings(); len(got) != 3 {
		t.Fatalf("defaults: want the three shipped patterns on line 7, got %v", got)
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
	lits = append(lits, c.Volatile.Exempt, c.Volatile.Dated)
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

// AC5: the template satelle ships holds exactly three active defaults and a
// commented model-id opt-in whose vendors are placeholders, so satelle ships
// no vendor list. The test reads the embedded file and requires Init to have
// written those same bytes, so what is asserted is what ships. It holds sample
// lines, pattern names and comment prefixes only, never pattern text.
func TestTemplateShipsThreeDefaultsAndACommentedModelID(t *testing.T) {
	shipped, err := fs.ReadFile(template, "template/"+VolatileFile)
	if err != nil {
		t.Fatal(err)
	}
	root, c := initVolatile(t)
	written, err := os.ReadFile(filepath.Join(root, VolatileFile))
	if err != nil || string(written) != string(shipped) {
		t.Fatalf("init did not write the embedded template (%v)", err)
	}

	// Three active defaults, each matching its shape.
	samples := map[string]string{
		"version":       "uses v1.0.57 of the binary",
		"present-tense": "This is currently broken.",
		"current-item":  "currently on sto_0f76dd4e",
	}
	var names []string
	for _, p := range c.Volatile.Patterns {
		names = append(names, p.Name)
		set, err := substrate.CompileVolatile(substrate.VolatileRules{Patterns: []substrate.VolatilePattern{p}})
		if err != nil {
			t.Fatal(err)
		}
		if got := substrate.VolatileText("f.md", samples[p.Name], set); len(got) != 1 {
			t.Errorf("%s does not match %q: %v", p.Name, samples[p.Name], got)
		}
	}
	if want := []string{"version", "present-tense", "current-item"}; !slices.Equal(names, want) {
		t.Fatalf("active patterns %v, want %v", names, want)
	}

	// The model-id example: three consecutive commented lines, not loaded,
	// whose alternation holds only placeholder vendors.
	lines := strings.Split(string(shipped), "\n")
	at := slices.Index(lines, `# name = "model-id"`)
	if at < 1 || lines[at-1] != "# [[pattern]]" || at+1 >= len(lines) || !strings.HasPrefix(lines[at+1], "# match = '") {
		t.Fatalf("no commented model-id example (# [[pattern]], # name, # match) in the template")
	}
	match := lines[at+1]
	open, close := strings.Index(match, "("), strings.Index(match, ")")
	if open < 0 || close < open {
		t.Fatalf("model-id example has no vendor group: %q", match)
	}
	for _, v := range strings.Split(match[open+1:close], "|") {
		if !strings.HasPrefix(v, "vendor-") {
			t.Errorf("model-id example names %q: the template ships placeholders, not a vendor list", v)
		}
	}

	// Uncommented, the example is valid configuration with a fourth pattern.
	un := strings.Join(slices.Concat(lines[:at-1], []string{"[[pattern]]", `name = "model-id"`, strings.TrimPrefix(match, "# ")}, lines[at+2:]), "\n")
	write(t, filepath.Join(root, VolatileFile), un)
	c2, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("the uncommented example does not load: %v", err)
	}
	if n := len(c2.Volatile.Patterns); n != 4 {
		t.Fatalf("uncommented template has %d patterns, want 4", n)
	}
}
