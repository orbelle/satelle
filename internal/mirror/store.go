// Package mirror is the push-fed read-only state for satelle serve
// (epic:serve-split / sty_dbdadfa0). Partitioned by repo_key — never a flat
// multi-repo bag (decision-local-db-placement). Serve renders only from this
// store; it never opens ~/.satelle/<repo-key>/satelle.db.
package mirror

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
)

// DefaultDirName is the subdirectory under GlobalDir() for the serve mirror.
const DefaultDirName = "serve"

// DefaultDBName is the mirror SQLite file name.
const DefaultDBName = "mirror.db"

// Store is the push-fed multi-repo mirror. All rows carry repo_key.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the mirror database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// DefaultPath returns ~/.satelle/serve/mirror.db (or SATELLE_HOME).
func DefaultPath(globalDir string) string {
	return filepath.Join(globalDir, DefaultDirName, DefaultDBName)
}

// ServerLogPath is the satelled request/push log under the machine home.
// One path, used by serve.Run and the mirror UI (sty_34037275).
func ServerLogPath(globalDir string) string {
	return filepath.Join(globalDir, DefaultDirName, "server.log")
}

func (s *Store) migrate() error {
	if err := s.createTables(); err != nil {
		return err
	}
	// snap_hash carries the digest of the last snapshot body applied to this
	// partition, so a periodic reconcile that finds nothing changed can record
	// freshness without rewriting rows or ringing the SSE doorbell
	// (sty_e6e467fe). Tolerated on an existing database: SQLite has no
	// ADD COLUMN IF NOT EXISTS, so a duplicate-column error means it is present.
	// seeded is 1 once a body replacing the story kind has been applied, so a
	// merge-only drain can tell a partition holding full state from one a topic
	// POST merely created (sty_a003a459). Same duplicate-column tolerance.
	for _, stmt := range []string{
		`ALTER TABLE partitions ADD COLUMN snap_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE partitions ADD COLUMN seeded INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return err
			}
		}
	}
	return nil
}

func (s *Store) createTables() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS partitions (
  repo_key   TEXT PRIMARY KEY,
  slug       TEXT NOT NULL DEFAULT '',
  seq        INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS items (
  repo_key TEXT NOT NULL,
  kind     TEXT NOT NULL,
  id       TEXT NOT NULL,
  payload  TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (repo_key, kind, id)
);
CREATE INDEX IF NOT EXISTS idx_items_repo_kind ON items(repo_key, kind);
CREATE TABLE IF NOT EXISTS docs (
  repo_key TEXT NOT NULL,
  kind     TEXT NOT NULL,
  name     TEXT NOT NULL,
  payload  TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (repo_key, kind, name)
);
`)
	return err
}

// Close closes the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// TouchPartition ensures a partition row exists and bumps seq. It never moves
// updated_at — that is the last CONFIRMED ingest, written by MarkFresh once a
// snapshot has committed — so a new row starts with none and reads as stale.
func (s *Store) TouchPartition(ctx context.Context, repoKey, slug string, now time.Time) (seq int64, err error) {
	repoKey = strings.TrimSpace(repoKey)
	if repoKey == "" {
		return 0, fmt.Errorf("mirror: empty repo_key")
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO partitions (repo_key, slug, seq, updated_at) VALUES (?, ?, 1, '')
ON CONFLICT(repo_key) DO UPDATE SET
  seq = seq + 1,
  slug = CASE WHEN excluded.slug != '' THEN excluded.slug ELSE partitions.slug END
`, repoKey, slug)
	if err != nil {
		return 0, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT seq FROM partitions WHERE repo_key = ?`, repoKey).Scan(&seq)
	return seq, err
}

