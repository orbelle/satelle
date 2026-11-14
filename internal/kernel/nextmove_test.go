package kernel

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// moveKernel is a repo whose delivery workflow holds every class of state a
// seated item can be at: a waits state, an operator state, a state with no
// exit, a park state, and states that enqueue into queues with no filter
// (release; first then second; shut, moved terminal by a case; badref,
// whose trigger base names no commit). Executions run the runbook workflow,
// which has a park state.
func moveKernel(t *testing.T) *Kernel {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "satelle.toml"), `[workflows]
default = "delivery"
[[workflows.rule]]
workflow = "runbook"
kind = "execution"
`)
	write(t, filepath.Join(root, "workflows", "runbook.toml"), `name = "runbook"
[states.backlog]
start = true
[states.in_progress]
[states.held]
park = true
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "in_progress"
[[transition]]
from = ["in_progress"]
to = "done"
[[transition]]
from = ["backlog"]
to = "held"
`)
	transitions := ""
	for _, to := range []string{"review", "ops", "stuck", "approved", "approved2", "closedq", "badbase", "blocked"} {
		transitions += "[[transition]]\nfrom = [\"in_progress\"]\nto = \"" + to + "\"\n"
	}
	write(t, filepath.Join(root, "workflows", "delivery.toml"), `name = "delivery"
[states.backlog]
start = true
[states.in_progress]
[states.review]
waits = "the reviewer's approval"
[states.ops]
performer = { operator = "deploy-record" }
[states.stuck]
[states.approved]
enqueue = ["release"]
[states.approved2]
enqueue = ["first", "second"]
[states.closedq]
enqueue = ["shut"]
[states.badbase]
enqueue = ["badref"]
[states.blocked]
park = true
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "in_progress"
[[transition]]
from = ["backlog", "review", "ops", "approved", "approved2", "closedq", "badbase"]
to = "done"
`+transitions)
	for _, q := range []string{"release", "first", "second", "shut"} {
		write(t, filepath.Join(root, "queues", q+".toml"), "title = \""+q+"\"\n[trigger]\nworkflow = \"runbook\"\nmin_size = 0\n")
	}
	write(t, filepath.Join(root, "queues", "badref.toml"), "title = \"badref\"\n[trigger]\nworkflow = \"runbook\"\nbase = \"refs/heads/nope\"\nmin_size = 0\n")
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

const me, other = "s-me", "s-other"

func seatOn(t *testing.T, k *Kernel, id, session string) {
	t.Helper()
	if _, err := k.Seat(id, session, "local:/tmp/w", "", "me"); err != nil {
		t.Fatal(err)
	}
}

func fire(t *testing.T, k *Kernel, queue string) string {
	t.Helper()
	f, err := k.Fire(queue, "", "queue fire", "me")
	if err != nil || f.Run == "" {
		t.Fatalf("fire %s: %+v %v", queue, f, err)
	}
	return f.Run
}

// terminalQueue moves a queue to its workflow's terminal state.
func terminalQueue(t *testing.T, k *Kernel, name string) {
	t.Helper()
	id, _ := k.ResolveQueue(name)
	move(t, k, id, "done")
}

