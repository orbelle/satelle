package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	VerbGate      = "gate"
	VerbContext   = "context"
	VerbPrompt    = "prompt"
	VerbStopcheck = "stopcheck"
)

var Verbs = []string{VerbGate, VerbContext, VerbPrompt, VerbStopcheck}

const (
	EventPreToolUse   = "PreToolUse"
	EventSessionStart = "SessionStart"
	EventPrompt       = "UserPromptSubmit"
	EventStop         = "Stop"
)

const TurnEndReason = "end_turn"

var (
	ErrNotOwned            = errors.New("not satelle's file")
	ErrAutoModeUnavailable = errors.New("auto-mode guidance is unavailable")
)

type HookInput struct {
	Event           string
	Session         string
	Cwd             string
	Roots           []string
	Tool            string
	Path            string
	Command         string
	StopReason      string
	Model           string
	DetectedHarness string
}

type HookAdapter interface {
	Name() string
	CLI() string
	File() string
	Fallback() bool
	CallerMarkers() (names, prefixes []string)
	ProjectDirVars() []string
	ShellTools() []string
	ParseInput(raw []byte) (HookInput, bool)
	Deny(reason string) []byte
	Context(event, text, notice string) []byte
	StopAnswer() []byte
	StopBlock(reason string) []byte
	Plan(prev []byte, absent bool, stale func(string) bool, ownedOnly bool) (body []byte, changed bool, err error)
	Remove(prev []byte) (body []byte, removeFile bool, err error)
	Commands(path string) ([]string, error)
	Installed(raw []byte) bool
	AutoModeDisplay() (string, error)
	FormatAutoMode(guidance []byte) ([]byte, error)
}

func Command(verb, harness string) string {
	return "satelle hook " + verb + " --harness " + harness
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

func OwnsCommand(cmd string) bool {
	if words, ok := SatelleCall(cmd); ok && len(words) > 0 && words[0] == "hook" {
		return true
	}
	return strings.Contains(cmd, "satelle-hook.sh")
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

func AutoModeUnavailable(harness string) error {
	return fmt.Errorf("%s: %w", harness, ErrAutoModeUnavailable)
}

func MarshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("hook: marshal: " + err.Error())
	}
	return b
}

func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func PreToolUseDeny(reason string) []byte {
	var doc struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	doc.HookSpecificOutput.HookEventName = EventPreToolUse
	doc.HookSpecificOutput.PermissionDecision = "deny"
	doc.HookSpecificOutput.PermissionDecisionReason = reason
	return MarshalJSON(doc)
}

func EventContext(event, text, notice string) []byte {
	var doc struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		SystemMessage string `json:"systemMessage,omitempty"`
	}
	doc.HookSpecificOutput.HookEventName = event
	doc.HookSpecificOutput.AdditionalContext = text
	doc.SystemMessage = notice
	return MarshalJSON(doc)
}

func DecisionBlock(reason string) []byte {
	return MarshalJSON(struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{"block", reason})
}
