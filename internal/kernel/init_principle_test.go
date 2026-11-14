package kernel

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/substrate"
)

// The shipped reference-not-restate principle (sto_7e024b28): its path under
// .satelle, and the session-start inject entry that names it.
const (
	referencePrinciple       = "principles/satelle-reference-not-restate.md"
	referencePrincipleInject = ".satelle/" + referencePrinciple
)

func embeddedReferencePrinciple(t *testing.T) string {
	t.Helper()
	b, err := fs.ReadFile(template, "template/"+referencePrinciple)
	if err != nil || len(b) == 0 {
		t.Fatalf("the template holds no %s: %v", referencePrinciple, err)
	}
	return string(b)
}

var (
	unitBreak   = regexp.MustCompile(`[.;:]\s+`)
	unitLead    = regexp.MustCompile(`^(?:[-*#>]+\s*|\d+\.\s+)+`)
	whitespaces = regexp.MustCompile(`\s+`)
)

func collapse(s string) string { return whitespaces.ReplaceAllString(strings.TrimSpace(s), " ") }

// principleSentences splits a file into its units: blocks are separated by
// blank lines and a block's lines are joined with a space; a block is split at
// a full stop, semicolon or colon followed by whitespace, and at its end. Each
// unit loses its leading list, heading and quote markers and its trailing
// punctuation, is lower-cased and has its whitespace collapsed. A unit of
// three or more words is a checked sentence; a shorter one is a skipped
// fragment. There is no length rule in characters.
func principleSentences(text string) (checked, skipped []string) {
	for _, block := range regexp.MustCompile(`\n\s*\n`).Split(text, -1) {
		joined := strings.Join(strings.Split(block, "\n"), " ")
		for _, u := range unitBreak.Split(joined, -1) {
			u = strings.TrimSpace(u)
			u = unitLead.ReplaceAllString(u, "")
			u = strings.TrimRight(u, ".;: ")
			u = collapse(strings.ToLower(u))
			if u == "" {
				continue
			}
			if len(strings.Fields(u)) >= 3 {
				checked = append(checked, u)
			} else {
				skipped = append(skipped, u)
			}
		}
	}
	return checked, skipped
}

// goFilesHolding walks root as TestNoResolveDefaultIsAGoLiteral does and
// returns, for each non-test Go file that holds one of the sentences (as
// given, or with its backticks removed) after lower-casing and collapsing its
// whitespace, the sentences it holds; read is the number of Go files read.
func goFilesHolding(t *testing.T, root string, sentences []string) (hits map[string][]string, read int) {
	t.Helper()
	hits = map[string][]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" || (n == ".satelle" && p != root) {
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
		read++
		src := collapse(strings.ToLower(string(b)))
		for _, s := range sentences {
			if strings.Contains(src, s) || strings.Contains(src, strings.ReplaceAll(s, "`", "")) {
				hits[p] = append(hits[p], s)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits, read
}

// sto_7e024b28 AC1: init writes the principle into .satelle/principles and the
// template's [hook] inject lists it; the fresh repo validates with no volatile
// finding, and every template inject entry names a file the template holds.
func TestInitShipsTheReferencePrinciple(t *testing.T) {
	root, c := initVolatile(t)
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(referencePrinciple)))
	if err != nil {
		t.Fatalf("init wrote no %s: %v", referencePrinciple, err)
	}
	for _, w := range []string{"\nname: satelle-reference-not-restate\n", "\ntype: principle\n"} {
		if !strings.Contains(string(b), w) {
			t.Errorf("the principle's frontmatter lacks %q", strings.TrimSpace(w))
		}
	}
	if !slices.Contains(c.Settings.Hook.Inject, referencePrincipleInject) {
		t.Errorf("the template's [hook] inject %v lacks %s", c.Settings.Hook.Inject, referencePrincipleInject)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Errorf("fresh init does not validate: %v", errs)
	}
	if got, err := c.VolatileFindings(); err != nil || len(got) != 0 {
		t.Errorf("fresh init: volatile findings %v, err %v", got, err)
	}
	for _, f := range c.Settings.Hook.Inject {
		rel, ok := strings.CutPrefix(f, ".satelle/")
		if !ok {
			continue
		}
		if _, err := fs.Stat(template, "template/"+rel); err != nil {
			t.Errorf("template inject entry %s names no template file: %v", f, err)
		}
	}
}

