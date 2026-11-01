package substratereview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/substrate"
)

// AC6: a disposition returned without its reason or evidence (or a reduce
// without its replacement) is reported not judged, never keep.
func TestBuildWithoutReasonIsNotJudged(t *testing.T) {
	in := JudgeInput{Items: []JudgeItem{
		{Item: Item{Key: "skills/a", Kind: KindSkill}},
		{Item: Item{Key: "skills/b", Kind: KindSkill}},
		{Item: Item{Key: "skills/c", Kind: KindSkill}},
		{Item: Item{Key: "skills/d", Kind: KindSkill}},
	}}
	rep := Build(in, Judgement{Items: []ItemJudgement{
		{Item: "skills/a", Disposition: DispKeep, Evidence: "a.md:1"},
		{Item: "skills/b", Disposition: DispKeep, Reason: "named rule"},
		{Item: "skills/c", Disposition: DispReduce, Reason: "narrates", Evidence: "c.md:3"},
		{Item: "skills/d", Disposition: DispKeep, Reason: "named rule", Evidence: "d.md:2", Rule: "r"},
	}})
	for key, note := range map[string]string{"skills/a": NoteNoReason, "skills/b": NoteNoReason, "skills/c": NoteNoReplacement} {
		it, _ := rep.Find(key)
		if it.Disposition != NotJudged || it.Note != note {
			t.Errorf("%s = %q (note %q), want not judged (%q)", key, it.Disposition, it.Note, note)
		}
	}
	if it, _ := rep.Find("skills/d"); it.Disposition != DispKeep || it.Citation != "d.md:2" {
		t.Errorf("a complete keep = %+v", it)
	}
	got, err := Dispositions(`{"items":[{"item":"skills/a","disposition":"keep"}]}`)
	if err != nil || got["skills/a"] != NotJudged {
		t.Errorf("Dispositions without a reason = %v, %v; want not judged", got, err)
	}
}

// Batches is one batch per area that has items, never an empty one; the rules
// ride the principles batch.
func TestBatchesIsOneBatchPerArea(t *testing.T) {
	in := JudgeInput{Rules: []Rule{{Name: "r"}}}
	for _, k := range []string{KindPrinciple, KindSkill, KindStep, KindGate, KindAgent, KindTask, KindSkill} {
		in.Items = append(in.Items, JudgeItem{Item: Item{Key: k + "/x", Kind: k}})
	}
	bs := Batches(in)
	if len(bs) != len(Areas) {
		t.Fatalf("got %d batches, want %d", len(bs), len(Areas))
	}
	for i, b := range bs {
		if b.Area != Areas[i] || len(b.Items) == 0 || b.Chunk != 0 {
			t.Errorf("batch %d = area %q, %d item(s), chunk %d", i, b.Area, len(b.Items), b.Chunk)
		}
		if (b.Area == AreaPrinciples) != (len(b.Rules) == 1) {
			t.Errorf("rules in area %s: %v", b.Area, b.Rules)
		}
	}
	if n := len(Batches(JudgeInput{})); n != 0 {
		t.Errorf("nothing to judge gave %d batch(es)", n)
	}
}

// sty_58a7bdfe AC5: an area is never split to fit a budget — 40 skills are one
// batch holding all 40.
func TestBatchesNeverSplitsAnArea(t *testing.T) {
	var skills JudgeInput
	for i := 0; i < 40; i++ {
		skills.Items = append(skills.Items, JudgeItem{Item: Item{Key: "skills/" + strings.Repeat("s", 20) + string(rune('a'+i%26)), Kind: KindSkill}})
	}
	bs := Batches(skills)
	if len(bs) != 1 || len(bs[0].Items) != 40 || bs[0].Chunk != 0 {
		t.Fatalf("batches = %d (first holds %d items, chunk %d), want one batch of 40 with chunk 0", len(bs), len(bs[0].Items), bs[0].Chunk)
	}
}

// AC3: tasks are inventoried like other items — a seeded copy of an embedded
// task and a repo-authored one — and judged in their own area.
func TestInventoryIncludesTasks(t *testing.T) {
	var seeded config.EmbeddedDefault
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "tasks" {
			seeded = d
			break
		}
	}
	if seeded.Name == "" {
		t.Fatal("the binary ships no embedded task")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, seeded.Name+".md"), []byte(seeded.Body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsk_repo-own.md"), []byte("---\ntype: task\nname: tsk_repo-own\n---\nrepo task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, err := substrate.ListTasks(dir)
	if err != nil {
		t.Fatal(err)
	}
	items := Inventory(InventoryInput{Tasks: tasks})
	origins := map[string]string{}
	for _, it := range items {
		if it.Kind == KindTask {
			origins[it.Key] = it.Origin
		}
	}
	for key, want := range map[string]string{"tasks/" + seeded.Name: OriginSeeded, "tasks/tsk_repo-own": OriginRepo} {
		if origins[key] != want {
			t.Errorf("origin of %s = %q, want %q (all: %v)", key, origins[key], want, origins)
		}
	}
	var in JudgeInput
	for _, it := range items {
		in.Items = append(in.Items, JudgeItem{Item: it})
	}
	bs := Batches(in)
	if len(bs) != 1 || bs[0].Area != AreaTasks || len(bs[0].Items) != len(items) {
		t.Errorf("tasks batch = %+v", bs)
	}
}
