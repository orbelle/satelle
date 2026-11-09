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
	before := chainLen(t, k, q.ID)
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
	events, _ := k.Ledger.Read(q.ID)
	if len(events) != before+1 || events[before].Kind != item.KindUpdated || events[before].Actor != "satelle" {
		t.Fatalf("open on the adopted chain: %d -> %d events, want one item.updated by satelle", before, len(events))
	}
	assertChainIsFile(t, k, q.ID, filepath.Join(root, QueuesDir, "release.toml"))
	k.Close()
	k = open()
	if n := chainLen(t, k, q.ID); n != before+1 {
		t.Fatalf("a second open wrote to the adopted chain: %d events, want %d", n, before+1)
	}
	got, st, ms, err := k.QueueMembers("release")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != q.ID || got.Title != "Release queue" || got.Inputs == nil || got.Inputs.Status != "done" ||
		got.Trigger == nil || got.Trigger.Base != "origin/main" {
		t.Fatalf("queue %+v", got)
	}
	if !slices.Equal(st.Explicit, []string{second.ID}) || !slices.Equal(ms.Members, []string{second.ID}) {
		t.Fatalf("explicit %v members %v, want [%s]", st.Explicit, ms.Members, second.ID)
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
	if _, _, ms, err := k.QueueMembers("inbox"); err != nil || !slices.Equal(ms.Members, []string{s.ID}) {
		t.Fatalf("members %v %v", ms.Members, err)
	}
}

// Editing the file changes the queue on the next open, which mirrors the edit
// onto its chain as one event.
func TestQueueFileEditTakesEffect(t *testing.T) {
	root, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("t")})
	k.Close()
	queueFile(t, root, "q", "title = \"Q\"\n[filter]\nstatus = \"done\"\n[trigger]\ntask = \""+task.ID+"\"\n")
	k = open()
	s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	if _, _, ms, _ := k.QueueMembers("q"); len(ms.Members) != 0 {
		t.Fatalf("members under status=done: %v", ms.Members)
	}
	size := chainLen(t, k, "que_q")
	k.Close()
	queueFile(t, root, "q", "title = \"Q2\"\n[filter]\nstatus = \"backlog\"\nkind = \"story\"\n[trigger]\ntask = \""+task.ID+"\"\nmin_size = 1\n")
	k = open()
	q, _, ms, err := k.QueueMembers("q")
	if err != nil || !slices.Equal(ms.Members, []string{s.ID}) || q.Title != "Q2" || q.Trigger.MinSize != 1 {
		t.Fatalf("after the edit: %+v members %v %v", q, ms.Members, err)
	}
	if n := chainLen(t, k, "que_q"); n != size+1 {
		t.Fatalf("the edit wrote %d -> %d events to the chain, want one", size, n)
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
	k.Close()
	queueFile(t, root, "release", "title = \"Release 2\"\n[trigger]\ntask = \""+task.ID+"\"\nmin_size = 2\n")
	k = open()
	events, _ := k.Ledger.Read("que_release")
	var mirrored int
	for _, e := range events {
		if e.Actor == "me" {
			continue
		}
		mirrored++
		var p struct{ Cause string }
		if e.Actor != "satelle" || e.Decode(&p) != nil || p.Cause != "queue file .satelle/queues/release.toml" {
			t.Fatalf("%s by %q cause %q, want satelle and the file", e.Kind, e.Actor, p.Cause)
		}
	}
	if mirrored != 2 || events[len(events)-1].Kind != item.KindUpdated {
		t.Fatalf("%d events by satelle, want the genesis and one item.updated", mirrored)
	}
}

// assertChainIsFile folds id's chain without the overlay and compares its
// title, inputs and trigger to the queue file at path.
func assertChainIsFile(t *testing.T, k *Kernel, id, path string) {
	t.Helper()
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := item.Fold(events)
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.ParseQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != def.Title {
		t.Fatalf("chain title %q, file %q", got.Title, def.Title)
	}
	if (got.Inputs == nil) != (def.Filter == nil) || (got.Inputs != nil && *got.Inputs != *def.Filter) {
		t.Fatalf("chain inputs %+v, file filter %+v", got.Inputs, def.Filter)
	}
	if (got.Trigger == nil) != (def.Trigger == nil) || (got.Trigger != nil && *got.Trigger != *def.Trigger) {
		t.Fatalf("chain trigger %+v, file trigger %+v", got.Trigger, def.Trigger)
	}
}

