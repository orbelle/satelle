package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// TestReconcileClaudeHooks covers the stale-hook reconciliation (sty_6a919dff):
// a retired satelle command inside an existing settings.json is rewritten to its
// replacement, the user's other content is preserved byte-for-byte, and the pass
// is idempotent.
func TestReconcileClaudeHooks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	stale := `{
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "command", "command": "satelle index" },
                   { "type": "command", "command": "my-custom-hook --flag" } ] }
    ]
  },
  "custom": "user setting"
}`
	if err := os.WriteFile(p, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := reconcileClaudeHooks(p)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(changed) != 1 || !strings.Contains(changed[0], "satelle index -> satelle reindex") {
		t.Errorf("changed = %v, want the index->reindex rename", changed)
	}
	got, _ := os.ReadFile(p)
	if !strings.Contains(string(got), `"satelle reindex"`) || strings.Contains(string(got), `"satelle index"`) {
		t.Errorf("stale command not rewritten:\n%s", got)
	}
	// User content preserved.
	for _, keep := range []string{"my-custom-hook --flag", `"custom": "user setting"`} {
		if !strings.Contains(string(got), keep) {
			t.Errorf("user content %q not preserved:\n%s", keep, got)
		}
	}
	// Idempotent: a second pass changes nothing.
	if changed, _ := reconcileClaudeHooks(p); len(changed) != 0 {
		t.Errorf("second pass should be a no-op, got %v", changed)
	}
	// A file with no stale commands is untouched (same bytes).
	before, _ := os.ReadFile(p)
	if _, err := reconcileClaudeHooks(p); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Error("clean file must be untouched")
	}
}

