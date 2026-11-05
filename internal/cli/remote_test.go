package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// remote fire pushes the item's chain and fires the run over RemoteService;
// remote status reports the run and pulls the chain once it concluded.
func TestRemoteFireAndStatus(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)
	must(t, "project", "create", "acme", "--bind", "--server", srv.URL)
	id := createID(t, "story", "create", "--title", "fire me")

	out := must(t, "remote", "fire", "ci", id)
	if !strings.Contains(out, "fired ci for "+id+": run ") {
		t.Fatalf("fire = %q", out)
	}
	if len(srv.Events("acme", id)) == 0 {
		t.Fatal("fire did not push the item's chain")
	}
	if out := must(t, "remote", "fire", "ci", id); !strings.Contains(out, "queued") {
		t.Fatalf("a second fire = %q, want queued", out)
	}
	if out := must(t, "remote", "status", "ci", id); !strings.Contains(out, "ci on "+id+": queued") {
		t.Fatalf("status = %q", out)
	}
	srv.SetItemRun("ci", id, &satellepb.ItemRun{State: "concluded", NodeId: "nod_1", Verdict: "accept", Notes: "drove it to done"})
	out = must(t, "remote", "status", "ci", id, "--wait", "1m")
	if !strings.Contains(out, "concluded (node nod_1)") || !strings.Contains(out, "verdict: accept") || !strings.Contains(out, "drove it to done") {
		t.Fatalf("status = %q", out)
	}
	var calls []string
	for _, c := range srv.Calls() {
		calls = append(calls, c.Method)
	}
	if got := strings.Join(calls, " "); !strings.Contains(got, "RemoteService/FireRemote") || !strings.Contains(got, "RemoteService/GetItemRun") {
		t.Fatalf("calls = %s", got)
	}
}

func TestRemoteNeedsABinding(t *testing.T) {
	newMachine(t)
	id := createID(t, "story", "create", "--title", "x")
	if _, err := run(t, "", "remote", "fire", "ci", id); err == nil {
		t.Fatal("fire without a bound project succeeded")
	}
}

func gitIn(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// story collect records a branch read-only: its commit and merge base land on
// the ledger and the repository is unchanged.
func TestStoryCollectRecordsTheBranch(t *testing.T) {
	newMachine(t)
	id := createID(t, "story", "create", "--title", "cloud work")
	gitIn(t, "checkout", "-q", "-b", "cloud/x")
	if err := os.WriteFile("work.txt", []byte("w\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, "add", "work.txt")
	gitIn(t, "commit", "-q", "-m", "work")
	tip := gitIn(t, "rev-parse", "HEAD")
	gitIn(t, "checkout", "-q", "-")
	before := gitIn(t, "for-each-ref")
	out := must(t, "story", "collect", id, "--branch", "cloud/x")
	if !strings.Contains(out, "collected cloud/x at "+tip[:12]) {
		t.Fatalf("collect = %q", out)
	}
	if gitIn(t, "for-each-ref") != before {
		t.Fatal("collect changed the refs")
	}
	if out := must(t, "ledger", "list", id); !strings.Contains(out, kernel.KindBranchCollected) {
		t.Fatalf("ledger = %q", out)
	}
	if _, err := run(t, "", "story", "collect", id, "--branch", "nope"); err == nil {
		t.Fatal("collecting a missing branch succeeded")
	}
}
