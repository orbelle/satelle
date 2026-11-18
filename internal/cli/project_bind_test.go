package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

func storyListIDs(t *testing.T) []string {
	t.Helper()
	var items []item.Item
	if err := json.Unmarshal([]byte(must(t, "story", "list", "--json")), &items); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

func serverIDs(srv *fakeserver.Server, slug string) []string {
	var ids []string
	for _, it := range srv.Items(slug) {
		ids = append(ids, it.GetId())
	}
	return ids
}

func sharedWithStories(t *testing.T, srv *fakeserver.Server, titles ...string) []string {
	t.Helper()
	a := newMachine(t)
	a.use(t)
	must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
	var ids []string
	for _, title := range titles {
		ids = append(ids, createID(t, "story", "create", "--title", title, "--criteria", "c"))
	}
	autosync(t, a)
	return ids
}

func forkOn(t *testing.T, id string) {
	t.Helper()
	k, _, err := openKernel()
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	kind, title := "story", "forked here"
	wf, start, err := k.WorkflowFor(item.Item{Kind: kind, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Ledger.Append(id, item.KindCreated, "ada", item.Fields{Kind: &kind, Workflow: &wf, Status: &start, Title: &title}); err != nil {
		t.Fatal(err)
	}
	if err := k.Cache.Sync(k.Ledger); err != nil {
		t.Fatal(err)
	}
}

func assertBound(t *testing.T, slug string) {
	t.Helper()
	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	b, ok, err := loadBinding(r)
	if err != nil || !ok || b.Slug != slug {
		t.Fatalf("binding = %+v, %v, %v; want bound to %s", b, ok, err, slug)
	}
	if out := must(t, "project", "list"); !strings.Contains(out, slug) {
		t.Fatalf("project list does not show the repo bound to %s:\n%s", slug, out)
	}
}

func TestBindSyncsBeforeReturning(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	theirs := sharedWithStories(t, srv, "a1", "a2", "a3")

	b := newMachine(t)
	b.use(t)
	mine := []string{
		createID(t, "story", "create", "--title", "b1", "--criteria", "c"),
		createID(t, "story", "create", "--title", "b2", "--criteria", "c"),
	}
	out := must(t, "project", "bind", "shared", "--server", srv.URL)
	if !strings.Contains(out, "Bound this repo to shared") {
		t.Errorf("bind = %q, want the binding line", out)
	}
	if !regexp.MustCompile(`pushed \d+ event\(s\) on 2 item\(s\), pulled \d+ event\(s\) on 3 item\(s\)`).MatchString(out) {
		t.Errorf("bind = %q, want 2 items pushed and 3 pulled", out)
	}
	local, onServer := storyListIDs(t), serverIDs(srv, "shared")
	for _, id := range append(slices.Clone(theirs), mine...) {
		if !slices.Contains(local, id) {
			t.Errorf("story list lacks %s after bind", id)
		}
		if !slices.Contains(onServer, id) {
			t.Errorf("the project lacks %s after bind", id)
		}
	}
	for _, id := range mine {
		if len(srv.Events("shared", id)) == 0 {
			t.Errorf("the project holds no event of %s", id)
		}
	}
	status := must(t, "ledger", "status")
	for _, id := range append(slices.Clone(theirs), mine...) {
		if !regexp.MustCompile(regexp.QuoteMeta(id) + `\s+\d+\s+\d+\s+in sync`).MatchString(status) {
			t.Errorf("ledger status for %s is not in sync:\n%s", id, status)
		}
	}
	if st := syncStateOf(t); st.SyncPushed != 2 || st.SyncPulled != 3 || st.SyncReason != "" {
		t.Errorf("recorded pass = pushed %d, pulled %d, reason %q", st.SyncPushed, st.SyncPulled, st.SyncReason)
	}
}

func TestBindSyncFailureKeepsTheBinding(t *testing.T) {
	for _, tc := range []struct{ name, fail string }{
		{"store unreachable", "satelle.chain.v2.Chain/"},
		{"fails part way", "Chain/Append"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeserver.Start(t, testToken)
			theirs := sharedWithStories(t, srv, "theirs")

			b := newMachine(t)
			b.use(t)
			mine := createID(t, "story", "create", "--title", "mine", "--criteria", "c")
			srv.FailMethods(tc.fail)
			out, err := run(t, "", "project", "bind", "shared", "--server", srv.URL)
			if ExitCodeOf(err) != ExitError {
				t.Fatalf("bind exit = %d (%v), want 1\n%s", ExitCodeOf(err), err, out)
			}
			if msg := err.Error(); !strings.Contains(msg, "Unavailable") || !strings.Contains(msg, "satelle project bind shared") {
				t.Errorf("bind error = %q, want the failure and the command to sync again", msg)
			}
			assertBound(t, "shared")
			if !slices.Contains(storyListIDs(t), mine) {
				t.Errorf("the local story %s is gone", mine)
			}
			if n := len(srv.Events("shared", mine)); n != 0 {
				t.Errorf("the project holds %d event(s) of %s after a failed sync", n, mine)
			}
			if st := syncStateOf(t); st.SyncReason == "" {
				t.Error("the failed pass recorded no reason")
			}

			srv.FailMethods()
			again := must(t, "project", "bind", "shared", "--server", srv.URL)
			if !regexp.MustCompile(`pushed \d+ event\(s\) on 1 item\(s\), pulled \d+ event\(s\) on 1 item\(s\)`).MatchString(again) {
				t.Errorf("bind again = %q, want the counts", again)
			}
			if len(srv.Events("shared", mine)) == 0 {
				t.Errorf("the project lacks %s after binding again", mine)
			}
			if !slices.Contains(storyListIDs(t), theirs[0]) {
				t.Errorf("story list lacks the project's %s after binding again", theirs[0])
			}
		})
	}
}

func TestBindLeavesADivergedItem(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	ids := sharedWithStories(t, srv, "x", "z")
	x, z := ids[0], ids[1]
	serverHashes := func() []string {
		var hs []string
		for _, e := range srv.Events("shared", x) {
			hs = append(hs, e.GetHash())
		}
		return hs
	}
	storeBefore := serverHashes()

	b := newMachine(t)
	b.use(t)
	forkOn(t, x)
	y := createID(t, "story", "create", "--title", "y", "--criteria", "c")
	path := runtimeLedger(t).Path(x)
	fileBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "", "project", "bind", "shared", "--server", srv.URL)
	if ExitCodeOf(err) != ExitError {
		t.Fatalf("bind exit = %d (%v), want 1\n%s", ExitCodeOf(err), err, out)
	}
	if !strings.Contains(err.Error(), x+" (diverged at 0)") {
		t.Errorf("bind error = %q, want %s named as diverged", err, x)
	}
	if !regexp.MustCompile(`pushed \d+ event\(s\) on 1 item\(s\), pulled \d+ event\(s\) on 1 item\(s\)`).MatchString(out) {
		t.Errorf("bind = %q, want 1 item pushed and 1 pulled", out)
	}
	if fileAfter, err := os.ReadFile(path); err != nil || !bytes.Equal(fileAfter, fileBefore) {
		t.Errorf("B's ledger for %s changed (%v)", x, err)
	}
	if got := serverHashes(); !slices.Equal(got, storeBefore) {
		t.Errorf("the project's chain for %s changed: %v, was %v", x, got, storeBefore)
	}
	if len(srv.Events("shared", y)) == 0 {
		t.Errorf("the project lacks %s", y)
	}
	if !slices.Contains(storyListIDs(t), z) {
		t.Errorf("story list lacks the project's %s", z)
	}
	assertBound(t, "shared")
}

func TestBindWithLocalStoreSyncsNothing(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	theirs := sharedWithStories(t, srv, "theirs")

	b := newMachine(t)
	b.use(t)
	appendLedger(t, b.dir, `store = "local"`)
	mine := createID(t, "story", "create", "--title", "mine", "--criteria", "c")
	before := len(srv.Calls())
	out := must(t, "project", "bind", "shared", "--server", srv.URL)
	for _, want := range []string{"Bound this repo to shared", "Nothing was synced", "ledger store is local"} {
		if !strings.Contains(out, want) {
			t.Errorf("bind = %q, want %q", out, want)
		}
	}
	if calls := calledAny(srv, before, storeServices...); len(calls) > 0 {
		t.Errorf("bind with a local store called the store: %v", calls)
	}
	assertBound(t, "shared")
	if slices.Contains(storyListIDs(t), theirs[0]) {
		t.Errorf("a local store pulled %s", theirs[0])
	}
	if slices.Contains(serverIDs(srv, "shared"), mine) {
		t.Errorf("a local store pushed %s", mine)
	}
}

func TestCreateBindSyncsTheSameWay(t *testing.T) {
	t.Run("syncs", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		m := newMachine(t)
		m.use(t)
		mine := []string{
			createID(t, "story", "create", "--title", "one", "--criteria", "c"),
			createID(t, "story", "create", "--title", "two", "--criteria", "c"),
		}
		out := must(t, "project", "create", "fresh", "--bind", "--server", srv.URL)
		for _, want := range []string{"Created fresh", "Bound this repo to fresh"} {
			if !strings.Contains(out, want) {
				t.Errorf("create --bind = %q, want %q", out, want)
			}
		}
		if !regexp.MustCompile(`pushed \d+ event\(s\) on 2 item\(s\), pulled 0 event\(s\) on 0 item\(s\)`).MatchString(out) {
			t.Errorf("create --bind = %q, want 2 items pushed and none pulled", out)
		}
		onServer := serverIDs(srv, "fresh")
		for _, id := range mine {
			if !slices.Contains(onServer, id) {
				t.Errorf("the project lacks %s", id)
			}
		}
	})
	t.Run("fails", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		m := newMachine(t)
		m.use(t)
		createID(t, "story", "create", "--title", "one", "--criteria", "c")
		srv.FailMethods("satelle.chain.v2.Chain/")
		out, err := run(t, "", "project", "create", "other", "--bind", "--server", srv.URL)
		if ExitCodeOf(err) != ExitError {
			t.Fatalf("create --bind exit = %d (%v), want 1\n%s", ExitCodeOf(err), err, out)
		}
		if msg := err.Error(); !strings.Contains(msg, "sync failed") || !strings.Contains(msg, "satelle project bind other") {
			t.Errorf("create --bind error = %q", msg)
		}
		assertBound(t, "other")
	})
}
