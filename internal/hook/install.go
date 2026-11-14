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

// ErrNotOwned is a pi extension file at satelle's path that does not carry
// satelle's owned marker: Refresh and Pending leave it, a hand-run Install
// replaces it.
var ErrNotOwned = errors.New("not satelle's file")

// ConfigDir is the repo-relative configuration directory of harness: the first
// path element of its hook file (.claude, .grok, .pi, .cursor). "" for an
// unknown harness.
func ConfigDir(harness string) string {
	rel, err := File(harness)
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(rel, "/")
	return first
}

// SatelleCall reads cmd's first whitespace-separated word: ok when it is the
// satelle binary (by lower-cased basename, .exe trimmed), and words are the
// words after it up to, not including, the first that starts with "-". No
// shell syntax is read: a command that wraps satelle (sh -c, env, timeout,
// NAME=value, if-blocks) has another first word and is not a satelle call.
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

// ownsCommand reports whether cmd is one of satelle's own hook entries, which
// an install replaces with the current set: a satelle call whose first word is
// hook, or the legacy satelle-hook.sh script.
func ownsCommand(cmd string) bool {
	if words, ok := SatelleCall(cmd); ok && len(words) > 0 && words[0] == "hook" {
		return true
	}
	return strings.Contains(cmd, "satelle-hook.sh")
}

// plan computes harness's hook file under root as an install would leave it:
// satelle's own entries replaced by the current set, and every handler stale
// reports removed (stale may be nil). changed is false when the resulting
// hooks equal the file's as JSON values (any indentation or key order), or,
// for pi, when the rendered extension equals the file's bytes. ownedOnly
// refuses a present pi extension without satelle's marker with ErrNotOwned.
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

// Install writes or updates harness's hook file under root, keeping every
// entry that is not satelle's and dropping each one stale reports (stale may
// be nil). A file whose hooks would not change is not written. It returns the
// file and whether it wrote it. A pi extension that is not satelle's is
// replaced.
func Install(root, harness string, stale func(string) bool) (string, bool, error) {
	path, body, changed, err := plan(root, harness, stale, false)
	if err != nil || !changed {
		return path, false, err
	}
	return path, true, writePlanned(path, body)
}

// Refresh is Install for an unattended pass: a present pi extension that is
// not satelle's is never written (ErrNotOwned).
func Refresh(root, harness string, stale func(string) bool) (string, bool, error) {
	path, body, changed, err := plan(root, harness, stale, true)
	if err != nil || !changed {
		return path, false, err
	}
	return path, true, writePlanned(path, body)
}

// Pending reports what Refresh would do, writing nothing.
func Pending(root, harness string, stale func(string) bool) (string, bool, error) {
	path, _, changed, err := plan(root, harness, stale, true)
	return path, changed, err
}

// Commands lists every handler command of harness's JSON hook file under
// root, nested groups and flat entries alike. Nil for pi and for an absent
// file.
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
	pruneSatelle(doc, nil)
	if len(doc) == 0 && harness == Grok {
		return path, os.Remove(path)
	}
	return path, writeJSON(path, doc)
}

// withHooks returns prev (a JSON settings document, possibly empty) with
// satelle's hook entries replaced by the current ones and every handler stale
// reports removed.
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

// pruneSatelle removes every hook handler that is satelle's own entry
// (ownsCommand) or that stale reports (stale may be nil), dropping groups and
// events left empty.
func pruneSatelle(doc map[string]any, stale func(string) bool) {
	walkHandlers(doc, func(cmd string) bool { return ownsCommand(cmd) || (stale != nil && stale(cmd)) })
}

// walkHandlers calls drop with every handler command in doc's hooks and
// removes the handlers it returns true for, dropping groups and events left
// empty. A group is claude's and grok's nested {matcher, hooks: [...]}; a
// flat entry with its own command (cursor's) is kept or dropped by that
// command.
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
// session started from Cursor's terminal keeps its own gate. root resolves
// the repo's root; it is called, once, only for a Cursor payload answered for
// another harness, so every other call pays no repo lookup. ok false (not a
// repo) never defers.
func Defers(harness string, in Input, root func() (string, bool)) bool {
	if harness == Cursor || !in.Cursor {
		return false
	}
	tree, ok := root()
	return ok && Installed(tree, Cursor)
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
