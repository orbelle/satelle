package substrate

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// testRules are rules with test-only patterns: the defaults are the init
// template's, proved in package kernel.
func testRules() VolatileRules {
	return VolatileRules{
		Exempt: "volatile-ok",
		Dated:  `\b[0-9]{4}-[0-9]{2}-[0-9]{2}\b`,
		Patterns: []VolatilePattern{
			{Name: "version", Match: `v?[0-9]+\.[0-9]+\.[0-9]+`},
			{Name: "present-tense", Match: `(?i)\bcurrently\b`},
		},
	}
}

func compile(t *testing.T, r VolatileRules) *VolatileSet {
	t.Helper()
	s, err := CompileVolatile(r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A line the configured dated expression matches, or holding the configured
// marker, is not reported; with no marker configured, or another marker on
// the line, or a date the expression does not match, it is (sto_3de33a72
// AC1). With dated unset no line is exempt for a date, and another dated
// expression exempts what it matches and nothing else: satelle holds no date
// rule of its own (sto_a5cdb024 AC1).
func TestVolatileTextExemptsDatedAndMarkedLines(t *testing.T) {
	marked := "uses v1.0.57 <!-- volatile-ok -->"
	noMarker := testRules()
	noMarker.Exempt = ""
	noDated := testRules()
	noDated.Dated = ""
	otherDated := testRules()
	otherDated.Dated = `^history:`
	cases := []struct {
		name  string
		rules VolatileRules
		line  string
		want  int
	}{
		{"dated", testRules(), "- 2026-11-08: released v1.0.57", 0},
		{"dated unset", noDated, "- 2026-11-08: released v1.0.57", 1},
		{"other dated, no date", otherDated, "history: released v1.0.57", 0},
		{"other dated, a date", otherDated, "- 2026-11-08: released v1.0.57", 1},
		{"marked", testRules(), marked, 0},
		{"no marker configured", noMarker, marked, 1},
		{"another marker", testRules(), "uses v1.0.57 <!-- stale-ok -->", 1},
		{"not YYYY-MM-DD", testRules(), "2026-1-8 v1.0.57", 1},
		{"plain", testRules(), "uses v1.0.57", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := VolatileText("f.md", tc.line, compile(t, tc.rules))
			if len(got) != tc.want {
				t.Fatalf("%q: want %d findings, got %v", tc.line, tc.want, got)
			}
		})
	}
	if VolatileText("f.md", "uses v1.0.57", nil) != nil {
		t.Error("a nil set reported")
	}
}

// A line matching two patterns gives one finding for each, naming it; a
// pattern matching twice on a line gives one.
func TestVolatileTextOneFindingPerPattern(t *testing.T) {
	got := VolatileText("f.md", "x\ncurrently v1.0.57 and v1.0.58\n", compile(t, testRules()))
	if len(got) != 2 || got[0].Subject != "version" || got[1].Subject != "present-tense" || got[0].Line != 2 || got[1].Line != 2 {
		t.Fatalf("got %v", got)
	}
	if !strings.Contains(got[0].Why, `"v1.0.57"`) {
		t.Errorf("why %q lacks the matched text", got[0].Why)
	}
}

// Every rule problem is named: a pattern with no name, a duplicate, an empty
// or uncompilable match, and each glob that is malformed or can leave the base.
func TestVolatileRulesProblems(t *testing.T) {
	ok := []VolatilePattern{{Name: "a", Match: "x"}}
	cases := []struct {
		name string
		r    VolatileRules
		want string
	}{
		{"nameless", VolatileRules{Patterns: []VolatilePattern{{Match: "x"}}}, "pattern 1 has no name"},
		{"duplicate", VolatileRules{Patterns: []VolatilePattern{{Name: "a", Match: "x"}, {Name: "a", Match: "y"}}}, `"a" is named twice`},
		{"empty match", VolatileRules{Patterns: []VolatilePattern{{Name: "a"}}}, `"a" has no match`},
		{"bad match", VolatileRules{Patterns: []VolatilePattern{{Name: "a", Match: "("}}}, `"a": match does not compile`},
		{"bad dated", VolatileRules{Patterns: ok, Dated: "("}, `dated does not compile`},
		{"empty glob", VolatileRules{Patterns: ok, Paths: []string{""}}, `paths glob "" is empty`},
		{"absolute", VolatileRules{Patterns: ok, Paths: []string{"/etc/*.md"}}, `paths glob "/etc/*.md" is absolute`},
		{"dotdot", VolatileRules{Patterns: ok, Paths: []string{"../outside/*.md"}}, `paths glob "../outside/*.md" has a .. segment`},
		{"inner dotdot", VolatileRules{Patterns: ok, Exclude: []string{"a/../b/**"}}, `exclude glob "a/../b/**" has a .. segment`},
		{"dot", VolatileRules{Patterns: ok, Exclude: []string{"./a"}}, `has a . segment`},
		{"empty segment", VolatileRules{Patterns: ok, Exclude: []string{"a//b"}}, `has an empty segment`},
		{"malformed", VolatileRules{Patterns: ok, Exclude: []string{"["}}, `exclude glob "[" has a malformed segment`},
		{"upstream no files", VolatileRules{Patterns: ok, Upstream: []VolatileUpstream{{Owner: "o"}}}, `upstream 1 names no files`},
		{"upstream dotdot", VolatileRules{Patterns: ok, Upstream: []VolatileUpstream{{Files: []string{"../x.md"}, Owner: "o"}}}, `upstream 1 files glob "../x.md" has a .. segment`},
		{"upstream ownerless", VolatileRules{Patterns: ok, Upstream: []VolatileUpstream{{Files: []string{"a.md"}}}}, `upstream 1 names no owner`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := tc.r.Problems()
			if len(ps) != 1 || !strings.Contains(ps[0].Error(), tc.want) {
				t.Fatalf("want one problem with %q, got %v", tc.want, ps)
			}
			if s, err := CompileVolatile(tc.r); err == nil || s != nil {
				t.Fatalf("CompileVolatile accepted %+v", tc.r)
			}
		})
	}
	if ps := (VolatileRules{Patterns: ok, Paths: []string{"docs/**/*.md"}, Exclude: []string{"**/testdata/**"}}).Problems(); len(ps) != 0 {
		t.Fatalf("valid rules: %v", ps)
	}
}

