package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/wire/chainpb"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

const wakeWorkflow = `name = "default"
[states.backlog]
start = true
[states.approved]
[states.done]
terminal = true
[states.cancelled]
terminal = true
[[transition]]
from = ["backlog"]
to = "approved"
[[transition]]
from = ["approved"]
to = "backlog"
[[transition]]
from = ["approved"]
to = "done"
[[transition]]
from = ["*"]
to = "cancelled"
`

// wakeFixture writes a release queue file whose filter takes approved
// stories, triggered on a task, and returns the task id.
func wakeFixture(t *testing.T, dir string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".satelle", "workflows", "default.toml"), []byte(wakeWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	task := createID(t, "task", "create", "--title", "release")
	if err := os.MkdirAll(filepath.Join(dir, ".satelle", "queues"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "title = \"Release\"\n[filter]\nkind = \"story\"\nstatus = \"approved\"\n[trigger]\ntask = \"" + task + "\"\nbase = \"HEAD\"\nmin_size = 0\n"
	if err := os.WriteFile(filepath.Join(dir, ".satelle", "queues", "release.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return task
}

func approvedStory(t *testing.T, title string) string {
	t.Helper()
	id := createID(t, "story", "create", "--title", title, "--criteria", "c")
	must(t, "story", "set", id, "--status", "approved")
	return id
}

func enableWake(t *testing.T, project string) {
	t.Helper()
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	gc.Wake = append(gc.Wake, config.WakeConfig{Project: project, Queue: "release"})
	if err := config.SaveGlobal(gc); err != nil {
		t.Fatal(err)
	}
}

func wakeFires(t *testing.T) []ledger.Event {
	t.Helper()
	_, evs := readChain(t, "que_release")
	var fired []ledger.Event
	for _, e := range evs {
		if e.Kind == item.KindQueueFired {
			fired = append(fired, e)
		}
	}
	return fired
}

func executions(t *testing.T) []item.Item {
	t.Helper()
	var list []item.Item
	if err := json.Unmarshal([]byte(must(t, "execution", "list", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func wokenRun(t *testing.T, e ledger.Event, task string, want []string) string {
	t.Helper()
	var f item.QueueFire
	if err := json.Unmarshal(e.Payload, &f); err != nil {
		t.Fatal(err)
	}
	if f.Cause != "wake" || e.Actor != syncServiceName {
		t.Fatalf("fire cause %q by %s, want wake by %s", f.Cause, e.Actor, syncServiceName)
	}
	var run item.Item
	if err := json.Unmarshal([]byte(must(t, "execution", "get", f.Run, "--json")), &run); err != nil {
		t.Fatal(err)
	}
	if run.Parent != task || run.Status != "backlog" {
		t.Fatalf("run %s: parent %s status %s, want %s at backlog", run.ID, run.Parent, run.Status, task)
	}
	rec := must(t, "ledger", "list", f.Run, "--kind", "task.run", "--json")
	for _, id := range want {
		if !strings.Contains(rec, id) {
			t.Fatalf("run %s's task.run lacks %s:\n%s", f.Run, id, rec)
		}
	}
	return f.Run
}

func TestWakeFiresQueueWithApprovedStoryOnce(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	task := wakeFixture(t, dir)
	s1, s2 := approvedStory(t, "one"), approvedStory(t, "two")
	must(t, "project", "add")
	enableWake(t, dir)
	out := must(t, "ledger", "autosync")
	if !strings.Contains(out, "wake release: fired") {
		t.Fatalf("the pass did not report the wake:\n%s", out)
	}
	fired := wakeFires(t)
	if len(fired) != 1 {
		t.Fatalf("%d queue.fired, want 1:\n%s", len(fired), out)
	}
	wokenRun(t, fired[0], task, []string{s1, s2})
	if n := len(executions(t)); n != 1 {
		t.Fatalf("%d executions, want 1", n)
	}
}

func TestWakeHoldsWhileRunOpen(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	wakeFixture(t, dir)
	approvedStory(t, "one")
	must(t, "project", "add")
	enableWake(t, dir)
	must(t, "ledger", "autosync")
	approvedStory(t, "two")
	out := must(t, "ledger", "autosync")
	if !strings.Contains(out, "wake release: held: the queue's last run is still open") {
		t.Fatalf("the pass did not hold:\n%s", out)
	}
	if n, m := len(wakeFires(t)), len(executions(t)); n != 1 || m != 1 {
		t.Fatalf("%d fires and %d executions, want 1 and 1", n, m)
	}
}

func TestWakeHoldsBehindSessionFire(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	wakeFixture(t, dir)
	approvedStory(t, "one")
	must(t, "queue", "fire", "release")
	must(t, "project", "add")
	enableWake(t, dir)
	out := must(t, "ledger", "autosync")
	if !strings.Contains(out, "wake release: held:") {
		t.Fatalf("the pass did not hold behind the session's run:\n%s", out)
	}
	if n, m := len(wakeFires(t)), len(executions(t)); n != 1 || m != 1 {
		t.Fatalf("%d fires and %d executions, want 1 and 1", n, m)
	}
}

func TestWakeFiresNothingOnEmptyQueue(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	wakeFixture(t, dir)
	createID(t, "story", "create", "--title", "waiting", "--criteria", "c")
	must(t, "project", "add")
	enableWake(t, dir)
	out := must(t, "ledger", "autosync")
	if strings.Contains(out, "wake") || len(wakeFires(t)) != 0 || len(executions(t)) != 0 {
		t.Fatalf("an empty queue woke:\n%s", out)
	}
	lapsed := approvedStory(t, "lapsed")
	must(t, "story", "set", lapsed, "--status", "backlog")
	out = must(t, "ledger", "autosync")
	if strings.Contains(out, "wake") || len(wakeFires(t)) != 0 || len(executions(t)) != 0 {
		t.Fatalf("a lapsed member woke the queue:\n%s", out)
	}
}

func TestWakeOffByDefault(t *testing.T) {
	for _, other := range []bool{false, true} {
		t.Run(map[bool]string{false: "unset", true: "another project"}[other], func(t *testing.T) {
			dir := isolate(t)
			must(t, "init")
			wakeFixture(t, dir)
			approvedStory(t, "one")
			must(t, "project", "add")
			if other {
				enableWake(t, t.TempDir())
			}
			out := must(t, "ledger", "autosync")
			if strings.Contains(out, "wake") || len(wakeFires(t)) != 0 || len(executions(t)) != 0 {
				t.Fatalf("a queue with no wake fired:\n%s", out)
			}
		})
	}
}

func TestWakeRecoversAfterCancelledRun(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	task := wakeFixture(t, dir)
	s1 := approvedStory(t, "one")
	must(t, "project", "add")
	enableWake(t, dir)
	must(t, "ledger", "autosync")
	first := wokenRun(t, wakeFires(t)[0], task, []string{s1})
	must(t, "execution", "set", first, "--status", "cancelled")
	must(t, "ledger", "autosync")
	fired := wakeFires(t)
	if len(fired) != 2 {
		t.Fatalf("%d fires after the cancel, want 2", len(fired))
	}
	if second := wokenRun(t, fired[1], task, []string{s1}); second == first {
		t.Fatalf("the second wake reused %s", first)
	}
}

func TestWakeSkipsUnreachedStore(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	wakeFixture(t, m.dir)
	approvedStory(t, "one")
	must(t, "project", "add")
	enableWake(t, m.dir)
	srv.SetDown(true)
	out, err := run(t, "", "ledger", "autosync")
	if err == nil || !strings.Contains(out, "sync failing") {
		t.Fatalf("the pass with the store down: %v\n%s", err, out)
	}
	if len(executions(t)) != 0 || strings.Contains(out, "wake release: fired") {
		t.Fatalf("an unreached store woke the queue:\n%s", out)
	}
}

func TestWakePushesFire(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	wakeFixture(t, m.dir)
	approvedStory(t, "one")
	must(t, "project", "add")
	enableWake(t, m.dir)
	out := must(t, "ledger", "autosync")
	runs := executions(t)
	if len(runs) != 1 {
		t.Fatalf("%d executions after the wake:\n%s", len(runs), out)
	}
	if _, _, ok := serverItem(t, srv, runs[0].ID); !ok {
		t.Fatalf("satelle.dev lacks the woken run %s:\n%s", runs[0].ID, out)
	}
	fired := slices.ContainsFunc(srv.Events("shared", "que_release"), func(e *chainpb.Event) bool { return e.GetKind() == item.KindQueueFired })
	if !fired {
		t.Fatalf("satelle.dev lacks the queue's fire:\n%s", out)
	}
}
