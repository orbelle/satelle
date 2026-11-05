package agentstep

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// maxArgStrlen is Linux MAX_ARG_STRLEN. It binds one argv token. The work
// item a judging reviewer receives stays under it on every seat.
const maxArgStrlen = 128 << 10

// boundShot is one Gate call's referenced work item, captured inside Run
// before a successful review deletes the scratch directory.
type boundShot struct {
	payload string
	opened  map[string][]byte
}

// boundObserver delegates to a real runner and snapshots the work item the
// runner was about to send.
type boundObserver struct {
	inner agentcli.Runner
	shots []boundShot
}

func (b *boundObserver) Name() string    { return b.inner.Name() }
func (b *boundObserver) Command() string { return b.inner.Command() }
func (b *boundObserver) Run(ctx context.Context, req agentcli.Request) ([]byte, error) {
	b.shots = append(b.shots, boundShot{payload: req.Payload, opened: openPayloadFiles(req.Payload)})
	return b.inner.Run(ctx, req)
}

// boundMatter is the pre-cap material the resolvers return. plan, patch and
// one history body grow between the two Gate calls; the work item must not.
type boundMatter struct {
	plan  string
	patch string
	msg   string
	notes string
	edit  string
}

func overArg(marker string) string {
	return marker + strings.Repeat("x", maxArgStrlen)
}

func boundWorkflow() string {
	return spineWF("", "cancelled", "",
		"in_progress|executor|",
		"done|||bound-review")
}

func boundItem() workitem.Item {
	return workitem.Item{
		ID:                 "sty_bound",
		Status:             "in_progress",
		Title:              "bounded review",
		Body:               "short body",
		AcceptanceCriteria: "1. short criterion",
	}
}

// newBoundEngine wires the real runner through decideReviewer → Invoke.
// repoRoot is stable across the two Gate calls so scratch path lengths match.
func newBoundEngine(t *testing.T, repo string, runner agentcli.Runner, matter *boundMatter, env map[string]string) *Engine {
	t.Helper()
	g := New(runner, fakeDocs{workflow: boundWorkflow(), skillBody: "Judge the work item.", skillFound: true}, repo, "")
	g.SetInjectPrinciples(false)
	g.SetReviewerTools("Read,Grep,Glob")
	g.idleTimeout = 15 * time.Second
	g.attempts = 1
	g.backoff = func(int) time.Duration { return 0 }
	g.warnOut = io.Discard
	if len(env) > 0 {
		g.SetReviewerEnv(env)
	}
	g.SetChildrenResolver(func(context.Context, workitem.Item) []ChildState {
		return []ChildState{{ID: "sty_child", Status: "done"}}
	})
	g.SetDocsResolver(func(context.Context, string) []DocState {
		return []DocState{{Name: "plan", Type: "plan", Body: matter.plan}}
	})
	g.SetDiffResolver(func(context.Context, string) *DiffState {
		return &DiffState{Baseline: "abc", Files: []string{"a.go"}, Stat: "1 file", Patch: matter.patch, Source: "engagement"}
	})
	g.SetMessagesResolver(func(context.Context, string, []string) []MessageState {
		return []MessageState{{ID: "msg1", From: "coder", To: "reviewer", Body: matter.msg}}
	})
	g.SetPriorVerdictsResolver(func(context.Context, string, string, string, verb.StaleExemption) []PriorVerdict {
		return []PriorVerdict{{Skill: "bound-review", Decision: "reject", Notes: matter.notes}}
	})
	g.SetDefinitionEditsResolver(func(context.Context, string) []DefinitionEdit {
		return []DefinitionEdit{{Field: "body", Old: matter.edit, New: "short", Actor: "driver"}}
	})
	return g
}

func requirePython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
}

