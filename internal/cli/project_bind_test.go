package cli

import (
	"encoding/json"
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

func noSyncOutput(t *testing.T, what, out string) {
	t.Helper()
	for _, bad := range []string{"pushed", "pulled", "synced", "sync failed", "sync again"} {
		if strings.Contains(out, bad) {
			t.Errorf("%s = %q, carries %q: bind makes no sync", what, out, bad)
		}
	}
}

func TestBindRecordsTheBindingWithoutSyncing(t *testing.T) {
	t.Run("satelle.dev store", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		theirs := sharedWithStories(t, srv, "a1", "a2", "a3")

		b := newMachine(t)
		b.use(t)
		mine := []string{
			createID(t, "story", "create", "--title", "b1", "--criteria", "c"),
			createID(t, "story", "create", "--title", "b2", "--criteria", "c"),
		}
		before := len(srv.Calls())
		out := must(t, "project", "bind", "shared", "--server", srv.URL)
		if !strings.Contains(out, "Bound this repo to shared") {
			t.Errorf("bind = %q, want the binding line", out)
		}
		noSyncOutput(t, "bind", out)
		if calls := calledAny(srv, before, storeServices...); len(calls) > 0 {
			t.Errorf("bind called the ledger store: %v", calls)
		}
		assertBound(t, "shared")
		local, onServer := storyListIDs(t), serverIDs(srv, "shared")
		for _, id := range theirs {
			if slices.Contains(local, id) {
				t.Errorf("bind pulled %s", id)
			}
		}
		for _, id := range mine {
			if slices.Contains(onServer, id) {
				t.Errorf("bind pushed %s", id)
			}
		}
		if st := syncStateOf(t); !st.SyncLastAttempt.IsZero() {
			t.Errorf("bind recorded a sync pass at %s", st.SyncLastAttempt)
		}

		synced := autosync(t, b)
		if !regexp.MustCompile(`pushed \d+ event\(s\) on 2 item\(s\), pulled \d+ event\(s\) on 3 item\(s\)`).MatchString(synced) {
			t.Errorf("the timer's pass = %q, want 2 items pushed and 3 pulled", synced)
		}
		status := must(t, "ledger", "status")
		for _, id := range append(slices.Clone(theirs), mine...) {
			if !regexp.MustCompile(regexp.QuoteMeta(id) + `\s+\d+\s+\d+\s+in sync`).MatchString(status) {
				t.Errorf("ledger status for %s is not in sync after the timer's pass:\n%s", id, status)
			}
		}
	})
	t.Run("local store", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		sharedWithStories(t, srv, "theirs")

		b := newMachine(t)
		b.use(t)
		appendLedger(t, b.dir, `store = "local"`)
		before := len(srv.Calls())
		out := must(t, "project", "bind", "shared", "--server", srv.URL)
		if !strings.Contains(out, "Bound this repo to shared") || strings.Contains(out, "Nothing was synced") {
			t.Errorf("bind = %q, want only the binding", out)
		}
		if calls := calledAny(srv, before, storeServices...); len(calls) > 0 {
			t.Errorf("bind with a local store called the store: %v", calls)
		}
		assertBound(t, "shared")
	})
}

func TestCreateBindRecordsTheBindingWithoutSyncing(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	m.use(t)
	mine := []string{
		createID(t, "story", "create", "--title", "one", "--criteria", "c"),
		createID(t, "story", "create", "--title", "two", "--criteria", "c"),
	}
	before := len(srv.Calls())
	out := must(t, "project", "create", "fresh", "--bind", "--server", srv.URL)
	for _, want := range []string{"Created fresh", "Bound this repo to fresh"} {
		if !strings.Contains(out, want) {
			t.Errorf("create --bind = %q, want %q", out, want)
		}
	}
	noSyncOutput(t, "create --bind", out)
	if calls := calledAny(srv, before, storeServices...); len(calls) > 0 {
		t.Errorf("create --bind called the ledger store: %v", calls)
	}
	onServer := serverIDs(srv, "fresh")
	for _, id := range mine {
		if slices.Contains(onServer, id) {
			t.Errorf("create --bind pushed %s", id)
		}
	}
	assertBound(t, "fresh")
}

func TestBindWithTheStoreUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name, slug string
		args       []string
		seed       bool
	}{
		{"bind", "shared", []string{"project", "bind", "shared"}, true},
		{"create --bind", "other", []string{"project", "create", "other", "--bind"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeserver.Start(t, testToken)
			if tc.seed {
				sharedWithStories(t, srv, "theirs")
			}
			m := newMachine(t)
			m.use(t)
			mine := createID(t, "story", "create", "--title", "mine", "--criteria", "c")
			srv.FailMethods(storeServices...)
			out, err := run(t, "", append(tc.args, "--server", srv.URL)...)
			if err != nil {
				t.Fatalf("%s with the store unreachable = %v, want exit 0\n%s", tc.name, err, out)
			}
			noSyncOutput(t, tc.name, out)
			if strings.Contains(out, "Unavailable") {
				t.Errorf("%s = %q, names a store failure", tc.name, out)
			}
			assertBound(t, tc.slug)
			if !slices.Contains(storyListIDs(t), mine) {
				t.Errorf("the local story %s is gone", mine)
			}
			if st := syncStateOf(t); st.SyncReason != "" {
				t.Errorf("bind recorded a sync failure: %q", st.SyncReason)
			}
		})
	}
}

func TestBindHelpLeavesSyncingToTheTimer(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{{"project", "bind", "--help"}, {"help", "projects"}} {
		out := must(t, args...)
		for _, want := range []string{"records the binding", "satelle-sync.timer"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v lacks %q:\n%s", args, want, out)
			}
		}
		for _, bad := range []string{"Before returning", "before it returns", "how many items", "are pushed", "are pulled", "sync fails", "again to sync", "Nothing was synced"} {
			if strings.Contains(out, bad) {
				t.Errorf("%v still says %q:\n%s", args, bad, out)
			}
		}
	}
	if out := must(t, "project", "create", "--help"); strings.Contains(out, "and sync it") {
		t.Errorf("project create --help still says --bind syncs:\n%s", out)
	}
	if out := must(t, "help", "ledger-store"); strings.Contains(out, "does not wait for the") {
		t.Errorf("help ledger-store still says bind runs the pass:\n%s", out)
	}
}
