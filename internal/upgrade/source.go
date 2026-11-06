package upgrade

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// OldItem is one pre-1.0 work_items row.
type OldItem struct {
	ID, Kind, Title, Body, Status, Priority, Category, Parent, Criteria, Assignee string
	Tags                                                                          []string
	Created, Updated                                                              time.Time
	Archived                                                                      bool
	// Extra holds the row's other columns (park_origin, input_decls, ...),
	// carried onto upgrade.mapped and never interpreted.
	Extra map[string]string
}

// OldEvent is one pre-1.0 evidence row: an item's history.
type OldEvent struct {
	ID, Item, Kind, Actor, Body, Refs, BaseID, WorkflowHash string
	Payload                                                 string
	At                                                      time.Time
}

// OldDoc is one file of an item's old document directory.
type OldDoc struct {
	Name, Path, MediaType string
	At                    time.Time
}

// Source is a pre-1.0 store, read only.
type Source struct {
	Dir      string
	Items    []OldItem
	Events   map[string][]OldEvent // item → its history, oldest first
	Docs     map[string][]OldDoc   // item → its documents
	Orphans  int                   // history rows naming no item
	Skipped  int                   // regenerated views and sidecars, not imported
	Problems []string              // rows that cannot be imported as they are
}

// OpenSource reads <dir>/satelle.db (read-only) and <dir>/stories.
func OpenSource(dir string) (*Source, error) {
	db := filepath.Join(dir, "satelle.db")
	if _, err := os.Stat(db); err != nil {
		return nil, fmt.Errorf("no pre-1.0 store at %s: %w", dir, err)
	}
	h, err := sql.Open("sqlite", "file:"+(&url.URL{Path: db}).EscapedPath()+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer h.Close()
	s := &Source{Dir: dir, Events: map[string][]OldEvent{}, Docs: map[string][]OldDoc{}}
	if err := s.readItems(h); err != nil {
		return nil, fmt.Errorf("read %s work_items: %w", db, err)
	}
	if err := s.readEvents(h); err != nil {
		return nil, fmt.Errorf("read %s evidence: %w", db, err)
	}
	if err := s.readDocs(); err != nil {
		return nil, err
	}
	return s, nil
}

// columns returns a table's column names.
func columns(h *sql.DB, table string) ([]string, error) {
	rows, err := h.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no table %s", table)
	}
	return out, rows.Err()
}