// sto_7e024b28 AC2: satelle init on a configured repo adds the missing
// shipped principle, and every authored file stays byte for byte.
func TestInitAddsTheShippedPrincipleToAConfiguredRepo(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	authored := map[string]string{
		SettingsFile: "[workflows]\ndefault = \"delivery\"\n",
		filepath.Join(WorkflowsDir, "delivery.toml"): "name = \"delivery\"\n\n[states.backlog]\nstart = true\n\n[states.done]\nterminal = true\n\n[[transition]]\nfrom = [\"backlog\"]\nto = \"done\"\n",
		filepath.Join("principles", "ours.md"):       "---\nname: ours\ntype: principle\ndescription: Ours.\n---\n\n# Ours\n",
	}
	for f, body := range authored {
		write(t, filepath.Join(root, f), body)
	}
	plan, err := InitPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan, referencePrinciple) {
		t.Fatalf("InitPlan for a configured repo lacking the principle: %v", plan)
	}
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(referencePrinciple))); err != nil {
		t.Fatalf("init did not add %s: %v", referencePrinciple, err)
	}
	for f, body := range authored {
		if b, err := os.ReadFile(filepath.Join(root, f)); err != nil || string(b) != body {
			t.Errorf("init changed the authored %s (%v):\n%s", f, err, b)
		}
	}
}

// sto_7e024b28 AC2 ('overridable'): a repo's own file of the same name stays,
// and InitPlan does not list it.
func TestInitLeavesAnAuthoredPrincipleOfTheSameNameAlone(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	own := "---\nname: satelle-reference-not-restate\ntype: principle\ndescription: Our own.\n---\n\n# Our own wording\n"
	write(t, filepath.Join(root, filepath.FromSlash(referencePrinciple)), own)
	plan, err := InitPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan, referencePrinciple) {
		t.Errorf("InitPlan lists an existing %s: %v", referencePrinciple, plan)
	}
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(referencePrinciple))); string(b) != own {
		t.Errorf("init changed the repo's own principle:\n%s", b)
	}
}

// sto_7e024b28 AC3: the principle is the embedded template file, and the file
// init writes is those bytes.
func TestThePrincipleIsTheEmbeddedTemplateFile(t *testing.T) {
	shipped := embeddedReferencePrinciple(t)
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(referencePrinciple))); err != nil || string(b) != shipped {
		t.Fatalf("init did not write the embedded %s (%v)", referencePrinciple, err)
	}
}

// sto_7e024b28 AC3: no non-test Go file in the module holds a sentence of
// three or more words of the shipped principle.
func TestThePrincipleIsNotAGoLiteral(t *testing.T) {
	checked, _ := principleSentences(embeddedReferencePrinciple(t))
	if len(checked) == 0 {
		t.Fatal("the principle has no checked sentence")
	}
	hits, read := goFilesHolding(t, filepath.Join("..", ".."), checked)
	if read == 0 {
		t.Fatal("the scan read no Go file")
	}
	for p, ss := range hits {
		for _, s := range ss {
			t.Errorf("%s holds the principle's sentence %q", p, s)
		}
	}
}

