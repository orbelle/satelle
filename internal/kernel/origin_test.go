package kernel

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func payloadOrigin(t *testing.T, e ledger.Event) (ledger.Origin, bool) {
	t.Helper()
	var m map[string]json.RawMessage
	if len(e.Payload) == 0 {
		return ledger.Origin{}, false
	}
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		t.Fatalf("%s payload %s: %v", e.Kind, e.Payload, err)
	}
	raw, ok := m[ledger.OriginKey]
	if !ok {
		return ledger.Origin{}, false
	}
	var o ledger.Origin
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatal(err)
	}
	return o, true
}

// originKernel opens a kernel on a fresh git repo, its clock fixed.
func originKernel(t *testing.T) (*Kernel, string) {
	t.Helper()
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", DefaultBaseBranch)
	write(t, filepath.Join(dir, "README"), "r\n")
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "init")
	root := filepath.Join(dir, ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	k, err := Open(Options{Root: root, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	k.Now = func() time.Time { at = at.Add(time.Second); return at }
	return k, dir
}

// TestOriginDoesNotChangeKernelReads writes an item's life through a kernel
// whose store records an origin, strips the origin from every payload into a
// twin chain with the same ids and times, and reads both through the
// kernel's decoders.
func TestOriginDoesNotChangeKernelReads(t *testing.T) {
	k, dir := originKernel(t)
	k.SetOrigin("m1", ledger.RolePerformer, "h")

	it := mustCreate(t, k, item.Fields{Kind: ptr("story"), Title: ptr("t"), Criteria: ptr([]string{"c"})})
	if _, err := k.Update(it.ID, item.Fields{Body: ptr("b"), Tags: ptr([]string{"x"})}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Seat(it.ID, "s1", LocalLocation(dir), "", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Attach(it.ID, "plan", []byte("the plan"), "text/markdown; type=plan", "me"); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "checkout", "-q", "-b", "work")
	write(t, filepath.Join(dir, "x.go"), "package x\n")
	gittest.Git(t, dir, "add", "x.go")
	gittest.Git(t, dir, "commit", "-q", "-m", "work")
	gittest.Git(t, dir, "checkout", "-q", DefaultBaseBranch)
	if _, err := k.Collect(it.ID, "work", DefaultBaseBranch, "me"); err != nil {
		t.Fatal(err)
	}
	sha, err := k.Ledger.PutBlob([]byte("out"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.append(it.ID, KindPerformerRun, "me", PerformerRun{State: it.Status, Binding: "b", Skill: "s", Output: sha}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.append(it.ID, gate.KindRun, "me", gate.Result{Transition: it.Status + "->done", Gate: "g", Result: "reject", Notes: "n", Attempt: "att_1"}); err != nil {
		t.Fatal(err)
	}
	if err := k.Release(it.ID, "s1", "me"); err != nil {
		t.Fatal(err)
	}
	run := mustCreate(t, k, item.Fields{Kind: ptr("note"), Title: ptr("run")})
	if _, err := k.append(run.ID, KindTaskRun, "me", TaskRunRecord{Task: "tsk", Inputs: []string{it.ID}}); err != nil {
		t.Fatal(err)
	}
	q := mustCreate(t, k, item.Fields{Kind: ptr(item.QueueKind), Title: ptr("q"), Inputs: &item.Selection{Kind: "story"}})
	if _, err := k.Enqueue(q.ID, []string{it.ID}, "c", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Dequeue(q.ID, []string{it.ID}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Enqueue(q.ID, []string{it.ID}, "c2", "me"); err != nil {
		t.Fatal(err)
	}

	twin, _ := originKernel(t)
	chains := map[string][]ledger.Event{}
	for _, id := range []string{it.ID, run.ID, q.ID} {
		events, err := k.Ledger.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		var stripped []ledger.Event
		prev := ""
		for _, e := range events {
			if _, ok := payloadOrigin(t, e); !ok {
				t.Fatalf("%s %s carries no origin: %s", id, e.Kind, e.Payload)
			}
			raw, err := ledger.WithOrigin(e.Payload, nil)
			if err != nil {
				t.Fatal(err)
			}
			s, err := ledger.NewEventWith(e.ID, e.At, e.Item, e.Kind, e.Actor, raw, prev)
			if err != nil {
				t.Fatal(err)
			}
			stripped = append(stripped, s)
			prev = s.Hash
		}
		if _, err := twin.Ledger.Import(stripped); err != nil {
			t.Fatal(err)
		}
		if err := twin.Cache.SyncItem(twin.Ledger, id); err != nil {
			t.Fatal(err)
		}
		chains[id] = events
		for i := range stripped {
			if _, ok := payloadOrigin(t, stripped[i]); ok {
				t.Fatalf("twin %s still carries an origin", stripped[i].Kind)
			}
		}
	}

	for id := range chains {
		a, err := k.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		b, err := twin.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("Get(%s) differs:\n%+v\n%+v", id, a, b)
		}
	}
	with, err := k.Ledger.Read(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	without, err := twin.Ledger.Read(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	ca, oka := collected(with)
	cb, okb := collected(without)
	if !oka || ca != cb || okb != oka {
		t.Fatalf("collected differs: %+v %v / %+v %v", ca, oka, cb, okb)
	}
	blobRefs := func(events []ledger.Event) []ledger.BlobRef {
		refs := BlobRefs(events)
		for i := range refs {
			refs[i].Event = ""
		}
		return refs
	}
	same := func(name string, a, b any) {
		t.Helper()
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s differs:\n%+v\n%+v", name, a, b)
		}
	}
	if len(BlobRefs(with)) != 2 || len(GateRuns(with)) != 1 {
		t.Fatalf("fixture: %d blob refs, %d gate runs", len(BlobRefs(with)), len(GateRuns(with)))
	}
	same("BlobRefs", blobRefs(with), blobRefs(without))
	same("GateRuns", GateRuns(with), GateRuns(without))
	same("attemptsLeaving", attemptsLeaving(it.Status, with), attemptsLeaving(it.Status, without))
	same("rejections", rejections(it, with), rejections(it, without))
	same("FoldSeats", item.FoldSeats(with[:3]), item.FoldSeats(without[:3]))
	same("FoldAttachments", item.FoldAttachments(with), item.FoldAttachments(without))
	if got := k.runInputs(run.ID); !reflect.DeepEqual(got, []string{it.ID}) {
		t.Fatalf("runInputs %v", got)
	}
	same("runInputs", k.runInputs(run.ID), twin.runInputs(run.ID))
	qa, err := k.Ledger.Read(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	qb, err := twin.Ledger.Read(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	same("FoldQueue", item.FoldQueue(qa), item.FoldQueue(qb))
	if st := item.FoldQueue(qa); len(st.Explicit) != 1 || st.Causes[it.ID] != "c2" {
		t.Fatalf("queue fold %+v", st)
	}
}

// TestQueueMirrorCarriesNoOrigin: two machines mirroring one queue file onto
// one tail write the identical event, whatever origin their stores record.
func TestQueueMirrorCarriesNoOrigin(t *testing.T) {
	rootA, openA := queueKernel(t)
	rootB, openB := queueKernel(t)
	file := "title = \"Release\"\n[filter]\nkind = \"story\"\n"
	queueFile(t, rootA, "release", file)
	queueFile(t, rootB, "release", file)
	a, idA, ea := genesisOf(t, openA, "release")
	b, idB, eb := genesisOf(t, openB, "release")
	if idA != idB || len(ea) != 1 || len(eb) != 1 || ea[0].Hash != eb[0].Hash {
		t.Fatalf("genesis differs: %+v / %+v", ea, eb)
	}
	a.Close()
	b.Close()

	changed := "title = \"Release\"\n[filter]\nkind = \"task\"\n"
	queueFile(t, rootA, "release", changed)
	queueFile(t, rootB, "release", changed)
	var tails []ledger.Event
	for i, open := range []func(...string) *Kernel{openA, openB} {
		k := open()
		k.SetOrigin([]string{"m1", "m2"}[i], []string{ledger.RoleSession, ledger.RoleSync}[i], "")
		// Open mirrored the change before the origin was set: take the chain
		// back to its genesis and mirror again with the origin in place.
		opened, err := k.Ledger.Read(idA)
		if err != nil || len(opened) != 2 {
			t.Fatalf("opened chain %d events %v", len(opened), err)
		}
		if err := k.Ledger.Replace(idA, opened[1].Hash, opened[:1]); err != nil {
			t.Fatal(err)
		}
		if err := k.Cache.SyncItem(k.Ledger, idA); err != nil {
			t.Fatal(err)
		}
		if wrote, err := k.Remirror(idA); err != nil || !wrote {
			t.Fatalf("remirror wrote %v: %v", wrote, err)
		}
		events, err := k.Ledger.Read(idA)
		if err != nil || len(events) != 2 {
			t.Fatalf("chain %d events %v", len(events), err)
		}
		tails = append(tails, events[1])
		for _, e := range events {
			if _, ok := payloadOrigin(t, e); ok || strings.Contains(string(e.Payload), `"origin"`) {
				t.Fatalf("mirror event carries an origin: %s", e.Payload)
			}
		}
	}
	x, y := tails[0], tails[1]
	if x.ID != y.ID || !x.At.Equal(y.At) || string(x.Payload) != string(y.Payload) || x.Hash != y.Hash {
		t.Fatalf("mirror events differ:\n%+v\n%+v", x, y)
	}
}
