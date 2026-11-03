package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// sty_4470bb38: a story whose coder is a one-shot command seat, rejected at its
// step's exit, gets the coder back by `story set <id> --status <current>`.

// redispatchRepo is a repo whose in_progress step is allocated to a [coder] with
// interface=command (a logging stub that answers rd-gate#1) and declares a rework
// loop, and whose integration step is gated by a check that rejects its first
// presentation and writes each payload it is handed to dir. The story is engaged
// at in_progress.
func redispatchRepo(t *testing.T) (dir, id string) {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	dir = t.TempDir()
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "coded", "integrated", "closed"]
park = { state = "blocked" }
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "coder"
requires = ["raised"]
rework = { consult = "consultant", rounds = 2 }

[integrated]
status = "integration"
reviewers = ["rd-gate"]
requires = ["coded"]

[closed]
status = "done"
terminal = true
requires = ["integrated"]
`)
	skills := filepath.Join(repo, ".satelle", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(dir, "gate-count")
	gate := "---\nname: rd-gate\ntype: skill\ndescription: scripted exit gate for the re-dispatch tests\n---\n\n```check\n" +
		"n=$(cat " + counter + " 2>/dev/null || echo 0)\nn=$((n+1))\necho $n > " + counter + "\n" +
		"cat > " + filepath.Join(dir, "gate-") + "$n.json\n" +
		"if [ \"$n\" -le 1 ]; then echo \"REJECT-NOTE-MARKER: the guard allows a downgrade\"; exit 1; fi\nexit 0\n```\n"
	if err := os.WriteFile(filepath.Join(skills, "rd-gate.md"), []byte(gate), 0o644); err != nil {
		t.Fatal(err)
	}
	coder := filepath.Join(dir, "fake-coder")
	script := "#!/bin/sh\n{ echo \"=== RUN\"; cat; echo; } >> " + filepath.Join(dir, "coder.log") + "\n" +
		`echo '{"answers":[{"finding":"rd-gate#1","answer":"adopted","evidence":"guard refuses the downgrade"}]}'` + "\n"
	if err := os.WriteFile(coder, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := "tools = \"Read,Grep,Glob,Edit,Write,Bash(satelle:*)\"\n"
	agents := "[executor]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n" +
		"[orchestrator]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n" +
		"[coder]\nrole = \"agent\"\ninterface = \"command\"\ncommand = \"" + coder + " -p {system}\"\n" + tools + "\n" +
		"[consultant]\nrole = \"reviewer\"\ninterface = \"stream\"\ncommand = \"" + coder + " --output-format stream-json\"\n" +
		"tools = \"Read,Grep,Glob\"\nisolation = \"operator-attested\"\n"
	if err := os.WriteFile(filepath.Join(wfDir, config.AgentsConfigName), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(repo, ".satelle", config.AgentsConfigName))
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	out, err := runRoot(t, "story", "create", "--title", "re-dispatch me", "--body", "a command coder answers its exit gate",
		"--acceptance", "1. the coder is re-dispatched", "--category", "chore")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("parse create: %v\n%s", err, out)
	}
	id = created["id"].(string)
	if out, err := runRoot(t, "story", "set", id, "--status", "in_progress"); err != nil {
		t.Fatalf("engage: %v\n%s", err, out)
	}
	return dir, id
}

// coderRuns is each payload the coder stub was handed, in order.
func coderRuns(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "coder.log"))
	if err != nil {
		return nil
	}
	return strings.Split(string(b), "=== RUN")[1:]
}

// AC5: a coder seat that is not live-capable is refused by `story rework` with
// the one-shot re-dispatch named, so the operator is not left without a way out.
func TestStoryReworkRefusalNamesTheOneShotRedispatch(t *testing.T) {
	_, id := redispatchRepo(t)
	out, err := runRoot(t, "story", "rework", id)
	if err == nil {
		t.Fatalf("a command-interface coder cannot be relayed:\n%s", out)
	}
	for _, want := range []string{"interface=command", "satelle story set " + id + " --status in_progress"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q, got: %v", want, err)
		}
	}
}