func TestDetectProcessHarnesses(t *testing.T) {
	repo := t.TempDir()
	// Neither forced nor dirs → nothing (no PATH, no claude default).
	c, g := detectProcessHarnesses(repo, nil)
	if c || g {
		t.Errorf("empty: claude=%v grok=%v, want none", c, g)
	}
	// Forced flag only.
	c, g = detectProcessHarnesses(repo, []string{"grok"})
	if c || !g {
		t.Errorf("forced grok: claude=%v grok=%v, want grok-only", c, g)
	}
	c, g = detectProcessHarnesses(repo, []string{"claude", "grok"})
	if !c || !g {
		t.Errorf("forced all: claude=%v grok=%v, want all", c, g)
	}
	// Existing harness dirs (PATH must not matter — forced nil).
	if err := os.MkdirAll(filepath.Join(repo, ".grok"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, g = detectProcessHarnesses(repo, nil)
	if c || !g {
		t.Errorf(".grok dir only: claude=%v grok=%v, want grok-only", c, g)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, g = detectProcessHarnesses(repo, nil)
	if !c || !g {
		t.Errorf("claude+grok dirs: claude=%v grok=%v, want all", c, g)
	}
}

func TestDetectSessionHarnesses(t *testing.T) {
	c, g := detectSessionHarnessesFrom([]string{"CLAUDE_CODE_ENTRYPOINT=cli", "PATH=/bin"})
	if !c || g {
		t.Errorf("CLAUDE_CODE_ only: claude=%v grok=%v", c, g)
	}
	c, g = detectSessionHarnessesFrom([]string{"GROK_AGENT=1", "PATH=/bin"})
	if c || !g {
		t.Errorf("GROK_AGENT only: claude=%v grok=%v", c, g)
	}
	c, g = detectSessionHarnessesFrom([]string{"PATH=/bin", "HOME=/tmp"})
	if c || g {
		t.Errorf("no markers: claude=%v grok=%v", c, g)
	}
	c, g = detectSessionHarnessesFrom([]string{"GROK_AGENT=0"})
	if g {
		t.Error("GROK_AGENT=0 must not count as grok session")
	}
}

// TestEnsureLazySessionHarness: marker-driven install only inside an initialised
// satelle repo; uninitialised repo → no-op; second call idempotent.
// Marker absence is covered by TestDetectSessionHarnesses (matrix); this test
// exercises the install guards on ensureLazySessionHarness itself.
func TestEnsureLazySessionHarness(t *testing.T) {
	// Uninitialised repo: no .satelle → no-op even with markers.
	bare := t.TempDir()
	t.Setenv("GROK_AGENT", "1")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	ensureLazySessionHarness(bare)
	if _, err := os.Stat(filepath.Join(bare, ".grok", "hooks", "satelle.json")); err == nil {
		t.Error("lazy install must not run outside initialised satelle repo")
	}
	if _, err := os.Stat(filepath.Join(bare, ".claude", "settings.json")); err == nil {
		t.Error("lazy install must not run outside initialised satelle repo (claude)")
	}

	// Initialised + GROK_AGENT → install grok scaffold when missing.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, config.DefaultDataDir), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_AGENT", "1")
	ensureLazySessionHarness(repo)
	path := filepath.Join(repo, filepath.FromSlash(grokHooksRel))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("GROK_AGENT session should install grok hooks: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ensureLazySessionHarness(repo)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("second lazy install must be idempotent (no rewrite)")
	}

	// Claude marker installs settings when missing.
	repo2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo2, config.DefaultDataDir), 0o755); err != nil {
		t.Fatal(err)
	}
	// Isolate from host GROK_AGENT so this path only asserts claude settings.
	t.Setenv("GROK_AGENT", "0")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	ensureLazySessionHarness(repo2)
	if _, err := os.Stat(filepath.Join(repo2, ".claude", "settings.json")); err != nil {
		t.Fatalf("CLAUDE_CODE_* session should install claude settings: %v", err)
	}
}

func TestParseHarnessFlag(t *testing.T) {
	got, err := parseHarnessFlag("")
	if err != nil || got != nil {
		t.Fatalf("empty: got %v err %v", got, err)
	}
	got, err = parseHarnessFlag("grok,claude,grok")
	if err != nil || len(got) != 2 || got[0] != "grok" || got[1] != "claude" {
		t.Fatalf("dedupe order: got %v err %v", got, err)
	}
	if _, err := parseHarnessFlag("kimi"); err == nil {
		t.Fatal("unknown harness must error")
	}
}

func TestEnsureGrokHooksCreateReconcileIdempotent(t *testing.T) {
	repo := t.TempDir()
	added, updated, _, err := ensureGrokHooks(repo)
	if err != nil || !added || len(updated) != 0 {
		t.Fatalf("create: added=%v updated=%v err=%v", added, updated, err)
	}
	path := filepath.Join(repo, filepath.FromSlash(grokHooksRel))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		renderHookCommand(repo, "grok", "gate"),
		renderHookCommand(repo, "grok", "commitgate"),
		"PATH=$HOME/.local/bin:$PATH satelle hook prompt",
		"PATH=$HOME/.local/bin:$PATH satelle hook stopcheck",
		"UserPromptSubmit",
		"Stop",
		"search_replace",
		"run_terminal_command",
		"satelle reindex",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("scaffold missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(preToolUseCommands(string(body)), "$") {
		t.Errorf("PreToolUse must be $-free (sty_adfb9862):\n%s", body)
	}
	if strings.Contains(string(body), "|| exit 2") {
		t.Errorf("scaffold must not use bare '|| exit 2' (sty_c75c73ed):\n%s", body)
	}
	// Script bodies hold the multi-candidate fail-visible wrapper.
	script, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(hookScriptRel("grok", "gate"))))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{failVisibleMarker, "$HOME/.local/bin/satelle", "policy denial"} {
		if !strings.Contains(string(script), want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
	// Second pass: no create, no reconcile.
	added, updated, _, err = ensureGrokHooks(repo)
	if err != nil || added || len(updated) != 0 {
		t.Fatalf("idempotent: added=%v updated=%v err=%v", added, updated, err)
	}
	// Stale command in satelle-owned file is reconciled; foreign content kept.
	stale := `{
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "command", "command": "satelle index" } ] }
    ]
  },
  "custom": "keep-me"
}`
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	// Sibling user hook is never touched.
	userHook := filepath.Join(repo, ".grok", "hooks", "user-extra.json")
	if err := os.WriteFile(userHook, []byte(`{"mine":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The stale file is BOTH reconciled (satelle index -> reindex) AND healed with
	// the reinforcement hooks it lacks: rename + SessionStart/PreToolUse/
	// UserPromptSubmit/Stop (sty_949e8739 + sty_0699637c).
	added, updated, incomplete, err := ensureGrokHooks(repo)
	if err != nil || added || len(updated) == 0 {
		t.Fatalf("reconcile+heal: added=%v updated=%v err=%v", added, updated, err)
	}
	if len(incomplete) > 0 {
		t.Errorf("hooks still incomplete after heal: %v", incomplete)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), `"satelle index"`) || !strings.Contains(string(got), `"satelle reindex"`) {
		t.Errorf("stale not fixed:\n%s", got)
	}
	for _, want := range []string{
		`"keep-me"`,
		"satelle hook prompt",
		"satelle hook stopcheck",
		"satelle hook context",
		"satelle-hook.sh",
		"satelle-hook.sh",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("reconcile+heal lost/omitted %q:\n%s", want, got)
		}
	}
	userBody, _ := os.ReadFile(userHook)
	if string(userBody) != `{"mine":true}` {
		t.Errorf("sibling hook touched: %s", userBody)
	}
}

// TestBuildGrokHookSettingsPassesHarnessFlag pins AC2 (sty_719c4a7b): the
// grok scaffold satelle init writes passes --harness grok on SessionStart,
// UserPromptSubmit and Stop, alongside PreToolUse which already carries it
// through renderHookCommand's positional harness argument.
func TestBuildGrokHookSettingsPassesHarnessFlag(t *testing.T) {
	repo := t.TempDir()
	body := string(buildGrokHookSettings(repo))
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		t.Fatal(err)
	}
	hooks := root["hooks"].(map[string]any)
	for _, event := range []string{"UserPromptSubmit", "Stop"} {
		if !hookEventHasMarker(hooks[event], "--harness grok") {
			t.Errorf("%s command missing --harness grok:\n%s", event, body)
		}
	}
	// Context is installed on the resolved channel, not left on SessionStart
	// beside reindex when that channel is a different event.
	event := grokSessionContextEvent(repo)
	if event == "" {
		t.Fatal("grok session context event resolved empty")
	}
	if !hookEventHasMarker(hooks[event], "satelle hook context --harness grok") {
		t.Errorf("context command missing on resolved event %s:\n%s", event, body)
	}
	if event != "SessionStart" && hookEventHasMarker(hooks["SessionStart"], "satelle hook context") {
		t.Errorf("SessionStart must not also carry context when the channel is %s:\n%s", event, body)
	}
	// PreToolUse (gate + commitgate) already carries harness positionally.
	if !hookEventHasMarker(hooks["PreToolUse"], "grok") {
		t.Errorf("PreToolUse missing grok harness:\n%s", body)
	}
	// satelle reindex is not a hook subcommand and must not gain the flag.
	if hookEventHasMarker(hooks["SessionStart"], "reindex --harness") {
		t.Errorf("satelle reindex must not carry --harness:\n%s", body)
	}
}

func TestEnsureProcessHooksBoth(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Force both via dirs so we do not depend on the host PATH.
	for _, d := range []string{".claude", ".grok"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := ensureProcessHooks(&buf, repo, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, ".claude/settings.json") || !strings.Contains(out, grokHooksRel) {
		t.Errorf("report missing both harnesses:\n%s", out)
	}
	if !strings.Contains(out, "[compat.claude] hooks=false") {
		t.Errorf("report missing Grok compat.claude line:\n%s", out)
	}
	if !strings.Contains(out, "trusted_folders.toml") || !strings.Contains(out, "Grok project hooks trusted") {
		t.Errorf("report missing Grok folder trust line:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude", "settings.json")); err != nil {
		t.Errorf("claude hooks missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(grokHooksRel))); err != nil {
		t.Errorf("grok hooks missing: %v", err)
	}
	// Grok-detected path also lands compat.claude hooks=false under HOME.
	gcfg, err := os.ReadFile(filepath.Join(home, ".grok", "config.toml"))
	if err != nil {
		t.Fatalf("grok config not written: %v", err)
	}
	if !strings.Contains(string(gcfg), "[compat.claude]") || !strings.Contains(string(gcfg), "hooks = false") {
		t.Errorf("compat.claude hooks=false missing:\n%s", gcfg)
	}
	// Folder trust for this repo root only (sty_edb01f49).
	trust, err := os.ReadFile(filepath.Join(home, ".grok", "trusted_folders.toml"))
	if err != nil {
		t.Fatalf("trusted_folders not written: %v", err)
	}
	abs, _ := filepath.Abs(repo)
	if !strings.Contains(string(trust), abs) || !strings.Contains(string(trust), "trusted = true") {
		t.Errorf("repo not trusted:\n%s", trust)
	}
	// Re-run: trust line silent (already trusted), hooks still present.
	var buf2 bytes.Buffer
	if err := ensureProcessHooks(&buf2, repo, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf2.String(), "Grok project hooks trusted") {
		t.Errorf("idempotent re-init should not re-report trust:\n%s", buf2.String())
	}
}

// TestEnsureGrokCompatConfigCreatesMissing covers AC1/AC3/AC4: absent
// ~/.grok/config.toml is created with [compat.claude] hooks = false and reported.
func TestEnsureGrokCompatConfigCreatesMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var buf bytes.Buffer
	if err := ensureGrokCompatConfig(&buf); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".grok", "config.toml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not created: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "[compat.claude]") || !strings.Contains(s, "hooks = false") {
		t.Errorf("expected [compat.claude] hooks=false:\n%s", s)
	}
	// Must not disable other compat facets.
	for _, ban := range []string{"skills", "rules", "agents", "mcps"} {
		if strings.Contains(s, ban) {
			t.Errorf("must not set %s; got:\n%s", ban, s)
		}
	}
	if !strings.Contains(buf.String(), "  + ") || !strings.Contains(buf.String(), "[compat.claude] hooks=false") {
		t.Errorf("create report missing:\n%s", buf.String())
	}
}

// TestEnsureGrokCompatConfigUpsertPreservesOtherKeys: surgical upsert keeps
// unrelated tables/keys and only sets hooks under [compat.claude].
func TestEnsureGrokCompatConfigUpsertPreservesOtherKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".grok", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `# keep me
[cli]
installer = "internal"

[compat.claude]
skills = true
hooks = true

[compat.cursor]
hooks = true
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := ensureGrokCompatConfig(&buf); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)
	for _, keep := range []string{
		"# keep me",
		`[cli]`,
		`installer = "internal"`,
		`skills = true`,
		`[compat.cursor]`,
		`hooks = true`, // cursor still true
	} {
		if !strings.Contains(s, keep) {
			t.Errorf("lost %q:\n%s", keep, s)
		}
	}
	// claude hooks flipped to false (the assignment under [compat.claude]).
	if !strings.Contains(s, "hooks = false") {
		t.Errorf("hooks not set false:\n%s", s)
	}
	// skills must remain true (not flipped).
	if !strings.Contains(s, "skills = true") {
		t.Errorf("skills clobbered:\n%s", s)
	}
	if !strings.Contains(buf.String(), "  ~ ") || !strings.Contains(buf.String(), "[compat.claude] hooks=false") {
		t.Errorf("update report missing:\n%s", buf.String())
	}
}

// TestEnsureGrokCompatConfigAlreadyFalseIdempotent: no write, no report.
func TestEnsureGrokCompatConfigAlreadyFalseIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".grok", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "[compat.claude]\nhooks = false\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := ensureGrokCompatConfig(&buf); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Errorf("file rewritten when already false:\n%s", after)
	}
	if buf.Len() != 0 {
		t.Errorf("idempotent pass must not report, got %q", buf.String())
	}
}

