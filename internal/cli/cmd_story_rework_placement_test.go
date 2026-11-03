package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
)

// Rework on a remote-placed child (sty_dde8b6a4): the step declares a rework
// loop AND a remote placement, so whether the relay may open depends on where
// the child runs. A remote child is refused — a cloud session cannot be a relay
// partner — and told to re-present the edge; a child placed local, including by
// the not-signed-in fallback, still opens the relay.

// placedReworkRepo is a repo with a parallel epic whose coded step is allocated
// to a live [coder] with a rework loop and a remote_agent, and one child of that
// epic engaged at in_progress by the local coder (the session is not signed in).
func placedReworkRepo(t *testing.T, childTags ...string) (id string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo := tempRepo(t)
	t.Chdir(repo)
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "coded", "closed"]

[epic-parent]
obligations = ["raised", "ready", "parent-closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "coder"
requires = ["raised"]
rework = { consult = "consultant", rounds = 3 }
remote_agent = "coder-cloud"
local_tags = ["lane:trunk"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[ready]
status = "ready"
waits_on_children = true
schedule = "parallel"
requires = ["raised"]

[parent-closed]
status = "done"
agent = "reviewer"
terminal = true
requires = ["ready"]
`)
	coderPeer := filepath.Join(t.TempDir(), "fake-coder")
	consultPeer := filepath.Join(t.TempDir(), "fake-consultant")
	reworkPeer(t, coderPeer, "E2E_CODER_REPLIES")
	reworkPeer(t, consultPeer, "E2E_CONSULT_REPLIES")
	agents := "[executor]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n" +
		"[orchestrator]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n" +
		"[coder]\nrole = \"agent\"\ninterface = \"stream\"\n" +
		"command = \"" + coderPeer + " --output-format stream-json\"\n" +
		"tools = \"Read,Grep,Glob,Edit,Write,Bash(satelle:*)\"\n\n" +
		"[coder-cloud]\nrole = \"agent\"\ninterface = \"cloud\"\ncommand = \"claude -p {system}\"\n\n" +
		"[consultant]\nrole = \"reviewer\"\ninterface = \"stream\"\n" +
		"command = \"" + consultPeer + " --output-format stream-json\"\n" +
		"tools = \"Read,Grep,Glob\"\nisolation = \"operator-attested\"\n"
	if err := os.WriteFile(filepath.Join(wfDir, config.AgentsConfigName), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(repo, ".satelle", config.AgentsConfigName))
	t.Setenv("E2E_CONSULT_REPLIES", "READY")
	t.Setenv("E2E_CODER_REPLIES", "done")

	epicOut, err := runRoot(t, "story", "create", "--title", "the epic", "--body", "container",
		"--acceptance", "1. children close", "--category", "epic-parent", "--tags", "epic:w")
	if err != nil {
		t.Fatalf("create epic: %v\n%s", err, epicOut)
	}
	// Children engage only once the epic's readiness round holds (sty_a476b913).
	acceptEpicReadiness(t, jsonField(t, epicOut, "id"), "backlog", "ready")
	tags := strings.Join(append([]string{"epic:w"}, childTags...), ",")
	out, err := runRoot(t, "story", "create", "--title", "a parallel child", "--body", "child",
		"--acceptance", "1. the relay is placement-aware", "--category", "chore", "--tags", tags)
	if err != nil {
		t.Fatalf("create child: %v\n%s", err, out)
	}
	id = jsonField(t, out, "id")
	if out, err := runRoot(t, "story", "set", id, "--status", "in_progress"); err != nil {
		t.Fatalf("engage child: %v\n%s", err, out)
	}
	return id
}

// acceptEpicReadiness records an accepted readiness round for the from→to edge of
// the epic id, as story ready would.
func acceptEpicReadiness(t *testing.T, id, from, to string) {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	payload, _ := json.Marshal(map[string]string{"from": from, "to": to, "attempt": "att_test"})
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{
		StoryID: id, Kind: ledger.KindReadinessAccept, Actor: "executor", Payload: payload,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func jsonField(t *testing.T, raw, key string) string {
	t.Helper()
	i := strings.Index(raw, `"`+key+`"`)
	if i < 0 {
		t.Fatalf("no %q in %s", key, raw)
	}
	rest := raw[i+len(key)+2:]
	rest = rest[strings.Index(rest, `"`)+1:]
	return rest[:strings.Index(rest, `"`)]
}

func signIn(t *testing.T) {
	t.Helper()
	saveCred(t, config.DefaultHostedServer, hosted.Credential{PrincipalID: "P1"})
}
