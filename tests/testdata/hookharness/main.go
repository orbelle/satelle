// Command hookharness is a claude stand-in that honours hooks the way claude
// does: before each scripted tool call it runs the PreToolUse hook commands
// that ./.claude/settings.json (as `satelle init --hooks claude` wrote it)
// matches to the tool, and makes the call only when no hook denies it.
//
// It reads its prompt on stdin, records its argv in $FAKE_DIR/argv, makes an
// Edit of perform-edit.txt and a Write of perform-write.txt, and appends
// "allow <tool>" or "deny <tool>: <reason>" for each to $FAKE_DIR/calls. Its
// hook payload carries a session id of its own, so an allow can only come
// from the environment it was started with. It answers with claude's JSON
// envelope, or exits 1 after the calls when $FAKE_DIR/fail exists.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type settings struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"hooks"`
	} `json:"hooks"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hookharness:", err)
		os.Exit(1)
	}
}

func run() error {
	fake := os.Getenv("FAKE_DIR")
	if fake == "" {
		return errors.New("FAKE_DIR is unset")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := os.WriteFile(filepath.Join(fake, "argv"), []byte(strings.Join(os.Args, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(cwd, ".claude", "settings.json"))
	if err != nil {
		return err
	}
	var s settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	session := hex.EncodeToString(b[:])
	calls, err := os.OpenFile(filepath.Join(fake, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer calls.Close()
	for _, c := range []struct{ tool, file string }{{"Edit", "perform-edit.txt"}, {"Write", "perform-write.txt"}} {
		path := filepath.Join(cwd, c.file)
		reason, err := preToolUse(s, cwd, session, c.tool, path)
		if err != nil {
			return err
		}
		if reason != "" {
			fmt.Fprintf(calls, "deny %s: %s\n", c.tool, strings.ReplaceAll(reason, "\n", " "))
			continue
		}
		if err := os.WriteFile(path, []byte(c.tool+" by the performer\n"), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(calls, "allow %s\n", c.tool)
	}
	if _, err := os.Stat(filepath.Join(fake, "fail")); err == nil {
		return errors.New("told to fail")
	}
	fmt.Println(`{"type":"result","subtype":"success","is_error":false,"result":"done","usage":{"input_tokens":5,"output_tokens":3}}`)
	return nil
}

// preToolUse runs every PreToolUse hook command whose matcher matches tool and
// returns the first deny's reason, "" when every hook allows. A hook that
// fails in any other way is an error, so a broken hook never reads as allow.
func preToolUse(s settings, cwd, session, tool, path string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      session,
		"cwd":             cwd,
		"tool_name":       tool,
		"tool_input":      map[string]any{"file_path": path},
	})
	if err != nil {
		return "", err
	}
	for _, g := range s.Hooks["PreToolUse"] {
		if g.Matcher != "" {
			re, err := regexp.Compile("^(?:" + g.Matcher + ")$")
			if err != nil {
				return "", err
			}
			if !re.MatchString(tool) {
				continue
			}
		}
		for _, h := range g.Hooks {
			if h.Type != "command" {
				continue
			}
			cmd := exec.Command("sh", "-c", h.Command)
			cmd.Dir = cwd
			cmd.Env = append(os.Environ(), "CLAUDE_PROJECT_DIR="+cwd)
			cmd.Stdin = bytes.NewReader(payload)
			var out, errb bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errb
			err := cmd.Run()
			var ee *exec.ExitError
			switch {
			case errors.As(err, &ee) && ee.ExitCode() == 2:
				return "exit 2: " + strings.TrimSpace(errb.String()), nil
			case err != nil:
				return "", fmt.Errorf("hook %q: %v: %s", h.Command, err, errb.String())
			}
			var v struct {
				HookSpecificOutput struct {
					PermissionDecision       string `json:"permissionDecision"`
					PermissionDecisionReason string `json:"permissionDecisionReason"`
				} `json:"hookSpecificOutput"`
			}
			if strings.TrimSpace(out.String()) != "" {
				if err := json.Unmarshal(out.Bytes(), &v); err != nil {
					return "", fmt.Errorf("hook %q printed %q: %v", h.Command, out.String(), err)
				}
			}
			if v.HookSpecificOutput.PermissionDecision == "deny" {
				return v.HookSpecificOutput.PermissionDecisionReason, nil
			}
		}
	}
	return "", nil
}
