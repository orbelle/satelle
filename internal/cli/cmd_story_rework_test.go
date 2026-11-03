package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/wfroute"
)

// --- resolution: what the route says about the loop --------------------------

func reworkDerived(t *testing.T, reworks []wfroute.Rework, agent string) wfgovern.DerivedRoute {
	t.Helper()
	step := `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
` + agent + `
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`
	spec, err := wfdot.ParseRoute(`["*"]
obligations = ["raised", "coded", "closed"]
`, step, "feature", nil)
	if err != nil {
		t.Fatalf("ParseRoute: %v", err)
	}
	return wfgovern.DerivedRoute{Spec: spec, Reworks: reworks}
}

func TestResolveReworkPlanReadsTheRoute(t *testing.T) {
	d := reworkDerived(t, []wfroute.Rework{{Step: "in_progress", Consult: "consultant", Rounds: 3}}, `agent = "coder"`)
	got, err := resolveReworkPlan(d, "in_progress")
	if err != nil {
		t.Fatalf("resolveReworkPlan: %v", err)
	}
	want := reworkPlan{CoderBinding: "coder", ConsultBinding: "consultant", Rounds: 3}
	if got != want {
		t.Errorf("plan = %+v, want %+v", got, want)
	}
}

func TestResolveReworkPlanRefusesRatherThanGuesses(t *testing.T) {
	// A step with no rework key has NO loop — inventing one would be the binary
	// deciding process.
	d := reworkDerived(t, nil, `agent = "coder"`)
	_, err := resolveReworkPlan(d, "in_progress")
	if err == nil {
		t.Fatalf("want a refusal for a step with no rework key")
	}
	for _, want := range []string{"declares no rework loop", "step.toml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must contain %q, got: %v", want, err)
		}
	}

	// The loop is declared on a step that allocates nobody to code with.
	d = reworkDerived(t, []wfroute.Rework{{Step: "done", Consult: "consultant", Rounds: 2}}, `agent = "coder"`)
	if _, err := resolveReworkPlan(d, "done"); err == nil || !strings.Contains(err.Error(), "allocates no performer") {
		t.Errorf("want an 'allocates no performer' refusal, got: %v", err)
	}

	// Another step's loop is not this step's loop.
	d = reworkDerived(t, []wfroute.Rework{{Step: "in_progress", Consult: "consultant", Rounds: 2}}, `agent = "coder"`)
	if _, err := resolveReworkPlan(d, "backlog"); err == nil {
		t.Errorf("want a refusal at a status with no loop of its own")
	}
}

func TestReworkCommandLongStatesTheMarkerContract(t *testing.T) {
	long := storyReworkCommand().Long
	for _, want := range []string{"READY", "NOT READY: <reason>", "rework = { consult", "Does NOT change status"} {
		if !strings.Contains(long, want) {
			t.Errorf("satelle story rework --help must state %q — a contract stated in only one place drifts", want)
		}
	}
}

// --- end to end: two live peers, real store, real verbs ---------------------

// reworkPeer writes a fake stream-json agent that answers each user turn with
// the next line of a scripted reply list, joined by "|" between turns.
func reworkPeer(t *testing.T, path, replyEnv string) {
	t.Helper()
	script := `#!/usr/bin/env python3
import json, os, sys
replies = os.environ[` + "\"" + replyEnv + "\"" + `].split("|")
n = 0
def send(o):
    sys.stdout.write(json.dumps(o) + "\n"); sys.stdout.flush()
while True:
    line = sys.stdin.readline()
    if not line:
        break
    msg = json.loads(line)
    if msg.get("type") != "user":
        continue
    text = replies[n] if n < len(replies) else "(exhausted)"
    n += 1
    send({"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":text}]}})
    send({"type":"result","result":text})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// reworkEnvEchoPeer is a sibling of reworkPeer whose reply text includes the
// process env the verb exported into that spawn (sty_7567f047 AC1/AC3). The
// scripted marker line stays last so parseReadyMarker still reads it.
func reworkEnvEchoPeer(t *testing.T, path, replyEnv string) {
	t.Helper()
	script := `#!/usr/bin/env python3
import json, os, sys
replies = os.environ[` + "\"" + replyEnv + "\"" + `].split("|")
n = 0
def send(o):
    sys.stdout.write(json.dumps(o) + "\n"); sys.stdout.flush()
while True:
    line = sys.stdin.readline()
    if not line:
        break
    msg = json.loads(line)
    if msg.get("type") != "user":
        continue
    body = replies[n] if n < len(replies) else "(exhausted)"
    n += 1
    text = ("relay=" + os.environ.get("SATELLE_RELAY_BINDING", "") +
            " session=" + os.environ.get("SATELLE_SESSION", "") +
            "\n" + body)
    send({"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":text}]}})
    send({"type":"result","result":text})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// reworkRepo sets up a repo whose in_progress step is allocated to a live
