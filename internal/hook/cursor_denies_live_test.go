//go:build !windows

package hook

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gittest"
)

// shellCall is one shell tool call in a cursor-agent stream: its command and
// how it completed (ran, denied, or neither when no completed event came).
type shellCall struct {
	Command string
	Ran     bool   // completed with an execution result (success: exit code, output)
	Denied  bool   // completed with permissionDenied
	Outcome string // the completed result's key(s), for a failure's report
}

// cursorShellCalls reads the shell tool calls of a stream-json stream. A
// completed call carries result.success when it ran and
// result.permissionDenied when Cursor's permission rules refused it
// (testdata/cursor-stream-denied.jsonl, captured from cursor-agent 2026.10.22).
func cursorShellCalls(stream []byte) []shellCall {
	type shellArgs struct {
		Command string `json:"command"`
	}
	type ev struct {
		Type     string `json:"type"`
		Subtype  string `json:"subtype"`
		ToolCall struct {
			Shell *struct {
				Args   shellArgs                  `json:"args"`
				Result map[string]json.RawMessage `json:"result"`
			} `json:"shellToolCall"`
		} `json:"tool_call"`
	}
	var out []shellCall
	sc := bufio.NewScanner(strings.NewReader(string(stream)))
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var e ev
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type != "tool_call" || e.ToolCall.Shell == nil {
			continue
		}
		s := e.ToolCall.Shell
		switch e.Subtype {
		case "started":
			out = append(out, shellCall{Command: s.Args.Command})
		case "completed":
			for i := len(out) - 1; i >= 0; i-- {
				if out[i].Command == s.Args.Command && !out[i].Ran && !out[i].Denied {
					var keys []string
					for k := range s.Result {
						if k != "isBackground" {
							keys = append(keys, k)
						}
					}
					out[i].Outcome = strings.Join(keys, ",")
					out[i].Ran = len(s.Result["success"]) > 0
					out[i].Denied = len(s.Result["permissionDenied"]) > 0
					break
				}
			}
		}
	}
	return out
}

// The classifier reads a real stream: the allowed printf ran, the satelle call
// was denied and never ran.
func TestCursorShellCallsClassifiesTheCapturedStream(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "agent", "testdata", "cursor-stream-denied.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	calls := cursorShellCalls(b)
	var printf, denied bool
	for _, c := range calls {
		if c.Command == "printf ok" && c.Ran && !c.Denied {
			printf = true
		}
		if c.Command == "satelle --version" && c.Denied && !c.Ran {
			denied = true
		}
	}
	if !printf || !denied {
		t.Fatalf("classified %+v", calls)
	}
}

// streamFromLog is the stdout lines a dispatch log recorded ("< " lines).
func streamFromLog(t *testing.T, log string) []byte {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if s, ok := strings.CutPrefix(l, "< "); ok {
			out = append(out, s)
		}
	}
	return []byte(strings.Join(out, "\n"))
}

// A cursor performer granted Edit and Write and no Bash runs force mode with
// every shell command denied: told to run a control command and each form of
// the edit gate's corpus with its shell, and to write a file with its edit
// tool, it attempts each command and Cursor refuses it, neither the fake
// satelle nor the control ever runs, and the file is written (sto_0f41dda9
// AC3). Opt-in: SATELLE_LIVE_CURSOR=1 and cursor-agent on PATH;
// SATELLE_LIVE_CURSOR_MODEL picks the model.
func TestCursorPerformerRefusesEveryEditGateForm(t *testing.T) {
	if os.Getenv("SATELLE_LIVE_CURSOR") != "1" {
		t.Skip("set SATELLE_LIVE_CURSOR=1 to run cursor-agent live")
	}
	if _, err := exec.LookPath("cursor-agent"); err != nil {
		t.Skip("cursor-agent is not on PATH")
	}
	model := os.Getenv("SATELLE_LIVE_CURSOR_MODEL")
	if model == "" {
		model = "gemini-3.8-flash"
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	satLog, ctlLog := filepath.Join(tmp, "satelle.log"), filepath.Join(tmp, "ctl.log")
	for name, log := range map[string]string{"satelle": satLog, "ctlprobe": ctlLog} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A scratch repo with no .satelle, whose path contains satelle: even a
	// real satelle reached by mistake refuses harmlessly here.
	run := filepath.Join(tmp, "satelle-worktree")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, run, "init", "-q")
	b := agent.Binding{Name: "live", Command: "cursor-agent", Model: model, Tools: []string{"Read", "Grep", "Glob", "Edit", "Write"}}
	env := []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	for i, form := range editGateDenyForms(bin) {
		n := i + 1
		id := fmt.Sprintf("sto_l%d", n)
		t.Run(id, func(t *testing.T) {
			log := filepath.Join(tmp, "logs", "dispatch", id+".log")
			prompt := fmt.Sprintf("You are testing shell permissions. Use your shell tool to run these two commands, one at a time, exactly as written, each even if one is refused:\n\n1. `ctlprobe %s`\n2. The command between the markers, verbatim:\n<<<COMMAND\n%s\nCOMMAND>>>\n\nThen, with your file edit or write tool (not the shell), create the file allowed_%d.txt containing ok. Report each outcome briefly.\n", id, form, n)
			_, err := agent.Runner{}.Dispatch(context.Background(), agent.Perform, agent.Request{Binding: b, Prompt: prompt, Dir: run, Env: env, Log: log})
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			calls := cursorShellCalls(streamFromLog(t, log))
			var formTried, formRan, ctlTried, ctlRan bool
			for _, c := range calls {
				if !strings.Contains(c.Command, id) {
					continue
				}
				refused := c.Denied && !c.Ran
				if strings.HasPrefix(strings.TrimSpace(c.Command), "ctlprobe") {
					ctlTried, ctlRan = true, ctlRan || !refused
				} else {
					formTried, formRan = true, formRan || !refused
				}
			}
			_, allowedErr := os.Stat(filepath.Join(run, fmt.Sprintf("allowed_%d.txt", n)))
			if !formTried || !ctlTried || allowedErr != nil {
				t.Fatalf("inconclusive: form attempted %v, control attempted %v, allowed file %v; calls %+v", formTried, ctlTried, allowedErr, calls)
			}
			if formRan {
				t.Errorf("Cursor did not deny %q: calls %+v", form, calls)
			}
			if ctlRan {
				t.Errorf("Cursor did not deny the control ctlprobe %s: calls %+v", id, calls)
			}
			for _, l := range []string{satLog, ctlLog} {
				if got, _ := os.ReadFile(l); strings.Contains(string(got), id) {
					t.Errorf("%s ran for %q", filepath.Base(l), form)
				}
			}
			if _, err := os.Stat(filepath.Join(run, ".cursor")); !os.IsNotExist(err) {
				t.Errorf("the run wrote .cursor in the working tree: %v", err)
			}
		})
	}
}
