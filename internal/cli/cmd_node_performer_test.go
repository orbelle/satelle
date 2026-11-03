//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
)

// The node side of a placed step (sty_7028d996): the allocated binding and the
// step's prompt reach the node on the agent job, and a node started for a
// performer publishes its channel key to satelle.dev before it dials.

func performerCfg() config.AgentsConfig {
	return config.AgentsConfig{
		Executor: config.AgentBinding{Command: "in-loop"},
		Agents: map[string]config.AgentBinding{
			"alpha":  {Command: "agent-a {system}"},
			"worker": {Command: "agent-w --system {system}"},
			"cloudy": {Command: "agent-c {system}", Interface: "cloud"},
			"inloop": {Command: "in-loop"},
			"bare":   {},
		},
	}
}

// With a name, talkBinding returns exactly that binding when the node defines it
// and it runs; a missing, cloud, in-loop or command-less one is none.
func TestTalkBindingHonoursANamedBinding(t *testing.T) {
	cfg := performerCfg()
	b, name, ok := talkBinding(cfg, "worker")
	if !ok || name != "worker" || b.Command != "agent-w --system {system}" {
		t.Fatalf("talkBinding(worker) = %+v, %q, %v", b, name, ok)
	}
	for _, want := range []string{"ghost", "cloudy", "inloop", "bare", "executor"} {
		if _, _, ok := talkBinding(cfg, want); ok {
			t.Errorf("talkBinding(%q) found a binding that cannot run", want)
		}
	}
	cfg.Executor = config.AgentBinding{Command: "agent-x {system}"}
	if _, name, ok := talkBinding(cfg, "executor"); !ok || name != "executor" {
		t.Errorf("a runnable executor named by want must answer, got %q %v", name, ok)
	}
}

// With no name it is today's rule: the executor if it runs, else the first
// runnable binding by name.
func TestTalkBindingWithoutANameIsTodaysRule(t *testing.T) {
	cfg := performerCfg()
	if _, name, ok := talkBinding(cfg, ""); !ok || name != "alpha" {
		t.Fatalf("no executor runs: got %q %v, want the first runnable binding alpha", name, ok)
	}
	cfg.Executor = config.AgentBinding{Command: "agent-x {system}"}
	if _, name, ok := talkBinding(cfg, ""); !ok || name != "executor" {
		t.Fatalf("a runnable executor: got %q %v, want executor", name, ok)
	}
}

