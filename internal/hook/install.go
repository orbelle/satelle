package hook

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The hook handlers a harness calls: `satelle hook <verb> --harness <name>`.
const (
	VerbGate      = "gate"
	VerbContext   = "context"
	VerbPrompt    = "prompt"
	VerbStopcheck = "stopcheck"
)

// Command is the hook command line a harness runs for verb.
func Command(verb, harness string) string {
	return "satelle hook " + verb + " --harness " + harness
}

// File is the repo-relative path of a harness's hook file.
func File(harness string) (string, error) {
	switch harness {
	case Claude:
		return ".claude/settings.json", nil
	case Grok:
		return ".grok/hooks/satelle.json", nil
	case Pi:
		return ".pi/extensions/satelle.ts", nil
	}
	return "", fmt.Errorf("unknown harness %q (one of %s)", harness, strings.Join(Harnesses, ", "))
}

// toolMatcher is the PreToolUse matcher: the harness's edit and shell tools.
func toolMatcher(harness string) string {
	if harness == Grok {
		return "Edit|Write|MultiEdit|NotebookEdit|search_replace|write|Bash|run_terminal_command"
	}
	return "Edit|Write|MultiEdit|NotebookEdit|Bash"
}

// Install writes or updates harness's hook file under root, keeping every
// entry that is not satelle's. It returns the file it wrote.
func Install(root, harness string) (string, error) {
	rel, err := File(harness)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	var body []byte
	if harness == Pi {
		body = piExtension()
	} else {
		prev, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return path, err
		}
		if body, err = withHooks(prev, harness); err != nil {
			return path, fmt.Errorf("%s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, err
	}
	return path, os.WriteFile(path, body, 0o644)
}

// Remove strips satelle's entries from harness's hook file under root. A file
// left with nothing of the user's is deleted; one that is not satelle's is
// left alone.
func Remove(root, harness string) (string, error) {
	rel, err := File(harness)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	prev, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return path, err
	}
	if harness == Pi {
		if !strings.Contains(string(prev), piOwnedMarker) {
			return path, nil
		}
		return path, os.Remove(path)
	}
	doc, err := decodeObject(prev)
	if err != nil {
		return path, fmt.Errorf("%s: %w", path, err)
	}
	pruneSatelle(doc)
	if len(doc) == 0 && harness == Grok {
		return path, os.Remove(path)
	}
	return path, writeJSON(path, doc)
}

// withHooks returns prev (a JSON settings document, possibly empty) with
// satelle's hook entries replaced by the current ones.
func withHooks(prev []byte, harness string) ([]byte, error) {
	doc, err := decodeObject(prev)
	if err != nil {
		return nil, err
	}
	pruneSatelle(doc)
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entry := func(matcher, verb string) map[string]any {
		g := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": Command(verb, harness)}}}
		if matcher != "" {
			g["matcher"] = matcher
		}
		return g
	}
	for event, g := range map[string]map[string]any{
		"PreToolUse":       entry(toolMatcher(harness), VerbGate),
		"SessionStart":     entry("", VerbContext),
		"UserPromptSubmit": entry("", VerbPrompt),
		"Stop":             entry("", VerbStopcheck),
	} {
		list, _ := hooks[event].([]any)
		hooks[event] = append(list, g)
	}
	doc["hooks"] = hooks
	b, err := json.MarshalIndent(doc, "", "  ")
	return append(b, '\n'), err
}

func decodeObject(b []byte) (map[string]any, error) {
	doc := map[string]any{}
	if strings.TrimSpace(string(b)) == "" {
		return doc, nil
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func writeJSON(path string, doc map[string]any) error {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// pruneSatelle removes every hook handler whose command is a satelle hook,
// dropping groups and events left empty.
func pruneSatelle(doc map[string]any) {
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		return
	}
	for event, val := range hooks {
		groups, _ := val.([]any)
		var keptGroups []any
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				keptGroups = append(keptGroups, g)
				continue
			}
			hs, _ := gm["hooks"].([]any)
			var kept []any
			for _, h := range hs {
				if hm, ok := h.(map[string]any); ok {
					if cmd, _ := hm["command"].(string); isSatelleCommand(cmd) {
						continue
					}
				}
				kept = append(kept, h)
			}
			if len(kept) > 0 {
				gm["hooks"] = kept
				keptGroups = append(keptGroups, gm)
			}
		}
		if len(keptGroups) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keptGroups
		}
	}
	if len(hooks) == 0 {
		delete(doc, "hooks")
	}
}

func isSatelleCommand(cmd string) bool {
	return strings.Contains(cmd, "satelle hook ") || strings.Contains(cmd, "satelle-hook.sh")
}

const piOwnedMarker = "// satelle-owned:"

//go:embed pi_extension.ts.tmpl
var piTemplate string

// piExtension renders the pi extension: it maps pi's events onto the same
// `satelle hook` handlers the other harnesses call, normalising pi's payload
// to claude's shape.
func piExtension() []byte {
	verbs, _ := json.Marshal(map[string]string{
		"gate": VerbGate, "context": VerbContext, "prompt": VerbPrompt, "stopcheck": VerbStopcheck,
	})
	return []byte(strings.NewReplacer("__SATELLE_VERBS__", string(verbs), "__SATELLE_HARNESS__", strconv.Quote(Pi)).Replace(piTemplate))
}
