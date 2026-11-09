package cache

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func ptr[T any](v T) *T { return &v }

type snapshot struct {
	Items  []item.Item
	Events []ledger.Event
	Seats  []item.Seat
	Atts   []item.Attachment
}

func snap(t *testing.T, c *Cache) snapshot {
	t.Helper()
	items, err := c.Items(Query{})
	if err != nil {
		t.Fatal(err)
	}
	events, _ := c.Events("", "")
	seats, _ := c.Seats("", "")
	atts, _ := c.Attachments("a")
	return snapshot{items, events, seats, atts}
}

func TestSyncQueryRebuild(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.Open(dir)
	c, err := Open(filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	l.Append("a", item.KindCreated, "me", item.Fields{Kind: ptr("k1"), Title: ptr("A"), Status: ptr("s0"), Tags: ptr([]string{"x", "y"}), Category: ptr("cat"), Workflow: ptr("w1"), Assignees: ptr([]string{"ann", "bob"})})
	l.Append("b", item.KindCreated, "me", item.Fields{Kind: ptr("k2"), Title: ptr("B"), Status: ptr("s0"), Parent: ptr("a"), Workflow: ptr("w2")})
	if err := c.Sync(l); err != nil {
		t.Fatal(err)
	}
	l.Append("a", item.KindTransitioned, "me", item.Move{From: "s0", To: "s1"})
	l.Append("a", item.KindSeatBound, "me", item.Seat{Session: "sess", Location: "local:/x", Base: "h", Machine: "m1", Host: "h1", User: "ada@x.io"})
	l.Append("a", item.KindAttachmentAdd, "me", item.Attachment{Name: "n", SHA256: "ab", Size: 2, MediaType: "text/plain"})
	if err := c.Sync(l); err != nil {
		t.Fatal(err)
	}
	if err := c.Sync(l); err != nil { // idempotent
		t.Fatal(err)
	}

	check := func(q Query, want ...string) {
		t.Helper()
		got, err := c.Items(q)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, it := range got {
			ids = append(ids, it.ID)
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("%+v: got %v want %v", q, ids, want)
		}
	}
	check(Query{Status: "s1"}, "a")
	check(Query{Kind: "k2"}, "b")
	check(Query{Tag: "y"}, "a")
	check(Query{Category: "cat"}, "a")
	check(Query{Parent: "a"}, "b")
	check(Query{Workflow: "w2"}, "b")
	check(Query{Tag: "nope"})

	if ev, _ := c.Events("a", item.KindTransitioned); len(ev) != 1 {
		t.Fatalf("events by kind: %d", len(ev))
	}
	if s, _ := c.Seats("sess", ""); len(s) != 1 || s[0].Item != "a" || s[0].Machine != "m1" || s[0].Host != "h1" || s[0].User != "ada@x.io" {
		t.Fatalf("seats %+v", s)
	}
	if it, ok, _ := c.Item("a"); !ok || it.Status != "s1" || len(it.Tags) != 2 || !reflect.DeepEqual(it.Assignees, []string{"ann", "bob"}) {
		t.Fatalf("item %+v", it)
	}

	before := snap(t, c)
	if err := c.Rebuild(l); err != nil {
		t.Fatal(err)
	}
	after := snap(t, c)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rebuild differs:\n%+v\n%+v", before, after)
	}
	if len(after.Events) != 5 || len(after.Atts) != 1 {
		t.Fatalf("counts %d %d", len(after.Events), len(after.Atts))
	}
}

func eventIDs(events []ledger.Event) []string {
	var ids []string
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	return ids
}