// MarkFresh records that the partition was confirmed current at now WITHOUT
// claiming anything changed: updated_at moves so the staleness badge clears,
// seq does not, so no viewer is told to re-render (sty_e6e467fe). Used by the
// reconcile path when the re-requested snapshot is byte-identical to the one
// already stored, and after every snapshot apply that commits. No-op success
// for an unknown repo_key.
func (s *Store) MarkFresh(ctx context.Context, repoKey string, now time.Time) error {
	repoKey = strings.TrimSpace(repoKey)
	if repoKey == "" {
		return fmt.Errorf("mirror: empty repo_key")
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE partitions SET updated_at = ? WHERE repo_key = ?`,
		now.UTC().Format(time.RFC3339Nano), repoKey)
	return err
}

// SnapshotHash returns the digest of the last snapshot applied to repoKey, or
// "" when the partition is unknown or predates hashing.
func (s *Store) SnapshotHash(ctx context.Context, repoKey string) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT snap_hash FROM partitions WHERE repo_key = ?`, repoKey).Scan(&h)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return h, err
}

// PartitionSeeded reports whether a full story set has ever been applied to
// repoKey. False for an unknown partition and for one only change events or
// merge-only bodies have touched.
func (s *Store) PartitionSeeded(ctx context.Context, repoKey string) (bool, error) {
	var seeded int
	err := s.db.QueryRowContext(ctx, `SELECT seeded FROM partitions WHERE repo_key = ?`, repoKey).Scan(&seeded)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return seeded == 1, err
}

// MarkSeeded records that repoKey holds a full story set: the unchanged-body
// short-circuit applies nothing but has just confirmed one.
func (s *Store) MarkSeeded(ctx context.Context, repoKey string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE partitions SET seeded = 1 WHERE repo_key = ?`, repoKey)
	return err
}

// SetSnapshotHash records the digest of the snapshot body just applied.
func (s *Store) SetSnapshotHash(ctx context.Context, repoKey, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE partitions SET snap_hash = ? WHERE repo_key = ?`, hash, repoKey)
	return err
}

// GetPartition returns one partition's metadata. ok is false when repoKey is
// unknown.
func (s *Store) GetPartition(ctx context.Context, repoKey string) (p Partition, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `
SELECT repo_key, slug, seq, updated_at FROM partitions WHERE repo_key = ?
`, repoKey).Scan(&p.RepoKey, &p.Slug, &p.Seq, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return Partition{}, false, nil
	}
	if err != nil {
		return Partition{}, false, err
	}
	return p, true, nil
}

// UpsertItem stores one work item JSON payload under (repo_key, kind, id).
func (s *Store) UpsertItem(ctx context.Context, repoKey, kind, id string, payload any, now time.Time) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	at := now.UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `
INSERT INTO items (repo_key, kind, id, payload, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(repo_key, kind, id) DO UPDATE SET payload = excluded.payload, updated_at = excluded.updated_at
`, repoKey, kind, id, string(b), at)
	return err
}

// KindRows is one kind's row set for ApplySnapshot (replace or merge).
type KindRows struct {
	Kind  string
	Items []ItemRow
}

// CollisionError names a duplicate item identity inside a snapshot replace set
// (sty_e4e1a008). The operator sees kind and id instead of a SQLite constraint code.
type CollisionError struct {
	Kind string
	ID   string
}

func (e *CollisionError) Error() string {
	return fmt.Sprintf("mirror: snapshot has duplicate item kind=%s id=%q", e.Kind, e.ID)
}

func duplicateItemID(items []ItemRow) (string, bool) {
	seen := make(map[string]struct{}, len(items))
	for _, it := range items {
		if _, ok := seen[it.ID]; ok {
			return it.ID, true
		}
		seen[it.ID] = struct{}{}
	}
	return "", false
}

func isUniqueConstraint(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		// SQLITE_CONSTRAINT = 19; extended codes keep that low byte.
		return se.Code()&0xff == 19
	}
	return strings.Contains(err.Error(), "UNIQUE constraint")
}

