package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
)

// ScaffoldFinding is one deployed-vs-canonical harness scaffold mismatch
// (sty_ac25b787). Paths are repo-relative; no absolute machine paths.
type ScaffoldFinding struct {
	Path   string // repo-relative path of the drifted artifact
	Kind   string // missing | content | command | timeout | config
	Detail string
}

// DetectScaffoldDrift compares deployed harness hook configs and
// .satelle/hooks scripts against this binary's canonical wrappers, and reports
// any removed [sync] scope key in .satelle/satelle.toml or satelle.local.toml —
// and any removed budget key or table (sty_58a7bdfe) — all healed by satelle
// init. Empty when nothing needs healing. Repo-agnostic:
// only well-known relative paths (settings, script and config names) are examined.
func DetectScaffoldDrift(repoRoot string) []ScaffoldFinding {
	if strings.TrimSpace(repoRoot) == "" {
		return nil
	}
	var findings []ScaffoldFinding
	// Settings-driven checks (only when the harness file exists).
	findings = append(findings, driftHarnessSettings(repoRoot, filepath.Join(repoRoot, ".claude", "settings.json"), "claude", ".claude/settings.json")...)
	findings = append(findings, driftHarnessSettings(repoRoot, filepath.Join(repoRoot, filepath.FromSlash(grokHooksRel)), "grok", grokHooksRel)...)
	// Single parameterized script must match canonical when present.
	rel := satelleHookScriptRel
	path := filepath.Join(repoRoot, filepath.FromSlash(rel))
	if b, err := os.ReadFile(path); err == nil {
		want := parameterizedHookScriptBody()
		if string(b) != want {
			findings = append(findings, ScaffoldFinding{
				Path:   rel,
				Kind:   "content",
				Detail: fmt.Sprintf("differs from binary canonical wrapper (sha %s vs want %s)", shortSHA(b), shortSHA([]byte(want))),
			})
		}
	}
	// Grok's SessionStart/UserPromptSubmit/Stop hooks must each name --harness
	// grok explicitly (sty_719c4a7b AC2/AC7) — PreToolUse already carries it via
	// renderHookCommand's positional arg, checked above through driftHarnessSettings.
	findings = append(findings, driftGrokHarnessFlag(repoRoot)...)
	findings = append(findings, driftPiExtension(repoRoot)...)
	findings = append(findings, driftCursorHooks(repoRoot)...)
	findings = append(findings, driftSyncScopeKeys(repoRoot)...)
	findings = append(findings, driftRemovedBudget(repoRoot)...)
	findings = append(findings, driftForeignSeatTools(repoRoot)...)
	// Legacy per-harness scripts on disk are drift (should have been retired).
	for _, harness := range []string{"claude", "grok", "kimi"} {
		for _, sub := range []string{"gate", "commitgate"} {
			lrel := legacyHookScriptRel(harness, sub)
			if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(lrel))); err == nil {
				findings = append(findings, ScaffoldFinding{
					Path:   lrel,
					Kind:   "content",
					Detail: "legacy per-harness wrapper present — run satelle init to retire (use " + satelleHookScriptRel + ")",
				})
			}
		}
	}
	return dedupeScaffoldFindings(findings)
}

// driftGrokHarnessFlag reports a deployed .grok/hooks/satelle.json whose
// SessionStart, UserPromptSubmit or Stop satelle hook command omits --harness
// grok (sty_719c4a7b AC7). Skips silently when the file is absent, unparseable,
// or that event's satelle hook isn't installed at all — those are other
// findings' concern, not this one's.
func driftGrokHarnessFlag(repoRoot string) []ScaffoldFinding {
	raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(grokHooksRel)))
	if err != nil {
		return nil
	}
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	hooks, _ := root["hooks"].(map[string]any)
	checks := []struct{ event, marker string }{
		{"SessionStart", "satelle hook context"},
		{"UserPromptSubmit", "satelle hook prompt"},
		{"Stop", "satelle hook stopcheck"},
	}
	var findings []ScaffoldFinding
	for _, c := range checks {
		ev := hooks[c.event]
		if !hookEventHasMarker(ev, c.marker) || hookEventHasMarker(ev, "--harness grok") {
			continue
		}
		findings = append(findings, ScaffoldFinding{
			Path:   grokHooksRel,
			Kind:   "command",
			Detail: fmt.Sprintf("%s command is missing --harness grok", c.event),
		})
	}
	// The resolved channel's context command, when that event is not already
	// covered by the SessionStart row above. A missing command is not this
	// row's finding — heal and init install it.
	if event := grokSessionContextEvent(repoRoot); event != "" && event != "SessionStart" {
		if detail, ok := contextCommandMissingHarness(hooks[event], event); ok {
			findings = append(findings, ScaffoldFinding{
				Path: grokHooksRel, Kind: "command", Detail: detail,
			})
		}
	}
	return findings
}