func noDuplicateIDs(t *testing.T, c *Cache) {
	t.Helper()
	rows, err := c.db.Query(`SELECT id, COUNT(*) FROM events GROUP BY id HAVING COUNT(*) > 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			t.Fatal(err)
		}
		t.Errorf("event %s indexed %d times", id, n)
	}
}

// A writer that ingests with a stale offset (as a second process that read
// the offset before the first one committed would) indexes nothing twice.
func TestReingestIsNoOp(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.Open(dir)
	c, err := Open(filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	l.Append("a", item.KindCreated, "me", item.Fields{Kind: ptr("k"), Title: ptr("A"), Status: ptr("s0")})
	l.Append("a", item.KindTransitioned, "me", item.Move{From: "s0", To: "s1"})
	if err := c.Sync(l); err != nil {
		t.Fatal(err)
	}
	var stale int64
	if err := c.db.QueryRow(`SELECT offset FROM files WHERE item = 'a'`).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	l.Append("a", item.KindTransitioned, "me", item.Move{From: "s1", To: "s2"})
	if err := c.Sync(l); err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.Exec(`UPDATE files SET offset = ? WHERE item = 'a'`, stale); err != nil {
		t.Fatal(err)
	}
	if err := c.Sync(l); err != nil {
		t.Fatal(err)
	}
	got, err := c.Events("a", "")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := l.Read("a")
	if !reflect.DeepEqual(eventIDs(got), eventIDs(want)) || len(got) != 3 {
		t.Fatalf("events %v want %v", eventIDs(got), eventIDs(want))
	}
	noDuplicateIDs(t, c)
}

// The schema itself refuses a second row for an event id, not only the code.
func TestSchemaRejectsDuplicateEventID(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.Open(dir)
	c, err := Open(filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	e, _ := l.Append("a", item.KindCreated, "me", item.Fields{Kind: ptr("k"), Title: ptr("A")})
	if err := c.Sync(l); err != nil {
		t.Fatal(err)
	}
	_, err = c.db.Exec(`INSERT INTO events (id, item, kind, at, actor, payload, prev, hash) VALUES (?,?,?,?,?,?,?,?)`,
		e.ID, e.Item, e.Kind, ts(e.At), e.Actor, string(e.Payload), e.Prev, e.Hash)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("duplicate insert: %v", err)
	}
}

// v2Schema is the v2 cache DDL (no events_id unique index), stamped v2: the
// shape a pre-v3 binary writes.
const v2Schema = `
CREATE TABLE files (item TEXT PRIMARY KEY, offset INTEGER NOT NULL);
CREATE TABLE items (
	id TEXT PRIMARY KEY, kind TEXT, title TEXT, body TEXT, criteria TEXT, status TEXT,
	tags TEXT, parent TEXT, category TEXT, workflow TEXT, assignee TEXT, inputs TEXT, trigger TEXT, base TEXT,
	created_at TEXT, updated_at TEXT);
CREATE TABLE item_tags (item TEXT NOT NULL, tag TEXT NOT NULL);
CREATE TABLE events (
	seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT, item TEXT, kind TEXT, at TEXT,
	actor TEXT, payload TEXT, prev TEXT, hash TEXT);
CREATE INDEX events_item ON events(item, kind);
CREATE TABLE seats (
	item TEXT, session TEXT, location TEXT, base TEXT, bound_at TEXT, PRIMARY KEY (item, session));
CREATE TABLE attachments (
	item TEXT, name TEXT, sha256 TEXT, size INTEGER, media_type TEXT, at TEXT, actor TEXT);
PRAGMA user_version = 2;`

// A v2 cache that already holds an event twice is rebuilt clean on open.
func TestOldCacheIsRebuiltOnSchemaBump(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.Open(dir)
	e, _ := l.Append("a", item.KindCreated, "me", item.Fields{Kind: ptr("k"), Title: ptr("A")})
	size, _ := l.Size("a")

	path := filepath.Join(dir, "cache.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(v2Schema); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(`INSERT INTO events (id, item, kind, at, actor, payload, prev, hash) VALUES (?,?,?,?,?,?,?,?)`,
			e.ID, e.Item, e.Kind, ts(e.At), e.Actor, string(e.Payload), e.Prev, e.Hash); err != nil {
			t.Fatal(err)
		}
	}
	// The offset says the tail is fully ingested, so only the rebuild heals it.
	if _, err := db.Exec(`INSERT INTO files (item, offset) VALUES ('a', ?)`, size); err != nil {
		t.Fatal(err)
	}
	db.Close()

	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Sync(l); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Events("a", "")
	if len(got) != 1 || got[0].ID != e.ID {
		t.Fatalf("events after rebuild %v", eventIDs(got))
	}
	var version int
	if err := c.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("user_version %d (%v)", version, err)
	}
	noDuplicateIDs(t, c)
}

// Two caches on one cache.db (two processes, at the sqlite lock level) syncing
// the same new tail at once index each event once.
func TestConcurrentSyncIngestsEachEventOnce(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.Open(dir)
	path := filepath.Join(dir, "cache.db")
	c1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if err := c1.Sync(l); err != nil {
		t.Fatal(err)
	}
	if err := c2.Sync(l); err != nil {
		t.Fatal(err)
	}

	items := []string{"a", "b"}
	appended := 0
	for round := 0; round < 50; round++ {
		id := items[round%len(items)]
		if round < len(items) {
			l.Append(id, item.KindCreated, "me", item.Fields{Kind: ptr("k"), Title: ptr(id), Status: ptr("s0")})
		} else {
			l.Append(id, item.KindTransitioned, "me", item.Move{From: "s0", To: "s0"})
		}
		appended++
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, c := range []*Cache{c1, c2} {
			wg.Add(1)
			go func(i int, c *Cache) {
				defer wg.Done()
				<-start
				errs[i] = c.Sync(l)
			}(i, c)
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}

	noDuplicateIDs(t, c1)
	all, err := c1.Events("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != appended {
		t.Fatalf("cache holds %d events, ledger %d", len(all), appended)
	}
	for _, id := range items {
		got, _ := c1.Events(id, "")
		want, _ := l.Read(id)
		if !reflect.DeepEqual(eventIDs(got), eventIDs(want)) {
			t.Fatalf("%s: cache %v ledger %v", id, eventIDs(got), eventIDs(want))
		}
	}
}

// masterNames lists the tables and indexes in db, read from sqlite_master.
func masterNames(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type IN ('table', 'index')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names[n] = true
	}
	return names
}

// An older binary (simulated: the cache is stamped one version past this
// binary's schemaVersion) refuses the cache, naming both versions and the
// binary to update, and drops nothing.
func TestOlderBinaryRefusesNewerCache(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.Open(dir)
	l.Append("a", item.KindCreated, "me", item.Fields{Kind: ptr("k"), Title: ptr("A"), Status: ptr("s0")})
	l.Append("a", item.KindTransitioned, "me", item.Move{From: "s0", To: "s1"})
	path := filepath.Join(dir, "cache.db")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Sync(l); err != nil {
		t.Fatal(err)
	}
	c.Close()

	newer := schemaVersion + 1
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, newer)); err != nil {
		t.Fatal(err)
	}
	counts := func() (events, items int) {
		t.Helper()
		if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&items); err != nil {
			t.Fatal(err)
		}
		return
	}
	events0, items0 := counts()
	if events0 != 2 || items0 != 1 {
		t.Fatalf("setup counts events %d items %d", events0, items0)
	}

	c, err = Open(path)
	if err == nil {
		c.Close()
		t.Fatal("an older binary opened a newer cache")
	}
	var tooNew *SchemaTooNewError
	if !errors.As(err, &tooNew) {
		t.Fatalf("error %v is not a SchemaTooNewError", err)
	}
	if tooNew.Cache != newer || tooNew.Binary != schemaVersion {
		t.Fatalf("versions cache %d binary %d", tooNew.Cache, tooNew.Binary)
	}
	msg := err.Error()
	for _, want := range []string{fmt.Sprintf("v%d", newer), fmt.Sprintf("v%d", schemaVersion), buildinfo.Resolve().Name} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not name %q", msg, want)
		}
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != newer {
		t.Fatalf("user_version %d (%v), want %d", version, err, newer)
	}
	names := masterNames(t, db)
	for _, want := range append(append([]string{}, tables...), "events_id") {
		if !names[want] {
			t.Errorf("%s was dropped", want)
		}
	}
	if events, items := counts(); events != events0 || items != items0 {
		t.Fatalf("counts events %d items %d, want %d %d", events, items, events0, items0)
	}
	db.Close()
}

// TestCacheOpenHelperProcess is not a test: TestConcurrentOpenDuringMigration
// re-execs the test binary into it, so each opener is a real OS process. It
// opens the cache and syncs the ledger, exiting 1 on any error.
func TestCacheOpenHelperProcess(t *testing.T) {
	dbPath := os.Getenv("SATELLE_CACHE_HELPER_DB")
	if dbPath == "" {
		return
	}
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "helper:", err)
		os.Exit(1)
	}
	l, err := ledger.Open(os.Getenv("SATELLE_CACHE_HELPER_LEDGER"))
	if err != nil {
		fail(err)
	}
	c, err := Open(dbPath)
	if err != nil {
		fail(err)
	}
	if err := c.Sync(l); err != nil {
		fail(err)
	}
	if err := c.Close(); err != nil {
		fail(err)
	}
	os.Exit(0)
}

// Several processes opening a v2 cache at once race through the v2 -> v3
// migration; every one of them opens and inserts without error, and the
// result is one complete v3 cache.
func TestConcurrentOpenDuringMigration(t *testing.T) {
	const procs, rounds = 4, 10
	dir := t.TempDir()
	ledgerDir := filepath.Join(dir, "ledger")
	l, err := ledger.Open(ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("i%d", i)
		l.Append(id, item.KindCreated, "me", item.Fields{Kind: ptr("k"), Title: ptr(id), Status: ptr("s0")})
		l.Append(id, item.KindTransitioned, "me", item.Move{From: "s0", To: "s1"})
		l.Append(id, item.KindTransitioned, "me", item.Move{From: "s1", To: "s2"})
		total += 3
	}

	for round := 0; round < rounds; round++ {
		path := filepath.Join(dir, fmt.Sprintf("cache-%d.db", round))
		// WAL, as every cache satelle writes is (Open's DSN sets it).
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(v2Schema); err != nil {
			t.Fatal(err)
		}
		db.Close()

		cmds := make([]*exec.Cmd, procs)
		outs := make([]*strings.Builder, procs)
		for i := range cmds {
			cmd := exec.Command(os.Args[0], "-test.run=^TestCacheOpenHelperProcess$", "-test.count=1")
			cmd.Env = append(os.Environ(), "SATELLE_CACHE_HELPER_DB="+path, "SATELLE_CACHE_HELPER_LEDGER="+ledgerDir)
			outs[i] = &strings.Builder{}
			cmd.Stdout, cmd.Stderr = outs[i], outs[i]
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			cmds[i] = cmd
		}
		for i, cmd := range cmds {
			if err := cmd.Wait(); err != nil {
				t.Errorf("round %d: opener %d: %v\n%s", round, i, err, outs[i].String())
			}
		}
		if t.Failed() {
			t.FailNow()
		}

		c, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if err := c.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
			t.Fatalf("round %d: user_version %d (%v)", round, version, err)
		}
		if !masterNames(t, c.db)["events_id"] {
			t.Fatalf("round %d: events_id index missing", round)
		}
		noDuplicateIDs(t, c)
		all, err := c.Events("", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != total {
			t.Fatalf("round %d: cache holds %d events, ledger %d", round, len(all), total)
		}
		c.Close()
	}
}