// TestGrokNotDetectedSkipsCompatConfig: Claude-only (no Grok signal) never
// touches ~/.grok/config.toml for this purpose (AC2/AC4).
func TestGrokNotDetectedSkipsCompatConfig(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Claude-only via existing .claude dir; no .grok → PATH is ignored entirely.
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := ensureProcessHooks(&buf, repo, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".grok", "config.toml")); err == nil {
		t.Errorf("compat config written without Grok detection:\n%s", buf.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".grok", "trusted_folders.toml")); err == nil {
		t.Errorf("folder trust written without Grok detection:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "compat.claude") {
		t.Errorf("report mentions compat.claude without Grok:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "trusted_folders") {
		t.Errorf("report mentions trusted_folders without Grok:\n%s", buf.String())
	}
}

// TestHookScaffoldFailVisible asserts PreToolUse gate/commitgate use the
// $-free script-file command (sty_adfb9862) and that script bodies carry the
// multi-candidate fail-visible wrapper (sty_c75c73ed). SessionStart stays simple.
func TestHookScaffoldFailVisible(t *testing.T) {
	repo := t.TempDir()
	if err := writeHookScripts(repo); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"claude": string(buildClaudeHookSettings(repo)),
		"grok":   string(buildGrokHookSettings(repo)),
	} {
		// AC1: harness command strings contain NO $ variable references.
		if strings.Contains(body, "$") {
			// SessionStart/prompt may still use $HOME in PATH=… for prompt/stopcheck
			// — only PreToolUse gate/commitgate must be $-free. Extract PreToolUse.
			if strings.Contains(preToolUseCommands(body), "$") {
				t.Errorf("%s PreToolUse command still has $ tokens:\n%s", name, body)
			}
		}
		if !strings.Contains(body, "satelle-hook.sh gate "+name) {
			t.Errorf("%s missing gate script command:\n%s", name, body)
		}
		if !strings.Contains(body, "satelle-hook.sh commitgate "+name) {
			t.Errorf("%s missing commitgate script command:\n%s", name, body)
		}
		if strings.Contains(body, "|| exit 2") {
			t.Errorf("%s must not use bare '|| exit 2':\n%s", name, body)
		}
		if strings.Contains(body, "sh -c ") {
			t.Errorf("%s must not use inline sh -c wrapper:\n%s", name, body)
		}
		// Script body holds marker + multi-candidate probe (not settings.json).
		script := filepath.Join(repo, filepath.FromSlash(hookScriptRel(name, "gate")))
		sb, err := os.ReadFile(script)
		if err != nil {
			t.Fatalf("%s script: %v", name, err)
		}
		for _, want := range []string{failVisibleMarker, "$HOME/.local/bin/satelle", ".satelle/satelle", "policy denial"} {
			if !strings.Contains(string(sb), want) {
				t.Errorf("%s script missing %q:\n%s", name, want, sb)
			}
		}
		// SessionStart stays bare.
		if strings.Contains(body, "PATH=$HOME/.local/bin:$PATH satelle reindex") {
			t.Errorf("%s SessionStart reindex should not be PATH-prefixed", name)
		}
	}
}