// VolatileSet holds its rules once: the rules themselves and their compiled
// expressions, with no field copying a string or list out of the rules
// (sto_a5cdb024 AC3).
func TestVolatileSetHoldsOneRepresentation(t *testing.T) {
	want := map[string]reflect.Type{
		"rules":    reflect.TypeOf(VolatileRules{}),
		"patterns": reflect.TypeOf([]*regexp.Regexp{}),
		"dated":    reflect.TypeOf((*regexp.Regexp)(nil)),
	}
	typ := reflect.TypeOf(VolatileSet{})
	if typ.NumField() != len(want) {
		t.Errorf("VolatileSet has %d fields, want %d (rules, patterns, dated)", typ.NumField(), len(want))
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		w, ok := want[f.Name]
		switch {
		case !ok:
			t.Errorf("VolatileSet field %s %s is not one of rules, patterns, dated", f.Name, f.Type)
		case f.Type != w:
			t.Errorf("VolatileSet field %s is %s, want %s", f.Name, f.Type, w)
		}
	}
	r := testRules()
	s := compile(t, r)
	if len(s.patterns) != len(r.Patterns) || s.dated == nil || s.dated.String() != r.Dated {
		t.Fatalf("compiled set does not pair with its rules: %d patterns for %d, dated %v", len(s.patterns), len(r.Patterns), s.dated)
	}
	for i, p := range r.Patterns {
		if s.patterns[i].String() != p.Match {
			t.Errorf("patterns[%d] = %s, want %s", i, s.patterns[i], p.Match)
		}
	}
	r.Dated = ""
	if compile(t, r).dated != nil {
		t.Error("dated unset compiled to an expression")
	}
}

// volatileRepo is <tmp>/repo with a .satelle data dir; it returns the data dir.
func volatileRepo(t *testing.T) (string, string) {
	t.Helper()
	tmp := t.TempDir()
	return tmp, filepath.Join(tmp, "repo", ".satelle")
}

