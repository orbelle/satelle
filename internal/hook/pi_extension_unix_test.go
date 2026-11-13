//go:build unix

package hook

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// piDriver loads the rendered extension, records its handlers on a fake pi,
// runs session_start with a ctx shaped by CASE_UI (notify, none or throw),
// then before_agent_start, and prints what the user was notified of, the
// system prompt the model got and whether a handler threw.
const piDriver = `import ext from "./satelle.mts";
const handlers = {};
const notified = [];
const pi = { on(name, fn) { handlers[name] = fn; }, sendUserMessage() {} };
ext(pi);
const ctx = { cwd: process.cwd() };
const mode = process.env.CASE_UI;
if (mode === "notify") ctx.ui = { notify(msg, level) { notified.push([msg, level]); } };
if (mode === "throw") ctx.ui = { notify() { throw new Error("no ui"); } };
let threw = false;
let systemPrompt = "";
try {
	await handlers.session_start({}, ctx);
	const r = await handlers.before_agent_start({ prompt: "p", systemPrompt: "base" }, ctx);
	systemPrompt = r?.systemPrompt ?? "";
} catch (e) {
	threw = true;
}
console.log(JSON.stringify({ notified, systemPrompt, threw }));
`

// pi's session_start shows the hook's systemMessage to the user through
// ctx.ui.notify, keeps additionalContext for the model, and never throws
// (sto_4d645aa4 AC4). It runs the rendered extension under node.
func TestPiExtensionNotifiesTheUserAtSessionStart(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command(node, "--experimental-strip-types", "-e", "").CombinedOutput(); err != nil {
		t.Skipf("node rejects --experimental-strip-types (needs 22.6 or later): %v\n%s", err, out)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\ncat >/dev/null\nif [ \"$2\" = \"" + VerbContext + "\" ]; then cat \"$FAKE_OUT\"; exit \"$FAKE_CODE\"; fi\nexit 0\n"
	for name, body := range map[string]string{
		filepath.Join(bin, "satelle"):     fake,
		filepath.Join(dir, "satelle.mts"): string(piExtension()),
		filepath.Join(dir, "driver.mjs"):  piDriver,
	} {
		if err := os.WriteFile(name, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const withMsg = `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"ctx"},"systemMessage":"warn"}`
	const noMsg = `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"ctx"}}`
	type result struct {
		Notified     [][]string `json:"notified"`
		SystemPrompt string     `json:"systemPrompt"`
		Threw        bool       `json:"threw"`
	}
	runCase := func(t *testing.T, envelope, code, ui string) result {
		t.Helper()
		out := filepath.Join(dir, "out.json")
		if err := os.WriteFile(out, []byte(envelope), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(node, "--experimental-strip-types", "driver.mjs")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"FAKE_OUT="+out, "FAKE_CODE="+code, "CASE_UI="+ui)
		stdout, err := cmd.Output()
		if err != nil {
			var stderr []byte
			if ee, ok := err.(*exec.ExitError); ok {
				stderr = ee.Stderr
			}
			t.Fatalf("node driver: %v\n%s\n%s", err, stdout, stderr)
		}
		var r result
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(stdout))), &r); err != nil {
			t.Fatalf("driver output: %v\n%s", err, stdout)
		}
		return r
	}

	t.Run("notifies the systemMessage", func(t *testing.T) {
		r := runCase(t, withMsg, "0", "notify")
		if len(r.Notified) != 1 || len(r.Notified[0]) != 2 || r.Notified[0][0] != "warn" || r.Notified[0][1] != "warning" {
			t.Errorf("notified %v, want [[warn warning]]", r.Notified)
		}
		if !strings.Contains(r.SystemPrompt, "ctx") || r.Threw {
			t.Errorf("model context lost or threw: %+v", r)
		}
	})
	t.Run("no systemMessage, no notice", func(t *testing.T) {
		r := runCase(t, noMsg, "0", "notify")
		if len(r.Notified) != 0 || !strings.Contains(r.SystemPrompt, "ctx") {
			t.Errorf("%+v", r)
		}
	})
	t.Run("no ui", func(t *testing.T) {
		r := runCase(t, withMsg, "0", "none")
		if r.Threw || !strings.Contains(r.SystemPrompt, "ctx") {
			t.Errorf("%+v", r)
		}
	})
	t.Run("failed hook, no notice", func(t *testing.T) {
		r := runCase(t, withMsg, "1", "notify")
		if len(r.Notified) != 0 || r.Threw {
			t.Errorf("%+v", r)
		}
	})
	t.Run("notify throws", func(t *testing.T) {
		r := runCase(t, withMsg, "0", "throw")
		if r.Threw || !strings.Contains(r.SystemPrompt, "ctx") {
			t.Errorf("%+v", r)
		}
	})
}