// preToolUseCommands returns a best-effort concat of PreToolUse command strings
// from a settings JSON body (test helper).
func preToolUseCommands(body string) string {
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return body
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		return ""
	}
	pre, _ := hooks["PreToolUse"].([]any)
	var b strings.Builder
	for _, g := range pre {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			fmt.Fprintln(&b, hm["command"])
		}
	}
	return b.String()
}

// TestFailVisibleWrapperShell drives the generated wrapper with no resolvable
// binary. Every harness receives a structured infrastructure deny with a
// successful handler exit; commitgate still fails open for irrelevant Bash.
func TestFailVisibleWrapperShell(t *testing.T) {
	repo := t.TempDir()
	if err := writeHookScripts(repo); err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{"claude", "grok"} {
		full := renderHookCommand(repo, harness, "gate")
		if strings.Contains(full, "$") || strings.HasPrefix(full, "sh -c ") {
			t.Fatalf("command must be $-free script form: %s", full)
		}
		code, got, stderr := runHookScript(t, repo, full,
			hookEditEvent(harness), t.TempDir())
		if code != 0 {
			t.Fatalf("%s gate with no binary: exit=%d stdout=%q stderr=%q",
				harness, code, got, stderr)
		}
		if !strings.Contains(got, "policy denial") {
			t.Errorf("%s gate infra deny missing reason: stdout=%q stderr=%q", harness, got, stderr)
		}
		topLevel := harness == "grok"
		if !topLevel && !strings.Contains(got, "hookSpecificOutput") {
			t.Errorf("%s want Claude shape: %q", harness, got)
		}
		if topLevel && !strings.Contains(got, `"decision":"deny"`) {
			t.Errorf("%s want top-level decision/reason shape: %q", harness, got)
		}
		assertHookDenyReason(t, harness, got)
	}

	for _, harness := range []string{"claude", "grok"} {
		full := renderHookCommand(repo, harness, "commitgate")
		code, stdout, stderr := runHookScript(t, repo, full,
			hookBashEvent(harness, "echo hello"), t.TempDir())
		if code != 0 || stdout != "" || stderr != "" {
			t.Errorf("%s commitgate echo hello = exit %d stdout=%q stderr=%q; want silent allow",
				harness, code, stdout, stderr)
		}
		code, stdout, stderr = runHookScript(t, repo, full,
			hookBashEvent(harness, "git commit -m x"), t.TempDir())
		if code != 0 {
			t.Errorf("%s commitgate git commit handler exit=%d stderr=%q", harness, code, stderr)
		}
		if !strings.Contains(stdout, "policy denial") {
			t.Errorf("%s commit deny missing reason: %q", harness, stdout)
		}
		assertHookDenyReason(t, harness, stdout)
	}
}

