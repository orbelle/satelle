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
	case Cursor:
		return ".cursor/hooks.json", nil
	}
	return "", fmt.Errorf("unknown harness %q (one of %s)", harness, strings.Join(Harnesses, ", "))
}

// toolMatcher is the PreToolUse matcher: the harness's edit and shell tools.
// Cursor names its tools by type: Write (create and edit), Delete and Shell.
func toolMatcher(harness string) string {
	switch harness {
	case Cursor:
		return "Write|Delete|Shell"
	case Grok:
		return "Edit|Write|MultiEdit|NotebookEdit|search_replace|write|Bash|run_terminal_command"
	}
	return "Edit|Write|MultiEdit|NotebookEdit|Bash"
}

// cursorEvents maps each hook verb to the Cursor hooks.json event that runs it.
var cursorEvents = []struct{ event, verb string }{
	{"preToolUse", VerbGate},
	{"sessionStart", VerbContext},
	{"beforeSubmitPrompt", VerbPrompt},
	{"stop", VerbStopcheck},
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
	if harness == Cursor {
		// Cursor's hooks.json (version 1) holds flat {command, matcher}
		// entries under each event name.
		if _, ok := doc["version"]; !ok {
			doc["version"] = 1
		}
		for _, ce := range cursorEvents {
			e := map[string]any{"command": Command(ce.verb, Cursor)}
			if ce.verb == VerbGate {
				e["matcher"] = toolMatcher(Cursor)
			}
			list, _ := hooks[ce.event].([]any)
			hooks[ce.event] = append(list, e)
		}
		doc["hooks"] = hooks
		b, err := json.MarshalIndent(doc, "", "  ")
		return append(b, '\n'), err
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
// dropping groups and events left empty. A group is claude's and grok's nested
// {matcher, hooks: [...]}; a flat entry with its own command (cursor's) is
// kept or dropped by that command.
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
			if _, nested := gm["hooks"]; !nested {
				if cmd, _ := gm["command"].(string); !isSatelleCommand(cmd) {
					keptGroups = append(keptGroups, g)
				}
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

// Installed reports whether harness's hook file under root holds a satelle
// hook entry for that harness.
func Installed(root, harness string) bool {
	rel, err := File(harness)
	if err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return false
	}
	if harness == Pi {
		return strings.Contains(string(b), piOwnedMarker)
	}
	for _, verb := range []string{VerbGate, VerbContext, VerbPrompt, VerbStopcheck} {
		if strings.Contains(string(b), Command(verb, harness)) {
			return true
		}
	}
	return false
}

// Defers reports whether a hook call answered for harness should stay silent
// because Cursor sent it and Cursor's own satelle hooks answer the same event:
// Cursor also runs a repo's .claude/settings.json hooks (its Claude Code
// compatibility, whose payloads carry cursor_version), so once `satelle agents
// install cursor` has run, the claude entries would judge and inject twice.
// Only the payload marks the caller; the environment does not, so a claude
// session started from Cursor's terminal keeps its own gate.
func Defers(root, harness string, in Input) bool {
	return harness != Cursor && in.Cursor && Installed(root, Cursor)
}

// ProjectDir is the project dir the calling harness pins for its hooks:
// CLAUDE_PROJECT_DIR (claude; Cursor sets it too), else CURSOR_PROJECT_DIR.
// "" when neither is set.
func ProjectDir() string {
	for _, k := range []string{"CLAUDE_PROJECT_DIR", "CURSOR_PROJECT_DIR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
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
