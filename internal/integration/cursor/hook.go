package cursor

import (
	"github.com/bobmcallan/satelle/internal/integration/common"
)

const (
	hookFile      = ".cursor/hooks.json"
	gateMatcher   = "Write|Delete|Shell"
	projectDirVar = "CURSOR_PROJECT_DIR"
	shellTool     = "Shell"
	versionKey    = "cursor_version"
)

var (
	callerMarkers = []string{"CURSOR_AGENT", "CURSOR_INVOKED_AS"}
	keys          = common.PayloadKeys{
		Event:     []string{"hook_event_name"},
		Session:   []string{"session_id", "conversation_id"},
		Model:     []string{"model", "model_id"},
		Tool:      []string{"tool_name"},
		ToolInput: []string{"tool_input"},
		Path:      []string{"file_path", "notebook_path", "path"},
		Command:   []string{"command"},
		Cwd:       "cwd",
		Roots:     "workspace_roots",
		Reason:    "reason",
	}
	events = []struct{ event, verb string }{
		{"preToolUse", common.VerbGate},
		{"sessionStart", common.VerbContext},
		{"beforeSubmitPrompt", common.VerbPrompt},
		{"stop", common.VerbStopcheck},
	}
)

type Hook struct{}

func (Hook) Name() string             { return Name }
func (Hook) CLI() string              { return CLI }
func (Hook) File() string             { return hookFile }
func (Hook) Fallback() bool           { return false }
func (Hook) ProjectDirVars() []string { return []string{projectDirVar} }
func (Hook) ShellTools() []string     { return []string{shellTool} }

func (Hook) CallerMarkers() (names, prefixes []string) {
	return append([]string{}, callerMarkers...), nil
}

func (Hook) ParseInput(raw []byte) (common.HookInput, bool) {
	p, ok := common.DecodePayload(raw)
	if !ok || p.Str(versionKey) == "" {
		return common.HookInput{}, false
	}
	in := p.Input(keys)
	in.DetectedHarness = Name
	return in, true
}

func (Hook) Deny(reason string) []byte {
	return common.MarshalJSON(struct {
		Permission   string `json:"permission"`
		UserMessage  string `json:"user_message"`
		AgentMessage string `json:"agent_message"`
	}{"deny", reason, reason})
}

func (Hook) Context(event, text, _ string) []byte {
	if event == common.EventSessionStart {
		return common.MarshalJSON(struct {
			AdditionalContext string `json:"additional_context"`
		}{text})
	}
	return common.MarshalJSON(struct {
		Continue bool `json:"continue"`
	}{true})
}

func (Hook) StopAnswer() []byte { return []byte("{}") }

func (Hook) StopBlock(reason string) []byte {
	return common.MarshalJSON(struct {
		FollowupMessage string `json:"followup_message"`
	}{reason})
}

func (Hook) Plan(prev []byte, absent bool, stale func(string) bool, _ bool) ([]byte, bool, error) {
	return common.PlanNestedHooks(prev, absent, stale, func(doc, hooks map[string]any) {
		if _, ok := doc["version"]; !ok {
			doc["version"] = 1
		}
		for _, ce := range events {
			e := map[string]any{"command": common.Command(ce.verb, Name)}
			if ce.verb == common.VerbGate {
				e["matcher"] = gateMatcher
			}
			list, _ := hooks[ce.event].([]any)
			hooks[ce.event] = append(list, e)
		}
	})
}

func (Hook) Remove(prev []byte) ([]byte, bool, error) { return common.RemoveNestedHooks(prev, false) }

func (Hook) Commands(path string) ([]string, error) { return common.CollectNestedHookCommands(path) }

func (Hook) Installed(raw []byte) bool { return common.NamesAnyVerb(raw, Name) }

func (Hook) AutoModeDisplay() (string, error) { return "", common.AutoModeUnavailable(Name) }

func (Hook) FormatAutoMode([]byte) ([]byte, error) { return nil, common.AutoModeUnavailable(Name) }
