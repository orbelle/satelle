package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/substrate"
)

// validateVolatile is satelle validate --json's verdict and volatile list.
type validateVolatile struct {
	Valid    bool                `json:"valid"`
	Problems []string            `json:"problems"`
	Volatile []substrate.Finding `json:"volatile"`
}

func validateVolatileJSON(t *testing.T) (validateVolatile, string, error) {
	t.Helper()
	out, err := run(t, "", "validate", "--json")
	var rep validateVolatile
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("validate --json: %v\n%s", jerr, out)
	}
	return rep, out, err
}

// In a repo satelle init made, satelle validate lists each line matching a
// default volatile pattern with its file, line and pattern, and passes; satelle
// doctor reports the same lines. A dated or marked line, and a run record, are
// not reported; a configured doc path is; an escaping glob is a problem; with
// no volatile.toml nothing is listed (sto_3de33a72 AC1, AC2, AC4).
func TestValidateReportsVolatileLiterals(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	data := filepath.Join(dir, ".satelle")
	writeFile(t, filepath.Join(data, "skills", "notes.md"), "---\nname: notes\ntype: skill\ndescription: Notes.\n---\n\n"+
		"Uses v1.0.57 of the binary.\n"+ // 7
		"This is currently broken.\n"+ // 8
		"- 2026-11-08: released v1.0.57\n"+ // 9
		"Pinned to v1.0.58 <!-- volatile-ok -->\n") // 10
	writeFile(t, filepath.Join(data, "runs", "x", "children.md"), "Uses v1.0.57 of the binary.\n")

	want := []string{".satelle/skills/notes.md:7: volatile: version — ", ".satelle/skills/notes.md:8: volatile: present-tense — "}
	absent := []string{"notes.md:9:", "notes.md:10:", "runs/"}
	out := must(t, "validate")
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("validate lacks %q:\n%s", w, out)
		}
	}
	for _, a := range absent {
		if strings.Contains(out, a) {
			t.Errorf("validate reports %q:\n%s", a, out)
		}
	}
	rep, js, err := validateVolatileJSON(t)
	if err != nil || !rep.Valid || len(rep.Volatile) != 2 {
		t.Fatalf("validate --json: err %v, want valid with two volatile entries:\n%s", err, js)
	}
	if f := rep.Volatile[0]; f.File != ".satelle/skills/notes.md" || f.Line != 7 || f.Kind != substrate.KindVolatile || f.Subject != "version" {
		t.Errorf("first entry %+v", f)
	}

	doctor, _ := run(t, "", "doctor")
	for _, w := range want {
		if !strings.Contains(doctor, "volatile: "+w) {
			t.Errorf("doctor lacks %q:\n%s", w, doctor)
		}
	}
	for _, a := range absent {
		if strings.Contains(doctor, a) {
			t.Errorf("doctor reports %q:\n%s", a, doctor)
		}
	}

	conf := filepath.Join(data, kernel.VolatileFile)
	b, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	setPaths := func(paths string) {
		t.Helper()
		body := strings.Replace(string(b), "\npaths = []\n", "\npaths = "+paths+"\n", 1)
		if body == string(b) {
			t.Fatalf("no paths = [] line in %s", conf)
		}
		writeFile(t, conf, body)
	}
	writeFile(t, filepath.Join(dir, "docs", "notes.md"), "Uses v1.0.57 of the binary.\n")
	if rep, _, _ = validateVolatileJSON(t); slices.ContainsFunc(rep.Volatile, func(f substrate.Finding) bool { return f.File == "docs/notes.md" }) {
		t.Errorf("docs/notes.md reported before paths names it: %+v", rep.Volatile)
	}
	setPaths(`["docs/*.md"]`)
	rep, js, _ = validateVolatileJSON(t)
	n := 0
	for _, f := range rep.Volatile {
		if f.File == "docs/notes.md" && f.Line == 1 {
			n++
		}
	}
	if n != 1 {
		t.Errorf("docs/notes.md reported %d times, want once:\n%s", n, js)
	}

	setPaths(`["../x/*.md"]`)
	rep, js, err = validateVolatileJSON(t)
	if err == nil || rep.Valid || len(rep.Volatile) != 0 || !slices.ContainsFunc(rep.Problems, func(p string) bool { return strings.Contains(p, kernel.VolatileFile+":") }) {
		t.Fatalf("escaping glob: err %v, want invalid, a %s problem and no volatile entries:\n%s", err, kernel.VolatileFile, js)
	}

	if err := os.Remove(conf); err != nil {
		t.Fatal(err)
	}
	if out := must(t, "validate", "--json"); !strings.Contains(out, `"volatile": []`) {
		t.Fatalf("no volatile.toml: want volatile []:\n%s", out)
	}
}