// TestFailVisibleWrapperShellBinaryPresent covers policy deny, malformed/empty
// failures, secret-bearing stderr, and silent allow for all harness shapes.
func TestFailVisibleWrapperShellBinaryPresent(t *testing.T) {
	repo := t.TempDir()
	if err := writeHookScripts(repo); err != nil {
		t.Fatal(err)
	}
	stubDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(stubDir, "satelle")
	body := `#!/bin/sh
case "$4" in
  grok) printf '%s\n' '{"decision":"deny","reason":"stub policy denial"}' ;;
  *) printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"stub policy denial"}}' ;;
esac
printf '%s\n' 'TOKEN=must-not-leak' >&2
exit 1
`
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	for _, harness := range []string{"claude", "grok"} {
		for _, sub := range []string{"gate", "commitgate"} {
			event := hookEditEvent(harness)
			if sub == "commitgate" {
				event = hookBashEvent(harness, "git push")
			}
			code, stdout, stderr := runHookScript(t, repo,
				renderHookCommand(repo, harness, sub), event, home)
			if code != 0 {
				t.Fatalf("%s %s structured deny: exit=%d stdout=%q stderr=%q",
					harness, sub, code, stdout, stderr)
			}
			if !strings.Contains(stdout, "stub policy denial") {
				t.Errorf("%s %s missing stub reason: %q", harness, sub, stdout)
			}
			assertHookDenyReason(t, harness, stdout)
			if strings.Contains(stdout+stderr, "TOKEN=must-not-leak") {
				t.Errorf("%s %s leaked captured stderr: stdout=%q stderr=%q",
					harness, sub, stdout, stderr)
			}
		}
	}

	for _, failure := range []string{
		"#!/bin/sh\nprintf '%s\\n' 'TOKEN=must-not-leak' >&2\nexit 1\n",
		"#!/bin/sh\nprintf '%s\\n' 'not-json'\nprintf '%s\\n' 'TOKEN=must-not-leak' >&2\nexit 1\n",
		"#!/bin/sh\nprintf '%s\\n' 'not-json'\nexit 0\n",
	} {
		if err := os.WriteFile(stub, []byte(failure), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, harness := range []string{"claude", "grok"} {
			code, stdout, stderr := runHookScript(t, repo,
				renderHookCommand(repo, harness, "gate"),
				hookEditEvent(harness), home)
			if code != 0 || !strings.Contains(stdout, "INFRASTRUCTURE failure") {
				t.Errorf("%s malformed failure = exit %d stdout=%q stderr=%q",
					harness, code, stdout, stderr)
			}
			if strings.Contains(stdout+stderr, "TOKEN=must-not-leak") || strings.Contains(stdout, "not-json") {
				t.Errorf("%s malformed failure leaked unsafe output: stdout=%q stderr=%q",
					harness, stdout, stderr)
			}
			assertHookDenyReason(t, harness, stdout)
		}
	}

	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{"claude", "grok"} {
		for _, sub := range []string{"gate", "commitgate"} {
			event := hookEditEvent(harness)
			if sub == "commitgate" {
				event = hookBashEvent(harness, "echo hello")
			}
			code, stdout, stderr := runHookScript(t, repo,
				renderHookCommand(repo, harness, sub), event, home)
			if code != 0 || stdout != "" || stderr != "" {
				t.Errorf("%s %s allow = exit %d stdout=%q stderr=%q",
					harness, sub, code, stdout, stderr)
			}
		}
	}
}

// TestFailVisibleWrapperRegression reproduces the old mixed contract exactly:
// deny JSON on stdout, blocking exit 2, and an empty stderr channel.
func TestFailVisibleWrapperRegression(t *testing.T) {
	repo := t.TempDir()
	stub := filepath.Join(repo, "satelle")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' '{\"hookSpecificOutput\":{\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"actionable\"}}'\nprintf '%s\\n' 'actionable' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(repo, "old.sh")
	if err := os.WriteFile(old, []byte("#!/bin/sh\no=$(\"$1\" 2>/dev/null); code=$?\n[ -n \"$o\" ] && printf '%s\\n' \"$o\"\n[ \"$code\" -eq 0 ] && exit 0\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", old, stub)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 || stdout.Len() == 0 || stderr.Len() != 0 {
		t.Fatalf("old wrapper did not reproduce exit-2/empty-stderr: err=%v stdout=%q stderr=%q",
			err, stdout.String(), stderr.String())
	}
}

func runHookScript(t *testing.T, repo, command, event, home string) (int, string, string) {
	t.Helper()
	c := exec.Command("sh", "-c", command)
	c.Dir = repo
	c.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	c.Stdin = strings.NewReader(event)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), stdout.String(), stderr.String()
	}
	t.Fatalf("run hook: %v", err)
	return -1, stdout.String(), stderr.String()
}

func hookEditEvent(harness string) string {
	if harness == "grok" {
		return `{"toolInput":{"filePath":"x.go"}}`
	}
	return `{"tool_input":{"file_path":"x.go"}}`
}

func hookBashEvent(harness, command string) string {
	key := "tool_input"
	field := "command"
	if harness == "grok" {
		key = "toolInput"
		field = "command"
	}
	b, _ := json.Marshal(map[string]any{key: map[string]string{field: command}})
	return string(b)
}

func assertHookDenyReason(t *testing.T, harness, stdout string) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil {
		t.Fatalf("%s deny is not JSON: %v: %q", harness, err, stdout)
	}
	if harness == "grok" {
		if doc["decision"] != "deny" {
			t.Fatalf("%s decision=%v, want deny", harness, doc["decision"])
		}
		if reason, _ := doc["reason"].(string); strings.TrimSpace(reason) == "" {
			t.Fatalf("%s deny has empty reason: %q", harness, stdout)
		}
		return
	}
	hso, _ := doc["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] != "deny" {
		t.Fatalf("%s permissionDecision=%v, want deny", harness, hso["permissionDecision"])
	}
	if reason, _ := hso["permissionDecisionReason"].(string); strings.TrimSpace(reason) == "" {
		t.Fatalf("%s deny has empty permissionDecisionReason: %q", harness, stdout)
	}
}

