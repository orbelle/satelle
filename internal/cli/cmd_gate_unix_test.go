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
	return fakeJudgeCapture(t, verdict, notes, "/dev/null")
}

func fakeJudgeCapture(t *testing.T, verdict, notes, dst string) string {
	t.Helper()
	answer, _ := json.Marshal(map[string]string{"verdict": verdict, "notes": notes})
	result, _ := json.Marshal(map[string]any{"result": "judged\n" + string(answer), "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\ncat >'" + dst + "'\nprintf '%s\\n' '" + string(result) + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
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

	out = must(t, "gate", "run", "dry/split", "--item", id, "--from", "st-a", "--show-input")
	for _, want := range []string{"gate split  judge", "judges: judge-yes, judge-no", "prompt:\nJudge item " + id + " moving from"} {
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

func TestGateRunJudgeBriefDeliveredOnStdin(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	agents, err := os.ReadFile(filepath.Join(dir, ".satelle", "agents.toml"))
	if err != nil {
		t.Fatal(err)
	}
	briefedCap := filepath.Join(t.TempDir(), "briefed.txt")
	plainCap := filepath.Join(t.TempDir(), "plain.txt")
	files := map[string]string{
		".satelle/agents.toml": string(agents) + `
[agents.judge-briefed]
command = "` + fakeJudgeCapture(t, "accept", "ok", briefedCap) + `"
model = "fake-briefed"
tools = ["Read"]
brief = "Read only the changed files and what they touch."
[agents.judge-plain]
command = "` + fakeJudgeCapture(t, "accept", "ok", plainCap) + `"
model = "fake-plain"
tools = ["Read"]
`,
		".satelle/workflows/brief.toml": `name = "brief"
[states.st-a]
start = true
[states.st-b]
terminal = true
[gates.briefed]
skill = "dry-review"
agent = "judge-briefed"
[gates.plain]
skill = "dry-review"
agent = "judge-plain"
[gates.mixed]
skill = "dry-review"
panel = ["judge-briefed", "judge-plain"]
[[transition]]
from = ["st-a"]
to = "st-b"
groups = [{ mode = "parallel", gates = ["briefed", "plain", "mixed"] }]
`,
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
	id := createID(t, "story", "create", "--title", "brief check")

	must(t, "gate", "run", "brief/briefed", "--item", id, "--from", "st-a")
	briefedBytes, err := os.ReadFile(briefedCap)
	if err != nil {
		t.Fatal(err)
	}
	wantBriefed := "Judge item " + id + " moving from \"st-a\" to \"st-b\".\n\nBrief:\nRead only the changed files and what they touch.\n\nKind: story\n"
	if !strings.HasPrefix(string(briefedBytes), wantBriefed) {
		t.Fatalf("briefed stdin:\n%s\nwant prefix:\n%s", briefedBytes, wantBriefed)
	}

	must(t, "gate", "run", "brief/plain", "--item", id, "--from", "st-a")
	plainBytes, err := os.ReadFile(plainCap)
	if err != nil {
		t.Fatal(err)
	}
	wantPlain := "Judge item " + id + " moving from \"st-a\" to \"st-b\".\n\nKind: story\n"
	if strings.Contains(string(plainBytes), "Brief:") || !strings.HasPrefix(string(plainBytes), wantPlain) {
		t.Fatalf("plain stdin:\n%s\nwant prefix:\n%s", plainBytes, wantPlain)
	}

	out := must(t, "gate", "run", "brief/briefed", "--item", id, "--from", "st-a", "--show-input")
	if !strings.Contains(out, "prompt:\n"+wantBriefed) {
		t.Fatalf("briefed --show-input:\n%s", out)
	}

	out = must(t, "gate", "run", "brief/mixed", "--item", id, "--from", "st-a", "--show-input")
	for _, want := range []string{"prompt (judge-briefed):\n" + wantBriefed, "prompt (judge-plain):\n" + wantPlain} {
		if !strings.Contains(out, want) {
			t.Fatalf("mixed --show-input lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\nprompt:\n") {
		t.Fatalf("mixed --show-input printed a shared prompt header:\n%s", out)
	}
	noGateRun(t, id)
}
