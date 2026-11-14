package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The shipped reference-not-restate principle (sto_7e024b28), as the repo
// path its [hook] inject entry names.
const referencePrinciplePath = ".satelle/principles/satelle-reference-not-restate.md"

// referenceElements is AC4's own fixed list: the card's rules, each held as
// the phrase the principle's matched body must contain.
var referenceElements = []struct{ name, phrase string }{
	{"reference rule", "name the command, file or [[link]] that answers a volatile question"},
	{"not the answer", "instead of its current answer"},
	{"category versions", "versions"},
	{"category model ids", "model ids"},
	{"category counts", "counts"},
	{"category statuses", "statuses"},
	{"category file:line", "file:line"},
	{"category current defaults", "current defaults"},
	{"dated-history rule", "a volatile fact may appear only as dated history"},
	{"dated form, written out", "on <date> x was y"},
	{"plain rules", "rules and process are stated plainly"},
}

var spaceRun = regexp.MustCompile(`\s+`)

// matchedForm is text with its backticks removed, lower-cased and its
// whitespace runs collapsed.
func matchedForm(s string) string {
	return spaceRun.ReplaceAllString(strings.ToLower(strings.ReplaceAll(s, "`", "")), " ")
}

// principleBody is the text after the frontmatter's closing --- line.
func principleBody(text string) string {
	lines := strings.Split(text, "\n")
	seen := 0
	for i, l := range lines {
		if strings.TrimSpace(l) == "---" {
			if seen++; seen == 2 {
				return strings.Join(lines[i+1:], "\n")
			}
		}
	}
	return text
}

// unmetElements is the numbers (1-based) of the elements matched contains no
// phrase for.
func unmetElements(matched string) []int {
	var out []int
	for i, e := range referenceElements {
		if !strings.Contains(matched, e.phrase) {
			out = append(out, i+1)
		}
	}
	return out
}

func initReferencePrinciple(t *testing.T) (dir, text string) {
	t.Helper()
	dir = isolate(t)
	must(t, "init")
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(referencePrinciplePath)))
	if err != nil {
		t.Fatalf("init wrote no %s: %v", referencePrinciplePath, err)
	}
	return dir, string(b)
}

// sto_7e024b28 AC4: the shipped principle states every element of the list,
// each once, the reference rule and its categories on one line and the
// dated-history rule with its written form on one line; deleting any one
// element's phrase makes the check report exactly that element.
func TestTheShippedPrincipleStatesEveryRule(t *testing.T) {
	_, text := initReferencePrinciple(t)
	body := principleBody(text)
	matched := matchedForm(body)
	for _, n := range unmetElements(matched) {
		t.Errorf("the principle omits element %d (%s): %q", n, referenceElements[n-1].name, referenceElements[n-1].phrase)
	}
	for i, e := range referenceElements {
		if c := strings.Count(matched, e.phrase); c != 1 {
			t.Errorf("element %d (%s) occurs %d times in the body", i+1, e.name, c)
		}
	}

	lineWith := func(phrase string) string {
		for _, l := range strings.Split(body, "\n") {
			if strings.Contains(matchedForm(l), phrase) {
				return matchedForm(l)
			}
		}
		return ""
	}
	ref := lineWith(referenceElements[0].phrase)
	at := strings.Index(ref, referenceElements[0].phrase)
	for _, e := range append(referenceElements[2:8:8], referenceElements[1]) {
		j := strings.Index(ref, e.phrase)
		if j < 0 || j < at {
			t.Errorf("%q is not on the reference rule's line after the element before it: %q", e.phrase, ref)
			continue
		}
		at = j
	}
	var datedLine string
	for _, l := range strings.Split(body, "\n") {
		if strings.Contains(matchedForm(l), referenceElements[8].phrase) {
			datedLine = l
		}
	}
	m := matchedForm(datedLine)
	if i, j := strings.Index(m, referenceElements[8].phrase), strings.Index(m, referenceElements[9].phrase); i < 0 || j < i {
		t.Errorf("the dated-history rule and its form are not on one line in order: %q", datedLine)
	}
	if !strings.Contains(datedLine, "`on <date> X was Y`") {
		t.Errorf("the dated-history line does not write the form as the card does: %q", datedLine)
	}

	for i, e := range referenceElements {
		t.Run(fmt.Sprintf("without element %d", i+1), func(t *testing.T) {
			got := unmetElements(strings.Replace(matched, e.phrase, "", 1))
			if !slices.Equal(got, []int{i + 1}) {
				t.Errorf("deleting %q: check reports %v, want [%d]", e.phrase, got, i+1)
			}
		})
	}
}