// contextCommandMissingHarness reports a satelle hook context command on event
// that lacks --harness grok. ok is false when that command is not installed.
func contextCommandMissingHarness(event any, name string) (string, bool) {
	groups, ok := event.([]any)
	if !ok {
		return "", false
	}
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		hs, ok := gm["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hs {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			cmd, _ := hm["command"].(string)
			if !strings.Contains(cmd, "satelle hook context") {
				continue
			}
			if strings.Contains(cmd, "--harness grok") {
				return "", false
			}
			return fmt.Sprintf("%s command is missing --harness grok", name), true
		}
	}
	return "", false
}

// driftPiExtension reports a deployed, satelle-owned pi extension that no longer
// matches what this binary would write for repoRoot (sty_b3c7b37d) — the binary
// changed, the repo moved (the wrapper path is absolute), or the file was edited —
// and a missing wrapper it calls. An absent extension means pi is not deployed,
// and one without the owned marker is the operator's; neither is drift.
func driftPiExtension(repoRoot string) []ScaffoldFinding {
	raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(piExtensionRel)))
	if err != nil || !strings.Contains(string(raw), piOwnedMarker) {
		return nil
	}
	var findings []ScaffoldFinding
	if want := buildPiExtension(repoRoot); string(raw) != string(want) {
		findings = append(findings, ScaffoldFinding{
			Path:   piExtensionRel,
			Kind:   "content",
			Detail: fmt.Sprintf("differs from binary canonical pi extension (sha %s vs want %s)", shortSHA(raw), shortSHA(want)),
		})
	}
	if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(satelleHookScriptRel))); err != nil {
		findings = append(findings, ScaffoldFinding{
			Path:   satelleHookScriptRel,
			Kind:   "missing",
			Detail: "canonical wrapper script missing — run satelle init",
		})
	}
	return findings
}

func driftHarnessSettings(repoRoot, absPath, harness, relPath string) []ScaffoldFinding {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return nil // harness not deployed — skip
	}
	cmds := preToolUseHookCommands(raw)
	if len(cmds) == 0 {
		return nil
	}
	var findings []ScaffoldFinding
	seenSub := map[string]bool{}
	for _, cmd := range cmds {
		sub := legacyHookSub(cmd)
		if sub == "" {
			continue
		}
		seenSub[sub] = true
		wantCmd := renderHookCommand(repoRoot, harness, sub)
		if strings.TrimSpace(cmd) != wantCmd {
			findings = append(findings, ScaffoldFinding{
				Path:   relPath,
				Kind:   "command",
				Detail: fmt.Sprintf("PreToolUse %s command is not the canonical script form (want %q)", sub, wantCmd),
			})
		}
		// Parameterized script must exist and match when this harness is deployed.
		rel := satelleHookScriptRel
		path := filepath.Join(repoRoot, filepath.FromSlash(rel))
		b, err := os.ReadFile(path)
		if err != nil {
			findings = append(findings, ScaffoldFinding{
				Path:   rel,
				Kind:   "missing",
				Detail: "canonical wrapper script missing — run satelle init",
			})
			continue
		}
		want := parameterizedHookScriptBody()
		if string(b) != want {
			findings = append(findings, ScaffoldFinding{
				Path:   rel,
				Kind:   "content",
				Detail: fmt.Sprintf("differs from binary canonical wrapper (sha %s vs want %s)", shortSHA(b), shortSHA([]byte(want))),
			})
		}
	}
	_ = seenSub
	return findings
}

