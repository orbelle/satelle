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

const DefaultDirName = "serve"

const DefaultDBName = "mirror.db"

type Store struct {
	db *sql.DB
}

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

func DefaultPath(globalDir string) string {
	return filepath.Join(globalDir, DefaultDirName, DefaultDBName)
}

func ServerLogPath(globalDir string) string {
	return filepath.Join(globalDir, DefaultDirName, "server.log")
}

func DoctorLogPath(globalDir string) string {
	return filepath.Join(globalDir, DefaultDirName, "doctor.log")
}

func (s *Store) migrate() error {
	if err := s.createTables(); err != nil {
		return err
	}
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

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

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

func (s *Store) SnapshotHash(ctx context.Context, repoKey string) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT snap_hash FROM partitions WHERE repo_key = ?`, repoKey).Scan(&h)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return h, err
}

func (s *Store) PartitionSeeded(ctx context.Context, repoKey string) (bool, error) {
	var seeded int
	err := s.db.QueryRowContext(ctx, `SELECT seeded FROM partitions WHERE repo_key = ?`, repoKey).Scan(&seeded)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return seeded == 1, err
}

func (s *Store) MarkSeeded(ctx context.Context, repoKey string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE partitions SET seeded = 1 WHERE repo_key = ?`, repoKey)
	return err
}

func (s *Store) SetSnapshotHash(ctx context.Context, repoKey, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE partitions SET snap_hash = ? WHERE repo_key = ?`, hash, repoKey)
	return err
}

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

type KindRows struct {
	Kind  string
	Items []ItemRow
}

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
		return se.Code()&0xff == 19
	}
	return strings.Contains(err.Error(), "UNIQUE constraint")
}

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

func (s *Store) ReplaceKind(ctx context.Context, repoKey, kind string, items []ItemRow, now time.Time) error {
	return s.ApplySnapshot(ctx, repoKey, []KindRows{{Kind: kind, Items: items}}, nil, now)
}

func (s *Store) UpsertItems(ctx context.Context, repoKey, kind string, items []ItemRow, now time.Time) error {
	return s.ApplySnapshot(ctx, repoKey, nil, []KindRows{{Kind: kind, Items: items}}, now)
}

type ItemRow struct {
	ID      string
	Payload string
}

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

func (s *Store) GetItem(ctx context.Context, repoKey, kind, id string) (string, error) {
	var payload string
	err := s.db.QueryRowContext(ctx, `
SELECT payload FROM items WHERE repo_key = ? AND kind = ? AND id = ?
`, repoKey, kind, id).Scan(&payload)
	return payload, err
}

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

type Partition struct {
	RepoKey   string
	Slug      string
	Seq       int64
	UpdatedAt string
}

type PartitionDetail struct {
	Partition
	Stories int
	Tasks   int
	Docs    int
	Path    string
}

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

func (s *Store) CountItems(ctx context.Context, repoKey, kind string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM items WHERE repo_key = ? AND kind = ?
`, repoKey, kind).Scan(&n)
	return n, err
}

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

func (s *Store) ApplyChange(ctx context.Context, repoKey, topic string, now time.Time) (seq int64, err error) {
	return s.TouchPartition(ctx, repoKey, "", now)
}
