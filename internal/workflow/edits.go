package workflow

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

func StateEditScope(dir, name, state string) (string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	var undecoded []error
	for _, p := range paths {
		var raw map[string]any
		if _, err := toml.DecodeFile(p, &raw); err != nil {
			if strings.TrimSuffix(filepath.Base(p), ".toml") == name {
				return "", fmt.Errorf("%s: %w", p, err)
			}
			undecoded = append(undecoded, fmt.Errorf("%s: %w", p, err))
			continue
		}
		wname, _ := raw["name"].(string)
		if wname == "" {
			wname = strings.TrimSuffix(filepath.Base(p), ".toml")
		}
		if wname != name {
			continue
		}
		return stateScope(p, raw, state)
	}
	if len(undecoded) > 0 {
		return "", fmt.Errorf("no workflow file names %q, and %w", name, undecoded[0])
	}
	return "", fmt.Errorf("no workflow file in %s names %q", dir, name)
}

func stateScope(path string, raw map[string]any, state string) (string, error) {
	states, _ := raw["states"].(map[string]any)
	st, ok := states[state].(map[string]any)
	if !ok {
		return "", fmt.Errorf("%s: state %q is not defined", path, state)
	}
	var s State
	if v, set := st["edits"]; set {
		e, isStr := v.(string)
		if !isStr {
			return "", fmt.Errorf("%s: state %q edits is not a string", path, state)
		}
		s.Edits = e
	}
	if p, isTable := st["performer"].(map[string]any); isTable {
		s.Performer = &Performer{}
		s.Performer.Agent, _ = p["agent"].(string)
		s.Performer.Skill, _ = p["skill"].(string)
		s.Performer.Operator, _ = p["operator"].(string)
	}
	scope := s.EditScope()
	switch scope {
	case "", EditsTree, EditsAttachments:
		return scope, nil
	}
	return "", fmt.Errorf("%s: state %q edits %q is not %s or %s", path, state, s.Edits, EditsTree, EditsAttachments)
}
