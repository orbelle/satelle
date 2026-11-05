//go:build !windows

package kernel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
)

// folderOf is the record folder a prompt names, or "".
func folderOf(prompt string) string {
	i := strings.Index(prompt, RecordLead)
	if i < 0 {
		return ""
	}
	rest := prompt[i+len(RecordLead):]
	return rest[:strings.Index(rest, ":\n")]
}

// recordSeen is what one dispatch found in its record folder.
type recordSeen struct {
	dir   string
	files map[string]string
	modes map[string]os.FileMode
}

// watch makes d read each dispatch's record folder while the run is live.
func watch(d *fakeDispatcher) *[]recordSeen {
	seen := &[]recordSeen{}
	d.onDispatch = func(_ agent.Role, req agent.Request) {
		dir := folderOf(req.Prompt)
		s := recordSeen{dir: dir, files: map[string]string{}, modes: map[string]os.FileMode{}}
		if dir != "" {
			_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				rel, _ := filepath.Rel(dir, p)
				s.modes[rel] = info.Mode().Perm()
				if !info.IsDir() {
					b, _ := os.ReadFile(p)
					s.files[rel] = string(b)
				}
				return nil
			})
		}
		*seen = append(*seen, s)
	}
	return seen
}

func mustAttach(t *testing.T, k *Kernel, id, name, body string) {
	t.Helper()
	if _, err := k.Attach(id, name, []byte(body), "text/markdown", "me"); err != nil {
		t.Fatal(err)
	}
}

// toWork creates an item in multiState and moves it into work.
func toWork(t *testing.T, k *Kernel, repo string) item.Item {
	t.Helper()
	it, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t"), Body: ptr("b")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(repo, "marker"), nil, 0o644)
	if res, err := k.Transition(context.Background(), it.ID, "work", "me"); err != nil || !res.Moved {
		t.Fatalf("open->work: %v %s", err, gates(res))
	}
	it, _ = k.Get(it.ID)
	return it
}

func runsEmpty(t *testing.T, k *Kernel) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(k.Root, runsDir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("record folders left behind: %v", entries)
	}
}