// [coder] binding and declares a rework loop with a live [consultant].
func reworkRepo(t *testing.T, rounds, consultReplies, coderReplies string) (repo, id string) {
	return reworkRepoWith(t, rounds, consultReplies, coderReplies, reworkPeer, "")
}

// reworkRepoWith is reworkRepo with a pluggable peer writer and an optional
// lease session stamped at engage (tempRepo clears SATELLE_SESSION, so a
// caller cannot set the lease id before the helper runs).
func reworkRepoWith(t *testing.T, rounds, consultReplies, coderReplies string, writePeer func(*testing.T, string, string), leaseSession string) (repo, id string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	repo = tempRepo(t)
	if leaseSession != "" {
		t.Setenv(config.SessionEnv, leaseSession)
	}
	t.Chdir(repo)
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "coder"
requires = ["raised"]
rework = { consult = "consultant", rounds = `+rounds+` }

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)

	coderPeer := filepath.Join(t.TempDir(), "fake-coder")
	consultPeer := filepath.Join(t.TempDir(), "fake-consultant")
	writePeer(t, coderPeer, "E2E_CODER_REPLIES")
	writePeer(t, consultPeer, "E2E_CONSULT_REPLIES")

	agents := "[executor]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n" +
		"[orchestrator]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n" +
		"[coder]\nrole = \"agent\"\ninterface = \"stream\"\n" +
		"command = \"" + coderPeer + " --output-format stream-json\"\n" +
		"tools = \"Read,Grep,Glob,Edit,Write,Bash(satelle:*)\"\n\n" +
		"[consultant]\nrole = \"reviewer\"\ninterface = \"stream\"\n" +
		"command = \"" + consultPeer + " --output-format stream-json\"\n" +
		"tools = \"Read,Grep,Glob\"\nisolation = \"operator-attested\"\n"
	if err := os.WriteFile(filepath.Join(wfDir, config.AgentsConfigName), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(repo, ".satelle", config.AgentsConfigName))

	// Set before the engaging transition: agent="coder" on the coded step means
	// `story set --status in_progress` DISPATCHES the coder once, one-shot, and
	// the peer needs its script. Each session is its own process, so the relay
	// below starts the script from the top again.
	t.Setenv("E2E_CONSULT_REPLIES", consultReplies)
	t.Setenv("E2E_CODER_REPLIES", coderReplies)

	out, err := runRoot(t, "story", "create",
		"--title", "rework relay e2e",
		"--body", "a coder and a consulting reviewer converge before the gate",
		"--acceptance", "1. the relay converges and never moves status",
		"--category", "chore",
	)
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created map[string]any
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&created); err != nil {
		t.Fatalf("parse create: %v\n%s", err, out)
	}
	id, _ = created["id"].(string)
	if id == "" {
		t.Fatalf("no id in %s", out)
	}
	if out, err := runRoot(t, "story", "set", id, "--status", "in_progress"); err != nil {
		t.Fatalf("engage: %v\n%s", err, out)
	}
	return repo, id
}

type reworkRow struct {
	From string `json:"from"`
	To   string `json:"to"`
	Cc   string `json:"cc"`
	Body string `json:"body"`
}

func reworkMessages(t *testing.T, id string) []reworkRow {
	t.Helper()
	raw, err := runRoot(t, "story", "messages", id)
	if err != nil {
		t.Fatalf("messages: %v\n%s", err, raw)
	}
	var rows []reworkRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("parse messages: %v\n%s", err, raw)
	}
	return rows
}

func storyStatus(t *testing.T, id string) string {
	t.Helper()
	raw, err := runRoot(t, "story", "get", id)
	if err != nil {
		t.Fatalf("get: %v\n%s", err, raw)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("parse get: %v\n%s", err, raw)
	}
	s, _ := got["status"].(string)
	return s
}
