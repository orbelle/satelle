package mirror_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// sty_a003a459 AC1: only a replace of the story kind seeds a partition. A topic
// POST (TouchPartition / ApplyChange) creates the row unseeded, and a merge-only
// apply neither sets nor clears the flag.
func TestPartitionSeededOnlyByStoryReplace(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now()
	seeded := func(want bool, when string) {
		t.Helper()
		got, err := s.PartitionSeeded(ctx, "rk")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s: seeded = %v, want %v", when, got, want)
		}
	}
	seeded(false, "unknown partition")
	if _, err := s.ApplyChange(ctx, "rk", "stories", now); err != nil {
		t.Fatal(err)
	}
	seeded(false, "after a change event created the partition")
	row := []mirror.ItemRow{{ID: "sty_1", Payload: `{"id":"sty_1"}`}}
	if err := s.ApplySnapshot(ctx, "rk", []mirror.KindRows{{Kind: "seat"}}, []mirror.KindRows{{Kind: "story", Items: row}}, now); err != nil {
		t.Fatal(err)
	}
	seeded(false, "after a merge-only story apply")
	if err := s.ReplaceKind(ctx, "rk", "story", row, now); err != nil {
		t.Fatal(err)
	}
	seeded(true, "after a story replace")
	if err := s.UpsertItems(ctx, "rk", "story", row, now); err != nil {
		t.Fatal(err)
	}
	seeded(true, "after a later merge-only apply")
}

// A database from before the seeded column gains it on Open, and its existing
// partitions read unseeded, so each repo's first drain posts one full body.
func TestPartitionSeededMigratesPreExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE partitions (repo_key TEXT PRIMARY KEY, slug TEXT NOT NULL DEFAULT '', seq INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL, snap_hash TEXT NOT NULL DEFAULT '');
INSERT INTO partitions (repo_key, slug, seq, updated_at, snap_hash) VALUES ('rk', 'old', 3, '', 'abc');`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	s, err := mirror.Open(path)
	if err != nil {
		t.Fatalf("open pre-migration database: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, err := s.PartitionSeeded(context.Background(), "rk")
	if err != nil || got {
		t.Fatalf("pre-migration partition seeded = %v (err %v), want false", got, err)
	}
}

func TestMirrorPartitionedUpsertAndList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mirror.db")
	s, err := mirror.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now()

	if _, err := s.TouchPartition(ctx, "repo-a", "alpha", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TouchPartition(ctx, "repo-b", "beta", now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertItem(ctx, "repo-a", "story", "sty_1", map[string]any{"id": "sty_1", "title": "A"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertItem(ctx, "repo-b", "story", "sty_1", map[string]any{"id": "sty_1", "title": "B"}, now); err != nil {
		t.Fatal(err)
	}

	a, err := s.ListItems(ctx, "repo-a", "story")
	if err != nil || len(a) != 1 {
		t.Fatalf("repo-a items = %v err=%v", a, err)
	}
	b, err := s.ListItems(ctx, "repo-b", "story")
	if err != nil || len(b) != 1 {
		t.Fatalf("repo-b items = %v err=%v", b, err)
	}
	if a[0].Payload == b[0].Payload {
		t.Error("partitions must not share item payloads")
	}

	parts, err := s.ListPartitions(ctx)
	if err != nil || len(parts) != 2 {
		t.Fatalf("partitions = %v err=%v", parts, err)
	}
}

func TestFindBySlug(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mirror.db")
	s, err := mirror.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now()
	if _, err := s.TouchPartition(ctx, "rk1", "alpha", now); err != nil {
		t.Fatal(err)
	}
	p, ok, err := s.FindBySlug(ctx, "alpha")
	if err != nil || !ok || p.RepoKey != "rk1" {
		t.Fatalf("FindBySlug alpha = %+v ok=%v err=%v", p, ok, err)
	}
	_, ok, err = s.FindBySlug(ctx, "missing")
	if err != nil || ok {
		t.Fatalf("missing: ok=%v err=%v", ok, err)
	}
	_, ok, err = s.FindBySlug(ctx, "")
	if err != nil || ok {
		t.Fatalf("empty: ok=%v err=%v", ok, err)
	}
}

func TestDeletePartition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	s, err := mirror.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now()
	if _, err := s.TouchPartition(ctx, "rk-del", "del", now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertItem(ctx, "rk-del", "story", "sty_1", map[string]any{"id": "sty_1"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePartition(ctx, "rk-del"); err != nil {
		t.Fatal(err)
	}
	parts, err := s.ListPartitions(ctx)
	if err != nil || len(parts) != 0 {
		t.Fatalf("parts after delete = %v err=%v", parts, err)
	}
	items, err := s.ListItems(ctx, "rk-del", "story")
	if err != nil || len(items) != 0 {
		t.Fatalf("items after delete = %v err=%v", items, err)
	}
	// Idempotent.
	if err := s.DeletePartition(ctx, "rk-del"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePartition(ctx, ""); err == nil {
		t.Fatal("empty repo_key must error")
	}
}

func TestReplaceKindIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	s, err := mirror.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now()
	_ = s.UpsertItem(ctx, "r", "story", "old", map[string]string{"id": "old"}, now)

	items := []mirror.ItemRow{
		{ID: "new1", Payload: `{"id":"new1"}`},
		{ID: "new2", Payload: `{"id":"new2"}`},
	}
	if err := s.ReplaceKind(ctx, "r", "story", items, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceKind(ctx, "r", "story", items, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListItems(ctx, "r", "story")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v err=%v", got, err)
	}
	for _, g := range got {
		if g.ID == "old" {
			t.Error("old item should be deleted by replace")
		}
	}
}
