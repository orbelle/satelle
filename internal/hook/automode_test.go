package hook

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestAutoModeLines(t *testing.T) {
	in := []byte("\n  First line.  \r\n\r\n\t\nSecond line with <tag> && value.\n   \nThird line.\r\n")
	want := []string{
		"First line.",
		"Second line with <tag> && value.",
		"Third line.",
	}
	if got := AutoModeLines(in); !slices.Equal(got, want) {
		t.Fatalf("AutoModeLines = %#v, want %#v", got, want)
	}
	if got := AutoModeLines([]byte(" \n\t\r\n ")); len(got) != 0 {
		t.Fatalf("AutoModeLines on blank input = %#v, want empty", got)
	}
}

func TestFormatAutoModeClaude(t *testing.T) {
	display, err := AutoModeDisplay(Claude)
	if err != nil {
		t.Fatalf("AutoModeDisplay(%q): %v", Claude, err)
	}
	if display != "~/.claude/settings.json" {
		t.Fatalf("AutoModeDisplay(%q) = %q, want ~/.claude/settings.json", Claude, display)
	}

	raw := []byte("\nFirst rule.\r\n\n  Run satelle --session <session> && check > out  \r\n")
	out, err := FormatAutoMode(Claude, raw)
	if err != nil {
		t.Fatalf("FormatAutoMode(%q): %v", Claude, err)
	}
	s := string(out)
	if !strings.HasSuffix(s, "\n") {
		t.Errorf("formatted JSON lacks trailing newline: %q", s)
	}
	for _, escaped := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(s, escaped) {
			t.Errorf("formatted JSON HTML-escaped prose (%s):\n%s", escaped, s)
		}
	}
	if !strings.Contains(s, "<session> && check > out") {
		t.Errorf("formatted JSON did not preserve raw <, >, &:\n%s", s)
	}

	var top map[string]any
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, s)
	}
	if len(top) != 1 {
		t.Fatalf("top-level keys = %v, want only autoMode", top)
	}
	if _, hasPerms := top["permissions"]; hasPerms {
		t.Errorf("formatted JSON must not contain permissions: %v", top)
	}
	am, ok := top["autoMode"].(map[string]any)
	if !ok {
		t.Fatalf("autoMode is not an object: %#v", top["autoMode"])
	}
	if len(am) != 1 {
		t.Fatalf("autoMode keys = %v, want only environment", am)
	}
	for _, forbidden := range []string{"allow", "soft_deny", "hard_deny", "classifyAllShell"} {
		if _, found := am[forbidden]; found {
			t.Errorf("autoMode must not set %q: %v", forbidden, am)
		}
	}
	var decoded struct {
		AutoMode struct {
			Environment []string `json:"environment"`
		} `json:"autoMode"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal typed: %v", err)
	}
	wantEnv := []string{"$defaults", "First rule.", "Run satelle --session <session> && check > out"}
	if !slices.Equal(decoded.AutoMode.Environment, wantEnv) {
		t.Errorf("environment = %#v, want %#v", decoded.AutoMode.Environment, wantEnv)
	}

	emptyOut, err := FormatAutoMode(Claude, []byte(" \r\n\t\n"))
	if err != nil {
		t.Fatalf("FormatAutoMode empty: %v", err)
	}
	var emptyDecoded struct {
		AutoMode struct {
			Environment []string `json:"environment"`
		} `json:"autoMode"`
	}
	if err := json.Unmarshal(emptyOut, &emptyDecoded); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if want := []string{"$defaults"}; !slices.Equal(emptyDecoded.AutoMode.Environment, want) {
		t.Errorf("empty guidance environment = %#v, want %#v", emptyDecoded.AutoMode.Environment, want)
	}
}

func TestAutoModeOtherHarnessesUnavailable(t *testing.T) {
	for _, h := range []string{Pi, Grok, Cursor, Agy} {
		display, err := AutoModeDisplay(h)
		if !errors.Is(err, ErrAutoModeUnavailable) || display != "" {
			t.Errorf("AutoModeDisplay(%q) = (%q, %v), want ErrAutoModeUnavailable", h, display, err)
		}
		if err != nil && !strings.Contains(err.Error(), h) {
			t.Errorf("AutoModeDisplay(%q) error %q does not name harness", h, err.Error())
		}
		out, err := FormatAutoMode(h, []byte("guidance\n"))
		if !errors.Is(err, ErrAutoModeUnavailable) || len(out) != 0 {
			t.Errorf("FormatAutoMode(%q) = (%q, %v), want ErrAutoModeUnavailable", h, out, err)
		}
		if err != nil && !strings.Contains(err.Error(), h) {
			t.Errorf("FormatAutoMode(%q) error %q does not name harness", h, err.Error())
		}
	}

	for _, call := range []struct {
		name string
		err  error
	}{
		{"AutoModeDisplay", func() error { _, err := AutoModeDisplay("unknown-tool"); return err }()},
		{"FormatAutoMode", func() error { _, err := FormatAutoMode("unknown-tool", nil); return err }()},
	} {
		if call.err == nil {
			t.Errorf("%s(unknown-tool): got nil error", call.name)
			continue
		}
		if errors.Is(call.err, ErrAutoModeUnavailable) {
			t.Errorf("%s(unknown-tool): got ErrAutoModeUnavailable, want unknown harness error", call.name)
		}
		if !strings.Contains(call.err.Error(), `unknown harness "unknown-tool"`) {
			t.Errorf("%s(unknown-tool): error = %q, want unknown harness message", call.name, call.err.Error())
		}
	}
}
