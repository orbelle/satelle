package agentstep

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// A repo binds the performer and the reviewers of its story readiness step
// (propose) and of its container readiness step (waits_on_children) to an
// OpenRouter model through agents.toml and step.toml alone: this drives both
// steps through the engine against a fake `pi` on PATH, with the unmodified
// dispatch and transport code, and reads back what pi was started with.

const (
	openRouterModel = "openrouter/minimax/minimax-m3"
	openRouterTools = "read,grep,find,ls"
)

var openRouterWF = wfDoc(`["*"]
obligations = ["raised", "planned", "closed"]

[epic-parent]
obligations = ["raised", "ready", "closed-epic"]
`, `[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "planner-openrouter"
propose = true
skills = ["plan"]
reviewers = ["story-review"]
reviewer_agent = "reviewer-openrouter"
requires = ["raised"]

[ready]
status = "ready"
agent = "planner-openrouter"
propose = true
waits_on_children = true
skills = ["plan"]
reviewers = ["container-review"]
reviewer_agent = "reviewer-openrouter"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["planned"]

[closed-epic]
status = "done"
terminal = true
requires = ["ready"]
`)

func openRouterBindings(name string) (config.AgentBinding, bool) {
	b := config.AgentBinding{
		Command: "pi -p --provider openrouter --tools {tools} --model {model} {payload}",
		Tools:   openRouterTools, Model: openRouterModel, Auth: config.AuthAPIKey,
	}
	switch name {
	case "planner-openrouter":
		b.Role = config.RoleAgent
	case "reviewer-openrouter":
		b.Role = config.RoleReviewer
	default:
		return config.AgentBinding{}, false
	}
	return b, true
}

// fakePi writes an executable named pi that records the provider, tools and model
// flags of every start, and answers an accepting verdict.
func fakePi(t *testing.T) (marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "spawns")
	script := `#!/bin/sh
cat > /dev/null
out=
prev=
for a in "$@"; do
  case "$prev" in --provider|--tools|--model) out="$out $prev=$a";; esac
  prev=$a
done
echo "spawn$out" >> ` + marker + `
printf '%s' '{"decision":"accept","notes":"ok"}'
`
	if err := os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	return marker
}
