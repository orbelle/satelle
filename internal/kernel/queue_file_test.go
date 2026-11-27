package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func queueFile(t *testing.T, root, name, body string) {
	t.Helper()
	write(t, filepath.Join(root, QueuesDir, name+".toml"), body)
}

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

	queueFile(t, root, "m", "title = \"M\"\n[filter]\nkind = \"story\"\n[trigger]\ntask = \""+task.ID+"\"\nmin_size = 2\n")
	k = open()
	if n := chainLen(t, k, "que_m"); n != 1 {
		t.Fatalf("que_m has %d events, want its genesis", n)
	}
	assertChainIsFile(t, k, "que_m", file)
	reopen(1)

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

	if _, err := k.Update("que_m", item.Fields{Body: ptr("notes"), Tags: &[]string{"x"}}, "me"); err != nil {
		t.Fatal(err)
	}
	reopen(3)

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

func TestQueueFileProblems(t *testing.T) {
	cases := map[string]string{
		"syntax":          "title = \n",
		"unknown key":     "title = \"x\"\ncolour = \"red\"\n",
		"misspelt filter": "title = \"x\"\n[filter]\nstatuz = \"approved\"\n",
		"no title":        "[filter]\nkind = \"story\"\n",
		"empty filter":    "title = \"x\"\n[filter]\n",
		"both targets":    "title = \"x\"\n[trigger]\ntask = \"tas_1\"\nworkflow = \"triage\"\n",
		"no target":       "title = \"x\"\n[trigger]\nbase = \"main\"\n",
		"negative size":   "title = \"x\"\n[trigger]\ntask = \"tas_1\"\nmin_size = -1\n",
		"unknown overlap": "title = \"x\"\n[trigger]\ntask = \"tas_1\"\noverlap = \"sometimes\"\n",
		"workflow":        "title = \"x\"\n[trigger]\nworkflow = \"nope\"\n",
		"invalid id":      "id = \"bad id\"\ntitle = \"x\"\n",
		"bad file name!":  "title = \"x\"\n",
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

func genesisOf(t *testing.T, open func(...string) *Kernel, ref string) (*Kernel, string, []ledger.Event) {
	t.Helper()
	k := open()
	id, _ := k.ResolveQueue(ref)
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return k, id, events
}

func TestQueueGenesisIsDeterministic(t *testing.T) {
	rootA, openA := queueKernel(t)
	rootB, openB := queueKernel(t)
	file := "title = \"Release\"\n[filter]\nkind = \"story\"\n"
	queueFile(t, rootA, "release", file)
	queueFile(t, rootB, "release", file)
	a, idA, ea := genesisOf(t, openA, "release")
	b, idB, eb := genesisOf(t, openB, "release")
	if idA != idB || len(ea) != 1 || len(eb) != 1 {
		t.Fatalf("chains %s %d events, %s %d events, want one each", idA, len(ea), idB, len(eb))
	}
	ga, gb := ea[0], eb[0]
	if ga.ID != gb.ID || !ga.At.Equal(gb.At) || ga.Prev != gb.Prev || ga.Hash != gb.Hash {
		t.Fatalf("genesis events differ:\n%+v\n%+v", ga, gb)
	}
	if ga.Kind != item.KindCreated || ga.Actor != "satelle" || ga.Prev != "" {
		t.Fatalf("genesis %s by %s after %q", ga.Kind, ga.Actor, ga.Prev)
	}
	if want := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC); !ga.At.Equal(want) {
		t.Fatalf("genesis time %s, want %s", ga.At, want)
	}
	sum := sha256.Sum256([]byte(idA + "\n" + "" + "\n" + string(ga.Payload)))
	if want := hex.EncodeToString(sum[:])[:24]; ga.ID != want {
		t.Fatalf("genesis id %s, want %s", ga.ID, want)
	}
	for _, k := range []*Kernel{a, b} {
		if err := k.Ledger.Verify(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestQueueGenesisLeavesExistingChain(t *testing.T) {
	root, open := queueKernel(t)
	k := open()
	qk, wf, st := item.QueueKind, "default", ""
	if name, start, err := k.WorkflowFor(item.Item{Kind: qk, Title: "legacy"}); err == nil {
		wf, st = name, start
	}
	old, err := k.Ledger.Append("que_legacy", item.KindCreated, "satelle", item.Fields{Kind: &qk, Workflow: &wf, Status: &st, Title: ptr("old")})
	if err != nil {
		t.Fatal(err)
	}
	k.Close()
	queueFile(t, root, "legacy", "title = \"Legacy\"\n[filter]\nkind = \"story\"\n")
	for range 2 {
		k = open()
		events, err := k.Ledger.Read("que_legacy")
		if err != nil || len(events) != 2 {
			t.Fatalf("que_legacy %d events %v, want genesis and one mirror", len(events), err)
		}
		if events[0].ID != old.ID || events[0].Hash != old.Hash || events[0].At.Equal(queueGenesisAt) {
			t.Fatalf("held genesis changed: %+v, was %+v", events[0], old)
		}
		if events[1].Kind != item.KindUpdated || !events[1].At.Equal(events[0].At) {
			t.Fatalf("mirror event %+v", events[1])
		}
		k.Close()
	}
}

func TestQueueGenesisDiffersWithFile(t *testing.T) {
	base := "title = \"Release\"\n[filter]\nkind = \"story\"\n"
	baseRoot, openBase := queueKernel(t)
	queueFile(t, baseRoot, "release", base)
	bk, _, be := genesisOf(t, openBase, "release")
	defer bk.Close()
	queueRule := "[workflows]\ndefault = \"default\"\n[[workflows.rule]]\nworkflow = \"runbook\"\nkind = \"execution\"\n[[workflows.rule]]\nworkflow = \"triage\"\nkind = \"queue\"\n"
	cases := []struct{ name, file, settings string }{
		{"title", "title = \"Other\"\n[filter]\nkind = \"story\"\n", ""},
		{"filter", "title = \"Release\"\n[filter]\nkind = \"task\"\n", ""},
		{"trigger", base + "[trigger]\nworkflow = \"triage\"\n", ""},
		{"workflow rule", base, queueRule},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var hashes []string
			var root string
			var open func(...string) *Kernel
			for range 2 {
				root, open = queueKernel(t)
				if c.settings != "" {
					write(t, filepath.Join(root, "satelle.toml"), c.settings)
				}
				queueFile(t, root, "release", c.file)
				k, _, events := genesisOf(t, open, "release")
				if len(events) != 1 {
					t.Fatalf("%d events", len(events))
				}
				hashes = append(hashes, events[0].Hash)
				k.Close()
			}
			if hashes[0] != hashes[1] {
				t.Fatalf("two runtimes with one variant disagree: %v", hashes)
			}
			if hashes[0] == be[0].Hash {
				t.Fatalf("variant genesis equals the base genesis %s", hashes[0])
			}
			if c.name != "title" {
				return
			}
			queueFile(t, root, "release", base)
			k, _, events := genesisOf(t, open, "release")
			defer k.Close()
			if len(events) != 2 || events[0].Hash != hashes[1] || events[0].Hash == be[0].Hash || events[1].Kind != item.KindUpdated {
				t.Fatalf("after matching the base file: %d events, genesis %s", len(events), events[0].Hash)
			}
		})
	}
}

func TestMirrorTailRefusesAnyOtherEvent(t *testing.T) {
	root, open := queueKernel(t)
	var mirrors [][]ledger.Event
	for _, title := range []string{"Draft", "Release", "Final"} {
		queueFile(t, root, "release", "title = \""+title+"\"\n[filter]\nkind = \"story\"\n")
		k := open()
		events, err := k.Ledger.Read("que_release")
		if err != nil {
			t.Fatal(err)
		}
		mirrors = append(mirrors, events)
		k.Close()
	}
	queueFile(t, root, "adopted", "id = \"que_adopted\"\ntitle = \"Adopted\"\n")
	k := open()
	defer k.Close()
	for i, m := range mirrors {
		if len(m) != i+1 {
			t.Fatalf("fixture: %d mirror events, want %d", len(m), i+1)
		}
	}
	after := func(chain []ledger.Event, kind, actor string, payload any) []ledger.Event {
		t.Helper()
		last := chain[len(chain)-1]
		e, err := ledger.NewEventWith("ext_"+kind+actor, last.At, last.Item, kind, actor, payload, last.Hash)
		if err != nil {
			t.Fatal(err)
		}
		return append(slices.Clone(chain), e)
	}
	genesis := mirrors[0][0]
	byOther, err := ledger.NewEventWith(genesis.ID, genesis.At, genesis.Item, genesis.Kind, "me", genesis.Payload, "")
	if err != nil {
		t.Fatal(err)
	}
	mirror := func(chain []ledger.Event, title string) []ledger.Event {
		t.Helper()
		last := chain[len(chain)-1]
		e, err := ledger.NewEventWith("mir_"+title, last.At, last.Item, item.KindUpdated, "satelle",
			map[string]any{"title": title, "cause": "queue file .satelle/queues/release.toml"}, last.Hash)
		if err != nil {
			t.Fatal(err)
		}
		return append(slices.Clone(chain), e)
	}
	worked := after(mirrors[1], item.KindQueueAdded, "me", item.QueueEntry{Item: "sto_1"})
	tail := mirror(worked, "Draft")
	adoptedGenesis, err := ledger.NewEventWith("ext_adopted", genesis.At, "que_adopted", item.KindCreated, "me", item.Fields{Title: ptr("Adopted")}, "")
	if err != nil {
		t.Fatal(err)
	}
	adoptedTail := mirror(after([]ledger.Event{adoptedGenesis}, item.KindQueueAdded, "me", item.QueueEntry{Item: "sto_1"}), "Adopted")
	cases := []struct {
		name  string
		id    string
		chain []ledger.Event
		from  int
		want  bool
	}{
		{"genesis alone", "que_release", mirrors[0], 0, true},
		{"genesis and one mirror update", "que_release", mirrors[1], 0, true},
		{"genesis and two mirror updates", "que_release", mirrors[2], 0, true},
		{"plus a queue.added", "que_release", worked, 0, false},
		{"plus a user's title update", "que_release", after(mirrors[1], item.KindUpdated, "me", item.Fields{Title: ptr("mine")}), 0, false},
		{"plus a satelle event with no cause", "que_release", after(mirrors[1], item.KindUpdated, "satelle", item.Fields{Title: ptr("x")}), 0, false},
		{"plus a queue.fired", "que_release", after(mirrors[0], item.KindQueueFired, "satelle", item.QueueFire{Run: "exe_1", Cause: "queue file x"}), 0, false},
		{"a genesis by another actor", "que_release", []ledger.Event{byOther}, 0, false},
		{"an id a file adopts", "que_adopted", mirrors[0], 0, false},
		{"an id no file defines", "que_nofile", mirrors[0], 0, false},
		{"an empty chain", "que_release", nil, 0, false},
		{"a mirror tail past a queue.added", "que_release", tail, len(worked), true},
		{"two mirror updates past a queue.added", "que_release", mirror(tail, "Final"), len(worked), true},
		{"a mirror tail on an id a file adopts", "que_adopted", adoptedTail, len(adoptedTail) - 1, true},
		{"no tail past the fork point", "que_release", tail, len(tail), false},
		{"a mirror tail on an id no file defines", "que_nofile", tail, len(worked), false},
		{"a mirror tail then a queue.added", "que_release", after(tail, item.KindQueueAdded, "me", item.QueueEntry{Item: "sto_2"}), len(worked), false},
		{"a mirror tail then a user's update", "que_release", after(tail, item.KindUpdated, "me", item.Fields{Title: ptr("mine")}), len(worked), false},
		{"a mirror tail then a satelle event with no cause", "que_release", after(tail, item.KindUpdated, "satelle", item.Fields{Title: ptr("x")}), len(worked), false},
		{"a mirror tail then a queue.fired", "que_release", after(tail, item.KindQueueFired, "satelle", item.QueueFire{Run: "exe_1", Cause: "queue file x"}), len(worked), false},
		{"a tail starting at a queue.added", "que_release", tail, len(worked) - 1, false},
	}
	for _, c := range cases {
		if got := k.MirrorTail(c.id, c.chain, c.from); got != c.want {
			t.Errorf("%s: MirrorTail(from %d) = %v, want %v", c.name, c.from, got, c.want)
		}
	}
	if wrote, err := k.Remirror("que_release"); err != nil || wrote {
		t.Fatalf("Remirror on an agreeing chain: wrote %v, %v", wrote, err)
	}
}

func overlapQueue(t *testing.T, overlap, rest string) (*Kernel, item.Item) {
	t.Helper()
	return overlapQueueSized(t, overlap, rest, 0)
}

func overlapQueueSized(t *testing.T, overlap, rest string, minSize int) (*Kernel, item.Item) {
	t.Helper()
	root, open := queueKernel(t)
	k := open()
	task := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("release")})
	k.Close()
	trig := "[trigger]\ntask = \"" + task.ID + "\"\nmin_size = " + strconv.Itoa(minSize) + "\n"
	if overlap != "" {
		trig += "overlap = \"" + overlap + "\"\n"
	}
	queueFile(t, root, "q", "title = \"Q\"\n"+rest+trig)
	return open(), task
}

