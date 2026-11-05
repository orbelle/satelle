package agentstep

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/secretref"
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

func newSecretWorld(t *testing.T, fail bool) secretWorld {
	t.Helper()
	bin := t.TempDir()
	argv := filepath.Join(bin, "argv")
	body := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done >> " + argv + "\n"
	if fail {
		body += "printf 'denied %s\\n' '" + secretValue + "' >&2\nexit 1\n"
	} else {
		body += "printf '%s\\n' '" + secretValue + "'\n"
	}
	if err := os.WriteFile(filepath.Join(bin, secretref.Binary), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	w := secretWorld{
		root: root, tree: filepath.Join(root, "tree"), runtime: filepath.Join(root, "runtime"),
		data: filepath.Join(root, "data"), infisicalArgv: argv,
	}
	for _, d := range []string{w.tree, filepath.Join(w.tree, ".satelle"), w.runtime, w.data, filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	return w
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

// AC1 + AC2: a task input holding a reference reaches a functional check as
// SATELLE_INPUT_<NAME>, and the check's output is scrubbed of it.
func TestSecretRefTaskInputReachesFunctionalCheck(t *testing.T) {
	w := newSecretWorld(t, false)
	item := runWithInputs()
	item.InputValues["api_token"] = secretRefText
	out, err := execCheck(withGateItem(context.Background(), item), w.tree,
		`printf 'input=%s\n' "$SATELLE_INPUT_API_TOKEN"; [ "$SATELLE_INPUT_API_TOKEN" != "`+secretRefText+`" ]`, "{}")
	if err != nil {
		t.Fatalf("execCheck: %v\n%s", err, out)
	}
	if out != "input="+secretMarker+"\n" {
		t.Errorf("check output = %q, want the value resolved and then scrubbed", out)
	}
	if item.InputValues["api_token"] != secretRefText {
		t.Error("the execution's inputs must keep the reference")
	}
	// The in-loop prompt shows the reference, never the value.
	if inl := inLoopInputs(item); !strings.Contains(inl, "export SATELLE_INPUT_API_TOKEN='"+secretRefText+"'") {
		t.Errorf("in-loop inputs must carry the reference:\n%s", inl)
	}
	w.noSecretUnder(t, map[string][]byte{"check output": []byte(out)})
}

// AC3: an unresolvable reference fails the action that needs it — the
// dispatch never starts its child, the check never runs — naming the reference
// and the sign-in, never the value.
func TestSecretRefUnresolvableFailsTheAction(t *testing.T) {
	w := newSecretWorld(t, true)
	wantMsg := func(t *testing.T, what string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: want an error", what)
		}
		var se *secretref.Error
		if !errors.As(err, &se) {
			t.Errorf("%s: %v is not a *secretref.Error", what, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, secretRefText) || !strings.Contains(msg, "infisical login") || strings.Contains(msg, secretValue) {
			t.Errorf("%s: message must name the reference and the sign-in, never the value: %s", what, msg)
		}
	}

	ran := filepath.Join(w.root, "ran")
	script := filepath.Join(w.root, "agent.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+ran+"\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner, err := agentcli.RunnerFromBinding(agentcli.InterfaceCommand, script+" --noop")
	if err != nil {
		t.Fatal(err)
	}
	g := New(runner, fakeDocs{}, w.tree, "")
	res := g.Invoke(context.Background(), InvokeRequest{
		Binding: config.AgentBinding{Command: script + " --noop", Principles: config.PrinciplesNone, Env: varsBindingEnv(t)},
		Section: "coder", Rubric: "do it", Payload: map[string]string{}, Expect: ExpectPerform, Runner: runner, StoryID: "sty_1",
	})
	wantMsg(t, "dispatch", res.Err)
	if _, err := os.Stat(ran); err == nil {
		t.Error("the child ran despite an unresolvable reference")
	}

	item := runWithInputs()
	item.InputValues["api_token"] = secretRefText
	out, err := execCheck(withGateItem(context.Background(), item), w.tree, "touch "+ran, "{}")
	wantMsg(t, "check", err)
	if _, serr := os.Stat(ran); serr == nil {
		t.Error("the check ran despite an unresolvable reference")
	}
	// Through a gate, the error is the check's notes: the gate rejects naming it.
	g2, _ := newEngine(t, "", fakeDocs{workflow: testWorkflow, skillBody: capCheckSkill, skillFound: true})
	dec, gerr := g2.Gate(context.Background(), item, "done")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if dec.Accept || !strings.Contains(dec.Notes, secretRefText) || strings.Contains(dec.Notes, secretValue) {
		t.Errorf("the gate must reject naming the reference: %+v", dec)
	}
	w.noSecretUnder(t, map[string][]byte{"check output": []byte(out)})
}