func put(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const staleLine = "uses v1.0.57 of the binary\n"

// filesOf maps each finding's file to its count.
func filesOf(fs []Finding) map[string]int {
	m := map[string]int{}
	for _, f := range fs {
		m[f.File]++
	}
	return m
}

func volatile(t *testing.T, dataDir string, r VolatileRules) []Finding {
	t.Helper()
	got, err := Volatile(dataDir, r)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Volatile reports each matching line of the substrate Markdown and of the
// configured paths, naming file, line and pattern; what exclude names is not
// read, and the exclusions are configuration only (sto_3de33a72 AC4).
func TestVolatileScansSubstrateAndConfiguredPaths(t *testing.T) {
	tmp, data := volatileRepo(t)
	repo := filepath.Dir(data)
	put(t, filepath.Join(data, "skills", "x.md"), "1\n2\n3\n4\n5\n6\n"+staleLine)
	subs := []string{"constitution.md", "principles/p.md", "workflows/w.md", "tasks/t.md", "queues/q.md", "documents/d.md"}
	for _, f := range subs {
		put(t, filepath.Join(data, filepath.FromSlash(f)), staleLine)
	}
	put(t, filepath.Join(data, "workflows", "w.toml"), "# "+staleLine)
	put(t, filepath.Join(repo, "docs", "a", "n.md"), staleLine)

	rules := testRules()
	got := volatile(t, data, rules)
	var x *Finding
	for i := range got {
		if got[i].File == ".satelle/skills/x.md" {
			x = &got[i]
		}
	}
	if x == nil || x.Line != 7 || x.Kind != KindVolatile || x.Subject != "version" {
		t.Fatalf("no version finding at .satelle/skills/x.md:7 in %v", got)
	}
	if s := x.String(); !strings.Contains(s, ".satelle/skills/x.md:7: volatile: version — ") || !strings.Contains(s, rules.Patterns[0].Match) {
		t.Errorf("line form %q", s)
	}
	files := filesOf(got)
	for _, f := range subs {
		if files[".satelle/"+f] != 1 {
			t.Errorf(".satelle/%s not reported once: %v", f, files)
		}
	}
	if files[".satelle/workflows/w.toml"] != 0 || files["docs/a/n.md"] != 0 {
		t.Errorf("a file no list names was read: %v", files)
	}

	rules.Paths = []string{"docs/**/*.md", ".satelle/workflows/*.toml"}
	files = filesOf(volatile(t, data, rules))
	if files["docs/a/n.md"] != 1 || files[".satelle/workflows/w.toml"] != 1 {
		t.Errorf("configured paths not reported: %v", files)
	}

	rules.Paths = []string{".satelle/**/*.md"}
	for f, n := range filesOf(volatile(t, data, rules)) {
		if n != 1 {
			t.Errorf("%s reported %d times", f, n)
		}
	}

	// Run records and testdata fixtures: excluded only by configuration.
	excludedFiles := []string{".satelle/runs/r1/children.md", ".satelle/skills/testdata/f.md", "docs/testdata/f.md"}
	for _, f := range excludedFiles {
		put(t, filepath.Join(repo, filepath.FromSlash(f)), staleLine)
	}
	for _, paths := range [][]string{{"docs/**"}, {"docs/**", ".satelle/**"}} {
		rules.Paths = paths
		rules.Exclude = []string{".satelle/runs/**", "**/testdata/**"}
		files = filesOf(volatile(t, data, rules))
		for _, f := range excludedFiles {
			if files[f] != 0 {
				t.Errorf("paths %v: excluded %s reported", paths, f)
			}
		}
		rules.Exclude = nil
		files = filesOf(volatile(t, data, rules))
		for _, f := range excludedFiles {
			if files[f] != 1 {
				t.Errorf("paths %v, exclude []: %s not reported: %v", paths, f, files)
			}
		}
	}

	// Nothing outside the base is read: an escaping glob is refused, and a
	// symlink is not followed.
	outside := filepath.Join(tmp, "outside")
	put(t, filepath.Join(outside, "x.md"), staleLine)
	for _, g := range []string{"../outside/*.md", filepath.ToSlash(outside) + "/*.md"} {
		r := testRules()
		r.Paths = []string{g}
		if got, err := Volatile(data, r); err == nil || got != nil || !strings.Contains(err.Error(), g) {
			t.Errorf("paths %q: got %v, err %v", g, got, err)
		}
		if ps := r.Problems(); len(ps) != 1 || !strings.Contains(ps[0].Error(), g) {
			t.Errorf("paths %q: problems %v", g, ps)
		}
	}
	if err := os.Symlink(filepath.Join(outside, "x.md"), filepath.Join(repo, "docs", "link.md")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	r := testRules()
	r.Paths = []string{"docs/*.md"}
	if files := filesOf(volatile(t, data, r)); files["docs/link.md"] != 0 {
		t.Errorf("a symlink was followed: %v", files)
	}
	r.Paths = []string{"docs/link.md"}
	if files := filesOf(volatile(t, data, r)); files["docs/link.md"] != 0 {
		t.Errorf("a wildcard-free symlink path was read: %v", files)
	}
}

// With no pattern, Volatile reports nothing, even over matching text.
func TestVolatileWithNoPatternReportsNothing(t *testing.T) {
	_, data := volatileRepo(t)
	put(t, filepath.Join(data, "skills", "x.md"), staleLine)
	if got := volatile(t, data, VolatileRules{Exempt: "volatile-ok", Paths: []string{"**"}}); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

// sto_1ee018c8 AC2: in a Markdown file no line of a fenced block (``` or ~~~,
// any info string, the fences too) is read; outside a fence, or in another
// file type, the same lines are.
func TestVolatileTextSkipsFencedBlocks(t *testing.T) {
	s, err := CompileVolatile(VolatileRules{Patterns: []VolatilePattern{{Name: "latest", Match: `latest`}}})
	if err != nil {
		t.Fatal(err)
	}
	text := "latest outside\n```sh\nlatest=$(git tag | tail -1)\n```\n~~~\nlatest inside tilde\n~~~\nlatest after\n"
	var lines []int
	for _, f := range VolatileText("f.md", text, s) {
		lines = append(lines, f.Line)
	}
	if want := []int{1, 8}; !slices.Equal(lines, want) {
		t.Errorf("f.md lines %v, want %v", lines, want)
	}
	if got := VolatileText("f.txt", text, s); len(got) != 4 {
		t.Errorf("f.txt: %d findings, want 4 (no fences outside Markdown): %v", len(got), got)
	}
}

// sto_1ee018c8 AC3: a file an [[upstream]] entry matches is still read, and
// each finding names its owner; the first matching entry wins.
func TestVolatileUpstreamNamesTheOwner(t *testing.T) {
	r := VolatileRules{
		Patterns: []VolatilePattern{{Name: "v", Match: `v[0-9]+`}},
		Upstream: []VolatileUpstream{{Files: []string{"skills/lane-*.md"}, Owner: "id4me.pipelines satelle-lane/"}, {Files: []string{"skills/**"}, Owner: "second"}},
	}
	s, err := CompileVolatile(r)
	if err != nil {
		t.Fatal(err)
	}
	got := VolatileText("skills/lane-a.md", "uses v1\n", s)
	if len(got) != 1 || !strings.HasSuffix(got[0].Why, "; owned by id4me.pipelines satelle-lane/") {
		t.Errorf("lane file: %+v", got)
	}
	if got := VolatileText("other.md", "uses v1\n", s); len(got) != 1 || strings.Contains(got[0].Why, "owned by") {
		t.Errorf("unowned file: %+v", got)
	}
	if u, ok := r.UpstreamOf("skills/x.md"); !ok || u.Owner != "second" {
		t.Errorf("UpstreamOf(skills/x.md) = %+v %v", u, ok)
	}
}

// A fence closes only on the same character, at least as long, with nothing
// after it: a ~~~ inside a ``` block, or a longer fence's shorter run, is the
// block's text (code-ac round 1 of sto_1ee018c8).
func TestVolatileTextFencesCloseOnTheirOwnMarker(t *testing.T) {
	s, err := CompileVolatile(VolatileRules{Patterns: []VolatilePattern{{Name: "latest", Match: `latest`}}})
	if err != nil {
		t.Fatal(err)
	}
	text := "```sh\n~~~\nlatest=1\n```\nlatest after\n````md\n```\nlatest inner\n````\nlatest end\n"
	var lines []int
	for _, f := range VolatileText("f.md", text, s) {
		lines = append(lines, f.Line)
	}
	if want := []int{5, 10}; !slices.Equal(lines, want) {
		t.Errorf("lines %v, want %v", lines, want)
	}
}