// A file queue's chain records the file: the genesis carries the whole
// definition, an edit writes one item.updated, an unchanged file nothing, and
// a body or tags update does not provoke a mirror.
func TestQueueFileMirrors(t *testing.T) {
	root, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("t")})
	k.Close()
	file := filepath.Join(root, QueuesDir, "m.toml")
	reopen := func(want int) {
		t.Helper()
		k.Close()
		k = open()
		if n := chainLen(t, k, "que_m"); n != want {
			t.Fatalf("que_m has %d events, want %d", n, want)
		}
	}
	lastPayload := func() map[string]any {
		t.Helper()
		events, _ := k.Ledger.Read("que_m")
		var p map[string]any
		if err := events[len(events)-1].Decode(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// (a) the genesis carries the definition
	queueFile(t, root, "m", "title = \"M\"\n[filter]\nkind = \"story\"\n[trigger]\ntask = \""+task.ID+"\"\nmin_size = 2\n")
	k = open()
	if n := chainLen(t, k, "que_m"); n != 1 {
		t.Fatalf("que_m has %d events, want its genesis", n)
	}
	assertChainIsFile(t, k, "que_m", file)
	reopen(1)

	// (b) an edit writes one item.updated naming title, inputs and trigger
	queueFile(t, root, "m", "title = \"M2\"\n[filter]\nstatus = \"done\"\n[trigger]\ntask = \""+task.ID+"\"\nmin_size = 1\n")
	reopen(2)
	events, _ := k.Ledger.Read("que_m")
	if e := events[1]; e.Kind != item.KindUpdated || e.Actor != "satelle" {
		t.Fatalf("mirror event %s by %s", e.Kind, e.Actor)
	}
	p := lastPayload()
	for _, key := range []string{"title", "inputs", "trigger"} {
		if _, ok := p[key]; !ok {
			t.Fatalf("mirror payload %v lacks %s", p, key)
		}
	}
	if p["cause"] != "queue file .satelle/queues/m.toml" {
		t.Fatalf("mirror cause %v", p["cause"])
	}
	assertChainIsFile(t, k, "que_m", file)
	reopen(2)

	// (e) a body and tags update is not the definition
	if _, err := k.Update("que_m", item.Fields{Body: ptr("notes"), Tags: &[]string{"x"}}, "me"); err != nil {
		t.Fatal(err)
	}
	reopen(3)

	// (c) dropping [filter] and [trigger] clears them on the chain
	queueFile(t, root, "m", "title = \"M2\"\n")
	reopen(4)
	p = lastPayload()
	if in, _ := p["inputs"].(map[string]any); in == nil || len(in) != 0 {
		t.Fatalf("cleared inputs %v, want {}", p["inputs"])
	}
	if tr, _ := p["trigger"].(map[string]any); tr == nil || tr["task"] != nil || tr["workflow"] != nil {
		t.Fatalf("cleared trigger %v, want neither target", p["trigger"])
	}
	assertChainIsFile(t, k, "que_m", file)
	reopen(4)
	k.Close()

	// (d) a chain from before the mirror (a bare genesis) is backfilled once
	k = open()
	kind, title, wf, st := item.QueueKind, "legacy", "default", "backlog"
	if _, err := k.Ledger.Append("que_legacy", item.KindCreated, "satelle", item.Fields{Kind: &kind, Title: &title, Workflow: &wf, Status: &st}); err != nil {
		t.Fatal(err)
	}
	k.Close()
	queueFile(t, root, "legacy", "title = \"Legacy\"\n[trigger]\ntask = \""+task.ID+"\"\nmin_size = 3\n")
	k = open()
	events, _ = k.Ledger.Read("que_legacy")
	if len(events) != 2 || events[1].Kind != item.KindUpdated {
		t.Fatalf("legacy chain %d events, want the genesis and one item.updated", len(events))
	}
	assertChainIsFile(t, k, "que_legacy", filepath.Join(root, QueuesDir, "legacy.toml"))
	k.Close()
	k = open()
	if n := chainLen(t, k, "que_legacy"); n != 2 {
		t.Fatalf("a second open wrote to que_legacy: %d events", n)
	}
	k.Close()
}

// A reader folding only the chain gets the file's title, filter and trigger,
// min_size included, before and after an edit.
func TestQueueFileChainFoldsToFile(t *testing.T) {
	root, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("t")})
	k.Close()
	file := filepath.Join(root, QueuesDir, "f.toml")
	queueFile(t, root, "f", "title = \"F\"\n[filter]\nkind = \"story\"\nstatus = \"done\"\n[trigger]\ntask = \""+task.ID+"\"\nbase = \"origin/main\"\nmin_size = 3\n")
	k = open()
	assertChainIsFile(t, k, "que_f", file)
	events, _ := k.Ledger.Read("que_f")
	if it, _ := item.Fold(events); it.Trigger == nil || it.Trigger.MinSize != 3 || it.Trigger.Base != "origin/main" {
		t.Fatalf("raw fold trigger %+v", it.Trigger)
	}
	k.Close()
	queueFile(t, root, "f", "title = \"F\"\n[filter]\nkind = \"task\"\n[trigger]\ntask = \""+task.ID+"\"\nmin_size = 0\n")
	k = open()
	assertChainIsFile(t, k, "que_f", file)
	k.Close()
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

