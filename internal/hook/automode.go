package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const autoModeDefaults = "$defaults"

var ErrAutoModeUnavailable = errors.New("auto-mode guidance is unavailable")

type autoModeBlock struct {
	Environment []string `json:"environment"`
}

type autoModeDoc struct {
	AutoMode autoModeBlock `json:"autoMode"`
}

func AutoModeLines(guidance []byte) []string {
	var out []string
	for _, line := range strings.Split(string(guidance), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func AutoModeDisplay(harness string) (string, error) {
	switch harness {
	case Claude:
		return "~/.claude/settings.json", nil
	case Pi, Grok, Cursor:
		return "", fmt.Errorf("%s: %w", harness, ErrAutoModeUnavailable)
	}
	return "", fmt.Errorf("unknown harness %q (one of %s)", harness, strings.Join(Harnesses, ", "))
}

func FormatAutoMode(harness string, guidance []byte) ([]byte, error) {
	if _, err := AutoModeDisplay(harness); err != nil {
		return nil, err
	}
	env := append([]string{autoModeDefaults}, AutoModeLines(guidance)...)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(autoModeDoc{AutoMode: autoModeBlock{Environment: env}}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
