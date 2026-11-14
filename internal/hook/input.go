// Package hook is satelle's harness hook surface: it reads a harness's hook
// input, judges an edit or a shell command against the edit gate, and answers
// in the harness's own envelope. It is agent-agnostic: claude, pi, grok and
// cursor each differ only in the shape of their input and output, never in
// policy.
package hook

import (
	"encoding/json"
	"strings"
)

// Harness names.
const (
	Claude = "claude"
	Pi     = "pi"
	Grok   = "grok"
	Cursor = "cursor"
)

// Harnesses lists the harnesses satelle installs hooks for.
var Harnesses = []string{Claude, Pi, Grok, Cursor}

// harnessCLI is each harness's command-line program, the binary an
// agents.toml binding's command names when it runs that harness.
var harnessCLI = map[string]string{Claude: "claude", Pi: "pi", Grok: "grok", Cursor: "cursor-agent"}

// ForCLI returns the harness whose command-line program is cli (a binding's
// command basename), and false when cli runs none of them.
func ForCLI(cli string) (string, bool) {
	for h, c := range harnessCLI {
		if c == cli {
			return h, true
		}
	}
	return "", false
}

// Input is the part of a harness hook payload satelle reads.
type Input struct {
	Event   string // hook_event_name
	Session string // session_id, else conversation_id (cursor)
	Cwd     string
	Roots   []string // workspace_roots (cursor): the session's project roots
	Tool    string   // tool_name
	Path    string   // the edit target of an edit tool
	Command string   // the command of a shell tool
	// StopReason is a Stop payload's reason (grok: end_turn when a turn
	// ends, channel_closed or shutdown when the session ends); "" when the
	// harness sends none.
	StopReason string
	// Cursor is true when Cursor sent the payload (it carries cursor_version),
	// whether through its own hooks file or its Claude Code compatibility.
	Cursor bool
}

// TurnEndReason is the Stop reason that ends a turn (grok's end_turn).
const TurnEndReason = "end_turn"

// TurnEnd reports whether a Stop payload ends a turn, the one stop satelle
// answers: true when it carries no reason (claude, pi, cursor) or end_turn;
// false for another reason (grok's channel_closed and shutdown fire at
// session end, and grok ignores their answer).
func (in Input) TurnEnd() bool {
	return in.StopReason == "" || in.StopReason == TurnEndReason
}

// shellTools are the tool names of a shell tool across harnesses.
var shellTools = map[string]bool{"Bash": true, "bash": true, "run_terminal_command": true, "Shell": true}

// IsShell reports whether the input is a shell tool call.
func (in Input) IsShell() bool { return shellTools[in.Tool] || (in.Tool == "" && in.Command != "") }

// IsEdit reports whether the input is a file-editing tool call.
func (in Input) IsEdit() bool { return !in.IsShell() && in.Path != "" }

// ParseInput reads a hook payload. Claude and pi (whose extension normalises
// to claude's shape) use snake_case; grok may use camelCase; cursor uses
// snake_case with its own tool names (Write, Delete, Shell) and carries
// conversation_id, cursor_version and workspace_roots. An unreadable payload is
// the zero Input.
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