// A queue file adopting an id this runtime holds no chain for writes nothing:
// the chain exists elsewhere and arrives with the next pull. Until it does,
// the queue verbs refuse naming the id (sto_5c706973).
func TestAdoptedQueueWithoutChainWritesNothing(t *testing.T) {
	root, open := queueKernel(t)
	queueFile(t, root, "release", "id = \"que_x\"\ntitle = \"Release\"\n[filter]\nkind = \"story\"\n")
	k := open()
	if n := chainLen(t, k, "que_x"); n != 0 {
		t.Fatalf("open wrote %d events to the adopted que_x", n)
	}
	s := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s")})
	want := "queue release adopts que_x, which this runtime does not hold yet; it arrives with the next ledger pull (or remove id from .satelle/queues/release.toml to start a new queue)"
	if _, err := k.Enqueue("release", []string{s.ID}, "", "me"); err == nil || err.Error() != want {
		t.Fatalf("enqueue: %v", err)
	}
	if _, _, _, err := k.QueueMembers("release"); err == nil || !strings.Contains(err.Error(), "adopts que_x") {
		t.Fatalf("members: %v", err)
	}
	if _, err := k.Fire("release", "", "by hand", "me"); err == nil || !strings.Contains(err.Error(), "adopts que_x") {
		t.Fatalf("fire: %v", err)
	}
	if n := chainLen(t, k, "que_x"); n != 0 {
		t.Fatalf("a refused verb wrote %d events to que_x", n)
	}
}

// Two runtimes holding the same chain for an adopted id, with a file newer
// than the chain, each mirror the file as the same event: same id, time and
// hash, so their chains never fork (sto_5c706973).
func TestQueueMirrorIsDeterministic(t *testing.T) {
	rootA, openA := queueKernel(t)
	rootB, openB := queueKernel(t)
	a := openA()
	q := mustCreate(t, a, item.Fields{Kind: ptr(item.QueueKind), Title: ptr("old"), Inputs: &item.Selection{Kind: "task"}})
	for _, body := range []string{"one", "two"} {
		if _, err := a.Update(q.ID, item.Fields{Body: ptr(body)}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	events, err := a.Ledger.Read(q.ID)
	if err != nil || len(events) != 3 {
		t.Fatalf("chain %d events %v", len(events), err)
	}
	a.Close()
	b := openB()
	if _, err := b.Ledger.Import(events); err != nil {
		t.Fatal(err)
	}
	b.Close()

	file := "id = \"" + q.ID + "\"\ntitle = \"Release\"\n[filter]\nkind = \"story\"\n"
	queueFile(t, rootA, "release", file)
	queueFile(t, rootB, "release", file)
	a, b = openA(), openB()
	ea, _ := a.Ledger.Read(q.ID)
	eb, _ := b.Ledger.Read(q.ID)
	if len(ea) != 4 || len(eb) != 4 {
		t.Fatalf("chains %d and %d events, want one mirror event each", len(ea), len(eb))
	}
	if ea[3].Hash != eb[3].Hash || ea[3].ID != eb[3].ID || !ea[3].At.Equal(events[2].At) {
		t.Fatalf("mirror events differ: %+v vs %+v", ea[3], eb[3])
	}
	if ea[3].Kind != item.KindUpdated || ea[3].Actor != "satelle" {
		t.Fatalf("mirror event %s by %s", ea[3].Kind, ea[3].Actor)
	}
	if err := a.Ledger.Verify(); err != nil {
		t.Fatal(err)
	}
}
