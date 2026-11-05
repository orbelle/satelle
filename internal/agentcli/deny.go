package agentcli

import (
	"fmt"
	"strings"
)

// Performer limits are tool grants, never prompt text (REBUILD kernel 5). A
// performer may do the step's work, but it may not move the item it performs:
// every performer dispatch carries PerformerDeniedTools in Request.DeniedTools,
// and the adapter translates the deny into the harness's own deny flag. An
// adapter that cannot express a deny refuses the dispatch before any process
// starts.

// PerformerDeniedTools are the satelle status/transition verbs a performer must
// not run.
var PerformerDeniedTools = []string{
	"Bash(satelle story set:*)",
	"Bash(satelle task set:*)",
	"Bash(satelle execution set:*)",
}

// DenyProblem reports why r cannot carry a non-empty Request.DeniedTools: nil
// when its adapter expresses a deny (claude, on the command and stream
// transports). A Runner satelle did not build (a test double) launches no
// harness and has no problem.
func DenyProblem(r Runner) error {
	switch v := r.(type) {
	case templateRunner:
		return denyProblem(v.binary, v.argTemplate, InterfaceCommand)
	case streamRunner:
		return denyProblem(v.binary, v.args, InterfaceStream)
	case acpRunner:
		return denyProblem(v.binary, v.args, InterfaceACP)
	case piRPCRunner:
		return denyProblem(v.binary, v.args, InterfacePiRPC)
	}
	return nil
}

func denyProblem(binary string, args []string, iface string) error {
	adapter := adapterOf(binary, args)
	if adapter == HarnessClaude && (iface == InterfaceCommand || iface == InterfaceStream) {
		return nil
	}
	return fmt.Errorf("agentcli: %s/%s cannot express a tool deny (needed to keep a performer from %s); bind the performer to claude on the command or stream transport",
		adapter, iface, strings.Join(PerformerDeniedTools, ", "))
}

// checkDeny refuses a request whose DeniedTools the spawn cannot express.
func checkDeny(binary string, args []string, iface string, req Request) error {
	if len(req.DeniedTools) == 0 {
		return nil
	}
	return denyProblem(binary, args, iface)
}

// denyArgs adds req.DeniedTools to a claude argv: merged into an existing
// --disallowedTools value, else appended as its own flag. Any other adapter is
// returned unchanged (checkDeny refused it already).
func denyArgs(binary string, args []string, req Request) []string {
	if len(req.DeniedTools) == 0 || adapterOf(binary, args) != HarnessClaude {
		return args
	}
	deny := strings.Join(req.DeniedTools, ",")
	out := append([]string(nil), args...)
	for i, a := range out {
		if a == "--disallowedTools" && i+1 < len(out) && !strings.HasPrefix(out[i+1], "--") {
			out[i+1] = out[i+1] + "," + deny
			return out
		}
		if v, ok := strings.CutPrefix(a, "--disallowedTools="); ok {
			out[i] = "--disallowedTools=" + v + "," + deny
			return out
		}
	}
	return append(out, "--disallowedTools", deny)
}

// writeCapable are the tool classes a reviewer grant may not admit: anything
// that can change the work tree or reach beyond reading it.
var writeCapable = map[ToolClass]bool{
	ClassWrite: true, ClassEdit: true, ClassShell: true, ClassSubprocess: true,
	ClassMCP: true, ClassUnknown: true,
}

// WriteCapableGrant returns the grant entries (scoped ones included) whose class
// is write-capable, per ClassifyTool.
func WriteCapableGrant(grant string) []string {
	var out []string
	for _, t := range parseGrant(grant) {
		if writeCapable[ClassifyTool(t.Base)] {
			out = append(out, t.Name)
		}
	}
	return out
}

// ReviewerProblem is the hard refusal for a reviewer dispatch: non-nil when r
// cannot keep the reviewer inside the read-only grant — any preflight gap, a
// harness no adapter recognises, or a grant naming a write-capable tool. A
// Runner satelle did not build (a test double) launches no harness, so only its
// grant is judged.
func ReviewerProblem(r Runner, grant string) error {
	if bad := WriteCapableGrant(grant); len(bad) > 0 {
		return fmt.Errorf("agentcli: reviewer grant %q admits write-capable tools %s; a reviewer is read-only", grant, strings.Join(bad, ", "))
	}
	if gaps := PreflightRunner(r, grant); len(gaps) > 0 {
		return fmt.Errorf("agentcli: reviewer cannot be held to its read-only grant (%s): %s — fix: %s", gaps[0].Adapter, GapSummary(gaps), GapFix(gaps))
	}
	return nil
}
