package verb_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// waveWF is one lane for every category plus an epic-parent lane that carries a
// container step (`ready`, waits_on_children) declaring the child schedule.
// schedule == "" leaves the key absent; a non-empty value is the deprecated
// `schedule` key.
func waveWF(schedule string) map[string]string {
	line := ""
	if schedule != "" {
		line = `schedule = "` + schedule + `"` + "\n"
	}
	return waveWFLine(line)
}

// waveSequentialWF declares the user's override, `sequential = true`.
func waveSequentialWF() map[string]string { return waveWFLine("sequential = true\n") }

// waveWFLine is waveWF with the container step's schedule line authored verbatim.
func waveWFLine(line string) map[string]string {
	return routeHalves(
		`["*"]
obligations = ["raised", "coded", "closed"]
park = { state = "blocked" }
cancel = { state = "cancelled" }

[epic-parent]
obligations = ["raised", "ready", "coded", "closed"]
park = { state = "blocked" }
cancel = { state = "cancelled" }
`,
		`[raised]
status = "backlog"
start = true

[ready]
status = "ready"
waits_on_children = true
`+line+`requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
}

func waveEpic(t *testing.T) workitem.Item {
	t.Helper()
	return mkEpicStory(t, map[string]any{"category": "epic-parent", "tags": []string{"epic:w"}})
}

func waveChild(t *testing.T, tags ...string) workitem.Item {
	t.Helper()
	return mkEpicStory(t, map[string]any{"category": "fix", "tags": append([]string{"epic:w"}, tags...)})
}

func cancelChild(t *testing.T, id string) {
	t.Helper()
	for _, s := range []string{"in_progress", "cancelled"} {
		if err := setStatus(t, id, s); err != nil {
			t.Fatalf("%s → %s: %v", id, s, err)
		}
	}
}

func wave(t *testing.T, id string) (verb.WaveResult, error) {
	t.Helper()
	raw, err := dispatchRaw(t, "story-wave", map[string]any{"id": id})
	var res verb.WaveResult
	if err == nil {
		if uerr := json.Unmarshal(raw, &res); uerr != nil {
			t.Fatal(uerr)
		}
	}
	return res, err
}

func omittedByID(res verb.WaveResult, id string) (verb.WaveOmitted, bool) {
	for _, o := range res.Omitted {
		if o.ID == id {
			return o, true
		}
	}
	return verb.WaveOmitted{}, false
}

// fourChildEpic builds the AC5 epic: two children with no dependency, one
// (c) depending on a fourth (d) that is already done.
func fourChildEpic(t *testing.T) (epic, a, b, c, d workitem.Item) {
	t.Helper()
	epic = waveEpic(t)
	d = waveChild(t)
	closeChild(t, d.ID)
	a = waveChild(t)
	b = waveChild(t)
	c = waveChild(t, "depends-on:"+d.ID)
	return
}

func TestWaveParallelFourChildEpic(t *testing.T) {
	wireWithWorkflows(t, waveWF("parallel"))
	epic, a, b, c, d := fourChildEpic(t)

	res, err := wave(t, epic.ID)
	if err != nil {
		t.Fatalf("wave: %v", err)
	}
	want := []string{a.ID, b.ID, c.ID}
	sort.Strings(want)
	if !reflect.DeepEqual(res.Runnable, want) {
		t.Errorf("runnable = %v, want %v (d is done, so not runnable)", res.Runnable, want)
	}
	if res.Schedule != "parallel" || res.Theme != "epic:w" {
		t.Errorf("schedule/theme = %q/%q", res.Schedule, res.Theme)
	}
	for _, id := range res.Runnable {
		if id == d.ID {
			t.Errorf("the done child %s must not be runnable", d.ID)
		}
	}
	if len(res.Omitted) != 0 {
		t.Errorf("nothing waits on an unfinished dependency: %+v", res.Omitted)
	}
}

// AC1: a container with no schedule setting is parallel — every runnable child.
func TestWaveDefaultsToParallelWhenNothingIsSet(t *testing.T) {
	wireWithWorkflows(t, waveWF(""))
	epic, a, b, c, _ := fourChildEpic(t)

	res, err := wave(t, epic.ID)
	if err != nil {
		t.Fatalf("an unscheduled container must be parallel, got refusal: %v", err)
	}
	want := []string{a.ID, b.ID, c.ID}
	sort.Strings(want)
	if !reflect.DeepEqual(res.Runnable, want) || res.Schedule != "parallel" {
		t.Errorf("runnable = %v schedule = %q, want %v parallel", res.Runnable, res.Schedule, want)
	}
}

// AC2: sequential = true is the override — more than one runnable child is
// refused, exactly one is returned.
func TestWaveSequentialTrueKeyIsTheOverride(t *testing.T) {
	wireWithWorkflows(t, waveSequentialWF())
	epic := waveEpic(t)
	first := waveChild(t)
	waiting := waveChild(t, "depends-on:"+first.ID)
	other := waveChild(t)

	if res, err := wave(t, epic.ID); err == nil || !strings.Contains(err.Error(), first.ID) || !strings.Contains(err.Error(), other.ID) {
		t.Fatalf("two runnable children under sequential = true must be refused naming both: %+v %v", res, err)
	}

	// Chain the second runnable child behind the first: exactly one is eligible.
	if _, err := verb.Dispatch(context.Background(), "story-set", []byte(`{"id":"`+other.ID+`","add_tags":["depends-on:`+waiting.ID+`"]}`)); err != nil {
		t.Fatalf("add edge: %v", err)
	}
	res, err := wave(t, epic.ID)
	if err != nil || !reflect.DeepEqual(res.Runnable, []string{first.ID}) || res.Schedule != "sequential" {
		t.Errorf("one runnable child: runnable = %v schedule = %q err = %v, want [%s] sequential", res.Runnable, res.Schedule, err, first.ID)
	}
}

func TestWaveSequentialRefusesMoreThanOneRunnable(t *testing.T) {
	wireWithWorkflows(t, waveWF("sequential"))
	epic, a, b, c, _ := fourChildEpic(t)

	res, err := wave(t, epic.ID)
	if err == nil {
		t.Fatalf("sequential with three runnable children must refuse, got %+v", res)
	}
	for _, want := range []string{a.ID, b.ID, c.ID, "depends-on"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must contain %q: %v", want, err)
		}
	}
	if len(res.Runnable) != 0 {
		t.Errorf("no id may be emitted as runnable on a refusal: %v", res.Runnable)
	}
}

// Sequential never selects by order: or created_at: the decoys favour one child
// and the command still refuses.
func TestWaveSequentialDoesNotPickByOrder(t *testing.T) {
	wireWithWorkflows(t, waveWF("sequential"))
	epic := waveEpic(t)
	first := waveChild(t, "order:1")
	time.Sleep(2 * time.Millisecond) // time-subject: created_at separation, so created_at is a real (and refused) tie-break candidate
	second := waveChild(t, "order:2")

	_, err := wave(t, epic.ID)
	if err == nil {
		t.Fatal("two runnable children under sequential must refuse; order: is not a tie-break")
	}
	if !strings.Contains(err.Error(), first.ID) || !strings.Contains(err.Error(), second.ID) {
		t.Errorf("refusal must name both runnable ids: %v", err)
	}
}

func TestWaveSequentialReturnsTheOneRunnable(t *testing.T) {
	wireWithWorkflows(t, waveWF("sequential"))
	epic := waveEpic(t)
	first := waveChild(t)
	second := waveChild(t, "depends-on:"+first.ID)

	res, err := wave(t, epic.ID)
	if err != nil {
		t.Fatalf("wave: %v", err)
	}
	if !reflect.DeepEqual(res.Runnable, []string{first.ID}) {
		t.Errorf("runnable = %v, want only %s", res.Runnable, first.ID)
	}
	o, ok := omittedByID(res, second.ID)
	if !ok || !strings.Contains(o.Reason, first.ID) {
		t.Errorf("the waiting child must be omitted naming %s: %+v", first.ID, res.Omitted)
	}

	// Once the first is done the second is the one runnable child.
	closeChild(t, first.ID)
	res, err = wave(t, epic.ID)
	if err != nil || !reflect.DeepEqual(res.Runnable, []string{second.ID}) {
		t.Errorf("after %s is done: runnable = %v err = %v, want [%s]", first.ID, res.Runnable, err, second.ID)
	}
}

// A dependency that is in progress or cancelled does not satisfy the edge; a
// cancelled one is named as such.
func TestWaveOmitsChildWhoseDependencyIsNotDone(t *testing.T) {
	wireWithWorkflows(t, waveWF("parallel"))
	epic := waveEpic(t)
	cancelled := waveChild(t)
	cancelChild(t, cancelled.ID)
	running := waveChild(t)
	if err := setStatus(t, running.ID, "in_progress"); err != nil {
		t.Fatal(err)
	}
	onRunning := waveChild(t, "depends-on:"+running.ID)
	onCancelled := waveChild(t, "depends-on:"+cancelled.ID)
	free := waveChild(t)

	res, err := wave(t, epic.ID)
	if err != nil {
		t.Fatalf("wave: %v", err)
	}
	want := []string{running.ID, free.ID}
	sort.Strings(want)
	if !reflect.DeepEqual(res.Runnable, want) {
		t.Errorf("runnable = %v, want %v (a cancelled child is not runnable)", res.Runnable, want)
	}
	if o, ok := omittedByID(res, onRunning.ID); !ok || !strings.Contains(o.Reason, running.ID) || !strings.Contains(o.Reason, "in_progress") {
		t.Errorf("child waiting on an in-progress dependency: %+v", res.Omitted)
	}
	o, ok := omittedByID(res, onCancelled.ID)
	if !ok || !strings.Contains(o.Reason, cancelled.ID) || !strings.Contains(o.Reason, "cancelled") {
		t.Fatalf("child waiting on a cancelled dependency must name it: %+v", res.Omitted)
	}
	if len(o.WaitingOn) != 1 || o.WaitingOn[0].ID != cancelled.ID || !o.WaitingOn[0].Cancelled {
		t.Errorf("waiting_on must carry the cancelled dependency: %+v", o.WaitingOn)
	}
}

// Membership is the epic: set, not parent_id: a story linked only by parent_id
// is not a candidate, and a tag-only story is.
func TestWaveMembershipIsTheEpicSet(t *testing.T) {
	wireWithWorkflows(t, waveWF("parallel"))
	epic := waveEpic(t)
	tagged := waveChild(t)
	linked := mkEpicStory(t, map[string]any{"category": "fix", "parent_id": epic.ID})

	res, err := wave(t, epic.ID)
	if err != nil {
		t.Fatalf("wave: %v", err)
	}
	if !reflect.DeepEqual(res.Runnable, []string{tagged.ID}) {
		t.Errorf("runnable = %v, want only the tagged child (not %s)", res.Runnable, linked.ID)
	}
}

// AC4: every refusal is an error that names the gap and emits no runnable id.
func TestWaveRefusals(t *testing.T) {
	cases := map[string]struct {
		wf    map[string]string
		setup func(t *testing.T) workitem.Item
		want  string
	}{
		"two epic-parents on one tag": {
			wf: waveWF("parallel"),
			setup: func(t *testing.T) workitem.Item {
				waveEpic(t)
				e := waveEpic(t)
				waveChild(t)
				return e
			},
			want: "2 epic-parents carry epic:w",
		},
		"no epic tag": {
			wf: waveWF("parallel"),
			setup: func(t *testing.T) workitem.Item {
				return mkEpicStory(t, map[string]any{"category": "epic-parent"})
			},
			want: "no epic:<theme> tag",
		},
		"not an epic-parent": {
			wf: waveWF("parallel"),
			setup: func(t *testing.T) workitem.Item {
				return waveChild(t)
			},
			want: "not an epic-parent",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wireWithWorkflows(t, tc.wf)
			container := tc.setup(t)
			raw, err := dispatchRaw(t, "story-wave", map[string]any{"id": container.ID})
			if err == nil {
				t.Fatalf("must refuse, got %s", raw)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal must name the gap %q: %v", tc.want, err)
			}
			if len(raw) != 0 {
				t.Errorf("a refusal must emit no response body, got %s", raw)
			}
		})
	}
}

// AC1: the assessment writes nothing — every story and the ledger are unchanged.
func TestWaveWritesNothing(t *testing.T) {
	db := wireWithWorkflowsStore(t, waveWF("parallel"))
	epic, a, b, c, d := fourChildEpic(t)
	snap := func() string {
		out := []string{string(call(t, "story-list", map[string]any{"tag": "epic:w"}))}
		for _, id := range []string{epic.ID, a.ID, b.ID, c.ID, d.ID} {
			out = append(out, string(call(t, "ledger-list", map[string]any{"story_id": id, "limit": 1000})))
		}
		leases, err := db.Leases.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		lb, _ := json.Marshal(leases)
		return strings.Join(append(out, string(lb)), "\n")
	}
	before := snap()
	if _, err := wave(t, epic.ID); err != nil {
		t.Fatalf("wave: %v", err)
	}
	if after := snap(); after != before {
		t.Errorf("story wave changed the store (stories, ledger or leases):\nbefore %s\nafter  %s", before, after)
	}
}

// --- cross-repo depends-on:<repo>/<id> ---

const sibName = "satelle-server"

// mkSibling builds a real satelle repo called name: the given workflows on disk,
// and a database under the isolated runtime plane holding whatever build creates.
// A nil build leaves the repo with no database at all.
func mkSibling(t *testing.T, name string, workflows map[string]string, build func(db *store.DB)) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	wfDir := filepath.Join(root, config.DefaultDataDir, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, config.DefaultDataDir, config.ConfigName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for n, body := range workflows {
		if err := os.WriteFile(filepath.Join(wfDir, n+".toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if build == nil {
		return root
	}
	db, err := store.Open(config.Config{}.ResolveDB(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	build(db)
	return root
}

// sibStory creates one story in a sibling database and returns its id.
func sibStory(t *testing.T, db *store.DB, in workitem.CreateInput) string {
	t.Helper()
	in.Kind = workitem.KindStory
	in.Title = "sibling"
	it, err := db.Stories.Create(context.Background(), in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return it.ID
}

// siblingEpic wires the invoking repo with the wave workflows, registers the
// sibling roots, and returns an epic whose children may depend across repos.
func siblingEpic(t *testing.T, registry map[string][]string) workitem.Item {
	t.Helper()
	wireWithWorkflows(t, waveWF("parallel"))
	verb.StubSiblingRegistry(t, registry, t.TempDir())
	return waveEpic(t)
}

func waitingOn(t *testing.T, res verb.WaveResult, child workitem.Item) verb.WaveBlocker {
	t.Helper()
	o, ok := omittedByID(res, child.ID)
	if !ok || len(o.WaitingOn) != 1 {
		t.Fatalf("%s must be omitted waiting on exactly one dependency, got %+v", child.ID, res.Omitted)
	}
	if !strings.Contains(o.Reason, o.WaitingOn[0].ID+" ("+o.WaitingOn[0].Status+")") && !o.WaitingOn[0].Cancelled {
		t.Errorf("reason %q must carry the blocker as <id> (<status>)", o.Reason)
	}
	return o.WaitingOn[0]
}

// AC4: a child is runnable only when the sibling story is terminal on the sibling's
// own route; otherwise it is omitted naming <repo>/<id> and the sibling status.
func TestWaveCrossRepoDependency(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	var done, running string
	root := mkSibling(t, sibName, waveWF("parallel"), func(db *store.DB) {
		done = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "done"})
		running = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "in_progress"})
	})
	epic := siblingEpic(t, map[string][]string{sibName: {root}})
	ready := waveChild(t, "depends-on:"+sibName+"/"+done)
	held := waveChild(t, "depends-on:"+sibName+"/"+running)

	res, err := wave(t, epic.ID)
	if err != nil {
		t.Fatalf("wave: %v", err)
	}
	if !reflect.DeepEqual(res.Runnable, []string{ready.ID}) {
		t.Errorf("runnable = %v, want only the child whose sibling story is done (%s)", res.Runnable, ready.ID)
	}
	b := waitingOn(t, res, held)
	if b.ID != sibName+"/"+running || b.Status != "in_progress" {
		t.Errorf("waiting_on = %+v, want %s/%s (in_progress)", b, sibName, running)
	}
}

// AC5: a sibling story that is done under an open container does not satisfy the
// edge — for an epic-set parent and for a parent_id ancestor.
func TestWaveCrossRepoDependencyNeedsEveryContainerTerminal(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	var openEpic, underOpenEpic, closedEpicChild, grand, underGrand string
	root := mkSibling(t, sibName, waveWF("parallel"), func(db *store.DB) {
		openEpic = sibStory(t, db, workitem.CreateInput{Category: "epic-parent", Status: "ready", Tags: []string{"epic:open"}})
		underOpenEpic = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "done", Tags: []string{"epic:open"}})
		sibStory(t, db, workitem.CreateInput{Category: "epic-parent", Status: "done", Tags: []string{"epic:closed"}})
		closedEpicChild = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "done", Tags: []string{"epic:closed"}})
		grand = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "in_progress"})
		mid := sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "done", ParentID: grand})
		underGrand = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "done", ParentID: mid})
	})
	epic := siblingEpic(t, map[string][]string{sibName: {root}})
	byEpic := waveChild(t, "depends-on:"+sibName+"/"+underOpenEpic)
	byGrand := waveChild(t, "depends-on:"+sibName+"/"+underGrand)
	ok := waveChild(t, "depends-on:"+sibName+"/"+closedEpicChild)

	res, err := wave(t, epic.ID)
	if err != nil {
		t.Fatalf("wave: %v", err)
	}
	if !reflect.DeepEqual(res.Runnable, []string{ok.ID}) {
		t.Errorf("runnable = %v, want only the child under a closed epic (%s)", res.Runnable, ok.ID)
	}
	if b := waitingOn(t, res, byEpic); b.Status != "done; container "+openEpic+" open" || b.ID != sibName+"/"+underOpenEpic {
		t.Errorf("epic-set container: waiting_on = %+v", b)
	}
	o, _ := omittedByID(res, byEpic.ID)
	if want := sibName + "/" + underOpenEpic + " (done; container " + openEpic + " open)"; !strings.Contains(o.Reason, want) {
		t.Errorf("reason %q must contain %q", o.Reason, want)
	}
	if b := waitingOn(t, res, byGrand); b.Status != "done; container "+grand+" open" {
		t.Errorf("parent_id ancestor: waiting_on = %+v, want the grandparent %s named", b, grand)
	}
}

// A done lane:trunk story released to trunk itself, so it satisfies the edge even
// under an open container; an untagged done sibling under the same open epic still
// holds its dependant.
func TestWaveCrossRepoLaneTrunkDoneUnderOpenContainerSatisfies(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	var openEpic, trunk, epicBranch string
	root := mkSibling(t, sibName, waveWF("parallel"), func(db *store.DB) {
		openEpic = sibStory(t, db, workitem.CreateInput{Category: "epic-parent", Status: "ready", Tags: []string{"epic:open"}})
		trunk = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "done", Tags: []string{"epic:open", "lane:trunk"}})
		epicBranch = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "done", Tags: []string{"epic:open"}})
	})
	epic := siblingEpic(t, map[string][]string{sibName: {root}})
	onTrunk := waveChild(t, "depends-on:"+sibName+"/"+trunk)
	held := waveChild(t, "depends-on:"+sibName+"/"+epicBranch)

	res, err := wave(t, epic.ID)
	if err != nil {
		t.Fatalf("wave: %v", err)
	}
	if !reflect.DeepEqual(res.Runnable, []string{onTrunk.ID}) {
		t.Errorf("runnable = %v, want only the child of the lane:trunk story (%s)", res.Runnable, onTrunk.ID)
	}
	if b := waitingOn(t, res, held); b.Status != "done; container "+openEpic+" open" {
		t.Errorf("untagged sibling: waiting_on = %+v, want its open container %s named", b, openEpic)
	}
}

// A local done target satisfies its edge under an open container, lane:trunk or not.
func TestWaveLocalLaneTrunkDoneUnderOpenContainerSatisfies(t *testing.T) {
	wireWithWorkflows(t, waveWF("parallel"))
	epic := waveEpic(t)
	trunk := waveChild(t, "lane:trunk")
	closeChild(t, trunk.ID)
	plain := waveChild(t)
	closeChild(t, plain.ID)
	onTrunk := waveChild(t, "depends-on:"+trunk.ID)
	onPlain := waveChild(t, "depends-on:"+plain.ID)

	res, err := wave(t, epic.ID)
	if err != nil {
		t.Fatalf("wave: %v", err)
	}
	want := []string{onTrunk.ID, onPlain.ID}
	sort.Strings(want)
	if !reflect.DeepEqual(res.Runnable, want) {
		t.Errorf("runnable = %v, want %v: the epic %s is open and both targets are done", res.Runnable, want, epic.ID)
	}
}

// AC5/AC6: a done sibling story whose parent_id names no story cannot be judged
// finished — its container is unknown — so the edge is unresolved, never satisfied.
func TestWaveCrossRepoDependencyMissingContainerIsUnresolved(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	var orphan string
	root := mkSibling(t, sibName, waveWF("parallel"), func(db *store.DB) {
		orphan = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "done", ParentID: "sty_00000000"})
	})
	epic := siblingEpic(t, map[string][]string{sibName: {root}})
	child := waveChild(t, "depends-on:"+sibName+"/"+orphan)

	res, err := wave(t, epic.ID)
	if err != nil {
		t.Fatalf("wave: %v", err)
	}
	if len(res.Runnable) != 0 {
		t.Errorf("runnable = %v, want none: the sibling story's container is missing", res.Runnable)
	}
	if b := waitingOn(t, res, child); !strings.HasPrefix(b.Status, "unresolved: ") || !strings.Contains(b.Status, "sty_00000000") {
		t.Errorf("waiting_on = %+v, want an unresolved status naming the missing container", b)
	}
}

// AC6: whatever cannot be read is a blocker whose status starts "unresolved:",
// never a satisfied dependency.
func TestWaveCrossRepoUnresolvedIsABlocker(t *testing.T) {
	cases := []struct {
		name  string
		tag   func(t *testing.T) (registry map[string][]string, ref string, after func(t *testing.T))
		wants []string
	}{
		{
			name: "story not found",
			tag: func(t *testing.T) (map[string][]string, string, func(*testing.T)) {
				root := mkSibling(t, sibName, waveWF("parallel"), func(db *store.DB) {})
				return map[string][]string{sibName: {root}}, "sty_deadbeef", nil
			},
			wants: []string{"not found"},
		},
		{
			name: "ambiguous repo name",
			tag: func(t *testing.T) (map[string][]string, string, func(*testing.T)) {
				var id string
				root := mkSibling(t, sibName, waveWF("parallel"), func(db *store.DB) {
					id = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "done"})
				})
				// Unambiguous when the tag is written, ambiguous when wave reads it.
				return map[string][]string{sibName: {root}}, id, func(t *testing.T) {
					verb.StubSiblingRegistry(t, map[string][]string{sibName: {root, t.TempDir()}}, t.TempDir())
				}
			},
			wants: []string{"ambiguous repo name"},
		},
		{
			name: "unreadable workflows",
			tag: func(t *testing.T) (map[string][]string, string, func(*testing.T)) {
				var id string
				root := mkSibling(t, sibName, waveWF("parallel"), func(db *store.DB) {
					id = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "done"})
				})
				wf := filepath.Join(root, config.DefaultDataDir, "workflows")
				if err := os.RemoveAll(wf); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(wf, []byte("not a directory"), 0o644); err != nil {
					t.Fatal(err)
				}
				return map[string][]string{sibName: {root}}, id, nil
			},
			wants: []string{"workflows", "unreadable"},
		},
		{
			name: "absent database",
			tag: func(t *testing.T) (map[string][]string, string, func(*testing.T)) {
				root := mkSibling(t, sibName, waveWF("parallel"), nil)
				return map[string][]string{sibName: {root}}, "sty_deadbeef", nil
			},
			wants: []string{"no such file"},
		},
		{
			name: "a selected column is missing",
			tag: func(t *testing.T) (map[string][]string, string, func(*testing.T)) {
				var id string
				root := mkSibling(t, sibName, waveWF("parallel"), func(db *store.DB) {
					id = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "done"})
					if _, err := db.SQL().Exec(`ALTER TABLE work_items DROP COLUMN assignee`); err != nil {
						t.Fatal(err)
					}
				})
				return map[string][]string{sibName: {root}}, id, nil
			},
			wants: []string{"read stories", "assignee"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SATELLE_HOME", t.TempDir())
			registry, ref, after := tc.tag(t)
			epic := siblingEpic(t, registry)
			child := waveChild(t, "depends-on:"+sibName+"/"+ref)
			if after != nil {
				after(t)
			}
			res, err := wave(t, epic.ID)
			if err != nil {
				t.Fatalf("an unresolved dependency must not fail wave: %v", err)
			}
			if len(res.Runnable) != 0 {
				t.Fatalf("an unresolved dependency must never be satisfied, runnable = %v", res.Runnable)
			}
			b := waitingOn(t, res, child)
			if !strings.HasPrefix(b.Status, "unresolved:") {
				t.Fatalf("status = %q, want it to begin %q", b.Status, "unresolved:")
			}
			for _, w := range tc.wants {
				if !strings.Contains(b.Status, w) {
					t.Errorf("status %q must say why: missing %q", b.Status, w)
				}
			}
		})
	}
}

// AC7: a cancelled sibling story does not satisfy the edge and reads as cancelled.
func TestWaveCrossRepoCancelledDependency(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	var id string
	root := mkSibling(t, sibName, waveWF("parallel"), func(db *store.DB) {
		id = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "cancelled"})
	})
	epic := siblingEpic(t, map[string][]string{sibName: {root}})
	child := waveChild(t, "depends-on:"+sibName+"/"+id)

	res, err := wave(t, epic.ID)
	if err != nil {
		t.Fatalf("wave: %v", err)
	}
	o, ok := omittedByID(res, child.ID)
	if !ok || !strings.Contains(o.Reason, "cancelled") || len(o.WaitingOn) != 1 || !o.WaitingOn[0].Cancelled || o.WaitingOn[0].ID != sibName+"/"+id {
		t.Fatalf("a cancelled sibling story must be reported cancelled: %+v", res.Omitted)
	}
}

// AC8: the sibling is read in-process and read-only — its database file is
// byte-identical, with an unchanged mtime, after a wave call.
func TestWaveCrossRepoReadLeavesTheSiblingDatabaseUntouched(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	var id string
	root := mkSibling(t, sibName, waveWF("parallel"), func(db *store.DB) {
		id = sibStory(t, db, workitem.CreateInput{Category: "fix", Status: "done"})
	})
	epic := siblingEpic(t, map[string][]string{sibName: {root}})
	child := waveChild(t, "depends-on:"+sibName+"/"+id)

	dbPath := config.Config{}.ResolveDB(root)
	fingerprint := func() (string, time.Time) {
		b, err := os.ReadFile(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", sha256.Sum256(b)), st.ModTime()
	}
	sumBefore, mtimeBefore := fingerprint()
	res, err := wave(t, epic.ID)
	if err != nil {
		t.Fatalf("wave: %v", err)
	}
	if !reflect.DeepEqual(res.Runnable, []string{child.ID}) {
		t.Fatalf("runnable = %v, want the child (sibling story is done)", res.Runnable)
	}
	if sumAfter, mtimeAfter := fingerprint(); sumAfter != sumBefore || !mtimeAfter.Equal(mtimeBefore) {
		t.Errorf("the sibling database changed: sha %s -> %s, mtime %v -> %v", sumBefore, sumAfter, mtimeBefore, mtimeAfter)
	}
}