// rowsOf selects every column of a table as text, by name.
func rowsOf(h *sql.DB, table, order string) ([]map[string]string, error) {
	cols, err := columns(h, table)
	if err != nil {
		return nil, err
	}
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = fmt.Sprintf("CAST(%q AS TEXT)", c)
	}
	rows, err := h.Query(fmt.Sprintf("SELECT %s FROM %q ORDER BY %s", strings.Join(sel, ", "), table, order))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]string{}
		for i, c := range cols {
			m[c] = vals[i].String
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// parseTime reads the old store's times: RFC 3339, else sqlite's format.
func parseTime(s string) (time.Time, error) {
	for _, f := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(f, strings.TrimSpace(s)); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unreadable time %q", s)
}

// knownItemColumns are the work_items columns an OldItem names.
var knownItemColumns = map[string]bool{
	"id": true, "kind": true, "title": true, "body": true, "status": true, "priority": true, "category": true,
	"parent_id": true, "acceptance_criteria": true, "tags": true, "created_at": true, "updated_at": true,
	"archived": true, "assignee": true,
}

func (s *Source) readItems(h *sql.DB) error {
	rows, err := rowsOf(h, "work_items", "created_at, rowid")
	if err != nil {
		return err
	}
	for _, r := range rows {
		it := OldItem{
			ID: r["id"], Kind: r["kind"], Title: r["title"], Body: r["body"], Status: r["status"], Priority: r["priority"],
			Category: r["category"], Parent: r["parent_id"], Criteria: r["acceptance_criteria"], Assignee: r["assignee"],
			Archived: r["archived"] == "1", Extra: map[string]string{},
		}
		if !ledger.ValidItem(it.ID) {
			s.Problems = append(s.Problems, fmt.Sprintf("work_items %q: not a valid item id", it.ID))
			continue
		}
		if tags := strings.TrimSpace(r["tags"]); tags != "" && tags != "[]" {
			if err := json.Unmarshal([]byte(tags), &it.Tags); err != nil {
				s.Problems = append(s.Problems, fmt.Sprintf("%s: tags %q are not a JSON list", it.ID, tags))
			}
		}
		if it.Created, err = parseTime(r["created_at"]); err != nil {
			s.Problems = append(s.Problems, fmt.Sprintf("%s: created_at: %v", it.ID, err))
		}
		if r["updated_at"] != "" {
			it.Updated, _ = parseTime(r["updated_at"])
		}
		for c, v := range r {
			if !knownItemColumns[c] && v != "" {
				it.Extra[c] = v
			}
		}
		s.Items = append(s.Items, it)
	}
	return nil
}

func (s *Source) readEvents(h *sql.DB) error {
	rows, err := rowsOf(h, "evidence", "created_at, rowid")
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, it := range s.Items {
		known[it.ID] = true
	}
	for _, r := range rows {
		if !known[r["story_id"]] {
			s.Orphans++
			continue
		}
		at, err := parseTime(r["created_at"])
		if err != nil {
			s.Problems = append(s.Problems, fmt.Sprintf("evidence %s: created_at: %v", r["id"], err))
			continue
		}
		e := OldEvent{
			ID: r["id"], Item: r["story_id"], Kind: r["kind"], Actor: r["actor"], Body: r["body"], Payload: r["payload"],
			Refs: r["refs"], BaseID: r["base_id"], WorkflowHash: r["workflow_hash"], At: at,
		}
		s.Events[e.Item] = append(s.Events[e.Item], e)
	}
	return nil
}

// sidecarSuffix marks a binary document's metadata file in the old store.
const sidecarSuffix = ".satelle.json"

// readDocs lists <dir>/stories/<id>/ regular files. Sidecars give a binary's
// media type and attach time and are not imported; subdirectories, dotfiles
// and the top-level stories/<id>.md regenerated views are skipped.
func (s *Source) readDocs() error {
	root := filepath.Join(s.Dir, "stories")
	ents, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, it := range s.Items {
		known[it.ID] = true
	}
	for _, d := range ents {
		if !d.IsDir() {
			s.Skipped++ // a regenerated stories/<id>.md view
			continue
		}
		id := d.Name()
		files, err := os.ReadDir(filepath.Join(root, id))
		if err != nil {
			return err
		}
		if !known[id] {
			s.Skipped += len(files)
			continue
		}
		side := map[string]struct {
			ContentType string `json:"content_type"`
			AttachedAt  string `json:"attached_at"`
		}{}
		for _, f := range files {
			if strings.HasSuffix(f.Name(), sidecarSuffix) {
				var m struct {
					ContentType string `json:"content_type"`
					AttachedAt  string `json:"attached_at"`
				}
				if b, err := os.ReadFile(filepath.Join(root, id, f.Name())); err == nil && json.Unmarshal(b, &m) == nil {
					side[strings.TrimSuffix(f.Name(), sidecarSuffix)] = m
				}
			}
		}
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || strings.HasPrefix(name, ".") || strings.HasSuffix(name, sidecarSuffix) || !f.Type().IsRegular() {
				s.Skipped++
				continue
			}
			p := filepath.Join(root, id, name)
			info, err := os.Stat(p)
			if err != nil {
				return err
			}
			doc := OldDoc{Name: name, Path: p, At: info.ModTime().UTC(), MediaType: mime.TypeByExtension(filepath.Ext(name))}
			if m, ok := side[name]; ok {
				if m.ContentType != "" {
					doc.MediaType = m.ContentType
				}
				if t, err := parseTime(m.AttachedAt); err == nil {
					doc.At = t
				}
			}
			if doc.MediaType == "" {
				doc.MediaType = "application/octet-stream"
			}
			s.Docs[id] = append(s.Docs[id], doc)
		}
		sort.Slice(s.Docs[id], func(i, j int) bool { return s.Docs[id][i].Name < s.Docs[id][j].Name })
	}
	return nil
}