// sto_7e024b28 AC4: satelle validate on a fresh init reports no volatile
// finding for the shipped principle, and does read it: a present-tense line
// appended to it is reported.
func TestValidateReportsNoVolatileFindingForTheShippedPrinciple(t *testing.T) {
	dir, text := initReferencePrinciple(t)
	rep, js, err := validateVolatileJSON(t)
	if err != nil || !rep.Valid {
		t.Fatalf("validate --json: err %v:\n%s", err, js)
	}
	for _, f := range rep.Volatile {
		if f.File == referencePrinciplePath {
			t.Errorf("volatile finding for the shipped principle: %s", f.String())
		}
	}
	if len(rep.Volatile) != 0 {
		t.Errorf("fresh init has volatile entries:\n%s", js)
	}

	appended := strings.TrimRight(text, "\n") + "\nThis is currently true.\n"
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(referencePrinciplePath)), []byte(appended), 0o644); err != nil {
		t.Fatal(err)
	}
	line := strings.Count(strings.TrimRight(text, "\n"), "\n") + 2
	rep, js, _ = validateVolatileJSON(t)
	if len(rep.Volatile) != 1 {
		t.Fatalf("control: want one volatile entry:\n%s", js)
	}
	if f := rep.Volatile[0]; f.File != referencePrinciplePath || f.Line != line || f.Subject != "present-tense" {
		t.Errorf("control entry %+v, want %s:%d present-tense", f, referencePrinciplePath, line)
	}
}

func referenceHook(t *testing.T, dir, event string) string {
	t.Helper()
	payload := fmt.Sprintf(`{"session_id":"s-ref","cwd":%q}`, dir)
	raw, err := run(t, payload, "hook", event, "--harness", "claude")
	if err != nil {
		t.Fatalf("hook %s: %v\n%s", event, err, raw)
	}
	return hookText(raw)
}

// sto_7e024b28 AC1: in a repo satelle init made, the session-start context
// carries the installed principle whole; the prompt context does not.
func TestSessionStartDeliversTheShippedPrinciple(t *testing.T) {
	dir, text := initReferencePrinciple(t)
	hasAll(t, "claude context", referenceHook(t, dir, "context"), strings.TrimRight(text, "\n"))
	heading := ""
	for _, l := range strings.Split(principleBody(text), "\n") {
		if strings.HasPrefix(l, "# ") {
			heading = l
			break
		}
	}
	if heading == "" {
		t.Fatal("the principle has no heading line")
	}
	hasNone(t, "claude prompt", referenceHook(t, dir, "prompt"), heading)
}

// sto_7e024b28 AC3: session start prints the principle from the installed
// file: a replaced file's words and none of the shipped rules, and with the
// file gone a line naming it and none of the shipped rules. The binary holds
// no copy.
func TestSessionStartPrintsThePrincipleFromTheInstalledFile(t *testing.T) {
	dir, text := initReferencePrinciple(t)
	file := filepath.Join(dir, filepath.FromSlash(referencePrinciplePath))
	hasAll(t, "context", referenceHook(t, dir, "context"), strings.TrimRight(text, "\n"))

	rules := []string{referenceElements[0].phrase, referenceElements[8].phrase, referenceElements[9].phrase, referenceElements[10].phrase}
	if err := os.WriteFile(file, []byte("nonce-principle-5d2e: this repo's own words.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := referenceHook(t, dir, "context")
	hasAll(t, "replaced context", ctx, "nonce-principle-5d2e: this repo's own words.")
	hasNone(t, "replaced context", matchedForm(ctx), rules...)

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	ctx = referenceHook(t, dir, "context")
	hasAll(t, "missing context", ctx, "[hook] inject file "+referencePrinciplePath)
	hasNone(t, "missing context", matchedForm(ctx), rules...)
}
