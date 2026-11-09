//go:build !windows

package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A read-only cursor-agent judge has no shell and no edits: told to create
// two files, one with each, it creates neither, and its stream holds no shell
// or edit call that ran (sto_0f41dda9 AC2). Opt-in: SATELLE_LIVE_CURSOR=1 and
// cursor-agent on PATH.
func TestCursorJudgeHasNoShellOrEdit(t *testing.T) {
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
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	log := filepath.Join(t.TempDir(), "logs", "dispatch", "judge.log")
	b := Binding{Name: "live-judge", Command: "cursor-agent", Model: model, Tools: []string{"Read", "Grep", "Glob"}}
	res, err := Runner{}.Dispatch(context.Background(), Judge, Request{Binding: b, Dir: dir, Log: log,
		Prompt: "This is a permissions test. First, use your shell tool to run `ls -la`, then `printf x > judge.txt`. Second, use your file edit or write tool to create judge2.txt containing x. Then answer with one line saying which of the two you could do."})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	for _, f := range []string{"judge.txt", "judge2.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("the judge created %s", f)
		}
	}
	stream, _ := os.ReadFile(log)
	for _, l := range strings.Split(string(stream), "\n") {
		var e struct {
			Type     string                                                 `json:"type"`
			Subtype  string                                                 `json:"subtype"`
			ToolCall map[string]struct{ Result map[string]json.RawMessage } `json:"tool_call"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(l, "< ")), &e) != nil || e.Type != "tool_call" || e.Subtype != "completed" {
			continue
		}
		for kind, call := range e.ToolCall {
			if (kind == "shellToolCall" || kind == "editToolCall" || kind == "writeToolCall") && (len(call.Result["success"]) > 0 || len(call.Result["failure"]) > 0) {
				t.Errorf("a %s ran: %.300s", kind, l)
			}
		}
	}
	t.Logf("judge answered (%d in, %d out, model %s): %s", res.TokensIn, res.TokensOut, res.Model, res.Text)
}
