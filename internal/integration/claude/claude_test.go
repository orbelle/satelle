package claude

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/integration/common"
)

func TestArgvTarget(t *testing.T) {
	self := common.Request{Binding: common.Binding{Name: "r", Command: "claude", Tools: []string{"Read", "Grep", "Glob"}}, System: "s"}
	selfArgv, err := Dispatch{}.Argv(self)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(selfArgv, "--restricted") || slices.Contains(selfArgv, "--setting-sources") || slices.Contains(selfArgv, "--add-dir") {
		t.Fatalf("self argv %q", selfArgv)
	}
	read := self
	read.Target = &common.Target{ReadDirs: []string{"/tgt", "/rec"}}
	argv, err := Dispatch{}.Argv(read)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--setting-sources user --restricted --add-dir /tgt --add-dir /rec --append-system-prompt s") || slices.Contains(argv, "--settings") {
		t.Fatalf("read-only target argv %q", argv)
	}
	write := read
	write.Binding.Tools = []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash"}
	argv, err = Dispatch{}.Argv(write)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(argv, "--settings")
	if i < 0 || argv[i+1] != TargetSandbox {
		t.Fatalf("bash target argv %q", argv)
	}
	var doc struct {
		Sandbox struct {
			Enabled                  bool     `json:"enabled"`
			AllowUnsandboxedCommands *bool    `json:"allowUnsandboxedCommands"`
			ExcludedCommands         []string `json:"excludedCommands"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal([]byte(argv[i+1]), &doc); err != nil {
		t.Fatal(err)
	}
	s := doc.Sandbox
	if !s.Enabled || s.AllowUnsandboxedCommands == nil || *s.AllowUnsandboxedCommands || s.ExcludedCommands == nil || len(s.ExcludedCommands) != 0 {
		t.Fatalf("sandbox settings %+v", s)
	}
	on := Dispatch{}.Confinement().OnRead
	if !slices.Equal(on.Paths, []string{".claude/rules"}) || !slices.Equal(on.Nested, []string{"CLAUDE.md", "AGENTS.md"}) || !slices.Equal(on.Anywhere, []string{"CLAUDE.local.md"}) {
		t.Fatalf("OnRead %+v", on)
	}
}

func TestParseEnvelope(t *testing.T) {
	res, err := parse([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"ok","usage":{"input_tokens":22,"cache_creation_input_tokens":11000,"cache_read_input_tokens":2500,"output_tokens":300},"modelUsage":{"claude-x":{"outputTokens":300}}}`))
	if err != nil || res.Text != "ok" || res.TokensIn != 13522 || res.TokensOut != 300 || res.Model != "claude-x" {
		t.Fatalf("claude %+v %v", res, err)
	}
	if _, err := parse([]byte(`{"subtype":"error_max_turns","is_error":true}`)); err == nil {
		t.Fatal("error envelope accepted")
	}
}