// ApplySnapshot applies every replace (delete+insert) and merge (upsert) kind
// inside one transaction so a mid-apply failure leaves the partition unchanged
// (sty_3562c820 AC2). Callers pass only the kinds they authorise. A replace of
// the story kind marks the partition seeded in the same transaction.
func (s *Store) ApplySnapshot(ctx context.Context, repoKey string, replace, merge []KindRows, now time.Time) error {
	repoKey = strings.TrimSpace(repoKey)
	if repoKey == "" {
		return fmt.Errorf("mirror: empty repo_key")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	at := now.UTC().Format(time.RFC3339Nano)
	for _, kr := range replace {
		if id, dup := duplicateItemID(kr.Items); dup {
			return &CollisionError{Kind: kr.Kind, ID: id}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM items WHERE repo_key = ? AND kind = ?`, repoKey, kr.Kind); err != nil {
			return err
		}
		for _, it := range kr.Items {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO items (repo_key, kind, id, payload, updated_at) VALUES (?, ?, ?, ?, ?)
`, repoKey, kr.Kind, it.ID, it.Payload, at); err != nil {
				if isUniqueConstraint(err) {
					return &CollisionError{Kind: kr.Kind, ID: it.ID}
				}
				return err
			}
		}
		if kr.Kind == "story" {
			if _, err := tx.ExecContext(ctx, `UPDATE partitions SET seeded = 1 WHERE repo_key = ?`, repoKey); err != nil {
				return err
			}
		}
	}
	for _, kr := range merge {
		for _, it := range kr.Items {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO items (repo_key, kind, id, payload, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(repo_key, kind, id) DO UPDATE SET payload = excluded.payload, updated_at = excluded.updated_at
`, repoKey, kr.Kind, it.ID, it.Payload, at); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// ReplaceKind deletes all items of kind for repoKey then inserts the given set.
// Thin wrapper over ApplySnapshot so the DELETE+INSERT SQL lives once.
func (s *Store) ReplaceKind(ctx context.Context, repoKey, kind string, items []ItemRow, now time.Time) error {
	return s.ApplySnapshot(ctx, repoKey, []KindRows{{Kind: kind, Items: items}}, nil, now)
}

// UpsertItems insert-or-updates rows of one kind without deleting siblings
// (ledger merge path for light drains — sty_3562c820).
func (s *Store) UpsertItems(ctx context.Context, repoKey, kind string, items []ItemRow, now time.Time) error {
	return s.ApplySnapshot(ctx, repoKey, nil, []KindRows{{Kind: kind, Items: items}}, now)
}

// ItemRow is a raw stored item.
type ItemRow struct {
	ID      string
	Payload string
}

// ListItems returns all items of kind for a partition.
func (s *Store) ListItems(ctx context.Context, repoKey, kind string) ([]ItemRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, payload FROM items WHERE repo_key = ? AND kind = ? ORDER BY id
`, repoKey, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ItemRow
	for rows.Next() {
		var r ItemRow
		if err := rows.Scan(&r.ID, &r.Payload); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetItem returns one item payload or sql.ErrNoRows.
func (s *Store) GetItem(ctx context.Context, repoKey, kind, id string) (string, error) {
	var payload string
	err := s.db.QueryRowContext(ctx, `
SELECT payload FROM items WHERE repo_key = ? AND kind = ? AND id = ?
`, repoKey, kind, id).Scan(&payload)
	return payload, err
}

// ListPartitions returns known repo keys.
func (s *Store) ListPartitions(ctx context.Context) ([]Partition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_key, slug, seq, updated_at FROM partitions ORDER BY slug, repo_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Partition
	for rows.Next() {
		var p Partition
		if err := rows.Scan(&p.RepoKey, &p.Slug, &p.Seq, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FindBySlug returns the first partition whose landing slug equals slug.
// ok is false when slug is empty or no row matches. Mechanism only — callers
// decide collision policy (sty_57d5ce25).
func (s *Store) FindBySlug(ctx context.Context, slug string) (p Partition, ok bool, err error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return Partition{}, false, nil
	}
	err = s.db.QueryRowContext(ctx, `
SELECT repo_key, slug, seq, updated_at FROM partitions WHERE slug = ? ORDER BY repo_key LIMIT 1
`, slug).Scan(&p.RepoKey, &p.Slug, &p.Seq, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return Partition{}, false, nil
	}
	if err != nil {
		return Partition{}, false, err
	}
	return p, true, nil
}

// Partition is one repo's mirror slice metadata.
type Partition struct {
	RepoKey   string
	Slug      string
	Seq       int64
	UpdatedAt string
}

// PartitionDetail is a partition plus kind counts and best-effort repo path
// (from identity meta) for CLI list/prune (sty_eb61be02 / epic:mirror-hygiene).
type PartitionDetail struct {
	Partition
	Stories int
	Tasks   int
	Docs    int
	Path    string
}

// DeletePartition removes a partition and all of its items/docs. No-op success
// when repo_key is unknown (idempotent prune). CLI remains the sole writer via
// POST /ingest/remove (sty_eb61be02).
func (s *Store) DeletePartition(ctx context.Context, repoKey string) error {
	repoKey = strings.TrimSpace(repoKey)
	if repoKey == "" {
		return fmt.Errorf("mirror: empty repo_key")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM items WHERE repo_key = ?`, repoKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM docs WHERE repo_key = ?`, repoKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM partitions WHERE repo_key = ?`, repoKey); err != nil {
		return err
	}
	return tx.Commit()
}

// CountItems returns how many rows of kind exist for repoKey.
func (s *Store) CountItems(ctx context.Context, repoKey, kind string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM items WHERE repo_key = ? AND kind = ?
`, repoKey, kind).Scan(&n)
	return n, err
}

// ListPartitionDetails returns every partition with story/task/doc counts and
// path from identity meta when present.
func (s *Store) ListPartitionDetails(ctx context.Context) ([]PartitionDetail, error) {
	parts, err := s.ListPartitions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PartitionDetail, 0, len(parts))
	for _, p := range parts {
		d := PartitionDetail{Partition: p}
		if d.Stories, err = s.CountItems(ctx, p.RepoKey, "story"); err != nil {
			return nil, err
		}
		if d.Tasks, err = s.CountItems(ctx, p.RepoKey, "task"); err != nil {
			return nil, err
		}
		if d.Docs, err = s.CountItems(ctx, p.RepoKey, "doc"); err != nil {
			return nil, err
		}
		if payload, err := s.GetItem(ctx, p.RepoKey, "identity", "meta"); err == nil {
			var meta IdentityMeta
			if json.Unmarshal([]byte(payload), &meta) == nil {
				d.Path = strings.TrimSpace(meta.RepoRoot)
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// SignedIn is the user named by the most recently pushed partition's identity:
// every partition on this machine is pushed by the same CLI login, so the
// latest push says who is signed in now. Nil when it names none.
func (s *Store) SignedIn(ctx context.Context) *User {
	var payload string
	err := s.db.QueryRowContext(ctx, `
SELECT payload FROM items WHERE kind = 'identity' AND id = 'meta'
ORDER BY updated_at DESC LIMIT 1`).Scan(&payload)
	if err != nil {
		return nil
	}
	var meta IdentityMeta
	if json.Unmarshal([]byte(payload), &meta) != nil || meta.User == nil || meta.User.Name+meta.User.Email == "" {
		return nil
	}
	return meta.User
}

// ApplyChange records a coarse change event (order:2 publisher). Bumps seq and
// leaves item bodies unchanged — full bodies arrive via snapshot (order:4).
func (s *Store) ApplyChange(ctx context.Context, repoKey, topic string, now time.Time) (seq int64, err error) {
	return s.TouchPartition(ctx, repoKey, "", now)
}
