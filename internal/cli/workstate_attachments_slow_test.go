//go:build clislow

package cli

import (
	"strings"
	"testing"
)

// TestSlowAttachmentPushHoldsAndScope (AC5): a story held for unpushed code keeps its
// attachments back with it, a scoped push sends only the scoped story's, and the
// leg still runs when every row is held.
func TestSlowAttachmentPushHoldsAndScope(t *testing.T) {
	t.Run("held stories keep their attachments back", func(t *testing.T) {
		docs := newFakeDocs()
		w := newHoldWorldWith(t, storiesOnlyToml, docs.routes)
		for _, id := range []string{w.dirty, w.unpushed, w.ok, w.plan} {
			attachDoc(t, id, "plan", "plan", "plan of "+id)
		}
		out := w.push(t)
		if !strings.Contains(out, "held with their stories") {
			t.Errorf("push should say attachments were held: %q", out)
		}
		for id, want := range map[string]bool{w.dirty: false, w.unpushed: false, w.ok: true, w.plan: true} {
			if _, got := docs.file("stories/" + id + "/plan.md"); got != want {
				t.Errorf("story %s attachment hosted = %v, want %v", id, got, want)
			}
		}
	})

	t.Run("a scoped push sends only its story", func(t *testing.T) {
		docs := newFakeDocs()
		w := newHoldWorldWith(t, storiesOnlyToml, docs.routes)
		attachDoc(t, w.ok, "plan", "plan", "ok plan")
		attachDoc(t, w.plan, "plan", "plan", "plan plan")
		w.push(t, w.ok)
		if _, ok := docs.file("stories/" + w.ok + "/plan.md"); !ok {
			t.Error("the scoped story's attachment should be uploaded")
		}
		if _, ok := docs.file("stories/" + w.plan + "/plan.md"); ok {
			t.Error("a story outside the scope must not be uploaded")
		}
	})

	t.Run("every row held still uploads the rest", func(t *testing.T) {
		docs := newFakeDocs()
		w := newHoldWorldWith(t, storiesOnlyToml, docs.routes)
		touch := func(priority string, ids ...string) {
			for _, id := range ids {
				if out, err := runRoot(t, "story", "set", id, "--priority", priority); err != nil {
					t.Fatalf("touch %s: %v\n%s", id, err, out)
				}
			}
		}
		// The held stories change after the others, so the first push can move the
		// items cursor past ok and plan and the second sees only held rows.
		touch("low", w.dirty, w.unpushed)
		w.push(t)
		attachDoc(t, w.ok, "late", "note", "attached after the cursor moved")
		touch("medium", w.dirty)
		out := w.push(t)
		if !strings.Contains(out, "after holding unpushed stories") {
			t.Fatalf("expected the everything-held exit: %q", out)
		}
		if _, ok := docs.file("stories/" + w.ok + "/late.md"); !ok {
			t.Error("the attachment leg should still run when every row is held")
		}
	})
}

// TestSlowAttachmentTwoMachinePullAndSnapshot (AC2): what machine A attached is read
// on machine B after the normal sync, through pull and through snapshot, and B
// does not upload back what it just received.
func TestSlowAttachmentTwoMachinePullAndSnapshot(t *testing.T) {
	for _, verbName := range []string{"pull", "snapshot"} {
		t.Run(verbName, func(t *testing.T) {
			url, _, docs := attachmentServer(t)
			a := newCLIMachine(t, storiesOnlyToml)
			b := newCLIMachine(t, storiesOnlyToml)

			a.use(t)
			id := createStoryID(t, "two machines")
			attachDoc(t, id, "change-in_progress-done", "change", "diff --git a/f b/f\n+changed\n")
			attachDoc(t, id, "plan", "plan", "plan body")
			wantChange := mustDocBody(t, id, "change-in_progress-done")
			wantPlan := mustDocBody(t, id, "plan")
			runPush(t, url)

			b.use(t)
			if _, err := storyDocBody(t, id, "plan"); err == nil {
				t.Fatal("machine B should not have the attachment before syncing")
			}
			out := syncWorkstateCLI(t, url, verbName)
			if !strings.Contains(out, "attachments: 2 written") {
				t.Errorf("%s should report the attachments written: %q", verbName, out)
			}
			if got := mustDocBody(t, id, "change-in_progress-done"); got != wantChange {
				t.Errorf("change patch on B = %q, want %q", got, wantChange)
			}
			if got := mustDocBody(t, id, "plan"); got != wantPlan {
				t.Errorf("plan on B = %q, want %q", got, wantPlan)
			}

			before := docs.putCount()
			runPush(t, url)
			if docs.putCount() != before {
				t.Errorf("B uploaded %d file(s) it had just pulled", docs.putCount()-before)
			}

			// A attaches later, with no new rows: B still receives it.
			a.use(t)
			attachDoc(t, id, "late", "note", "attached later")
			runPush(t, url)
			b.use(t)
			syncWorkstateCLI(t, url, verbName)
			if got := mustDocBody(t, id, "late"); !strings.Contains(got, "attached later") {
				t.Errorf("late attachment on B = %q", got)
			}
		})
	}
}