func firedClaims(t *testing.T, k *Kernel, queueID string) []item.QueueFire {
	t.Helper()
	events, err := k.Ledger.Read(queueID)
	if err != nil {
		t.Fatal(err)
	}
	var out []item.QueueFire
	for _, e := range events {
		if e.Kind == item.KindQueueFired {
			var f item.QueueFire
			if err := e.Decode(&f); err != nil {
				t.Fatal(err)
			}
			out = append(out, f)
		}
	}
	return out
}

func TestQueueOverlapTakesOnlyUntaken(t *testing.T) {
	k, _ := overlapQueue(t, "allow", "[filter]\nkind = \"story\"\n")
	s1 := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s1")})
	s2 := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s2")})
	a, err := k.Fire("q", "", "by hand", "me")
	if err != nil || a.Run == "" || !slices.Equal(a.Inputs, []string{s1.ID, s2.ID}) {
		t.Fatalf("fire 1 %+v %v", a, err)
	}
	if _, err := k.Enqueue("q", []string{s1.ID}, "", "me"); err != nil {
		t.Fatal(err)
	}
	s3 := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s3")})
	b, err := k.Fire("q", "", "by hand", "me")
	if err != nil || b.Run == "" || b.Run == a.Run || !slices.Equal(b.Inputs, []string{s3.ID}) {
		t.Fatalf("fire 2 with run %s open: %+v %v", a.Run, b, err)
	}
	if _, st, _, err := k.QueueMembers("q"); err != nil || !slices.Equal(st.Explicit, []string{s1.ID}) {
		t.Fatalf("an explicit member the open run holds is kept queued: %v %v", st.Explicit, err)
	}

	disjoint(t, ledgerRun(t, k, a.Run), ledgerRun(t, k, b.Run))
	claims := firedClaims(t, k, "que_q")
	if len(claims) != 2 || claims[0].Run != a.Run || claims[1].Run != b.Run || !slices.Equal(claims[1].Kept, []string{s1.ID}) {
		t.Fatalf("claims %+v", claims)
	}

	c, err := k.Fire("q", "", "by hand", "me")
	if err != nil || c.Run != "" {
		t.Fatalf("fire 3 with every member held: %+v %v", c, err)
	}
	if n := len(firedClaims(t, k, "que_q")); n != 2 {
		t.Fatalf("an empty fire wrote a claim: %d", n)
	}

	if res, err := k.Transition(context.Background(), a.Run, "cancelled", "me"); err != nil || !res.Moved {
		t.Fatalf("cancel %s: %+v %v", a.Run, res, err)
	}
	d, err := k.Fire("q", "", "by hand", "me")
	if err != nil || d.Run == "" || !slices.Equal(d.Inputs, []string{s1.ID, s2.ID}) {
		t.Fatalf("fire 4 after %s closed: %+v %v", a.Run, d, err)
	}
	disjoint(t, ledgerRun(t, k, b.Run), ledgerRun(t, k, d.Run))
}

