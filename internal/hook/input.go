package hook

import (
	"encoding/json"
	"strings"
)

const (
	Claude = "claude"
	Pi     = "pi"
	Grok   = "grok"
	Cursor = "cursor"
)

var Harnesses = []string{Claude, Pi, Grok, Cursor}

var harnessCLI = map[string]string{Claude: "claude", Pi: "pi", Grok: "grok", Cursor: "cursor-agent"}

func ForCLI(cli string) (string, bool) {
	for h, c := range harnessCLI {
		if c == cli {
			return h, true
		}
	}
	return "", false
}

type Input struct {
	Event      string
	Session    string
	Cwd        string
	Roots      []string
	Tool       string
	Path       string
	Command    string
	StopReason string
	Cursor     bool
}

const TurnEndReason = "end_turn"

func (in Input) TurnEnd() bool {
	return in.StopReason == "" || in.StopReason == TurnEndReason
}

var shellTools = map[string]bool{"Bash": true, "bash": true, "run_terminal_command": true, "Shell": true}

func (in Input) IsShell() bool { return shellTools[in.Tool] || (in.Tool == "" && in.Command != "") }

func (in Input) IsEdit() bool { return !in.IsShell() && in.Path != "" }

func ParseInput(raw []byte) Input {
	type toolInput struct {
		FilePath          string `json:"file_path"`
		FilePathCamel     string `json:"filePath"`
		Path              string `json:"path"`
		NotebookPath      string `json:"notebook_path"`
		NotebookPathCamel string `json:"notebookPath"`
		Command           string `json:"command"`
		Cmd               string `json:"cmd"`
	}
	var ev struct {
		Event      string    `json:"hook_event_name"`
		EventCamel string    `json:"hookEventName"`
		Session    string    `json:"session_id"`
		SessCamel  string    `json:"sessionId"`
		Convers    string    `json:"conversation_id"`
		CursorVer  string    `json:"cursor_version"`
		Roots      []string  `json:"workspace_roots"`
		Cwd        string    `json:"cwd"`
		Tool       string    `json:"tool_name"`
		ToolCamel  string    `json:"toolName"`
		Input      toolInput `json:"tool_input"`
		InputCamel toolInput `json:"toolInput"`
		Reason     string    `json:"reason"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return Input{}
	}
	in := Input{
		Event:      first(ev.Event, ev.EventCamel),
		Session:    first(ev.Session, ev.SessCamel, ev.Convers),
		Cwd:        strings.TrimSpace(ev.Cwd),
		Roots:      ev.Roots,
		Cursor:     strings.TrimSpace(ev.CursorVer) != "",
		Tool:       first(ev.Tool, ev.ToolCamel),
		StopReason: strings.TrimSpace(ev.Reason),
	}
	for _, ti := range []toolInput{ev.Input, ev.InputCamel} {
		in.Path = first(in.Path, ti.FilePath, ti.NotebookPath, ti.FilePathCamel, ti.Path, ti.NotebookPathCamel)
		in.Command = first(in.Command, ti.Command, ti.Cmd)
	}
	return in
}

func first(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
