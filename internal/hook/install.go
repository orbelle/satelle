package hook

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

const (
	VerbGate      = "gate"
	VerbContext   = "context"
	VerbPrompt    = "prompt"
	VerbStopcheck = "stopcheck"
)

func Command(verb, harness string) string {
	return "satelle hook " + verb + " --harness " + harness
}

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

func toolMatcher(harness string) string {
	switch harness {
	case Cursor:
		return "Write|Delete|Shell"
	case Grok:
		return "Edit|Write|MultiEdit|NotebookEdit|search_replace|write|Bash|run_terminal_command"
	}
	return "Edit|Write|MultiEdit|NotebookEdit|Bash"
}

var cursorEvents = []struct{ event, verb string }{
	{"preToolUse", VerbGate},
	{"sessionStart", VerbContext},
	{"beforeSubmitPrompt", VerbPrompt},
	{"stop", VerbStopcheck},
}

var ErrNotOwned = errors.New("not satelle's file")

func ConfigDir(harness string) string {
	rel, err := File(harness)
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(rel, "/")
	return first
}

func SatelleCall(cmd string) (words []string, ok bool) {
	f := strings.Fields(cmd)
	if len(f) == 0 || strings.TrimSuffix(strings.ToLower(filepath.Base(f[0])), ".exe") != "satelle" {
		return nil, false
	}
	for _, w := range f[1:] {
		if strings.HasPrefix(w, "-") {
			break
		}
		words = append(words, w)
	}
	return words, true
}

func ownsCommand(cmd string) bool {
	if words, ok := SatelleCall(cmd); ok && len(words) > 0 && words[0] == "hook" {
		return true
	}
	return strings.Contains(cmd, "satelle-hook.sh")
}

func plan(root, harness string, stale func(string) bool, ownedOnly bool) (path string, body []byte, changed bool, err error) {
	rel, err := File(harness)
	if err != nil {
		return "", nil, false, err
	}
	path = filepath.Join(root, filepath.FromSlash(rel))
	prev, err := os.ReadFile(path)
	absent := errors.Is(err, os.ErrNotExist)
	if err != nil && !absent {
		return path, nil, false, err
	}
	if harness == Pi {
		if ownedOnly && !absent && !strings.Contains(string(prev), piOwnedMarker) {
			return path, nil, false, ErrNotOwned
		}
		body = piExtension()
		return path, body, absent || !bytes.Equal(prev, body), nil
	}
	if body, err = withHooks(prev, harness, stale); err != nil {
		return path, nil, false, fmt.Errorf("%s: %w", path, err)
	}
	if absent {
		return path, body, true, nil
	}
	before, err := decodeObject(prev)
	if err != nil {
		return path, nil, false, fmt.Errorf("%s: %w", path, err)
	}
	after, err := decodeObject(body)
	if err != nil {
		return path, nil, false, fmt.Errorf("%s: %w", path, err)
	}
	return path, body, !reflect.DeepEqual(before["hooks"], after["hooks"]), nil
}

func writePlanned(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

func Install(root, harness string, stale func(string) bool) (string, bool, error) {
	path, body, changed, err := plan(root, harness, stale, false)
	if err != nil || !changed {
		return path, false, err
	}
	return path, true, writePlanned(path, body)
}

func Refresh(root, harness string, stale func(string) bool) (string, bool, error) {
	path, body, changed, err := plan(root, harness, stale, true)
	if err != nil || !changed {
		return path, false, err
	}
	return path, true, writePlanned(path, body)
}

func Pending(root, harness string, stale func(string) bool) (string, bool, error) {
	path, _, changed, err := plan(root, harness, stale, true)
	return path, changed, err
}

func Commands(root, harness string) ([]string, error) {
	rel, err := File(harness)
	if err != nil || harness == Pi {
		return nil, err
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	doc, err := decodeObject(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []string
	walkHandlers(doc, func(cmd string) bool { out = append(out, cmd); return false })
	return out, nil
}

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
	pruneSatelle(doc, nil)
	if len(doc) == 0 && harness == Grok {
		return path, os.Remove(path)
	}
	return path, writeJSON(path, doc)
}

func withHooks(prev []byte, harness string, stale func(string) bool) ([]byte, error) {
	doc, err := decodeObject(prev)
	if err != nil {
		return nil, err
	}
	pruneSatelle(doc, stale)
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	if harness == Cursor {
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

func pruneSatelle(doc map[string]any, stale func(string) bool) {
	walkHandlers(doc, func(cmd string) bool { return ownsCommand(cmd) || (stale != nil && stale(cmd)) })
}

func walkHandlers(doc map[string]any, drop func(cmd string) bool) {
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		return
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
			if _, nested := gm["hooks"]; !nested {
				if cmd, isStr := gm["command"].(string); !isStr || !drop(cmd) {
					keptGroups = append(keptGroups, g)
				}
				continue
			}
			hs, _ := gm["hooks"].([]any)
			var kept []any
			for _, h := range hs {
				if hm, ok := h.(map[string]any); ok {
					if cmd, isStr := hm["command"].(string); isStr && drop(cmd) {
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

func Defers(harness string, in Input, root func() (string, bool)) bool {
	if harness == Cursor || !in.Cursor {
		return false
	}
	tree, ok := root()
	return ok && Installed(tree, Cursor)
}

func ProjectDir() string {
	for _, k := range []string{"CLAUDE_PROJECT_DIR", "CURSOR_PROJECT_DIR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

const piOwnedMarker = "// satelle-owned:"

//go:embed pi_extension.ts.tmpl
var piTemplate string

func piExtension() []byte {
	verbs, _ := json.Marshal(map[string]string{
		"gate": VerbGate, "context": VerbContext, "prompt": VerbPrompt, "stopcheck": VerbStopcheck,
	})
	return []byte(strings.NewReplacer("__SATELLE_VERBS__", string(verbs), "__SATELLE_HARNESS__", strconv.Quote(Pi)).Replace(piTemplate))
}