func TestQueueOverlapHoldsRunRecordInputs(t *testing.T) {
	k, task := overlapQueue(t, "allow", "[filter]\nkind = \"story\"\n")
	s1 := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s1")})
	s2 := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s2")})
	other := newQueue(t, k, nil, item.Trigger{Task: task.ID})
	if _, err := k.Enqueue(other.ID, []string{s1.ID}, "", "me"); err != nil {
		t.Fatal(err)
	}
	legacy, err := k.Fire(other.ID, "", "by hand", "me")
	if err != nil || legacy.Run == "" {
		t.Fatalf("fire %+v %v", legacy, err)
	}
	if _, err := k.append("que_q", item.KindQueueFired, "me", item.QueueFire{Run: legacy.Run, Task: task.ID}); err != nil {
		t.Fatal(err)
	}
	f, err := k.Fire("q", "", "by hand", "me")
	if err != nil || !slices.Equal(f.Inputs, []string{s2.ID}) {
		t.Fatalf("an open run holds its run record's inputs: %+v %v", f, err)
	}
}

func TestQueueOverlapRefused(t *testing.T) {
	for _, overlap := range []string{"", "refuse"} {
		t.Run("overlap="+overlap, func(t *testing.T) {
			k, _ := overlapQueue(t, overlap, "")
			q, _ := k.Get("que_q")
			if q.Trigger == nil || q.Trigger.Overlaps() {
				t.Fatalf("trigger %+v", q.Trigger)
			}
			s1 := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s1")})
			if _, err := k.Enqueue("q", []string{s1.ID}, "", "me"); err != nil {
				t.Fatal(err)
			}
			a, err := k.Fire("q", "", "by hand", "me")
			if err != nil || a.Run == "" {
				t.Fatalf("fire 1 %+v %v", a, err)
			}
			s2 := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s2")})
			if _, err := k.Enqueue("q", []string{s2.ID}, "", "me"); err != nil {
				t.Fatal(err)
			}
			_, err = k.Fire("q", "", "by hand", "me")
			if !errors.Is(err, ErrRunOpen) || !strings.Contains(err.Error(), a.Run) {
				t.Fatalf("fire with %s open: %v", a.Run, err)
			}
			if n := len(firedClaims(t, k, "que_q")); n != 1 {
				t.Fatalf("a refused fire wrote a claim: %d", n)
			}
		})
	}
}