// agentJobHandler runs the named binding with the step's system prompt, and falls
// back to its fixed prompt and today's binding choice for a plain message.
func TestAgentJobHandlerRunsTheNamedBindingWithTheStepPrompt(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("the fake agent is a shell script")
	}
	bin := t.TempDir()
	for name, label := range map[string]string{"agent-w": "W", "agent-a": "A"} {
		script := "#!/bin/sh\necho \"" + label + " SYS=$2\"\ncat\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dataDir := t.TempDir()
	agents := "[alpha]\nrole = \"agent\"\ncommand = \"agent-a --system {system}\"\n\n[worker]\nrole = \"agent\"\ncommand = \"agent-w --system {system}\"\n"
	if err := os.WriteFile(filepath.Join(dataDir, config.AgentsConfigName), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	h := agentJobHandler(dataDir, t.TempDir(), nil)
	ask := func(req remotenode.AgentRequest) (string, error) {
		body, _ := json.Marshal(req)
		out, err := h(context.Background(), body)
		if err != nil {
			return "", err
		}
		reply, derr := remotenode.DecodeAgentReply(out)
		return reply.Text, derr
	}

	got, err := ask(remotenode.AgentRequest{Binding: "worker", SystemPrompt: "STEP-PROMPT", Text: "the-payload"})
	if err != nil || !strings.Contains(got, "W SYS=STEP-PROMPT") || !strings.Contains(got, "the-payload") {
		t.Fatalf("named job = %q, %v; want the worker run with the step prompt and the payload", got, err)
	}
	got, err = ask(remotenode.AgentRequest{Text: "hello"})
	if err != nil || !strings.Contains(got, "A SYS=You are the agent on a remote node") {
		t.Fatalf("plain job = %q, %v; want today's first binding with the fixed prompt", got, err)
	}
	_, err = ask(remotenode.AgentRequest{Binding: "ghost", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), `remote defines no runnable binding "ghost"`) {
		t.Fatalf("job for a missing binding = %v, want it named", err)
	}
}

// The relay agent-talk path hands its runner the secrets join read, and makes no
// read of its own: the handler takes the join-time values, not a client, so the
// agent's environment carries exactly them (sty_2dabc0f6 AC2).
func TestAgentJobHandlerHandsTheRunnerTheJoinSecrets(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("the fake agent is a shell script")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"K=$JOIN_SECRET_K\"\ncat >/dev/null\n"
	if err := os.WriteFile(filepath.Join(bin, "agent-env"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dataDir := t.TempDir()
	agents := "[worker]\nrole = \"agent\"\ncommand = \"agent-env --system {system}\"\n"
	if err := os.WriteFile(filepath.Join(dataDir, config.AgentsConfigName), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, had := os.LookupEnv("JOIN_SECRET_K"); had {
		t.Setenv("JOIN_SECRET_K", "")
		_ = os.Unsetenv("JOIN_SECRET_K")
	}

	h := agentJobHandler(dataDir, t.TempDir(), map[string]string{"JOIN_SECRET_K": "join-read-value"})
	body, _ := json.Marshal(remotenode.AgentRequest{Binding: "worker", Text: "hi"})
	out, err := h(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := remotenode.DecodeAgentReply(out)
	if err != nil || !strings.Contains(reply.Text, "K=join-read-value") {
		t.Fatalf("agent reply = %q, %v; want the join-read secret in the runner's environment", reply.Text, err)
	}
}

// A node started in performer mode publishes its key once, before it dials, so the
// starter can pin it from satelle.dev; the key it published is the one it
// enrolls with. The job then reaches the named binding.
func TestNodeJoinInPerformerModePublishesItsChannelKey(t *testing.T) {
	r := newNodeRig(t)
	r.fakeSetup = func(f *devtest.Node) {
		f.Run = map[string]string{"mode": remotenode.ModePerformer, "story_id": "sty_1"}
	}
	run, fake, joined, spec := r.open(t)

	if n := fake.Count(http.MethodPut, "/channel-key"); n != 1 {
		t.Fatalf("channel-key publishes = %d, want 1\nnode log:\n%s", n, r.log)
	}
	if len(fake.ChannelKey()) == 0 {
		t.Fatal("the fake holds no published key")
	}
	order := map[string]int{}
	for i, c := range fake.Calls() {
		order[c.Method+" "+strings.TrimPrefix(c.Path, "/api/v1/remote-nodes/"+spec.Session)] = i
	}
	if order["GET /run"] > order["PUT /channel-key"] {
		t.Errorf("calls = %v, want the run read before the key publish", fake.Calls())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	body, err := run.Request(ctx, remotenode.AgentJob, remotenode.AgentRequest{Binding: "worker", SystemPrompt: "SP", Text: "do it"})
	if err != nil {
		t.Fatalf("agent job: %v\nnode log:\n%s", err, r.log)
	}
	if reply, derr := remotenode.DecodeAgentReply(body); derr != nil || !strings.Contains(reply.Text, "do it") {
		t.Errorf("reply = %+v, %v", reply, derr)
	}
	if _, err := run.Request(ctx, remotenode.AgentJob, remotenode.AgentRequest{Binding: "ghost", Text: "x"}); err == nil ||
		!strings.Contains(err.Error(), `no runnable binding "ghost"`) {
		t.Errorf("job for a binding the definition lacks = %v", err)
	}
	_ = run.End()
	_ = waitJoin(t, joined)
}

// Every other mode prints the key line and publishes nothing.
func TestNodeJoinInOtherModesPublishesNothing(t *testing.T) {
	r := newNodeRig(t)
	run, fake, joined, _ := r.open(t) // the fake's default run is mode "node"
	if n := fake.Count(http.MethodPut, "/channel-key"); n != 0 {
		t.Errorf("channel-key publishes = %d in node mode, want 0", n)
	}
	if len(fake.ChannelKey()) != 0 {
		t.Error("a node-mode node published a key")
	}
	_ = run.End()
	_ = waitJoin(t, joined)
}
