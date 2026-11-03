//go:build clislow

package cli

import (
	"strings"
	"testing"
)

// AC5/AC7: a push runs the validation init runs. A workflow naming an agent
// section nothing declares refuses every push command — dry runs included — with
// the failing finding printed and nothing sent; declare the section and it goes.
func TestSlowPushRefusesAnInvalidConfigurationBeforeAnyRequest(t *testing.T) {
	useRealPushValidator(t)
	for _, r := range pushRunners() {
		for _, dry := range []bool{false, true} {
			name := r.name
			if dry {
				name += " --dry-run"
			}
			t.Run(name, func(t *testing.T) {
				ts, requests := newCountingServer(t)
				seedCred(t, ts.URL)
				routedPushRepo(t, r.toml, executorOnlyAgents)

				out, err := r.run(t, ts.URL, dry)
				if err == nil {
					t.Fatalf("an invalid configuration was pushed:\n%s", out)
				}
				if !strings.Contains(err.Error(), "push refused") || !strings.Contains(out, "node.alloc") || !strings.Contains(out, "ghost") {
					t.Errorf("the refusal must print the failing finding (node.alloc, ghost):\nerr: %v\nout: %s", err, out)
				}
				if n := requests(); n != 0 {
					t.Fatalf("a refused push contacted the server %d time(s)", n)
				}
			})
		}
	}

	// Declared in the shared file, the same route validates and a dry run passes.
	t.Run("a declared section passes", func(t *testing.T) {
		ts, _ := newCountingServer(t)
		seedCred(t, ts.URL)
		routedPushRepo(t, configPushToml, executorOnlyAgents+ghostBinding)
		cmd, buf := testCmd()
		if err := runSyncConfigPush(cmd, ts.URL, "", true); err != nil {
			t.Fatalf("a valid configuration was refused: %v\n%s", err, buf.String())
		}
	})
}
