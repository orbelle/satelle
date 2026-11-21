package grok

import (
	"github.com/bobmcallan/satelle/internal/integration/common"
)

const (
	hookFile     = ".grok/hooks/satelle.json"
	gateMatcher  = "Edit|Write|MultiEdit|NotebookEdit|search_replace|write|Bash|run_terminal_command"
	callerMarker = "GROK_AGENT"
	shellTool    = "run_terminal_command"
)

var (
	keys = common.PayloadKeys{
		Event:     []string{"hook_event_name", "hookEventName"},
		Session:   []string{"session_id", "sessionId"},
		Model:     []string{"model", "modelId"},
		Tool:      []string{"tool_name", "toolName"},
		ToolInput: []string{"tool_input", "toolInput"},
		Path:      []string{"file_path", "notebook_path", "filePath", "path", "notebookPath"},
		Command:   []string{"command", "cmd"},
		Cwd:       "cwd",
		Reason:    "reason",
	}
	ownKeys = []string{"hookEventName", "sessionId", "toolName", "toolInput", "modelId"}
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
	if !ok {
		return common.HookInput{}, false
	}
	for _, k := range ownKeys {
		if p.Has(k) {
			return p.Input(keys), true
		}
	}
	return common.HookInput{}, false
}

func (Hook) Deny(reason string) []byte {
	return common.MarshalJSON(struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{"deny", reason})
}

func (Hook) Context(event, text, _ string) []byte {
	if event != common.EventSessionStart {
		return nil
	}
	return common.MarshalJSON(struct {
		SystemMessage string `json:"systemMessage"`
	}{text})
}

func (Hook) StopAnswer() []byte             { return nil }
func (Hook) StopBlock(reason string) []byte { return common.DecisionBlock(reason) }

func (Hook) Plan(prev []byte, absent bool, stale func(string) bool, _ bool) ([]byte, bool, error) {
	return common.PlanNestedHooks(prev, absent, stale, func(_, hooks map[string]any) {
		common.AddGroupedHooks(hooks, Name, gateMatcher)
	})
}

func (Hook) Remove(prev []byte) ([]byte, bool, error) { return common.RemoveNestedHooks(prev, true) }

func (Hook) Commands(path string) ([]string, error) { return common.CollectNestedHookCommands(path) }

func (Hook) Installed(raw []byte) bool { return common.NamesAnyVerb(raw, Name) }

func (Hook) AutoModeDisplay() (string, error) { return "", common.AutoModeUnavailable(Name) }

func (Hook) FormatAutoMode([]byte) ([]byte, error) { return nil, common.AutoModeUnavailable(Name) }
