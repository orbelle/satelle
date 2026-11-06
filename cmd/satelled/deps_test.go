package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestServeBinaryImportIsolation proves AC1: the dedicated serve binary does not
// link the repo-writing stack (sty_80233c10).
func TestServeBinaryImportIsolation(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	// Write-capable / CLI surfaces must not link. config→agentcli is accepted
	// (config.LoadGlobal for port defaults); the binary has no verb dispatch.
	forbidden := []string{
		"github.com/bobmcallan/satelle/internal/verb",
		"github.com/bobmcallan/satelle/internal/app",
		"github.com/bobmcallan/satelle/internal/store",
		"github.com/bobmcallan/satelle/internal/agentstep",
		"github.com/bobmcallan/satelle/internal/hosted",
		"github.com/bobmcallan/satelle/internal/cli",
		"github.com/bobmcallan/satelle/internal/kernel",
		"github.com/bobmcallan/satelle/internal/uipush",
	}
	deps := string(out)
	for _, f := range forbidden {
		if strings.Contains(deps, f) {
			t.Errorf("serve binary must not depend on %s", f)
		}
	}
}
