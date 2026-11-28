package cache

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS files (item TEXT PRIMARY KEY, offset INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS items (
	id TEXT PRIMARY KEY, kind TEXT, title TEXT, body TEXT, criteria TEXT, status TEXT,
	tags TEXT, parent TEXT, category TEXT, workflow TEXT, assignees TEXT, inputs TEXT, trigger TEXT, base TEXT, repo TEXT,
	created_at TEXT, updated_at TEXT);
CREATE TABLE IF NOT EXISTS item_tags (item TEXT NOT NULL, tag TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS item_tags_tag ON item_tags(tag);
CREATE TABLE IF NOT EXISTS events (
	seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT, item TEXT, kind TEXT, at TEXT,
	actor TEXT, payload TEXT, prev TEXT, hash TEXT);
CREATE INDEX IF NOT EXISTS events_item ON events(item, kind);
CREATE UNIQUE INDEX IF NOT EXISTS events_id ON events(id);
CREATE TABLE IF NOT EXISTS seats (
	item TEXT, session TEXT, location TEXT, base TEXT, bound_at TEXT,
	machine TEXT, host TEXT, user TEXT, PRIMARY KEY (item, session));
CREATE TABLE IF NOT EXISTS attachments (
	item TEXT, name TEXT, sha256 TEXT, size INTEGER, media_type TEXT, at TEXT, actor TEXT);
CREATE INDEX IF NOT EXISTS attachments_item ON attachments(item);
`

const schemaVersion = 5

var tables = []string{"files", "items", "item_tags", "events", "seats", "attachments"}

type SchemaTooNewError struct {
	Path   string
	Cache  int
	Binary int
	Build  string
}

func (e *SchemaTooNewError) Error() string {
	return fmt.Sprintf("cache: %s is at schema v%d, newer than v%d that this binary understands; it was written by a newer satelle — update %s to the release that wrote it (an older satelle never lowers the cache)",
		e.Path, e.Cache, e.Binary, e.Build)
}

func tooNew(path string, version int) error {
	return &SchemaTooNewError{Path: path, Cache: version, Binary: schemaVersion, Build: buildinfo.Resolve().String()}
}

type Cache struct{ db *sql.DB }

func Open(path string) (*Cache, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("cache: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db, path); err != nil {
		db.Close()
		return nil, err
	}
	return &Cache{db: db}, nil
}

func migrate(db *sql.DB, path string) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	switch {
	case version > schemaVersion:
		return tooNew(path, version)
	case version == schemaVersion:
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	switch {
	case version > schemaVersion:
		return tooNew(path, version)
	case version == schemaVersion:
		return tx.Commit()
	}
	for _, t := range tables {
		if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
			return fmt.Errorf("cache: %w", err)
		}
	}
	for _, stmt := range strings.Split(schema, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("cache: schema: %w", err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("cache: schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	return nil
}

func (c *Cache) Close() error { return c.db.Close() }

func (c *Cache) Sync(l *ledger.Store) error {
	items, err := l.Items()
	if err != nil {
		return err
	}
	for _, id := range items {
		if err := c.syncItem(l, id, false); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cache) SyncItem(l *ledger.Store, id string) error { return c.syncItem(l, id, false) }

func (c *Cache) ResetItem(l *ledger.Store, id string) error { return c.syncItem(l, id, true) }

func (c *Cache) syncItem(l *ledger.Store, id string, reset bool) error {
	var offset int64
	err := c.db.QueryRow(`SELECT offset FROM files WHERE item = ?`, id).Scan(&offset)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	size, err := l.Size(id)
	if err != nil {
		return err
	}
	if size == offset && !reset {
		return nil
	}
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	offset = 0
	err = tx.QueryRow(`SELECT offset FROM files WHERE item = ?`, id).Scan(&offset)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if size, err = l.Size(id); err != nil {
		return err
	}
	if size == offset && !reset {
		return nil
	}
	if size < offset || reset {
		offset = 0
	}
	fresh, next, err := l.ReadFrom(id, offset)
	if err != nil {
		return err
	}
	if len(fresh) == 0 && offset != 0 {
		return nil
	}
	all, err := l.Read(id)
	if err != nil {
		return err
	}
	if offset == 0 {
		for _, t := range []string{"events", "items", "item_tags", "seats", "attachments"} {
			col := "item"
			if t == "items" {
				col = "id"
			}
			if _, err := tx.Exec(`DELETE FROM `+t+` WHERE `+col+` = ?`, id); err != nil {
				return err
			}
		}
	}
	for _, e := range fresh {
		if _, err := tx.Exec(`INSERT INTO events (id, item, kind, at, actor, payload, prev, hash) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
			e.ID, e.Item, e.Kind, ts(e.At), e.Actor, string(e.Payload), e.Prev, e.Hash); err != nil {
			return err
		}
	}
	if err := writeFolds(tx, id, all); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO files (item, offset) VALUES (?, ?) ON CONFLICT(item) DO UPDATE SET offset = excluded.offset`, id, next); err != nil {
		return err
	}
	return tx.Commit()
}

func writeFolds(tx *sql.Tx, id string, all []ledger.Event) error {
	for _, t := range []string{"item_tags", "seats", "attachments"} {
		if _, err := tx.Exec(`DELETE FROM `+t+` WHERE item = ?`, id); err != nil {
			return err
		}
	}
	if it, err := item.Fold(all); err == nil {
		criteria, _ := json.Marshal(it.Criteria)
		tags, _ := json.Marshal(it.Tags)
		assignees, _ := json.Marshal(it.Assignees)
		inputs := ""
		if it.Inputs != nil {
			b, _ := json.Marshal(it.Inputs)
			inputs = string(b)
		}
		trigger := ""
		if it.Trigger != nil {
			b, _ := json.Marshal(it.Trigger)
			trigger = string(b)
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO items VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			it.ID, it.Kind, it.Title, it.Body, string(criteria), it.Status, string(tags), it.Parent,
			it.Category, it.Workflow, string(assignees), inputs, trigger, it.Base, it.Repo, ts(it.CreatedAt), ts(it.UpdatedAt)); err != nil {
			return err
		}
		for _, tag := range it.Tags {
			if _, err := tx.Exec(`INSERT INTO item_tags (item, tag) VALUES (?, ?)`, id, tag); err != nil {
				return err
			}
		}
	}
	for _, s := range item.FoldSeats(all) {
		if _, err := tx.Exec(`INSERT INTO seats VALUES (?,?,?,?,?,?,?,?)`, s.Item, s.Session, s.Location, s.Base, ts(s.BoundAt),
			s.Machine, s.Host, s.User); err != nil {
			return err
		}
	}
	for _, a := range item.FoldAttachments(all) {
		if _, err := tx.Exec(`INSERT INTO attachments VALUES (?,?,?,?,?,?,?)`, a.Item, a.Name, a.SHA256, a.Size, a.MediaType, ts(a.At), a.Actor); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cache) Rebuild(l *ledger.Store) error {
	for _, t := range tables {
		if _, err := c.db.Exec(`DELETE FROM ` + t); err != nil {
			return err
		}
	}
	return c.Sync(l)
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

type Query struct {
	ID, Kind, Status, Tag, Category, Parent, Workflow string
}

func (c *Cache) Items(q Query) ([]item.Item, error) {
	var where []string
	var args []any
	add := func(cond, v string) {
		if v != "" {
			where = append(where, cond)
			args = append(args, v)
		}
	}
	add("id = ?", q.ID)
	add("kind = ?", q.Kind)
	add("status = ?", q.Status)
	add("category = ?", q.Category)
	add("parent = ?", q.Parent)
	add("workflow = ?", q.Workflow)
	add("id IN (SELECT item FROM item_tags WHERE tag = ?)", q.Tag)
	sqlText := `SELECT id, kind, title, body, criteria, status, tags, parent, category, workflow, assignees, inputs, trigger, base, repo, created_at, updated_at FROM items`
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := c.db.Query(sqlText+" ORDER BY created_at, id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []item.Item
	for rows.Next() {
		var it item.Item
		var criteria, tags, assignees, inputs, trigger, created, updated string
		if err := rows.Scan(&it.ID, &it.Kind, &it.Title, &it.Body, &criteria, &it.Status, &tags, &it.Parent,
			&it.Category, &it.Workflow, &assignees, &inputs, &trigger, &it.Base, &it.Repo, &created, &updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(criteria), &it.Criteria)
		_ = json.Unmarshal([]byte(tags), &it.Tags)
		_ = json.Unmarshal([]byte(assignees), &it.Assignees)
		if inputs != "" {
			it.Inputs = &item.Selection{}
			_ = json.Unmarshal([]byte(inputs), it.Inputs)
		}
		if trigger != "" {
			it.Trigger = &item.Trigger{}
			_ = json.Unmarshal([]byte(trigger), it.Trigger)
		}
		it.CreatedAt, it.UpdatedAt = parseTS(created), parseTS(updated)
		out = append(out, it)
	}
	return out, rows.Err()
}

func (c *Cache) Item(id string) (it item.Item, ok bool, err error) {
	rows, err := c.Items(Query{ID: id})
	if err != nil || len(rows) == 0 {
		return it, false, err
	}
	return rows[0], true, nil
}

func (c *Cache) Events(itemID, kind string) ([]ledger.Event, error) {
	var where []string
	var args []any
	if itemID != "" {
		where, args = append(where, "item = ?"), append(args, itemID)
	}
	if kind != "" {
		where, args = append(where, "kind = ?"), append(args, kind)
	}
	q := `SELECT id, item, kind, at, actor, payload, prev, hash FROM events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	order := " ORDER BY at, seq"
	if itemID != "" {
		order = " ORDER BY seq"
	}
	rows, err := c.db.Query(q+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledger.Event
	for rows.Next() {
		var e ledger.Event
		var at, payload string
		if err := rows.Scan(&e.ID, &e.Item, &e.Kind, &at, &e.Actor, &payload, &e.Prev, &e.Hash); err != nil {
			return nil, err
		}
		e.At = parseTS(at)
		if payload != "" {
			e.Payload = json.RawMessage(payload)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (c *Cache) Seats(session, itemID string) ([]item.Seat, error) {
	rows, err := c.db.Query(`SELECT item, session, location, base, bound_at, machine, host, user FROM seats
		WHERE (? = '' OR session = ?) AND (? = '' OR item = ?) ORDER BY bound_at, item`, session, session, itemID, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []item.Seat
	for rows.Next() {
		var s item.Seat
		var at string
		if err := rows.Scan(&s.Item, &s.Session, &s.Location, &s.Base, &at, &s.Machine, &s.Host, &s.User); err != nil {
			return nil, err
		}
		s.BoundAt = parseTS(at)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (c *Cache) Attachments(itemID string) ([]item.Attachment, error) {
	rows, err := c.db.Query(`SELECT item, name, sha256, size, media_type, at, actor FROM attachments WHERE item = ? ORDER BY rowid`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []item.Attachment
	for rows.Next() {
		var a item.Attachment
		var at string
		if err := rows.Scan(&a.Item, &a.Name, &a.SHA256, &a.Size, &a.MediaType, &at, &a.Actor); err != nil {
			return nil, err
		}
		a.At = parseTS(at)
		out = append(out, a)
	}
	return out, rows.Err()
}