func ledgerRun(t *testing.T, k *Kernel, run string) TaskRunRecord {
	t.Helper()
	events, err := k.Ledger.Read(run)
	if err != nil {
		t.Fatal(err)
	}
	var recs []TaskRunRecord
	created := 0
	for _, e := range events {
		switch e.Kind {
		case item.KindCreated:
			created++
		case KindTaskRun:
			var rec TaskRunRecord
			if err := e.Decode(&rec); err != nil {
				t.Fatal(err)
			}
			recs = append(recs, rec)
		}
	}
	if created != 1 || len(recs) != 1 {
		t.Fatalf("%s holds %d created and %d task.run events", run, created, len(recs))
	}
	return recs[0]
}

func disjoint(t *testing.T, a, b TaskRunRecord) {
	t.Helper()
	for _, id := range a.Inputs {
		if slices.Contains(b.Inputs, id) {
			t.Fatalf("%s is in both runs: %v and %v", id, a.Inputs, b.Inputs)
		}
	}
}

type overlapFixture struct {
	k      *Kernel
	task   item.Item
	s1, s2 string
}

func newOverlapFixture(t *testing.T, minSize int) overlapFixture {
	t.Helper()
	k, task := overlapQueueSized(t, "allow", "[filter]\nkind = \"story\"\n", minSize)
	s1 := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s1")})
	s2 := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s2")})
	return overlapFixture{k: k, task: task, s1: s1.ID, s2: s2.ID}
}