// TestWriteHookScriptsRetiresLegacyAndKimi: AC5 file-retirement half —
// writeHookScripts removes per-harness and kimi residue; second call is a no-op.
func TestWriteHookScriptsRetiresLegacyAndKimi(t *testing.T) {
	repo := t.TempDir()
	// Plant legacy pretooluse scripts + kimi residue.
	legacy := []string{
		".satelle/hooks/pretooluse-gate-claude.sh",
		".satelle/hooks/pretooluse-commitgate-claude.sh",
		".satelle/hooks/pretooluse-gate-grok.sh",
		".satelle/hooks/pretooluse-commitgate-grok.sh",
		".satelle/hooks/pretooluse-gate-kimi.sh",
		".satelle/hooks/pretooluse-commitgate-kimi.sh",
		".satelle/hooks/stop-kimi.sh",
		".satelle/bin/kimi-argv.sh",
		".satelle/kimi/config.toml",
	}
	for _, rel := range legacy {
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("legacy\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeHookScripts(repo); err != nil {
		t.Fatal(err)
	}
	// Canonical single script present.
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(satelleHookScriptRel))); err != nil {
		t.Fatalf("parameterized script missing: %v", err)
	}
	for _, rel := range legacy {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(rel))); err == nil {
			t.Errorf("legacy path still present after writeHookScripts: %s", rel)
		}
	}
	// Idempotent second pass: script body unchanged, still no legacy.
	path := filepath.Join(repo, filepath.FromSlash(satelleHookScriptRel))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeHookScripts(repo); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("second writeHookScripts must not rewrite clean script body")
	}
	for _, rel := range legacy {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(rel))); err == nil {
			t.Errorf("legacy reappeared after second pass: %s", rel)
		}
	}
}

