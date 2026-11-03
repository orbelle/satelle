//go:build clislow

package cli

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSlowTransitionUnaffectedByUnreachablePush proves AC4 of sty_e6e467fe: making
// the mirror self-repairing must not have made the mutating verb pay for UI
// delivery. A status transition against a push endpoint that is refused — and
// then one that accepts and never answers — still succeeds, still records the
// new state in the repo database, and stays inside the drain budget.
//
// This is the invariant that rules OUT the rejected shape: any CLI-side retry
// would show up here as a slower or failable transition.
func TestSlowTransitionUnaffectedByUnreachablePush(t *testing.T) {
	repo := tempRepo(t)

	// An ungated lifecycle: this test is about the push path, not about gates.
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRoute(t, wfDir,
		`["*"]
obligations = ["raised", "planned", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "executor"
requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["planned"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}

	out, err := runRoot(t, "story", "create",
		"--title", "Push Drop Probe",
		"--body", "transition with nowhere to push",
		"--acceptance", "1. transitions anyway",
		"--category", "chore",
	)
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("parse create: %v out=%s", err, out)
	}

	// A black hole: accepts the connection, never answers. Worse than a refused
	// port, because only the drain budget can end it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 1)
				for {
					if _, err := conn.Read(buf); err != nil {
						return
					}
				}
			}(c)
		}
	}()

	cases := []struct {
		name     string
		endpoint string
		status   string
		budget   time.Duration
	}{
		// time-subject: the drain budget is the subject. The ceiling is the budget
		// plus headroom for a loaded runner's store open; a drain that ignored its
		// budget against a peer that never answers would not return at all.
		//
		// The service is gone (the restart case): connection refused.
		{"refused", "http://127.0.0.1:1", "plan", uiDrainBudget + 10*time.Second},
		// The service is going away mid-request: accepted, never answered.
		{"black hole", "http://" + ln.Addr().String(), "in_progress", uiDrainBudget + 10*time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvServerEndpoint, tc.endpoint)
			start := time.Now()
			out, err := runRoot(t, "story", "set", created.ID, "--status", tc.status)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("transition must not fail on an undeliverable push: %v\n%s", err, out)
			}
			if elapsed > tc.budget {
				t.Errorf("transition took %v with an undeliverable push — want ≤%v (drain budget %v plus store open)",
					elapsed, tc.budget, uiDrainBudget)
			}
			got, err := runRoot(t, "story", "get", created.ID)
			if err != nil {
				t.Fatalf("story get: %v\n%s", err, got)
			}
			if !strings.Contains(got, `"status": "`+tc.status+`"`) {
				t.Fatalf("repo database must hold the new state regardless of the mirror:\n%s", got)
			}
		})
	}
}