// The baseline AC3 records is real and repeatable: this repo's .satelle
// carries the default patterns, and every finding over it names an existing
// file and line, never a run record or a testdata fixture. No count is pinned:
// the baseline shrinks as lines are fixed (sto_3de33a72 AC3).
func TestRepoVolatileBaselineRuns(t *testing.T) {
	root := filepath.Join("..", "..")
	cfg, err := kernel.LoadConfig(filepath.Join(root, ".satelle"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range cfg.Volatile.Patterns {
		names = append(names, p.Name)
	}
	// This repo carries every pattern the init template ships, plus a model-id
	// pattern the template does not load (AC6): compared with what a fresh
	// init writes, not with a hardcoded list.
	fresh := filepath.Join(t.TempDir(), ".satelle")
	if err := kernel.Init(fresh); err != nil {
		t.Fatal(err)
	}
	tmpl, err := kernel.LoadConfig(fresh)
	if err != nil {
		t.Fatal(err)
	}
	var shipped []string
	for _, p := range tmpl.Volatile.Patterns {
		shipped = append(shipped, p.Name)
		if !slices.Contains(names, p.Name) {
			t.Errorf("this repo's volatile.toml lacks the template's pattern %q (has %v)", p.Name, names)
		}
	}
	if slices.Contains(shipped, "model-id") || !slices.Contains(names, "model-id") {
		t.Errorf("model-id: the template loads %v and this repo %v; want it in this repo's only", shipped, names)
	}
	// AC6: this repo enables model-id with its own vendors: one model id from
	// each family satelle-agent-alt-reviewer.md names for this repo matches,
	// and the near misses do not. The samples are test literals.
	for _, p := range cfg.Volatile.Patterns {
		if p.Name != "model-id" {
			continue
		}
		set, err := substrate.CompileVolatile(substrate.VolatileRules{Patterns: []substrate.VolatilePattern{p}})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"claude-fable-5-2", "gpt-5.6-sol-high", "gemini-3.8-flash-high", "grok-4"} {
			if got := substrate.VolatileText("f.md", "runs on "+id, set); len(got) != 1 {
				t.Errorf("this repo's model-id does not match %s: %v", id, got)
			}
		}
		// A model id inside a hyphenated name (an agent named after its model,
		// sto_e65b2d30) is not reported, while the bare id above is. The names
		// are test literals, not this repo's bindings.
		for _, miss := range []string{"claude-code", "reviewer-grok", "judge-gpt-9.9-x", "judge-gemini-0.1-y", "judge-grok-7.7", "`judge-grok-7.7`"} {
			if got := substrate.VolatileText("f.md", "uses "+miss, set); len(got) != 0 {
				t.Errorf("this repo's model-id matches %s: %v", miss, got)
			}
		}
	}
	found, err := cfg.VolatileFindings()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if strings.HasPrefix(f.File, ".satelle/runs/") || strings.Contains("/"+f.File, "/testdata/") {
			t.Errorf("excluded file scanned: %s", f)
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.File)))
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if lines := strings.Count(string(b), "\n") + 1; f.Line < 1 || f.Line > lines {
			t.Errorf("%s: line outside the file's %d lines", f, lines)
		}
	}
}
