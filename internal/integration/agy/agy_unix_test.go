//go:build !windows

package agy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/integration/common"
)

type (
	Binding = agent.Binding
	Request = agent.Request
	Runner  = agent.Runner
)

var (
	readOnly = []string{"Read", "Grep", "Glob"}
	writer   = []string{"Read", "Grep", "Glob", "Edit", "Write"}
)

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type fake struct {
	bin, rec string
}

func (f fake) file(name string) string { return filepath.Join(f.rec, name) }

func fakeAgy(t *testing.T, stream string, exit int) fake {
	t.Helper()
	dir := t.TempDir()
	f := fake{bin: filepath.Join(dir, "agy"), rec: dir}
	if err := os.WriteFile(f.file("stream"), []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > %[1]s/argv
cat > %[1]s/stdin
guard=""
while [ $# -gt 0 ]; do
  case "$1" in
    --settings) cp "$2" %[1]s/settings; echo "$2" > %[1]s/settings.path; shift ;;
    --add-dir) guard="$2"; shift ;;
  esac
  shift
done
cp "$guard/.agents/hooks.json" %[1]s/hooks
echo "$guard" > %[1]s/guard.path
echo "$AGY_CLI_DISABLE_AUTO_UPDATE" > %[1]s/env
cat %[1]s/stream
exit %[2]d
`, dir, exit)
	if err := os.WriteFile(f.bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

type permissions struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
}

type hookFile map[string]map[string][]struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Command string `json:"command"`
	} `json:"hooks"`
}

func decode[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("%v:\n%s", err, s)
	}
	return v
}

func matches(matcher, name string) bool {
	for _, b := range strings.Split(matcher, "|") {
		if ok, _ := regexp.MatchString(b, name); ok {
			return true
		}
	}
	return false
}

func stream(t *testing.T) string {
	return read(t, "testdata/agy-stream.jsonl")
}

func TestDispatchAgy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := fakeAgy(t, stream(t), 0)
	big := strings.Repeat("p", 300<<10) + "END"
	run := t.TempDir()
	judge := Binding{Name: "j", Command: f.bin, Model: "gemini-4-hydrogen", Effort: "high", Tools: readOnly}
	res, err := Runner{}.Dispatch(context.Background(), agent.Judge, Request{Binding: judge, Prompt: big, System: "the skill", Dir: run})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "OK\n" || res.TokensIn != 10967 || res.TokensOut != 26 || res.Model != "gemini-4-hydrogen-high" {
		t.Fatalf("result %+v", res)
	}
	argv := strings.Split(strings.TrimSpace(read(t, f.file("argv"))), "\n")
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "pppp") || strings.Contains(joined, "the skill") || slices.Contains(argv, "-p") || slices.Contains(argv, "--mode") {
		t.Errorf("argv carries the prompt, -p or --mode:\n%.300s", joined)
	}
	settingsPath := strings.TrimSpace(read(t, f.file("settings.path")))
	guardPath := strings.TrimSpace(read(t, f.file("guard.path")))
	want := "--input-format stream-json --output-format stream-json --disable-slash-commands --settings " + settingsPath +
		" --add-dir " + run + " --add-dir " + guardPath + " --model gemini-4-hydrogen --effort high"
	if joined != want {
		t.Errorf("argv\n%s\nwant\n%s", joined, want)
	}
	content, _ := json.Marshal("<system>\nthe skill\n</system>\n\n" + big)
	if got := read(t, f.file("stdin")); got != `{"event":"user","message":{"content":`+string(content)+"}}\n" {
		t.Errorf("stdin is not one user event carrying the skill then the prompt (%d bytes): %.200s", len(got), got)
	}
	p := decode[permissions](t, read(t, f.file("settings")))
	if !slices.Equal(p.Permissions.Allow, []string{"read_file(*)"}) ||
		!slices.Equal(p.Permissions.Deny, []string{"write_file(*)", "command(*)", "unsandboxed(*)", "mcp(*)", "read_url(*)", "execute_url(*)", "download_url(*)"}) {
		t.Errorf("judge settings %+v", p)
	}
	hooks := decode[hookFile](t, read(t, f.file("hooks")))
	pre := hooks["satelle-dispatch-guard"]["PreToolUse"]
	if len(hooks) != 1 || len(pre) != 1 || len(pre[0].Hooks) != 1 || !strings.Contains(pre[0].Hooks[0].Command, `"decision":"deny"`) {
		t.Fatalf("guard hooks %+v", hooks)
	}
	if matches(pre[0].Matcher, "read_file") || !matches(pre[0].Matcher, "write_file") || !matches(pre[0].Matcher, "run_command") || !matches(pre[0].Matcher, "search_web") {
		t.Errorf("judge guard matcher %s", pre[0].Matcher)
	}
	for _, p := range []string{settingsPath, guardPath} {
		if strings.HasPrefix(p, run) {
			t.Errorf("%s is inside the run dir", p)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s was left after the run: %v", p, err)
		}
	}
	if got := strings.TrimSpace(read(t, f.file("env"))); got != "true" {
		t.Errorf("AGY_CLI_DISABLE_AUTO_UPDATE = %q", got)
	}

	if _, err := (Runner{}).Dispatch(context.Background(), agent.Judge, Request{Binding: Binding{Name: "x", Command: f.bin, Tools: writer}}); err == nil || !strings.Contains(err.Error(), "may not judge") {
		t.Fatalf("an Edit-granted agy judge: %v", err)
	}

	perf := Binding{Name: "p", Command: f.bin, Tools: writer}
	if _, err := (Runner{}).Dispatch(context.Background(), agent.Perform, Request{Binding: perf, Prompt: "work", Dir: run}); err != nil {
		t.Fatal(err)
	}
	p = decode[permissions](t, read(t, f.file("settings")))
	if !slices.Equal(p.Permissions.Allow, []string{"read_file(*)", "write_file(*)"}) || !slices.Contains(p.Permissions.Deny, "command(*)") || !slices.Contains(p.Permissions.Deny, "unsandboxed(*)") || slices.Contains(p.Permissions.Deny, "write_file(*)") {
		t.Errorf("performer settings %+v", p)
	}
	pre = decode[hookFile](t, read(t, f.file("hooks")))["satelle-dispatch-guard"]["PreToolUse"]
	for _, allowed := range []string{"read_file", "write_file", "edit_file"} {
		if matches(pre[0].Matcher, allowed) {
			t.Errorf("performer guard denies %s", allowed)
		}
	}
	if !matches(pre[0].Matcher, "run_command") {
		t.Errorf("performer guard allows run_command")
	}
	if strings.Contains(read(t, f.file("stdin")), "<system>") {
		t.Error("a run with no system text folded one in")
	}
	if entries, _ := os.ReadDir(run); len(entries) != 0 {
		t.Errorf("the working tree was written: %v", entries)
	}
}

func TestAgyHostConfig(t *testing.T) {
	write := func(t *testing.T, p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	type setup func(t *testing.T, root, home string) string
	at := func(rel, body string) setup {
		return func(t *testing.T, root, home string) string {
			p := filepath.Join(root, rel)
			write(t, p, body)
			return p
		}
	}
	inHome := func(rel, body string) setup {
		return func(t *testing.T, root, home string) string {
			p := filepath.Join(home, rel)
			write(t, p, body)
			return p
		}
	}
	for name, c := range map[string]struct {
		set    setup
		refuse bool
	}{
		"workspace hooks above":      {at(".agents/hooks.json", `{}`), true},
		"toolPermission":             {at(".antigravity/settings.json", `{"toolPermission":"always-proceed"}`), true},
		"permissions.allow":          {at(".antigravity/settings.json", `{"permissions":{"allow":["command(*)"]}}`), true},
		"not JSON":                   {at(".antigravity/settings.json", `allow everything`), true},
		"home .agents hooks":         {inHome(".agents/hooks.json", `{}`), true},
		"user PreToolUse hook":       {inHome(".gemini/config/hooks.json", `{"o":{"PreToolUse":[{"matcher":"*","hooks":[]}]}}`), true},
		"user lifecycle hooks":       {inHome(".gemini/config/hooks.json", `{"orca":{"PreInvocation":[],"Stop":[],"PostToolUse":[{"matcher":"*","hooks":[]}]}}`), false},
		"workspace deny-only":        {at(".antigravity/settings.json", `{"permissions":{"deny":["command(*)"]}}`), false},
		"no config":                  {func(*testing.T, string, string) string { return "" }, false},
		"user settings with allow":   {inHome(".gemini/antigravity-cli/settings.json", `{"toolPermission":"always-proceed","permissions":{"allow":["command(*)"]}}`), false},
		"user settings with hooks":   {inHome(".gemini/antigravity-cli/settings.json", `{"hooks":{}}`), true},
		"cli hooks with unknown evt": {inHome(".gemini/antigravity-cli/hooks.json", `{"o":{"Decide":[]}}`), true},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			root := t.TempDir()
			dir := filepath.Join(root, "a", "b")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			file := c.set(t, root, home)
			f := fakeAgy(t, stream(t), 0)
			_, err := Runner{}.Dispatch(context.Background(), agent.Judge, Request{Binding: Binding{Name: "j", Command: f.bin, Tools: readOnly}, Prompt: "p", Dir: dir})
			ran := common.FileExists(f.file("argv"))
			if c.refuse {
				if err == nil || !strings.Contains(err.Error(), file) || ran {
					t.Fatalf("not refused (ran=%v): %v", ran, err)
				}
				return
			}
			if err != nil || !ran {
				t.Fatalf("refused (ran=%v): %v", ran, err)
			}
		})
	}
}

func TestDispatchAgyFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := (Runner{}).Dispatch(context.Background(), agent.Judge, Request{Binding: Binding{Name: "m", Command: "/no/such/agy", Tools: readOnly}, Prompt: "p"}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("missing binary: %v", err)
	}
	f := fakeAgy(t, read(t, "testdata/agy-stream-error.jsonl"), 1)
	if _, err := (Runner{}).Dispatch(context.Background(), agent.Judge, Request{Binding: Binding{Name: "e", Command: f.bin, Tools: readOnly}, Prompt: "p"}); err == nil || !strings.Contains(err.Error(), "does-not-exist-model") {
		t.Errorf("error result: %v", err)
	}
	dir := t.TempDir()
	login := filepath.Join(dir, "agy")
	if err := os.WriteFile(login, []byte("#!/bin/sh\ncat >/dev/null\necho 'Please login to continue' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (Runner{}).Dispatch(context.Background(), agent.Judge, Request{Binding: Binding{Name: "l", Command: login, Tools: readOnly}, Prompt: "p"}); err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Errorf("not signed in: %v", err)
	}
}
