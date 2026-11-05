// harness_hooks.go — the hook-scaffold side of each harness's satelle hook
// surface. The surface table itself (matchers, events) is agentcli.HarnessHooks,
// the one definition the builder, the healer and the checker all read
// (sty_338a53f8).
//
// This file owns the uninstall side of the hook scaffolds: stripping
// satelle-owned entries from .claude/settings.json and .grok/hooks/satelle.json
// while preserving everything the user wrote.
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// turnEndHookCommand is the command wired to a harness's turnEndEvents. It names
// its harness, since the payload of these events carries no tool to sniff.
func turnEndHookCommand(harness string) string {
	return "PATH=$HOME/.local/bin:$PATH satelle hook turnend --harness " + harness
}

// removeClaudeHooks strips satelle-owned entries from .claude/settings.json.
// Never deletes the file or the .claude/ directory.
func removeClaudeHooks(repoRoot string) (action, path, note string, err error) {
	path = filepath.Join(repoRoot, ".claude", "settings.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "absent", path, "", nil
		}
		return "", path, "", err
	}
	stripped := raw
	if !strings.Contains(string(raw), "satelle-hook.sh") && !strings.Contains(string(raw), "satelle hook ") {
		return "skipped", path, "no satelle-owned hook entries — left in place", nil
	}
	pruned, _, perr := pruneSatelleHookEntries(stripped)
	if perr != nil {
		return "skipped", path, "unparseable settings.json — left in place", nil
	}
	if string(pruned) == string(raw) {
		return "unchanged", path, "", nil
	}
	if err := os.WriteFile(path, pruned, 0o644); err != nil {
		return "", path, "", err
	}
	return "updated", path, "stripped satelle-owned entries; user keys preserved", nil
}

// removeGrokHooks strips satelle-owned entries from .grok/hooks/satelle.json.
// Deletes the file only when nothing non-satelle remains.
func removeGrokHooks(repoRoot string) (action, path, note string, err error) {
	path = filepath.Join(repoRoot, filepath.FromSlash(grokHooksRel))
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "absent", path, "", nil
		}
		return "", path, "", err
	}
	if !strings.Contains(string(raw), "satelle-hook.sh") && !strings.Contains(string(raw), "satelle hook ") {
		return "skipped", path, "not satelle-owned — left in place", nil
	}
	pruned, empty, perr := pruneSatelleHookEntries(raw)
	if perr != nil {
		return "skipped", path, "unparseable — left in place", nil
	}
	if empty {
		if err := os.Remove(path); err != nil {
			return "", path, "", err
		}
		_ = os.Remove(filepath.Dir(path)) // best-effort empty hooks dir
		return "removed", path, "", nil
	}
	if err := os.WriteFile(path, pruned, 0o644); err != nil {
		return "", path, "", err
	}
	return "updated", path, "stripped satelle-owned entries; user hooks preserved", nil
}

// removePiHooks deletes the satelle-owned pi extension. A pi extension is one
// TypeScript file, so there is nothing to prune: the file is removed only when it
// carries the satelle-owned marker, and the .pi/extensions and .pi directories
// only when that leaves them empty — a foreign extension or pi config survives.
func removePiHooks(repoRoot string) (action, path, note string, err error) {
	path = filepath.Join(repoRoot, filepath.FromSlash(piExtensionRel))
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "absent", path, "", nil
		}
		return "", path, "", err
	}
	if !strings.Contains(string(raw), piOwnedMarker) {
		return "skipped", path, "not satelle-owned — left in place", nil
	}
	if err := os.Remove(path); err != nil {
		return "", path, "", err
	}
	// Best-effort: os.Remove refuses a non-empty directory, which is the guard.
	dir := filepath.Dir(path)
	_ = os.Remove(dir)
	_ = os.Remove(filepath.Dir(dir))
	return "removed", path, "", nil
}

// pruneSatelleHookEntries removes hook handlers whose command carries a satelle
// marker (satelle-hook.sh or "satelle hook "). Empty matcher groups are dropped.
// empty is true when no hooks remain under any event and no non-hooks top-level
// keys remain that would justify keeping the file.
func pruneSatelleHookEntries(raw []byte) (pruned []byte, empty bool, err error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, false, err
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return raw, false, nil
	}
	for event, val := range hooks {
		groups, ok := val.([]any)
		if !ok {
			continue
		}
		var keptGroups []any
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				keptGroups = append(keptGroups, g)
				continue
			}
			hs, ok := gm["hooks"].([]any)
			if !ok {
				keptGroups = append(keptGroups, g)
				continue
			}
			var kept []any
			for _, h := range hs {
				hm, ok := h.(map[string]any)
				if !ok {
					kept = append(kept, h)
					continue
				}
				cmd, _ := hm["command"].(string)
				if isSatelleOwnedHookCommand(cmd) {
					continue
				}
				kept = append(kept, h)
			}
			if len(kept) == 0 {
				continue // drop empty matcher group
			}
			gm["hooks"] = kept
			keptGroups = append(keptGroups, gm)
		}
		if len(keptGroups) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keptGroups
		}
	}
	root["hooks"] = hooks
	// empty: no events left and no other top-level content. Any other top-level
	// key is user (or unknown) content and keeps the file.
	if len(hooks) == 0 {
		for k := range root {
			if k == "hooks" {
				continue
			}
			b, err := json.MarshalIndent(root, "", "  ")
			if err != nil {
				return nil, false, err
			}
			return append(b, '\n'), false, nil
		}
		return nil, true, nil
	}
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(b, '\n'), false, nil
}

// isSatelleOwnedHookCommand reports whether a hook command is satelle-managed.
// The rule has one definition, shared with the cursor scaffold, in agentinstall;
// the prompt and stopcheck constants both contain "satelle hook ".
func isSatelleOwnedHookCommand(cmd string) bool {
	return strings.Contains(cmd, "satelle-hook.sh") || strings.Contains(cmd, "satelle hook ")
}

// maybeRemoveSharedHookScript deletes .satelle/hooks/satelle-hook.sh only when
// no harness scaffold still references it (sty_9e86f407 AC3).
func maybeRemoveSharedHookScript(repoRoot string) (action, path, note string, err error) {
	path = filepath.Join(repoRoot, filepath.FromSlash(satelleHookScriptRel))
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "absent", path, "", nil
	} else if err != nil {
		return "", path, "", err
	}
	for _, p := range []string{
		filepath.Join(repoRoot, ".claude", "settings.json"),
		filepath.Join(repoRoot, filepath.FromSlash(grokHooksRel)),
		filepath.Join(repoRoot, filepath.FromSlash(piExtensionRel)),
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.Contains(string(b), "satelle-hook.sh") {
			return "skipped", path, "still referenced by a harness scaffold", nil
		}
	}
	if err := os.Remove(path); err != nil {
		return "", path, "", err
	}
	return "removed", path, "", nil
}
