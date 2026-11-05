package agentstep

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// sty_bd8d963c: a config value naming a secret by its Infisical location is
// resolved at the point of use, reaches the consumer only in its environment,
// and is scrubbed from everything satelle writes.

const (
	secretRefText = "infisical://p/dev/ci/TOKEN"
	secretMarker  = "[redacted:" + secretRefText + "]"
	// secretValue is what the fake infisical CLI resolves secretRefText to.
	secretValue = "s3cr3t-sentinel-7f3a9c-value"
)

// secretWorld is one test's whole filesystem: the repo tree, the runtime dir
// (logs), the data dir and TMPDIR (where every dispatch's scratch lives) all
// sit under root, which noSecretUnder walks. The fake infisical CLI — the one
// file that must hold the value — lives outside root.
type secretWorld struct {
	root, tree, runtime, data string
	infisicalArgv             string
}

// noSecretUnder fails when any file under the world's root, or any extra
// capture, holds the raw value. It returns how many files it read, so a test
// can show the walk reached what it meant to.
func (w secretWorld) noSecretUnder(t *testing.T, extra map[string][]byte) int {
	t.Helper()
	files := 0
	err := filepath.WalkDir(w.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		files++
		if bytes.Contains(b, []byte(secretValue)) {
			t.Errorf("%s holds the resolved secret:\n%s", p, b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range extra {
		if bytes.Contains(b, []byte(secretValue)) {
			t.Errorf("%s holds the resolved secret:\n%s", name, b)
		}
	}
	if b, err := os.ReadFile(w.infisicalArgv); err == nil && bytes.Contains(b, []byte(secretValue)) {
		t.Errorf("infisical argv holds the resolved secret:\n%s", b)
	}
	return files
}

// streamHarness is a fake interface=stream agent that echoes the value its
// environment carries (TOKEN) everywhere it can: an init model, a message,
// a tool call's shell command, a failed tool result, stderr and its final
// answer. It records its argv under the tree so the walk reads it too.
func streamHarness(t *testing.T, w secretWorld) string {
	t.Helper()
	path := filepath.Join(w.root, "harness.sh")
	script := `#!/bin/sh
printf '%s\n' "$@" > ` + filepath.Join(w.tree, "harness-argv") + `
read -r line
printf 'stderr val=%s\n' "$TOKEN" >&2
printf '{"type":"system","subtype":"init","model":"m-%s","tools":["Bash","t-%s"]}\n' "$TOKEN" "$TOKEN"
printf '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"say val=%s"}]}}\n' "$TOKEN"
printf '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"echo val=%s"}}]}}\n' "$TOKEN"
printf '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":"boom val=%s"}]}}\n' "$TOKEN"
printf '{"type":"result","result":"done val=%s","usage":{"input_tokens":1,"output_tokens":1}}\n' "$TOKEN"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// varsBindingEnv is the binding env exactly as the CLI wires it: `${TOKEN}` in
// the binding, the reference under [vars]. Wiring-time expansion leaves the
// reference in place; nothing is resolved until a dispatch.
func varsBindingEnv(t *testing.T) map[string]string {
	t.Helper()
	agents, err := config.ResolveAgentEnvs(config.AgentsConfig{
		Executor: config.AgentBinding{Command: "x", Env: map[string]string{"TOKEN": "${TOKEN}"}},
	}, map[string]string{"TOKEN": secretRefText})
	if err != nil {
		t.Fatal(err)
	}
	if got := agents.Executor.Env["TOKEN"]; got != secretRefText {
		t.Fatalf("wiring resolved the reference early: TOKEN=%q", got)
	}
	return agents.Executor.Env
}

type captured struct {
	mu   sync.Mutex
	rows [][]byte
}

func (c *captured) add(b []byte) {
	c.mu.Lock()
	c.rows = append(c.rows, b)
	c.mu.Unlock()
}

func (c *captured) all() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Join(c.rows, []byte("\n"))
}