func (f overlapFixture) claim(t *testing.T) string {
	t.Helper()
	run := item.NewID(f.k.Settings.Prefix(ExecutionKind))
	events, err := f.k.Ledger.Read("que_q")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.k.appendAfter("que_q", item.KindQueueFired, "me", events[len(events)-1].Hash, item.QueueFire{Run: run, Task: f.task.ID, Cause: "by hand"}); err != nil {
		t.Fatal(err)
	}
	return run
}

func (f overlapFixture) runFields() item.Fields {
	kind, parent := ExecutionKind, f.task.ID
	return item.Fields{Kind: &kind, Title: ptr("release"), Parent: &parent}
}

func (f overlapFixture) createRun(t *testing.T, run string) {
	t.Helper()
	if _, err := f.k.create(f.runFields(), "me", run); err != nil {
		t.Fatal(err)
	}
}

func (f overlapFixture) unsettledHolds(t *testing.T, run string) {
	t.Helper()
	k := f.k
	x := mustCreate(t, k, item.Fields{Kind: ptr("task"), Title: ptr("x")})
	before := len(firedClaims(t, k, "que_q"))
	size, _ := k.Ledger.Size(run)
	out, err := k.Enqueue("q", []string{x.ID}, "", "me")
	if err != nil || out.Fire != nil || !strings.Contains(out.Held, run) {
		t.Fatalf("enqueue at min size with %s unsettled: %+v %v", run, out, err)
	}
	if after, _ := k.Ledger.Size(run); after != size || len(firedClaims(t, k, "que_q")) != before {
		t.Fatalf("a held enqueue wrote to %s or the queue", run)
	}
	if _, err := k.Dequeue("q", []string{x.ID}, "me"); err != nil {
		t.Fatal(err)
	}
}