// TestUpgradeFailVisibleHooks heals a legacy bare-exit-2 scaffold on re-init.
func TestUpgradeFailVisibleHooks(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Edit|Write",
        "hooks": [
          { "type": "command", "command": "PATH=$HOME/.local/bin:$PATH satelle hook gate || exit 2" }
        ]
      },
      {
        "matcher": "Bash",
        "hooks": [
          { "type": "command", "command": "PATH=$HOME/.local/bin:$PATH satelle hook commitgate || exit 2" }
        ]
      }
    ]
  }
}
`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	added, updated, _, err := ensureClaudeHooks(repo)
	if err != nil || added {
		t.Fatalf("heal: added=%v err=%v", added, err)
	}
	if len(updated) == 0 {
		t.Fatalf("expected fail-visible upgrade notes, got %v", updated)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), renderHookCommand(repo, "claude", "gate")) {
		t.Fatalf("upgrade missing script-form command:\n%s", body)
	}
	if strings.Contains(string(body), "|| exit 2") {
		t.Fatalf("legacy || exit 2 still present:\n%s", body)
	}
	if strings.Contains(preToolUseCommands(string(body)), "$") {
		t.Fatalf("PreToolUse still has $ after upgrade:\n%s", body)
	}
	// Script files materialised.
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(hookScriptRel("claude", "gate")))); err != nil {
		t.Fatalf("gate script not written: %v", err)
	}
	// Idempotent second pass.
	_, updated2, _, err := ensureClaudeHooks(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range updated2 {
		if strings.Contains(u, "script-file") || strings.Contains(u, "fail-visible") {
			t.Fatalf("second pass should not re-upgrade: %v", updated2)
		}
	}
}

// TestUpgradeInlineFailVisibleToScript heals the sty_c75c73ed inline sh -c
// wrapper (with $ tokens) to the sty_adfb9862 script-file form.
func TestUpgradeInlineFailVisibleToScript(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Minimal inline form (marker + $ + hook gate) as deployed pre-sty_adfb9862.
	inline := `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Edit|Write",
        "hooks": [
          { "type": "command", "command": "sh -c '#satelle-failvisible\nb=\"\"; for c in \"$HOME/.local/bin/satelle\"; do :; done; satelle hook gate'" }
        ]
      },
      {
        "matcher": "Bash",
        "hooks": [
          { "type": "command", "command": "sh -c '#satelle-failvisible\nsatelle hook commitgate'" }
        ]
      }
    ]
  }
}
`
	if err := os.WriteFile(path, []byte(inline), 0o644); err != nil {
		t.Fatal(err)
	}
	_, updated, _, err := ensureClaudeHooks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) == 0 {
		t.Fatalf("expected script-file upgrade notes, got %v", updated)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "sh -c ") {
		t.Fatalf("inline sh -c still present:\n%s", body)
	}
	if strings.Contains(preToolUseCommands(string(body)), "$") {
		t.Fatalf("PreToolUse still has $:\n%s", body)
	}
	for _, sub := range []string{"gate", "commitgate"} {
		want := renderHookCommand(repo, "claude", sub)
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

// TestReinforceSessionStartAndPreToolUse (sty_0699637c): partial settings
// missing SessionStart+PreToolUse gains both on re-heal without clobbering
// existing entries.
func TestReinforceSessionStartAndPreToolUse(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Partial: only UserPromptSubmit (the pre-sty_0699637c reinforcement set).
	partial := `{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [ { "type": "command", "command": "PATH=$HOME/.local/bin:$PATH satelle hook prompt" } ] }
    ]
  },
  "custom": true
}
`
	if err := os.WriteFile(path, []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}
	created, updated, incomplete, err := ensureClaudeHooks(repo)
	if err != nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if len(updated) == 0 {
		t.Fatalf("expected SessionStart/PreToolUse/Stop adds, got %v", updated)
	}
	if len(incomplete) > 0 {
		t.Errorf("still incomplete: %v", incomplete)
	}
	body, _ := os.ReadFile(path)
	s := string(body)
	for _, want := range []string{
		`"custom": true`,
		"satelle hook prompt",
		"satelle hook stopcheck",
		"satelle hook context",
		"satelle-hook.sh",
		"satelle-hook.sh",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
}

// TestReinforceWarnsOnUnparseableSettings (sty_0699637c AC5 negative path): a
// non-object JSON root cannot be reinforced; incomplete reports unparseable and
// ensureProcessHooks emits a WARN naming it.
func TestReinforceWarnsOnUnparseableSettings(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Array root: Unmarshal into map[string]any fails → left untouched.
	if err := os.WriteFile(path, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, updated, incomplete, err := ensureClaudeHooks(repo)
	if err != nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if len(updated) != 0 {
		t.Fatalf("unparseable file must not be rewritten: updated=%v", updated)
	}
	if len(incomplete) == 0 {
		t.Fatal("expected incomplete non-empty for unparseable settings")
	}
	joined := strings.Join(incomplete, ",")
	if !strings.Contains(joined, "unparseable") && !strings.Contains(joined, "SessionStart") {
		t.Fatalf("incomplete = %v, want unparseable or missing events", incomplete)
	}
	// Operator-visible WARN via ensureProcessHooks.
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Force Claude-only detection.
	_ = os.MkdirAll(filepath.Join(repo, ".claude"), 0o755)
	var buf bytes.Buffer
	if err := ensureProcessHooks(&buf, repo, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "incomplete satelle hooks") {
		t.Fatalf("want incomplete WARN, got:\n%s", out)
	}
}

// TestRenderHookCommandAbsoluteCwdSafe: PreToolUse commands use an absolute
// script path (no "$") so a drifted shell cwd cannot fail open the script.
func TestRenderHookCommandAbsoluteCwdSafe(t *testing.T) {
	repo := t.TempDir()
	cmd := renderHookCommand(repo, "claude", "commitgate")
	if strings.Contains(cmd, "$") {
		t.Fatalf("PreToolUse command must be $-free: %q", cmd)
	}
	if !strings.HasPrefix(cmd, "sh /") && !strings.HasPrefix(cmd, "sh ") {
		t.Fatalf("want sh <path> …, got %q", cmd)
	}
	// Absolute: after "sh " the next token must be absolute.
	parts := strings.Fields(cmd)
	if len(parts) < 4 {
		t.Fatalf("want sh <script> <sub> <harness>, got %q", cmd)
	}
	if !filepath.IsAbs(parts[1]) {
		t.Fatalf("script path must be absolute, got %q", parts[1])
	}
	if !strings.HasSuffix(parts[1], satelleHookScriptRel) && !strings.HasSuffix(parts[1], "satelle-hook.sh") {
		t.Fatalf("script path must end with satelle-hook.sh, got %q", parts[1])
	}
	// Body must probe CLAUDE_PROJECT_DIR for binary resolution.
	body := parameterizedHookScriptBody()
	if !strings.Contains(body, "CLAUDE_PROJECT_DIR") || !strings.Contains(body, "SATELLE_PROJECT_DIR") {
		t.Fatalf("wrapper body must probe project-dir env pins:\n%s", body)
	}
}

// TestAbsoluteHookCommandFromSubdirCwd (AC5): the absolute script path from
// renderHookCommand remains openable when the process cwd is a nested subdir.
func TestAbsoluteHookCommandFromSubdirCwd(t *testing.T) {
	repo := t.TempDir()
	if err := writeHookScripts(repo); err != nil {
		t.Fatal(err)
	}
	cmdLine := renderHookCommand(repo, "claude", "commitgate")
	parts := strings.Fields(cmdLine)
	if len(parts) < 4 || !filepath.IsAbs(parts[1]) {
		t.Fatalf("need absolute script command, got %q", cmdLine)
	}
	sub := filepath.Join(repo, "nested", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Relative form fails from subdir (the live defect).
	rel := exec.Command("sh", satelleHookScriptRel, "commitgate", "claude")
	rel.Dir = sub
	rel.Stdin = strings.NewReader(`{"tool_input":{"command":"echo ok"}}`)
	if out, err := rel.CombinedOutput(); err == nil {
		t.Fatalf("relative script from subdir should fail to open; out=%s", out)
	}
	// Absolute form succeeds (script openable; exit 0 for non-mutating fail-open path
	// when satelle binary may be missing — either 0 or structured infra is fine as
	// long as the script file itself was found, i.e. not "No such file").
	abs := exec.Command(parts[0], parts[1:]...)
	abs.Dir = sub
	abs.Stdin = strings.NewReader(`{"tool_input":{"command":"echo ok"}}`)
	out, err := abs.CombinedOutput()
	if strings.Contains(string(out), "No such file") || (err != nil && strings.Contains(err.Error(), "no such file")) {
		t.Fatalf("absolute script must be openable from subdir: err=%v out=%s", err, out)
	}
}

// TestUpgradeRelativeScriptFormToAbsolute (AC2): relative
// `sh .satelle/hooks/satelle-hook.sh …` is rewritten to the absolute form for repoRoot.
func TestUpgradeRelativeScriptFormToAbsolute(t *testing.T) {
	repo := t.TempDir()
	if err := writeHookScripts(repo); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repo, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	// Seed the LEGACY relative form (the form that bricks after cwd drift).
	legacy := `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Edit|Write",
        "hooks": [{"type": "command", "command": "sh .satelle/hooks/satelle-hook.sh gate claude"}]
      },
      {
        "matcher": "Bash",
        "hooks": [{"type": "command", "command": "sh .satelle/hooks/satelle-hook.sh commitgate claude"}]
      }
    ]
  }
}
`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := upgradeFailVisibleHooks(path, "claude", repo)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("expected at least one relative→absolute rewrite, got n=%d", n)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantGate := renderHookCommand(repo, "claude", "gate")
	wantCommit := renderHookCommand(repo, "claude", "commitgate")
	s := string(body)
	if !strings.Contains(s, wantGate) || !strings.Contains(s, wantCommit) {
		t.Fatalf("relative form not upgraded to absolute:\nwant %q and %q\ngot:\n%s", wantGate, wantCommit, s)
	}
	if strings.Contains(s, `"sh .satelle/hooks/satelle-hook.sh`) {
		t.Fatalf("legacy relative form still present:\n%s", s)
	}
	// Idempotent second pass.
	n2, err := upgradeFailVisibleHooks(path, "claude", repo)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("second pass must be idempotent, n=%d", n2)
	}
}

