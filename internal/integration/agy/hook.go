package agy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"

	"github.com/bobmcallan/satelle/internal/integration/common"
)

const (
	Name          = "agy"
	hookFile      = ".agents/hooks.json"
	owner         = "satelle"
	shellTool     = "run_command"
	callerMarker  = "ANTIGRAVITY_AGENT"
	timeout       = 10
	gateMatcher   = "write_file|edit_file|write_to_file|replace_file_content|multi_replace_file_content|sed_file|notebook_edit|run_command"
	eventPreInv   = "PreInvocation"
	noToolCall    = "NO_TOOL_CALL"
	continuation  = "continuation_active"
	unknownReason = "unknown"
)

type Hook struct{}

func (Hook) Name() string             { return Name }
func (Hook) CLI() string              { return CLI }
func (Hook) File() string             { return hookFile }
func (Hook) Fallback() bool           { return false }
func (Hook) ProjectDirVars() []string { return nil }
func (Hook) ShellTools() []string     { return []string{shellTool} }

func (Hook) CallerMarkers() (names, prefixes []string) {
	return []string{callerMarker}, nil
}

func (Hook) ParseInput(raw []byte) (common.HookInput, bool) {
	p, ok := common.DecodePayload(raw)
	if !ok || !p.Has("conversationId") || !p.Has("artifactDirectoryPath") {
		return common.HookInput{}, false
	}
	tc := p.Sub("toolCall")
	args := tc.Sub("args")
	in := common.HookInput{
		Event:           p.Str("hookEventName"),
		Session:         p.Str("conversationId"),
		Model:           p.Str("modelName"),
		Cwd:             p.Str("cwd"),
		Roots:           p.Strs("workspacePaths"),
		Tool:            tc.Str("name"),
		Path:            args.Str("Path", "TargetFile", "file_path", "notebook_path", "path"),
		Command:         args.Str("CommandLine", "command"),
		DetectedHarness: Name,
	}
	switch in.Event {
	case eventPreInv:
		var n int
		_ = json.Unmarshal(p["invocationNum"], &n)
		in.MidTurn = n > 0
	case common.EventStop:
		in.StopReason = stopReason(p)
	}
	return in, true
}

func stopReason(p common.Payload) string {
	var active bool
	_ = json.Unmarshal(p["continuationActive"], &active)
	reason := p.Str("terminationReason")
	switch {
	case reason == noToolCall && !active:
		return common.TurnEndReason
	case active:
		return continuation
	case reason != "":
		return reason
	}
	return unknownReason
}

type message struct {
	SystemMessage string `json:"systemMessage"`
}

type step struct {
	SystemMessage message `json:"systemMessage"`
}

func (Hook) Deny(reason string) []byte {
	return common.MarshalJSON(struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{"deny", reason})
}

func (Hook) Context(event, text, notice string) []byte {
	if text == "" {
		return []byte("{}")
	}
	steps := []step{{message{text}}}
	if event == common.EventSessionStart && notice != "" {
		steps = append(steps, step{message{notice}})
	}
	return common.MarshalJSON(struct {
		InjectSteps []step `json:"injectSteps"`
	}{steps})
}

func (Hook) StopAnswer() []byte { return []byte("{}") }

func (Hook) StopBlock(reason string) []byte {
	return common.MarshalJSON(struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{"continue", reason})
}

func walkOwners(doc map[string]any, drop func(cmd string) bool) {
	for name, val := range doc {
		obj, ok := val.(map[string]any)
		if !ok {
			continue
		}
		wrap := map[string]any{"hooks": obj}
		common.WalkNestedHooks(wrap, drop)
		if len(obj) == 0 {
			delete(doc, name)
		}
	}
}

func prune(doc map[string]any, stale func(string) bool) {
	walkOwners(doc, func(cmd string) bool { return common.OwnsCommand(cmd) || (stale != nil && stale(cmd)) })
}

func handler(verb string) map[string]any {
	return map[string]any{"type": "command", "command": common.Command(verb, Name), "timeout": timeout}
}

func satelleEvents() map[string]any {
	return map[string]any{
		common.EventPreToolUse:   map[string]any{"matcher": gateMatcher, "hooks": []any{handler(common.VerbGate)}},
		common.EventSessionStart: handler(common.VerbContext),
		eventPreInv:              handler(common.VerbPrompt),
		common.EventStop:         handler(common.VerbStopcheck),
	}
}

func (Hook) Plan(prev []byte, absent bool, stale func(string) bool, _ bool) ([]byte, bool, error) {
	doc, err := common.DecodeObject(prev)
	if err != nil {
		return nil, false, err
	}
	prune(doc, stale)
	own, _ := doc[owner].(map[string]any)
	if own == nil {
		own = map[string]any{}
	}
	for event, entry := range satelleEvents() {
		list, _ := own[event].([]any)
		own[event] = append(list, entry)
	}
	doc[owner] = own
	body, err := common.MarshalPrettyJSON(doc)
	if err != nil {
		return nil, false, err
	}
	if absent {
		return body, true, nil
	}
	before, err := common.DecodeObject(prev)
	if err != nil {
		return nil, false, err
	}
	after, err := common.DecodeObject(body)
	if err != nil {
		return nil, false, err
	}
	return body, !reflect.DeepEqual(before, after), nil
}

func (Hook) Remove(prev []byte) ([]byte, bool, error) {
	doc, err := common.DecodeObject(prev)
	if err != nil {
		return nil, false, err
	}
	prune(doc, nil)
	if len(doc) == 0 {
		return nil, true, nil
	}
	body, err := common.MarshalPrettyJSON(doc)
	return body, false, err
}

func (Hook) Commands(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	doc, err := common.DecodeObject(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []string
	walkOwners(doc, func(cmd string) bool { out = append(out, cmd); return false })
	return out, nil
}

func (Hook) Installed(raw []byte) bool { return common.NamesAnyVerb(raw, Name) }

func (Hook) AutoModeDisplay() (string, error) { return "", common.AutoModeUnavailable(Name) }

func (Hook) FormatAutoMode([]byte) ([]byte, error) { return nil, common.AutoModeUnavailable(Name) }