func (f overlapFixture) completesThenTakesOnlyNew(t *testing.T, run string) {
	t.Helper()
	k := f.k
	out, err := k.Fire("q", "", "by hand", "me")
	if !errors.Is(err, ErrQueueMoved) || !strings.Contains(err.Error(), run) || out.Run != "" {
		t.Fatalf("fire with %s unsettled: %+v %v", run, out, err)
	}
	first := ledgerRun(t, k, run)
	if !slices.Equal(first.Inputs, []string{f.s1, f.s2}) {
		t.Fatalf("completed %s took %v", run, first.Inputs)
	}
	s3 := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s3")})
	next, err := k.Fire("q", "", "by hand", "me")
	if err != nil || next.Run == "" || next.Run == run || !slices.Equal(next.Inputs, []string{s3.ID}) {
		t.Fatalf("fire after %s settled: %+v %v", run, next, err)
	}
	disjoint(t, first, ledgerRun(t, k, next.Run))
}

func TestQueueOverlapClaimWithoutRun(t *testing.T) {
	f := newOverlapFixture(t, 1)
	run := f.claim(t)
	f.unsettledHolds(t, run)
	if n, _ := f.k.Ledger.Size(run); n != 0 {
		t.Fatalf("%s exists before a fire completes it", run)
	}
	f.completesThenTakesOnlyNew(t, run)
}

func TestQueueOverlapRunWithoutTaskRun(t *testing.T) {
	f := newOverlapFixture(t, 1)
	run := f.claim(t)
	f.createRun(t, run)
	f.unsettledHolds(t, run)
	f.completesThenTakesOnlyNew(t, run)
}