// sto_7e024b28 AC3: the scan checks every sentence of three or more words,
// with no length gap: the plain-rules sentence is a checked unit and is found
// when pasted into Go, every checked sentence is found when pasted, case and
// wrapping do not hide one, and only fragments under three words are skipped.
func TestThePrincipleScanChecksEverySentence(t *testing.T) {
	text := embeddedReferencePrinciple(t)
	checked, skipped := principleSentences(text)
	const plain = "rules and process are stated plainly"
	if !slices.Contains(checked, plain) {
		t.Fatalf("%q is not a checked sentence: %q", plain, checked)
	}

	tree := func(files map[string]string) string {
		dir := t.TempDir()
		for f, body := range files {
			write(t, filepath.Join(dir, filepath.FromSlash(f)), body)
		}
		return dir
	}
	line := "package pkg\n\nconst s = \"" + plain + "\"\n"
	dir := tree(map[string]string{"pkg/lit.go": line, "pkg/lit_test.go": line})
	hits, _ := goFilesHolding(t, dir, checked)
	if got := hits[filepath.Join(dir, "pkg", "lit.go")]; !slices.Contains(got, plain) {
		t.Errorf("the pasted plain-rules sentence was not reported: %v", hits)
	}
	if _, ok := hits[filepath.Join(dir, "pkg", "lit_test.go")]; ok {
		t.Errorf("a _test.go file was reported: %v", hits)
	}

	for i, s := range checked {
		dir := tree(map[string]string{"pkg/p.go": "package pkg\n\n// " + strings.ReplaceAll(s, "`", "") + "\n"})
		if hits, _ := goFilesHolding(t, dir, checked); !slices.Contains(hits[filepath.Join(dir, "pkg", "p.go")], s) {
			t.Errorf("sentence %d %q pasted into Go was not reported", i, s)
		}
	}

	shout := strings.ReplaceAll(strings.ToUpper(plain), " ", "  ")
	dir = tree(map[string]string{"pkg/p.go": "package pkg\n\n// " + shout + "\n"})
	if hits, _ := goFilesHolding(t, dir, checked); !slices.Contains(hits[filepath.Join(dir, "pkg", "p.go")], plain) {
		t.Errorf("the plain-rules sentence upper-cased with doubled spaces was not reported")
	}

	units := 0
	for _, block := range regexp.MustCompile(`\n\s*\n`).Split(text, -1) {
		for _, u := range unitBreak.Split(strings.Join(strings.Split(block, "\n"), " "), -1) {
			if strings.TrimRight(collapse(unitLead.ReplaceAllString(strings.TrimSpace(u), "")), ".;: ") != "" {
				units++
			}
		}
	}
	if len(checked)+len(skipped) != units {
		t.Errorf("checked %d + skipped %d != %d units", len(checked), len(skipped), units)
	}
	for _, f := range skipped {
		if len(strings.Fields(f)) >= 3 {
			t.Errorf("skipped fragment %q has three or more words", f)
		}
	}
	if len(skipped) > 0 {
		frag := skipped[0]
		dir := tree(map[string]string{"pkg/p.go": "package pkg\n\n// " + frag + "\n"})
		if hits, _ := goFilesHolding(t, dir, checked); len(hits) != 0 {
			t.Errorf("a skipped fragment %q was reported: %v", frag, hits)
		}
	}

	plainForm := func(s string) string { return strings.ReplaceAll(s, "`", "") }
	var ref, dated, form bool
	for _, s := range checked {
		ref = ref || strings.Contains(plainForm(s), "name the command, file or [[link]] that answers a volatile question")
		dated = dated || s == "a volatile fact may appear only as dated history"
		form = form || plainForm(s) == "on <date> x was y"
	}
	if !ref || !dated || !form {
		t.Errorf("rule coverage: reference %v, dated-history rule %v, dated form %v in %q", ref, dated, form, checked)
	}
}

// sto_7e024b28 AC4: the principle's dated-history line, under the shipped
// volatile rules, is read (not dated, not exempt) and matches no pattern; no
// line of the principle passes by exemption; the same line with a present-tense
// word appended is reported.
func TestTheDatedHistoryLineDrawsNoVolatileFinding(t *testing.T) {
	_, c := initVolatile(t)
	set, err := substrate.CompileVolatile(c.Volatile)
	if err != nil {
		t.Fatal(err)
	}
	if c.Volatile.Dated == "" || c.Volatile.Exempt == "" {
		t.Fatalf("the shipped volatile.toml has dated %q exempt %q", c.Volatile.Dated, c.Volatile.Exempt)
	}
	dated := regexp.MustCompile(c.Volatile.Dated)
	var line string
	for _, l := range strings.Split(embeddedReferencePrinciple(t), "\n") {
		if strings.Contains(l, "`on <date> X was Y`") {
			line = l
		}
		if dated.MatchString(l) || strings.Contains(l, c.Volatile.Exempt) {
			t.Errorf("principle line passes by exemption: %q", l)
		}
	}
	if line == "" {
		t.Fatal("the principle has no line holding `on <date> X was Y`")
	}
	if got := substrate.VolatileText("p.md", line, set); len(got) != 0 {
		t.Errorf("the dated-history line draws %v", got)
	}
	got := substrate.VolatileText("p.md", line+" currently", set)
	if len(got) != 1 || got[0].Subject != "present-tense" {
		t.Errorf("control: the line with ' currently' drew %v, want one present-tense finding", got)
	}
}