// The record folder holds the newest version of each attachment and the
// children, read-only, named in the prompt, and is removed after the run;
// performers and every judge get it.
func TestRecordFolderBeforeEveryLocalRun(t *testing.T) {
	k, d, repo := multiState(t)
	seen := watch(d)
	it := toWork(t, k, repo)
	mustAttach(t, k, it.ID, "plan", "plan v1\n")
	mustAttach(t, k, it.ID, "plan", "plan v2\n")
	mustAttach(t, k, it.ID, "notes/md", "the notes\n")
	for _, title := range []string{"first child", "second child"} {
		if _, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr(title), Parent: &it.ID}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	*seen = nil
	if _, err := k.Perform(context.Background(), it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	check := func(s recordSeen, prompt string) {
		t.Helper()
		if s.dir == "" || !strings.HasPrefix(s.dir, filepath.Join(k.Root, runsDir, it.ID+"-")) || !strings.HasPrefix(s.dir, k.Dir) {
			t.Fatalf("record folder %q is not under %s", s.dir, filepath.Join(k.Root, runsDir))
		}
		if s.files["attachments/plan"] != "plan v2\n" || s.files["attachments/notes_md"] != "the notes\n" {
			t.Errorf("attachments in the folder: %v", s.files)
		}
		ch := s.files["children.md"]
		if !strings.Contains(ch, "[open] first child") || !strings.Contains(ch, "[open] second child") {
			t.Errorf("children.md = %q", ch)
		}
		for rel, mode := range s.modes {
			want := os.FileMode(0o444)
			if _, isFile := s.files[rel]; !isFile {
				want = 0o555
			}
			if rel != "." && mode != want {
				t.Errorf("%s mode %o, want %o", rel, mode, want)
			}
			if _, isFile := s.files[rel]; isFile && !strings.Contains(prompt, filepath.Join(s.dir, rel)) {
				t.Errorf("the prompt does not name %s", rel)
			}
		}
	}
	if len(*seen) != 1 {
		t.Fatalf("dispatches %d", len(*seen))
	}
	check((*seen)[0], d.reqs[len(d.reqs)-1].Prompt)
	runsEmpty(t, k)

	// The judges of work -> closed (review, then the panel and strict) read
	// the same record; the panel's members share one folder.
	*seen = nil
	d.reqs = nil
	if _, err := k.Transition(context.Background(), it.ID, "closed", "me"); err != nil {
		t.Fatal(err)
	}
	if len(*seen) < 4 {
		t.Fatalf("judges dispatched %d", len(*seen))
	}
	for i, s := range *seen {
		check(s, d.reqs[i].Prompt)
		if s.dir != (*seen)[0].dir {
			t.Errorf("judges of one presentation saw different folders: %s and %s", s.dir, (*seen)[0].dir)
		}
	}
	runsEmpty(t, k)
}

// No attachments and no children: no record, no folder.
func TestNoRecordWithoutAttachmentsOrChildren(t *testing.T) {
	k, d, repo := multiState(t)
	it := toWork(t, k, repo)
	if _, err := k.Perform(context.Background(), it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	if p := d.reqs[len(d.reqs)-1].Prompt; strings.Contains(p, RecordLead) {
		t.Fatalf("a record without attachments or children: %q", p)
	}
	if _, err := os.Stat(filepath.Join(k.Root, runsDir)); !os.IsNotExist(err) {
		t.Fatal("runs/ was created with nothing to record")
	}
}

// A transition no agent judges writes no folder.
func TestNoRecordForATransitionNoAgentJudges(t *testing.T) {
	k, _, _ := multiState(t)
	it, err := k.Create(item.Fields{Kind: ptr("chore"), Title: ptr("c")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	mustAttach(t, k, it.ID, "plan", "p\n")
	if _, err := k.Create(item.Fields{Kind: ptr("chore"), Title: ptr("kid"), Parent: &it.ID}, "me"); err != nil {
		t.Fatal(err)
	}
	if res, err := k.Transition(context.Background(), it.ID, "z", "me"); err != nil || !res.Moved {
		t.Fatalf("a -> z: %v", err)
	}
	if _, err := os.Stat(filepath.Join(k.Root, runsDir)); !os.IsNotExist(err) {
		t.Fatal("a gateless transition wrote a record folder")
	}
}

// Bodies are inlined newest first, 16 KiB each and 64 KiB in all, every cut
// marked with the bytes shown and the file to read.
func TestRecordInlineBounds(t *testing.T) {
	k, d, repo := multiState(t)
	it := toWork(t, k, repo)
	body := func(name string) string {
		return strings.Repeat(name+" filler line.\n", 20480/len(name+" filler line.\n"))[:20480-len(name)-6] + "\nlast " + name
	}
	for _, n := range []string{"e1", "e2", "e3", "e4", "e5"} {
		mustAttach(t, k, it.ID, n, body(n))
	}
	small := strings.Repeat("s", 1023) + "\n"
	mustAttach(t, k, it.ID, "small", small)
	if _, err := k.Perform(context.Background(), it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	p := d.reqs[len(d.reqs)-1].Prompt
	dir := folderOf(p)
	path := func(n string) string { return filepath.Join(dir, "attachments", n) }
	for _, want := range []string{
		small,
		"[shown 16384 of 20480 bytes; full body: " + path("e5") + "]",
		"[shown 16384 of 20480 bytes; full body: " + path("e4") + "]",
		"[shown 16384 of 20480 bytes; full body: " + path("e3") + "]",
		"[shown 15360 of 20480 bytes: the 65536-byte inline budget is spent; full body: " + path("e2") + "]",
		"[shown 0 of 20480 bytes: the 65536-byte inline budget is spent; full body: " + path("e1") + "]",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %.80q", want)
		}
	}
	if strings.Contains(p, "e1 filler") {
		t.Error("e1 was inlined past the budget")
	}
	if strings.Index(p, "Attachment small") > strings.Index(p, "Attachment e5") {
		t.Error("attachments are not newest first")
	}
	inlined := len(small) + 3*16384 + 15360
	if inlined != InlineTotalMax {
		t.Fatalf("fixture arithmetic: %d", inlined)
	}
}

// A body that is not UTF-8 is never inlined; a cut never splits a rune;
// children beyond 200 are marked; the judge sees the performer's section.
func TestRecordMarkers(t *testing.T) {
	k, d, repo := multiState(t)
	it := toWork(t, k, repo)
	mustAttach(t, k, it.ID, "bin", "\xff\xfebinary")
	rune3 := strings.Repeat("a", InlineBodyMax-1) + "€tail" // € is 3 bytes straddling 16384
	mustAttach(t, k, it.ID, "rune", rune3)
	for i := 0; i < InlineChildrenMax+1; i++ {
		if _, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr(fmt.Sprintf("child %03d", i)), Parent: &it.ID}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	seen := watch(d)
	if _, err := k.Perform(context.Background(), it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	p := d.reqs[len(d.reqs)-1].Prompt
	dir := folderOf(p)
	for _, want := range []string{
		fmt.Sprintf("[shown 0 of %d bytes: not UTF-8 text; full body: %s]", len("\xff\xfebinary"), filepath.Join(dir, "attachments", "bin")),
		fmt.Sprintf("[shown %d of %d bytes; full body: %s]", InlineBodyMax-1, len(rune3), filepath.Join(dir, "attachments", "rune")),
		fmt.Sprintf("[… 1 more children; all are listed in %s]", filepath.Join(dir, "children.md")),
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	if n := strings.Count((*seen)[0].files["children.md"], "\n"); n != InlineChildrenMax+1 {
		t.Errorf("children.md lists %d children", n)
	}
	section := p[strings.Index(p, "\n"+RecordLead):]
	d.reqs = nil
	if _, err := k.Transition(context.Background(), it.ID, "closed", "me"); err != nil {
		t.Fatal(err)
	}
	// The judge's section is the same, bar its own folder name.
	jp := d.reqs[0].Prompt
	jdir := folderOf(jp)
	if !strings.Contains(jp, strings.ReplaceAll(section, dir, jdir)) {
		t.Error("the judge's record section differs from the performer's")
	}
}

// The performer sees the gates that rejected the newest attempt to leave its
// state, whatever was appended since, and nothing of earlier attempts.
func TestPerformListsTheLatestRejectedAttempt(t *testing.T) {
	k, d, repo := multiState(t)
	it := toWork(t, k, repo)
	d.set("strict", `{"verdict":"reject","notes":"first-notes"}`)
	if res, _ := k.Transition(context.Background(), it.ID, "closed", "me"); res.Moved {
		t.Fatal("attempt 1 moved")
	}
	mustAttach(t, k, it.ID, "notes", "n\n")
	if _, err := k.Update(it.ID, item.Fields{Title: ptr("t2")}, "me"); err != nil {
		t.Fatal(err)
	}
	long := "second-notes " + strings.Repeat("x", 9000-len("second-notes "))
	d.set("strict", `{"verdict":"reject","notes":"`+long+`"}`)
	d.set("r3", reject)
	if res, _ := k.Transition(context.Background(), it.ID, "closed", "me"); res.Moved {
		t.Fatal("attempt 2 moved")
	}
	mustAttach(t, k, it.ID, "notes", "n2\n")
	if _, err := k.Update(it.ID, item.Fields{Title: ptr("t3")}, "me"); err != nil {
		t.Fatal(err)
	}
	runs, _ := k.Cache.Events(it.ID, gate.KindRun)
	attempts := map[string]bool{}
	for _, e := range runs {
		var r gate.Result
		if e.Decode(&r) != nil || r.Attempt == "" {
			t.Fatalf("a gate.run without an attempt: %s", e.Payload)
		}
		if strings.HasPrefix(r.Transition, "work->") {
			attempts[r.Attempt] = true
		}
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts out of work: %v", attempts)
	}
	if _, err := k.Perform(context.Background(), it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	p := d.reqs[len(d.reqs)-1].Prompt
	head := "The latest attempt to leave \"work\" was rejected"
	panel := "- gate panel (r1,r2,r3) on work->closed:"
	strict := "- gate strict (strict) on work->closed:"
	if !strings.Contains(p, head) || !strings.Contains(p, panel) || !strings.Contains(p, strict) || strings.Index(p, panel) > strings.Index(p, strict) {
		t.Fatalf("rejection section:\n%s", p)
	}
	if !strings.Contains(p, "[shown 8192 of 9000 bytes of notes]") || strings.Contains(p, "first-notes") || strings.Contains(p, "- gate review") {
		t.Fatalf("rejection section content:\n%s", p)
	}
}

// With nothing to add, the performer prompt is exactly the item.
func TestPerformPromptUnchangedWithNothingToAdd(t *testing.T) {
	k, d, repo := multiState(t)
	it := toWork(t, k, repo)
	if _, err := k.Perform(context.Background(), it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	if p := d.reqs[len(d.reqs)-1].Prompt; p != renderItem(it) {
		t.Fatalf("prompt = %q, want renderItem", p)
	}
	// A rejection, then leaving and re-entering work: the new stay has no
	// rejection, so the prompt is the item again.
	if res, _ := k.Transition(context.Background(), it.ID, "closed", "me"); res.Moved {
		t.Fatal("moved")
	}
	for _, to := range []string{"hold", "work"} {
		if res, err := k.Transition(context.Background(), it.ID, to, "me"); err != nil || !res.Moved {
			t.Fatalf("-> %s: %v", to, err)
		}
	}
	it, _ = k.Get(it.ID)
	if _, err := k.Perform(context.Background(), it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	if p := d.reqs[len(d.reqs)-1].Prompt; p != renderItem(it) {
		t.Fatalf("after re-entering work, prompt = %q", p)
	}
	// With a rejection and an attachment, the prompt still starts with it.
	if res, _ := k.Transition(context.Background(), it.ID, "closed", "me"); res.Moved {
		t.Fatal("moved")
	}
	mustAttach(t, k, it.ID, "plan", "p\n")
	if _, err := k.Perform(context.Background(), it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	if p := d.reqs[len(d.reqs)-1].Prompt; !strings.HasPrefix(p, renderItem(it)) || p == renderItem(it) {
		t.Fatalf("prompt does not extend renderItem: %q", p)
	}
}
