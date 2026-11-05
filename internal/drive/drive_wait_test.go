package drive_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/drive"
	"github.com/bobmcallan/satelle/internal/verb"
)

const waitDone = `["*"]
obligations = ["raised", "coded", "queued", "closed"]
park = { state = "blocked" }
cancel = { state = "cancelled" }
`

const waitRoute = `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[queued]
status = "awaiting_release"
wait = true
requires = ["coded"]

[closed]
status = "done"
terminal = true
requires = ["queued"]
`

// inLocalRepo runs the test in a git repository with one commit and no remote,
// the tree a story entering the wait step is queued from.
func inLocalRepo(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(dir)
}

func wantWaitingAtTheWaitStep(t *testing.T, res drive.Result) {
	t.Helper()
	if !res.Waiting || res.Parked || res.Terminal || res.Status != "awaiting_release" || res.Reason == "" {
		t.Fatalf("the drive must stop waiting (not parked, not terminal) at the wait step, got %+v", res)
	}
}

// A drive started on a story already resting at the wait step reports it as
// waiting and moves nothing.
func TestStoryReportsAStoryAlreadyAtAWaitStep(t *testing.T) {
	inLocalRepo(t)
	e := newEnv(t, waitDone, waitRoute)
	id := e.story()
	perf := &fakePerformers{kinds: map[string]verb.PerformerKind{"in_progress": verb.PerformerExecutor}, executor: headless()}
	if _, err := drive.Story(context.Background(), id, drive.Options{Performers: perf}); err != nil {
		t.Fatal(err)
	}
	res, err := drive.Story(context.Background(), id, drive.Options{Performers: perf})
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	wantWaitingAtTheWaitStep(t, res)
	if len(res.Moves) != 0 || e.status(id) != "awaiting_release" {
		t.Fatalf("a waiting story is reported, not moved: %+v (stored %q)", res, e.status(id))
	}
}
