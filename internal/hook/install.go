package hook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/integration"
	"github.com/bobmcallan/satelle/internal/integration/common"
)

const (
	VerbGate      = common.VerbGate
	VerbContext   = common.VerbContext
	VerbPrompt    = common.VerbPrompt
	VerbStopcheck = common.VerbStopcheck
)

var ErrNotOwned = common.ErrNotOwned

func Command(verb, harness string) string { return common.Command(verb, harness) }

func SatelleCall(cmd string) (words []string, ok bool) { return common.SatelleCall(cmd) }

func known(harness string) (common.HookAdapter, error) {
	if a, ok := integration.Hook(harness); ok {
		return a, nil
	}
	return nil, fmt.Errorf("unknown harness %q (one of %s)", harness, strings.Join(Harnesses, ", "))
}

func File(harness string) (string, error) {
	a, err := known(harness)
	if err != nil {
		return "", err
	}
	return a.File(), nil
}

func ConfigDir(harness string) string {
	rel, err := File(harness)
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(rel, "/")
	return first
}

func hookPath(root string, a common.HookAdapter) string {
	return filepath.Join(root, filepath.FromSlash(a.File()))
}

func plan(root, harness string, stale func(string) bool, ownedOnly bool) (p string, body []byte, changed bool, err error) {
	a, err := known(harness)
	if err != nil {
		return "", nil, false, err
	}
	p = hookPath(root, a)
	prev, err := os.ReadFile(p)
	absent := errors.Is(err, os.ErrNotExist)
	if err != nil && !absent {
		return p, nil, false, err
	}
	body, changed, err = a.Plan(prev, absent, stale, ownedOnly)
	if err != nil && !errors.Is(err, ErrNotOwned) {
		err = fmt.Errorf("%s: %w", p, err)
	}
	return p, body, changed, err
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
	a, err := known(harness)
	if err != nil {
		return nil, err
	}
	return a.Commands(hookPath(root, a))
}

func Remove(root, harness string) (string, error) {
	a, err := known(harness)
	if err != nil {
		return "", err
	}
	p := hookPath(root, a)
	prev, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	body, removeFile, err := a.Remove(prev)
	switch {
	case err != nil:
		return p, fmt.Errorf("%s: %w", p, err)
	case removeFile:
		return p, os.Remove(p)
	case body == nil:
		return p, nil
	}
	return p, os.WriteFile(p, body, 0o644)
}

func Installed(root, harness string) bool {
	a, err := known(harness)
	if err != nil {
		return false
	}
	b, err := os.ReadFile(hookPath(root, a))
	if err != nil {
		return false
	}
	return a.Installed(b)
}

func Defers(harness string, in Input, root func() (string, bool)) bool {
	detected := in.DetectedHarness
	if detected == "" || detected == harness {
		return false
	}
	tree, ok := root()
	return ok && Installed(tree, detected)
}

func ProjectDir() string {
	for _, a := range integration.Hooks() {
		for _, k := range a.ProjectDirVars() {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v
			}
		}
	}
	return ""
}