func TestNextMove(t *testing.T) {
	type want struct {
		act, target, to, queue string
		carries                bool
		targetIsRun            bool
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, k *Kernel) (item, run string)
		want  want
	}{
		{"terminal", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "done")
			return s, ""
		}, want{}},
		{"park", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "blocked")
			return s, ""
		}, want{}},
		{"waits", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "review")
			return s, ""
		}, want{}},
		{"operator state", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "ops")
			return s, ""
		}, want{}},
		{"plain transition", func(t *testing.T, k *Kernel) (string, string) {
			return story(t, k, "s"), ""
		}, want{act: MoveSet, to: "in_progress"}},
		{"no exit", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "stuck")
			return s, ""
		}, want{}},
		{"fireable queue", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "approved")
			return s, ""
		}, want{act: MoveFire, target: "release"}},
		{"first queue's open run is another session's, second fires", func(t *testing.T, k *Kernel) (string, string) {
			x := story(t, k, "x")
			move(t, k, x, "in_progress", "approved2")
			run := fire(t, k, "first")
			seatOn(t, k, run, other)
			if _, err := k.Dequeue("second", []string{x}, "me"); err != nil {
				t.Fatal(err)
			}
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "approved2")
			return s, ""
		}, want{act: MoveFire, target: "second"}},
		{"terminal queue holding the item", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "closedq")
			terminalQueue(t, k, "shut")
			return s, ""
		}, want{}},
		{"removed from its open queue", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "approved")
			if _, err := k.Dequeue("release", []string{s}, "me"); err != nil {
				t.Fatal(err)
			}
			return s, ""
		}, want{act: MoveSet, to: "done"}},
		{"removed from a terminal queue", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "closedq")
			if _, err := k.Dequeue("shut", []string{s}, "me"); err != nil {
				t.Fatal(err)
			}
			terminalQueue(t, k, "shut")
			return s, ""
		}, want{act: MoveSet, to: "done"}},
		{"trigger base names no commit", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "badbase")
			return s, ""
		}, want{}},
		{"run took it, this session holds the run", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "approved")
			run := fire(t, k, "release")
			seatOn(t, k, run, me)
			return s, run
		}, want{act: MoveSet, to: "in_progress", queue: "release", carries: true, targetIsRun: true}},
		{"run took it, another session holds the run", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "approved")
			seatOn(t, k, fire(t, k, "release"), other)
			return s, ""
		}, want{}},
		{"run took it, no session holds the run", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "approved")
			return s, fire(t, k, "release")
		}, want{act: MoveSeat, queue: "release", carries: true, targetIsRun: true}},
		{"approved after the fire, this session holds the run", func(t *testing.T, k *Kernel) (string, string) {
			x := story(t, k, "x")
			move(t, k, x, "in_progress", "approved")
			run := fire(t, k, "release")
			seatOn(t, k, run, me)
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "approved")
			return s, run
		}, want{act: MoveSet, to: "in_progress", queue: "release", targetIsRun: true}},
		{"approved after the fire, no session holds the run", func(t *testing.T, k *Kernel) (string, string) {
			x := story(t, k, "x")
			move(t, k, x, "in_progress", "approved")
			run := fire(t, k, "release")
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "approved")
			return s, run
		}, want{act: MoveSeat, queue: "release", targetIsRun: true}},
		{"approved after the fire, another session holds the run", func(t *testing.T, k *Kernel) (string, string) {
			x := story(t, k, "x")
			move(t, k, x, "in_progress", "approved")
			seatOn(t, k, fire(t, k, "release"), other)
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "approved")
			return s, ""
		}, want{}},
		{"open run at a park state", func(t *testing.T, k *Kernel) (string, string) {
			s := story(t, k, "s")
			move(t, k, s, "in_progress", "approved")
			move(t, k, fire(t, k, "release"), "held")
			return s, ""
		}, want{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := moveKernel(t)
			id, run := c.setup(t, k)
			got, err := k.NextMove(id, me)
			if err != nil {
				t.Fatal(err)
			}
			w := c.want
			target := w.target
			switch {
			case w.targetIsRun:
				target = run
			case w.act == MoveSet:
				target = id
			}
			if got.Item != id || got.Act != w.act || got.Target != target || got.To != w.to || got.Queue != w.queue || got.Carries != w.carries {
				t.Fatalf("NextMove = %+v, want act %q target %q to %q queue %q carries %v", got, w.act, target, w.to, w.queue, w.carries)
			}
			if w.act != "" && got.Status == "" {
				t.Fatalf("a move without the item's status: %+v", got)
			}
		})
	}
}

// Fire refuses with exactly what fireReady refuses, and QueueMembers reads a
// terminal queue; runInputs is what the fire took.
func TestFireReadyIsFiresRefusal(t *testing.T) {
	k := moveKernel(t)
	s := story(t, k, "s")
	move(t, k, s, "in_progress", "closedq")
	terminalQueue(t, k, "shut")
	b := story(t, k, "b")
	move(t, k, b, "in_progress", "badbase")
	for _, q := range []string{"shut", "badref"} {
		id, _ := k.ResolveQueue(q)
		_, ready := k.fireReady(id, "")
		_, fired := k.Fire(q, "", "queue fire", "me")
		if ready == nil || fired == nil || ready.Error() != fired.Error() {
			t.Errorf("%s: fireReady %v, Fire %v", q, ready, fired)
		}
	}
	if _, _, m, err := k.QueueMembers("shut"); err != nil || !slices.Equal(m.Members, []string{s}) {
		t.Errorf("members of a terminal queue: %+v %v", m, err)
	}
	r := story(t, k, "r")
	move(t, k, r, "in_progress", "approved")
	f, err := k.Fire("release", "", "queue fire", "me")
	if err != nil {
		t.Fatal(err)
	}
	if got := k.runInputs(f.Run); !slices.Equal(got, f.Inputs) {
		t.Errorf("runInputs %v, fire inputs %v", got, f.Inputs)
	}
	if got := k.runInputs(r); got != nil {
		t.Errorf("runInputs of an item that is no run: %v", got)
	}
	if _, err := k.Fire("release", "", "queue fire", "me"); err == nil || !strings.Contains(err.Error(), ErrRunOpen.Error()) {
		t.Errorf("a second fire with the run open: %v", err)
	}
}
