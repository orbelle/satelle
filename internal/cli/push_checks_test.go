package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/pflag"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/doctor"
	"github.com/bobmcallan/satelle/internal/health"
)

// What a push checks and what it sends (sty_89b9df20): the three push commands
// refuse a shared agents.toml that carries machine-specific content and a
// configuration that does not validate, before any network call, and what they
// send is the file's own bytes.

// newCountingServer is a hosted server that must never be reached: every request
// is counted and answered with a 500.
func newCountingServer(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	return ts, func() int { return int(n.Load()) }
}

// pushRunner is one push command, run as `satelle` would: it returns the output
// and the error.
type pushRunner struct {
	name string
	toml string // the satelle.toml the command needs to get as far as the server
	run  func(t *testing.T, url string, dryRun bool) (string, error)
}

// pushableAgents is a shared agents layer that carries nothing machine-specific.
const pushableAgents = `# authored by hand — a push must send these bytes, comment and all
[executor]
role    = "agent"
command = "in-loop"

[reviewer]
role    = "reviewer"
command = "` + agentcli.DefaultClaudeCommand + `"
tools   = "Read,Grep,Glob"
model   = "opus"
env     = { ANTHROPIC_AUTH_TOKEN = "${GLM_API_KEY}", BLANK = "" }
settings = { model = "opus", env = { CLAUDE_TOKEN = "${CLAUDE_TOKEN}" } }
`

const (
	executorOnlyAgents = "[executor]\nrole = \"agent\"\ncommand = \"in-loop\"\n"
	ghostBinding       = "\n[ghost]\nrole = \"agent\"\ncommand = \"claude -p --allowedTools {tools} --append-system-prompt {system}\"\ntools = \"Read,Edit,Bash(satelle:*)\"\n"
)

const (
	piPerformerCommand = "pi -p {system} {payload}"
	// roleOnlyCoder is a shared [coder] that writes no command and no tools: its
	// grant comes from each machine's agents.local.toml or profile.
	roleOnlyCoder = executorOnlyAgents + "\n[coder]\nrole = \"agent\"\n"
)

// nodeAllocFindings returns the node.alloc findings of one severity.
func nodeAllocFindings(rep doctor.Report, sev health.Severity) []health.Finding {
	var out []health.Finding
	for _, f := range rep.Findings.WithSeverity(sev) {
		if f.ID == health.IDNodeAlloc {
			out = append(out, f)
		}
	}
	return out
}

// AC10: no help text says a push or pull redacts agent configuration.
func TestSyncHelpTextDoesNotDescribeRedaction(t *testing.T) {
	root := NewRootCmd()
	for _, path := range [][]string{{"sync", "bindings", "push"}, {"sync", "bindings", "pull"}, {"sync", "bindings"}, {"sync", "config", "push"}, {"publish", "push"}} {
		c, _, err := root.Find(path)
		if err != nil || c == nil || c.Name() != path[len(path)-1] {
			t.Fatalf("command %v not found: %v", path, err)
		}
		text := c.Short + "\n" + c.Long
		c.Flags().VisitAll(func(f *pflag.Flag) { text += "\n" + f.Usage })
		if strings.Contains(strings.ToLower(text), "redact") {
			t.Errorf("satelle %s help still describes redaction:\n%s", strings.Join(path, " "), text)
		}
	}
}
