//go:build integration

package tests

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const workflowStandardSkill = ".satelle/skills/satelle-workflow-standard.md"

func readRepo(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInitWritesTheWorkflowStandardSkill(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	b, err := os.ReadFile(filepath.Join(s.dir, workflowStandardSkill))
	if err != nil {
		t.Fatalf("init did not write the workflow-standard skill: %v", err)
	}
	body := string(b)
	if tmpl := readRepo(t, "internal/kernel/template/skills/satelle-workflow-standard.md"); body != tmpl {
		t.Error("init's skill differs from the template")
	}
	if own := readRepo(t, workflowStandardSkill); withoutStandardLine(body) != withoutStandardLine(own) {
		t.Error("this repo's copy differs from the template on more than its Standard line")
	}
	for _, w := range []string{"name: satelle-workflow-standard", "Standard: none set", "any workflow", "rule ids", "read it"} {
		if !strings.Contains(body, w) {
			t.Errorf("the skill lacks %q", w)
		}
	}
	if strings.Contains(body, "workflows/") {
		t.Error("the skill restricts itself to a workflows/ path")
	}

	var conf map[string]any
	if _, err := toml.DecodeFile(filepath.Join(s.dir, ".satelle", "satelle.toml"), &conf); err != nil {
		t.Fatal(err)
	}
	hook, _ := conf["hook"].(map[string]any)
	if len(hook) != 1 {
		t.Fatalf("init's [hook] holds %v, want only inject", hook)
	}
	inject, _ := hook["inject"].([]any)
	if !slices.Contains(inject, any(workflowStandardSkill)) {
		t.Fatalf("init's [hook] inject is %v, want it to hold %s", hook["inject"], workflowStandardSkill)
	}

	var own struct {
		Hook struct {
			Inject []string `toml:"inject"`
		} `toml:"hook"`
	}
	if _, err := toml.Decode(readRepo(t, ".satelle/satelle.toml"), &own); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(own.Hook.Inject, workflowStandardSkill) {
		t.Errorf("this repo does not inject %s: %v", workflowStandardSkill, own.Hook.Inject)
	}
}

func (s *sandbox) hookContext(event, payload string) string {
	s.t.Helper()
	r := s.exec(satelleBin, payload, "hook", event, "--harness", "claude")
	if r.code != 0 {
		s.t.Fatalf("hook %s: exit %d\n%s%s", event, r.code, r.out, r.err)
	}
	var doc struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if strings.TrimSpace(r.out) == "" {
		return ""
	}
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil {
		s.t.Fatalf("hook %s output %q: %v", event, r.out, err)
	}
	return doc.HookSpecificOutput.AdditionalContext
}

func TestSessionStartCarriesTheWorkflowStandardSkill(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	payload := fmt.Sprintf(`{"session_id":"s-wfs","cwd":%q}`, s.dir)
	ctx := s.hookContext("context", payload)
	for _, w := range []string{"name: satelle-workflow-standard", "Standard: none set", "any workflow", "rule ids"} {
		if !strings.Contains(ctx, w) {
			t.Errorf("session-start context lacks %q:\n%s", w, ctx)
		}
	}
	if p := s.hookContext("prompt", payload); strings.Contains(p, "Standard: none set") {
		t.Errorf("the prompt context carries the skill:\n%s", p)
	}

	b, err := os.ReadFile(filepath.Join(s.dir, workflowStandardSkill))
	if err != nil {
		t.Fatal(err)
	}
	url := "https://example.invalid/standard"
	s.write(workflowStandardSkill, strings.Replace(string(b), "Standard: none set", "Standard: "+url, 1))
	ctx = s.hookContext("context", payload)
	if !strings.Contains(ctx, "Standard: "+url) || strings.Contains(ctx, "none set") {
		t.Errorf("session-start context does not carry the named standard as text:\n%s", ctx)
	}
}

func withoutStandardLine(s string) string {
	lines := strings.Split(s, "\n")
	return strings.Join(slices.DeleteFunc(lines, func(l string) bool { return strings.HasPrefix(l, "Standard: ") }), "\n")
}
