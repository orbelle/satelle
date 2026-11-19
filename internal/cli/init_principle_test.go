package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
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

func referenceContext(t *testing.T, dir string) string {
	t.Helper()
	raw, err := run(t, fmt.Sprintf(`{"session_id":"s-ref","cwd":%q}`, dir), "hook", "context", "--harness", "claude")
	if err != nil {
		t.Fatalf("hook context: %v\n%s", err, raw)
	}
	var env struct {
		Hook struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("hook context: %v\n%s", err, raw)
	}
	return env.Hook.Context
}

func TestSessionStartOpensWithTheInstalledPrinciplesDescription(t *testing.T) {
	const session = "satelle session: s-ref"
	dir, text := initReferencePrinciple(t)
	file := filepath.Join(dir, filepath.FromSlash(referencePrinciplePath))
	settingsFile := filepath.Join(dir, ".satelle", "satelle.toml")
	settings, err := os.ReadFile(settingsFile)
	if err != nil {
		t.Fatal(err)
	}
	sk, err := workflow.ParseSkill(referencePrinciplePath, text)
	if err != nil || !sk.IsFraming() || strings.TrimSpace(sk.Meta["description"]) == "" {
		t.Fatalf("the installed principle is not a framing principle with a description (%v): %+v", err, sk.Meta)
	}
	shipped := sk.Meta["description"]
	if ctx := referenceContext(t, dir); !strings.HasPrefix(ctx, shipped+"\n"+session) {
		t.Fatalf("the context does not open with the installed description, then the session line:\n%s", ctx)
	}
	hasNone(t, "prompt", referenceHook(t, dir, "prompt"), shipped)

	rewrite := func(old, next string) {
		t.Helper()
		if !strings.Contains(text, old) {
			t.Fatalf("the installed principle has no %q", old)
		}
		if err := os.WriteFile(file, []byte(strings.Replace(text, old, next, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opensWithSession := func(what string) string {
		t.Helper()
		ctx := referenceContext(t, dir)
		if !strings.HasPrefix(ctx, session) {
			t.Errorf("%s: the context does not open with the session line:\n%s", what, ctx)
		}
		return ctx
	}

	rewrite("description: "+shipped, "description: nonce-own-framing-7c1a")
	ctx := referenceContext(t, dir)
	if !strings.HasPrefix(ctx, "nonce-own-framing-7c1a\n"+session) {
		t.Errorf("a rewritten description does not open the context:\n%s", ctx)
	}
	if i := strings.Index(ctx, shipped); i >= 0 && i < strings.Index(ctx, session) {
		t.Errorf("the shipped description still opens the context after the file was rewritten:\n%s", ctx)
	}

	rewrite("framing: true\n", "")
	opensWithSession("no framing key")

	rewrite("type: principle", "type: skill")
	opensWithSession("another type")

	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	writeHookSettings(t, dir, "inject = []\n")
	opensWithSession("not injected")

	if err := os.WriteFile(settingsFile, settings, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	hasAll(t, "deleted", opensWithSession("deleted"), "[hook] inject file "+referencePrinciplePath)
}
