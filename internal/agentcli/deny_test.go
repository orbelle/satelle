package agentcli

import (
	"context"
	"strings"
	"testing"
)

func TestReviewerProblem(t *testing.T) {
	for _, tc := range []struct {
		name, iface, command, grant string
		refused                     bool
	}{
		{"claude default", InterfaceCommand, DefaultClaudeCommand, "Read,Grep,Glob", false},
		{"grok default", InterfaceCommand, DefaultGrokCommand, "Read,Grep,Glob", false},
		{"write in grant", InterfaceCommand, DefaultClaudeCommand, "Read,Write", true},
		{"scoped shell in grant", InterfaceCommand, DefaultClaudeCommand, "Read,Bash(satelle:*)", true},
		{"subagent in grant", InterfaceCommand, DefaultClaudeCommand, "Read,Task", true},
		{"unrecognised harness", InterfaceCommand, "mystery -p {payload}", "Read", true},
		{"claude bypass, no allow-list", InterfaceCommand, "claude -p --dangerously-skip-permissions", "Read", true},
		{"grok always-approve, no allow-list", InterfaceCommand, "grok -p {payload} --always-approve", "Read", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := RunnerFromBinding(tc.iface, tc.command)
			if err != nil {
				t.Fatal(err)
			}
			if got := ReviewerProblem(r, tc.grant) != nil; got != tc.refused {
				t.Errorf("refused = %v, want %v (%v)", got, tc.refused, ReviewerProblem(r, tc.grant))
			}
		})
	}
	if _, err := RunnerFromBinding(InterfaceACP, "grok agent stdio"); err == nil {
		t.Error("grok acp must be refused")
	}
}

func TestPerformerDeny(t *testing.T) {
	req := Request{DeniedTools: PerformerDeniedTools}
	want := strings.Join(PerformerDeniedTools, ",")

	got := denyArgs("claude", []string{"-p", "--disallowedTools", "Write,Edit"}, req)
	if strings.Join(got, " ") != "-p --disallowedTools Write,Edit,"+want {
		t.Errorf("merged deny = %q", got)
	}
	got = denyArgs("claude", []string{"-p"}, req)
	if strings.Join(got, " ") != "-p --disallowedTools "+want {
		t.Errorf("appended deny = %q", got)
	}
	if got := denyArgs("claude", []string{"-p"}, Request{}); len(got) != 1 {
		t.Errorf("no deny requested, argv changed: %q", got)
	}

	for _, tc := range []struct {
		iface, command string
		ok             bool
	}{
		{InterfaceCommand, DefaultClaudeCommand, true},
		{InterfaceStream, DefaultClaudeStreamCommand, true},
		{InterfaceCommand, DefaultGrokCommand, false},
		{InterfaceCommand, "mystery -p {payload}", false},
	} {
		r, err := RunnerFromBinding(tc.iface, tc.command)
		if err != nil {
			t.Fatal(err)
		}
		if got := DenyProblem(r) == nil; got != tc.ok {
			t.Errorf("%s: deny expressible = %v, want %v", tc.command, got, tc.ok)
		}
	}

	// The runner itself refuses a deny it cannot express before spawning.
	r, _ := RunnerFromBinding(InterfaceCommand, "/nonexistent/grok -p {payload}")
	if _, err := r.Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), "cannot express a tool deny") {
		t.Errorf("grok run with a deny = %v, want a refusal", err)
	}
}
