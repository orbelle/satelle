package kernel

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func queueFile(t *testing.T, root, name, body string) {
	t.Helper()
	write(t, filepath.Join(root, QueuesDir, name+".toml"), body)
}

// A queue file adopting an existing chain keeps its members and fire
// history, shows the file's definition, and writes nothing on open.
func TestQueueFileAdoptsChain(t *testing.T) {
	root, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	q := newQueue(t, k, &item.Selection{Kind: "task"}, item.Trigger{Task: task.ID})
	first := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("first")})
	if _, err := k.Enqueue(q.ID, []string{first.ID}, "", "me"); err != nil {
		t.Fatal(err)
	}
	fire, err := k.Fire(q.ID, "", "by hand", "me")
	if err != nil || fire.Run == "" {
		t.Fatalf("fire %+v %v", fire, err)
	}
	if res, err := k.Transition(context.Background(), fire.Run, "cancelled", "me"); err != nil || !res.Moved {
		t.Fatalf("closing the run %+v %v", res, err)
	}
	second := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("second")})
	if _, err := k.Enqueue(q.ID, []string{second.ID}, "", "me"); err != nil {
		t.Fatal(err)
	}
	before, _ := k.Ledger.Size(q.ID)
	k.Close()

	queueFile(t, root, "release", `id = "`+q.ID+`"
title = "Release queue"
[filter]
kind = "story"
status = "done"
[trigger]
task = "`+task.ID+`"
base = "origin/main"
`)
	k = open()
	if after, _ := k.Ledger.Size(q.ID); after != before {
		t.Fatalf("open wrote to the adopted chain: %d -> %d events", before, after)
	}
	got, st, members, err := k.QueueMembers("release")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != q.ID || got.Title != "Release queue" || got.Inputs == nil || got.Inputs.Status != "done" ||
		got.Trigger == nil || got.Trigger.Base != "origin/main" {
		t.Fatalf("queue %+v", got)
	}
	if !slices.Equal(st.Explicit, []string{second.ID}) || !slices.Equal(members, []string{second.ID}) {
		t.Fatalf("explicit %v members %v, want [%s]", st.Explicit, members, second.ID)
	}
	if st.LastFire == nil || st.LastFire.Run != fire.Run || st.LastFire.Cause != "by hand" {
		t.Fatalf("last fire %+v, want run %s", st.LastFire, fire.Run)
	}
	queues, err := k.List(cacheQuery(item.QueueKind))
	if err != nil || len(queues) != 1 || queues[0].Title != "Release queue" {
		t.Fatalf("queues %+v %v", queues, err)
	}
	if def := k.QueueFile(q.ID); def == nil || def.Name != "release" {
		t.Fatalf("QueueFile(%s) = %+v", q.ID, def)
	}
}

// A queue file without an id gets the chain que_<name>, created once.
func TestQueueFileMaterializes(t *testing.T) {
	root, open := queueKernel(t)
	queueFile(t, root, "inbox", "title = \"Inbox\"\n[filter]\nkind = \"story\"\n")
	k := open()
	if n := chainLen(t, k, "que_inbox"); n != 1 {
		t.Fatalf("que_inbox has %d events, want its genesis", n)
	}
	it, err := k.Get("que_inbox")
	if err != nil || it.Kind != item.QueueKind || it.Title != "Inbox" || it.Inputs == nil || it.Inputs.Kind != "story" {
		t.Fatalf("que_inbox %+v %v", it, err)
	}
	k.Close()
	k = open()
	if n := chainLen(t, k, "que_inbox"); n != 1 {
		t.Fatalf("a second open wrote to que_inbox: %d events", n)
	}
	if queues, _ := k.List(cacheQuery(item.QueueKind)); len(queues) != 1 {
		t.Fatalf("queues %+v", queues)
	}
	s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	if _, _, members, err := k.QueueMembers("inbox"); err != nil || !slices.Equal(members, []string{s.ID}) {
		t.Fatalf("members %v %v", members, err)
	}
}

// Editing the file changes the queue on the next open; nothing is written to
// its chain.
func TestQueueFileEditTakesEffect(t *testing.T) {
	root, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("t")})
	k.Close()
	queueFile(t, root, "q", "title = \"Q\"\n[filter]\nstatus = \"done\"\n[trigger]\ntask = \""+task.ID+"\"\n")
	k = open()
	s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	if _, _, members, _ := k.QueueMembers("q"); len(members) != 0 {
		t.Fatalf("members under status=done: %v", members)
	}
	size, _ := k.Ledger.Size("que_q")
	k.Close()
	queueFile(t, root, "q", "title = \"Q2\"\n[filter]\nstatus = \"backlog\"\nkind = \"story\"\n[trigger]\ntask = \""+task.ID+"\"\nmin_size = 1\n")
	k = open()
	q, _, members, err := k.QueueMembers("q")
	if err != nil || !slices.Equal(members, []string{s.ID}) || q.Title != "Q2" || q.Trigger.MinSize != 1 {
		t.Fatalf("after the edit: %+v members %v %v", q, members, err)
	}
	if n, _ := k.Ledger.Size("que_q"); n != size {
		t.Fatalf("the edit wrote to the chain: %d -> %d", size, n)
	}
}

