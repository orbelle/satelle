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

func TestValidateReportsVolatileLiterals(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	data := filepath.Join(dir, ".satelle")
	writeFile(t, filepath.Join(data, "skills", "notes.md"), "---\nname: notes\ntype: skill\ndescription: Notes.\n---\n\n"+
		"Uses v1.0.57 of the binary.\n"+
		"This is currently broken.\n"+
		"- 2026-11-08: released v1.0.57\n"+
		"Pinned to v1.0.58 <!-- volatile-ok -->\n")
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
		body := strings.TrimPrefix(strings.Replace("\n"+string(b), "\npaths = []\n", "\npaths = "+paths+"\n", 1), "\n")
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
		if p.Name == "present-tense" {
			for _, q := range cfg.Volatile.Patterns {
				if q.Name == p.Name && q.Match != p.Match {
					t.Errorf("present-tense: this repo matches %q, a fresh init %q", q.Match, p.Match)
				}
			}
		}
	}
	if cfg.Volatile.Dated == "" || cfg.Volatile.Dated != tmpl.Volatile.Dated {
		t.Errorf("dated: this repo %q, a fresh init %q", cfg.Volatile.Dated, tmpl.Volatile.Dated)
	}
	if u := cfg.Doctor.Resolve.Unset(); len(u) != 0 {
		t.Errorf("this repo's doctor.toml leaves [resolve] %v unset", u)
	}
	if slices.Contains(shipped, "model-id") || !slices.Contains(names, "model-id") {
		t.Errorf("model-id: the template loads %v and this repo %v; want it in this repo's only", shipped, names)
	}
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

func TestDoctorNamesTheUpstreamOwner(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	data := filepath.Join(dir, ".satelle")
	for _, n := range []string{"vendored", "local"} {
		writeFile(t, filepath.Join(data, "skills", n+".md"), "---\nname: "+n+"\ntype: skill\ndescription: A skill.\n---\n\nUses v1.0.57 of the binary.\n")
	}
	vt := filepath.Join(data, kernel.VolatileFile)
	b, err := os.ReadFile(vt)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, vt, string(b)+"\n[[upstream]]\nfiles = [\".satelle/skills/vendored.md\"]\nowner = \"lane-repo satelle-lane/\"\nfix = \"satelle-lane/sync\"\n")
	out := must(t, "validate")
	if !strings.Contains(out, "vendored.md:7: volatile: version") || !strings.Contains(out, "owned by lane-repo satelle-lane/") {
		t.Errorf("validate does not name the vendored file's owner:\n%s", out)
	}
	if !strings.Contains(out, "local.md:7: volatile: version") {
		t.Errorf("validate lacks the local file:\n%s", out)
	}
	doc, _ := run(t, "", "doctor")
	var vendored, local string
	lines := strings.Split(doc, "\n")
	for i, l := range lines {
		if i+1 < len(lines) && strings.Contains(l, "vendored.md:7") {
			vendored = lines[i+1]
		}
		if i+1 < len(lines) && strings.Contains(l, "local.md:7") {
			local = lines[i+1]
		}
	}
	if !strings.Contains(vendored, "owned by lane-repo satelle-lane/") || !strings.Contains(vendored, "satelle-lane/sync") || !strings.Contains(vendored, "do not edit it in place") {
		t.Errorf("doctor's fix for the vendored file: %q\n%s", vendored, doc)
	}
	if !strings.Contains(local, "reword the line") {
		t.Errorf("doctor's fix for the local file: %q", local)
	}
}

func TestInitSaysItEnabledTheVolatileCheck(t *testing.T) {
	dir := isolate(t)
	if out := must(t, "init"); !strings.Contains(out, "volatile check enabled") || !strings.Contains(out, "flags 0 line(s)") {
		t.Errorf("first init:\n%s", out)
	}
	data := filepath.Join(dir, ".satelle")
	if err := os.Remove(filepath.Join(data, kernel.VolatileFile)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(data, "skills", "notes.md"), "---\nname: notes\ntype: skill\ndescription: Notes.\n---\n\nUses v1.0.57 of the binary.\nThis is currently broken.\nCurrently on v1.0.58.\n")
	if out := must(t, "init"); !strings.Contains(out, "volatile check enabled") || !strings.Contains(out, "flags 3 line(s)") {
		t.Errorf("init that adds volatile.toml to an existing repo:\n%s", out)
	}
	if out := must(t, "init"); strings.Contains(out, "volatile check enabled") {
		t.Errorf("an init that adds nothing announced the check:\n%s", out)
	}
}

func TestRepoHasNoVolatileLine(t *testing.T) {
	cfg, err := kernel.LoadConfig(filepath.Join("..", "..", ".satelle"))
	if err != nil {
		t.Fatal(err)
	}
	found, err := cfg.VolatileFindings()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		t.Errorf("volatile line: %s", f.String())
	}
}
