//go:build !windows

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeCursor is a cursor-agent that records argv and stdin, copies any live
// .cursor/cli.json of its working directory, then prints stream.
func fakeCursor(t *testing.T, stream string, exit int) (bin, argvFile, stdinFile, permsFile string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "cursor-agent")
	argvFile, stdinFile, permsFile = filepath.Join(dir, "argv"), filepath.Join(dir, "stdin"), filepath.Join(dir, "perms")
	streamFile := filepath.Join(dir, "stream")
	if err := os.WriteFile(streamFile, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\ncat > " + stdinFile + "\n" +
		"cp \"$CURSOR_CONFIG_DIR/cli-config.json\" " + permsFile + "\n" +
		"echo \"$CURSOR_CONFIG_DIR\" > " + permsFile + ".dir\n" +
		"if [ -L \"$CURSOR_CONFIG_DIR/auth.json\" ]; then readlink \"$CURSOR_CONFIG_DIR/auth.json\" > " + permsFile + ".auth; fi\n" +
		"cat " + streamFile + "\nexit " + string(rune('0'+exit)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile, stdinFile, permsFile
}

func TestDispatchCursor(t *testing.T) {
	stream, err := os.ReadFile("testdata/cursor-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	bin, argvFile, stdinFile, permsFile := fakeCursor(t, string(stream), 0)

	authHome := t.TempDir()
	t.Setenv("CURSOR_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", authHome)
	if err := os.MkdirAll(filepath.Join(authHome, "cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authHome, "cursor", "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A judge: ask mode, the prompt and skill byte for byte on stdin, the
	// parsed result.
	big := strings.Repeat("p", 300<<10) + "END"
	run := t.TempDir()
	judge := Binding{Name: "j", Command: bin, Model: "claude-opus-5-5-high", Tools: []string{"Read", "Grep", "Glob"}}
	res, err := Runner{}.Dispatch(context.Background(), Judge, Request{Binding: judge, Prompt: big, System: "the skill", Dir: run})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "OK" || res.TokensIn != 4+9854+10126 || res.TokensOut != 4 || res.Model != "Claude Opus 5.5 300K Medium" {
		t.Fatalf("result %+v", res)
	}
	argv := read(t, argvFile)
	for _, want := range []string{"-p\n", "--output-format\nstream-json\n", "--trust\n", "--model\nclaude-opus-5-5-high\n", "--mode\nask\n"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv lacks %q:\n%s", want, argv)
		}
	}
	if strings.Contains(argv, "--force") || strings.Contains(argv, "pppp") || strings.Contains(argv, "the skill") {
		t.Errorf("judge argv carries force or the prompt:\n%.300s", argv)
	}
	if got := read(t, stdinFile); got != "<system>\nthe skill\n</system>\n\n"+big {
		t.Errorf("stdin is not the skill then the prompt (%d bytes)", len(got))
	}
	// The judge's own config denies every shell command (ask mode still runs
	// commands Cursor deems read-only), links the machine's sign-in, writes
	// nothing in the working tree and is removed after the run.
	if got := read(t, permsFile); !strings.Contains(got, `"Shell(**)"`) {
		t.Errorf("judge config:\n%s", got)
	}
	if got := strings.TrimSpace(read(t, permsFile+".auth")); got != filepath.Join(authHome, "cursor", "auth.json") {
		t.Errorf("auth link = %q", got)
	}
	if _, err := os.Stat(strings.TrimSpace(read(t, permsFile+".dir"))); !os.IsNotExist(err) {
		t.Errorf("the run's config dir was left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(run, ".cursor")); !os.IsNotExist(err) {
		t.Errorf("the run wrote .cursor in the working tree: %v", err)
	}

	// A judge granted Edit and Write is refused by the judge check.
	if _, err := (Runner{}).Dispatch(context.Background(), Judge, Request{Binding: Binding{Name: "x", Command: bin, Tools: []string{"Read", "Grep", "Glob", "Edit", "Write"}}}); err == nil || !strings.Contains(err.Error(), "may not judge") {
		t.Fatalf("an Edit-granted cursor judge: %v", err)
	}

	// A performer: force mode, its config denies every shell command, and the
	// working tree is untouched.
	perf := Binding{Name: "p", Command: bin, Model: "m", Tools: []string{"Read", "Grep", "Glob", "Edit", "Write"}}
	if _, err := (Runner{}).Dispatch(context.Background(), Perform, Request{Binding: perf, Prompt: "work", Dir: run}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, argvFile); !strings.Contains(got, "--force\n") || strings.Contains(got, "--mode\n") {
		t.Errorf("performer argv:\n%s", got)
	}
	var cfg struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(read(t, permsFile)), &cfg); err != nil || len(cfg.Permissions.Allow) != 0 || !slices.Equal(cfg.Permissions.Deny, []string{"Shell(**)"}) {
		t.Errorf("performer config %+v %v:\n%s", cfg, err, read(t, permsFile))
	}
	if entries, _ := os.ReadDir(run); len(entries) != 0 {
		t.Errorf("the working tree was written: %v", entries)
	}
}

// A project .cursor/cli.json at or above the run dir that could widen the run
// refuses it, naming the file; a deny-only file lets it proceed.
func TestCursorProjectConfig(t *testing.T) {
	stream, err := os.ReadFile("testdata/cursor-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CURSOR_CONFIG_DIR", t.TempDir())
	ro := []string{"Read", "Grep", "Glob"}
	for name, c := range map[string]struct {
		body   string
		refuse bool
	}{
		"allow":        {`{"permissions":{"allow":["Shell(**)"]}}`, true},
		"approvalMode": {`{"approvalMode":"unrestricted"}`, true},
		"not JSON":     {`allow everything`, true},
		"deny only":    {`{"permissions":{"allow":[],"deny":["Shell(rm)"]}}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".cursor"), 0o755); err != nil {
				t.Fatal(err)
			}
			cli := filepath.Join(root, ".cursor", "cli.json")
			if err := os.WriteFile(cli, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "a", "b")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			bin, argvFile, _, _ := fakeCursor(t, string(stream), 0)
			_, err := Runner{}.Dispatch(context.Background(), Judge, Request{Binding: Binding{Name: "j", Command: bin, Tools: ro}, Prompt: "p", Dir: dir})
			ran := fileExists(argvFile)
			if c.refuse {
				if err == nil || !strings.Contains(err.Error(), cli) || ran {
					t.Fatalf("not refused (ran=%v): %v", ran, err)
				}
				return
			}
			if err != nil || !ran {
				t.Fatalf("a deny-only file stopped the run (ran=%v): %v", ran, err)
			}
		})
	}
}

// A dispatch that fails names its cause and is never a verdict.
func TestDispatchCursorFailures(t *testing.T) {
	ro := []string{"Read", "Grep", "Glob"}
	if _, err := (Runner{}).Dispatch(context.Background(), Judge, Request{Binding: Binding{Name: "m", Command: "/no/such/cursor-agent", Tools: ro}}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("missing binary: %v", err)
	}
	dir := t.TempDir()
	login := filepath.Join(dir, "cursor-agent")
	os.WriteFile(login, []byte("#!/bin/sh\ncat >/dev/null\necho 'Authentication required. Please run cursor-agent login' >&2\nexit 1\n"), 0o755)
	if _, err := (Runner{}).Dispatch(context.Background(), Judge, Request{Binding: Binding{Name: "l", Command: login, Tools: ro}}); err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Errorf("not signed in: %v", err)
	}
	for want, stream := range map[string]string{
		"quota exceeded":         `{"type":"result","subtype":"error","is_error":true,"result":"quota exceeded"}`,
		"no answer":              `{"type":"result","subtype":"success","is_error":false,"result":""}`,
		"without a result event": `{"type":"system","subtype":"init","model":"m"}`,
		"not a stream-json":      `plain text`,
	} {
		bin, _, _, _ := fakeCursor(t, stream+"\n", 0)
		if _, err := (Runner{}).Dispatch(context.Background(), Judge, Request{Binding: Binding{Name: "f", Command: bin, Tools: ro}, Prompt: "p"}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
}
