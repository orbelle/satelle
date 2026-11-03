//go:build clislow

package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
)

// TestSlowStopcheckSeatScopeMatrix (sty_211d8419 AC1-AC5): four seat states × dirty
// or clean. The block fires only when no live seat exists anywhere in the repo;
// a sibling's live seat turns a dirty tree into an allow that names the holder;
// this session's own seat and a clean tree stay silent allows.
func TestSlowStopcheckSeatScopeMatrix(t *testing.T) {
	cases := []struct {
		seat               stopcheckSeatState
		dirty              bool
		wantBlock          bool
		wantMentionsHolder bool
	}{
		{seatNone, true, true, false},
		{seatNone, false, false, false},
		{seatMine, true, false, false},
		{seatMine, false, false, false},
		{seatOther, true, false, true},
		{seatOther, false, false, false},
		{seatExpired, true, true, false},
		{seatExpired, false, false, false},
	}
	for _, tc := range cases {
		name := string(tc.seat) + "/clean"
		if tc.dirty {
			name = string(tc.seat) + "/dirty"
		}
		t.Run(name, func(t *testing.T) {
			repo, storyID := stopcheckRepo(t, tc.seat)
			if tc.dirty {
				dirtyTree(t, repo)
			}
			var buf bytes.Buffer
			if err := runHookStopcheck([]byte("{}"), &buf); err != nil {
				t.Fatalf("stopcheck: %v", err)
			}
			out := buf.String()
			gotBlock := strings.Contains(out, `"decision":"block"`)
			if gotBlock != tc.wantBlock {
				t.Fatalf("block = %v, want %v; stdout:\n%s", gotBlock, tc.wantBlock, out)
			}
			if tc.wantBlock {
				var blk stopBlockOut
				if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &blk); err != nil || blk.Decision != "block" {
					t.Fatalf("block payload = %q (%v)", out, err)
				}
				if !strings.Contains(blk.Reason, "STOP BLOCKED") || !strings.Contains(blk.Reason, "internal/foo.go") {
					t.Fatalf("block reason must be the existing message naming the path: %q", blk.Reason)
				}
			}
			if tc.wantMentionsHolder {
				var note agentcli.StopAllow
				if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &note); err != nil || note.SystemMessage == "" {
					t.Fatalf("allow-with-note payload = %q (%v)", out, err)
				}
				for _, want := range []string{storyID, siblingSession, "not blocked", "satelle story seat"} {
					if !strings.Contains(note.SystemMessage, want) {
						t.Errorf("note missing %q: %q", want, note.SystemMessage)
					}
				}
			} else if !tc.wantBlock && strings.TrimSpace(out) != "" {
				t.Fatalf("silent allow expected, got %q", out)
			}
		})
	}
}

// A dispatched process never judges the tree (sty_c7577f0e, superseding
// sty_aeb8a138 AC4): its final output is a verdict, and a block would run another
// turn that replaces it. A dirty tree, with no seat or with a sibling's seat,
// stays silent for every dispatch marker; the driver's running gate does not
// hold it.
func TestSlowStopcheckDispatchedNeverJudgesTree(t *testing.T) {
	markers := []string{config.DispatchAgentEnv, config.DispatchStepEnv, config.DispatchItemEnv, config.SpawnEnv}
	for _, marker := range markers {
		t.Run("no seat/"+marker, func(t *testing.T) {
			repo, _ := stopcheckRepo(t, seatNone)
			dirtyTree(t, repo)
			var buf bytes.Buffer
			if err := runHookStopcheck([]byte("{}"), &buf); err != nil {
				t.Fatalf("stopcheck: %v", err)
			}
			if !strings.Contains(buf.String(), "STOP BLOCKED") {
				t.Fatalf("the driving session (no marker) must still block: %q", buf.String())
			}
			t.Setenv(marker, "reviewer")
			buf.Reset()
			if err := runHookStopcheck([]byte("{}"), &buf); err != nil {
				t.Fatalf("stopcheck: %v", err)
			}
			if buf.Len() != 0 {
				t.Fatalf("a dispatched stop (%s) must be silent on a dirty tree: %q", marker, buf.String())
			}
		})
	}
	t.Run("sibling seat", func(t *testing.T) {
		repo, _ := stopcheckRepo(t, seatOther)
		dirtyTree(t, repo)
		var buf bytes.Buffer
		if err := runHookStopcheck([]byte("{}"), &buf); err != nil || !strings.Contains(buf.String(), siblingSession) {
			t.Fatalf("the driving session must still get the sibling note: out=%q err=%v", buf.String(), err)
		}
		t.Setenv(config.SpawnEnv, "1")
		buf.Reset()
		if err := runHookStopcheck([]byte("{}"), &buf); err != nil || buf.Len() != 0 {
			t.Fatalf("a dispatched stop must emit no sibling note: out=%q err=%v", buf.String(), err)
		}
	})
	t.Run("driver's gate still running", func(t *testing.T) {
		repo, _ := stopcheckRepo(t, seatNone)
		dirtyTree(t, repo)
		t.Setenv(stopGateWaitEnv, "10s")
		t.Setenv(config.DispatchAgentEnv, "reviewer")
		runningGateForSession(t, gateStoreForTest(t), "sty_x", thisSession)

		start := time.Now()
		var buf bytes.Buffer
		if err := runHookStopcheck([]byte("{}"), &buf); err != nil {
			t.Fatalf("stopcheck: %v", err)
		}
		// The wait bound is 10s: a stop that waited would run all of it, so half of
		// it separates "did not wait" from "waited" on any machine.
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("a dispatched stop waited on the driver's gate (%s)", elapsed)
		}
		if buf.Len() != 0 {
			t.Fatalf("a dispatched stop must say nothing, neither gate nor tree: %q", buf.String())
		}
	})
}