// TestSlowAttachmentPushUploadsByteExact (AC1): every markdown attachment, the
// type:change patch included, reaches the personal documents collection, with the
// ledger area opted in and with it local. Unchanged files are not re-sent; an
// edited one is sent alone.
func TestSlowAttachmentPushUploadsByteExact(t *testing.T) {
	for _, tc := range []struct{ name, toml string }{
		{"ledger personal", ledgerPushToml},
		{"ledger local", storiesOnlyToml},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, _, docs := attachmentServer(t)
			workstateRepo(t, tc.toml)
			id := createStoryID(t, "attachment push")
			attachDoc(t, id, "change-in_progress-done", "change", "diff --git a/x.go b/x.go\n+hello\n")
			attachDoc(t, id, "plan", "plan", "the plan")

			out := runPush(t, url)
			if !strings.Contains(out, "attachments: 2 uploaded") {
				t.Errorf("push should report the uploads: %q", out)
			}
			for _, name := range []string{"change-in_progress-done", "plan"} {
				got, ok := docs.file("stories/" + id + "/" + name + ".md")
				if !ok {
					t.Fatalf("%s not uploaded", name)
				}
				if want := mustDocBody(t, id, name); string(got) != want {
					t.Errorf("%s uploaded bytes differ:\n got %q\nwant %q", name, got, want)
				}
			}

			if docs.putCount() != 2 {
				t.Fatalf("PUTs = %d, want 2", docs.putCount())
			}
			runPush(t, url)
			if docs.putCount() != 2 {
				t.Errorf("unchanged re-push PUTs = %d, want no more", docs.putCount())
			}
			attachDoc(t, id, "plan", "plan", "the plan, revised")
			runPush(t, url)
			if docs.putCount() != 3 {
				t.Errorf("edited attachment PUTs = %d, want exactly one more", docs.putCount())
			}
			got, _ := docs.file("stories/" + id + "/plan.md")
			if want := mustDocBody(t, id, "plan"); string(got) != want {
				t.Errorf("revised plan bytes differ: got %q want %q", got, want)
			}
		})
	}
}

// TestSlowAttachmentScopedPullLeavesTheCursor (AC2): a pull scoped to one story takes
// only that story's attachments and does not move the cursor past the others.
func TestSlowAttachmentScopedPullLeavesTheCursor(t *testing.T) {
	url, _, _ := attachmentServer(t)
	a := newCLIMachine(t, storiesOnlyToml)
	b := newCLIMachine(t, storiesOnlyToml)

	a.use(t)
	x := createStoryID(t, "scope x")
	y := createStoryID(t, "scope y")
	runPush(t, url)
	b.use(t)
	syncWorkstateCLI(t, url, "pull")

	a.use(t)
	attachDoc(t, x, "plan", "plan", "x plan")
	attachDoc(t, y, "plan", "plan", "y plan")
	runPush(t, url)

	b.use(t)
	syncWorkstateCLI(t, url, "pull", x)
	if _, err := storyDocBody(t, x, "plan"); err != nil {
		t.Errorf("the scoped story's attachment should arrive: %v", err)
	}
	if _, err := storyDocBody(t, y, "plan"); err == nil {
		t.Error("a story outside the scope must not receive its attachment")
	}
	syncWorkstateCLI(t, url, "pull")
	if _, err := storyDocBody(t, y, "plan"); err != nil {
		t.Errorf("the scoped pull must not move the cursor past the other story: %v", err)
	}
}
