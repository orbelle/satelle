package agentcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// acpAdapters are the spawn configurations that drive the one shared ACP
// client: the binary's name is what selects the adapter.
var acpAdapters = []struct{ name, binary string }{
	{"claude", "claude-acp"},
	{"generic", "acp-peer"},
}

// writeAdapterPeer writes a fake ACP peer named binary (so adapterOf picks the
// adapter), recording every initialize it receives to the returned log. extra
// runs at the start of session/prompt; a peer-side failure is reported as a
// JSON-RPC error that fails the run. An initialize that advertises a true
// capability is followed, in session/prompt, by that capability's request,
// which must not be answered -32601.
func writeAdapterPeer(t *testing.T, binary, extra string) (bin, initLog string) {
	t.Helper()
	skipWithoutPython3(t)
	dir := t.TempDir()
	bin = filepath.Join(dir, binary)
	initLog = filepath.Join(dir, "initialize.json")
	script := `#!/usr/bin/env python3
import json, sys

caps = {}

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

def read():
    line = sys.stdin.readline()
    if not line:
        return None
    return json.loads(line)

CAP_METHODS = {("fs", "readTextFile"): "fs/read_text_file", ("fs", "writeTextFile"): "fs/write_text_file", ("terminal", None): "terminal/create"}

while True:
    msg = read()
    if msg is None:
        break
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        caps = msg["params"].get("clientCapabilities", {})
        open("` + strings.ReplaceAll(initLog, `\`, `\\`) + `", "w").write(json.dumps(msg["params"]))
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}})
    elif method == "session/new":
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"sess_test","modes":{"currentModeId":"agent","availableModes":[{"id":"agent"},{"id":"plan"},{"id":"ask"}]}}})
    elif method == "session/prompt":
        for (group, key), m in CAP_METHODS.items():
            v = caps.get(group)
            on = (v is True) if key is None else bool(isinstance(v, dict) and v.get(key) is True)
            if on:
                send({"jsonrpc":"2.0","id":500,"method":m,"params":{"sessionId":"sess_test","path":"/x"}})
                resp = read()
                if resp is None or resp.get("error",{}).get("code") == -32601:
                    send({"jsonrpc":"2.0","id":mid,"error":{"code":1,"message":"advertised %s but answered %s" % (m, resp)}})
                    sys.exit(0)
` + extra + `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"{\"decision\":\"accept\",\"notes\":\"ok\"}"}}}})
        send({"jsonrpc":"2.0","id":mid,"result":{"stopReason":"end_turn"}})
    elif method == "session/cancel":
        pass
    elif mid is not None:
        send({"jsonrpc":"2.0","id":mid,"result":{}})
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, initLog
}

// runAdapterPeer runs a mutator-denying dispatch against bin and returns the
// interactive-denied events.
func runAdapterPeer(t *testing.T, bin string) []Event {
	t.Helper()
	r, err := RunnerFromBinding(InterfaceACP, bin+" acp")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var denied []Event
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := r.Run(ctx, Request{
		SystemPrompt: "r",
		Payload:      "{}",
		AllowedTools: "read_file,grep,list_dir",
		OnEvent: func(ev Event) {
			if ev.Kind == EventInteractiveDenied {
				mu.Lock()
				denied = append(denied, ev)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(out), "accept") {
		t.Fatalf("out = %q, want the verdict", out)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]Event(nil), denied...)
}

// AC1 + AC3: a denied tool call never gets an allow option, whatever the peer
// offered, for each adapter.
func TestACPClient_DenyNeverSelectsAllow(t *testing.T) {
	opt := func(id, kind string) map[string]any {
		return map[string]any{"optionId": id, "name": id, "kind": kind}
	}
	cases := []struct {
		name       string
		options    []map[string]any
		wantOption string // "" means the outcome must be cancelled
	}{
		{"only allow kinds", []map[string]any{opt("allow-once", "allow_once"), opt("allow-always", "allow_always")}, ""},
		{"only allow kinds, allow first and unknown last", []map[string]any{opt("allow-once", "allow_once"), opt("x", "weird")}, ""},
		{"unknown kinds", []map[string]any{opt("a", "frobnicate"), opt("b", "")}, ""},
		{"no options", []map[string]any{}, ""},
		{"reject_once among allow", []map[string]any{opt("allow-once", "allow_once"), opt("reject-once", "reject_once")}, "reject-once"},
		{"reject_always only", []map[string]any{opt("reject-always", "reject_always")}, "reject-always"},
	}
	for _, ad := range acpAdapters {
		for _, tc := range cases {
			t.Run(ad.name+"/"+tc.name, func(t *testing.T) {
				opts, err := json.Marshal(tc.options)
				if err != nil {
					t.Fatal(err)
				}
				check := `resp.get("result",{}).get("outcome",{}).get("outcome") != "cancelled" or "optionId" in resp.get("result",{}).get("outcome",{})`
				if tc.wantOption != "" {
					check = `resp.get("result",{}).get("outcome",{}) != {"outcome":"selected","optionId":"` + tc.wantOption + `"}`
				}
				extra := `
        send({"jsonrpc":"2.0","id":99,"method":"session/request_permission","params":{"sessionId":"sess_test","toolCall":{"toolCallId":"c1","kind":"edit","title":"Write"},"options":` + string(opts) + `}})
        resp = read()
        if resp is None or ` + check + `:
            send({"jsonrpc":"2.0","id":mid,"error":{"code":1,"message":"deny reply wrong: %s" % (resp,)}})
            continue
`
				bin, _ := writeAdapterPeer(t, ad.binary, extra)
				runAdapterPeer(t, bin)
			})
		}
	}
}

// AC2 + AC3: every capability the client advertises true is served, for each
// adapter. The peer itself sends each advertised capability's request and fails
// the run on -32601; this also checks the advertised set against the table the
// dispatch is driven from.
func TestACPClient_AdvertisesOnlyServedCapabilities(t *testing.T) {
	capMethods := map[string]string{
		"fs.readTextFile":  "fs/read_text_file",
		"fs.writeTextFile": "fs/write_text_file",
		"terminal":         "terminal/create",
	}
	for _, ad := range acpAdapters {
		t.Run(ad.name, func(t *testing.T) {
			bin, initLog := writeAdapterPeer(t, ad.binary, "")
			runAdapterPeer(t, bin)
			raw, err := os.ReadFile(initLog)
			if err != nil {
				t.Fatalf("initialize not captured: %v", err)
			}
			var init struct {
				ClientCapabilities struct {
					FS       map[string]bool `json:"fs"`
					Terminal bool            `json:"terminal"`
				} `json:"clientCapabilities"`
			}
			if err := json.Unmarshal(raw, &init); err != nil {
				t.Fatalf("initialize %s: %v", raw, err)
			}
			advertised := map[string]bool{
				"fs.readTextFile":  init.ClientCapabilities.FS["readTextFile"],
				"fs.writeTextFile": init.ClientCapabilities.FS["writeTextFile"],
				"terminal":         init.ClientCapabilities.Terminal,
			}
			if _, ok := init.ClientCapabilities.FS["readTextFile"]; !ok {
				t.Fatalf("initialize %s carries no fs.readTextFile", raw)
			}
			for capName, on := range advertised {
				if _, served := acpServedMethods[capMethods[capName]]; on && !served {
					t.Errorf("capability %s advertised true but %s has no handler", capName, capMethods[capName])
				}
			}
		})
	}
}
