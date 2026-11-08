//go:build integration && unix

package tests

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

// assertTaskAttached asserts run exe carries its parent task as the
// attachment task, as satelle task get prints it: the after-review
// (satelle-task-validate-after-review) judges the run against it.
func assertTaskAttached(t *testing.T, s *sandbox, where, exe, task string) {
	t.Helper()
	r := s.run("execution", "attachment", exe, "task")
	if r.code != 0 {
		t.Fatalf("%s: run %s has no task attachment: exit %d\n%s%s", where, exe, r.code, r.out, r.err)
	}
	if want := s.must("task", "get", task); r.out != want {
		t.Fatalf("%s: run %s's task attachment is not satelle task get %s:\n%s\nwant:\n%s", where, exe, task, r.out, want)
	}
}

// onlyRun returns the one in-progress run of task.
func onlyRun(t *testing.T, s *sandbox, where, task string) string {
	t.Helper()
	var runs []item.Item
	s.mustJSON(&runs, "execution", "list", "--parent", task, "--status", "in_progress")
	if len(runs) != 1 {
		t.Fatalf("%s: %d in-progress runs of %s, want 1", where, len(runs), task)
	}
	return runs[0].ID
}

// Every task procedure's start block, run as written in a sandbox, leaves the
// run it starts carrying its parent task as the attachment task: release.md
// step 0 (the release run), release.md step 10 (the cleanup run) and
// drift-review.md step 0 (the drift run release.md step 11 started).
func TestTaskProceduresAttachTheirTask(t *testing.T) {
	t.Run("release step 0", func(t *testing.T) {
		t.Parallel()
		s, _, task := releaseSandbox(t)
		q := s.create("queue", "--title", "release")
		writeReleaseQueue(t, s, q.ID, task.ID)
		s.must("validate")
		st := s.create("story", "--title", "story")
		s.must("story", "set", st.ID, "--status", "approved")
		s.must("queue", "add", q.ID, st.ID)
		r := s.exec("bash", "", "-c", shBlock(t, ".satelle/skills/release.md", "## 0.", task.ID))
		if r.code != 0 {
			t.Fatalf("release step 0: exit %d\n%s%s", r.code, r.out, r.err)
		}
		assertTaskAttached(t, s, "release step 0", onlyRun(t, s, "release step 0", task.ID), task.ID)
	})

	t.Run("release step 10", func(t *testing.T) {
		t.Parallel()
		s, _, _ := releaseSandbox(t)
		task := s.create("task", "--title", "Remove finished worktrees", "--input-kind", "story")
		block := shBlock(t, ".satelle/skills/release.md", "## 10.", task.ID)
		if strings.Contains(block, "clear-worktrees.sh") {
			t.Fatalf("release.md step 10's start block runs the cleanup itself:\n%s", block)
		}
		cd := "cd <the main checkout>\n"
		if !strings.Contains(block, cd) {
			t.Fatalf("release.md step 10's start block has no %q line:\n%s", cd, block)
		}
		r := s.exec("bash", "", "-c", strings.Replace(block, cd, "", 1)) // the sandbox is the main checkout
		if r.code != 0 {
			t.Fatalf("release step 10: exit %d\n%s%s", r.code, r.out, r.err)
		}
		assertTaskAttached(t, s, "release step 10", onlyRun(t, s, "release step 10", task.ID), task.ID)
	})

	t.Run("drift-review step 0", func(t *testing.T) {
		t.Parallel()
		d := newDriftSandbox(t, nil)
		exe := d.start(d.drift, stepSeat(t)) // release.md step 11 starts and seats it
		r := d.exec("bash", "", "-c", shBlock(t, ".satelle/skills/drift-review.md", "## 0.", d.drift))
		if r.code != 0 {
			t.Fatalf("drift-review step 0: exit %d\n%s%s", r.code, r.out, r.err)
		}
		d.assertJudgedFromPrev(exe)
		assertTaskAttached(t, d.sandbox, "drift-review step 0", exe, d.drift)
	})
}