func TestQueueOverlapTaskRunNotInCache(t *testing.T) {
	f := newOverlapFixture(t, 0)
	k := f.k
	run := f.claim(t)
	f.createRun(t, run)
	if _, err := k.Ledger.Append(run, KindTaskRun, "me", TaskRunRecord{Task: f.task.ID, Inputs: []string{f.s1, f.s2}, Queue: "que_q"}); err != nil {
		t.Fatal(err)
	}
	if ev, err := k.Cache.Events(run, KindTaskRun); err != nil || len(ev) != 0 {
		t.Fatalf("the cache holds %s's task.run: %v %v", run, ev, err)
	}
	s3 := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("s3")})
	next, err := k.Fire("q", "", "by hand", "me")
	if err != nil || next.Run == "" || !slices.Equal(next.Inputs, []string{s3.ID}) {
		t.Fatalf("fire with %s's task.run only in the ledger: %+v %v", run, next, err)
	}
	disjoint(t, ledgerRun(t, k, run), ledgerRun(t, k, next.Run))
}

func TestQueueOverlapNeverCreatedRecovers(t *testing.T) {
	t.Run("completed then closed", func(t *testing.T) {
		f := newOverlapFixture(t, 0)
		k := f.k
		run := f.claim(t)
		if _, err := k.Fire("q", "", "by hand", "me"); !errors.Is(err, ErrQueueMoved) {
			t.Fatalf("fire with %s never created: %v", run, err)
		}
		if res, err := k.Transition(context.Background(), run, "cancelled", "me"); err != nil || !res.Moved {
			t.Fatalf("cancel %s: %+v %v", run, res, err)
		}
		next, err := k.Fire("q", "", "by hand", "me")
		if err != nil || next.Run == "" || next.Run == run || !slices.Equal(next.Inputs, []string{f.s1, f.s2}) {
			t.Fatalf("fire after %s closed: %+v %v", run, next, err)
		}
	})
	t.Run("an earlier unsettled claim holds nothing", func(t *testing.T) {
		f := newOverlapFixture(t, 0)
		k := f.k
		lost := f.claim(t)
		held := f.claim(t)
		if _, _, err := k.startRun(held, f.runFields(), "", TaskRunRecord{Task: f.task.ID, Inputs: []string{f.s1}, Queue: "que_q"}, "me"); err != nil {
			t.Fatal(err)
		}
		next, err := k.Fire("q", "", "by hand", "me")
		if err != nil || next.Run == "" || !slices.Equal(next.Inputs, []string{f.s2}) {
			t.Fatalf("fire past the unsettled %s: %+v %v", lost, next, err)
		}
		if n, _ := k.Ledger.Size(lost); n != 0 {
			t.Fatalf("an earlier unsettled claim %s was created", lost)
		}
	})
}

func TestStartRunAdoptsExisting(t *testing.T) {
	f := newOverlapFixture(t, 0)
	k := f.k
	run := item.NewID(k.Settings.Prefix(ExecutionKind))
	first, rec, err := k.startRun(run, f.runFields(), "", TaskRunRecord{Task: f.task.ID, Inputs: []string{f.s1}, Queue: "que_q"}, "me")
	if err != nil || first.ID != run {
		t.Fatalf("first startRun %+v %v", first, err)
	}
	again, got, err := k.startRun(run, f.runFields(), "", TaskRunRecord{Task: f.task.ID, Inputs: []string{f.s2}, Queue: "que_q"}, "me")
	if err != nil || again.ID != run || !slices.Equal(got.Inputs, rec.Inputs) {
		t.Fatalf("second startRun adopted %+v %+v %v", again, got, err)
	}
	if r := ledgerRun(t, k, run); !slices.Equal(r.Inputs, []string{f.s1}) {
		t.Fatalf("%s's task.run %v", run, r.Inputs)
	}
	story := f.s1
	other := f.runFields()
	other.Parent = &story
	if _, _, err := k.startRun(run, other, "", TaskRunRecord{}, "me"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("startRun at %s under another parent: %v", run, err)
	}
}
