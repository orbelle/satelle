//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/logsread"
)

// fake writes an executable named name that records its argv and stdin and
// runs body.
func fake(t *testing.T, name, body string) (path, argvFile, stdinFile string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, name)
	argvFile, stdinFile = filepath.Join(dir, "argv"), filepath.Join(dir, "stdin")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, argvFile, stdinFile
}

func read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDispatchClaude(t *testing.T) {
	bin, argvFile, stdinFile := fake(t, "claude", "cat > STDIN\necho '{\"result\":\"done\",\"usage\":{\"input_tokens\":5,\"output_tokens\":7}}'\n")
	os.WriteFile(bin, []byte(strings.Replace(read(t, bin), "STDIN", stdinFile, 1)), 0o755)
	b := Binding{Name: "w", Command: bin, Model: "m9", Tools: []string{"Read", "Bash"}}
	res, err := Runner{}.Dispatch(context.Background(), Perform, Request{Binding: b, Prompt: "the work", System: "skill"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" || res.TokensIn != 5 || res.TokensOut != 7 || res.Model != "m9" || res.Duration <= 0 {
		t.Fatalf("result %+v", res)
	}
	argv := read(t, argvFile)
	if !strings.Contains(argv, "Bash(satelle story set:*),Bash(satelle task set:*),Bash(satelle execution set:*)") {
		t.Fatalf("performer denies missing:\n%s", argv)
	}
	if read(t, stdinFile) != "the work" {
		t.Fatal("prompt not on stdin")
	}
	if _, err := (Runner{}).Dispatch(context.Background(), Judge, Request{Binding: b}); err == nil {
		t.Fatal("judge with a shell grant was dispatched")
	}
	b.Tools = []string{"Read"}
	if _, err := (Runner{}).Dispatch(context.Background(), Judge, Request{Binding: b}); err != nil {
		t.Fatal(err)
	}
	b.Placement = "cloud"
	if _, err := (Runner{}).Dispatch(context.Background(), Judge, Request{Binding: b}); err == nil {
		t.Fatal("cloud placement dispatched locally")
	}
}

// grok reads its prompt from --prompt-file: a prompt of any size dispatches
// (one argv element is capped at 128 KiB on Linux), arrives byte for byte,
// never appears in argv, and the file is removed after the run.
func TestDispatchGrok(t *testing.T) {
	bin, argvFile, _ := fake(t, "grok", `prev=
for a in "$@"; do [ "$prev" = "--prompt-file" ] && cp "$a" "`+"${0%/*}"+`/prompt" && printf '%s' "$a" > "`+"${0%/*}"+`/promptpath"; prev=$a; done
echo '{"text":"{\"verdict\":\"accept\"}","usage":{"input_tokens":1,"output_tokens":2}}'
`)
	dir := filepath.Dir(bin)
	b := Binding{Name: "r", Command: bin, Tools: []string{"Read", "Grep"}}
	prompt := strings.Repeat("judge this record line\n", 300<<10/23) // ~300 KiB
	res, err := Runner{}.Dispatch(context.Background(), Judge, Request{Binding: b, Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != `{"verdict":"accept"}` || res.TokensOut != 2 {
		t.Fatalf("result %+v", res)
	}
	if got := read(t, filepath.Join(dir, "prompt")); got != prompt {
		t.Fatalf("the fake read %d bytes from --prompt-file, want %d", len(got), len(prompt))
	}
	if strings.Contains(read(t, argvFile), "judge this record line") {
		t.Fatal("the prompt is in argv")
	}
	if _, err := os.Stat(read(t, filepath.Join(dir, "promptpath"))); !os.IsNotExist(err) {
		t.Fatal("the prompt file outlived the run")
	}
}

func TestDispatchPi(t *testing.T) {
	bin, argvFile, _ := fake(t, "pi", `while IFS= read -r line; do
  case "$line" in
    *'"type":"prompt"'*)
      echo '{"type":"response","id":"satelle-prompt","command":"prompt","success":true}'
      echo '{"type":"extension_ui_request","id":"u1","method":"confirm"}'
      echo '{"type":"message_end","message":{"role":"assistant","provider":"prov","model":"m1","content":[{"type":"text","text":"interim"}]}}'
      echo '{"type":"agent_settled"}';;
    *get_last_assistant_text*) echo '{"type":"response","id":"satelle-text","success":true,"data":{"text":"final"}}';;
    *get_session_stats*) echo '{"type":"response","id":"satelle-stats","success":true,"data":{"tokens":{"input":3,"output":4,"cacheRead":1,"cacheWrite":0}}}';;
  esac
done
`)
	b := Binding{Name: "r", Command: bin, Tools: []string{"Read"}}
	res, err := Runner{}.Dispatch(context.Background(), Judge, Request{Binding: b, Prompt: "p", System: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "final" || res.TokensIn != 4 || res.TokensOut != 4 || res.Model != "prov/m1" {
		t.Fatalf("result %+v", res)
	}
	if argv := read(t, argvFile); !strings.Contains(argv, "--mode\nrpc\n") || !strings.Contains(argv, "--tools\nread\n") {
		t.Fatalf("argv %s", argv)
	}
}

func TestCancelKillsProcessGroup(t *testing.T) {
	bin, _, _ := fake(t, "claude", "sleep 30\necho '{}'\n")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Runner{}.Dispatch(ctx, Judge, Request{Binding: Binding{Name: "r", Command: bin}})
	if err == nil {
		t.Fatal("cancelled run succeeded")
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("cancel took %v", time.Since(start))
	}
}

// fakePi speaks the pi RPC: it answers the prompt, settles, then answers the
// text and stats requests.
const fakePi = `while IFS= read -r line; do
  case "$line" in
    *'"type":"prompt"'*)
      echo '{"type":"response","id":"satelle-prompt","command":"prompt","success":true}'
      echo '{"type":"message_end","message":{"role":"assistant","provider":"prov","model":"m1","content":[{"type":"text","text":"interim"}]}}'
      echo '{"type":"agent_settled"}';;
    *get_last_assistant_text*) echo '{"type":"response","id":"satelle-text","success":true,"data":{"text":"final"}}';;
    *get_session_stats*) echo '{"type":"response","id":"satelle-stats","success":true,"data":{"tokens":{"input":3,"output":4}}}'; echo "pi says bye" >&2;;
  esac
done
`

// A dispatch with Log set writes the full pi RPC exchange there, in order:
// argv, each line sent (> ) and read (< ), then stderr.
func TestDispatchLogPi(t *testing.T) {
	bin, _, _ := fake(t, "pi", fakePi)
	logs := t.TempDir()
	path := logsread.DispatchPath(logs, "r", "sto_1", time.Now().UnixNano())
	b := Binding{Name: "r", Command: bin, Tools: []string{"Read"}}
	if _, err := (Runner{}).Dispatch(context.Background(), Judge, Request{Binding: b, Prompt: "p", System: "s", Log: path}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	want := []string{
		"argv: [",
		`> {"id":"satelle-prompt","message":"p","type":"prompt"}`,
		`< {"type":"response","id":"satelle-prompt"`,
		`< {"type":"message_end"`,
		`< {"type":"agent_settled"}`,
		`> {"id":"satelle-text","type":"get_last_assistant_text"}`,
		`< {"type":"response","id":"satelle-text"`,
		`< {"type":"response","id":"satelle-stats"`,
		"stderr: pi says bye",
	}
	at := 0
	for _, w := range want {
		i := strings.Index(got[at:], w)
		if i < 0 {
			t.Fatalf("log lacks %q after offset %d:\n%s", w, at, got)
		}
		at += i + len(w)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v %v", fi, err)
	}
	if f, ok, err := logsread.Select(logs, "sto_1", "r"); err != nil || !ok || f.Path != path {
		t.Fatalf("satelle logs does not find it: %+v %v %v", f, ok, err)
	}
}

// claude (and grok, through runOnce) log argv and stdout.
func TestDispatchLogClaude(t *testing.T) {
	bin, _, _ := fake(t, "claude", "cat >/dev/null\necho '{\"result\":\"done\",\"usage\":{\"input_tokens\":5,\"output_tokens\":7}}'\necho oops >&2\n")
	path := logsread.DispatchPath(t.TempDir(), "c", "sto_1", 1)
	b := Binding{Name: "c", Command: bin, Tools: []string{"Read"}}
	if _, err := (Runner{}).Dispatch(context.Background(), Judge, Request{Binding: b, Prompt: "p", Log: path}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.HasPrefix(got, "argv: [") || !strings.Contains(got, `< {"result":"done"`) || !strings.Contains(got, "stderr: oops") {
		t.Fatalf("log:\n%s", got)
	}
}

// Logging keeps only the newest dispatch logs, and a log that cannot be
// written does not stop the dispatch.
func TestDispatchLogPrunesAndNeverFails(t *testing.T) {
	bin, _, _ := fake(t, "pi", fakePi)
	b := Binding{Name: "r", Command: bin, Tools: []string{"Read"}}
	logs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(logs, "dispatch"), 0o700); err != nil {
		t.Fatal(err)
	}
	for n := int64(1); n <= int64(config.DefaultLogsMaxFiles)+3; n++ {
		if err := os.WriteFile(logsread.DispatchPath(logs, "r", "sto_1", n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := logsread.DispatchPath(logs, "r", "sto_1", time.Now().UnixNano())
	if _, err := (Runner{}).Dispatch(context.Background(), Judge, Request{Binding: b, Prompt: "p", Log: path}); err != nil {
		t.Fatal(err)
	}
	files, err := logsread.ListDispatch(logs)
	if err != nil || len(files) != config.DefaultLogsMaxFiles || files[0].Path != path {
		t.Fatalf("%v %d %+v", err, len(files), files)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Runner{}.Dispatch(context.Background(), Judge, Request{Binding: b, Prompt: "p", Log: filepath.Join(blocked, "dispatch", "x.log")})
	if err != nil || res.Text != "final" {
		t.Fatalf("unwritable log failed the dispatch: %v %+v", err, res)
	}
}
