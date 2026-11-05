package remotenode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
)

// embeddedPRApprove is the pr-approve bundle template the binary ships.
func embeddedPRApprove(t *testing.T) map[string]string {
	t.Helper()
	files, ok := config.EmbeddedRemote("pr-approve")
	if !ok {
		t.Fatal("the binary ships no pr-approve remote bundle")
	}
	return files
}

// prApproveEnv is a task env whose node runs the embedded tsk_pr-approve: the
// task is ingested from the binary's defaults, the repo's constitution principle
// is installed, and the fake GitHub serves orbelle/satelle to the run's token.
func prApproveEnv(t *testing.T) (*taskEnv, *devtest.GitHub) {
	t.Helper()
	e := newTaskEnv(t, false)
	pdir := filepath.Join(t.TempDir(), "principles")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: repo-constitution\ntype: principle\ndescription: the repo's constitution\n---\n\n# Constitution\n"
	if err := os.WriteFile(filepath.Join(pdir, "repo-constitution.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.DocIndex.Sync(context.Background(), map[string]string{"principles": pdir}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Stories.Get(context.Background(), "tsk_pr-approve"); err != nil {
		t.Fatalf("the embedded tsk_pr-approve was not ingested: %v", err)
	}
	gh := devtest.NewGitHub(t)
	gh.Mint("ghs_run", "orbelle/satelle")
	return e, gh
}

func prApproveRun(e *taskEnv, perf executorOnly, log *statusLog) TaskRun {
	tr := taskRun(e, perf, log)
	tr.Def = remotedef.Definition{Drive: remotedef.Drive{Kind: remotedef.DriveTask, Task: "pr-approve"}}
	return tr
}
