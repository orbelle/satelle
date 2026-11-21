package claude

import (
	"bytes"
	"encoding/json"

	"github.com/bobmcallan/satelle/internal/integration/common"
)

const (
	hookFile      = ".claude/settings.json"
	autoModeFile  = "~/.claude/settings.json"
	autoModeSeed  = "$defaults"
	gateMatcher   = "Edit|Write|MultiEdit|NotebookEdit|Bash"
	projectDirVar = "CLAUDE_PROJECT_DIR"
	callerMarker  = "CLAUDECODE"
	shellToolName = "Bash"
)

var keys = common.PayloadKeys{
	Event:     []string{"hook_event_name"},
	Session:   []string{"session_id"},
	Model:     []string{"model"},
	Tool:      []string{"tool_name"},
	ToolInput: []string{"tool_input"},
	Path:      []string{"file_path", "notebook_path", "path"},
	Command:   []string{"command"},
	Cwd:       "cwd",
	Reason:    "reason",
}

type Hook struct{}

func (Hook) Name() string             { return Name }
func (Hook) CLI() string              { return CLI }
func (Hook) File() string             { return hookFile }
func (Hook) Fallback() bool           { return true }
func (Hook) ProjectDirVars() []string { return []string{projectDirVar} }
func (Hook) ShellTools() []string     { return []string{shellToolName} }

func (Hook) CallerMarkers() (names, prefixes []string) {
	return []string{callerMarker}, nil
}

func (Hook) ParseInput(raw []byte) (common.HookInput, bool) {
	p, ok := common.DecodePayload(raw)
	if !ok {
		return common.HookInput{}, false
	}
	return p.Input(keys), false
}

func (Hook) Deny(reason string) []byte { return common.PreToolUseDeny(reason) }

func (Hook) Context(event, text, notice string) []byte {
	return common.EventContext(event, text, notice)
}

func (Hook) StopAnswer() []byte             { return nil }
func (Hook) StopBlock(reason string) []byte { return common.DecisionBlock(reason) }

func (Hook) AutoModeDisplay() string { return autoModeFile }

func (Hook) FormatAutoMode(guidance []byte) ([]byte, error) {
	type block struct {
		Environment []string `json:"environment"`
	}
	type doc struct {
		AutoMode block `json:"autoMode"`
	}
	env := append([]string{autoModeSeed}, common.AutoModeLines(guidance)...)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc{AutoMode: block{Environment: env}}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (Hook) Plan(prev []byte, absent bool, stale func(string) bool, _ bool) ([]byte, bool, error) {
	return common.PlanNestedHooks(prev, absent, stale, func(_, hooks map[string]any) {
		common.AddGroupedHooks(hooks, Name, gateMatcher)
	})
}

func (Hook) Remove(prev []byte) ([]byte, bool, error) { return common.RemoveNestedHooks(prev, false) }

func (Hook) Commands(path string) ([]string, error) { return common.CollectNestedHookCommands(path) }

func (Hook) Installed(raw []byte) bool { return common.NamesAnyVerb(raw, Name) }
