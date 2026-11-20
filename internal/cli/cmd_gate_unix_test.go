//go:build !windows

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeJudge(t *testing.T, verdict, notes string) string {
	t.Helper()
	bin, _ := fakeJudgeWithStdin(t, verdict, notes)
	return bin
}

func fakeJudgeWithStdin(t *testing.T, verdict, notes string) (string, string) {
	t.Helper()
	answer, _ := json.Marshal(map[string]string{"verdict": verdict, "notes": notes})
	result, _ := json.Marshal(map[string]any{"result": "judged\n" + string(answer), "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	bin := filepath.Join(t.TempDir(), "claude")
	stdinPath := bin + ".stdin"
	script := "#!/bin/sh\ncat > \"$0.stdin\"\nprintf '%s\\n' '" + string(result) + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, stdinPath
}

func writeDryWorkflow(t *testing.T, dir string) {
	t.Helper()
	agents, err := os.ReadFile(filepath.Join(dir, ".satelle", "agents.toml"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".satelle/agents.toml": string(agents) + `
[agents.judge-yes]
command = "` + fakeJudge(t, "accept", "looks right\nsecond line of notes") + `"
model = "fake-yes"
tools = ["Read"]
[agents.judge-no]
command = "` + fakeJudge(t, "reject", "AC1: missing") + `"
model = "fake-no"
tools = ["Read"]
`,
		".satelle/workflows/dry.toml": `name = "dry"
[states.st-a]
start = true
[states.st-b]
terminal = true
[gates.ok]
skill = "dry-ok"
[gates.bad]
skill = "dry-bad"
[gates.one]
skill = "dry-review"
agent = "judge-yes"
diff_max_bytes = 1024
[gates.split]
skill = "dry-review"
panel = ["judge-yes", "judge-no"]
[[transition]]
from = ["st-a"]
to = "st-b"
groups = [{ mode = "parallel", gates = ["ok", "bad", "one", "split"] }]
`,
		".satelle/skills/dry-ok.md":     "---\ntype: check\n---\n```sh\necho first line\necho last line\n```\n",
		".satelle/skills/dry-bad.md":    "---\ntype: check\n---\n```sh\necho the check failed here\nexit 1\n```\n",
		".satelle/skills/dry-review.md": "---\nname: dry-review\n---\nReview it.\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func noGateRun(t *testing.T, id string) {
	t.Helper()
	if out := must(t, "ledger", "list", id); strings.Contains(out, "gate.run") {
		t.Fatalf("a dry run recorded a gate.run:\n%s", out)
	}
}

func TestGateRunPrintsTheResult(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeDryWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "dry")

	out := must(t, "gate", "run", "dry/ok", "--item", id, "--from", "st-a")
	for _, want := range []string{"gate ok  accept  (st-a -> st-b; dry run, nothing recorded)", "first line\nlast line"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ok: no %q in\n%s", want, out)
		}
	}

	out, err := run(t, "", "gate", "run", "dry/bad", "--item", id, "--from", "st-a")
	if ExitCodeOf(err) != ExitBlocked || !strings.Contains(out, "gate bad  reject") || !strings.Contains(out, "the check failed here") {
		t.Fatalf("bad: exit %d\n%s", ExitCodeOf(err), out)
	}

	out = must(t, "gate", "run", "dry/one", "--item", id, "--from", "st-a")
	if !strings.Contains(out, "gate one  accept") || !strings.Contains(out, "judge judge-yes") || !strings.Contains(out, "looks right\nsecond line of notes") {
		t.Fatalf("one:\n%s", out)
	}

	out, err = run(t, "", "gate", "run", "dry/split", "--item", id, "--from", "st-a")
	for _, want := range []string{"gate split  reject", "  judge-yes  accept", "  judge-no  reject", "panel (all): 1 of 2 accept", "AC1: missing"} {
		if !strings.Contains(out, want) {
			t.Fatalf("split: no %q in\n%s", want, out)
		}
	}
	if ExitCodeOf(err) != ExitBlocked {
		t.Fatalf("split exit %d", ExitCodeOf(err))
	}
	noGateRun(t, id)
}

func TestGateRunShowInput(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeDryWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "dry")

	out := must(t, "gate", "run", "dry/bad", "--item", id, "--from", "st-a", "--show-input")
	for _, want := range []string{"gate bad  check", "SATELLE_ITEM=" + id, "SATELLE_FROM=st-a", `stdin: {"base":`, `"id":"` + id + `"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("check input: no %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "the check failed here") {
		t.Fatalf("the check ran:\n%s", out)
	}
	if strings.Contains(out, "diff_max_bytes:") {
		t.Fatalf("check input printed diff_max_bytes:\n%s", out)
	}

	out = must(t, "gate", "run", "dry/one", "--item", id, "--from", "st-a", "--show-input")
	for _, want := range []string{"gate one  judge", "diff_max_bytes: 1024 (gates.one diff_max_bytes)", "judges: judge-yes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("one judge input: no %q in\n%s", want, out)
		}
	}

	out = must(t, "gate", "run", "dry/split", "--item", id, "--from", "st-a", "--show-input")
	for _, want := range []string{"gate split  judge", "diff_max_bytes: 64000 (default; set diff_max_bytes on [gates.split] to change it)", "judges: judge-yes, judge-no", "prompt:\nJudge item " + id + " moving from"} {
		if !strings.Contains(out, want) {
			t.Fatalf("judge input: no %q in\n%s", want, out)
		}
	}
	noGateRun(t, id)
}

func TestGateRunRefusesAMalformedCall(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeDryWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "dry")
	if _, err := run(t, "", "gate", "run", "ok", "--item", id); err == nil || !strings.Contains(err.Error(), "<workflow>/<gate>") {
		t.Fatalf("no workflow: %v", err)
	}
	if _, err := run(t, "", "gate", "run", "dry/ok"); err == nil || !strings.Contains(err.Error(), "--item") {
		t.Fatalf("no item: %v", err)
	}
}

func TestGateRunLedgerRecordsEffortAndPromptBytes(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	highBin, highStdin := fakeJudgeWithStdin(t, "accept", "single ok")
	medBin, medStdin := fakeJudgeWithStdin(t, "accept", "med ok")
	noneBin, noneStdin := fakeJudgeWithStdin(t, "accept", "none ok")
	agents, err := os.ReadFile(filepath.Join(dir, ".satelle", "agents.toml"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".satelle/agents.toml": string(agents) + `
[agents.judge-high]
command = "` + highBin + `"
model = "fake-high"
effort = "high"
tools = ["Read"]
[agents.judge-med]
command = "` + medBin + `"
model = "fake-med"
effort = "medium"
tools = ["Read"]
[agents.judge-none]
command = "` + noneBin + `"
model = "fake-none"
tools = ["Read"]
`,
		".satelle/workflows/recorded.toml": `name = "recorded"
[states.st-a]
start = true
[states.st-b]
terminal = true
[gates.chk]
skill = "rec-ok"
[gates.single]
skill = "rec-review"
agent = "judge-high"
[gates.panel]
skill = "rec-review"
panel = ["judge-med", "judge-none"]
combine = "all"
[[transition]]
from = ["st-a"]
to = "st-b"
groups = [{ mode = "sequential", gates = ["chk", "single", "panel"] }]
`,
		".satelle/skills/rec-ok.md":     "---\ntype: check\n---\n```sh\necho ok\n```\n",
		".satelle/skills/rec-review.md": "---\nname: rec-review\n---\nReview it.\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	id := createID(t, "story", "create", "--workflow", "recorded", "--title", "record effort —é")
	must(t, "story", "set", id, "--status", "st-b")

	stdinBytes := func(path string) int {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read stdin capture %s: %v", path, err)
		}
		if len(b) == 0 {
			t.Fatalf("captured stdin %s is empty", path)
		}
		return len(b)
	}
	highBytes := stdinBytes(highStdin)
	medBytes := stdinBytes(medStdin)
	noneBytes := stdinBytes(noneStdin)

	out := must(t, "ledger", "list", id, "--kind", "gate.run", "--json")
	var events []struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(out), &events); err != nil {
		t.Fatalf("unmarshal gate.run events: %v\n%s", err, out)
	}
	byGate := map[string]map[string]any{}
	for _, ev := range events {
		name, _ := ev.Payload["gate"].(string)
		byGate[name] = ev.Payload
	}
	if len(byGate) != 3 {
		t.Fatalf("expected 3 gate.run events, got %d:\n%s", len(byGate), out)
	}

	chk := byGate["chk"]
	for _, key := range []string{"effort", "prompt_bytes"} {
		if _, has := chk[key]; has {
			t.Errorf("check gate.run has %q: %v", key, chk)
		}
	}

	single := byGate["single"]
	if single["effort"] != "high" {
		t.Errorf("single gate.run effort = %v, want high", single["effort"])
	}
	if single["prompt_bytes"] != float64(highBytes) {
		t.Errorf("single gate.run prompt_bytes = %v, want %d", single["prompt_bytes"], highBytes)
	}

	panel := byGate["panel"]
	for _, key := range []string{"effort", "prompt_bytes"} {
		if _, has := panel[key]; has {
			t.Errorf("panel top-level gate.run has %q: %v", key, panel)
		}
	}
	members, ok := panel["members"].([]any)
	if !ok || len(members) != 2 {
		t.Fatalf("panel gate.run members = %v", panel["members"])
	}
	m0 := members[0].(map[string]any)
	if m0["binding"] != "judge-med" || m0["effort"] != "medium" || m0["prompt_bytes"] != float64(medBytes) {
		t.Errorf("panel member 0 = %v, want binding=judge-med effort=medium prompt_bytes=%d", m0, medBytes)
	}
	m1 := members[1].(map[string]any)
	if _, has := m1["effort"]; has {
		t.Errorf("panel member 1 without effort has effort key: %v", m1)
	}
	if m1["binding"] != "judge-none" || m1["prompt_bytes"] != float64(noneBytes) {
		t.Errorf("panel member 1 = %v, want binding=judge-none prompt_bytes=%d", m1, noneBytes)
	}
}
