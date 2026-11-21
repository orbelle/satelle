package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
)

func DecodeObject(b []byte) (map[string]any, error) {
	doc := map[string]any{}
	if strings.TrimSpace(string(b)) == "" {
		return doc, nil
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func MarshalPrettyJSON(doc map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(doc, "", "  ")
	return append(b, '\n'), err
}

func WalkNestedHooks(doc map[string]any, drop func(cmd string) bool) {
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

func PruneNestedHooks(doc map[string]any, stale func(string) bool) {
	WalkNestedHooks(doc, func(cmd string) bool { return OwnsCommand(cmd) || (stale != nil && stale(cmd)) })
}

func CollectNestedHookCommands(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	doc, err := DecodeObject(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []string
	WalkNestedHooks(doc, func(cmd string) bool { out = append(out, cmd); return false })
	return out, nil
}

func PlanNestedHooks(prev []byte, absent bool, stale func(string) bool, add func(doc, hooks map[string]any)) ([]byte, bool, error) {
	doc, err := DecodeObject(prev)
	if err != nil {
		return nil, false, err
	}
	PruneNestedHooks(doc, stale)
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	add(doc, hooks)
	doc["hooks"] = hooks
	body, err := MarshalPrettyJSON(doc)
	if err != nil {
		return nil, false, err
	}
	if absent {
		return body, true, nil
	}
	before, err := DecodeObject(prev)
	if err != nil {
		return nil, false, err
	}
	after, err := DecodeObject(body)
	if err != nil {
		return nil, false, err
	}
	return body, !reflect.DeepEqual(before["hooks"], after["hooks"]), nil
}

func RemoveNestedHooks(prev []byte, deleteWhenEmpty bool) ([]byte, bool, error) {
	doc, err := DecodeObject(prev)
	if err != nil {
		return nil, false, err
	}
	PruneNestedHooks(doc, nil)
	if len(doc) == 0 && deleteWhenEmpty {
		return nil, true, nil
	}
	body, err := MarshalPrettyJSON(doc)
	return body, false, err
}

func AddGroupedHooks(hooks map[string]any, harness, gateMatcher string) {
	entry := func(matcher, verb string) map[string]any {
		g := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": Command(verb, harness)}}}
		if matcher != "" {
			g["matcher"] = matcher
		}
		return g
	}
	for event, g := range map[string]map[string]any{
		EventPreToolUse:   entry(gateMatcher, VerbGate),
		EventSessionStart: entry("", VerbContext),
		EventPrompt:       entry("", VerbPrompt),
		EventStop:         entry("", VerbStopcheck),
	} {
		list, _ := hooks[event].([]any)
		hooks[event] = append(list, g)
	}
}

func NamesAnyVerb(raw []byte, harness string) bool {
	for _, verb := range Verbs {
		if strings.Contains(string(raw), Command(verb, harness)) {
			return true
		}
	}
	return false
}