// A file-defined queue's title, filter and trigger change only in its file.
func TestQueueFileRefusesSet(t *testing.T) {
	root, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("t")})
	other := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("other")})
	k.Close()
	queueFile(t, root, "release", "title = \"Release\"\n[trigger]\ntask = \""+task.ID+"\"\n")
	k = open()
	for name, f := range map[string]item.Fields{
		"title":   {Title: ptr("x")},
		"inputs":  {Inputs: &item.Selection{Status: "x"}},
		"clear":   {Inputs: &item.Selection{}},
		"trigger": {Trigger: &item.Trigger{Task: other.ID}},
	} {
		if _, err := k.Update("que_release", f, "me"); err == nil || !strings.Contains(err.Error(), ".satelle/queues/release.toml") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := k.Update("que_release", item.Fields{Body: ptr("notes"), Tags: &[]string{"x"}}, "me"); err != nil {
		t.Fatalf("body and tags: %v", err)
	}
	created := newQueue(t, k, nil, item.Trigger{Task: other.ID})
	if created.ID == "que_release" {
		t.Fatal("create reused the file's chain")
	}
	got, err := k.Get("que_release")
	if err != nil || got.Title != "Release" || got.Trigger.Task != task.ID || got.Body != "notes" {
		t.Fatalf("que_release %+v %v", got, err)
	}
}

// A retired file queue refuses a move into a state that enqueues into it
// before any gate runs.
func TestQueueFileRetiredRefusesMove(t *testing.T) {
	root, open := queueKernel(t)
	write(t, filepath.Join(root, "satelle.toml"), `[workflows]
default = "default"
[[workflows.rule]]
workflow = "runbook"
kind = "execution"
[[workflows.rule]]
workflow = "triage"
kind = "queue"
`)
	queueFile(t, root, "q", "title = \"Q\"\n")
	k := open()
	if res, err := k.Transition(context.Background(), "que_q", "closed", "me"); err != nil || !res.Moved {
		t.Fatalf("retiring %+v %v", res, err)
	}
	k.Close()
	k = open("q")
	s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	if _, err := k.Transition(context.Background(), s.ID, "done", "me"); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("moved into a state enqueueing into a retired queue: %v", err)
	}
	if events, _ := k.Ledger.Read(s.ID); len(eventKinds(events, "gate.")) != 0 || len(eventKinds(events, item.KindTransitioned)) != 0 {
		t.Fatalf("refused move recorded %v", eventKinds(events, ""))
	}
}

// Queue files are validated: each malformed file is a problem naming it, and
// two files cannot bind one chain.
func TestQueueFileProblems(t *testing.T) {
	cases := map[string]string{
		"syntax":         "title = \n",
		"unknown key":    "title = \"x\"\ncolour = \"red\"\n",
		"no title":       "[filter]\nkind = \"story\"\n",
		"empty filter":   "title = \"x\"\n[filter]\n",
		"both targets":   "title = \"x\"\n[trigger]\ntask = \"tas_1\"\nworkflow = \"triage\"\n",
		"no target":      "title = \"x\"\n[trigger]\nbase = \"main\"\n",
		"negative size":  "title = \"x\"\n[trigger]\ntask = \"tas_1\"\nmin_size = -1\n",
		"workflow":       "title = \"x\"\n[trigger]\nworkflow = \"nope\"\n",
		"invalid id":     "id = \"bad id\"\ntitle = \"x\"\n",
		"bad file name!": "title = \"x\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root, _ := queueKernel(t)
			file := "q"
			if name == "bad file name!" {
				file = "Rel_Ease"
			}
			queueFile(t, root, file, body)
			problems := Validate(root)
			if len(problems) == 0 {
				t.Fatal("no problem")
			}
			joined := ""
			for _, p := range problems {
				joined += p.Error() + "\n"
			}
			if !strings.Contains(joined, filepath.Join(QueuesDir, file+".toml")) {
				t.Fatalf("problems do not name the file:\n%s", joined)
			}
		})
	}
	t.Run("two files bind one chain", func(t *testing.T) {
		root, _ := queueKernel(t)
		queueFile(t, root, "a", "id = \"que_x\"\ntitle = \"a\"\n")
		queueFile(t, root, "b", "id = \"que_x\"\ntitle = \"b\"\n")
		problems := Validate(root)
		if len(problems) != 1 || !strings.Contains(problems[0].Error(), "binds queue que_x") {
			t.Fatalf("problems %v", problems)
		}
	})
}

func TestQueueIndex(t *testing.T) {
	s := Settings{Items: ItemSettings{Prefix: map[string]string{item.QueueKind: "qq"}}}
	idx := QueueIndex(s, map[string]*workflow.Queue{"a": {Name: "a"}, "b": {Name: "b", ID: "que_b1"}})
	if idx["qq_a"] == nil || idx["que_b1"] == nil || len(idx) != 2 {
		t.Fatalf("index %v", idx)
	}
}

func cacheQuery(kind string) cache.Query { return cache.Query{Kind: kind} }

func chainLen(t *testing.T, k *Kernel, id string) int {
	t.Helper()
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return len(events)
}