// preToolUseHookCommands extracts command strings from PreToolUse hooks in a
// settings JSON document. Parse failure yields nil (no confident findings).
func preToolUseHookCommands(raw []byte) []string {
	var s struct {
		Hooks struct {
			PreToolUse []struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	var out []string
	for _, e := range s.Hooks.PreToolUse {
		for _, h := range e.Hooks {
			if c := strings.TrimSpace(h.Command); c != "" {
				out = append(out, c)
			}
		}
	}
	return out
}

func shortSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// driftSyncScopeKeys reports each removed [sync] scope key the repo's config
// files still carry: such a repo fails to load until satelle init removes it. A
// file that cannot be read or parsed is another finding's concern.
func driftSyncScopeKeys(repoRoot string) []ScaffoldFinding {
	found, err := config.FindSyncScopeKeys(filepath.Join(repoRoot, config.DefaultDataDir))
	if err != nil {
		return nil
	}
	var out []ScaffoldFinding
	for _, name := range []string{config.ConfigName, config.LocalConfigName} {
		for _, k := range found[name] {
			out = append(out, ScaffoldFinding{
				Path:   config.DefaultDataDir + "/" + name,
				Kind:   "config",
				Detail: "[sync] " + k + " is removed — run satelle init",
			})
		}
	}
	return out
}

// driftRemovedBudget reports each removed budget key or table (sty_58a7bdfe) a
// repo file still carries: such a repo fails to load until satelle init removes
// it. A file that cannot be read is another finding's concern.
func driftRemovedBudget(repoRoot string) []ScaffoldFinding {
	edits, err := config.FindRemovedBudget(filepath.Join(repoRoot, config.DefaultDataDir))
	if err != nil {
		return nil
	}
	var out []ScaffoldFinding
	for _, e := range edits {
		for _, k := range e.Removed {
			out = append(out, ScaffoldFinding{
				Path:   config.DefaultDataDir + "/" + e.Rel,
				Kind:   "config",
				Detail: k + " is removed — run satelle init",
			})
		}
	}
	return out
}

func dedupeScaffoldFindings(in []ScaffoldFinding) []ScaffoldFinding {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []ScaffoldFinding
	for _, f := range in {
		key := f.Path + "\x00" + f.Kind + "\x00" + f.Detail
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

// formatScaffoldDriftWarning is the SessionStart / status human block.
// heal command is always `satelle init`.
func formatScaffoldDriftWarning(findings []ScaffoldFinding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "⚠️ satelle: deployed harness scaffolding drifts from this binary — run `satelle init` to heal (%d item(s)):\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(&b, "  - %s [%s]: %s\n", f.Path, f.Kind, f.Detail)
	}
	return strings.TrimRight(b.String(), "\n")
}

// warnScaffoldDrift tells the operator, on w (stderr), that the deployed harness
// scaffolding differs from this binary's canonical wrappers, and never refuses
// (sty_e56ea643). Every file DetectScaffoldDrift reports is satelle-owned, so
// staleness is a heal the operator can run when convenient, not a reason to
// stop ordinary commands across every repo the moment a release changes a
// managed byte. The only hard refusal left is the CHANGELOG `### Breaking`
// gate (checkBreakingDrift), which the release itself declares (`init-heals:`).
// Dev builds and uninitialised repos skip.
func warnScaffoldDrift(repoRoot string, w io.Writer) {
	if isDevVersion(strings.TrimSpace(buildinfo.Resolve().Version)) {
		return
	}
	dataDir := filepath.Join(repoRoot, config.DefaultDataDir)
	if st, err := os.Stat(dataDir); err != nil || !st.IsDir() {
		return
	}
	if line := formatScaffoldDriftOneLine(DetectScaffoldDrift(repoRoot)); line != "" {
		fmt.Fprintln(w, line)
	}
}

// formatScaffoldDriftOneLine is the ordinary-command warning: ONE line naming
// each stale artifact once and the heal, "" when nothing drifts. It goes to
// stderr so stdout stays clean for the JSON the store verbs print.
func formatScaffoldDriftOneLine(findings []ScaffoldFinding) string {
	if len(findings) == 0 {
		return ""
	}
	var items []string
	seen := map[string]bool{}
	for _, f := range findings {
		item := f.Path + "[" + f.Kind + "]"
		if !seen[item] {
			seen[item] = true
			items = append(items, item)
		}
	}
	return fmt.Sprintf("⚠️ satelle: harness scaffolding is stale (%d): %s — satelle-managed files; run `satelle init` to heal (idempotent)",
		len(items), strings.Join(items, ", "))
}