func writeExec(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

const commandStubScript = `#!/usr/bin/env python3
import json, os, sys
data = sys.stdin.buffer.read()
if os.environ.get("ARGV_OUT"):
    with open(os.environ["ARGV_OUT"], "w") as f:
        json.dump(sys.argv, f)
if os.environ.get("STDIN_OUT"):
    with open(os.environ["STDIN_OUT"], "wb") as f:
        f.write(data)
sys.stdout.write('{"decision":"accept","notes":"ok"}\n')
`

const streamPeerScript = `#!/usr/bin/env python3
import json, sys

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

def read():
    line = sys.stdin.readline()
    if not line:
        return None
    return json.loads(line)

while True:
    msg = read()
    if msg is None:
        break
    if msg.get("type") != "user":
        continue
    send({"type": "assistant", "message": {"role": "assistant", "content": [{"type": "text", "text": "ok"}]}})
    send({"type": "result", "result": "{\"decision\":\"accept\",\"notes\":\"ok\"}"})
    break
`

const acpPeerScript = `#!/usr/bin/env python3
import json, os, sys

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

def read():
    line = sys.stdin.readline()
    if not line:
        return None
    return json.loads(line)

final = "{\"decision\":\"accept\",\"notes\":\"ok\"}"
while True:
    msg = read()
    if msg is None:
        break
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1, "agentCapabilities": {}, "authMethods": [{"id": "cached_token"}]}})
    elif method == "authenticate":
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
    elif method == "session/new":
        send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": "sess_test"}})
    elif method == "session/prompt":
        params = msg.get("params") or {}
        prompt = params.get("prompt") or []
        path = os.environ.get("PAYLOAD_BLOCK", "")
        if path and len(prompt) > 1 and isinstance(prompt[1], dict):
            with open(path, "w") as f:
                f.write(prompt[1].get("text") or "")
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "sess_test", "update": {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": final}}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
    elif method == "session/cancel":
        pass
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
`

func prependPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// TestReviewerVerdictWhenMaterialExceedsArgLimit is AC1–AC3: one Gate per
// adapter, through the real runner, on a story whose plan, diff and history
// are each larger than 128 KiB. The verdict is Accept with PayloadBytes equal
// to the referenced work item. Growing those bodies does not grow it.
func TestReviewerVerdictWhenMaterialExceedsArgLimit(t *testing.T) {
	requirePython(t)
	repo := t.TempDir()
	st := openEngineLedger(t)
	verb.SetLedgerStore(st)
	t.Cleanup(func() { verb.SetLedgerStore(nil) })
	if _, err := st.Append(context.Background(), ledger.AppendInput{
		StoryID: "sty_bound", Kind: ledger.KindComment, Body: "LEDGER-MARKER",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	matter := &boundMatter{
		plan:  overArg("PLAN-MARKER-"),
		patch: overArg("PATCH-MARKER-"),
		msg:   overArg("MSG-MARKER-"),
		notes: overArg("VERDICT-MARKER-"),
		edit:  overArg("EDIT-MARKER-"),
	}

	type seat struct {
		name    string
		command string
		iface   string
		bin     string
		script  string
		// block is the ACP payload-block file. command seats use argv and stdin.
		block bool
		turn  bool
	}
	seats := []seat{
		{name: "claude command", iface: agentcli.InterfaceCommand, command: agentcli.DefaultClaudeCommand, bin: "claude", script: commandStubScript},
		{name: "grok command", iface: agentcli.InterfaceCommand, command: "pi -p {payload}", bin: "pi", script: commandStubScript},
		{name: "claude stream", iface: agentcli.InterfaceStream, command: agentcli.DefaultClaudeStreamCommand, bin: "claude", script: streamPeerScript, turn: true},
	}
	for _, seat := range seats {
		t.Run(seat.name, func(t *testing.T) {
			dir := t.TempDir()
			writeExec(t, dir, seat.bin, seat.script)
			prependPath(t, dir)
			argvPath := filepath.Join(dir, "argv.json")
			stdinPath := filepath.Join(dir, "stdin.bin")
			blockPath := filepath.Join(dir, "block.txt")
			env := map[string]string{}
			if seat.script == commandStubScript {
				env["ARGV_OUT"] = argvPath
				env["STDIN_OUT"] = stdinPath
			}
			command := seat.command
			if seat.iface == agentcli.InterfaceACP {
				command = filepath.Join(dir, seat.bin) + " stdio"
			}
			inner, err := agentcli.RunnerFromBinding(seat.iface, command)
			if err != nil {
				t.Fatal(err)
			}
			obs := &boundObserver{inner: inner}
			if seat.block {
				env["PAYLOAD_BLOCK"] = blockPath
			}
			g := newBoundEngine(t, repo, obs, matter, env)

			var turns []string
			if seat.turn {
				agentcli.SetTurnObserver(func(turn agentcli.Turn) { turns = append(turns, turn.Text) })
				t.Cleanup(func() { agentcli.SetTurnObserver(nil) })
			}

			item := boundItem()
			dec, err := g.Gate(context.Background(), item, "done")
			if err != nil {
				t.Fatalf("gate: %v", err)
			}
			if len(obs.shots) != 1 {
				t.Fatalf("runner calls = %d, want 1", len(obs.shots))
			}
			first := obs.shots[0]
			assertBoundVerdict(t, dec, first.payload)
			assertBoundShape(t, first, matter)
			assertBoundChannel(t, seat.name, first.payload, argvPath, stdinPath, blockPath, turns)

			matter.plan += matter.plan
			matter.patch += matter.patch
			matter.msg += matter.msg
			dec2, err := g.Gate(context.Background(), item, "done")
			if err != nil {
				t.Fatalf("grown gate: %v", err)
			}
			if len(obs.shots) != 2 {
				t.Fatalf("runner calls = %d, want 2", len(obs.shots))
			}
			second := obs.shots[1]
			assertBoundVerdict(t, dec2, second.payload)
			if len(second.payload) != len(first.payload) {
				t.Fatalf("growing the plan, the patch and a history body changed the work item from %d to %d bytes", len(first.payload), len(second.payload))
			}
			if strings.Contains(second.payload, "PLAN-MARKER-") || strings.Contains(second.payload, "MSG-MARKER-") {
				t.Fatal("the grown bodies rode in the work item")
			}
			// Restore so the next seat starts from the same sizes. Path
			// lengths, not body lengths, decide the work-item length.
			matter.plan = matter.plan[:len(matter.plan)/2]
			matter.patch = matter.patch[:len(matter.patch)/2]
			matter.msg = matter.msg[:len(matter.msg)/2]
		})
	}
}

func assertBoundVerdict(t *testing.T, dec verb.GateDecision, payload string) {
	t.Helper()
	if !dec.Accept {
		t.Fatalf("Accept = false, notes %q", dec.Notes)
	}
	if dec.PayloadBytes != len(payload) {
		t.Fatalf("PayloadBytes = %d, work item length = %d", dec.PayloadBytes, len(payload))
	}
	if len(payload) >= maxArgStrlen {
		t.Fatalf("work item is %d bytes, want < %d", len(payload), maxArgStrlen)
	}
}

func assertBoundShape(t *testing.T, shot boundShot, matter *boundMatter) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(shot.payload), &top); err != nil {
		t.Fatal(err)
	}
	var story workitem.Item
	if err := json.Unmarshal(top["story"], &story); err != nil {
		t.Fatal(err)
	}
	if story.Body != "short body" || story.AcceptanceCriteria != "1. short criterion" {
		t.Fatalf("story inline = body %q ac %q", story.Body, story.AcceptanceCriteria)
	}
	var from, to, skill string
	mustJSON(t, top["from"], &from)
	mustJSON(t, top["to"], &to)
	mustJSON(t, top["review_skill"], &skill)
	if from != "in_progress" || to != "done" || skill != "bound-review" {
		t.Fatalf("edge = %s → %s skill %s", from, to, skill)
	}
	var kids []ChildState
	mustJSON(t, top["children"], &kids)
	if len(kids) != 1 || kids[0].ID != "sty_child" {
		t.Fatalf("children = %+v", kids)
	}
	for _, marker := range []string{"PLAN-MARKER-", "PATCH-MARKER-", "MSG-MARKER-", "VERDICT-MARKER-", "EDIT-MARKER-"} {
		if strings.Contains(shot.payload, marker) {
			t.Fatalf("work item contains %s", marker)
		}
	}
	docs := openedDocs(t, shot.payload, shot.opened)
	if docs["plan"] != matter.plan {
		t.Fatalf("plan file length %d, want %d", len(docs["plan"]), len(matter.plan))
	}
	var diff DiffState
	if err := json.Unmarshal(openedAt(t, shot.opened, shot.payload, "diff"), &diff); err != nil {
		t.Fatal(err)
	}
	if diff.Patch != matter.patch || len(diff.Files) != 1 || diff.Files[0] != "a.go" || diff.Stat != "1 file" {
		t.Fatalf("diff file = files %v stat %q patch %d", diff.Files, diff.Stat, len(diff.Patch))
	}
	var msgs []MessageState
	if err := json.Unmarshal(openedAt(t, shot.opened, shot.payload, "messages"), &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Body != matter.msg {
		t.Fatalf("messages file length %d", len(msgs))
	}
	var verdicts []PriorVerdict
	if err := json.Unmarshal(openedAt(t, shot.opened, shot.payload, "prior_verdicts"), &verdicts); err != nil {
		t.Fatal(err)
	}
	if len(verdicts) != 1 || verdicts[0].Notes != matter.notes || verdicts[0].Attempt != 1 {
		t.Fatalf("prior verdicts = %+v", verdicts)
	}
	var edits []DefinitionEdit
	if err := json.Unmarshal(openedAt(t, shot.opened, shot.payload, "definition_edits"), &edits); err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0].Old != matter.edit {
		t.Fatalf("definition edits old length %d", len(edits))
	}
	ledgerFile := openedAt(t, shot.opened, shot.payload, "ledger")
	if !strings.Contains(string(ledgerFile), "LEDGER-MARKER") {
		t.Fatalf("ledger file = %s", ledgerFile)
	}
}

func assertBoundChannel(t *testing.T, seat, payload, argvPath, stdinPath, blockPath string, turns []string) {
	t.Helper()
	switch seat {
	case "claude command":
		stdin := readFile(t, stdinPath)
		if string(stdin) != payload {
			t.Fatalf("stdin length %d, work item %d", len(stdin), len(payload))
		}
		var argv []string
		mustJSON(t, readFile(t, argvPath), &argv)
		for _, a := range argv {
			if a == payload || strings.Contains(a, "PLAN-MARKER-") {
				t.Fatalf("argv element is the work item (%d bytes)", len(a))
			}
		}
	case "grok command":
		stdin := readFile(t, stdinPath)
		if string(stdin) != payload {
			t.Fatal("grok stdin is not the work item")
		}
		var argv []string
		mustJSON(t, readFile(t, argvPath), &argv)
		found := false
		for _, a := range argv {
			if a == payload {
				found = true
			}
		}
		if !found {
			t.Fatal("{payload} argv token is not the work item")
		}
		if len(stdin) >= maxArgStrlen || len(payload) >= maxArgStrlen {
			t.Fatalf("grok work item %d bytes", len(payload))
		}
	case "claude stream":
		if len(turns) != 1 || turns[0] != payload {
			t.Fatalf("turn.Text calls = %d, equal = %v", len(turns), len(turns) == 1 && turns[0] == payload)
		}
		if strings.Contains(turns[0], "isolated satelle reviewer") {
			t.Fatal("turn.Text includes the system prompt")
		}
	case "grok acp":
		if len(turns) != 1 || turns[0] != payload {
			t.Fatalf("turn.Text is not the work item (%d calls)", len(turns))
		}
		block := readFile(t, blockPath)
		if string(block) != turns[0] {
			t.Fatalf("payload text block length %d, turn.Text %d", len(block), len(turns[0]))
		}
		if strings.Contains(string(block), "isolated satelle reviewer") {
			t.Fatal("payload text block includes the system prompt")
		}
	default:
		t.Fatalf("unknown seat %s", seat)
	}
}

func mustJSON(t *testing.T, raw []byte, dest any) {
	t.Helper()
	if err := json.Unmarshal(raw, dest); err != nil {
		t.Fatalf("json: %v", err)
	}
}

// TestCheckStdinStaysCapped is the functional-check half of AC3: the check
// still receives today's capped JSON, including a doc body, and a body past
// the ceiling is truncated rather than inlined.
func TestCheckStdinStaysCapped(t *testing.T) {
	wf := spineWF("", "cancelled", "",
		"in_progress|executor|",
		"done|||cap-check")
	small := "SMALL-PLAN-BODY"
	huge := "HUGE-DOC-" + strings.Repeat("h", docsPayloadCeiling)
	item := workitem.Item{ID: "sty_cap", Status: "in_progress", Title: "cap", Body: "body", AcceptanceCriteria: "1. a"}
	stdin := cappedGateStdin(t, wf, item, "done", func(g *Engine) {
		g.SetDocsResolver(func(context.Context, string) []DocState {
			return []DocState{
				{Name: "plan", Type: "plan", Body: small},
				{Name: "overflow", Type: "note", Body: huge},
			}
		})
	})
	if !strings.Contains(stdin, `"body"`) || !strings.Contains(stdin, small) {
		t.Fatal("check stdin lost the doc body that still fits")
	}
	if strings.Contains(stdin, "HUGE-DOC-") {
		t.Fatal("check stdin carried the body past the ceiling")
	}
	if !strings.Contains(stdin, `"truncated":true`) {
		t.Fatal("check stdin did not mark the overflow truncated")
	}
}

// TestJudgingLedgerUnavailableWhenStoreUnset records an adapter-named
// unavailable when the ledger list cannot run. The key is still present.
func TestJudgingLedgerUnavailableWhenStoreUnset(t *testing.T) {
	verb.SetLedgerStore(nil)
	t.Cleanup(func() { verb.SetLedgerStore(nil) })
	g, r := newEngine(t, `{"decision":"accept","notes":"ok"}`, fakeDocs{workflow: boundWorkflow(), skillBody: "Judge the work item.", skillFound: true})
	g.SetInjectPrinciples(false)
	if _, err := g.Gate(context.Background(), boundItem(), "done"); err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.got.Payload), &top); err != nil {
		t.Fatal(err)
	}
	raw, ok := top["ledger"]
	if !ok {
		t.Fatal("judging payload omitted ledger")
	}
	var ref struct {
		Path        string `json:"path"`
		Unavailable string `json:"unavailable"`
	}
	mustJSON(t, raw, &ref)
	if ref.Path != "" || !strings.Contains(ref.Unavailable, "ledger:") || !strings.Contains(ref.Unavailable, "verb: store not configured") {
		t.Fatalf("ledger = %+v", ref)
	}
}
