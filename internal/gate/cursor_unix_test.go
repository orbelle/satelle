//go:build !windows

package gate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// A cursor-agent judge whose run ends in an error result is a dispatch error,
// never a reject or an accept (sto_0f41dda9 AC4).
func TestCursorErrorResultIsAnError(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cursor-agent")
	stream := `{"type":"result","subtype":"error","is_error":true,"result":"quota exceeded"}`
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat >/dev/null\necho '"+stream+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gates := map[string]workflow.Gate{"g": {Skill: "judge", Agent: "c"}}
	r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"g"}}}, gates)
	r.Agents["c"] = agent.Binding{Name: "c", Command: bin, Model: "m", Tools: []string{"Read", "Grep", "Glob"}}
	r.Dispatcher = agent.Runner{}
	rs, pass := r.Execute(context.Background())
	if pass || len(rs) != 1 || rs[0].Result != Error || !strings.HasPrefix(rs[0].Notes, "dispatch failed:") || !strings.Contains(rs[0].Notes, "quota exceeded") {
		t.Fatalf("pass %v, results %+v", pass, rs)
	}
}
