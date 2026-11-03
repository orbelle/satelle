package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// sty_0367d20c AC2: the sync children satelled runs stamp their ingests hosted,
// so their own writes never schedule another push. push and follow do so by
// annotation; `workspace add` — local when an operator runs it — only when
// satelled sets mirror.EnvIngestOrigin on the reseed child.
func TestSatelledSyncChildrenDrainAsHostedOrigin(t *testing.T) {
	t.Setenv(mirror.EnvIngestOrigin, "")
	root := NewRootCmd()
	origin := func(path ...string) mirror.IngestOrigin {
		t.Helper()
		cmd, _, err := root.Find(path)
		if err != nil || cmd.Name() != path[len(path)-1] {
			t.Fatalf("no %v command: %v", path, err)
		}
		return ingestOrigin(cmd)
	}
	for _, path := range [][]string{{"sync", "workstate", "push"}, {"sync", "workstate", "follow"}} {
		if got := origin(path...); got != mirror.OriginHosted {
			t.Errorf("%v drains as %q, want hosted", path, got)
		}
	}
	if got := origin("workspace", "add"); got != mirror.OriginLocal {
		t.Errorf("an operator's workspace add drains as %q, want local", got)
	}
	t.Setenv(mirror.EnvIngestOrigin, string(mirror.OriginHosted))
	if got := origin("workspace", "add"); got != mirror.OriginHosted {
		t.Errorf("satelled's reseed workspace add drains as %q, want hosted", got)
	}
}

// outcomeLine finds the --outcome-json line in a verb's output.
func outcomeLine(out string) (workstateOutcome, bool) {
	var o workstateOutcome
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, `{"held":`) {
			return o, json.Unmarshal([]byte(ln), &o) == nil
		}
	}
	return o, false
}

// sty_0367d20c AC1: a push that holds in-flight stories exits 0 and says how
// many it held, which is how satelled tells it from a push that converged.
func TestPushOutcomeCountsTheStoriesItHeld(t *testing.T) {
	w := newHoldWorld(t, holdSyncToml)
	out, err := runRoot(t, "sync", "workstate", "push", "--server", w.url, "--"+outcomeFlagName)
	if err != nil {
		t.Fatalf("a push that only holds must exit zero: %v\n%s", err, out)
	}
	o, ok := outcomeLine(out)
	if !ok || o.Held != 2 || o.Conflict != 0 {
		t.Fatalf("outcome = %+v (found %v), want held 2 (the dirty and the unpushed story):\n%s", o, ok, out)
	}
	if strings.Contains(runRootOut(t, "sync", "workstate", "push", "--help"), outcomeFlagName) {
		t.Error("--outcome-json is satelled's flag and must stay out of help")
	}
}

// A drifted or refused push exits non-zero — the signal satelled backs off on —
// and prints no outcome line.
func TestPushThatCannotPublishExitsNonZeroWithNoOutcome(t *testing.T) {
	t.Run("drifted", func(t *testing.T) {
		ts, _ := newFakeWorkstateServer(t)
		seedCred(t, ts.URL)
		workstateRepo(t, holdOffToml)
		drifted := createStoryID(t, "drifted")
		corruptRow(t, drifted, "body", "corrupted in the cache")
		out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL, "--"+outcomeFlagName)
		if err == nil {
			t.Fatalf("a drifted push must exit non-zero:\n%s", out)
		}
		if _, ok := outcomeLine(out); ok {
			t.Fatalf("a failed push printed an outcome line:\n%s", out)
		}
	})
	t.Run("refused", func(t *testing.T) {
		ts, f := newFakeWorkstateServer(t)
		seedCred(t, ts.URL)
		workstateRepo(t, holdOffToml)
		denied := createStoryID(t, "denied")
		f.chain.refuse[denied] = status.Error(codes.PermissionDenied, "viewers may not append")
		out, err := runRoot(t, "sync", "workstate", "push", "--server", ts.URL, "--"+outcomeFlagName)
		if err == nil {
			t.Fatalf("a refused push must exit non-zero:\n%s", out)
		}
		if _, ok := outcomeLine(out); ok {
			t.Fatalf("a failed push printed an outcome line:\n%s", out)
		}
	})
}

// sty_0367d20c AC1: a snapshot that stops on a story in flight here and moved on
// hosted exits 0 and counts it as a conflict; a clean one reports none.
func TestSnapshotOutcomeCountsTheConflictsItStoppedOn(t *testing.T) {
	t.Run("conflict", func(t *testing.T) {
		fx := newHoldFixture(t, holdNoGit)
		id := createStoryID(t, "diverges")
		fx.push(t)
		exec, _ := executorStateFor(t, id)
		time.Sleep(1100 * time.Millisecond) // time-subject: the local row must be a second newer than the hosted one
		putInState(t, id, exec, "", "", false)
		out, err := runRoot(t, "sync", "workstate", "snapshot", "--server", fx.ts.URL, "--"+outcomeFlagName)
		if err != nil {
			t.Fatalf("a conflict stop must exit zero: %v\n%s", err, out)
		}
		if o, ok := outcomeLine(out); !ok || o.Conflict != 1 {
			t.Fatalf("outcome = %+v (found %v), want conflict 1:\n%s", o, ok, out)
		}
	})
	t.Run("converged", func(t *testing.T) {
		fx := newHoldFixture(t, holdNoGit)
		createStoryID(t, "in step")
		fx.push(t)
		out, err := runRoot(t, "sync", "workstate", "snapshot", "--server", fx.ts.URL, "--"+outcomeFlagName)
		if err != nil {
			t.Fatalf("snapshot: %v\n%s", err, out)
		}
		if o, ok := outcomeLine(out); !ok || o != (workstateOutcome{}) {
			t.Fatalf("a converged snapshot's outcome = %+v (found %v), want zero:\n%s", o, ok, out)
		}
	})
}

func runRootOut(t *testing.T, args ...string) string {
	t.Helper()
	out, _ := runRoot(t, args...)
	return out
}