// A repo session_context_event override moves the installed context command
// for both a fresh scaffold and a heal of an older file. The install target
// is the resolved event, not a compiled event name.
func TestGrokContextHookFollowsSessionContextEvent(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, config.DefaultDataDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, config.DefaultDataDir, config.ConfigName), []byte(`
[harness.grok]
session_context_event = "Stop"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	body := string(buildGrokHookSettings(repo))
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		t.Fatal(err)
	}
	hooks := root["hooks"].(map[string]any)
	if !hookEventHasMarker(hooks["Stop"], "satelle hook context --harness grok") {
		t.Fatalf("fresh scaffold did not install context on Stop:\n%s", body)
	}
	if hookEventHasMarker(hooks["PostToolUse"], "satelle hook context") || hookEventHasMarker(hooks["SessionStart"], "satelle hook context") {
		t.Fatalf("fresh scaffold left context off Stop:\n%s", body)
	}
	if !hookEventHasMarker(hooks["SessionStart"], "satelle reindex") {
		t.Fatalf("reindex must stay on SessionStart:\n%s", body)
	}

	path := filepath.Join(repo, filepath.FromSlash(grokHooksRel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	older := `{
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "command", "command": "satelle reindex" }, { "type": "command", "command": "satelle hook context" } ] }
    ],
    "Stop": [
      { "hooks": [ { "type": "command", "command": "PATH=$HOME/.local/bin:$PATH satelle hook stopcheck" } ] }
    ]
  }
}
`
	if err := os.WriteFile(path, []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := healExistingHookFile(path, "grok", repo); err != nil {
		t.Fatal(err)
	}
	healed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var hroot map[string]any
	if err := json.Unmarshal(healed, &hroot); err != nil {
		t.Fatal(err)
	}
	hhooks := hroot["hooks"].(map[string]any)
	if !hookEventHasMarker(hhooks["Stop"], "satelle hook context --harness grok") {
		t.Fatalf("heal did not install context on Stop:\n%s", healed)
	}
	if hookEventHasMarker(hhooks["PostToolUse"], "satelle hook context") || hookEventHasMarker(hhooks["SessionStart"], "satelle hook context") {
		t.Fatalf("heal left context off Stop:\n%s", healed)
	}
	if !hookEventHasMarker(hhooks["SessionStart"], "satelle reindex") {
		t.Fatalf("heal dropped reindex:\n%s", healed)
	}
}
