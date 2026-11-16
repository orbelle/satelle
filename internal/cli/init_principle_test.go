package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const referencePrinciplePath = ".satelle/principles/satelle-reference-not-restate.md"

var spaceRun = regexp.MustCompile(`\s+`)

func matchedForm(s string) string {
	return spaceRun.ReplaceAllString(strings.ToLower(strings.ReplaceAll(s, "`", "")), " ")
}

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

func principleLines(text string) []string {
	var out []string
	for _, l := range strings.Split(principleBody(text), "\n") {
		if l = strings.TrimSpace(matchedForm(l)); l != "" {
			out = append(out, l)
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

func TestSessionStartPrintsThePrincipleFromTheInstalledFile(t *testing.T) {
	dir, text := initReferencePrinciple(t)
	file := filepath.Join(dir, filepath.FromSlash(referencePrinciplePath))
	hasAll(t, "context", referenceHook(t, dir, "context"), strings.TrimRight(text, "\n"))

	lines := principleLines(text)
	if len(lines) == 0 {
		t.Fatal("the shipped principle has no body line")
	}
	if err := os.WriteFile(file, []byte("nonce-principle-5d2e: this repo's own words.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := referenceHook(t, dir, "context")
	hasAll(t, "replaced context", ctx, "nonce-principle-5d2e: this repo's own words.")
	hasNone(t, "replaced context", matchedForm(ctx), lines...)

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	ctx = referenceHook(t, dir, "context")
	hasAll(t, "missing context", ctx, "[hook] inject file "+referencePrinciplePath)
	hasNone(t, "missing context", matchedForm(ctx), lines...)
}
