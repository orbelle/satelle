package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// sty_9e86f407 AC1/AC3: agents install/remove harness scaffolds.
func TestRemoveGrokHooksStripsOnlySatelle(t *testing.T) {
	repo := t.TempDir()
	created, _, _, err := ensureGrokHooks(repo)
	if err != nil || !created {
		t.Fatalf("ensure: created=%v err=%v", created, err)
	}
	path := filepath.Join(repo, filepath.FromSlash(grokHooksRel))
	// Inject user entry.
	raw, _ := os.ReadFile(path)
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	hooks := root["hooks"].(map[string]any)
	ss, _ := hooks["SessionStart"].([]any)
	ss = append(ss, map[string]any{"hooks": []any{
		map[string]any{"type": "command", "command": "echo keep-me"},
	}})
	hooks["SessionStart"] = ss
	b, _ := json.MarshalIndent(root, "", "  ")
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	action, _, note, err := removeGrokHooks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if action != "updated" {
		t.Fatalf("action=%s note=%s, want updated", action, note)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("file deleted despite user entry")
	}
	if !strings.Contains(string(after), "echo keep-me") {
		t.Fatalf("user entry must remain:\n%s", after)
	}
	if strings.Contains(string(after), "satelle-hook.sh") {
		t.Fatalf("satelle entries must be stripped:\n%s", after)
	}

	// A second remove finds nothing satelle-owned left and leaves the file alone.
	if a2, _, _, err := removeGrokHooks(repo); err != nil || a2 != "skipped" {
		t.Fatalf("second remove: action=%q err=%v, want skipped", a2, err)
	}
}

func TestEnsureClaudeHooksPreservesUserKeys(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Minimal user settings with a foreign hook.
	user := `{
  "env": {"MY": "1"},
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{"type": "command", "command": "echo foreign"}]
      }
    ]
  }
}
`
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := ensureClaudeHooks(repo)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"MY"`) || !strings.Contains(string(raw), "echo foreign") {
		t.Fatalf("user keys/hooks must survive:\n%s", raw)
	}
	if !strings.Contains(string(raw), "satelle-hook.sh") {
		t.Fatalf("satelle gate must be added:\n%s", raw)
	}
}

func TestRemoveGrokSkipsUnmarked(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".grok", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, filepath.FromSlash(grokHooksRel))
	if err := os.WriteFile(path, []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	action, _, note, err := removeGrokHooks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if action != "skipped" {
		t.Fatalf("want skipped for unmarked, got %s (%s)", action, note)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("unmarked file must remain")
	}
}

// TestEnsureGrokHooksSkipsUnmarked (AC1): install must not mutate user-owned
// .grok/hooks/satelle.json that lacks a satelle marker.
func TestEnsureGrokHooksSkipsUnmarked(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".grok", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, filepath.FromSlash(grokHooksRel))
	foreign := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo user-only"}]}]}}`
	if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	created, updated, _, err := ensureGrokHooks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("must not recreate over unmarked file")
	}
	if len(updated) == 0 || !strings.Contains(strings.Join(updated, " "), "skipped") {
		t.Fatalf("want skipped update note, got %v", updated)
	}
	after, _ := os.ReadFile(path)
	if string(after) != foreign {
		t.Fatalf("unmarked content must be byte-identical:\nbefore=%s\nafter=%s", foreign, after)
	}
	if strings.Contains(string(after), "satelle") {
		t.Fatal("must not inject satelle into unmarked file")
	}
}

func TestRemoveSharedHookScriptWhenUnreferenced(t *testing.T) {
	repo := t.TempDir()
	if _, _, _, err := ensureGrokHooks(repo); err != nil {
		t.Fatal(err)
	}
	// Still referenced → skip.
	a, _, note, err := maybeRemoveSharedHookScript(repo)
	if err != nil {
		t.Fatal(err)
	}
	if a != "skipped" {
		t.Fatalf("want skipped while referenced, got %s (%s)", a, note)
	}
	// Strip the grok hooks entirely then remove the shared script.
	path := filepath.Join(repo, filepath.FromSlash(grokHooksRel))
	_ = os.Remove(path)
	a2, _, _, err := maybeRemoveSharedHookScript(repo)
	if err != nil {
		t.Fatal(err)
	}
	if a2 != "removed" {
		t.Fatalf("want removed, got %s", a2)
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(satelleHookScriptRel))); !os.IsNotExist(err) {
		t.Fatal("shared script should be gone")
	}
}

func TestRemoveGrokKeepsUserTopLevelKey(t *testing.T) {
	repo := t.TempDir()
	if _, _, _, err := ensureGrokHooks(repo); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, filepath.FromSlash(grokHooksRel))
	raw, _ := os.ReadFile(path)
	var root map[string]any
	_ = json.Unmarshal(raw, &root)
	root["description"] = "user kept this"
	b, _ := json.MarshalIndent(root, "", "  ")
	_ = os.WriteFile(path, append(b, '\n'), 0o644)
	action, _, _, err := removeGrokHooks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if action == "removed" {
		t.Fatal("must not delete file when a user key remains")
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "user kept this") {
		t.Fatalf("user key must remain:\n%s", after)
	}
	if strings.Contains(string(after), "satelle-hook.sh") {
		t.Fatalf("satelle hooks should be stripped:\n%s", after)
	}
}

// TestInstalledGrokScaffoldDeniesMutation (AC2/AC5): the agents-install path
// writes .grok/hooks/satelle.json + wrapper; the deny path the wrapper calls with
// --harness grok emits Grok's top-level decision/reason shape.
func TestInstalledGrokScaffoldDeniesMutation(t *testing.T) {
	repo := t.TempDir()
	created, _, _, err := ensureGrokHooks(repo)
	if err != nil || !created {
		t.Fatalf("ensureGrokHooks: created=%v err=%v", created, err)
	}
	raw, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(grokHooksRel)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "satelle-hook.sh") {
		t.Fatalf("scaffold missing wrapper:\n%s", raw)
	}
	prev := hookHarnessFlag
	hookHarnessFlag = "grok"
	t.Cleanup(func() { hookHarnessFlag = prev })
	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&buf)
	ev := []byte(`{"toolInput":{"file_path":"` + filepath.Join(repo, "main.go") + `"}}`)
	// denyPreToolUse is the gate's deny path; the gate RunE needs a store.
	if err := denyPreToolUse(c, ev, "no engaged story"); err == nil {
		t.Fatal("expected deny error")
	}
	if !strings.Contains(buf.String(), `"decision":"deny"`) {
		t.Fatalf("want grok deny: %s", buf.String())
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("hooks file invalid: %v", err)
	}
	hooks := root["hooks"].(map[string]any)
	for _, ev := range []string{"SessionStart", "PreToolUse", "UserPromptSubmit", "Stop"} {
		if _, ok := hooks[ev]; !ok {
			t.Errorf("missing event %s", ev)
		}
	}
}
